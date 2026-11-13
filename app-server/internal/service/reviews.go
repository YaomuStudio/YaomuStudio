package service

import (
	"context"
	"fmt"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// ApproveReviewRequest 是人工放行输入。
//
// CurrentBinding 可选：新客户端提交它时，Core 在批准事务内重算同一 gate 的
// 当前绑定；tuple 不再存在即 REVISION_CONFLICT。缺省保留旧客户端记录历史
// gate 的语义（历史批准不代表放行新内容）。
type ApproveReviewRequest struct {
	RevisionDigest string                       `json:"revision_digest"`
	Type           domain.ReviewType            `json:"type"`
	CurrentBinding *domain.ReviewCurrentBinding `json:"current_binding,omitempty"`
}

// ListProjectReviews 返回项目内审核门槛；runID 非空时按运行过滤。
//
// 列表与 current_bindings 来自同一只读事务快照；每个 Run 只加载一次运行与
// 字幕轨（单连接库禁止在事务内再走 Store 查询）。
func (s *Service) ListProjectReviews(ctx context.Context, projectID, runID string) ([]domain.ReviewGate, error) {
	if projectID == "" {
		return nil, invalidRequestf("project id required")
	}
	var gates []domain.ReviewGate
	err := s.store.ReadTx(ctx, func(tx *store.Tx) error {
		if _, err := tx.GetProject(ctx, projectID); err != nil {
			return err
		}
		if runID != "" {
			run, err := tx.GetWorkflowRun(ctx, runID)
			if err != nil {
				return err
			}
			if run.ProjectID != projectID {
				return invalidRequestf("run %s does not belong to project %s", runID, projectID)
			}
		}
		var err error
		gates, err = tx.ListProjectReviews(ctx, projectID, runID)
		if err != nil {
			return err
		}
		if gates == nil {
			gates = []domain.ReviewGate{}
		}
		return attachReviewCurrentBindings(ctx, tx, gates)
	})
	if err != nil {
		return nil, err
	}
	return gates, nil
}

// GetReview 返回单个审核门槛（详情含同一读事务派生的 current_bindings）。
func (s *Service) GetReview(ctx context.Context, id string) (domain.ReviewGate, error) {
	if id == "" {
		return domain.ReviewGate{}, invalidRequestf("review id required")
	}
	var gate domain.ReviewGate
	err := s.store.ReadTx(ctx, func(tx *store.Tx) error {
		var err error
		gate, err = tx.GetReviewGate(ctx, id)
		if err != nil {
			return err
		}
		gates := []domain.ReviewGate{gate}
		if err := attachReviewCurrentBindings(ctx, tx, gates); err != nil {
			return err
		}
		gate = gates[0]
		return nil
	})
	if err != nil {
		return domain.ReviewGate{}, err
	}
	return gate, nil
}

// ApproveReview 人工放行（200）：绑定精确 revision_digest，不一致返回 REVISION_CONFLICT。
//
// 请求携带可选 current_binding 时，在同一写事务内、任何状态变更前按当前持久
// 事实重算该 gate 的绑定；tuple 不再存在返回 REVISION_CONFLICT，不写状态/事件/
// 幂等回执。缺省字段的旧请求保留既有的“记录历史 gate 批准”语义。
//
// 暂停中的 Run 允许登记审核结果（只登记、不派发）；终止 Run 由存储层拒绝。
func (s *Service) ApproveReview(ctx context.Context, idem Idem, id string, req ApproveReviewRequest) (Response, error) {
	if req.RevisionDigest == "" {
		return Response{}, invalidRequestf("revision_digest required")
	}
	if !req.Type.Valid() {
		return Response{}, invalidRequestf("invalid review type %q", req.Type)
	}
	if req.CurrentBinding != nil {
		if err := validateReviewCurrentBinding(*req.CurrentBinding); err != nil {
			return Response{}, err
		}
	}
	var events []domain.DomainEvent
	resp, err := s.write(ctx, "reviews.approve", idem, 200, func(ctx context.Context, tx *store.Tx) (any, error) {
		gate, err := tx.GetReviewGate(ctx, id)
		if err != nil {
			return nil, err
		}
		// 先比较请求摘要与当前绑定；不一致直接 409，不读取/修改任何状态。
		if gate.RevisionDigest != req.RevisionDigest {
			return nil, fmt.Errorf("%w: review %s bound to another revision", domain.ErrRevisionConflict, id)
		}
		run, err := tx.GetWorkflowRun(ctx, gate.RunID)
		if err != nil {
			return nil, err
		}
		if !domain.RunAllowsReviewApproval(run.Status) {
			return nil, errRunClosed(run.ID, run.Status)
		}
		// 可选当前绑定守卫：必须在任何写操作之前复核；不再匹配时保持
		// gate/run/任务/事件/幂等回执全部不变。
		if req.CurrentBinding != nil {
			bindings, err := gateCurrentBindings(ctx, tx, run, gate)
			if err != nil {
				return nil, err
			}
			if !currentBindingMatches(bindings, *req.CurrentBinding) {
				return nil, fmt.Errorf("%w: review %s current binding changed", domain.ErrRevisionConflict, id)
			}
		}
		approved, err := tx.ApproveReview(ctx, id, req.RevisionDigest, req.Type, s.Now())
		if err != nil {
			return nil, err
		}
		// 放行后由 Core 聚合节点状态并推进下游；流水线开关关闭时保留旧的
		// “直接置 done + 唤醒 Run” 语义，供手动任务路径使用。
		if s.opts.PipelineOrchestration {
			if err := s.advancePipeline(ctx, tx, approved.RunID, &events); err != nil {
				return nil, err
			}
		} else {
			if nodeKey, ok := nodeKeyForReview(approved.Type); ok {
				if current, err := tx.GetWorkflowRun(ctx, approved.RunID); err == nil &&
					nodeStatusOf(current, nodeKey) == domain.NodeStatusWaitingReview {
					if err := tx.UpdateRunNode(ctx, current.ID, nodeKey, domain.NodeStatusDone, 100, ""); err != nil {
						return nil, err
					}
				}
			}
			if err := s.maybeCompleteRun(ctx, tx, approved.RunID, &events); err != nil {
				return nil, err
			}
			if current, err := tx.GetWorkflowRun(ctx, approved.RunID); err == nil &&
				current.Status == domain.RunStatusWaitingReview &&
				domain.CanTransitionRun(current.Status, domain.RunStatusRunning) {
				if _, err := tx.UpdateRunStatus(ctx, current.ID, domain.RunStatusRunning, current.StateVersion, s.Now()); err != nil {
					return nil, err
				}
			}
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type: "review.approved",
			Data: map[string]any{
				"review_id":       approved.ID,
				"run_id":          approved.RunID,
				"review_type":     approved.Type,
				"language":        approved.Language,
				"revision_digest": approved.RevisionDigest,
			},
		}); err != nil {
			return nil, err
		}
		return approved, nil
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	s.notify()
	return resp, nil
}

// createReviewGate 在事务内新建 pending 审核门槛（上游变化时旧 approved 不覆盖）。
func (s *Service) createReviewGate(ctx context.Context, tx *store.Tx, runID domain.ID, typ domain.ReviewType, language, digest string) (domain.ReviewGate, error) {
	createdAt := s.Now().UTC()
	gate := domain.ReviewGate{
		ID:             store.NewID("review"),
		RunID:          runID,
		Type:           typ,
		Language:       language,
		RevisionDigest: digest,
		Status:         domain.ReviewStatusPending,
		CreatedAt:      &createdAt,
	}
	return gate, tx.CreateReviewGate(ctx, gate)
}

// onNodeTaskSucceeded 是节点任务成功后的业务钩子：
// 更新节点状态、为 06/08 建待审核门槛（导出任务由 finalizeExport 处理）。
func (s *Service) onNodeTaskSucceeded(ctx context.Context, task domain.Task) error {
	var events []domain.DomainEvent
	err := s.store.DoTx(ctx, func(tx *store.Tx) error {
		now := s.Now()
		if task.Kind != domain.TaskKindNode || task.RunID == "" || task.NodeKey == "" {
			return nil
		}
		run, err := tx.GetWorkflowRun(ctx, task.RunID)
		if err != nil {
			return err
		}
		if run.Status.Terminal() {
			return nil
		}
		nodeStatus := nodeStatusOf(run, task.NodeKey)
		if nodeStatus == domain.NodeStatusPending {
			if err := tx.UpdateRunNode(ctx, run.ID, task.NodeKey, domain.NodeStatusRunning, 0, ""); err != nil {
				return err
			}
			nodeStatus = domain.NodeStatusRunning
		}
		if domain.NodeRequiresReview(task.NodeKey) {
			digest, err := domain.NodeReviewDigest(task.RunID, task.NodeKey, task.Language,
				task.ID, task.ArtifactIDs, task.InputDigest)
			if err != nil {
				return err
			}
			reviewType := domain.ReviewTypeTranslation
			language := task.Language
			if task.NodeKey == domain.NodeKeyMix {
				reviewType = domain.ReviewTypeDelivery
				language = ""
			}
			if err := tx.UpdateRunNode(ctx, run.ID, task.NodeKey, domain.NodeStatusWaitingReview, 100, ""); err != nil {
				return err
			}
			gate, err := s.createReviewGate(ctx, tx, run.ID, reviewType, language, digest)
			if err != nil {
				return err
			}
			if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
				Type:         "workflow.waiting_review",
				ProjectID:    run.ProjectID,
				TaskID:       task.ID,
				StateVersion: run.StateVersion,
				Data: map[string]any{
					"run_id":      run.ID,
					"review_id":   gate.ID,
					"review_type": reviewType,
					"language":    language,
				},
			}); err != nil {
				return err
			}
			if run.Status != domain.RunStatusWaitingReview && domain.CanTransitionRun(run.Status, domain.RunStatusWaitingReview) {
				if _, err := tx.UpdateRunStatus(ctx, run.ID, domain.RunStatusWaitingReview, run.StateVersion, now); err != nil {
					return err
				}
			}
			return nil
		}
		if domain.CanTransitionNode(nodeStatus, domain.NodeStatusDone, task.NodeKey) {
			if err := tx.UpdateRunNode(ctx, run.ID, task.NodeKey, domain.NodeStatusDone, 100, ""); err != nil {
				return err
			}
		}
		return s.maybeCompleteRun(ctx, tx, run.ID, &events)
	})
	if err != nil {
		return err
	}
	s.publish(events...)
	return nil
}

