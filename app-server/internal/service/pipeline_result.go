package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/resultcommit"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// preparedOutput 是一个已落盘、等待随任务终态登记的产物。
type preparedOutput struct {
	AssetID domain.ID
	RelPath string
	Size    int64
}

// preparedNodeResult 是节点/句段任务的产物提交计划。
type preparedNodeResult struct {
	svc       *Service
	task      domain.Task
	now       time.Time
	assets    []domain.Asset
	locations []domain.AssetLocation
	outputs   []preparedOutput
	intents   []*fileIntent

	segments         []domain.SegmentInput
	trackType        domain.TrackType
	trackLanguage    string
	sourceRevisionID domain.ID
	audioOutputs     []preparedOutput
	previewAssetID   domain.ID
}

// ArtifactIDs 实现 resultcommit.Prepared。
func (p *preparedNodeResult) ArtifactIDs() []domain.ID {
	ids := make([]domain.ID, 0, len(p.outputs))
	for _, output := range p.outputs {
		ids = append(ids, output.AssetID)
	}
	return ids
}

// PrepareNodeResult 在任务终态事务前校验并落盘节点/句段/导出产物。
//
// 只做文件 IO 与解析，不写业务库；任何失败都让任务走失败路径，不产生
// SUCCEEDED 空产物。
func (s *Service) PrepareNodeResult(
	ctx context.Context,
	task domain.Task,
	result resultcommit.ExecutionResult,
) (resultcommit.Prepared, error) {
	if task.Kind == domain.TaskKindExport {
		return s.prepareExportResult(ctx, task, result)
	}
	if len(result.Outputs) == 0 {
		return nil, fmt.Errorf("node task produced no outputs")
	}
	if result.OutputDir == "" {
		return nil, fmt.Errorf("node task output dir is empty")
	}
	taskRoot := filepath.Join(s.workDir(), string(task.ID))
	if !pathWithin(taskRoot, result.OutputDir) {
		return nil, invalidRequestf("output dir is outside task %s work dir", task.ID)
	}
	// 拒绝 OutputDir 及其子路径上的符号链接：Lstat 最终文件只能识别文件本身，
	// 父目录被替换成符号链接时仍会把写入导向工作区外。
	if err := rejectSymlinkPath(s.workDir(), result.OutputDir); err != nil {
		return nil, invalidRequestf("output dir path contains symlink: %v", err)
	}

	prepared := &preparedNodeResult{svc: s, task: task, now: s.Now()}
	type validatedOutput struct {
		output  resultcommit.ExecutionOutput
		source  string
		size    int64
		assetID domain.ID
	}
	validated := make([]validatedOutput, 0, len(result.Outputs))
	for _, output := range result.Outputs {
		if err := validateAttemptRelPath(output.RelPath); err != nil {
			return nil, invalidRequestf("%v", err)
		}
		source := filepath.Join(result.OutputDir, filepath.FromSlash(output.RelPath))
		if !pathWithin(result.OutputDir, source) {
			return nil, invalidRequestf("output %q escapes attempt dir", output.RelPath)
		}
		if err := rejectSymlinkPath(result.OutputDir, source); err != nil {
			return nil, invalidRequestf("output %q path contains symlink: %v", output.RelPath, err)
		}
		info, err := os.Lstat(source)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			return nil, fmt.Errorf("output %q is missing, not a regular file, or empty", output.RelPath)
		}
		if output.SizeBytes > 0 && output.SizeBytes != info.Size() {
			return nil, fmt.Errorf("output %q size %d != reported %d", output.RelPath, info.Size(), output.SizeBytes)
		}
		validated = append(validated, validatedOutput{
			output: output, source: source, size: info.Size(), assetID: store.NewID("asset"),
		})
	}

	for _, item := range validated {
		ext := filepath.Ext(item.output.RelPath)
		destination := filepath.Join(s.mediaDir(), task.ProjectID, string(item.assetID)+ext)
		intent, err := s.beginFileIntent(item.assetID, destination, true)
		if err != nil {
			return nil, err
		}
		prepared.intents = append(prepared.intents, intent)
		copied, digest, err := copyWithDigest(item.source, destination)
		if err != nil {
			return nil, err
		}
		assetType := assetTypeForName(item.output.RelPath)
		if task.Kind == domain.TaskKindSegment || task.NodeKey == domain.NodeKeySeparate {
			if assetType == domain.AssetTypeAudio {
				assetType = domain.AssetTypeAudioTrack
			}
		}
		meta := resultMediaMeta(item.output.Media)
		prepared.assets = append(prepared.assets, domain.Asset{
			ID:         item.assetID,
			ProjectID:  task.ProjectID,
			Type:       assetType,
			Role:       assetRoleForTask(task, meta),
			Language:   task.Language,
			RunID:      task.RunID,
			Name:       filepath.Base(item.output.RelPath),
			Path:       copied,
			SizeBytes:  item.size,
			SHA256:     digest,
			Meta:       meta,
			Status:     domain.AssetStatusReady,
			ImportedAt: prepared.now,
		})
		prepared.locations = append(prepared.locations, domain.AssetLocation{
			ID:        store.NewID("loc"),
			AssetID:   item.assetID,
			Path:      copied,
			Source:    domain.LocationSourceManagedCopy,
			Owner:     domain.LocationOwnerApp,
			Available: true,
			CreatedAt: prepared.now,
		})
		prepared.outputs = append(prepared.outputs, preparedOutput{
			AssetID: item.assetID, RelPath: item.output.RelPath, Size: item.size,
		})
	}

	if err := prepared.materializeOutputs(); err != nil {
		return nil, err
	}
	return prepared, nil
}

