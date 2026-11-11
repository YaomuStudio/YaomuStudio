package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// pipelineSnapshot 是一次推进/聚合读取到的事务内事实。
type pipelineSnapshot struct {
	run      *domain.WorkflowRun
	tasks    []domain.Task
	taskByID map[domain.ID]domain.Task
	gates    []domain.ReviewGate
	tracks   []domain.SubtitleTrack
}

func (s *Service) loadPipelineSnapshot(ctx context.Context, tx *store.Tx, runID domain.ID) (*pipelineSnapshot, error) {
	run, err := tx.GetWorkflowRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	tasks, err := tx.ListRunTasks(ctx, runID)
	if err != nil {
		return nil, err
	}
	gates, err := tx.ListRunReviews(ctx, runID)
	if err != nil {
		return nil, err
	}
	tracks, err := tx.ListProjectTracks(ctx, run.ProjectID)
	if err != nil {
		return nil, err
	}
	byID := make(map[domain.ID]domain.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
	}
	return &pipelineSnapshot{run: &run, tasks: tasks, taskByID: byID, gates: gates, tracks: tracks}, nil
}

// advancePipeline 在事务内创建当前可执行的流水线 Task；可重复调用。
//
// 只创建“前置节点已完成”的下一批单元，不一次性铺开全部节点；已有确定性
// 身份的 Task 会被跳过。审核未放行不创建下游 07/08。
func (s *Service) advancePipeline(ctx context.Context, tx *store.Tx, runID domain.ID, events *[]domain.DomainEvent) error {
	if !s.opts.PipelineOrchestration || runID == "" {
		return nil
	}
	if err := s.recomputeRunNodes(ctx, tx, runID, events); err != nil {
		return err
	}
	snap, err := s.loadPipelineSnapshot(ctx, tx, runID)
	if err != nil {
		return err
	}
	if snap.run.Status.Terminal() {
		return nil
	}
	tpl, err := pipelineTemplateForRun(*snap.run)
	if err != nil {
		return err
	}
	if !tpl.Has(domain.NodeKeyImport) {
		return invalidRequestf("template %s has no import node", tpl.ID)
	}
	// 复用配置在推进前校验，让创建 Run/恢复时立即返回明确错误，而不是等
	// 到 03 成功后才在任务事务里失败。
	if err := validatePipelineReuse(snap); err != nil {
		return err
	}
	if err := s.ensureImportNode(ctx, tx, snap, events); err != nil {
		return err
	}

	if tpl.Has(domain.NodeKeyAnalyze) && !analyzeSkipped(tpl) && predecessorsDone(tpl, snap.run, domain.NodeKeyAnalyze) {
		if err := s.ensureNodeTask(ctx, tx, snap, domain.NodeKeyAnalyze, "", snap.run.Config.InputRevisionIDs, digestOfStrings(snap.run.Config.InputRevisionIDs), nil, events); err != nil {
			return err
		}
	}
	if tpl.Has(domain.NodeKeySeparate) && predecessorsDone(tpl, snap.run, domain.NodeKeySeparate) {
		if snap.run.Config.ReuseAudioTrackID != "" {
			if err := s.setNodeStatus(ctx, tx, snap.run, domain.NodeKeySeparate, domain.NodeStatusReused, events); err != nil {
				return err
			}
		} else if err := s.ensureNodeTask(ctx, tx, snap, domain.NodeKeySeparate, "", snap.run.Config.InputRevisionIDs, digestOfStrings(snap.run.Config.InputRevisionIDs), nil, events); err != nil {
			return err
		}
	}
	if tpl.Has(domain.NodeKeyExtract) && predecessorsDone(tpl, snap.run, domain.NodeKeyExtract) {
		// 专属链路的擦除必须先跑 OCR 定位字幕；即使绑定了上传原文轨，04 也不能复用，
		// 否则 05 没有 OCR 产物。06 会显式绑定上传轨，不依赖 04 的新轨。
		// full_pipeline 保持既有复用语义，避免改变已归档行为。
		if sourceExtractReused(tpl, *snap.run) {
			if err := s.setNodeStatus(ctx, tx, snap.run, domain.NodeKeyExtract, domain.NodeStatusReused, events); err != nil {
				return err
			}
		} else if tpl.ID == templateCustomPipeline && !s.customExtractNeeded(snap, tpl) {
			// 所有目标译文都已导入且不需要擦除 OCR 时，04 不产生下游输入。
			if err := s.setNodeStatus(ctx, tx, snap.run, domain.NodeKeyExtract, domain.NodeStatusSkipped, events); err != nil {
				return err
			}
		} else {
			inputs := snap.run.Config.InputRevisionIDs
			if snap.run.Config.ReuseAudioTrackID != "" {
				inputs = []string{snap.run.Config.ReuseAudioTrackID}
			} else if tpl.Has(domain.NodeKeySeparate) {
				if artifacts := latestSucceededArtifacts(snap.tasks, domain.NodeKeySeparate, ""); len(artifacts) > 0 {
					inputs = artifacts
				}
			} else if artifacts := latestSucceededArtifacts(snap.tasks, domain.NodeKeyAnalyze, ""); len(artifacts) > 0 {
				inputs = append(append([]string{}, inputs...), artifacts...)
			}
			// 04 的内部 adapter 参数：language 是唯一把源语言带到 execute 请求的通道
			// （Task.Language 不进 execadapter.Request）；extract 形状镜像冻结的 RunConfig，
			// 不是 FlashCLI argv/bundle 契约。
			params := map[string]any{"language": snap.run.SourceLanguage}
			if extract := snap.run.Config.Extract; extract != nil {
				extractParams := map[string]any{"mode": string(extract.Mode)}
				if extract.OCRLanguage != "" {
					extractParams["ocr_language"] = string(extract.OCRLanguage)
				}
				if extract.OCRQuality != "" {
					extractParams["ocr_quality"] = string(extract.OCRQuality)
				}
				params["extract"] = extractParams
			}
			if err := s.ensureNodeTask(ctx, tx, snap, domain.NodeKeyExtract, snap.run.SourceLanguage,
				inputs, digestOfStrings(inputs), params, events); err != nil {
				return err
			}
		}
	}
	if tpl.Has(domain.NodeKeyErase) && predecessorsDone(tpl, snap.run, domain.NodeKeyErase) {
		inputs := append([]string{}, snap.run.Config.InputRevisionIDs...)
		inputs = append(inputs, latestSucceededArtifacts(snap.tasks, domain.NodeKeyExtract, snap.run.SourceLanguage)...)
		// 05 的内部 adapter 参数：擦除范围；未配置时保持 nil，避免改变无擦除节点的 Run 身份。
		var params map[string]any
		if mode := snap.run.Config.EraseMode; mode != "" {
			params = map[string]any{"erase_mode": string(mode)}
		}
		if err := s.ensureNodeTask(ctx, tx, snap, domain.NodeKeyErase, "",
			inputs, digestOfStrings(inputs), params, events); err != nil {
			return err
		}
	}
	if tpl.Has(domain.NodeKeyTranslate) && predecessorsDone(tpl, snap.run, domain.NodeKeyTranslate) {
		if revision := sourceTranscriptionRevision(snap); revision != nil {
			for _, language := range snap.run.Languages {
				if s.translationApproved(snap, language) {
					continue
				}
				if s.translationPending(snap, language) {
					continue
				}
				// 专属链路允许用上传译稿替代 06；没有 06 Task 时才把现有译稿视为已确认，
				// 避免平台任务运行期间误用旧轨。
				if tpl.ID == templateCustomPipeline && s.customTranslationImported(snap, language) {
					continue
				}
				// 06 冻结源字幕 revision 与渲染摘要；派发前物化为任务专属
				// SRT，Runner 不再依赖“项目最新轨”或 FlashCLI 占位参数。
				params := map[string]any{"language": language}
				core := coreTaskParams(params)
				core["subtitle_revision_id"] = revision.ID
				digest := sha256.Sum256([]byte(renderSRT(revision.Segments)))
				core["subtitle_digest"] = hex.EncodeToString(digest[:])
				if err := s.ensureNodeTask(ctx, tx, snap, domain.NodeKeyTranslate, language,
					[]string{revision.ID}, revision.ID, params, events); err != nil {
					return err
				}
			}
		}
	}

	if tpl.Has(domain.NodeKeyClone) {
		for _, language := range snap.run.Languages {
			if !s.translationReady(snap, language, tpl) {
				continue
			}
			revision := translationRevisionForRun(snap, language, tpl)
			if revision == nil {
				continue
			}
			if tpl.Has(domain.NodeKeySeparate) && !nodeCompleted(snap.run, domain.NodeKeySeparate) {
				continue
			}
			work, err := s.segmentWorkForRevision(ctx, tx, snap, *revision, language)
			if err != nil {
				if isCloneTimelineFailure(err) {
					// 当前译稿越界/时长未知时不生成可执行 07；失败归属 07 自身，
					// 不能回滚刚提交成功的合法任务与其他语言成果。
					if failErr := s.ensureCloneTimelineFailure(ctx, tx, snap, language, revision, err, events); failErr != nil {
						return failErr
					}
					continue
				}
				return err
			}
			for _, segment := range revision.Segments {
				item, err := requireSegmentWork(work, segment.ID)
				if err != nil {
					return err
				}
				if err := s.ensureSegmentTask(ctx, tx, snap, domain.NodeKeyClone, language, segment.ID,
					[]string{revision.ID, item.SourceID}, item.CacheKey, item.Params, events); err != nil {
					return err
				}
			}
			if err := s.ensureCloneAudioAssembly(ctx, tx, snap, language, revision, events); err != nil {
				return err
			}
		}
	}

	if tpl.Has(domain.NodeKeyMix) && mixDependenciesReady(snap, tpl) {
		for _, language := range snap.run.Languages {
			if !s.translationReady(snap, language, tpl) {
				if mixDependenciesFailed(snap, tpl) {
					if failErr := s.ensureMixPlanFailure(ctx, tx, snap, language, nil,
						invalidRequestf("mix prerequisites failed before language %s was ready", language),
						events); failErr != nil {
						return failErr
					}
				}
				continue
			}
			revision := translationRevisionForRun(snap, language, tpl)
			if revision == nil {
				if mixDependenciesFailed(snap, tpl) {
					if failErr := s.ensureMixPlanFailure(ctx, tx, snap, language, nil,
						invalidRequestf("mix prerequisites failed before language %s had a revision", language),
						events); failErr != nil {
						return failErr
					}
				}
				continue
			}
			if tpl.Has(domain.NodeKeyClone) {
				complete, err := s.cloneComplete(ctx, tx, snap, language, revision)
				if err != nil {
					return err
				}
				if !complete {
					if mixDependenciesFailed(snap, tpl) {
						if failErr := s.ensureMixPlanFailure(ctx, tx, snap, language, revision,
							invalidRequestf("mix prerequisites failed before language %s clone work completed", language),
							events); failErr != nil {
							return failErr
						}
					}
					continue
				}
			}
			plan, err := s.freezeMixPlan(ctx, tx, snap, tpl, *revision, language)
			if err != nil {
				switch {
				case errors.Is(err, errMixPlanNotReady):
					// 05/09 尚未提交所需产物：等待下一次推进，不创建缺输入任务。
					continue
				case errors.Is(err, domain.ErrInvalidRequest), errors.Is(err, errMixPlanUnavailable):
					// 08 自己的输入/本地准备问题登记为可读失败，不把上游 07 的
					// 成功事务拖回滚。
					if failErr := s.ensureMixPlanFailure(ctx, tx, snap, language, revision, err, events); failErr != nil {
						return failErr
					}
					continue
				default:
					return err
				}
			}
			params := map[string]any{"language": language, "plan": plan.Params}
			core := coreTaskParams(params)
			core["subtitle_revision_id"] = plan.SubtitleRevisionID
			core["subtitle_digest"] = plan.SubtitleDigest
			if err := s.ensureNodeTask(ctx, tx, snap, domain.NodeKeyMix, language,
				plan.InputIDs, plan.Digest, params, events); err != nil {
				return err
			}
		}
	}

	if err := s.recomputeRunNodes(ctx, tx, runID, events); err != nil {
		return err
	}
	return s.maybeCompleteRun(ctx, tx, runID, events)
}

