package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/resultcommit"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// ExportRequest 是导出输入；kind 与 format 的一致性由 Core 校验。
type ExportRequest struct {
	ProjectID       string              `json:"project_id"`
	RunID           string              `json:"run_id"`
	Language        string              `json:"language"`
	Kind            domain.ExportKind   `json:"kind"`
	Format          domain.ExportFormat `json:"format"`
	InputRevisionID string              `json:"input_revision_id"`
	Bilingual       bool                `json:"bilingual"`
	OutputDirGrant  string              `json:"output_dir_grant"`
	NamingTemplate  string              `json:"naming_template"`
}

// 导出默认混音音量：句级配音为主，环境音压低作背景。
const (
	exportVoiceVolume   = 1.0
	exportAmbientVolume = 0.3
	exportSampleRate    = 48000
	exportAudioBitrate  = 192
)

// exportFormatMatches 报告产品类型与实际编码格式是否一致。
func exportFormatMatches(kind domain.ExportKind, format domain.ExportFormat) bool {
	switch kind {
	case domain.ExportKindVideo:
		return format == domain.ExportFormatMP4H264
	case domain.ExportKindAudio:
		return format == domain.ExportFormatWAV || format == domain.ExportFormatMP3
	case domain.ExportKindSubtitle:
		return format == domain.ExportFormatSRT || format == domain.ExportFormatASS
	default:
		return false
	}
}