func (p *preparedNodeResult) AfterCommit() error {
	return finishFileIntents(p.intents)
}

// assetRoleForTask 按任务/节点与媒体职责给出产物角色；未知组合留空。
func assetRoleForTask(task domain.Task, meta *domain.MediaMeta) domain.AssetRole {
	if task.Kind == domain.TaskKindSegment {
		return domain.AssetRoleDubAudio
	}
	switch task.NodeKey {
	case domain.NodeKeyImport:
		return domain.AssetRoleOriginal
	case domain.NodeKeySeparate:
		if meta == nil {
			return ""
		}
		switch meta.Role {
		case "vocals":
			return domain.AssetRoleVoice
		case "background":
			return domain.AssetRoleAmbience
		default:
			return ""
		}
	case domain.NodeKeyExtract:
		return domain.AssetRoleSourceSubtitle
	case domain.NodeKeyTranslate:
		return domain.AssetRoleTranslatedSubtitle
	case domain.NodeKeyErase:
		return domain.AssetRoleCleanVideo
	case domain.NodeKeyMix:
		return domain.AssetRoleFinalVideo
	default:
		return ""
	}
}

// materializeOutputs 按节点语义解析产物，准备字幕轨/候选/预览的登记信息。
func (p *preparedNodeResult) materializeOutputs() error {
	task := p.task
	switch {
	case task.Kind == domain.TaskKindSegment:
		if task.SegmentID == "" {
			return invalidRequestf("segment task requires segment_id")
		}
		for _, output := range p.outputs {
			if isAudioPath(output.RelPath) {
				p.audioOutputs = append(p.audioOutputs, output)
			}
		}
		if len(p.audioOutputs) == 0 {
			return fmt.Errorf("segment task produced no audio outputs")
		}
		return nil
	case task.Kind != domain.TaskKindNode:
		return nil
	}

	switch task.NodeKey {
	case domain.NodeKeyExtract, domain.NodeKeyTranslate:
		language := task.Language
		if language == "" {
			return invalidRequestf("node %s requires language", task.NodeKey)
		}
		subtitle, err := p.uniqueSubtitleOutput()
		if err != nil {
			return err
		}
		// 先按已校验的文件大小拒绝超限字幕，避免整文件读入把 Core 内存打满；
		// parseSRT 内部的长度检查只作为第二道防线。
		if subtitle.Size > maxSubtitleBytes {
			return invalidRequestf("subtitle exceeds %d bytes", maxSubtitleBytes)
		}
		data, err := os.ReadFile(subtitle.AssetPath(p.assets))
		if err != nil {
			return fmt.Errorf("read subtitle output: %w", err)
		}
		segments, err := parseSRT(data)
		if err != nil {
			return err
		}
		p.segments = segments
		p.trackLanguage = language
		if task.NodeKey == domain.NodeKeyExtract {
			p.trackType = domain.TrackTypeTranscription
		} else {
			p.trackType = domain.TrackTypeTranslation
			if len(task.InputRevisionIDs) > 0 {
				p.sourceRevisionID = task.InputRevisionIDs[0]
			}
		}
	case domain.NodeKeyMix:
		if task.Language == "" {
			return invalidRequestf("node 08 requires language")
		}
		for _, output := range p.outputs {
			switch assetTypeForName(output.RelPath) {
			case domain.AssetTypeVideo, domain.AssetTypeAudio:
				p.previewAssetID = output.AssetID
				return nil
			}
		}
		return fmt.Errorf("node 08 produced no media output")
	}
	return nil
}

