package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

const cloneAudioAssemblyOut = "full.wav"

// errCloneAudioPlanNotReady 表示完整音轨合成所需的选择/候选尚未就绪；推进时
// 等待下一次提交，不创建缺输入任务。
var errCloneAudioPlanNotReady = errors.New("clone audio plan input not ready")

// frozenCloneAudioPlan 是 07 完整音轨合成的冻结计划。
//
// 与 08 视频合成不同，这里只按译文时间轴放置已选句级配音，间隔保持静音；
// 输出长度由最后一个句段的播放结束时间决定，对齐 demo full 模式的成品音频。
type frozenCloneAudioPlan struct {
	InputIDs []domain.ID
	Digest   string
	Params   map[string]any
}

// cloneAudioAssemblyRequired 限定在音色克隆工具模板：它的产出必须是完整音频，
// 而不是只有逐句候选。自定义流水线/全流程的 08 仍按各自计划合成视频。
func cloneAudioAssemblyRequired(run domain.WorkflowRun) bool {
	return run.Template == templateVoiceClone
}

func isCloneAudioAssemblyParams(params map[string]any) bool {
	op, _ := params["op"].(string)
	return op == "assemble"
}

// isCloneAudioAssemblyTask 识别 07 的完整音轨任务（无 SegmentID，op=assemble）。
func isCloneAudioAssemblyTask(task domain.Task) bool {
	return task.Kind == domain.TaskKindNode &&
		task.NodeKey == domain.NodeKeyClone &&
		task.SegmentID == "" &&
		isCloneAudioAssemblyParams(task.Params)
}

