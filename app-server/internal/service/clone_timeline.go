package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// isCloneTimelineFailure 区分“当前译稿无法按冻结媒体执行”与真实基础设施错误：
// 前者只让 07/08 以可读失败收敛，不能回滚已完成任务的终态事务；后者继续传播。
func isCloneTimelineFailure(err error) bool {
	return errors.Is(err, domain.ErrCloneTimelineOutOfRange) ||
		errors.Is(err, domain.ErrCloneTimelineUnknownDuration)
}

// cloneMediaDuration 返回已冻结媒体的可靠时长。
// 缺失/非正时长不能假定通过，交由调用方给出可读阻断。
func cloneMediaDuration(asset domain.Asset) (domain.Millis, error) {
	if asset.Meta == nil || asset.Meta.DurationMs <= 0 {
		return 0, domain.ErrCloneTimelineUnknownDuration
	}
	return asset.Meta.DurationMs, nil
}

// cloneTimelinePreflightIssues 在 tool_voice_clone 预检/创建时，按实际选中的
// 声源资产与原文/译文轨当前 revision 校验时间窗。07 生成与派发准备复用同一
// 领域规则，避免只显示开关而不生效。
func (s *Service) cloneTimelinePreflightIssues(
	ctx context.Context,
	q preflightQuerier,
	projectID domain.ID,
	in CreateWorkflowRunInput,
	tracks []domain.SubtitleTrack,
) ([]domain.PreflightIssue, error) {
	if !boolValue(in.ValidateTimestamps) || len(in.InputRevisionIDs) == 0 {
		return nil, nil
	}
	// 与 cloneReferenceAsset 同序：显式复用音轨优先于 Run 输入素材。
	sourceID := in.InputRevisionIDs[0]
	if in.ReuseAudioTrackID != "" {
		sourceID = in.ReuseAudioTrackID
	}
	asset, err := q.GetAsset(ctx, sourceID)
	if err != nil || asset.ProjectID != projectID {
		// 输入资产问题由既有 INPUT_ASSET_INVALID/REUSE_ASSET_INVALID 统一报告。
		return nil, nil
	}
	duration, err := cloneMediaDuration(asset)
	if err != nil {
		return []domain.PreflightIssue{{
			Severity:   domain.PreflightBlocker,
			Code:       "CLONE_TIMELINE_UNKNOWN_DURATION",
			MessageKey: "preflight.clone_timeline_unknown_duration",
			ActionIDs:  []domain.ID{},
		}}, nil
	}
	var sourceTrack *domain.SubtitleTrack
	if in.ReuseSubtitleTrackID != "" {
		sourceTrack = findTrackByID(tracks, in.ReuseSubtitleTrackID)
	} else {
		sourceTrack = pickPipelineTrack(tracks, in.SourceLanguage, domain.TrackTypeTranscription)
	}
	revisions := make([]*domain.SubtitleRevision, 0, len(in.TargetLanguages)+1)
	if sourceTrack != nil {
		revisions = append(revisions, currentRevision(*sourceTrack))
	}
	for _, language := range in.TargetLanguages {
		track := pickPipelineTrack(tracks, language, domain.TrackTypeTranslation)
		if track != nil {
			revisions = append(revisions, currentRevision(*track))
		}
	}
	for _, revision := range revisions {
		if revision == nil {
			continue
		}
		if err := domain.ValidateMediaTimeline(duration, domain.SegmentTimelineWindows(revision.Segments)); err != nil {
			return []domain.PreflightIssue{{
				Severity:   domain.PreflightBlocker,
				Code:       "CLONE_TIMELINE_OUT_OF_RANGE",
				MessageKey: "preflight.clone_timeline_out_of_range",
				ActionIDs:  []domain.ID{},
			}}, nil
		}
	}
	return nil, nil
}