func (p *preparedNodeResult) uniqueSubtitleOutput() (preparedOutput, error) {
	var found []preparedOutput
	for _, output := range p.outputs {
		if strings.EqualFold(filepath.Ext(output.RelPath), ".srt") {
			found = append(found, output)
		}
	}
	if len(found) == 0 {
		return preparedOutput{}, fmt.Errorf("node %s produced no SRT output", p.task.NodeKey)
	}
	if len(found) > 1 {
		return preparedOutput{}, fmt.Errorf("node %s produced %d SRT outputs, want exactly one", p.task.NodeKey, len(found))
	}
	return found[0], nil
}

// AssetPath 返回已登记 Asset 的托管路径（在 p.assets 中按 ID 查找）。
func (o preparedOutput) AssetPath(assets []domain.Asset) string {
	for _, asset := range assets {
		if asset.ID == o.AssetID {
			return asset.Path
		}
	}
	return ""
}

func isAudioPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav", ".mp3", ".flac", ".m4a", ".aac":
		return true
	default:
		return false
	}
}

// rejectSymlinkPath 拒绝 root 之下任一路径组件是符号链接。
//
// root 本身不检查（应用数据目录可能位于平台符号链接下），只检查 root 的
// 后代；目标必须在 root 内。
func rejectSymlinkPath(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("target is outside root")
	}
	if rel == "." {
		return nil
	}
	current := rootAbs
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in path: %s", current)
		}
	}
	return nil
}

// Commit 实现 resultcommit.Prepared：在任务终态事务内登记产物与节点回写。
func (p *preparedNodeResult) Commit(
	ctx context.Context,
	tx *store.Tx,
	task domain.Task,
	events *[]domain.DomainEvent,
) error {
	for i := range p.assets {
		if err := tx.CreateAsset(ctx, p.assets[i]); err != nil {
			return err
		}
		if err := tx.AddAssetLocation(ctx, p.locations[i]); err != nil {
			return err
		}
	}
	attempt, ok, err := tx.LatestAttempt(ctx, task.ID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("task %s has no attempt for artifact commit", task.ID)
	}
	committedAt := p.now
	if err := tx.CreateArtifactCommit(ctx, domain.ArtifactCommit{
		ID:          store.NewID("commit"),
		AttemptID:   attempt.ID,
		AssetIDs:    p.ArtifactIDs(),
		Status:      domain.CommitStatusCommitted,
		CommittedAt: &committedAt,
	}); err != nil {
		return err
	}

	if err := p.commitNodeOutputs(ctx, tx, task, events); err != nil {
		return err
	}
	if task.RunID != "" {
		if p.svc.opts.PipelineOrchestration {
			return p.svc.advancePipeline(ctx, tx, task.RunID, events)
		}
		return p.svc.recomputeRunNodes(ctx, tx, task.RunID, events)
	}
	return nil
}