// validatePipelineReuse 校验复用素材与节点语义一致：04 只接受原文（transcription）
// 字幕轨；译文轨复用尚未实现，必须在推进前显式拒绝而不是静默跳过 06。
func validatePipelineReuse(snap *pipelineSnapshot) error {
	id := snap.run.Config.ReuseSubtitleTrackID
	if id == "" {
		return nil
	}
	track := findTrackByID(snap.tracks, id)
	if track == nil {
		return invalidRequestf("reuse subtitle track %s not found", id)
	}
	if track.Type != domain.TrackTypeTranscription || track.Language != snap.run.SourceLanguage {
		return invalidRequestf("reusing translated subtitle tracks is not implemented")
	}
	if currentRevision(*track) == nil {
		return invalidRequestf("reuse subtitle track %s has no current revision", id)
	}
	return nil
}

// ensureImportNode 把 01 导入标记为完成；没有输入资产时标记失败，阻止下游。
func (s *Service) ensureImportNode(ctx context.Context, tx *store.Tx, snap *pipelineSnapshot, events *[]domain.DomainEvent) error {
	if nodeCompleted(snap.run, domain.NodeKeyImport) || nodeStatusOf(*snap.run, domain.NodeKeyImport) == domain.NodeStatusFailed {
		return nil
	}
	if len(snap.run.Config.InputRevisionIDs) == 0 {
		return s.setNodeStatus(ctx, tx, snap.run, domain.NodeKeyImport, domain.NodeStatusFailed, events)
	}
	return s.setNodeStatus(ctx, tx, snap.run, domain.NodeKeyImport, domain.NodeStatusDone, events)
}