// OnTaskFailed 是任务失败/中断后的钩子：标记节点失败与导出状态。
func (s *Service) OnTaskFailed(ctx context.Context, task domain.Task, info *domain.ErrorInfo) error {
	var events []domain.DomainEvent
	err := s.store.DoTx(ctx, func(tx *store.Tx) error {
		now := s.Now()
		if task.Kind == domain.TaskKindExport {
			rec, err := tx.GetExportByTask(ctx, task.ID)
			if err == nil {
				if err := tx.UpdateExportStatus(ctx, rec.ID, domain.TaskStatusFailed, "", now); err != nil {
					return err
				}
				if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
					Type:      "export.failed",
					ProjectID: rec.ProjectID,
					TaskID:    task.ID,
					Data: map[string]any{
						"export_id":   rec.ID,
						"code":        errorCode(info),
						"message_key": errorMessageKey(info),
					},
				}); err != nil {
					return err
				}
			}
			return nil
		}
		if task.Kind != domain.TaskKindNode && task.Kind != domain.TaskKindSegment {
			return nil
		}
		if task.RunID == "" || task.NodeKey == "" {
			return nil
		}
		if s.opts.PipelineOrchestration {
			// 多语言/多句段节点由聚合决定是否失败；单个任务失败不能直接把
			// 整个节点标成终态，否则其他语言仍在运行时 Run 会被提前收敛。
			return s.recomputeRunNodes(ctx, tx, task.RunID, &events)
		}
		run, err := tx.GetWorkflowRun(ctx, task.RunID)
		if err != nil {
			return err
		}
		if run.Status.Terminal() {
			return nil
		}
		nodeStatus := nodeStatusOf(run, task.NodeKey)
		if nodeStatus == domain.NodeStatusPending {
			if err := tx.UpdateRunNode(ctx, run.ID, task.NodeKey, domain.NodeStatusRunning, 0, ""); err != nil {
				return err
			}
			nodeStatus = domain.NodeStatusRunning
		}
		if domain.CanTransitionNode(nodeStatus, domain.NodeStatusFailed, task.NodeKey) {
			if err := tx.UpdateRunNode(ctx, run.ID, task.NodeKey, domain.NodeStatusFailed, 100, ""); err != nil {
				return err
			}
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type:      "workflow.node_failed",
			ProjectID: run.ProjectID,
			TaskID:    task.ID,
			Data: map[string]any{
				"run_id":   run.ID,
				"node_key": task.NodeKey,
				"code":     errorCode(info),
			},
		}); err != nil {
			return err
		}
		return s.maybeCompleteRun(ctx, tx, run.ID, &events)
	})
	if err != nil {
		return err
	}
	s.publish(events...)
	return nil
}