func (p *preparedNodeResult) commitNodeOutputs(
	ctx context.Context,
	tx *store.Tx,
	task domain.Task,
	events *[]domain.DomainEvent,
) error {
	switch {
	case task.Kind == domain.TaskKindSegment:
		return p.svc.commitCandidateOutputs(ctx, tx, task, p, events)
	case task.Kind != domain.TaskKindNode:
		return nil
	}
	switch task.NodeKey {
	case domain.NodeKeyExtract:
		_, err := p.svc.commitSubtitleTrack(ctx, tx, task, p, events)
		return err
	case domain.NodeKeyTranslate:
		_, err := p.svc.commitSubtitleTrack(ctx, tx, task, p, events)
		return err
	case domain.NodeKeyMix:
		if p.previewAssetID == "" {
			return fmt.Errorf("node 08 has no committed preview asset")
		}
		return p.svc.commitDeliveryGate(ctx, tx, task, events)
	default:
		return nil
	}
}

// commitSubtitleTrack 创建或追加 04/06 字幕轨，06 同时创建翻译审核门槛。
func (s *Service) commitSubtitleTrack(
	ctx context.Context,
	tx *store.Tx,
	task domain.Task,
	p *preparedNodeResult,
	events *[]domain.DomainEvent,
) (domain.SubtitleRevision, error) {
	if len(p.segments) == 0 {
		return domain.SubtitleRevision{}, fmt.Errorf("node %s produced no subtitle segments", task.NodeKey)
	}
	if task.RunID == "" {
		return domain.SubtitleRevision{}, invalidRequestf("node %s requires run_id", task.NodeKey)
	}
	revision, err := s.saveTaskSubtitleResult(ctx, tx, task, p)
	if err != nil {
		return domain.SubtitleRevision{}, err
	}
	if task.NodeKey != domain.NodeKeyTranslate {
		return revision, nil
	}
	track, err := tx.GetSubtitleTrack(ctx, revision.TrackID)
	if err != nil {
		return domain.SubtitleRevision{}, err
	}
	if track.CurrentRevisionID != revision.ID {
		return revision, nil
	}
	run, err := tx.GetWorkflowRun(ctx, task.RunID)
	if err != nil {
		return domain.SubtitleRevision{}, err
	}
	tpl, err := pipelineTemplateForRun(run)
	if err != nil {
		return domain.SubtitleRevision{}, err
	}
	// tool_translate 等模板显式跳过 06 审核；full_pipeline 仍创建 translation gate。
	if !tpl.RequiresReview(task.NodeKey) {
		return revision, nil
	}
	digest, err := domain.RevisionBindingDigest(revision)
	if err != nil {
		return domain.SubtitleRevision{}, err
	}
	gate, err := s.createReviewGate(ctx, tx, task.RunID, domain.ReviewTypeTranslation, p.trackLanguage, digest)
	if err != nil {
		return domain.SubtitleRevision{}, err
	}
	if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
		Type:      "review.created",
		ProjectID: task.ProjectID,
		Data: map[string]any{
			"run_id":          task.RunID,
			"review_id":       gate.ID,
			"review_type":     gate.Type,
			"language":        gate.Language,
			"revision_digest": gate.RevisionDigest,
		},
	}); err != nil {
		return domain.SubtitleRevision{}, err
	}
	return revision, nil
}