func (s *Service) ensureNodeTask(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	node domain.PipelineNodeKey,
	language string,
	inputIDs []string,
	inputDigest string,
	params map[string]any,
	events *[]domain.DomainEvent,
) error {
	return s.ensurePipelineTask(ctx, tx, snap, domain.TaskKindNode, node, language, "", inputIDs, inputDigest, params, events)
}

func (s *Service) ensureSegmentTask(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	node domain.PipelineNodeKey,
	language, segmentID string,
	inputIDs []string,
	inputDigest string,
	params map[string]any,
	events *[]domain.DomainEvent,
) error {
	return s.ensurePipelineTask(ctx, tx, snap, domain.TaskKindSegment, node, language, segmentID, inputIDs, inputDigest, params, events)
}

func (s *Service) ensurePipelineTask(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	kind domain.TaskKind,
	node domain.PipelineNodeKey,
	language, segmentID string,
	inputIDs []string,
	inputDigest string,
	params map[string]any,
	events *[]domain.DomainEvent,
) error {
	if snap.run.Status.Terminal() {
		return nil
	}
	params, modelLock, err := freezeTaskParams(*snap.run, node, params)
	if err != nil {
		return err
	}
	identityDigest, err := digestOf(struct {
		Input     string
		Params    map[string]any
		ModelLock *domain.ReleaseRef
	}{Input: inputDigest, Params: params, ModelLock: modelLock})
	if err != nil {
		return err
	}
	id := pipelineTaskID(string(snap.run.ID), string(node), language, segmentID, identityDigest)
	if _, exists := snap.taskByID[id]; exists {
		return nil
	}
	freezeSubtitleOutput(snap, node, language, params)
	status := domain.TaskStatusQueued
	if !domain.RunAllowsDispatch(snap.run.Status) {
		status = domain.TaskStatusPaused
	}
	now := s.Now()
	stepID := stepIDForTask(kind, node)
	// InputDigest 走与手动 CreateTask 相同的口径，RetryTask 才能验证
	// “输入未变化”；流水线身份另用 pipelineTaskID 的输入摘要。
	inputHash, err := taskInputDigest(CreateTaskRequest{
		RunID:            string(snap.run.ID),
		Kind:             kind,
		NodeKey:          node,
		SegmentID:        segmentID,
		Language:         language,
		InputRevisionIDs: inputIDs,
		Params:           params,
		ModelLock:        modelLock,
	})
	if err != nil {
		return err
	}
	task := domain.Task{
		ID:               id,
		ProjectID:        snap.run.ProjectID,
		RunID:            snap.run.ID,
		Kind:             kind,
		NodeKey:          node,
		SegmentID:        segmentID,
		Language:         language,
		Status:           status,
		Progress:         0,
		ArtifactIDs:      []domain.ID{},
		InputRevisionIDs: inputIDs,
		InputDigest:      inputHash,
		Params:           params,
		ModelLock:        modelLock,
		CreatedAt:        now,
		UpdatedAt:        now,
		Steps: []domain.TaskStep{{
			StepID: stepID, Status: domain.StepStatusPending, AttemptNo: 1, Epoch: s.opts.ServerEpoch,
		}},
	}
	if err := tx.CreateTask(ctx, task); err != nil {
		return err
	}
	if err := tx.CreateAttempt(ctx, domain.Attempt{
		ID:        store.NewID("attempt"),
		TaskID:    task.ID,
		StepID:    stepID,
		AttemptNo: 1,
		Epoch:     s.opts.ServerEpoch,
		Status:    domain.StepStatusPending,
	}); err != nil {
		return err
	}
	if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
		Type:         "task.created",
		ProjectID:    task.ProjectID,
		TaskID:       task.ID,
		StateVersion: task.StateVersion,
		Data: map[string]any{
			"kind":       task.Kind,
			"node_key":   task.NodeKey,
			"segment_id": task.SegmentID,
			"language":   task.Language,
			"status":     task.Status,
		},
	}); err != nil {
		return err
	}
	eventType := "task.queued"
	if status == domain.TaskStatusPaused {
		eventType = "task.paused"
	}
	if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
		Type:         eventType,
		ProjectID:    task.ProjectID,
		TaskID:       task.ID,
		StateVersion: task.StateVersion,
	}); err != nil {
		return err
	}
	snap.tasks = append(snap.tasks, task)
	snap.taskByID[task.ID] = task
	return nil
}

// recomputeRunNodes 按 Task/审核/字幕轨事实聚合八节点状态。
func (s *Service) recomputeRunNodes(ctx context.Context, tx *store.Tx, runID domain.ID, events *[]domain.DomainEvent) error {
	snap, err := s.loadPipelineSnapshot(ctx, tx, runID)
	if err != nil {
		return err
	}
	if snap.run.Status.Terminal() {
		return nil
	}
	tpl, err := pipelineTemplateForRun(*snap.run)
	if err != nil {
		return err
	}
	if err := s.ensureCloneTimelineFailures(ctx, tx, snap, tpl, events); err != nil {
		return err
	}
	for _, node := range tpl.Nodes {
		status, err := s.computeNodeStatus(ctx, tx, snap, tpl, node)
		if err != nil {
			return err
		}
		if err := s.setNodeStatus(ctx, tx, snap.run, node, status, events); err != nil {
			return err
		}
	}
	if err := s.syncRunStatus(ctx, tx, snap.run, snap.tasks); err != nil {
		return err
	}
	return s.maybeCompleteRun(ctx, tx, runID, events)
}

// analyzeSkipped 报告模板的 02 分析节点是否在“没有历史任务”时以 SKIPPED 收敛。
//
// 提取工具的源语言由 Run 显式指定，04 会在本地完成媒体探测与音频准备；
// 真实的“分析”适配器尚未交付，02 若继续派发给 flashcli 门控桩，tool_extract
// 将永远无法到达 04。节点仍保留在 Run 节点集合中（UI 显示已跳过），
// full_pipeline 保持原有门控语义，直到分析适配器接入。
func analyzeSkipped(tpl pipelineTemplate) bool {
	return tpl.ID == templateToolExtract
}

