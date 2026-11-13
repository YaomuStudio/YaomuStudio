package service

import (
	"context"
	"fmt"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// CreateTaskRequest 是提交执行单元的输入（contracts/openapi.yaml CreateTaskRequest）。
type CreateTaskRequest struct {
	ProjectID        string                 `json:"project_id"`
	RunID            string                 `json:"run_id"`
	Kind             domain.TaskKind        `json:"kind"`
	NodeKey          domain.PipelineNodeKey `json:"node_key"`
	SegmentID        string                 `json:"segment_id"`
	Language         string                 `json:"language"`
	InputRevisionIDs []string               `json:"input_revision_ids"`
	Params           map[string]any         `json:"params"`
	ModelLock        *domain.ReleaseRef     `json:"model_lock"`
}

// ListTasksQuery 是任务列表过滤条件。
type ListTasksQuery struct {
	ProjectID string
	RunID     string
	Status    domain.TaskStatus
	Cursor    string
	Limit     int
}

// ListTasks 分页返回任务列表（不含完整步骤历史）。
func (s *Service) ListTasks(ctx context.Context, query ListTasksQuery) (domain.TaskList, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if query.Status != "" && !query.Status.Valid() {
		return domain.TaskList{}, invalidRequestf("invalid task status %q", query.Status)
	}
	if query.Cursor != "" {
		if _, _, err := store.DecodeTaskCursor(query.Cursor); err != nil {
			return domain.TaskList{}, err
		}
	}
	tasks, err := s.store.ListTasks(ctx, store.TaskFilter{
		ProjectID: query.ProjectID,
		RunID:     query.RunID,
		Status:    query.Status,
		Cursor:    query.Cursor,
		Limit:     limit,
	})
	if err != nil {
		return domain.TaskList{}, err
	}
	list := domain.TaskList{Items: tasks}
	if len(tasks) > limit {
		list.Items = tasks[:limit]
		last := list.Items[len(list.Items)-1]
		list.NextCursor = store.EncodeTaskCursor(last.CreatedAt, last.ID)
	}
	if list.Items == nil {
		list.Items = []domain.Task{}
	}
	for i := range list.Items {
		normalizeTask(&list.Items[i])
	}
	return list, nil
}

// GetTask 返回任务快照（含步骤与产物）。
func (s *Service) GetTask(ctx context.Context, id string) (domain.Task, error) {
	if id == "" {
		return domain.Task{}, invalidRequestf("task id required")
	}
	task, err := s.store.GetTask(ctx, id)
	if err != nil {
		return domain.Task{}, err
	}
	normalizeTask(&task)
	return task, nil
}

func normalizeTask(task *domain.Task) {
	if task.ArtifactIDs == nil {
		task.ArtifactIDs = []domain.ID{}
	}
	if task.Steps == nil {
		task.Steps = []domain.TaskStep{}
	}
}

// stepIDForTask 返回任务步骤的稳定 ID（不使用数组下标）。
func stepIDForTask(kind domain.TaskKind, nodeKey domain.PipelineNodeKey) string {
	switch kind {
	case domain.TaskKindNode:
		if nodeKey != "" {
			return string(nodeKey)
		}
		return "node"
	case domain.TaskKindSegment:
		return "segment"
	case domain.TaskKindExport:
		return "export"
	default:
		return "main"
	}
}

// nodeSlotKey 返回节点对应的能力槽位（受控参数校验用）。
func nodeSlotKey(node domain.PipelineNodeKey) (domain.CapabilitySlotKey, bool) {
	switch node {
	case domain.NodeKeyImport, domain.NodeKeyMix:
		return domain.CapabilitySlotFFmpeg, true
	case domain.NodeKeyAnalyze, domain.NodeKeyExtract:
		return domain.CapabilitySlotASR, true
	case domain.NodeKeySeparate:
		return domain.CapabilitySlotSeparation, true
	case domain.NodeKeyErase:
		return domain.CapabilitySlotSubtitleErase, true
	case domain.NodeKeyTranslate:
		return domain.CapabilitySlotTranslate, true
	case domain.NodeKeyClone:
		// 克隆模型自身包含合成，执行身份取 voice-clone 槽，不借用 TTS 槽。
		return domain.CapabilitySlotVoiceClone, true
	default:
		return "", false
	}
}

// validateTaskParams 只接受能力 Schema 内的受控参数键。
func validateTaskParams(kind domain.TaskKind, node domain.PipelineNodeKey, params map[string]any) error {
	if len(params) == 0 {
		return nil
	}
	if _, reserved := params["_core"]; reserved {
		return invalidRequestf("task param %q is reserved for Core", "_core")
	}
	slot, ok := nodeSlotKey(node)
	if !ok || kind != domain.TaskKindNode {
		return invalidRequestf("task params are not allowed for this task kind")
	}
	allowed := make(map[string]struct{})
	for _, field := range paramCatalog(slot) {
		allowed[field.Key] = struct{}{}
	}
	if slot == domain.CapabilitySlotVoiceClone {
		for _, field := range paramCatalog(domain.CapabilitySlotTTS) {
			allowed[field.Key] = struct{}{}
		}
	}
	for key := range params {
		if _, ok := allowed[key]; !ok {
			return invalidRequestf("task param %q is not in the controlled schema", key)
		}
	}
	return nil
}

// taskInputDigest 计算冻结输入摘要（重试时用于确认输入未变化）。
// nil 与空集合归一为同一表示，避免持久化往返改变摘要。
func taskInputDigest(req CreateTaskRequest) (string, error) {
	inputIDs := req.InputRevisionIDs
	if inputIDs == nil {
		inputIDs = []string{}
	}
	params := req.Params
	if params == nil {
		params = map[string]any{}
	}
	doc := struct {
		RunID            string
		Kind             domain.TaskKind
		NodeKey          domain.PipelineNodeKey
		SegmentID        string
		Language         string
		InputRevisionIDs []string
		Params           map[string]any
		ModelLock        *domain.ReleaseRef
	}{
		RunID:            req.RunID,
		Kind:             req.Kind,
		NodeKey:          req.NodeKey,
		SegmentID:        req.SegmentID,
		Language:         req.Language,
		InputRevisionIDs: inputIDs,
		Params:           params,
		ModelLock:        req.ModelLock,
	}
	return digestOf(doc)
}

// CreateTask 提交执行单元（202）。
//
// 派发闸门在同一事务内完成：读取 Run 状态版本 → 允许派发才进入 QUEUED，
// PAUSE_REQUESTED/PAUSED 只登记为 PAUSED，终止 Run 拒绝；任务、步骤、
// 首次 Attempt、关键事件与幂等回执同事务提交。
func (s *Service) CreateTask(ctx context.Context, idem Idem, req CreateTaskRequest) (Response, error) {
	if err := validateCreateTaskRequest(req); err != nil {
		return Response{}, err
	}
	digest, err := taskInputDigest(req)
	if err != nil {
		return Response{}, err
	}
	var events []domain.DomainEvent
	resp, err := s.write(ctx, "tasks.create", idem, 202, func(ctx context.Context, tx *store.Tx) (any, error) {
		if _, err := tx.GetProject(ctx, req.ProjectID); err != nil {
			return nil, mapReference(err, "project")
		}
		status := domain.TaskStatusQueued
		taskParams := req.Params
		taskLock := req.ModelLock
		var runVersion int64
		var runStatus domain.RunStatus
		var run *domain.WorkflowRun
		if req.RunID != "" {
			current, err := tx.GetWorkflowRun(ctx, req.RunID)
			if err != nil {
				return nil, mapReference(err, "run")
			}
			if current.ProjectID != req.ProjectID {
				return nil, invalidRequestf("run %s does not belong to project %s", req.RunID, req.ProjectID)
			}
			if current.Status.Terminal() {
				return nil, errRunClosed(current.ID, current.Status)
			}
			runVersion = current.StateVersion
			runStatus = current.Status
			run = &current
			tpl, err := pipelineTemplateForRun(current)
			if err != nil {
				return nil, err
			}
			if req.Kind == domain.TaskKindNode && !tpl.Has(req.NodeKey) {
				return nil, invalidRequestf("node %s is not enabled by this run", req.NodeKey)
			}
			if req.Kind == domain.TaskKindSegment && !tpl.Has(domain.NodeKeyClone) {
				return nil, invalidRequestf("run does not enable segment work")
			}
			if !domain.RunAllowsDispatch(current.Status) {
				status = domain.TaskStatusPaused
			}
		}
		if s.opts.PipelineOrchestration && req.Kind != domain.TaskKindExport {
			if run == nil {
				return nil, executionUnverified()
			}
			node := req.NodeKey
			if req.Kind == domain.TaskKindSegment {
				node = domain.NodeKeyClone
			}
			taskParams, taskLock, err = freezeTaskParams(*run, node, req.Params)
			if err != nil {
				return nil, err
			}
			if node == domain.NodeKeyExtract || node == domain.NodeKeyTranslate {
				snap, err := s.loadPipelineSnapshot(ctx, tx, run.ID)
				if err != nil {
					return nil, err
				}
				freezeSubtitleOutput(snap, node, req.Language, taskParams)
			}
			digest, err = taskInputDigest(CreateTaskRequest{
				RunID: req.RunID, Kind: req.Kind, NodeKey: req.NodeKey,
				SegmentID: req.SegmentID, Language: req.Language,
				InputRevisionIDs: req.InputRevisionIDs, Params: taskParams, ModelLock: taskLock,
			})
			if err != nil {
				return nil, err
			}
		}

		now := s.Now()
		task := domain.Task{
			ID:               store.NewID("task"),
			ProjectID:        req.ProjectID,
			RunID:            req.RunID,
			Kind:             req.Kind,
			NodeKey:          req.NodeKey,
			SegmentID:        req.SegmentID,
			Language:         req.Language,
			Status:           status,
			Progress:         0,
			ArtifactIDs:      []domain.ID{},
			InputRevisionIDs: req.InputRevisionIDs,
			InputDigest:      digest,
			Params:           taskParams,
			ModelLock:        taskLock,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		stepID := stepIDForTask(req.Kind, req.NodeKey)
		task.Steps = []domain.TaskStep{{
			StepID:    stepID,
			Status:    domain.StepStatusPending,
			AttemptNo: 1,
			Epoch:     s.opts.ServerEpoch,
		}}
		if err := tx.CreateTask(ctx, task); err != nil {
			return nil, err
		}
		if err := tx.CreateAttempt(ctx, domain.Attempt{
			ID:        store.NewID("attempt"),
			TaskID:    task.ID,
			StepID:    stepID,
			AttemptNo: 1,
			Epoch:     s.opts.ServerEpoch,
			Status:    domain.StepStatusPending,
		}); err != nil {
			return nil, err
		}

		// 事务内复核 Run 状态版本：状态在评估后被动过则拒绝派发。
		if req.RunID != "" {
			current, err := tx.GetWorkflowRun(ctx, req.RunID)
			if err != nil {
				return nil, err
			}
			if current.StateVersion != runVersion || current.Status != runStatus {
				return nil, fmt.Errorf("%w: run %s changed during dispatch", domain.ErrStateConflict, req.RunID)
			}
		}

		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
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
			return nil, err
		}
		if status == domain.TaskStatusQueued {
			if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
				Type:         "task.queued",
				ProjectID:    task.ProjectID,
				TaskID:       task.ID,
				StateVersion: task.StateVersion,
			}); err != nil {
				return nil, err
			}
		} else {
			if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
				Type:         "task.paused",
				ProjectID:    task.ProjectID,
				TaskID:       task.ID,
				StateVersion: task.StateVersion,
			}); err != nil {
				return nil, err
			}
		}
		return task, nil
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	s.notify()
	return resp, nil
}