// validateFrozenCloneTimeline 在派发准备阶段只按 Task 冻结输入复核 07 时间窗：
// 已冻结 revision 的 SegmentID、voice_source_asset_id 与参考 start_ms/end_ms，
// 不回退项目最新轨。旧 Task 缺少选项时为 false，不新增门槛；显式 true 但证据
// 不足时安全拒绝。
func (s *Service) validateFrozenCloneTimeline(
	ctx context.Context,
	task domain.Task,
	params map[string]any,
	assets map[domain.ID]domain.Asset,
) error {
	if task.NodeKey != domain.NodeKeyClone || !taskBoolParam(params, "validate_timestamps") {
		return nil
	}
	sourceID, _ := params["voice_source_asset_id"].(string)
	if sourceID == "" {
		return invalidRequestf("frozen voice source is missing")
	}
	source, ok := assets[domain.ID(sourceID)]
	if !ok {
		return invalidRequestf("frozen voice source is missing")
	}
	duration, err := cloneMediaDuration(source)
	if err != nil {
		return err
	}
	revision, err := s.frozenCloneRevision(ctx, task)
	if err != nil {
		return err
	}
	track, err := s.store.GetSubtitleTrack(ctx, revision.TrackID)
	if err != nil {
		return mapReference(err, "subtitle track")
	}
	if track.ProjectID != task.ProjectID {
		return invalidRequestf("frozen clone revision belongs to another project")
	}
	// 冻结列表里第一个可读 revision 必须是该任务语言的 translation 轨；不能
	// 用原文/其他语种 revision 凑出同名句段而跳过目标译文时间窗。
	if track.Type != domain.TrackTypeTranslation || track.Language != task.Language {
		return invalidRequestf("frozen clone revision is not the task's translation track")
	}
	if op, _ := params["op"].(string); op == "script" {
		windows := make([]domain.TimelineWindow, 0, len(revision.Segments))
		for _, segment := range revision.Segments {
			windows = append(windows, domain.TimelineWindow{StartMs: segment.StartMs, EndMs: segment.EndMs})
		}
		return domain.ValidateMediaTimeline(duration, windows)
	}
	var segment *domain.Segment
	for i := range revision.Segments {
		if revision.Segments[i].ID == task.SegmentID {
			segment = &revision.Segments[i]
			break
		}
	}
	if segment == nil {
		return invalidRequestf("frozen clone segment is missing")
	}
	start, startOK := taskMillisParam(params, "start_ms")
	end, endOK := taskMillisParam(params, "end_ms")
	if !startOK || !endOK {
		return invalidRequestf("frozen clone reference window is missing")
	}
	return domain.ValidateMediaTimeline(duration, []domain.TimelineWindow{
		{StartMs: segment.StartMs, EndMs: segment.EndMs},
		{StartMs: start, EndMs: end},
	})
}

// frozenCloneRevision 只认 Task 冻结的 revision ID；不读取轨道最新 revision。
func (s *Service) frozenCloneRevision(ctx context.Context, task domain.Task) (domain.SubtitleRevision, error) {
	for _, id := range task.InputRevisionIDs {
		revision, err := s.store.GetSubtitleRevision(ctx, id)
		if err == nil {
			return revision, nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return domain.SubtitleRevision{}, err
		}
	}
	return domain.SubtitleRevision{}, invalidRequestf("frozen clone subtitle revision is missing")
}

// ensureCloneTimelineFailures 在状态聚合前记录当前译稿的媒体时间轴失败，
// 使 07 的可读失败与已完成任务同事务保留，而不是把成功提交拖回滚。
func (s *Service) ensureCloneTimelineFailures(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	tpl pipelineTemplate,
	events *[]domain.DomainEvent,
) error {
	if !snap.run.Config.ValidateTimestamps || !tpl.Has(domain.NodeKeyClone) {
		return nil
	}
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
		if _, err := s.segmentWorkForRevision(ctx, tx, snap, *revision, language); err != nil {
			if !isCloneTimelineFailure(err) {
				// 非时间轴问题不是本次失败归属范围，原样交给调用方传播，
				// 不能依赖后续第二次调用才发现。
				return err
			}
			if failErr := s.ensureCloneTimelineFailure(ctx, tx, snap, language, revision, err, events); failErr != nil {
				return failErr
			}
		}
	}
	return nil
}