func (s *Service) computeNodeStatus(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	tpl pipelineTemplate,
	node domain.PipelineNodeKey,
) (domain.NodeStatus, error) {
	switch node {
	case domain.NodeKeyImport:
		if len(snap.run.Config.InputRevisionIDs) == 0 {
			return domain.NodeStatusFailed, nil
		}
		return domain.NodeStatusDone, nil
	case domain.NodeKeyAnalyze, domain.NodeKeySeparate, domain.NodeKeyExtract, domain.NodeKeyErase:
		if node == domain.NodeKeyAnalyze && analyzeSkipped(tpl) {
			// 修复前创建的 02 任务可能仍是 running/failed；按其真实状态收敛，
			// 不把终态/执行中节点改写成 skipped（该迁移不被状态机允许）。
			if _, ok := latestNodeTask(snap.tasks, node, ""); !ok {
				return domain.NodeStatusSkipped, nil
			}
		}
		if node == domain.NodeKeySeparate && snap.run.Config.ReuseAudioTrackID != "" {
			return domain.NodeStatusReused, nil
		}
		if node == domain.NodeKeyExtract && sourceExtractReused(tpl, *snap.run) {
			return domain.NodeStatusReused, nil
		}
		if node == domain.NodeKeyExtract && tpl.ID == templateCustomPipeline &&
			!s.customExtractNeeded(snap, tpl) {
			return domain.NodeStatusSkipped, nil
		}
		language := ""
		if node == domain.NodeKeyExtract {
			language = snap.run.SourceLanguage
		}
		task, ok := latestNodeTask(snap.tasks, node, language)
		if !ok {
			return domain.NodeStatusPending, nil
		}
		return taskNodeStatus(task), nil
	case domain.NodeKeyTranslate:
		return s.computeTranslateStatus(snap, tpl), nil
	case domain.NodeKeyClone:
		return s.computeCloneStatus(ctx, tx, snap, tpl)
	case domain.NodeKeyMix:
		return s.computeMixStatus(ctx, tx, snap, tpl)
	default:
		return domain.NodeStatusPending, nil
	}
}

func (s *Service) computeTranslateStatus(snap *pipelineSnapshot, tpl pipelineTemplate) domain.NodeStatus {
	source := pickPipelineTrack(snap.tracks, snap.run.SourceLanguage, domain.TrackTypeTranscription)
	if source == nil {
		// 专属链路在所有目标译文都已导入时不需要原文轨（04 可跳过）；
		// 其他情况仍视为产物未正确回写，不能无限 pending。
		if tpl.ID != templateCustomPipeline || !s.customAllTranslationsImported(snap) {
			for _, predecessor := range tpl.Predecessors[domain.NodeKeyTranslate] {
				if nodeCompleted(snap.run, predecessor) || nodeTerminalFailure(snap, predecessor) {
					return domain.NodeStatusFailed
				}
			}
		}
	}
	reviewRequired := tpl.RequiresReview(domain.NodeKeyTranslate)
	outcomes := make([]domain.NodeStatus, 0, len(snap.run.Languages))
	for _, language := range snap.run.Languages {
		task, hasTask := latestNodeTask(snap.tasks, domain.NodeKeyTranslate, language)
		if tpl.ID == templateCustomPipeline && !hasTask {
			imported := false
			if snap.run.Config.ImportedTranslationTracks == nil {
				// 旧 Run 没有冻结 map，保持“最新 translation 轨即已导入”的兼容语义。
				track := pickPipelineTrack(snap.tracks, language, domain.TrackTypeTranslation)
				imported = track != nil && currentRevision(*track) != nil
			} else {
				imported = frozenTranslationRevision(snap, language) != nil
			}
			if imported {
				outcomes = append(outcomes, domain.NodeStatusDone)
				continue
			}
		}
		if hasTask {
			switch taskNodeStatus(task) {
			case domain.NodeStatusFailed:
				outcomes = append(outcomes, domain.NodeStatusFailed)
				continue
			case domain.NodeStatusRunning:
				outcomes = append(outcomes, domain.NodeStatusRunning)
				continue
			}
		}
		track := pickPipelineTrack(snap.tracks, language, domain.TrackTypeTranslation)
		if track != nil {
			if revision := currentRevision(*track); revision != nil {
				if !reviewRequired {
					outcomes = append(outcomes, domain.NodeStatusDone)
					continue
				}
				digest, err := domain.RevisionBindingDigest(*revision)
				if err == nil {
					approved, pending := snap.gateState(domain.ReviewTypeTranslation, language, digest)
					switch {
					case approved:
						outcomes = append(outcomes, domain.NodeStatusDone)
						continue
					case pending:
						outcomes = append(outcomes, domain.NodeStatusWaitingReview)
						continue
					}
				}
			}
		}
		if !hasTask {
			outcomes = append(outcomes, domain.NodeStatusPending)
			continue
		}
		if track == nil || !reviewRequired {
			// 任务成功但无译文轨（或 no-review 模板缺 current revision）是失败，不是待审核。
			outcomes = append(outcomes, domain.NodeStatusFailed)
			continue
		}
		// 任务成功但没有绑定当前 revision 的审核门槛：停在待审核，
		// 不能因为任务 SUCCEEDED 就把 06 标记完成。
		outcomes = append(outcomes, domain.NodeStatusWaitingReview)
	}
	return aggregateNodeStatus(outcomes, domain.NodeKeyTranslate)
}

// translationReady 报告某目标语言的译文是否可进入 07。
// full_pipeline 要求 06 审核放行；无 06 的工具模板（voice_clone）以导入轨为准。
func (s *Service) translationReady(snap *pipelineSnapshot, language string, tpl pipelineTemplate) bool {
	if !tpl.Has(domain.NodeKeyTranslate) || !tpl.RequiresReview(domain.NodeKeyTranslate) {
		return true
	}
	if tpl.ID == templateCustomPipeline {
		return s.customTranslationReady(snap, language)
	}
	return s.translationApproved(snap, language)
}

// customTranslationReady：导入译稿没有 ReviewGate，视为已确认；平台 06 生成的
// 译文仍必须有绑定当前 revision 的放行门槛。新 Run 只认创建时冻结的译稿；
// 旧 Run（imported map 为 nil）保留“最新轨”兼容逻辑。
func (s *Service) customTranslationReady(snap *pipelineSnapshot, language string) bool {
	if snap.run.Config.ImportedTranslationTracks == nil {
		track := pickPipelineTrack(snap.tracks, language, domain.TrackTypeTranslation)
		if track == nil || currentRevision(*track) == nil {
			return false
		}
		if _, hasTask := latestNodeTask(snap.tasks, domain.NodeKeyTranslate, language); hasTask {
			return s.translationApproved(snap, language)
		}
		return true
	}
	if _, hasTask := latestNodeTask(snap.tasks, domain.NodeKeyTranslate, language); hasTask {
		return s.translationApproved(snap, language)
	}
	return frozenTranslationRevision(snap, language) != nil
}