func validateCreateTaskRequest(req CreateTaskRequest) error {
	if req.ProjectID == "" {
		return invalidRequestf("project_id required")
	}
	if !req.Kind.Valid() {
		return invalidRequestf("invalid task kind %q", req.Kind)
	}
	if req.Kind == domain.TaskKindExport {
		return invalidRequestf("export tasks are created through the exports API")
	}
	if req.Kind == domain.TaskKindNode {
		if !req.NodeKey.Valid() {
			return invalidRequestf("node_key required for node tasks")
		}
	}
	if req.Kind == domain.TaskKindSegment {
		if req.SegmentID == "" {
			return invalidRequestf("segment_id required for segment tasks")
		}
		if req.NodeKey != domain.NodeKeyClone {
			return invalidRequestf("segment tasks must use node_key 07_clone")
		}
		if req.Language == "" {
			return invalidRequestf("language required for segment tasks")
		}
	}
	if req.NodeKey != "" && !req.NodeKey.Valid() {
		return invalidRequestf("invalid node_key %q", req.NodeKey)
	}
	if err := validateTaskParams(req.Kind, req.NodeKey, req.Params); err != nil {
		return err
	}
	if req.ModelLock != nil && (req.ModelLock.ReleaseID == "" ||
		req.ModelLock.Version == "" || req.ModelLock.Digest == "") {
		return invalidRequestf("model_lock requires release_id, version and digest")
	}
	return nil
}

