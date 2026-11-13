package scheduler

import (
	"context"
	"errors"
	"sort"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// 执行器阻断的持久等待原因。消息键由 UI 的安全错误展示消费；领域事件只携带
// 固定 reason，不携带底层异常、路径、命令或原始输出。
const (
	waitingReasonRunnerStarting    = "runner_starting"
	waitingReasonRunnerUnavailable = "runner_unavailable"
	waitingReasonRunnerIsolated    = "runner_isolated"
	waitingReasonPreDispatch       = "dynamic_check_failed"
)

// dispatchBlock 描述当前 Core 实例为何不能安全派发新尝试。
type dispatchBlock struct {
	reason    string
	errorInfo *domain.ErrorInfo
}

func runnerUnavailableBlock() dispatchBlock {
	return dispatchBlock{
		reason: waitingReasonRunnerUnavailable,
		errorInfo: &domain.ErrorInfo{
			Code:       domain.ErrorCodeCapabilityUnverified,
			MessageKey: "task.runner_unavailable",
			Retryable:  true,
			ActionIDs:  []domain.ID{},
		},
	}
}

func runnerIsolatedBlock() dispatchBlock {
	return dispatchBlock{
		reason: waitingReasonRunnerIsolated,
		errorInfo: &domain.ErrorInfo{
			Code:       domain.ErrorCodeWorkerExited,
			MessageKey: "task.runner_isolated",
			Retryable:  true,
			ActionIDs:  []domain.ID{},
		},
	}
}

// runnerStartingBlock 是异步初始化窗口的等待原因。它不是失败或永久隔离：
// 初始化完成后同一调度流可正常认领，并清除该原因。
func runnerStartingBlock() dispatchBlock {
	return dispatchBlock{
		reason: waitingReasonRunnerStarting,
		errorInfo: &domain.ErrorInfo{
			Code:       domain.ErrorCodeCapabilityUnverified,
			MessageKey: "task.runner_starting",
			Retryable:  true,
			ActionIDs:  []domain.ID{},
		},
	}
}

// currentDispatchBlock 返回执行阻断原因；未配置执行器与已确认永久隔离都
// fail-closed：不认领、不创建尝试、不释放未确认的执行槽。RoutingExecutor
// 非 nil 但模型侧未就绪属于执行期可见失败，不在这里归类为“无执行器”。
//
// 异步初始化持有者通过可选 Readiness 接口参与同一闸门：starting 使用可恢复
// 的启动中原因，unavailable/closed 复用未配置执行器的安全原因；永久隔离
// 优先于就绪状态。既有 executor 不实现该接口时保持原行为。
func (s *Scheduler) currentDispatchBlock() (dispatchBlock, bool) {
	if s.executor == nil {
		return runnerUnavailableBlock(), true
	}
	if s.Isolated() {
		return runnerIsolatedBlock(), true
	}
	if isolated, ok := s.executor.(interface{ IsolationErr() error }); ok {
		if err := isolated.IsolationErr(); err != nil {
			// 只记录“已隔离”这一事实；原始错误仍只进本地日志，不进入任务响应。
			s.isolate(err)
			return runnerIsolatedBlock(), true
		}
	}
	if readiness, ok := s.executor.(interface {
		Readiness() execadapter.ExecutorReadiness
	}); ok {
		switch readiness.Readiness() {
		case execadapter.ExecutorReadinessStarting:
			return runnerStartingBlock(), true
		case execadapter.ExecutorReadinessUnavailable, execadapter.ExecutorReadinessClosed:
			return runnerUnavailableBlock(), true
		}
	}
	return dispatchBlock{}, false
}

// markBlockedTasks 把与派发候选同一 Run 闸门下的 QUEUED/WAITING_RESOURCE 任务
// 批量写入等待原因。候选 ID 在事务外读取，事务内重读并用状态版本 CAS 更新；
// 同一安全原因不重复写库或追加事件。存储失败时保持未派发状态，由下一轮重试。
func (s *Scheduler) markBlockedTasks(ctx context.Context, block dispatchBlock) {
	candidates, err := s.store.ListWaitingResourceCandidateIDs(ctx)
	if err != nil {
		s.logError("scheduler", "scheduler.waiting_scan_failed", err)
		return
	}
	if len(candidates) == 0 {
		return
	}
	var events []domain.DomainEvent
	err = s.store.DoTx(ctx, func(tx *store.Tx) error {
		now := s.Now()
		// 同一事务内 Run 状态是同一快照：按 Run 缓存派发闸门，避免每个任务
		// 重复读取 Run + 节点。导出与无 Run 任务仍按各自例外放行。
		runAllows := make(map[domain.ID]bool)
		for _, candidateID := range candidates {
			current, err := tx.GetTask(ctx, candidateID)
			if err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					continue
				}
				return err
			}
			if current.Status != domain.TaskStatusQueued &&
				current.Status != domain.TaskStatusWaitingResource {
				continue
			}
			// 同原因等待不会有任何写入；先于 Run 闸门跳过，避免稳态扫描
			// 为不会发生的更新重复读取 Run/节点。Run 不允许派发时同样跳过，
			// 因此该顺序不改变可观察行为。
			if current.Status == domain.TaskStatusWaitingResource &&
				sameErrorInfo(current.Error, block.errorInfo) {
				continue
			}
			allowed := true
			if current.Kind != domain.TaskKindExport && current.RunID != "" {
				if cached, ok := runAllows[current.RunID]; ok {
					allowed = cached
				} else {
					allowed, err = taskRunAllowsDispatch(ctx, tx, current)
					if err != nil {
						return err
					}
					runAllows[current.RunID] = allowed
				}
			}
			if !allowed {
				continue
			}
			updated, err := tx.UpdateTaskResult(ctx, current.ID, domain.TaskStatusWaitingResource,
				current.ArtifactIDs, block.errorInfo, current.StateVersion, now)
			if err != nil {
				if errors.Is(err, domain.ErrStateConflict) || errors.Is(err, domain.ErrInvalidTransition) {
					continue
				}
				return err
			}
			ev, err := s.appendEvent(ctx, tx, domain.DomainEvent{
				Type:         "task.waiting_resource",
				ProjectID:    updated.ProjectID,
				TaskID:       updated.ID,
				StateVersion: updated.StateVersion,
				Data:         map[string]any{"reason": block.reason},
			})
			if err != nil {
				return err
			}
			events = append(events, ev)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrSealed) {
			// 更新准备已封闸：拒绝写入等待原因，且不误报为扫描失败。
			return
		}
		s.logError("scheduler", "scheduler.waiting_update_failed", err)
		return
	}
	s.publish(events...)
}