// frozenTranslationRevision 只返回 Run 创建时冻结的译稿，不回退到后来保存的 current。
func frozenTranslationRevision(snap *pipelineSnapshot, language string) *domain.SubtitleRevision {
	ref, ok := snap.run.Config.ImportedTranslationTracks[language]
	if !ok {
		return nil
	}
	track := findTrackByID(snap.tracks, ref.TrackID)
	if track == nil || track.Language != language || track.Type != domain.TrackTypeTranslation {
		return nil
	}
	for i := range track.Revisions {
		if track.Revisions[i].ID == ref.RevisionID {
			return &track.Revisions[i]
		}
	}
	return nil
}

// translationRevisionForRun：新 custom Run 在 06 Task 不存在时只使用冻结译稿；
// 有 06 Task 时使用 06 生成的最新轨。其他模板/旧 Run 保持最新轨语义。
func translationRevisionForRun(snap *pipelineSnapshot, language string, tpl pipelineTemplate) *domain.SubtitleRevision {
	if tpl.ID == templateCustomPipeline && snap.run.Config.ImportedTranslationTracks != nil {
		if _, hasTask := latestNodeTask(snap.tasks, domain.NodeKeyTranslate, language); !hasTask {
			return frozenTranslationRevision(snap, language)
		}
	}
	track := pickPipelineTrack(snap.tracks, language, domain.TrackTypeTranslation)
	if track == nil {
		return nil
	}
	return currentRevision(*track)
}

// customExtractNeeded：custom 的 04 只在需要生成原文时执行。擦除必须 OCR；
// 已绑定原文轨或所有目标译文都已导入时，04 不再需要。
func (s *Service) customExtractNeeded(snap *pipelineSnapshot, tpl pipelineTemplate) bool {
	if tpl.ID != templateCustomPipeline || !tpl.Has(domain.NodeKeyExtract) {
		return false
	}
	if tpl.Has(domain.NodeKeyErase) {
		return true
	}
	if snap.run.Config.ReuseSubtitleTrackID != "" {
		return false
	}
	if !tpl.Has(domain.NodeKeyTranslate) {
		return false
	}
	for _, language := range snap.run.Languages {
		if translationRevisionForRun(snap, language, tpl) == nil {
			return true
		}
	}
	return false
}

func (s *Service) customAllTranslationsImported(snap *pipelineSnapshot) bool {
	if len(snap.run.Languages) == 0 {
		return false
	}
	if snap.run.Config.ImportedTranslationTracks == nil {
		for _, language := range snap.run.Languages {
			track := pickPipelineTrack(snap.tracks, language, domain.TrackTypeTranslation)
			if track == nil || currentRevision(*track) == nil {
				return false
			}
		}
		return true
	}
	for _, language := range snap.run.Languages {
		if frozenTranslationRevision(snap, language) == nil {
			return false
		}
	}
	return true
}

func (s *Service) customTranslationImported(snap *pipelineSnapshot, language string) bool {
	if _, hasTask := latestNodeTask(snap.tasks, domain.NodeKeyTranslate, language); hasTask {
		return false
	}
	if snap.run.Config.ImportedTranslationTracks == nil {
		track := pickPipelineTrack(snap.tracks, language, domain.TrackTypeTranslation)
		return track != nil && currentRevision(*track) != nil
	}
	return frozenTranslationRevision(snap, language) != nil
}

func (s *Service) computeCloneStatus(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	tpl pipelineTemplate,
) (domain.NodeStatus, error) {
	outcomes := make([]domain.NodeStatus, 0)
	for _, language := range snap.run.Languages {
		if !s.translationReady(snap, language, tpl) {
			if languageTranslateFailed(snap, language) {
				outcomes = append(outcomes, domain.NodeStatusFailed)
			} else {
				outcomes = append(outcomes, domain.NodeStatusPending)
			}
			continue
		}
		revision := translationRevisionForRun(snap, language, tpl)
		if revision == nil {
			if (tpl.ID == templateVoiceClone || tpl.ID == templateCustomPipeline) &&
				nodeCompleted(snap.run, domain.NodeKeyImport) {
				outcomes = append(outcomes, domain.NodeStatusFailed)
			} else {
				outcomes = append(outcomes, domain.NodeStatusPending)
			}
			continue
		}
		if tpl.Has(domain.NodeKeySeparate) && !nodeCompleted(snap.run, domain.NodeKeySeparate) {
			outcomes = append(outcomes, domain.NodeStatusPending)
			continue
		}
		segmentOutcomes, err := s.cloneTaskOutcomes(ctx, tx, snap, language, revision)
		if err != nil {
			return domain.NodeStatusPending, err
		}
		if cloneAudioAssemblyRequired(*snap.run) &&
			aggregateNodeStatus(segmentOutcomes, domain.NodeKeyClone) == domain.NodeStatusDone {
			if task, ok := latestCloneAudioAssemblyTask(snap.tasks, language); ok {
				outcomes = append(outcomes, taskNodeStatus(task))
			} else {
				outcomes = append(outcomes, domain.NodeStatusPending)
			}
		}
		outcomes = append(outcomes, segmentOutcomes...)
	}
	return aggregateNodeStatus(outcomes, domain.NodeKeyClone), nil
}

func (s *Service) computeMixStatus(ctx context.Context, tx *store.Tx, snap *pipelineSnapshot, tpl pipelineTemplate) (domain.NodeStatus, error) {
	if !mixDependenciesReady(snap, tpl) {
		return domain.NodeStatusPending, nil
	}
	outcomes := make([]domain.NodeStatus, 0, len(snap.run.Languages))
	for _, language := range snap.run.Languages {
		if task, ok := latestNodeTask(snap.tasks, domain.NodeKeyMix, language); ok &&
			taskNodeStatus(task) == domain.NodeStatusFailed {
			outcomes = append(outcomes, domain.NodeStatusFailed)
			continue
		}
		if !s.translationReady(snap, language, tpl) {
			if languageTranslateFailed(snap, language) {
				outcomes = append(outcomes, domain.NodeStatusFailed)
			} else {
				outcomes = append(outcomes, domain.NodeStatusPending)
			}
			continue
		}
		revision := translationRevisionForRun(snap, language, tpl)
		if revision == nil {
			outcomes = append(outcomes, domain.NodeStatusPending)
			continue
		}
		if tpl.Has(domain.NodeKeyClone) {
			outcome, err := s.cloneTaskOutcomes(ctx, tx, snap, language, revision)
			if err != nil {
				return domain.NodeStatusPending, err
			}
			status := aggregateNodeStatus(outcome, domain.NodeKeyClone)
			if status != domain.NodeStatusDone {
				outcomes = append(outcomes, status)
				continue
			}
		}
		task, ok := latestNodeTask(snap.tasks, domain.NodeKeyMix, language)
		if !ok {
			outcomes = append(outcomes, domain.NodeStatusPending)
			continue
		}
		if taskNodeStatus(task) != domain.NodeStatusDone {
			outcomes = append(outcomes, taskNodeStatus(task))
			continue
		}
		if !mixTaskMatchesRevision(task, revision) {
			// 08 仍冻结在旧 revision：编辑后必须重新合成，旧交付审核不能沿用。
			outcomes = append(outcomes, domain.NodeStatusPending)
			continue
		}
		digest, err := domain.NodeReviewDigest(task.RunID, task.NodeKey, task.Language, task.ID,
			task.ArtifactIDs, task.InputDigest)
		if err != nil {
			return domain.NodeStatusPending, err
		}
		approved, pending := snap.gateState(domain.ReviewTypeDelivery, language, digest)
		switch {
		case approved:
			outcomes = append(outcomes, domain.NodeStatusDone)
		case pending:
			outcomes = append(outcomes, domain.NodeStatusWaitingReview)
		default:
			outcomes = append(outcomes, domain.NodeStatusWaitingReview)
		}
	}
	return aggregateNodeStatus(outcomes, domain.NodeKeyMix), nil
}