// CancelTask 请求取消（幂等，202）；响应只代表受理，实际终态见任务快照。
func (s *Service) CancelTask(ctx context.Context, idem Idem, id string) (Response, error) {
	var (
		events          []domain.DomainEvent
		cancelExecuting bool
		canceledTask    *domain.Task
	)
	resp, err := s.write(ctx, "tasks.cancel", idem, 202, func(ctx context.Context, tx *store.Tx) (any, error) {
		task, err := tx.GetTask(ctx, id)
		if err != nil {
			return nil, err
		}
		normalizeTask(&task)
		if task.Status.Terminal() || task.Status == domain.TaskStatusFailed || task.Status == domain.TaskStatusInterrupted {
			// 终态/不可取消状态：取消是幂等 no-op，返回当前快照。
			return task, nil
		}
		now := s.Now()
		switch task.Status {
		case domain.TaskStatusRunning:
			updated, err := tx.UpdateTaskStatus(ctx, id, domain.TaskStatusCancelRequested, task.StateVersion, now)
			if err != nil {
				return nil, err
			}
			cancelExecuting = true
			if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
				Type:         "task.cancel_requested",
				ProjectID:    updated.ProjectID,
				TaskID:       updated.ID,
				StateVersion: updated.StateVersion,
			}); err != nil {
				return nil, err
			}
			return updated, nil
		case domain.TaskStatusCancelRequested:
			return task, nil
		default:
			// 取消排队/等待/暂停任务时清除只属于旧状态的提示：CANCELED 快照
			// 不能继续提示“等待资源/执行器”。
			updated, err := tx.UpdateTaskResult(ctx, id, domain.TaskStatusCanceled,
				task.ArtifactIDs, nil, task.StateVersion, now)
			if err != nil {
				return nil, err
			}
			if attempt, ok, err := tx.LatestAttempt(ctx, id); err != nil {
				return nil, err
			} else if ok && !attempt.Status.Terminal() {
				if err := tx.UpdateAttempt(ctx, attempt.ID, domain.StepStatusCanceled, nil, &now); err != nil {
					return nil, err
				}
			}
			if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
				Type:         "task.canceled",
				ProjectID:    updated.ProjectID,
				TaskID:       updated.ID,
				StateVersion: updated.StateVersion,
			}); err != nil {
				return nil, err
			}
			canceledTask = &updated
			return updated, nil
		}
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	if cancelExecuting && s.canceler != nil {
		s.canceler.CancelTask(id)
	}
	if canceledTask != nil {
		if err := s.OnTaskCanceled(ctx, *canceledTask); err != nil && s.log != nil {
			// 取消事务已提交；导出记录收敛失败只记诊断，不回滚已受理的取消。
			s.log.Error("service", "service.export_cancel_sync_failed", err, map[string]any{"task_id": id})
		}
	}
	s.notify()
	return resp, nil
}