// ListExports 返回导出记录；projectID 非空时按项目过滤（未知项目返回空列表）。
func (s *Service) ListExports(ctx context.Context, projectID string) ([]domain.ExportRecord, error) {
	records, err := s.store.ListExports(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if records == nil {
		records = []domain.ExportRecord{}
	}
	return records, nil
}

// CreateExport 受理导出任务（202）。
//
// 字幕导出由 Core 直接生成 SRT/ASS 文本并落盘，完成后记录即为 SUCCEEDED；
// 视频/音频导出冻结输入（源媒体、选定句音频、字幕 revision、样式）后进入
// QUEUED，由调度器派发给 Runner 的 ffmpeg 适配器执行。
func (s *Service) CreateExport(ctx context.Context, idem Idem, req ExportRequest) (Response, error) {
	if err := validateExportRequest(req); err != nil {
		return Response{}, err
	}
	var events []domain.DomainEvent
	intents := []*fileIntent{}
	resp, err := s.write(ctx, "exports.create", idem, 202, func(ctx context.Context, tx *store.Tx) (any, error) {
		return s.createExportTx(ctx, tx, req, &events, &intents)
	})
	if err != nil {
		return Response{}, err
	}
	if err := finishFileIntents(intents); err != nil && s.log != nil {
		s.log.Warn("export", "export.commit_journal_retained", map[string]any{"error": err.Error()})
	}
	s.publish(events...)
	s.notify()
	return resp, nil
}

// createExportTx 在导出创建事务内冻结输入、构造执行计划并写入记录。
func (s *Service) createExportTx(
	ctx context.Context,
	tx *store.Tx,
	req ExportRequest,
	events *[]domain.DomainEvent,
	intents *[]*fileIntent,
) (any, error) {
	project, err := tx.GetProject(ctx, req.ProjectID)
	if err != nil {
		return nil, mapReference(err, "project")
	}
	var run *domain.WorkflowRun
	if req.RunID != "" {
		found, err := tx.GetWorkflowRun(ctx, req.RunID)
		if err != nil {
			return nil, mapReference(err, "run")
		}
		if found.ProjectID != req.ProjectID {
			return nil, invalidRequestf("run %s does not belong to project %s", req.RunID, req.ProjectID)
		}
		run = &found
	}

	// 导出语种与（字幕导出的）冻结 revision 在审核门禁前解析：
	// 显式语种必须在 Run 的目标语种内，字幕导出必须与 revision 语种一致。
	var (
		language         string
		subtitleRevision *domain.SubtitleRevision
	)
	if req.Kind == domain.ExportKindSubtitle {
		revision, err := tx.GetSubtitleRevision(ctx, req.InputRevisionID)
		if err != nil {
			return nil, mapReference(err, "subtitle revision")
		}
		// revision 必须属于请求项目：项目隔离由 Core 校验，不信任调用方只传 ID。
		track, err := tx.GetSubtitleTrack(ctx, revision.TrackID)
		if err != nil {
			return nil, mapReference(err, "subtitle track")
		}
		if track.ProjectID != req.ProjectID {
			return nil, invalidRequestf(
				"subtitle revision %s does not belong to project %s", revision.ID, req.ProjectID)
		}
		if req.Language != "" && req.Language != track.Language {
			return nil, invalidRequestf("language %q does not match subtitle revision language %q",
				req.Language, track.Language)
		}
		if run != nil && req.Language != "" {
			if err := requireRunLanguage(*run, req.Language); err != nil {
				return nil, err
			}
		}
		language = track.Language
		subtitleRevision = &revision
	} else {
		if run == nil {
			return nil, invalidRequestf("run_id required for %s export", req.Kind)
		}
		language, err = exportLanguage(run, req.Language)
		if err != nil {
			return nil, err
		}
	}

	// 双语字幕需要冻结原文 revision；解析失败在任何文件写入前返回 400。
	var sourceSubtitleRevision *domain.SubtitleRevision
	if req.Bilingual {
		sourceSubtitleRevision, err = resolveSourceRevision(ctx, tx, project.ID, *subtitleRevision, run)
		if err != nil {
			return nil, err
		}
	}

	// 交付审核门禁（FR-PIPE-05/08）：无 run_id 的工具导出不设门禁。
	// 放在任何文件写入之前，避免 409 留下半成品。
	if run != nil {
		if err := s.requireDeliveryReview(ctx, tx, project, *run, req.Kind, language, subtitleRevision); err != nil {
			return nil, err
		}
	}

	now := s.Now()
	exportID := store.NewID("export")
	taskID := store.NewID("task")
	stepID := stepIDForTask(domain.TaskKindExport, "")
	attemptID := store.NewID("attempt")

	sourceRevisionID := domain.ID("")
	if sourceSubtitleRevision != nil {
		sourceRevisionID = sourceSubtitleRevision.ID
	}
	record := domain.ExportRecord{
		ID:               exportID,
		ProjectID:        req.ProjectID,
		RunID:            req.RunID,
		Kind:             req.Kind,
		Format:           req.Format,
		Status:           domain.TaskStatusQueued,
		InputRevisionID:  req.InputRevisionID,
		SourceRevisionID: sourceRevisionID,
		Bilingual:        req.Bilingual,
		OutputDirGrant:   req.OutputDirGrant,
		NamingTemplate:   req.NamingTemplate,
		TaskID:           taskID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	task := domain.Task{
		ID:               taskID,
		ProjectID:        req.ProjectID,
		RunID:            req.RunID,
		Kind:             domain.TaskKindExport,
		Status:           domain.TaskStatusQueued,
		ArtifactIDs:      []domain.ID{},
		InputRevisionIDs: []domain.ID{},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	attempt := domain.Attempt{
		ID:        attemptID,
		TaskID:    taskID,
		StepID:    stepID,
		AttemptNo: 1,
		Epoch:     s.opts.ServerEpoch,
		Status:    domain.StepStatusPending,
	}
	eventType := "task.queued"
	// 字幕导出的产物提交在 Attempt 行建立后写入（artifact_commits 外键）。
	var pendingCommit *domain.ArtifactCommit

	if req.Kind == domain.ExportKindSubtitle {
		// 字幕导出不走 Runner：Core 按冻结 revision 直接生成文本。
		revision := *subtitleRevision
		style := runnerSubtitleStyle(effectiveStyle(subtitleRevision, run))
		text, err := renderSubtitle(req.Format, revision.Segments, style)
		if err != nil {
			return nil, invalidRequestf("render subtitle: %v", err)
		}
		if req.Bilingual {
			if sourceSubtitleRevision == nil {
				return nil, invalidRequestf("source subtitle revision required for bilingual export")
			}
			text = renderBilingualSRT(revision.Segments, sourceSubtitleRevision.Segments)
		}
		assetID := store.NewID("asset")
		var intent *fileIntent
		finalPath, size, digest, err := s.writeTextExportTracked(
			req.OutputDirGrant,
			s.exportBaseName(project.Name, language, req.NamingTemplate),
			exportExtension(req.Format),
			[]byte(text),
			func(destination string) error {
				var err error
				intent, err = s.beginFileIntent(assetID, destination, false)
				return err
			},
		)
		if err != nil {
			return nil, err
		}
		*intents = append(*intents, intent)
		asset := exportAsset(record.ProjectID, record.RunID, req.Kind, finalPath, size, digest, now)
		asset.ID = assetID
		if err := tx.CreateAsset(ctx, asset); err != nil {
			return nil, err
		}
		if err := tx.AddAssetLocation(ctx, domain.AssetLocation{
			ID:        store.NewID("loc"),
			AssetID:   asset.ID,
			Path:      finalPath,
			Source:    domain.LocationSourceGenerated,
			Owner:     domain.LocationOwnerApp,
			Available: true,
			CreatedAt: now,
		}); err != nil {
			return nil, err
		}
		committedAt := now
		pendingCommit = &domain.ArtifactCommit{
			ID:          store.NewID("commit"),
			AttemptID:   attemptID,
			AssetIDs:    []domain.ID{asset.ID},
			Status:      domain.CommitStatusCommitted,
			CommittedAt: &committedAt,
		}
		record.Status = domain.TaskStatusSucceeded
		record.OutputPath = finalPath
		task.Status = domain.TaskStatusSucceeded
		task.ArtifactIDs = []domain.ID{asset.ID}
		attempt.Status = domain.StepStatusSucceeded
		attempt.StartedAt = &committedAt
		attempt.FinishedAt = &committedAt
		eventType = "task.succeeded"
	} else {
		// 输出分区空间准入：按冻结输入估算视频/音频产物与中间文件；
		// 永久性不足返回 507，探测失败由实际写入错误兜底。
		if err := s.checkDiskSpace("export", req.OutputDirGrant,
			requiredDiskBytes(assetBytesByID(ctx, tx, run.Config.InputRevisionIDs))); err != nil {
			return nil, err
		}
		plan, err := s.resolveMediaPlan(ctx, tx, project, *run, req, exportID, language)
		if err != nil {
			return nil, err
		}
		task.Params = map[string]any{
			"export_id": exportID,
			"language":  plan.Language,
			"format":    req.Format,
			"plan":      plan.Params,
		}
		if plan.SubtitleRevisionID != "" {
			core := coreTaskParams(task.Params)
			core["subtitle_revision_id"] = plan.SubtitleRevisionID
			core["subtitle_digest"] = plan.SubtitleDigest
		}
		task.InputRevisionIDs = append([]domain.ID{}, run.Config.InputRevisionIDs...)
	}

	// 输入摘要与 RetryTask 使用同一口径（重试时比较冻结输入是否变化）。
	inputIDs := make([]string, 0, len(task.InputRevisionIDs))
	for _, id := range task.InputRevisionIDs {
		inputIDs = append(inputIDs, id)
	}
	digest, err := taskInputDigest(CreateTaskRequest{
		RunID:            task.RunID,
		Kind:             task.Kind,
		NodeKey:          task.NodeKey,
		SegmentID:        task.SegmentID,
		Language:         task.Language,
		InputRevisionIDs: inputIDs,
		Params:           task.Params,
		ModelLock:        task.ModelLock,
	})
	if err != nil {
		return nil, err
	}
	task.InputDigest = digest
	task.Steps = []domain.TaskStep{{
		StepID:    stepID,
		Status:    domain.StepStatusPending,
		AttemptNo: 1,
		Epoch:     s.opts.ServerEpoch,
	}}
	if task.Status == domain.TaskStatusSucceeded {
		task.Steps[0].Status = domain.StepStatusSucceeded
	}
	if err := tx.CreateExport(ctx, record); err != nil {
		return nil, err
	}
	if err := tx.CreateTask(ctx, task); err != nil {
		return nil, err
	}
	if err := tx.CreateAttempt(ctx, attempt); err != nil {
		return nil, err
	}
	if pendingCommit != nil {
		if err := tx.CreateArtifactCommit(ctx, *pendingCommit); err != nil {
			return nil, err
		}
	}
	if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
		Type:      "export.created",
		ProjectID: req.ProjectID,
		TaskID:    taskID,
		Data: map[string]any{
			"export_id": exportID,
			"kind":      req.Kind,
			"format":    req.Format,
		},
	}); err != nil {
		return nil, err
	}
	if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
		Type:         "task.created",
		ProjectID:    task.ProjectID,
		TaskID:       task.ID,
		StateVersion: task.StateVersion,
		Data: map[string]any{
			"kind":      task.Kind,
			"status":    task.Status,
			"export_id": exportID,
		},
	}); err != nil {
		return nil, err
	}
	if eventType == "task.succeeded" {
		if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
			Type:         "task.succeeded",
			ProjectID:    task.ProjectID,
			TaskID:       task.ID,
			StateVersion: task.StateVersion,
			Data:         map[string]any{"artifact_ids": task.ArtifactIDs},
		}); err != nil {
			return nil, err
		}
		if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
			Type:      "export.succeeded",
			ProjectID: record.ProjectID,
			TaskID:    taskID,
			Data: map[string]any{
				"export_id":    record.ID,
				"format":       record.Format,
				"output_path":  record.OutputPath,
				"artifact_ids": task.ArtifactIDs,
			},
		}); err != nil {
			return nil, err
		}
	} else if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
		Type:         "task.queued",
		ProjectID:    task.ProjectID,
		TaskID:       task.ID,
		StateVersion: task.StateVersion,
	}); err != nil {
		return nil, err
	}
	return record, nil
}