// upsertTrackRevision 优先复用同项目/语言/类型的现有轨道；否则新建轨道。
func (s *Service) upsertTrackRevision(
	ctx context.Context,
	tx *store.Tx,
	projectID domain.ID,
	language string,
	trackType domain.TrackType,
	inputRevisionID domain.ID,
	segments []domain.SegmentInput,
	now time.Time,
) (domain.SubtitleRevision, error) {
	tracks, err := tx.ListProjectTracks(ctx, projectID)
	if err != nil {
		return domain.SubtitleRevision{}, err
	}
	var existing *domain.SubtitleTrack
	for i := range tracks {
		track := &tracks[i]
		if track.Language != language || track.Type != trackType || track.CurrentRevisionID == "" {
			continue
		}
		if existing == nil || track.UpdatedAt.After(existing.UpdatedAt) ||
			(track.UpdatedAt.Equal(existing.UpdatedAt) && track.ID > existing.ID) {
			existing = track
		}
	}
	if existing != nil {
		revision, err := tx.SaveSubtitleRevision(ctx, existing.ID, existing.CurrentRevisionID, store.RevisionSave{
			AuthorType:      domain.AuthorTypeSystem,
			Segments:        segments,
			InputRevisionID: inputRevisionID,
		}, now)
		if err != nil {
			return domain.SubtitleRevision{}, err
		}
		return revision, nil
	}

	revisionID := store.NewID("rev")
	trackID := store.NewID("track")
	revision := domain.SubtitleRevision{
		ID:              revisionID,
		TrackID:         trackID,
		AuthorType:      domain.AuthorTypeSystem,
		Segments:        make([]domain.Segment, 0, len(segments)),
		InputRevisionID: inputRevisionID,
		CreatedAt:       now,
	}
	for _, segment := range segments {
		id := segment.ID
		if id == "" {
			id = store.NewID("seg")
		}
		revision.Segments = append(revision.Segments, domain.Segment{
			ID:      id,
			StartMs: segment.StartMs,
			EndMs:   segment.EndMs,
			Text:    segment.Text,
			Speaker: segment.Speaker,
			Lineage: segment.Lineage,
		})
	}
	track := domain.SubtitleTrack{
		ID:                trackID,
		ProjectID:         projectID,
		Language:          language,
		Type:              trackType,
		CurrentRevisionID: revisionID,
		Revisions:         []domain.SubtitleRevision{revision},
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := tx.CreateSubtitleTrack(ctx, track); err != nil {
		return domain.SubtitleRevision{}, err
	}
	stored, err := tx.GetSubtitleTrack(ctx, trackID)
	if err != nil {
		return domain.SubtitleRevision{}, err
	}
	if current := currentRevision(stored); current != nil {
		return *current, nil
	}
	return domain.SubtitleRevision{}, fmt.Errorf("created track %s has no current revision", trackID)
}

// commitCandidateOutputs 把 07 句级音频登记为候选，并在无选择时推荐首个候选。
func (s *Service) commitCandidateOutputs(
	ctx context.Context,
	tx *store.Tx,
	task domain.Task,
	p *preparedNodeResult,
	events *[]domain.DomainEvent,
) error {
	if task.SegmentID == "" {
		return invalidRequestf("segment task requires segment_id")
	}
	if task.Language == "" {
		return invalidRequestf("segment task requires language")
	}
	revision := 0
	if raw, ok := task.Params["content_revision"]; ok {
		switch value := raw.(type) {
		case float64:
			revision = int(value)
		case int:
			revision = value
		case int64:
			revision = int(value)
		}
	}
	if revision < 1 {
		segment, err := tx.GetSegmentContext(ctx, task.SegmentID)
		if err != nil {
			return err
		}
		revision = segment.Segment.ContentRevision
	}
	if revision < 1 {
		return invalidRequestf("segment %s has invalid content revision", task.SegmentID)
	}

	existing, err := tx.ListAudioCandidates(ctx, task.SegmentID, task.Language, &revision)
	if err != nil {
		return err
	}
	hasRecommended := false
	dependencyKey, _ := readCoreTaskParams(task.Params)["segment_cache_key"].(string)
	assetKeys := map[domain.ID]string{}
	if dependencyKey != "" {
		tasks, err := tx.ListProjectTasks(ctx, task.ProjectID)
		if err != nil {
			return err
		}
		for _, previous := range tasks {
			if previous.Kind != domain.TaskKindSegment || previous.Status != domain.TaskStatusSucceeded {
				continue
			}
			key, _ := readCoreTaskParams(previous.Params)["segment_cache_key"].(string)
			for _, assetID := range previous.ArtifactIDs {
				assetKeys[assetID] = key
			}
		}
	}
	for _, candidate := range existing {
		if candidate.IsRecommended && (dependencyKey == "" || assetKeys[candidate.AudioAssetID] == dependencyKey) {
			hasRecommended = true
			break
		}
	}
	modelVersion := "unlocked"
	if task.ModelLock != nil {
		if task.ModelLock.Version != "" {
			modelVersion = task.ModelLock.Version
		} else if task.ModelLock.ReleaseID != "" {
			modelVersion = task.ModelLock.ReleaseID
		}
	}

	recommendedID := domain.ID("")
	for i, output := range p.audioOutputs {
		candidate := domain.AudioCandidate{
			ID:            store.NewID("cand"),
			ProjectID:     task.ProjectID,
			SegmentID:     task.SegmentID,
			Language:      task.Language,
			Revision:      revision,
			ModelVersion:  modelVersion,
			AudioAssetID:  output.AssetID,
			IsRecommended: !hasRecommended && i == 0,
			CreatedAt:     p.now,
		}
		if err := tx.CreateAudioCandidate(ctx, candidate); err != nil {
			return err
		}
		if candidate.IsRecommended {
			recommendedID = candidate.ID
		}
	}
	if recommendedID == "" {
		return nil
	}
	selection, err := tx.GetCandidateSelection(ctx, task.SegmentID, task.Language)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	needSelect := errors.Is(err, domain.ErrNotFound) || selection.Revision != revision
	if !needSelect && dependencyKey != "" {
		for _, candidate := range existing {
			if candidate.ID == selection.CandidateID && assetKeys[candidate.AudioAssetID] != dependencyKey {
				needSelect = true
			}
		}
	}
	current, currentErr := tx.GetSegmentContext(ctx, task.SegmentID)
	if currentErr != nil {
		return currentErr
	}
	if current.Segment.ContentRevision != revision {
		// The late audio remains a candidate for its frozen revision, without
		// changing a selection made for a newer edit.
		return nil
	}
	if needSelect {
		if err := tx.SaveCandidateSelection(ctx, domain.CandidateSelection{
			SegmentID:   task.SegmentID,
			Language:    task.Language,
			CandidateID: recommendedID,
			Revision:    revision,
			SelectedAt:  p.now,
		}); err != nil {
			return err
		}
		if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
			Type:      "segment.candidate_selected",
			ProjectID: task.ProjectID,
			Data: map[string]any{
				"segment_id":   task.SegmentID,
				"language":     task.Language,
				"candidate_id": recommendedID,
				"revision":     revision,
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

// commitDeliveryGate 为 08 成功任务创建交付审核门槛。
func (s *Service) commitDeliveryGate(
	ctx context.Context,
	tx *store.Tx,
	task domain.Task,
	events *[]domain.DomainEvent,
) error {
	if task.RunID == "" {
		return invalidRequestf("node 08 requires run_id")
	}
	digest, err := domain.NodeReviewDigest(task.RunID, task.NodeKey, task.Language, task.ID,
		task.ArtifactIDs, task.InputDigest)
	if err != nil {
		return err
	}
	gate, err := s.createReviewGate(ctx, tx, task.RunID, domain.ReviewTypeDelivery, task.Language, digest)
	if err != nil {
		return err
	}
	return s.stageEvent(ctx, tx, events, domain.DomainEvent{
		Type:      "review.created",
		ProjectID: task.ProjectID,
		Data: map[string]any{
			"run_id":          task.RunID,
			"review_id":       gate.ID,
			"review_type":     gate.Type,
			"language":        gate.Language,
			"revision_digest": gate.RevisionDigest,
		},
	})
}