// RetryTask 重试冻结输入的任务（202）：新建单调 attempt 并回到 QUEUED；
// 成功终态或输入改变返回冲突。
func (s *Service) RetryTask(ctx context.Context, idem Idem, id string) (Response, error) {
	var events []domain.DomainEvent
	resp, err := s.write(ctx, "tasks.retry", idem, 202, func(ctx context.Context, tx *store.Tx) (any, error) {
		task, err := tx.GetTask(ctx, id)
		if err != nil {
			return nil, err
		}
		normalizeTask(&task)
		// 冻结媒体/时间轴失败重试仍会复用同一无效输入，且会丢失可读原因；
		// 必须在任何状态、attempt 或事件修改前拒绝，要求修正后新建 Run。
		if retryErr := cloneTimelineRetryError(task); retryErr != nil {
			return nil, retryErr
		}
		if !domain.TaskRetryable(task.Status) {
			// 成功/取消终态或正在执行的状态不原地重试：契约要求返回冲突。
			return nil, errConflictf(domain.ErrorCodeRevisionConflict,
				"error.task_not_retryable", "task %s is %s", id, task.Status)
		}
		currentDigest, err := taskInputDigest(CreateTaskRequest{
			RunID:            task.RunID,
			Kind:             task.Kind,
			NodeKey:          task.NodeKey,
			SegmentID:        task.SegmentID,
			Language:         task.Language,
			InputRevisionIDs: task.InputRevisionIDs,
			Params:           task.Params,
			ModelLock:        task.ModelLock,
		})
		if err != nil {
			return nil, err
		}
		prepared, err := domain.PrepareTaskRetry(task, currentDigest)
		if err != nil {
			return nil, err
		}
		paused := false
		var run *domain.WorkflowRun
		// 导出是交付动作，重试不受来源 Run 暂停/终止闸门约束。
		if task.RunID != "" && task.Kind != domain.TaskKindExport {
			current, err := tx.GetWorkflowRun(ctx, task.RunID)
			if err != nil {
				return nil, err
			}
			run = &current
			if run.Status.Terminal() {
				return nil, errRunClosed(run.ID, run.Status)
			}
			if !domain.RunAllowsDispatch(run.Status) {
				paused = true
			}
		}
		// 显式重试失败节点时把节点从 failed 拉回 running；否则任务成功也
		// 会被终态节点状态挡住，无法推进下游。
		if s.opts.PipelineOrchestration && run != nil && task.NodeKey != "" &&
			nodeStatusOf(*run, task.NodeKey) == domain.NodeStatusFailed {
			if err := tx.UpdateRunNode(ctx, run.ID, task.NodeKey, domain.NodeStatusRunning, 0, ""); err != nil {
				return nil, err
			}
		}
		now := s.Now()
		// 显式重试开启新尝试：清除上一轮失败原因，避免重新排队的
		// QUEUED 快照继续显示旧提示。
		updated, err := tx.UpdateTaskResult(ctx, id, prepared.Status,
			task.ArtifactIDs, nil, task.StateVersion, now)
		if err != nil {
			return nil, err
		}
		if paused {
			middle, err := tx.UpdateTaskStatus(ctx, id, domain.TaskStatusPauseRequested, updated.StateVersion, now)
			if err != nil {
				return nil, err
			}
			updated, err = tx.UpdateTaskStatus(ctx, id, domain.TaskStatusPaused, middle.StateVersion, now)
			if err != nil {
				return nil, err
			}
		}
		attempts, err := tx.ListAttempts(ctx, id)
		if err != nil {
			return nil, err
		}
		attemptNo := domain.NextAttemptNo(attempts)
		stepID := stepIDForTask(task.Kind, task.NodeKey)
		if err := tx.CreateAttempt(ctx, domain.Attempt{
			ID:        store.NewID("attempt"),
			TaskID:    id,
			StepID:    stepID,
			AttemptNo: attemptNo,
			Epoch:     s.opts.ServerEpoch,
			Status:    domain.StepStatusPending,
		}); err != nil {
			return nil, err
		}
		steps := []domain.TaskStep{{
			StepID:    stepID,
			Status:    domain.StepStatusPending,
			AttemptNo: attemptNo,
			Epoch:     s.opts.ServerEpoch,
		}}
		if err := tx.SetTaskSteps(ctx, id, steps, now); err != nil {
			return nil, err
		}
		// 返回包含新步骤计划的快照（UpdateTaskStatus 的快照早于 SetTaskSteps）。
		if refreshed, err := tx.GetTask(ctx, id); err == nil {
			updated = refreshed
		}
		// 重新排队与“暂停中重试”用不同事件：PAUSED 任务只登记，不派发。
		eventType := "task.queued"
		if paused {
			eventType = "task.paused"
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type:         eventType,
			ProjectID:    updated.ProjectID,
			TaskID:       updated.ID,
			StateVersion: updated.StateVersion,
			Data:         map[string]any{"attempt_no": attemptNo},
		}); err != nil {
			return nil, err
		}
		return updated, nil
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	s.notify()
	return resp, nil
}

// SetTaskProgress 写入可丢弃的进度反馈（不推进持久游标）。
func (s *Service) SetTaskProgress(ctx context.Context, id string, progress int) error {
	return s.store.SetTaskProgress(ctx, id, progress, s.Now())
}