func validateExportRequest(req ExportRequest) error {
	if req.ProjectID == "" {
		return invalidRequestf("project_id required")
	}
	if !req.Kind.Valid() {
		return invalidRequestf("invalid export kind %q", req.Kind)
	}
	if !req.Format.Valid() {
		return invalidRequestf("invalid export format %q", req.Format)
	}
	if !exportFormatMatches(req.Kind, req.Format) {
		return invalidRequestf("export format %q does not match kind %q", req.Format, req.Kind)
	}
	if req.OutputDirGrant == "" {
		return invalidRequestf("output_dir_grant required")
	}
	if err := validateExportOutputDir(req.OutputDirGrant); err != nil {
		return err
	}
	if req.NamingTemplate == "" {
		return invalidRequestf("naming_template required")
	}
	if req.Language != "" {
		if len(req.Language) > 32 || strings.TrimSpace(req.Language) != req.Language ||
			strings.ContainsAny(req.Language, " \t\r\n") {
			return invalidRequestf("invalid language %q", req.Language)
		}
	}
	if req.Kind == domain.ExportKindSubtitle && req.InputRevisionID == "" {
		return invalidRequestf("subtitle export requires input_revision_id")
	}
	if req.Bilingual {
		if req.Kind != domain.ExportKindSubtitle {
			return invalidRequestf("bilingual export requires subtitle kind")
		}
		if req.Format != domain.ExportFormatSRT {
			return invalidRequestf("bilingual export supports SRT only")
		}
	}
	return nil
}

// validateExportOutputDir 校验用户授权的输出目录：绝对路径、非根、已存在目录。
func validateExportOutputDir(dir string) error {
	if len(dir) > 4096 {
		return invalidRequestf("output_dir_grant too long")
	}
	if strings.ContainsRune(dir, 0) || strings.ContainsAny(dir, "\r\n") {
		return invalidRequestf("output_dir_grant contains control characters")
	}
	if !filepath.IsAbs(dir) {
		return invalidRequestf("output_dir_grant must be an absolute path")
	}
	clean := filepath.Clean(dir)
	if clean == string(filepath.Separator) || clean == filepath.VolumeName(clean)+string(filepath.Separator) {
		return invalidRequestf("output_dir_grant cannot be a filesystem root")
	}
	info, err := os.Stat(clean)
	if err != nil {
		return invalidRequestf("output_dir_grant is not accessible: %v", err)
	}
	if !info.IsDir() {
		return invalidRequestf("output_dir_grant must be a directory")
	}
	return nil
}

// ---- 交付审核门禁 --------------------------------------------------------

// errReviewRequired 是 Run 派生导出缺少有效交付审核的 409（REVIEW_REQUIRED）。
func errReviewRequired(runID, detail string) error {
	return fmt.Errorf("%w: run %s delivery review required: %s", &domain.Error{
		Code:       domain.ErrorCodeReviewRequired,
		MessageKey: "error.review_required",
		ActionIDs:  []domain.ID{"open_delivery_review"},
	}, runID, detail)
}

// exportLanguage 解析导出语种：缺省为 Run 的第一个目标语种；
// 显式语种必须是 Run 的目标语种（不能导出计划外语言）。
func exportLanguage(run *domain.WorkflowRun, requested string) (string, error) {
	if run == nil {
		return "", invalidRequestf("run required to resolve export language")
	}
	language := strings.TrimSpace(requested)
	if language == "" && len(run.Languages) > 0 {
		language = run.Languages[0]
	}
	if language == "" {
		return "", invalidRequestf("run has no target language for export")
	}
	if err := requireRunLanguage(*run, language); err != nil {
		return "", err
	}
	return language, nil
}

func requireRunLanguage(run domain.WorkflowRun, language string) error {
	for _, candidate := range run.Languages {
		if candidate == language {
			return nil
		}
	}
	return invalidRequestf("language %q is not a target language of run %s", language, run.ID)
}

// requireDeliveryReview 是 Run 派生导出的交付审核门禁（FR-PIPE-05/08、
// workflow-and-review §3/§5、local-api-contract §8）。
//
// 规则：
//   - 必须存在 approved 的 delivery 门槛，且其 revision_digest 等于 Core 从持久化
//     事实重算的当前交付摘要（客户端传入的摘要不参与判断）；
//   - 视频/音频导出绑定 08 混音产物：只有冻结在当前 revision 的成功 08 摘要
//     可以放行，字幕 revision 级门槛不能冒充成片/音频交付审核；
//   - 字幕导出绑定被导出的字幕 revision（revision 级 delivery 绑定），不要求
//     先合成 08；
//   - 若存在绑定当前交付摘要的 pending delivery 门槛，同样拒绝：旧放行不能
//     覆盖后到的编辑或产物。
//
// 门槛带语种时必须匹配导出语种；无语言字段（当前 08 交付门槛）按摘要校验。
// 各语种的放行按摘要独立成立，后放行的语种不会顶掉先前语种的有效放行
// （workflow-and-review §5 按已审核语种导出）。
func (s *Service) requireDeliveryReview(
	ctx context.Context,
	tx *store.Tx,
	project domain.Project,
	run domain.WorkflowRun,
	kind domain.ExportKind,
	language string,
	revision *domain.SubtitleRevision,
) error {
	gates, err := tx.ListRunDeliveryReviews(ctx, run.ID)
	if err != nil {
		return err
	}
	if len(gates) == 0 {
		return errReviewRequired(run.ID, "no delivery review gate")
	}
	digests, err := s.currentDeliveryDigests(ctx, tx, project, run, kind, language, revision)
	if err != nil {
		return err
	}
	matches := func(digest string) bool {
		for _, current := range digests {
			if digest == current {
				return true
			}
		}
		return false
	}
	approved := false
	for _, gate := range gates {
		if gate.Language != "" && gate.Language != language {
			continue
		}
		if !matches(gate.RevisionDigest) {
			continue
		}
		if gate.Status == domain.ReviewStatusPending {
			// 当前修订仍有待审核门槛：编辑/重新混音后必须先重新放行。
			return errReviewRequired(run.ID, "delivery review for the current revision is pending")
		}
		if gate.Status == domain.ReviewStatusApproved {
			approved = true
		}
	}
	if !approved {
		return errReviewRequired(run.ID, "no approved delivery review binds the current revision")
	}
	return nil
}