func (s *Service) cloneTaskOutcomes(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	language string,
	revision *domain.SubtitleRevision,
) ([]domain.NodeStatus, error) {
	if revision == nil {
		return []domain.NodeStatus{domain.NodeStatusPending}, nil
	}
	work, err := s.segmentWorkForRevision(ctx, tx, snap, *revision, language)
	if err != nil {
		if isCloneTimelineFailure(err) {
			// 当前译稿已无法按冻结媒体执行：07 先按失败单元聚合，但同语言
			// 仍排队的冻结 07 任务必须继续排空；只有这些单元全部收敛后
			// aggregateNodeStatus 才会把节点标为 failed。终态旧结果不替代
			// 当前输入，不能把节点提前判为 done 或跳过失败事实。
			outcomes := []domain.NodeStatus{domain.NodeStatusFailed}
			for _, task := range snap.tasks {
				if task.Kind != domain.TaskKindSegment || task.NodeKey != domain.NodeKeyClone ||
					task.Language != language || task.Status.Terminal() {
					continue
				}
				outcomes = append(outcomes, taskNodeStatus(task))
			}
			return outcomes, nil
		}
		return nil, err
	}
	outcomes := make([]domain.NodeStatus, 0, len(revision.Segments))
	for _, segment := range revision.Segments {
		task, ok := currentSegmentTask(snap.tasks, language, segment.ID, work[segment.ID].CacheKey)
		if !ok {
			outcomes = append(outcomes, domain.NodeStatusPending)
			continue
		}
		outcomes = append(outcomes, taskNodeStatus(task))
	}
	return outcomes, nil
}

func (s *Service) cloneComplete(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	language string,
	revision *domain.SubtitleRevision,
) (bool, error) {
	outcomes, err := s.cloneTaskOutcomes(ctx, tx, snap, language, revision)
	if err != nil {
		return false, err
	}
	return aggregateNodeStatus(outcomes, domain.NodeKeyClone) == domain.NodeStatusDone, nil
}

func languageTranslateFailed(snap *pipelineSnapshot, language string) bool {
	task, ok := latestNodeTask(snap.tasks, domain.NodeKeyTranslate, language)
	return ok && task.Status == domain.TaskStatusFailed
}

// mixTaskMatchesRevision 报告 08 任务是否冻结在当前 revision。
//
// 任务创建时 `_core` 写入 subtitle_revision_id/subtitle_digest；编辑后 revision
// ID（以及文本摘要）变化即视为不匹配，旧 08 的交付放行不能覆盖新内容。
// 没有 `_core` 绑定信息的旧任务视为匹配，保持升级前行为。
func mixTaskMatchesRevision(task domain.Task, revision *domain.SubtitleRevision) bool {
	if revision == nil {
		return true
	}
	core, ok := task.Params["_core"].(map[string]any)
	if !ok {
		return true
	}
	revisionID, _ := core["subtitle_revision_id"].(string)
	digest, _ := core["subtitle_digest"].(string)
	if revisionID == "" && digest == "" {
		return true
	}
	if revisionID != "" && revisionID != string(revision.ID) {
		return false
	}
	if digest != "" && digest != mixSubtitleDigest(*revision) {
		return false
	}
	return true
}

// mixSubtitleDigest 是 08 `_core.subtitle_digest` 的同一口径（renderSRT 的 SHA-256）。
func mixSubtitleDigest(revision domain.SubtitleRevision) string {
	sum := sha256.Sum256([]byte(renderSRT(revision.Segments)))
	return hex.EncodeToString(sum[:])
}

func (s *Service) translationApproved(snap *pipelineSnapshot, language string) bool {
	track := pickPipelineTrack(snap.tracks, language, domain.TrackTypeTranslation)
	if track == nil {
		return false
	}
	revision := currentRevision(*track)
	if revision == nil {
		return false
	}
	digest, err := domain.RevisionBindingDigest(*revision)
	if err != nil {
		return false
	}
	approved, _ := snap.gateState(domain.ReviewTypeTranslation, language, digest)
	return approved
}

func (s *Service) translationPending(snap *pipelineSnapshot, language string) bool {
	track := pickPipelineTrack(snap.tracks, language, domain.TrackTypeTranslation)
	if track == nil {
		return false
	}
	revision := currentRevision(*track)
	if revision == nil {
		return false
	}
	digest, err := domain.RevisionBindingDigest(*revision)
	if err != nil {
		return false
	}
	_, pending := snap.gateState(domain.ReviewTypeTranslation, language, digest)
	return pending
}

func (s *Service) selectionDigest(ctx context.Context, tx *store.Tx, segments []domain.Segment, language string) (string, error) {
	ids := make([]string, 0)
	for _, segment := range segments {
		selection, err := tx.GetCandidateSelection(ctx, segment.ID, language)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				continue
			}
			return "", err
		}
		if selection.Revision != segment.ContentRevision {
			// 旧 revision 的选择不能作为当前混音输入。
			continue
		}
		ids = append(ids, selection.CandidateID)
	}
	sort.Strings(ids)
	return digestOf(struct {
		Selections []string
	}{Selections: ids})
}