// latestCloneAudioAssemblyTask 返回该语言最近一次完整音轨任务；候选选择变化后
// 会产生新的确定性任务，旧任务保留为历史。
func latestCloneAudioAssemblyTask(tasks []domain.Task, language string) (domain.Task, bool) {
	var latest domain.Task
	found := false
	for _, task := range tasks {
		if task.Language != language || !isCloneAudioAssemblyTask(task) {
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

// freezeCloneAudioPlan 把当前译稿与每句已选候选投影为一次受控 FFmpeg 混音。
//
// 输入路径保持在项目托管位置；调度层 PrepareTaskExecution 会按冻结资产 ID
// 复制到任务专属目录并重写 path。计划摘要包含 revision、选择 ID、顺序和
// 时间轴，候选选择或译稿变化会产生新的任务身份。
func (s *Service) freezeCloneAudioPlan(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	revision domain.SubtitleRevision,
	language string,
) (frozenCloneAudioPlan, error) {
	work, err := s.segmentWorkForRevision(ctx, tx, snap, revision, language)
	if err != nil {
		return frozenCloneAudioPlan{}, err
	}
	if len(revision.Segments) == 0 {
		return frozenCloneAudioPlan{}, invalidRequestf("clone revision contains no subtitle cues")
	}

	inputIDs := make([]domain.ID, 0, len(revision.Segments)+1)
	inputIDs = append(inputIDs, revision.ID)
	selectionIDs := make([]domain.ID, 0, len(revision.Segments))
	audio := make([]exportAudioInput, 0, len(revision.Segments))
	for _, segment := range revision.Segments {
		selection, err := tx.GetCandidateSelection(ctx, segment.ID, language)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return frozenCloneAudioPlan{}, fmt.Errorf(
					"%w: segment %s has no selected voice candidate", errCloneAudioPlanNotReady, segment.ID)
			}
			return frozenCloneAudioPlan{}, mapReference(err, "current sentence selection")
		}
		if selection.Revision != segment.ContentRevision {
			return frozenCloneAudioPlan{}, invalidRequestf(
				"sentence selection does not match its content revision for language %s", language)
		}
		candidates, err := tx.ListAudioCandidates(ctx, segment.ID, language, &selection.Revision)
		if err != nil {
			return frozenCloneAudioPlan{}, err
		}
		var chosen *domain.AudioCandidate
		for i := range candidates {
			if candidates[i].ID == selection.CandidateID {
				chosen = &candidates[i]
				break
			}
		}
		if chosen == nil || !candidateMatchesWork(snap.tasks, *chosen, work[segment.ID].CacheKey) {
			return frozenCloneAudioPlan{}, invalidRequestf(
				"sentence selection does not match frozen generation dependencies for language %s", language)
		}
		asset, err := pipelineAsset(ctx, tx, snap.run.ProjectID, chosen.AudioAssetID)
		if err != nil {
			return frozenCloneAudioPlan{}, err
		}
		if !isPipelineAudio(asset) {
			return frozenCloneAudioPlan{}, invalidRequestf("selected sentence asset is not audio")
		}
		path, err := exportAssetPath(asset)
		if err != nil {
			return frozenCloneAudioPlan{}, err
		}
		audio = append(audio, exportAudioInput{
			Path:    path,
			Volume:  exportVoiceVolume,
			StartMs: segment.StartMs,
		})
		inputIDs = append(inputIDs, asset.ID)
		selectionIDs = append(selectionIDs, chosen.ID)
	}

	text := renderSRT(revision.Segments)
	params := map[string]any{
		"language":    language,
		"op":          "assemble",
		"inputs":      audioInputParams(audio),
		"out":         cloneAudioAssemblyOut,
		"audio_codec": "pcm_s16le",
		"sample_rate": exportSampleRate,
	}
	digest, err := mixPlanDigest(snap, revision, language, inputIDs, selectionIDs, text, params)
	if err != nil {
		return frozenCloneAudioPlan{}, err
	}
	return frozenCloneAudioPlan{InputIDs: inputIDs, Digest: digest, Params: params}, nil
}

// ensureCloneAudioAssembly 在句级克隆全部成功后创建一条完整音轨任务。
// 失败只登记为 07 自身任务（不把刚提交成功的句级结果拖回滚）。
func (s *Service) ensureCloneAudioAssembly(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	language string,
	revision *domain.SubtitleRevision,
	events *[]domain.DomainEvent,
) error {
	if revision == nil || !cloneAudioAssemblyRequired(*snap.run) {
		return nil
	}
	complete, err := s.cloneComplete(ctx, tx, snap, language, revision)
	if err != nil || !complete {
		return err
	}
	plan, err := s.freezeCloneAudioPlan(ctx, tx, snap, *revision, language)
	if err != nil {
		if errors.Is(err, errCloneAudioPlanNotReady) {
			return nil
		}
		return s.ensureCloneAudioAssemblyFailure(ctx, tx, snap, language, revision, err, events)
	}
	return s.ensureNodeTask(ctx, tx, snap, domain.NodeKeyClone, language,
		plan.InputIDs, plan.Digest, plan.Params, events)
}

// ensureCloneAudioAssemblyFailure 为无法构建完整音轨的当前选择登记一次可读失败。
// 任务带 op=assemble 标记，状态聚合据此把 07 收敛为失败；修复选择后新的计划
// 会以不同身份创建新任务。
func (s *Service) ensureCloneAudioAssemblyFailure(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	language string,
	revision *domain.SubtitleRevision,
	cause error,
	events *[]domain.DomainEvent,
) error {
	params := map[string]any{
		"language": language,
		"op":       "assemble",
		"out":      cloneAudioAssemblyOut,
	}
	coreTaskParams(params)["clone_audio_plan_error"] = true
	inputIDs := []string{revision.ID}
	inputHash, err := taskInputDigest(CreateTaskRequest{
		RunID:            snap.run.ID,
		Kind:             domain.TaskKindNode,
		NodeKey:          domain.NodeKeyClone,
		Language:         language,
		InputRevisionIDs: inputIDs,
		Params:           params,
	})
	if err != nil {
		return err
	}
	id := pipelineTaskID(snap.run.ID, string(domain.NodeKeyClone), language, "", inputHash)
	if _, exists := snap.taskByID[id]; exists {
		return nil
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
		Params:           params,
		Error: &domain.ErrorInfo{
			Code:       domain.ErrorCodeInvalidRequest,
			MessageKey: "task.clone_audio_plan_invalid",
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
		s.log.Warn("service", "service.clone_audio_plan_invalid", map[string]any{
			"run_id":   string(snap.run.ID),
			"language": language,
			"error":    cause.Error(),
		})
	}
	snap.tasks = append(snap.tasks, task)
	snap.taskByID[task.ID] = task
	return nil
}