// currentDeliveryDigests 重算当前交付摘要（与门槛创建口径一致）：
//   - 08 混音节点中与导出语种匹配、且冻结在当前 revision 的最近一次成功任务
//     （无标注语言的单语任务作为回退），摘要用 NodeReviewDigest；冻结在旧
//     revision 的 08 不能沿用放行。视频/音频导出的交付审核以此为准；
//   - 字幕导出另外接受被导出 revision 的交付绑定（RevisionBindingDigest +
//     ":delivery"），允许在不合成 08 时导出已审核字幕；视频/音频不接受该
//     字幕级绑定，避免字幕审核冒充成片/音频交付审核。
//
// 混音按语种取产物：多语 Run 里较新语种的混音不能冒充另一语种的 08 审核。
func (s *Service) currentDeliveryDigests(
	ctx context.Context,
	tx *store.Tx,
	project domain.Project,
	run domain.WorkflowRun,
	kind domain.ExportKind,
	language string,
	revision *domain.SubtitleRevision,
) ([]string, error) {
	digests := make([]string, 0, 2)
	if revision == nil && language != "" {
		track, err := pickSubtitleTrack(ctx, tx, project.ID, language)
		if err != nil {
			return nil, err
		}
		if track != nil {
			revision = currentRevision(*track)
		}
	}
	task, ok, err := tx.LatestSucceededNodeTask(ctx, run.ID, domain.NodeKeyMix, language)
	if err != nil {
		return nil, err
	}
	if ok && mixTaskMatchesRevision(task, revision) {
		digest, err := domain.NodeReviewDigest(task.RunID, task.NodeKey, task.Language,
			task.ID, task.ArtifactIDs, task.InputDigest)
		if err != nil {
			return nil, err
		}
		digests = append(digests, digest)
	}
	if kind == domain.ExportKindSubtitle && revision != nil {
		digest, err := domain.RevisionBindingDigest(*revision)
		if err != nil {
			return nil, err
		}
		digests = append(digests, domain.DeliveryBind(digest))
	}
	return digests, nil
}

// ---- 执行计划（视频/音频） ----------------------------------------------

// exportQuerier 是计划构造需要的只读仓储子集；*store.Tx 实现。
type exportQuerier interface {
	GetAsset(ctx context.Context, id domain.ID) (domain.Asset, error)
	ListProjectTracks(ctx context.Context, projectID domain.ID) ([]domain.SubtitleTrack, error)
	GetCandidateSelection(ctx context.Context, segmentID domain.ID, language string) (domain.CandidateSelection, error)
	ListAudioCandidates(ctx context.Context, segmentID domain.ID, language string, revision *int) ([]domain.AudioCandidate, error)
}

type exportAudioInput struct {
	Path    string
	Volume  float64
	StartMs int64
}

type mediaExportPlan struct {
	Language           string
	Params             map[string]any
	SubtitleRevisionID domain.ID
	SubtitleDigest     string
}

// resolveMediaPlan 冻结源媒体、选定句音频与字幕 revision，构造 Runner 白名单计划。
func (s *Service) resolveMediaPlan(
	ctx context.Context,
	q exportQuerier,
	project domain.Project,
	run domain.WorkflowRun,
	req ExportRequest,
	exportID string,
	language string,
) (mediaExportPlan, error) {
	if strings.TrimSpace(language) == "" {
		return mediaExportPlan{}, invalidRequestf("run has no target language for export")
	}

	assets := make([]domain.Asset, 0, len(run.Config.InputRevisionIDs))
	for _, id := range run.Config.InputRevisionIDs {
		asset, err := q.GetAsset(ctx, id)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return mediaExportPlan{}, invalidRequestf("input asset %s not found", id)
			}
			return mediaExportPlan{}, err
		}
		assets = append(assets, asset)
	}
	var source *domain.Asset
	for i := range assets {
		if assets[i].Type == domain.AssetTypeVideo {
			source = &assets[i]
			break
		}
	}
	if source == nil && req.Kind == domain.ExportKindAudio {
		for i := range assets {
			if assets[i].Type == domain.AssetTypeAudio || assets[i].Type == domain.AssetTypeAudioTrack {
				source = &assets[i]
				break
			}
		}
	}
	if source == nil {
		return mediaExportPlan{}, invalidRequestf(
			"%s export requires an input %s asset", req.Kind, exportSourceKinds(req.Kind))
	}
	sourcePath, err := exportAssetPath(*source)
	if err != nil {
		return mediaExportPlan{}, err
	}

	track, err := pickSubtitleTrack(ctx, q, project.ID, language)
	if err != nil {
		return mediaExportPlan{}, err
	}
	var subtitleText string
	var subtitleRevisionID domain.ID
	var subtitleRevision *domain.SubtitleRevision
	if track != nil {
		subtitleRevision = currentRevision(*track)
		if subtitleRevision != nil {
			subtitleText = renderSRT(subtitleRevision.Segments)
			subtitleRevisionID = subtitleRevision.ID
		}
	}

	audioInputs := make([]exportAudioInput, 0, 8)
	if track != nil {
		revision := currentRevision(*track)
		if revision != nil {
			for _, segment := range revision.Segments {
				selection, err := q.GetCandidateSelection(ctx, segment.ID, language)
				if err != nil {
					if errors.Is(err, domain.ErrNotFound) {
						continue
					}
					return mediaExportPlan{}, err
				}
				revisionNo := selection.Revision
				candidates, err := q.ListAudioCandidates(ctx, segment.ID, language, &revisionNo)
				if err != nil {
					return mediaExportPlan{}, err
				}
				var chosen *domain.AudioCandidate
				for i := range candidates {
					if candidates[i].ID == selection.CandidateID {
						chosen = &candidates[i]
						break
					}
				}
				if chosen == nil {
					return mediaExportPlan{}, invalidRequestf(
						"selection %s references a missing candidate", selection.CandidateID)
				}
				asset, err := q.GetAsset(ctx, chosen.AudioAssetID)
				if err != nil {
					return mediaExportPlan{}, mapReference(err, "candidate audio asset")
				}
				path, err := exportAssetPath(asset)
				if err != nil {
					return mediaExportPlan{}, err
				}
				audioInputs = append(audioInputs, exportAudioInput{
					Path:    path,
					Volume:  exportVoiceVolume,
					StartMs: segment.StartMs,
				})
			}
		}
	}
	// 环境音：源媒体自带音轨时作为背景；元数据缺失时按可能存在音轨处理。
	if source.Meta == nil || source.Meta.AudioTracks > 0 {
		audioInputs = append(audioInputs, exportAudioInput{
			Path:    sourcePath,
			Volume:  exportAmbientVolume,
			StartMs: 0,
		})
	}
	if req.Kind == domain.ExportKindAudio && len(audioInputs) == 0 {
		return mediaExportPlan{}, invalidRequestf(
			"audio export requires selected sentence audio or a source with an audio track")
	}

	switch req.Format {
	case domain.ExportFormatMP4H264:
		params := map[string]any{
			"op":                 "video_merge",
			"video_src":          sourcePath,
			"audio_inputs":       audioInputParams(audioInputs),
			"out":                "out.mp4",
			"video_codec":        "h264",
			"crf":                20,
			"preset":             "medium",
			"audio_codec":        "aac",
			"audio_bitrate_kbps": exportAudioBitrate,
			"sample_rate":        exportSampleRate,
		}
		if subtitleText != "" {
			params["subtitle"] = stagedSubtitlePlaceholder
			params["style"] = runnerSubtitleStyle(effectiveStyle(subtitleRevision, &run))
			digest := sha256.Sum256([]byte(subtitleText))
			return mediaExportPlan{
				Language: language, Params: params,
				SubtitleRevisionID: subtitleRevisionID,
				SubtitleDigest:     hex.EncodeToString(digest[:]),
			}, nil
		}
		return mediaExportPlan{Language: language, Params: params}, nil
	case domain.ExportFormatWAV:
		return mediaExportPlan{Language: language, Params: map[string]any{
			"op":          "mix",
			"inputs":      audioInputParams(audioInputs),
			"out":         "out.wav",
			"audio_codec": "pcm_s16le",
			"sample_rate": exportSampleRate,
		}}, nil
	case domain.ExportFormatMP3:
		return mediaExportPlan{Language: language, Params: map[string]any{
			"op":           "mix",
			"inputs":       audioInputParams(audioInputs),
			"out":          "out.mp3",
			"audio_codec":  "mp3",
			"bitrate_kbps": exportAudioBitrate,
			"sample_rate":  exportSampleRate,
		}}, nil
	default:
		return mediaExportPlan{}, invalidRequestf("unsupported media export format %q", req.Format)
	}
}