// taskRunAllowsDispatch 在事务内复核任务来源 Run 的派发闸门。导出与无 Run 的
// 独立任务沿用既有交付例外；暂停/终止 Run 的模型任务保持原状态。
func taskRunAllowsDispatch(ctx context.Context, tx *store.Tx, task domain.Task) (bool, error) {
	if task.RunID == "" || task.Kind == domain.TaskKindExport {
		return true, nil
	}
	run, err := tx.GetWorkflowRun(ctx, task.RunID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return domain.RunAllowsDispatch(run.Status), nil
}

// safeWaitingErrorInfo 把派发前重检失败映射为可持久化的安全原因。已知领域
// 错误保留其安全错误码与消息键；未知错误固定通用描述，不把原始文本写进响应。
func safeWaitingErrorInfo(err error) *domain.ErrorInfo {
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		info := domainErr.Info()
		return &info
	}
	return &domain.ErrorInfo{
		Code:       domain.ErrorCodeCapabilityUnverified,
		MessageKey: "task.capability_not_ready",
		Retryable:  true,
		ActionIDs:  []domain.ID{},
	}
}

// sameErrorInfo 比较安全错误结构（码、消息键、可重试性与动作集合），不比较
// 原始异常字符串。动作按集合比较，避免顺序变化制造虚假的“原因变化”。
func sameErrorInfo(a, b *domain.ErrorInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Code == b.Code && a.MessageKey == b.MessageKey &&
		a.Retryable == b.Retryable && sameIDs(a.ActionIDs, b.ActionIDs)
}

func sameIDs(a, b []domain.ID) bool {
	if len(a) != len(b) {
		return false
	}
	left := append([]domain.ID(nil), a...)
	right := append([]domain.ID(nil), b...)
	sort.Slice(left, func(i, j int) bool { return left[i] < left[j] })
	sort.Slice(right, func(i, j int) bool { return right[i] < right[j] })
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