// setNodeStatus 按状态机允许的路径把节点推进到目标状态。
//
// 聚合结果可能跳过中间状态（如 pending 直接到 waiting_review），这里按合法
// 迁移补齐；但不会让 waiting_review 退回 running，也不改动终态节点。
func (s *Service) setNodeStatus(
	ctx context.Context,
	tx *store.Tx,
	run *domain.WorkflowRun,
	key domain.PipelineNodeKey,
	target domain.NodeStatus,
	events *[]domain.DomainEvent,
) error {
	current := nodeStatusOf(*run, key)
	if current == target {
		return nil
	}
	tpl, err := pipelineTemplateForRun(*run)
	if err != nil {
		return err
	}
	path := nodeTransitionPathWithReview(current, target, key, tpl.RequiresReview(key))
	for _, step := range path {
		if err := tx.UpdateRunNodeWithReview(ctx, run.ID, key, step, nodeProgress(step), "", tpl.RequiresReview(key)); err != nil {
			return err
		}
		for i := range run.Nodes {
			if run.Nodes[i].Key == key {
				run.Nodes[i].Status = step
				run.Nodes[i].Progress = nodeProgress(step)
			}
		}
		if step == domain.NodeStatusWaitingReview {
			if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
				Type:      "workflow.waiting_review",
				ProjectID: run.ProjectID,
				Data: map[string]any{
					"run_id":   run.ID,
					"node_key": key,
				},
			}); err != nil {
				return err
			}
		}
		if step == domain.NodeStatusFailed {
			if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
				Type:      "workflow.node_failed",
				ProjectID: run.ProjectID,
				Data: map[string]any{
					"run_id":   run.ID,
					"node_key": key,
				},
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func nodeTransitionPath(from, to domain.NodeStatus, key domain.PipelineNodeKey) []domain.NodeStatus {
	return nodeTransitionPathWithReview(from, to, key, domain.NodeRequiresReview(key))
}

func nodeTransitionPathWithReview(
	from, to domain.NodeStatus,
	key domain.PipelineNodeKey,
	requiresReview bool,
) []domain.NodeStatus {
	if from == to {
		return nil
	}
	// failed 是终态，但显式重试或句级重抽成功后，聚合事实会再次给出
	// running/waiting_review/done；按 failed → running → … 的合法路径收敛，
	// 否则节点会永久停在 failed，Run 也无法再完成。
	if from == domain.NodeStatusFailed {
		switch to {
		case domain.NodeStatusRunning:
			return []domain.NodeStatus{domain.NodeStatusRunning}
		case domain.NodeStatusWaitingReview:
			if !requiresReview {
				return nil
			}
			return []domain.NodeStatus{domain.NodeStatusRunning, domain.NodeStatusWaitingReview}
		case domain.NodeStatusDone:
			if requiresReview {
				return []domain.NodeStatus{domain.NodeStatusRunning, domain.NodeStatusWaitingReview, domain.NodeStatusDone}
			}
			return []domain.NodeStatus{domain.NodeStatusRunning, domain.NodeStatusDone}
		}
		return nil
	}
	if from.Terminal() {
		return nil
	}
	switch from {
	case domain.NodeStatusPending:
		switch to {
		case domain.NodeStatusRunning:
			return []domain.NodeStatus{domain.NodeStatusRunning}
		case domain.NodeStatusFailed:
			return []domain.NodeStatus{domain.NodeStatusFailed}
		case domain.NodeStatusReused, domain.NodeStatusSkipped:
			return []domain.NodeStatus{to}
		case domain.NodeStatusWaitingReview:
			return []domain.NodeStatus{domain.NodeStatusRunning, domain.NodeStatusWaitingReview}
		case domain.NodeStatusDone:
			if requiresReview {
				return []domain.NodeStatus{domain.NodeStatusRunning, domain.NodeStatusWaitingReview, domain.NodeStatusDone}
			}
			return []domain.NodeStatus{domain.NodeStatusRunning, domain.NodeStatusDone}
		}
	case domain.NodeStatusRunning:
		switch to {
		case domain.NodeStatusWaitingReview:
			return []domain.NodeStatus{domain.NodeStatusWaitingReview}
		case domain.NodeStatusDone:
			if requiresReview {
				return []domain.NodeStatus{domain.NodeStatusWaitingReview, domain.NodeStatusDone}
			}
			return []domain.NodeStatus{domain.NodeStatusDone}
		case domain.NodeStatusFailed:
			return []domain.NodeStatus{domain.NodeStatusFailed}
		}
	case domain.NodeStatusWaitingReview:
		switch to {
		case domain.NodeStatusDone:
			return []domain.NodeStatus{domain.NodeStatusDone}
		case domain.NodeStatusFailed:
			return []domain.NodeStatus{domain.NodeStatusFailed}
		}
	}
	return nil
}

func nodeProgress(status domain.NodeStatus) int {
	switch status {
	case domain.NodeStatusDone, domain.NodeStatusWaitingReview, domain.NodeStatusFailed,
		domain.NodeStatusReused, domain.NodeStatusSkipped:
		return 100
	default:
		return 0
	}
}

func (s *Service) syncRunStatus(ctx context.Context, tx *store.Tx, run *domain.WorkflowRun, tasks []domain.Task) error {
	if run.Status.Terminal() || domain.RunIsPaused(run.Status) {
		return nil
	}
	active := false
	for _, task := range tasks {
		if taskActive(task.Status) {
			active = true
			break
		}
	}
	waitingReview := false
	for _, node := range run.Nodes {
		if node.Status == domain.NodeStatusWaitingReview {
			waitingReview = true
			break
		}
	}
	var path []domain.RunStatus
	switch {
	case active:
		if run.Status == domain.RunStatusQueued || run.Status == domain.RunStatusWaitingReview {
			path = []domain.RunStatus{domain.RunStatusRunning}
		}
	case waitingReview:
		switch run.Status {
		case domain.RunStatusQueued:
			path = []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusWaitingReview}
		case domain.RunStatusRunning:
			path = []domain.RunStatus{domain.RunStatusWaitingReview}
		}
	}
	for _, target := range path {
		updated, err := tx.UpdateRunStatus(ctx, run.ID, target, run.StateVersion, s.Now())
		if err != nil {
			return err
		}
		run.Status = updated.Status
		run.StateVersion = updated.StateVersion
	}
	return nil
}

func taskActive(status domain.TaskStatus) bool {
	switch status {
	case domain.TaskStatusQueued, domain.TaskStatusWaitingResource, domain.TaskStatusRunning,
		domain.TaskStatusPauseRequested, domain.TaskStatusCancelRequested, domain.TaskStatusInterrupted:
		return true
	default:
		return false
	}
}

func taskNodeStatus(task domain.Task) domain.NodeStatus {
	switch task.Status {
	case domain.TaskStatusSucceeded:
		return domain.NodeStatusDone
	case domain.TaskStatusFailed:
		return domain.NodeStatusFailed
	case domain.TaskStatusQueued, domain.TaskStatusWaitingResource, domain.TaskStatusRunning,
		domain.TaskStatusPauseRequested, domain.TaskStatusPaused, domain.TaskStatusCancelRequested,
		domain.TaskStatusInterrupted:
		return domain.NodeStatusRunning
	default:
		return domain.NodeStatusPending
	}
}