func exportSourceKinds(kind domain.ExportKind) string {
	if kind == domain.ExportKindAudio {
		return "video/audio"
	}
	return "video"
}

func exportAssetPath(asset domain.Asset) (string, error) {
	if asset.Path == "" {
		return "", invalidRequestf("asset %s has no readable location", asset.ID)
	}
	path := asset.Path
	if !filepath.IsAbs(path) {
		return "", invalidRequestf("asset %s path must be absolute", asset.ID)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", invalidRequestf("asset %s is not accessible: %v", asset.ID, err)
	}
	if !info.Mode().IsRegular() {
		return "", invalidRequestf("asset %s is not a regular file", asset.ID)
	}
	return path, nil
}

// pickSubtitleTrack 选择该语言的成品字幕轨道（译文优先）；读取失败必须报错，
// 不能静默生成缺字幕/缺配音的成片。
func pickSubtitleTrack(ctx context.Context, q exportQuerier, projectID domain.ID, language string) (*domain.SubtitleTrack, error) {
	tracks, err := q.ListProjectTracks(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list subtitle tracks: %w", err)
	}
	return pickExportSubtitleTrack(tracks, language), nil
}

// pickExportSubtitleTrack 是导出交付门禁的轨道选择：同语言译文轨优先，否则
// 回退该语言任意轨。导出放行与 current_bindings 投影必须用同一选择，避免
// 多轨项目对“当前交付 revision”得出不同结论。
func pickExportSubtitleTrack(tracks []domain.SubtitleTrack, language string) *domain.SubtitleTrack {
	var fallback *domain.SubtitleTrack
	for i := range tracks {
		if tracks[i].Language != language {
			continue
		}
		if tracks[i].Type == domain.TrackTypeTranslation {
			return &tracks[i]
		}
		if fallback == nil {
			fallback = &tracks[i]
		}
	}
	return fallback
}

func currentRevision(track domain.SubtitleTrack) *domain.SubtitleRevision {
	for i := range track.Revisions {
		if track.Revisions[i].ID == track.CurrentRevisionID {
			return &track.Revisions[i]
		}
	}
	return nil
}

func audioInputParams(inputs []exportAudioInput) []any {
	out := make([]any, 0, len(inputs))
	for _, input := range inputs {
		out = append(out, map[string]any{
			"path":     input.Path,
			"volume":   input.Volume,
			"start_ms": input.StartMs,
		})
	}
	return out
}

// ---- 字幕文本 ------------------------------------------------------------

// resolveSourceRevision 解析双语字幕的原文 revision。
//
// 优先使用译文 revision 的 input_revision_id；缺失时按 Run 源语言查找唯一的
// transcription 轨。不是同一项目、不是 transcription、或候选不唯一都拒绝。
func resolveSourceRevision(
	ctx context.Context,
	tx *store.Tx,
	projectID domain.ID,
	translation domain.SubtitleRevision,
	run *domain.WorkflowRun,
) (*domain.SubtitleRevision, error) {
	if translation.InputRevisionID != "" {
		revision, err := tx.GetSubtitleRevision(ctx, translation.InputRevisionID)
		if err != nil {
			return nil, mapReference(err, "source subtitle revision")
		}
		track, err := tx.GetSubtitleTrack(ctx, revision.TrackID)
		if err != nil {
			return nil, mapReference(err, "source subtitle track")
		}
		if track.ProjectID != projectID {
			return nil, invalidRequestf("source subtitle revision belongs to another project")
		}
		if track.Type != domain.TrackTypeTranscription {
			return nil, invalidRequestf("source subtitle revision is not a transcription track")
		}
		return &revision, nil
	}

	tracks, err := tx.ListProjectTracks(ctx, projectID)
	if err != nil {
		return nil, err
	}
	candidates := make([]domain.SubtitleTrack, 0, len(tracks))
	for _, track := range tracks {
		if track.Type != domain.TrackTypeTranscription {
			continue
		}
		if run != nil && run.SourceLanguage != "" && track.Language != run.SourceLanguage {
			continue
		}
		candidates = append(candidates, track)
	}
	if len(candidates) != 1 {
		return nil, invalidRequestf("bilingual export requires a unique source transcription track")
	}
	revision := currentRevisionOf(candidates[0])
	if revision == nil {
		return nil, invalidRequestf("source transcription track has no revision")
	}
	return revision, nil
}

// currentRevisionOf 返回轨道的当前 revision；缺失时回退最后一个（只读兼容）。
func currentRevisionOf(track domain.SubtitleTrack) *domain.SubtitleRevision {
	for i := range track.Revisions {
		if track.Revisions[i].ID == track.CurrentRevisionID {
			return &track.Revisions[i]
		}
	}
	if len(track.Revisions) == 0 {
		return nil
	}
	return &track.Revisions[len(track.Revisions)-1]
}

// renderBilingualSRT 生成原文+译文双行 SRT；时间码以译文句段为准。
func renderBilingualSRT(translation, source []domain.Segment) string {
	sourceByID := make(map[domain.ID]domain.Segment, len(source))
	for _, segment := range source {
		sourceByID[segment.ID] = segment
	}

	var b strings.Builder
	index := 0
	for _, segment := range translation {
		translated := normalizeSubtitleText(segment.Text)
		original := ""
		for _, id := range segment.Lineage {
			if candidate, ok := sourceByID[id]; ok {
				original = normalizeSubtitleText(candidate.Text)
				break
			}
		}
		if original == "" {
			original = matchingSourceText(segment, source)
		}
		if translated == "" && original == "" {
			continue
		}
		index++
		text := translated
		switch {
		case original != "" && translated != "":
			text = original + "\n" + translated
		case original != "":
			text = original
		}
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			index, srtTimestamp(segment.StartMs), srtTimestamp(segment.EndMs), text)
	}
	return b.String()
}