// ensureCloneTimelineFailure 为无法按冻结媒体执行的当前译稿登记一次可读失败。
//
// 该失败属于 07 自身，不能把已完成 07/上游任务的终态事务一起回滚；同一
// revision 的确定性身份保证重复推进幂等。修复译稿后新的推进按合法 revision
// 正常建任务，不删除历史失败记录。
func (s *Service) ensureCloneTimelineFailure(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	language string,
	revision *domain.SubtitleRevision,
	cause error,
	events *[]domain.DomainEvent,
) error {
	if revision == nil {
		return nil
	}
	inputIDs := []string{revision.ID}
	inputHash, err := taskInputDigest(CreateTaskRequest{
		RunID:            snap.run.ID,
		Kind:             domain.TaskKindNode,
		NodeKey:          domain.NodeKeyClone,
		Language:         language,
		InputRevisionIDs: inputIDs,
	})
	if err != nil {
		return err
	}
	id := pipelineTaskID(snap.run.ID, string(domain.NodeKeyClone), language, "", inputHash)
	if _, exists := snap.taskByID[id]; exists {
		return nil
	}
	messageKey := "task.clone_timeline_out_of_range"
	if errors.Is(cause, domain.ErrCloneTimelineUnknownDuration) {
		messageKey = "task.clone_timeline_unknown_duration"
	}
	now := s.Now()
	task := domain.Task{
		ID:               id,
		ProjectID:        snap.run.ProjectID,
		RunID:            snap.run.ID,
		Kind:             domain.TaskKindNode,
		NodeKey:          domain.NodeKeyClone,
		Language:         language,
		Status:           domain.TaskStatusFailed,
		Progress:         0,
		ArtifactIDs:      []domain.ID{},
		InputRevisionIDs: []domain.ID{revision.ID},
		InputDigest:      inputHash,
		Error: &domain.ErrorInfo{
			Code:       domain.ErrorCodeInvalidRequest,
			MessageKey: messageKey,
			Retryable:  false,
			ActionIDs:  []domain.ID{},
		},
		CreatedAt: now,
		UpdatedAt: now,
		Steps: []domain.TaskStep{{
			StepID: stepIDForTask(domain.TaskKindNode, domain.NodeKeyClone),
			Status: domain.StepStatusFailed, AttemptNo: 1, Epoch: s.opts.ServerEpoch,
		}},
	}
	if err := tx.CreateTask(ctx, task); err != nil {
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
	if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
		Type:         "task.failed",
		ProjectID:    task.ProjectID,
		TaskID:       task.ID,
		StateVersion: task.StateVersion,
		Data: map[string]any{
			"code":        task.Error.Code,
			"message_key": task.Error.MessageKey,
		},
	}); err != nil {
		return err
	}
	if s.log != nil {
		s.log.Warn("service", "service.clone_timeline_invalid", map[string]any{
			"run_id":   string(snap.run.ID),
			"language": language,
			"error":    cause.Error(),
		})
	}
	snap.tasks = append(snap.tasks, task)
	snap.taskByID[task.ID] = task
	return nil
}

func taskBoolParam(params map[string]any, key string) bool {
	value, _ := params[key].(bool)
	return value
}

// taskMillisParam 读取冻结 JSON 参数中的整数毫秒；非整数/越界值视为缺失。
func taskMillisParam(params map[string]any, key string) (domain.Millis, bool) {
	switch value := params[key].(type) {
	case int64:
		return domain.Millis(value), true
	case int:
		return domain.Millis(value), true
	case float64:
		// JSON 解码得到 float64；超出 int64 范围的转换是实现相关行为，
		// 必须显式视为缺失，不能依赖平台饱和/回绕结果。
		if math.IsNaN(value) || math.IsInf(value, 0) ||
			value != math.Trunc(value) ||
			value < -float64(1<<63) || value >= float64(1<<63) {
			return 0, false
		}
		return domain.Millis(value), true
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		return domain.Millis(parsed), true
	default:
		return 0, false
	}
}