func aggregateNodeStatus(outcomes []domain.NodeStatus, key domain.PipelineNodeKey) domain.NodeStatus {
	if len(outcomes) == 0 {
		return domain.NodeStatusPending
	}
	anyRunning := false
	anyWaiting := false
	anyPending := false
	anyFailed := false
	for _, outcome := range outcomes {
		switch outcome {
		case domain.NodeStatusFailed:
			anyFailed = true
		case domain.NodeStatusRunning:
			anyRunning = true
		case domain.NodeStatusWaitingReview:
			anyWaiting = true
		case domain.NodeStatusDone, domain.NodeStatusReused, domain.NodeStatusSkipped:
		default:
			anyPending = true
		}
	}
	// 多语言节点只有“所有单元都进入终态且至少一个失败”时才标记 failed；
	// 其他语言仍在运行/待审核/待开始时保持非终态，避免 Run 被提前收敛。
	switch {
	case anyRunning:
		return domain.NodeStatusRunning
	case anyWaiting:
		return domain.NodeStatusWaitingReview
	case anyPending:
		return domain.NodeStatusPending
	case anyFailed:
		return domain.NodeStatusFailed
	default:
		return domain.NodeStatusDone
	}
}

func nodeCompleted(run *domain.WorkflowRun, key domain.PipelineNodeKey) bool {
	switch nodeStatusOf(*run, key) {
	case domain.NodeStatusDone, domain.NodeStatusReused, domain.NodeStatusSkipped:
		return true
	default:
		return false
	}
}

func latestNodeTask(tasks []domain.Task, node domain.PipelineNodeKey, language string) (domain.Task, bool) {
	var latest domain.Task
	found := false
	for _, task := range tasks {
		if task.Kind != domain.TaskKindNode || task.NodeKey != node || task.Language != language {
			continue
		}
		if !found || task.CreatedAt.After(latest.CreatedAt) ||
			(task.CreatedAt.Equal(latest.CreatedAt) && task.ID > latest.ID) {
			latest = task
			found = true
		}
	}
	return latest, found
}

func latestSucceededArtifacts(tasks []domain.Task, node domain.PipelineNodeKey, language string) []string {
	task, ok := latestNodeTask(tasks, node, language)
	if !ok || task.Status != domain.TaskStatusSucceeded {
		return nil
	}
	out := make([]string, 0, len(task.ArtifactIDs))
	for _, id := range task.ArtifactIDs {
		out = append(out, string(id))
	}
	return out
}

func latestSegmentTask(tasks []domain.Task, language, segmentID string) (domain.Task, bool) {
	var latest domain.Task
	found := false
	for _, task := range tasks {
		if task.Kind != domain.TaskKindSegment || task.NodeKey != domain.NodeKeyClone ||
			task.Language != language || task.SegmentID != segmentID {
			continue
		}
		if !found || task.CreatedAt.After(latest.CreatedAt) ||
			(task.CreatedAt.Equal(latest.CreatedAt) && task.ID > latest.ID) {
			latest = task
			found = true
		}
	}
	return latest, found
}

func taskContentRevision(task domain.Task) int {
	raw, ok := task.Params["content_revision"]
	if !ok {
		return 0
	}
	switch value := raw.(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	default:
		return 0
	}
}

func digestOfStrings(values []string) string {
	digest, _ := digestOf(struct {
		Values []string
	}{Values: values})
	return digest
}

// sourceTranscriptionRevision 返回 06 应使用的原文 revision：显式复用的轨优先，
// 避免擦除 OCR 新轨或后导入轨改变已绑定的输入。
// sourceExtractReused：专属链路的擦除必须保留 04 OCR；其余模板保持既有
// “绑定原文轨即复用 04”的语义。
func sourceExtractReused(tpl pipelineTemplate, run domain.WorkflowRun) bool {
	if run.Config.ReuseSubtitleTrackID == "" {
		return false
	}
	if tpl.ID == templateCustomPipeline && tpl.Has(domain.NodeKeyErase) {
		return false
	}
	return true
}

func sourceTranscriptionRevision(snap *pipelineSnapshot) *domain.SubtitleRevision {
	if id := snap.run.Config.ReuseSubtitleRevisionID; id != "" {
		track := findTrackByID(snap.tracks, snap.run.Config.ReuseSubtitleTrackID)
		if track == nil {
			return nil
		}
		for i := range track.Revisions {
			if track.Revisions[i].ID == id {
				return &track.Revisions[i]
			}
		}
		return nil
	}
	if id := snap.run.Config.ReuseSubtitleTrackID; id != "" {
		if track := findTrackByID(snap.tracks, id); track != nil {
			if revision := currentRevision(*track); revision != nil {
				return revision
			}
		}
	}
	track := pickPipelineTrack(snap.tracks, snap.run.SourceLanguage, domain.TrackTypeTranscription)
	if track == nil {
		return nil
	}
	return currentRevision(*track)
}

func findRevisionByID(tracks []domain.SubtitleTrack, id domain.ID) *domain.SubtitleRevision {
	for i := range tracks {
		for j := range tracks[i].Revisions {
			if tracks[i].Revisions[j].ID == id {
				return &tracks[i].Revisions[j]
			}
		}
	}
	return nil
}

func pickPipelineTrack(tracks []domain.SubtitleTrack, language string, trackType domain.TrackType) *domain.SubtitleTrack {
	var picked *domain.SubtitleTrack
	for i := range tracks {
		track := &tracks[i]
		if track.Language != language || track.Type != trackType || track.CurrentRevisionID == "" {
			continue
		}
		if picked == nil || track.UpdatedAt.After(picked.UpdatedAt) ||
			(track.UpdatedAt.Equal(picked.UpdatedAt) && track.ID > picked.ID) {
			picked = track
		}
	}
	return picked
}

func findTrackByID(tracks []domain.SubtitleTrack, id domain.ID) *domain.SubtitleTrack {
	for i := range tracks {
		if tracks[i].ID == id {
			return &tracks[i]
		}
	}
	return nil
}

// gateState 返回绑定指定摘要的审核门槛状态；空语言门槛按单语回退匹配。
func (p *pipelineSnapshot) gateState(typ domain.ReviewType, language, digest string) (approved, pending bool) {
	for _, gate := range p.gates {
		if gate.Type != typ {
			continue
		}
		if gate.Language != "" && language != "" && gate.Language != language {
			continue
		}
		if gate.RevisionDigest != digest {
			continue
		}
		switch gate.Status {
		case domain.ReviewStatusApproved:
			approved = true
		case domain.ReviewStatusPending:
			pending = true
		}
	}
	return approved, pending
}