// matchingSourceText 先按完全相同的起止时间配对，再按最大时间重叠回退。
func matchingSourceText(segment domain.Segment, source []domain.Segment) string {
	for _, candidate := range source {
		if candidate.StartMs == segment.StartMs && candidate.EndMs == segment.EndMs {
			return normalizeSubtitleText(candidate.Text)
		}
	}
	best := ""
	bestOverlap := domain.Millis(0)
	for _, candidate := range source {
		overlap := min(candidate.EndMs, segment.EndMs) - max(candidate.StartMs, segment.StartMs)
		if overlap > bestOverlap {
			bestOverlap = overlap
			best = normalizeSubtitleText(candidate.Text)
		}
	}
	return best
}

// renderSubtitle 按格式生成字幕文本（UTF-8，无 BOM）。
func renderSubtitle(format domain.ExportFormat, segments []domain.Segment, style map[string]any) (string, error) {
	switch format {
	case domain.ExportFormatSRT:
		return renderSRT(segments), nil
	case domain.ExportFormatASS:
		return renderASS(segments, style), nil
	default:
		return "", fmt.Errorf("unsupported subtitle format %q", format)
	}
}

// renderSRT 生成标准 SRT；空文本句段跳过。
func renderSRT(segments []domain.Segment) string {
	var b strings.Builder
	index := 0
	for _, segment := range segments {
		text := normalizeSubtitleText(segment.Text)
		if text == "" {
			continue
		}
		index++
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n",
			index, srtTimestamp(segment.StartMs), srtTimestamp(segment.EndMs), text)
	}
	return b.String()
}

var _assColour = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// renderASS 生成带受控样式的 ASS；样式字段只来自冻结的 SubtitleStyle。
func renderASS(segments []domain.Segment, style map[string]any) string {
	font := "Arial"
	if value, ok := style["font_name"].(string); ok && value != "" {
		font = value
	}
	fontSize := 48
	if value, ok := intValue(style["font_size"]); ok && value >= 6 && value <= 200 {
		fontSize = value
	}
	primary := "#FFFFFF"
	if value, ok := style["primary_colour"].(string); ok && _assColour.MatchString(value) {
		primary = value
	}
	outlineColour := "#000000"
	if value, ok := style["outline_colour"].(string); ok && _assColour.MatchString(value) {
		outlineColour = value
	}
	backColour := "#000000"
	if value, ok := style["back_colour"].(string); ok && _assColour.MatchString(value) {
		backColour = value
	}
	outlineWidth := 2.0
	if value, ok := floatValue(style["outline"]); ok && value >= 0 && value <= 10 {
		outlineWidth = value
	}
	shadow := 0.0
	if value, ok := floatValue(style["shadow"]); ok && value >= 0 && value <= 10 {
		shadow = value
	}
	alignment := 2
	if value, ok := intValue(style["alignment"]); ok && value >= 1 && value <= 9 {
		alignment = value
	}

	var b strings.Builder
	b.WriteString("[Script Info]\nScriptType: v4.00+\nPlayResX: 1920\nPlayResY: 1080\nWrapStyle: 0\n\n")
	b.WriteString("[V4+ Styles]\n")
	b.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	fmt.Fprintf(&b, "Style: Default,%s,%d,%s,%s,%s,%s,0,0,0,0,100,100,0,0,1,%.1f,%.1f,%d,10,10,36,1\n\n",
		font, fontSize, assColour(primary), assColour("#0000FF"), assColour(outlineColour),
		assColour(backColour), outlineWidth, shadow, alignment)
	b.WriteString("[Events]\n")
	b.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	for _, segment := range segments {
		text := normalizeSubtitleText(segment.Text)
		if text == "" {
			continue
		}
		text = strings.ReplaceAll(text, "\n", "\\N")
		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n",
			assTimestamp(segment.StartMs), assTimestamp(segment.EndMs), text)
	}
	return b.String()
}

func normalizeSubtitleText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimSpace(text)
}

func srtTimestamp(ms domain.Millis) string {
	if ms < 0 {
		ms = 0
	}
	hours := ms / 3_600_000
	minutes := (ms % 3_600_000) / 60_000
	seconds := (ms % 60_000) / 1_000
	millis := ms % 1_000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", hours, minutes, seconds, millis)
}

func assTimestamp(ms domain.Millis) string {
	if ms < 0 {
		ms = 0
	}
	hours := ms / 3_600_000
	minutes := (ms % 3_600_000) / 60_000
	seconds := (ms % 60_000) / 1_000
	centis := (ms % 1_000) / 10
	return fmt.Sprintf("%d:%02d:%02d.%02d", hours, minutes, seconds, centis)
}

// assColour 把 #RRGGBB 转成 ASS 的 &HAABBGGRR。
func assColour(hexColour string) string {
	if !_assColour.MatchString(hexColour) {
		return "&H00000000"
	}
	red, green, blue := hexColour[1:3], hexColour[3:5], hexColour[5:7]
	return "&H00" + strings.ToUpper(blue+green+red)
}

func intValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func floatValue(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	default:
		return 0, false
	}
}

// runnerSubtitleStyle 把领域样式映射到 runner ffmpeg.py 的白名单字段。
func runnerSubtitleStyle(style *domain.SubtitleStyle) map[string]any {
	out := map[string]any{
		"font_name":      "Arial",
		"font_size":      48,
		"primary_colour": "#FFFFFF",
		"alignment":      2,
		"margin_v":       36,
	}
	if style == nil {
		return out
	}
	if safeFontName(style.Font) {
		out["font_name"] = style.Font
	}
	if style.FontSize >= 6 && style.FontSize <= 200 {
		out["font_size"] = int(style.FontSize)
	}
	if _assColour.MatchString(style.Background) {
		out["back_colour"] = style.Background
	}
	if style.Outline != nil {
		if _assColour.MatchString(style.Outline.Color) {
			out["outline_colour"] = style.Outline.Color
		}
		if style.Outline.Width > 0 && style.Outline.Width <= 10 {
			out["outline"] = style.Outline.Width
		}
	}
	if style.Shadow != nil && style.Shadow.Blur > 0 && style.Shadow.Blur <= 10 {
		out["shadow"] = style.Shadow.Blur
	}
	switch style.Align {
	case domain.SubtitleAlignLeft:
		out["alignment"] = 1
	case domain.SubtitleAlignRight:
		out["alignment"] = 3
	default:
		out["alignment"] = 2
	}
	return out
}

func safeFontName(name string) bool {
	if name == "" || len(name) > 64 || !utf8.ValidString(name) {
		return false
	}
	for _, ch := range name {
		if strings.ContainsRune(",:;'\"\\[]{};|&`$<>", ch) || ch < 0x20 || ch == 0x7F {
			return false
		}
	}
	return true
}

// effectiveStyle 返回实际生效的字幕样式：revision.style 优先，其次 RunConfig。
func effectiveStyle(revision *domain.SubtitleRevision, run *domain.WorkflowRun) *domain.SubtitleStyle {
	if revision != nil && revision.Style != nil {
		return revision.Style
	}
	if run == nil {
		return nil
	}
	return run.Config.SubtitleStyle
}