// maybeCompleteRun 在所有节点进入终态后收敛 Run：任一节点失败 → PARTIAL，
// 否则 SUCCEEDED。仍有待审核/未执行节点时保持当前状态。
func (s *Service) maybeCompleteRun(ctx context.Context, tx *store.Tx, runID domain.ID, events *[]domain.DomainEvent) error {
	run, err := tx.GetWorkflowRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status.Terminal() || len(run.Nodes) == 0 {
		return nil
	}
	anyFailed := false
	for _, node := range run.Nodes {
		if !node.Status.Terminal() {
			return nil
		}
		if node.Status == domain.NodeStatusFailed {
			anyFailed = true
		}
	}
	target := domain.RunStatusSucceeded
	if anyFailed {
		target = domain.RunStatusPartial
	}
	// QUEUED 不能直接进入 SUCCEEDED/PARTIAL：所有节点在首个可派发任务之前
	// 就进入终态时（例如 08 计划输入缺失、导入译稿跳过 04/06），先按合法路径
	// 进入 RUNNING 再收敛，避免 Run 永久停在 QUEUED。
	path := []domain.RunStatus{target}
	if !domain.CanTransitionRun(run.Status, target) {
		if run.Status == domain.RunStatusQueued && anyFailed {
			// 没有任何任务就出现的节点失败通常是预检/输入推导的瞬态结果，
			// 保留 QUEUED 让调用方仍可创建真实任务；只有存在失败任务时才
			// 通过合法路径收敛，避免把空 Run 锁成 PARTIAL。
			tasks, err := tx.ListRunTasks(ctx, run.ID)
			if err != nil {
				return err
			}
			if len(tasks) == 0 {
				return nil
			}
		}
		if domain.CanTransitionRun(run.Status, domain.RunStatusRunning) &&
			domain.CanTransitionRun(domain.RunStatusRunning, target) {
			path = []domain.RunStatus{domain.RunStatusRunning, target}
		} else {
			return nil
		}
	}
	var updated domain.WorkflowRun
	for _, status := range path {
		next, err := tx.UpdateRunStatus(ctx, run.ID, status, run.StateVersion, s.Now())
		if err != nil {
			return err
		}
		run.Status = next.Status
		run.StateVersion = next.StateVersion
		updated = next
	}
	return s.stageEvent(ctx, tx, events, domain.DomainEvent{
		Type:         "workflow.completed",
		ProjectID:    updated.ProjectID,
		StateVersion: updated.StateVersion,
		Data:         map[string]any{"run_id": updated.ID, "status": updated.Status},
	})
}

// nodeKeyForReview 返回审核类型对应的节点（translation→06，delivery→08）。
func nodeKeyForReview(typ domain.ReviewType) (domain.PipelineNodeKey, bool) {
	switch typ {
	case domain.ReviewTypeTranslation:
		return domain.NodeKeyTranslate, true
	case domain.ReviewTypeDelivery:
		return domain.NodeKeyMix, true
	default:
		return "", false
	}
}

func nodeStatusOf(run domain.WorkflowRun, key domain.PipelineNodeKey) domain.NodeStatus {
	for _, node := range run.Nodes {
		if node.Key == key {
			return node.Status
		}
	}
	return domain.NodeStatusPending
}

func errorCode(info *domain.ErrorInfo) string {
	if info == nil {
		return string(domain.ErrorCodeWorkerExited)
	}
	return string(info.Code)
}

func errorMessageKey(info *domain.ErrorInfo) string {
	if info == nil || info.MessageKey == "" {
		return "task.worker_error"
	}
	return info.MessageKey
}