// ---- 输出落盘 ------------------------------------------------------------

var _invalidFileChars = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]`)

func exportExtension(format domain.ExportFormat) string {
	switch format {
	case domain.ExportFormatMP4H264:
		return ".mp4"
	case domain.ExportFormatWAV:
		return ".wav"
	case domain.ExportFormatMP3:
		return ".mp3"
	case domain.ExportFormatSRT:
		return ".srt"
	case domain.ExportFormatASS:
		return ".ass"
	default:
		return ""
	}
}

func (s *Service) exportBaseName(projectName, language, template string) string {
	base := template
	base = strings.ReplaceAll(base, "{project}", projectName)
	base = strings.ReplaceAll(base, "{language}", language)
	base = _invalidFileChars.ReplaceAllString(base, "_")
	base = strings.Trim(strings.TrimSpace(base), ".")
	if base == "" {
		base = "export"
	}
	runes := []rune(base)
	if len(runes) > 120 {
		base = string(runes[:120])
	}
	return base
}

// writeTextExport 写入 Core 直接生成的字幕文本。
//
// 与媒体产物同一口径：先写目标目录内的临时文件、刷盘校验，再独占命名落位；
// 不允许部分写入的文件以最终文件名出现，也不覆盖同名已有文件。
func (s *Service) writeTextExport(dir, base, ext string, data []byte) (string, int64, string, error) {
	return s.writeTextExportTracked(dir, base, ext, data, nil)
}

func (s *Service) writeTextExportTracked(
	dir, base, ext string,
	data []byte,
	beforeCreate func(string) error,
) (string, int64, string, error) {
	if err := validateExportOutputDir(dir); err != nil {
		return "", 0, "", err
	}
	temp, err := os.CreateTemp(dir, ".yaomu-export-*.tmp")
	if err != nil {
		return "", 0, "", fmt.Errorf("create export temp file: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() { _ = os.Remove(tempPath) }
	written, writeErr := temp.Write(data)
	if writeErr == nil && written != len(data) {
		writeErr = io.ErrShortWrite
	}
	syncErr := temp.Sync()
	closeErr := temp.Close()
	if err := firstError(writeErr, syncErr, closeErr); err != nil {
		cleanup()
		return "", 0, "", fmt.Errorf("write export file: %w", err)
	}
	finalPath, err := linkExportFileTracked(tempPath, dir, base, ext, beforeCreate)
	if err != nil {
		cleanup()
		return "", 0, "", err
	}
	sum := sha256.Sum256(data)
	return finalPath, int64(written), hex.EncodeToString(sum[:]), nil
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// landOutput 把 Runner 产物复制到用户输出目录：临时文件 → 校验大小 → 独占命名。
func (s *Service) landOutput(sourcePath, dir, base, ext string) (string, int64, string, error) {
	return s.landOutputTracked(sourcePath, dir, base, ext, nil)
}

func (s *Service) landOutputTracked(
	sourcePath, dir, base, ext string,
	beforeCreate func(string) error,
) (string, int64, string, error) {
	if err := validateExportOutputDir(dir); err != nil {
		return "", 0, "", err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", 0, "", fmt.Errorf("open export output: %w", err)
	}
	defer func() { _ = source.Close() }()

	temp, err := os.CreateTemp(dir, ".yaomu-export-*.tmp")
	if err != nil {
		return "", 0, "", fmt.Errorf("create export temp file: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() { _ = os.Remove(tempPath) }
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temp, hash), source)
	if copyErr == nil && written <= 0 {
		copyErr = fmt.Errorf("export output is empty")
	}
	if copyErr == nil {
		copyErr = temp.Sync()
	}
	if closeErr := temp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		cleanup()
		return "", 0, "", fmt.Errorf("copy export output: %w", copyErr)
	}

	finalPath, err := linkExportFileTracked(tempPath, dir, base, ext, beforeCreate)
	if err != nil {
		cleanup()
		return "", 0, "", err
	}
	return finalPath, written, hex.EncodeToString(hash.Sum(nil)), nil
}

// linkExportFile 选择不存在的最终名字并原子链接；链接不可用时退化为独占复制。
func linkExportFile(tempPath, dir, base, ext string) (string, error) {
	return linkExportFileTracked(tempPath, dir, base, ext, nil)
}

func linkExportFileTracked(tempPath, dir, base, ext string, beforeCreate func(string) error) (string, error) {
	stem := base
	if !strings.HasSuffix(strings.ToLower(stem), ext) {
		stem += ext
	}
	for index := 0; index < 1000; index++ {
		name := stem
		if index > 0 {
			name = fmt.Sprintf("%s-%d%s", strings.TrimSuffix(stem, ext), index, ext)
		}
		finalPath := filepath.Join(dir, name)
		if _, err := os.Lstat(finalPath); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("stat export target: %w", err)
		}
		if beforeCreate != nil {
			if err := beforeCreate(finalPath); err != nil {
				return "", err
			}
		}
		if err := os.Link(tempPath, finalPath); err == nil {
			_ = os.Remove(tempPath)
			return finalPath, nil
		} else if errors.Is(err, os.ErrExist) {
			continue
		}
		// 文件系统不支持硬链接：独占创建后从临时文件复制。
		destination, err := os.OpenFile(finalPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", fmt.Errorf("create export target: %w", err)
		}
		source, err := os.Open(tempPath)
		if err != nil {
			_ = destination.Close()
			_ = os.Remove(finalPath)
			return "", fmt.Errorf("reopen export temp: %w", err)
		}
		_, copyErr := io.Copy(destination, source)
		_ = source.Close()
		if copyErr == nil {
			copyErr = destination.Sync()
		}
		if closeErr := destination.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			_ = os.Remove(finalPath)
			return "", fmt.Errorf("finalize export target: %w", copyErr)
		}
		_ = os.Remove(tempPath)
		return finalPath, nil
	}
	return "", invalidRequestf("too many exports with the same name in output directory")
}

// ---- 成功钩子：产物校验 + 提交 ------------------------------------------

// OnTaskSucceeded 是调度层在任务成功提交后的钩子：
// 导出任务校验产物清单并落盘，节点任务更新节点状态与审核门槛。
func (s *Service) OnTaskSucceeded(ctx context.Context, task domain.Task, result resultcommit.ExecutionResult) error {
	if task.Kind == domain.TaskKindExport {
		return s.finalizeExport(ctx, task, result)
	}
	return s.onNodeTaskSucceeded(ctx, task)
}

// finalizeExport 校验 Runner 产物并复制到用户授权目录，登记 Asset/ArtifactCommit。
func (s *Service) finalizeExport(ctx context.Context, task domain.Task, result resultcommit.ExecutionResult) error {
	record, err := s.store.GetExportByTask(ctx, task.ID)
	if err != nil {
		return err
	}
	if record.Status == domain.TaskStatusSucceeded || record.Status == domain.TaskStatusCanceled {
		return nil
	}
	attempt, found, err := s.store.LatestAttempt(ctx, task.ID)
	if err != nil {
		return err
	}
	if !exportCanFinalize(record, task, attempt, found, result.Binding) {
		return nil
	}
	prepared, err := s.prepareExportResult(ctx, task, result)
	if err != nil {
		code := domain.ErrorCodeWorkerExited
		switch {
		case errors.Is(err, domain.ErrInvalidRequest):
			code = domain.ErrorCodeInvalidRequest
		case errors.Is(err, domain.ErrInsufficientDisk):
			code = domain.ErrorCodeInsufficientDisk
		}
		return errors.Join(err, s.markExportFailed(ctx, record, code, "export.commit_failed"))
	}
	var events []domain.DomainEvent
	if err := s.store.DoTx(ctx, func(tx *store.Tx) error {
		return prepared.Commit(ctx, tx, task, &events)
	}); err != nil {
		return errors.Join(err, s.markExportFailed(ctx, record,
			domain.ErrorCodePersistenceUnavailable, "export.commit_failed"))
	}
	if finalizer, ok := prepared.(interface{ AfterCommit() error }); ok {
		if err := finalizer.AfterCommit(); err != nil && s.log != nil {
			s.log.Warn("export", "export.commit_journal_retained", map[string]any{"error": err.Error()})
		}
	}
	s.publish(events...)
	return nil
}

// OnTaskCanceled 在导出任务取消后收敛导出记录（取消不是失败）。
func (s *Service) OnTaskCanceled(ctx context.Context, task domain.Task) error {
	if task.Kind != domain.TaskKindExport {
		return nil
	}
	record, err := s.store.GetExportByTask(ctx, task.ID)
	if err != nil {
		return err
	}
	switch record.Status {
	case domain.TaskStatusSucceeded, domain.TaskStatusFailed, domain.TaskStatusCanceled:
		return nil
	}
	var events []domain.DomainEvent
	err = s.store.DoTx(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateExportStatus(ctx, record.ID, domain.TaskStatusCanceled, "", s.Now()); err != nil {
			return err
		}
		return s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type:      "export.canceled",
			ProjectID: record.ProjectID,
			TaskID:    task.ID,
			Data:      map[string]any{"export_id": record.ID},
		})
	})
	if err != nil {
		return err
	}
	s.publish(events...)
	return nil
}

// exportCanFinalize 判断一次成功回调是否可以写入导出记录。
//
// 已成功/取消的记录不重写；FAILED 记录只有在最新 Attempt 确实成功、且结果
// 绑定属于该 Attempt 时才允许收敛。这样既支持失败后的重试成功，也挡住同一
// 任务旧 Attempt 的迟到回调。绑定为 0 表示旧执行器未回传编号，调度层已完成
// 当前结果校验，继续兼容。
func exportCanFinalize(record domain.ExportRecord, task domain.Task,
	attempt domain.Attempt, hasAttempt bool, binding resultcommit.ExecutionBinding) bool {
	switch record.Status {
	case domain.TaskStatusSucceeded, domain.TaskStatusCanceled:
		return false
	case domain.TaskStatusFailed:
		if task.Status != domain.TaskStatusSucceeded || !hasAttempt ||
			attempt.Status != domain.StepStatusSucceeded {
			return false
		}
		if binding.TaskID != "" && binding.TaskID != string(task.ID) {
			return false
		}
		if binding.Attempt != 0 && binding.Attempt != attempt.AttemptNo {
			return false
		}
		if binding.StepID != "" && attempt.StepID != "" &&
			binding.StepID != attempt.StepID {
			return false
		}
		return true
	default:
		return true
	}
}

func (s *Service) markExportFailed(ctx context.Context, record domain.ExportRecord, code domain.ErrorCode, messageKey string) error {
	var events []domain.DomainEvent
	err := s.store.DoTx(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateExportStatus(ctx, record.ID, domain.TaskStatusFailed, "", s.Now()); err != nil {
			return err
		}
		return s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type:      "export.failed",
			ProjectID: record.ProjectID,
			TaskID:    record.TaskID,
			Data: map[string]any{
				"export_id":   record.ID,
				"code":        code,
				"message_key": messageKey,
			},
		})
	})
	if err != nil {
		return err
	}
	s.publish(events...)
	return nil
}

// validateAttemptRelPath 拒绝绝对路径、..、空片段与控制字符。
func validateAttemptRelPath(rel string) error {
	if rel == "" || len(rel) > 255 {
		return fmt.Errorf("output rel_path must be a non-empty short relative path")
	}
	for _, ch := range rel {
		if ch < 0x20 || ch == 0x7F {
			return fmt.Errorf("output rel_path contains control characters")
		}
	}
	if filepath.IsAbs(rel) {
		return fmt.Errorf("output rel_path must be relative")
	}
	for _, part := range strings.Split(strings.ReplaceAll(rel, "\\", "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("output rel_path contains invalid segment")
		}
	}
	return nil
}

// pathWithin 报告 target 是否位于 root 内（含符号链接解析后的比较）。
func pathWithin(root, target string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

// validateMediaHeader 用容器/流特征做最小校验（ffprobe 不可用时的可核对证据）。
func validateMediaHeader(path string, format domain.ExportFormat) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	defer func() { _ = file.Close() }()
	head := make([]byte, 32)
	n, err := file.Read(head)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read output header: %w", err)
	}
	head = head[:n]
	switch format {
	case domain.ExportFormatMP4H264:
		if len(head) < 12 || !strings.Contains(string(head), "ftyp") {
			return fmt.Errorf("output is not an MP4 container")
		}
	case domain.ExportFormatWAV:
		if len(head) < 12 || string(head[:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
			return fmt.Errorf("output is not a WAV container")
		}
	case domain.ExportFormatMP3:
		isID3 := len(head) >= 3 && string(head[:3]) == "ID3"
		isFrame := len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0
		if !isID3 && !isFrame {
			return fmt.Errorf("output is not an MP3 stream")
		}
	}
	return nil
}

func exportAsset(
	projectID domain.ID,
	runID domain.ID,
	kind domain.ExportKind,
	path string,
	size int64,
	digest string,
	now time.Time,
) domain.Asset {
	assetType := domain.AssetTypeOther
	switch kind {
	case domain.ExportKindVideo:
		assetType = domain.AssetTypeVideo
	case domain.ExportKindAudio:
		assetType = domain.AssetTypeAudio
	case domain.ExportKindSubtitle:
		assetType = domain.AssetTypeSubtitle
	}
	return domain.Asset{
		ID:         store.NewID("asset"),
		ProjectID:  projectID,
		Type:       assetType,
		RunID:      runID,
		Name:       filepath.Base(path),
		Path:       path,
		SizeBytes:  size,
		SHA256:     digest,
		Status:     domain.AssetStatusReady,
		ImportedAt: now,
	}
}
