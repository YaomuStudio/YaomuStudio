// Package scheduler 是 Core 的执行调度：单重型槽（可配置）、派发事务、
// 暂停收敛、取消与重启恢复。
//
// 边界（docs/local-api-contract.md §8、docs/workflow-and-review.md §6）：
//   - 只有 QUEUED 且所属 Run 允许派发的任务会被选中；
//   - 任务状态、尝试、关键事件在同一事务提交后才执行；内存队列只负责唤醒；
//   - 成功/取消/提交由事务比较状态版本决定，迟到结果（旧 epoch/attempt）不改终态；
//   - 确认执行进程退出前不释放资源槽。
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/events"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/resultcommit"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// Executor 执行一次尝试；实现可以是 execadapter.Executor 或测试替身。
type Executor interface {
	Execute(ctx context.Context, req execadapter.Request, progress execadapter.ProgressFunc, logf execadapter.LogFunc) execadapter.Result
}

// PreparedResult 是节点/句段任务成功前的产物提交计划。
//
// PrepareResult 在事务外完成文件校验与落盘；Commit 在任务终态事务内登记
// Asset/ArtifactCommit、回写字幕轨/候选并推进节点。任何一步失败都回滚终态，
// 不允许出现“任务 SUCCEEDED 但产物未登记”。
//
// 契约定义在中立包，避免 service 反向依赖调度器。
type PreparedResult = resultcommit.Prepared

// Hooks 是任务终态前后的业务钩子（节点状态、审核门槛、导出记录）。
//
// PrepareResult covers every artifact-producing task, including exports.
type Hooks struct {
	// PrepareExecution validates/stages frozen inputs outside the dispatch
	// transaction. Returned parameters are still filtered before Runner transport.
	PrepareExecution func(context.Context, domain.Task) (map[string]any, error)
	PrepareResult    func(ctx context.Context, task domain.Task, result resultcommit.ExecutionResult) (PreparedResult, error)
	OnTaskSucceeded  func(ctx context.Context, task domain.Task, result resultcommit.ExecutionResult) error
	OnTaskFailed     func(ctx context.Context, task domain.Task, info *domain.ErrorInfo) error
	// OnTaskCanceled 在取消终态提交后调用（导出记录需要收敛，不当作失败）。
	OnTaskCanceled func(ctx context.Context, task domain.Task) error
}

// Options 是调度器构造参数。
type Options struct {
	Store    *store.Store
	Bus      *events.Bus
	Log      *logging.Logger
	Executor Executor
	Hooks    Hooks
	// PreDispatch 在派发前重检动态条件（能力/资源）；返回错误时任务转入
	// WAITING_RESOURCE 等待下一轮，不派发执行器。
	PreDispatch func(ctx context.Context, tx *store.Tx, task domain.Task) error
	Slots       int
	WorkspaceID string
	Epoch       int64
	WorkDir     string
	// DataDir 是应用数据目录；执行过程日志写入 {DataDir}/cache/execution-logs。
	// 为空时不落盘，只发布可丢弃实时事件。
	DataDir string
	Now     func() time.Time
	// TickInterval 是暂停收敛与恢复扫描的兜底间隔。
	TickInterval time.Duration
}

// Scheduler 是内存唤醒 + 数据库事实的调度器。
type Scheduler struct {
	store       *store.Store
	bus         *events.Bus
	log         *logging.Logger
	executor    Executor
	hooks       Hooks
	preDispatch func(ctx context.Context, tx *store.Tx, task domain.Task) error
	epoch       int64
	workspace   string
	workDir     string
	dataDir     string
	now         func() time.Time
	tick        time.Duration

	slots chan struct{}
	// exportSlots 是导出任务的执行器级互斥：Runner 单执行，同一时刻只派发
	// 一个导出；其余导出保持在 QUEUED，完成后再取（不丢弃任务）。
	exportSlots chan struct{}
	wake        chan struct{}

	mu           sync.Mutex
	running      map[domain.ID]context.CancelFunc
	isolationErr error

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed sync.Once
}

// New 构造调度器；Slots<=0 时使用 1。
func New(opts Options) *Scheduler {
	if opts.Slots <= 0 {
		opts.Slots = 1
	}
	if limited, ok := opts.Executor.(interface{ Capacity() int }); ok {
		if capacity := limited.Capacity(); capacity > 0 && capacity < opts.Slots {
			opts.Slots = capacity
		}
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.TickInterval <= 0 {
		// 兜底扫描间隔；新工作由 Notify 立即唤醒，不依赖轮询。
		opts.TickInterval = time.Second
	}
	return &Scheduler{
		store:       opts.Store,
		bus:         opts.Bus,
		log:         opts.Log,
		executor:    opts.Executor,
		hooks:       opts.Hooks,
		preDispatch: opts.PreDispatch,
		epoch:       opts.Epoch,
		workspace:   opts.WorkspaceID,
		workDir:     opts.WorkDir,
		dataDir:     opts.DataDir,
		now:         opts.Now,
		tick:        opts.TickInterval,
		slots:       make(chan struct{}, opts.Slots),
		exportSlots: make(chan struct{}, 1),
		wake:        make(chan struct{}, 1),
		running:     make(map[domain.ID]context.CancelFunc),
	}
}

// Notify 唤醒调度循环（非阻塞）。
func (s *Scheduler) Notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Start 恢复遗留状态并启动调度循环。
//
// 恢复是启动拥有者必须等待的可靠 Core 准备：存储恢复失败时返回错误且不启动
// 循环，调用方不得监听业务 HTTP 或派发新任务。数据库事实保持原样，留给下一次
// 启动或人工恢复。
func (s *Scheduler) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)
	if err := s.recoverInterrupted(s.ctx); err != nil {
		s.cancel()
		return fmt.Errorf("recover interrupted tasks: %w", err)
	}
	s.wg.Add(1)
	go s.loop()
	return nil
}

// Close 停止调度循环并等待退出（有界；既有调用方不需要收敛错误）。
func (s *Scheduler) Close(ctx context.Context) {
	_ = s.Stop(ctx)
}

// Stop 停止调度循环并等待全部执行 goroutine 收敛。
//
// 返回错误表示在 ctx 预算内未能确认停止：调用方不得据此声称清理完成，
// 也不得释放未确认的执行槽或租约。重复调用共享同一取消与等待。
func (s *Scheduler) Stop(ctx context.Context) error {
	s.closed.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("scheduler shutdown did not converge: %w", ctx.Err())
	}
}

// Idle 报告调度器当前不拥有任何执行或预留：无运行中尝试、无占用槽位、
// 无导出互斥持有。它是更新准备封闸前的内存拥有者事实，不读取数据库。
func (s *Scheduler) Idle() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	running := len(s.running)
	s.mu.Unlock()
	return running == 0 && len(s.slots) == 0 && len(s.exportSlots) == 0
}

func (s *Scheduler) loop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
		s.settlePauses(s.ctx)
		s.dispatch(s.ctx)
	}
}

// ---- 重启恢复 ----

// recoverInterrupted 处理上一次 Core 实例遗留的任务：
// RUNNING/CANCEL_REQUESTED → INTERRUPTED（首版等待用户确认恢复，不自动重跑），
// WAITING_RESOURCE → FAILED（可重试，不自动重复推理），PAUSE_REQUESTED → PAUSED。
//
// 存储恢复失败必须返回给启动拥有者：Core 不能在任务事实未收敛时对外提供业务
// 控制面或开始新派发。导出记录的收敛仍按既有职责只记录日志，不改变恢复事务。
func (s *Scheduler) recoverInterrupted(ctx context.Context) error {
	var events []domain.DomainEvent
	var interruptedExports []domain.Task
	err := s.store.DoTx(ctx, func(tx *store.Tx) error {
		now := s.Now()
		statuses := []domain.TaskStatus{
			domain.TaskStatusRunning,
			domain.TaskStatusCancelRequested,
			domain.TaskStatusWaitingResource,
			domain.TaskStatusPauseRequested,
		}
		for _, status := range statuses {
			tasks, err := tx.ListTasksByStatus(ctx, status)
			if err != nil {
				return err
			}
			for _, task := range tasks {
				target := domain.TaskStatusInterrupted
				switch status {
				case domain.TaskStatusWaitingResource:
					// WAITING_RESOURCE 没有直接回到 QUEUED 的合法迁移；
					// 重启后标记为可重试失败，由用户显式重试（不自动重复推理）。
					target = domain.TaskStatusFailed
				case domain.TaskStatusPauseRequested:
					target = domain.TaskStatusPaused
				}
				info := &domain.ErrorInfo{
					Code:       domain.ErrorCodeWorkerExited,
					MessageKey: "task.worker_lost_on_restart",
					Retryable:  true,
					ActionIDs:  []domain.ID{"retry_task"},
				}
				updated, err := tx.UpdateTaskResult(ctx, task.ID, target, task.ArtifactIDs, info, task.StateVersion, now)
				if err != nil {
					if errors.Is(err, domain.ErrInvalidTransition) || errors.Is(err, domain.ErrStateConflict) {
						continue
					}
					return err
				}
				if attempt, ok, err := tx.LatestAttempt(ctx, task.ID); err != nil {
					return err
				} else if ok && !attempt.Status.Terminal() {
					if err := tx.UpdateAttempt(ctx, attempt.ID, domain.StepStatusInterrupted, nil, &now); err != nil && !errors.Is(err, domain.ErrNotFound) {
						return err
					}
				}
				ev, err := s.appendEvent(ctx, tx, domain.DomainEvent{
					Type:         "task.interrupted",
					ProjectID:    updated.ProjectID,
					TaskID:       updated.ID,
					StateVersion: updated.StateVersion,
					Data:         map[string]any{"reason": "core_restart"},
				})
				if err != nil {
					return err
				}
				events = append(events, ev)
				if updated.Kind == domain.TaskKindExport {
					interruptedExports = append(interruptedExports, updated)
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store recovery: %w", err)
	}
	s.publish(events...)
	// 恢复中失败的导出需要收敛导出记录（节点任务的失败标记维持既有不自动重跑语义）。
	info := &domain.ErrorInfo{
		Code:       domain.ErrorCodeWorkerExited,
		MessageKey: "task.worker_lost_on_restart",
		Retryable:  true,
		ActionIDs:  []domain.ID{"retry_task"},
	}
	for _, task := range interruptedExports {
		if s.hooks.OnTaskFailed != nil {
			if err := s.hooks.OnTaskFailed(ctx, task, info); err != nil {
				s.logError("scheduler", "scheduler.recover_export_failed", err)
			}
		}
	}
	if len(events) > 0 {
		s.logInfo("scheduler", "scheduler.recovered", map[string]any{"events": len(events)})
	}
	return nil
}

// ---- 暂停收敛 ----

// settlePauses 把无执行中任务的 PAUSE_REQUESTED Run 收敛为 PAUSED，并挂起排队任务。
func (s *Scheduler) settlePauses(ctx context.Context) {
	refs, err := s.store.ListRunRefsByStatus(ctx, domain.RunStatusPauseRequested)
	if err != nil {
		s.logError("scheduler", "scheduler.pause_scan_failed", err)
		return
	}
	for _, ref := range refs {
		running, err := s.store.ListTasksByStatuses(ctx, ref.ID,
			domain.TaskStatusRunning, domain.TaskStatusCancelRequested)
		if err != nil {
			s.logError("scheduler", "scheduler.pause_scan_failed", err)
			continue
		}
		modelRunning := 0
		for _, task := range running {
			if task.Kind != domain.TaskKindExport {
				modelRunning++
			}
		}
		if modelRunning > 0 {
			continue // 安全单元边界尚未到达（导出不算模型执行单元）。
		}
		var events []domain.DomainEvent
		err = s.store.DoTx(ctx, func(tx *store.Tx) error {
			now := s.Now()
			parked, err := tx.ListTasksByStatuses(ctx, ref.ID,
				domain.TaskStatusQueued, domain.TaskStatusWaitingResource,
				domain.TaskStatusPauseRequested)
			if err != nil {
				return err
			}
			for _, task := range parked {
				// 导出是交付动作，不受 Run 暂停影响；只挂起模型/节点任务。
				if task.Kind == domain.TaskKindExport {
					continue
				}
				current := task
				if current.Status == domain.TaskStatusQueued ||
					current.Status == domain.TaskStatusWaitingResource {
					// 暂停收敛与取消/恢复一致：PAUSED 不保留旧等待原因。
					next, err := tx.UpdateTaskResult(ctx, current.ID, domain.TaskStatusPauseRequested,
						current.ArtifactIDs, nil, current.StateVersion, now)
					if err != nil {
						return err
					}
					current = next
				}
				if current.Status == domain.TaskStatusPauseRequested {
					if _, err := tx.UpdateTaskResult(ctx, current.ID, domain.TaskStatusPaused,
						current.ArtifactIDs, nil, current.StateVersion, now); err != nil {
						return err
					}
				}
			}
			run, err := tx.GetWorkflowRun(ctx, ref.ID)
			if err != nil {
				return err
			}
			if run.Status != domain.RunStatusPauseRequested {
				return nil
			}
			paused, err := tx.UpdateRunStatus(ctx, ref.ID, domain.RunStatusPaused, run.StateVersion, now)
			if err != nil {
				return err
			}
			ev, err := s.appendEvent(ctx, tx, domain.DomainEvent{
				Type:         "workflow.paused",
				ProjectID:    paused.ProjectID,
				StateVersion: paused.StateVersion,
				Data:         map[string]any{"run_id": paused.ID},
			})
			if err != nil {
				return err
			}
			events = append(events, ev)
			return nil
		})
		if err != nil {
			if errors.Is(err, store.ErrSealed) {
				// 更新准备已封闸：暂停收敛不得再写业务事实。
				return
			}
			// 并发状态变化（CAS 冲突）留给下一轮 tick 重试。
			if !errors.Is(err, domain.ErrStateConflict) && !errors.Is(err, domain.ErrInvalidTransition) {
				s.logError("scheduler", "scheduler.pause_settle_failed", err)
			}
			continue
		}
		s.publish(events...)
	}
}

// ---- 派发 ----

var (
	errSkip            = errors.New("scheduler: skip task")
	errDispatchBlocked = errors.New("scheduler: dispatch blocked by executor isolation")
)

// claim 在单事务内完成派发闸门：Run 状态版本比较 → 允许派发 → 任务 RUNNING、
// 节点 running、尝试 RUNNING 与关键事件。
func (s *Scheduler) claim(ctx context.Context, taskID domain.ID) (*claim, bool, error) {
	var out *claim
	var skippedEvent *domain.DomainEvent
	err := s.store.DoTx(ctx, func(tx *store.Tx) error {
		// 事务开始等待连接期间隔离可能才落定：在写入任何派发事实前复查。
		if _, blocked := s.currentDispatchBlock(); blocked {
			return errDispatchBlocked
		}
		task, err := tx.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if task.Status != domain.TaskStatusQueued && task.Status != domain.TaskStatusWaitingResource {
			return errSkip
		}
		var run *domain.WorkflowRun
		// 导出是交付动作，不受来源 Run 的暂停/终止闸门约束（不占用模型槽）。
		if task.RunID != "" && task.Kind != domain.TaskKindExport {
			current, err := tx.GetWorkflowRun(ctx, task.RunID)
			if err != nil {
				return err
			}
			if !domain.RunAllowsDispatch(current.Status) {
				// 已成功的专属链路/克隆工具可以补跑缺失的完整音轨，不重开 Run。
				if current.Status != domain.RunStatusSucceeded || !taskAssemblesCloneAudio(task) {
					return errSkip
				}
			}
			run = &current
			if current.Status == domain.RunStatusQueued {
				updated, err := tx.UpdateRunStatus(ctx, current.ID, domain.RunStatusRunning, current.StateVersion, s.Now())
				if err != nil {
					return err
				}
				run = &updated
			}
		}
		if s.preDispatch != nil {
			if err := s.preDispatch(ctx, tx, task); err != nil {
				// 等待原因必须与状态同事务提交；同一安全原因不重复写库。
				info := safeWaitingErrorInfo(err)
				if task.Status == domain.TaskStatusQueued || !sameErrorInfo(task.Error, info) {
					now := s.Now()
					updated, updateErr := tx.UpdateTaskResult(ctx, task.ID,
						domain.TaskStatusWaitingResource, task.ArtifactIDs, info,
						task.StateVersion, now)
					if updateErr != nil {
						return updateErr
					}
					ev, appendErr := s.appendEvent(ctx, tx, domain.DomainEvent{
						Type:         "task.waiting_resource",
						ProjectID:    updated.ProjectID,
						TaskID:       updated.ID,
						StateVersion: updated.StateVersion,
						Data:         map[string]any{"reason": waitingReasonPreDispatch},
					})
					if appendErr != nil {
						return appendErr
					}
					skippedEvent = &ev
				}
				// 状态变更与事件必须提交，因此这里不能返回错误（会回滚）。
				return nil
			}
		}
		attempts, err := tx.ListAttempts(ctx, taskID)
		if err != nil {
			return err
		}
		if len(attempts) == 0 {
			if err := tx.CreateAttempt(ctx, domain.Attempt{
				ID:        store.NewID("attempt"),
				TaskID:    taskID,
				StepID:    stepIDOf(task),
				AttemptNo: 1,
				Epoch:     s.epoch,
				Status:    domain.StepStatusPending,
			}); err != nil {
				return err
			}
			attempts, err = tx.ListAttempts(ctx, taskID)
			if err != nil {
				return err
			}
		}
		attempt := attempts[len(attempts)-1]
		if attempt.Status != domain.StepStatusPending {
			return errSkip
		}
		// preDispatch 可能包含查询/IO；提交 RUNNING 前再复查一次隔离，避免在
		// 阻断落定后仍认领任务。这是尽力收敛，不是执行器协议的完全线性化。
		if _, blocked := s.currentDispatchBlock(); blocked {
			return errDispatchBlocked
		}
		now := s.Now()
		// 恢复派发时清除等待原因：RUNNING 的任务不能再显示“仍在等待资源”。
		updated, err := tx.UpdateTaskResult(ctx, taskID, domain.TaskStatusRunning,
			task.ArtifactIDs, nil, task.StateVersion, now)
		if err != nil {
			return err
		}
		steps := make([]domain.TaskStep, len(updated.Steps))
		copy(steps, updated.Steps)
		for i := range steps {
			if steps[i].Status == domain.StepStatusPending {
				steps[i].Status = domain.StepStatusRunning
				steps[i].AttemptNo = attempt.AttemptNo
				steps[i].Epoch = s.epoch
			}
		}
		if err := tx.SetTaskSteps(ctx, taskID, steps, now); err != nil {
			return err
		}
		if err := tx.UpdateAttempt(ctx, attempt.ID, domain.StepStatusRunning, &now, nil); err != nil {
			return err
		}
		executorID := ""
		if identified, ok := s.executor.(interface{ Identity() string }); ok {
			executorID = identified.Identity()
		}
		if err := tx.BindAttemptExecutor(ctx, attempt.ID, s.epoch, executorID); err != nil {
			return err
		}
		attempt.Epoch = s.epoch
		attempt.ExecutorID = executorID
		if run != nil && task.NodeKey != "" &&
			(task.Kind == domain.TaskKindNode || task.Kind == domain.TaskKindSegment) {
			if nodeStatus(*run, task.NodeKey) == domain.NodeStatusPending {
				if err := tx.UpdateRunNode(ctx, run.ID, task.NodeKey, domain.NodeStatusRunning, 0, ""); err != nil {
					return err
				}
			}
		}
		ev, err := s.appendEvent(ctx, tx, domain.DomainEvent{
			Type:         "task.started",
			ProjectID:    updated.ProjectID,
			TaskID:       updated.ID,
			StateVersion: updated.StateVersion,
			Data: map[string]any{
				"attempt_no": attempt.AttemptNo,
				"epoch":      s.epoch,
			},
		})
		if err != nil {
			return err
		}
		out = &claim{
			task:      updated,
			attempt:   attempt,
			runID:     updated.RunID,
			event:     ev,
			nodeKey:   updated.NodeKey,
			projectID: updated.ProjectID,
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errSkip) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if out == nil {
		// 预检未通过：WAITING_RESOURCE 已提交，发布事件但不派发。
		if skippedEvent != nil {
			s.publish(*skippedEvent)
		}
		return nil, false, nil
	}
	return out, true, nil
}

type claim struct {
	task      domain.Task
	attempt   domain.Attempt
	runID     domain.ID
	nodeKey   domain.PipelineNodeKey
	projectID domain.ID
	event     domain.DomainEvent
}

// dispatch 在有空槽时派发可执行任务。
//
// 导出串行化：同一执行器（Runner）同一时刻只派发一个导出。已有导出在执行时，
// 用 NextDispatchableNonExportTask 跳过排队中的导出（避免阻塞其他任务），
// 导出完成后由 execute→Notify 唤醒再取；未派发的导出保持 QUEUED。
func (s *Scheduler) dispatch(ctx context.Context) {
	if block, blocked := s.currentDispatchBlock(); blocked {
		s.markBlockedTasks(ctx, block)
		return
	}
	attempted := make(map[domain.ID]struct{})
	for {
		select {
		case s.slots <- struct{}{}:
		default:
			return
		}
		// 入口检查后槽位可能与执行完成同时变化：每轮认领前再复查隔离，
		// 避免同一 dispatch 调用在已释放槽后继续认领。隔离是永久的，
		// 因此这里提前返回不会漏掉后续恢复扫描。
		if block, blocked := s.currentDispatchBlock(); blocked {
			<-s.slots
			s.markBlockedTasks(ctx, block)
			return
		}
		var (
			task domain.Task
			ok   bool
			err  error
		)
		if s.exportBusy() {
			task, ok, err = s.store.NextDispatchableNonExportTask(ctx)
		} else {
			task, ok, err = s.store.NextDispatchableTask(ctx)
		}
		if err != nil {
			<-s.slots
			s.logError("scheduler", "scheduler.pick_failed", err)
			return
		}
		if !ok {
			<-s.slots
			return
		}
		if _, done := attempted[task.ID]; done {
			<-s.slots
			return
		}
		attempted[task.ID] = struct{}{}

		heldExport := false
		if task.Kind == domain.TaskKindExport {
			if !s.tryAcquireExport() {
				// 与 exportBusy 判断之间的兜底：保持 QUEUED，等执行完成唤醒。
				<-s.slots
				return
			}
			heldExport = true
		}

		cl, claimed, err := s.claim(ctx, task.ID)
		if err != nil {
			if heldExport {
				s.releaseExport()
			}
			<-s.slots
			if errors.Is(err, store.ErrSealed) {
				// 应用更新封闸：不认领、不写等待原因、不把它记成 claim 失败。
				return
			}
			if errors.Is(err, errDispatchBlocked) {
				if block, blocked := s.currentDispatchBlock(); blocked {
					s.markBlockedTasks(ctx, block)
				}
				return
			}
			s.logError("scheduler", "scheduler.claim_failed", err)
			return
		}
		if !claimed {
			if heldExport {
				s.releaseExport()
			}
			<-s.slots
			continue
		}
		s.publish(cl.event)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			if !s.execute(ctx, cl) {
				return // retain both reservations while the execution tree is unknown
			}
			<-s.slots
			if heldExport {
				s.releaseExport()
			}
			s.Notify()
		}()
	}
}

// exportBusy 报告是否已有导出在执行（调度循环单 goroutine 调用）。
func (s *Scheduler) exportBusy() bool { return len(s.exportSlots) > 0 }

func (s *Scheduler) tryAcquireExport() bool {
	select {
	case s.exportSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Scheduler) releaseExport() { <-s.exportSlots }

// execute 运行一次尝试并在事务内提交终态。
func (s *Scheduler) execute(parent context.Context, cl *claim) bool {
	ctx, cancel := context.WithCancel(parent)
	stopped := true
	s.mu.Lock()
	s.running[cl.task.ID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if stopped {
			delete(s.running, cl.task.ID)
		}
		s.mu.Unlock()
		cancel()
	}()

	outputDir := filepath.Join(s.workDir, cl.task.ID, fmt.Sprintf("attempt-%d", cl.attempt.AttemptNo))
	if s.workDir != "" {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			s.logError("scheduler", "scheduler.output_dir_failed", err)
			s.executionPreparationFailed(cl, err)
			return true
		}
	}
	adapter, runtime, model := adapterForTask(cl.task)
	params := cl.task.Params
	if s.hooks.PrepareExecution != nil {
		preparedParams, err := s.hooks.PrepareExecution(ctx, cl.task)
		if err != nil {
			s.executionPreparationFailed(cl, err)
			return true
		}
		params = preparedParams
	}
	params = runnerParamsFor(adapter.ID, params)
	if cl.task.Kind == domain.TaskKindExport || cl.task.NodeKey == domain.NodeKeyMix {
		// 导出走受控 FFmpeg 适配器；execute params 是适配器私有 Schema，
		// service 把执行计划放在 task.Params["plan"]，其余键仅作控制面元数据。
		if plan, ok := params["plan"].(map[string]any); ok {
			params = runnerParamsFor(adapter.ID, plan)
		} else {
			params = nil
		}
	}
	// separate / voice_clone mock 需要绝对路径 input；从冻结输入资产投影，不猜 FlashCLI argv。
	params = projectFileInputParam(parent, s.store, cl.task, adapter.ID, params)
	req := execadapter.Request{
		Workspace:   s.workspace,
		ServerEpoch: s.epoch,
		TaskID:      cl.task.ID,
		StepID:      cl.attempt.StepID,
		Attempt:     cl.attempt.AttemptNo,
		Model:       model,
		Adapter:     adapter,
		Runtime:     runtime,
		Params:      params,
		// 输入绑定使用冻结的输入 ID（资产或字幕 revision）；Runner 只透传绑定，
		// 不解释具体类型，真实适配器接入时再按资产投影。
		InputAssetIDs: toStrings(cl.task.InputRevisionIDs),
		OutputDir:     outputDir,
	}
	progress := newProgressReporter(ctx, s.store, s.bus, s.log, cl, s.now)
	// 兜底关闭；正常路径在 Execute 返回后、任何终态/prepare 之前立即关闭。
	defer progress.Close()
	logDir := s.dataDir
	if logDir == "" && s.workDir != "" {
		// workDir 约定为 {dataDir}/work；兼容未显式配置 DataDir 的调用方。
		logDir = filepath.Dir(s.workDir)
	}
	logs := newExecutionLogReporter(logDir, s.bus, s.log, cl.task.ID, cl.attempt.AttemptNo, s.now)
	defer logs.Close()
	result := s.executor.Execute(ctx, req, progress.report, logs.report)
	progress.Close()
	if result.StopUnconfirmed {
		stopped = false
		s.isolate(errors.Join(result.TransportErr, execadapter.ErrStopUnconfirmed))
		return false
	}
	// A newer attempt may already own the task. In that case this request's
	// mismatched terminal is old evidence and must not settle the new attempt.
	latest, found, err := s.store.LatestAttempt(context.Background(), cl.task.ID)
	if err != nil || !found {
		s.logError("scheduler", "scheduler.result_attempt_check_failed", errors.Join(err, domain.ErrNotFound))
		return true
	}
	if latest.ID != cl.attempt.ID || latest.Epoch != s.epoch || latest.Status.Terminal() {
		return true
	}
	if result.TransportErr == nil && !result.Binding.Matches(req) {
		s.logWarn("scheduler", "scheduler.stale_result_dropped", map[string]any{
			"task_id": cl.task.ID, "attempt_no": cl.attempt.AttemptNo,
			"result_task": result.Binding.TaskID, "result_attempt": result.Binding.Attempt,
			"result_epoch": result.Binding.ServerEpoch, "result_step": result.Binding.StepID,
		})
		s.complete(cl, execadapter.Result{
			Error: &execadapter.ExecError{
				Code:      string(domain.ErrorCodeInvalidRequest),
				Message:   "execution result binding does not match the active attempt",
				Retryable: false,
			},
			Binding: execadapter.Binding{
				Workspace: s.workspace, ServerEpoch: s.epoch, TaskID: cl.task.ID,
				StepID: cl.attempt.StepID, Attempt: cl.attempt.AttemptNo,
			},
		}, nil)
		return true
	}
	if result.OutputDir == "" {
		result.OutputDir = outputDir
	}
	var prepared PreparedResult
	if result.Error == nil && result.TransportErr == nil &&
		s.hooks.PrepareResult != nil {
		preparedResult, err := s.hooks.PrepareResult(ctx, cl.task, executionResultFor(result))
		if err != nil {
			s.logWarn("scheduler", "scheduler.result_prepare_failed", map[string]any{
				"task_id": cl.task.ID,
				"error":   err.Error(),
			})
			result = execadapter.Result{
				Error:       prepareFailure(err),
				Binding:     result.Binding,
				OutputDir:   result.OutputDir,
				Outputs:     nil,
				ArtifactIDs: nil,
			}
		} else if preparedResult != nil {
			prepared = preparedResult
			result.ArtifactIDs = idsToStrings(preparedResult.ArtifactIDs())
		}
	}
	s.complete(cl, result, prepared)
	s.Notify()
	return true
}

func (s *Scheduler) executionPreparationFailed(cl *claim, err error) {
	s.logWarn("scheduler", "scheduler.execution_prepare_failed", map[string]any{
		"task_id": cl.task.ID, "error": err.Error(),
	})
	s.complete(cl, execadapter.Result{
		Error: prepareFailure(err),
		Binding: execadapter.Binding{
			Workspace: s.workspace, ServerEpoch: s.epoch, TaskID: cl.task.ID,
			StepID: cl.attempt.StepID, Attempt: cl.attempt.AttemptNo,
		},
	}, nil)
}

// coreRunnerParamKeys 是 Core 冻结参数中的控制面字段：用于预检、重检与
// 输入编排，但不属于 Runner Adapter 的参数白名单，必须在 wire 投影时剥离。
var coreRunnerParamKeys = map[string]struct{}{
	"_core":                 {},
	"device_id":             {},
	"voice_source_asset_id": {},
}

// runnerParamsFor 把冻结任务参数投影到 Runner Adapter 的参数契约。
// device_id 是 Core 的引擎配置而非 Runner 参数；cuda:N 按 Runner 契约落到
// params.device.cuda_visible_devices，其余设备不附加 device（不猜测映射）。
func runnerParamsFor(adapterID string, params map[string]any) map[string]any {
	out := make(map[string]any, len(params))
	for key, value := range params {
		if _, internal := coreRunnerParamKeys[key]; internal {
			continue
		}
		out[key] = value
	}
	if adapterID == separateAdapterID || adapterID == voiceCloneAdapterID || adapterID == asrAdapterID {
		if deviceID, ok := params["device_id"].(string); ok {
			if visibleDevices, ok := cudaVisibleDevices(deviceID); ok {
				out["device"] = map[string]any{"cuda_visible_devices": visibleDevices}
			}
		}
	}
	if adapterID == voiceCloneAdapterID {
		// 07 的句级 revision 与运行选项属于 Core 控制面；MVP 适配器只接受
		// README 已确认的业务字段，尚不实现时长动态调整/时间戳校验。
		delete(out, "content_revision")
		delete(out, "adjust_duration")
		delete(out, "validate_timestamps")
	}
	if adapterID == ffmpegAdapterID {
		// language 用来区分任务身份，不是 ffmpeg 混音参数。
		delete(out, "language")
	}
	return out
}

// cudaVisibleDevices 把 Core 设备 ID（cuda:N）转换为 Runner 的
// CUDA_VISIBLE_DEVICES 值；cpu/mps/auto 等不绑定 CUDA。
func cudaVisibleDevices(deviceID string) (string, bool) {
	const prefix = "cuda:"
	if !strings.HasPrefix(deviceID, prefix) {
		return "", false
	}
	index := strings.TrimPrefix(deviceID, prefix)
	if index == "" {
		return "", false
	}
	for _, ch := range index {
		if ch < '0' || ch > '9' {
			return "", false
		}
	}
	return index, true
}

func (s *Scheduler) isolate(err error) {
	s.mu.Lock()
	first := s.isolationErr == nil
	s.isolationErr = err
	s.mu.Unlock()
	if first {
		s.logError("scheduler", "scheduler.executor_isolated", err)
	}
}

// Isolated reports that execution cannot safely resume in this Core instance.
func (s *Scheduler) Isolated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isolationErr != nil
}

// prepareFailure 把准备阶段错误映射为安全执行错误；原始细节只进本地日志。
func prepareFailure(err error) *execadapter.ExecError {
	code := string(domain.ErrorCodeWorkerExited)
	message := "result artifact preparation failed"
	switch {
	case errors.Is(err, domain.ErrCloneTimelineOutOfRange):
		return &execadapter.ExecError{Code: "CLONE_TIMELINE_OUT_OF_RANGE", Message: "frozen clone timeline is outside the media duration", Retryable: false}
	case errors.Is(err, domain.ErrCloneTimelineUnknownDuration):
		return &execadapter.ExecError{Code: "CLONE_TIMELINE_UNKNOWN_DURATION", Message: "frozen clone media duration is unknown", Retryable: false}
	case errors.Is(err, domain.ErrInvalidRequest):
		code = string(domain.ErrorCodeInvalidRequest)
		message = "invalid result artifacts"
	}
	return &execadapter.ExecError{Code: code, Message: message, Retryable: false}
}

// idsToStrings 把领域 ID 列表转换为执行协议字符串列表。
func idsToStrings(ids []domain.ID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

// 执行适配器标识（与 runner/src/yaomustudio_runner 对端一致）。
const (
	ffmpegAdapterID         = "ffmpeg"
	ffmpegAdapterDigest     = "ffmpeg-whitelist-v1"
	flashcliAdapterID       = "flashcli"
	flashcliAdapterDigest   = "unlocked"
	separateAdapterID       = "separate"
	separateAdapterDigest   = "separate-mock-v1"
	voiceCloneAdapterID     = "voice_clone"
	voiceCloneAdapterDigest = "voice-clone-mock-v1"
	translateAdapterID      = "translate"
	translateAdapterDigest  = "mlstools-subtitle-v1"
	asrAdapterID            = "asr"
	asrAdapterDigest        = "asr-flashcli-v1"
)

func componentRef(lock *domain.ReleaseRef, fallbackID string) execadapter.ComponentRef {
	if lock == nil {
		return execadapter.ComponentRef{ID: fallbackID, Digest: "unlocked"}
	}
	ref := execadapter.ComponentRef{ID: lock.ReleaseID, Digest: lock.Digest}
	if ref.ID == "" {
		ref.ID = fallbackID
	}
	if ref.Digest == "" {
		ref.Digest = "unlocked"
	}
	return ref
}

// projectFileInputParam 把首个输入资产路径写入 params.input（若尚未设置）。
// separate / voice_clone / asr 需要绝对路径输入；asr 的输入是原始媒体或
// 分离后的人声资产，由 Core 冻结的 InputRevisionIDs 顺序决定。
func projectFileInputParam(
	ctx context.Context,
	st *store.Store,
	task domain.Task,
	adapterID string,
	params map[string]any,
) map[string]any {
	switch adapterID {
	case separateAdapterID, voiceCloneAdapterID, asrAdapterID:
	default:
		return params
	}
	if st == nil || len(task.InputRevisionIDs) == 0 {
		return params
	}
	out := params
	if out == nil {
		out = map[string]any{}
	} else {
		cloned := make(map[string]any, len(params)+1)
		for k, v := range params {
			cloned[k] = v
		}
		out = cloned
	}
	if raw, ok := out["input"].(string); ok && strings.TrimSpace(raw) != "" {
		return out
	}
	asset, err := st.GetAsset(ctx, task.InputRevisionIDs[0])
	if err != nil || strings.TrimSpace(asset.Path) == "" {
		return out
	}
	out["input"] = asset.Path
	return out
}

// taskAssemblesCloneAudio 识别把已完成句级配音铺到时间轴上的完整音轨任务。
// 它使用 ffmpeg，不再次调用克隆模型。
func taskAssemblesCloneAudio(task domain.Task) bool {
	if task.Kind != domain.TaskKindNode || task.NodeKey != domain.NodeKeyClone || task.SegmentID != "" {
		return false
	}
	op, _ := task.Params["op"].(string)
	return op == "assemble"
}

// adapterForTask 按任务类型选择执行适配器。
//
// 03 分离 → separate；07 克隆 / 句段任务 → voice_clone；06 翻译 → translate
// （mlstools 字幕翻译）；04 ASR 提取 → asr（直调 FlashCLI Qwen3-ASR）；
// 08 混音与导出走 ffmpeg。其余模型节点暂走 flashcli 门控桩。
// ModelLock 只作模型引用，不决定 adapter.id。
func adapterForTask(task domain.Task) (adapter, runtime, model execadapter.ComponentRef) {
	model = componentRef(task.ModelLock, "model")
	if task.Kind == domain.TaskKindExport || task.NodeKey == domain.NodeKeyMix || taskAssemblesCloneAudio(task) {
		ref := execadapter.ComponentRef{ID: ffmpegAdapterID, Digest: ffmpegAdapterDigest}
		return ref, ref, model
	}
	switch {
	case task.NodeKey == domain.NodeKeySeparate:
		ref := execadapter.ComponentRef{ID: separateAdapterID, Digest: separateAdapterDigest}
		return ref, ref, model
	case task.NodeKey == domain.NodeKeyClone || task.Kind == domain.TaskKindSegment:
		ref := execadapter.ComponentRef{ID: voiceCloneAdapterID, Digest: voiceCloneAdapterDigest}
		return ref, ref, model
	case task.Kind == domain.TaskKindNode && task.NodeKey == domain.NodeKeyTranslate:
		ref := execadapter.ComponentRef{ID: translateAdapterID, Digest: translateAdapterDigest}
		return ref, ref, model
	case task.NodeKey == domain.NodeKeyExtract && extractModeForTask(task) == string(domain.ExtractModeASR):
		ref := execadapter.ComponentRef{ID: asrAdapterID, Digest: asrAdapterDigest}
		return ref, ref, model
	default:
		ref := execadapter.ComponentRef{ID: flashcliAdapterID, Digest: flashcliAdapterDigest}
		return ref, ref, model
	}
}

// extractModeForTask 返回 04 的提取模式；未冻结 ExtractOptions 时按默认 ASR。
func extractModeForTask(task domain.Task) string {
	raw, ok := task.Params["extract"]
	if !ok || raw == nil {
		return string(domain.ExtractModeASR)
	}
	switch value := raw.(type) {
	case map[string]any:
		if mode, ok := value["mode"].(string); ok && mode != "" {
			return mode
		}
	case map[string]string:
		if mode := value["mode"]; mode != "" {
			return mode
		}
	}
	return string(domain.ExtractModeASR)
}

func toStrings(ids []domain.ID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

// executionResultFor 把执行器结果投影为业务提交层所需的最小视图。
//
// 协议错误、transport 状态、停止确认与产物 ID 覆盖只由调度层消费，不进入
// service；这里在钩子边界一次性完成转换，业务层不解析执行协议类型。
func executionResultFor(result execadapter.Result) resultcommit.ExecutionResult {
	var outputs []resultcommit.ExecutionOutput
	if result.Outputs != nil {
		outputs = make([]resultcommit.ExecutionOutput, len(result.Outputs))
		for i, output := range result.Outputs {
			outputs[i] = resultcommit.ExecutionOutput{
				RelPath:   output.RelPath,
				SizeBytes: output.SizeBytes,
				Media:     output.Media,
			}
		}
	}
	return resultcommit.ExecutionResult{
		Outputs:   outputs,
		OutputDir: result.OutputDir,
		Binding: resultcommit.ExecutionBinding{
			TaskID:  result.Binding.TaskID,
			StepID:  result.Binding.StepID,
			Attempt: result.Binding.Attempt,
		},
	}
}

// complete 在事务内提交终态；迟到结果（旧 epoch/attempt）被丢弃。
func (s *Scheduler) complete(cl *claim, result execadapter.Result, prepared PreparedResult) {
	ctx := context.Background()
	if result.StopUnconfirmed {
		return
	}
	if result.TransportErr == nil && !result.Binding.Matches(execadapter.Request{
		Workspace: s.workspace, ServerEpoch: s.epoch, TaskID: cl.task.ID,
		StepID: cl.attempt.StepID, Attempt: cl.attempt.AttemptNo,
	}) {
		return
	}

	var (
		events    []domain.DomainEvent
		target    domain.TaskStatus
		info      *domain.ErrorInfo
		hooked    bool
		artifacts []domain.ID
		finalTask domain.Task
	)
	err := s.store.DoTx(ctx, func(tx *store.Tx) error {
		task, err := tx.GetTask(ctx, cl.task.ID)
		if err != nil {
			return err
		}
		if task.Status != domain.TaskStatusRunning && task.Status != domain.TaskStatusCancelRequested {
			return errSkip
		}
		latest, found, err := tx.LatestAttempt(ctx, cl.task.ID)
		if err != nil {
			return err
		}
		if !found || latest.ID != cl.attempt.ID || latest.Epoch != s.epoch || latest.Status.Terminal() {
			return errSkip
		}
		now := s.Now()
		artifactIDs := make([]domain.ID, 0, len(result.ArtifactIDs))
		for _, id := range result.ArtifactIDs {
			artifactIDs = append(artifactIDs, domain.ID(id))
		}
		// 取消/失败事件的 data 也保持数组形状（契约 data 为对象，但客户端按数组解析）。
		artifacts = []domain.ID{}
		switch {
		case task.Status == domain.TaskStatusCancelRequested:
			if result.Error == nil && result.TransportErr == nil {
				target = domain.TaskStatusSucceeded
				artifacts = artifactIDs
			} else {
				target = domain.TaskStatusCanceled
			}
		case result.Error != nil || result.TransportErr != nil:
			target = domain.TaskStatusFailed
			info = errorInfoFor(result)
		default:
			target = domain.TaskStatusSucceeded
			artifacts = artifactIDs
		}
		updated, err := tx.UpdateTaskResult(ctx, cl.task.ID, target, artifacts, info, task.StateVersion, now)
		if err != nil {
			return err
		}
		if target == domain.TaskStatusSucceeded && prepared != nil {
			// 节点/句段产物登记、节点回写与下游推进必须在同一终态事务内完成；
			// 失败则整体回滚，任务不会被标记为成功。
			if err := prepared.Commit(ctx, tx, updated, &events); err != nil {
				return err
			}
		}
		attemptStatus := domain.StepStatusSucceeded
		switch target {
		case domain.TaskStatusFailed:
			attemptStatus = domain.StepStatusFailed
		case domain.TaskStatusCanceled:
			attemptStatus = domain.StepStatusCanceled
		}
		if err := tx.UpdateAttempt(ctx, cl.attempt.ID, attemptStatus, nil, &now); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		eventType := "task.succeeded"
		switch target {
		case domain.TaskStatusFailed:
			eventType = "task.failed"
		case domain.TaskStatusCanceled:
			eventType = "task.canceled"
		}
		data := map[string]any{"artifact_ids": artifacts}
		if info != nil {
			data["code"] = info.Code
		}
		ev, err := s.appendEvent(ctx, tx, domain.DomainEvent{
			Type:         eventType,
			ProjectID:    updated.ProjectID,
			TaskID:       updated.ID,
			StateVersion: updated.StateVersion,
			Data:         data,
		})
		if err != nil {
			return err
		}
		events = append(events, ev)
		finalTask = updated
		hooked = true
		return nil
	})
	if err != nil {
		if errors.Is(err, errSkip) {
			s.logWarn("scheduler", "scheduler.result_dropped", map[string]any{
				"task_id":    cl.task.ID,
				"attempt_no": cl.attempt.AttemptNo,
			})
			return
		}
		s.logError("scheduler", "scheduler.commit_failed", err)
		s.failAfterCommit(cl, err)
		return
	}
	s.publish(events...)
	if target == domain.TaskStatusSucceeded {
		if committed, ok := prepared.(interface{ AfterCommit() error }); ok {
			if err := committed.AfterCommit(); err != nil {
				s.logError("scheduler", "scheduler.result_post_commit_failed", err)
			}
		}
	}
	if !hooked {
		return
	}
	if target == domain.TaskStatusSucceeded {
		if prepared != nil {
			// 节点/句段回写与推进已在终态事务内由 prepared.Commit 完成。
			return
		}
		if s.hooks.OnTaskSucceeded != nil {
			if err := s.hooks.OnTaskSucceeded(ctx, finalTask, executionResultFor(result)); err != nil {
				s.logError("scheduler", "scheduler.success_hook_failed", err)
			}
		}
		return
	}
	if target != domain.TaskStatusFailed {
		// 取消不是失败：节点与导出记录不标记为 failed，留给重试/新任务。
		if target == domain.TaskStatusCanceled && s.hooks.OnTaskCanceled != nil {
			if err := s.hooks.OnTaskCanceled(ctx, finalTask); err != nil {
				s.logError("scheduler", "scheduler.cancel_hook_failed", err)
			}
		}
		return
	}
	if s.hooks.OnTaskFailed != nil {
		if err := s.hooks.OnTaskFailed(ctx, finalTask, info); err != nil {
			s.logError("scheduler", "scheduler.failure_hook_failed", err)
		}
	}
}

// failAfterCommit 在终态事务回滚后把仍处于 RUNNING 的任务收敛为 FAILED。
//
// 触发场景：产物登记/节点回写/下游推进在终态事务内失败。任务不能停留在
// RUNNING，也不能被误标为 SUCCEEDED；取消竞态让任务离开 RUNNING 时保持原终态。
func (s *Scheduler) failAfterCommit(cl *claim, cause error) {
	ctx := context.Background()
	info := &domain.ErrorInfo{
		Code:       domain.ErrorCodePersistenceUnavailable,
		MessageKey: "task.result_commit_failed",
		Retryable:  true,
		ActionIDs:  []domain.ID{"retry_task"},
	}
	var (
		events []domain.DomainEvent
		final  domain.Task
	)
	err := s.store.DoTx(ctx, func(tx *store.Tx) error {
		task, err := tx.GetTask(ctx, cl.task.ID)
		if err != nil {
			return err
		}
		if task.Status != domain.TaskStatusRunning {
			return errSkip
		}
		now := s.Now()
		updated, err := tx.UpdateTaskResult(ctx, task.ID, domain.TaskStatusFailed, nil, info, task.StateVersion, now)
		if err != nil {
			return err
		}
		if err := tx.UpdateAttempt(ctx, cl.attempt.ID, domain.StepStatusFailed, &now, nil); err != nil &&
			!errors.Is(err, domain.ErrNotFound) {
			return err
		}
		ev, err := s.appendEvent(ctx, tx, domain.DomainEvent{
			Type:         "task.failed",
			ProjectID:    updated.ProjectID,
			TaskID:       updated.ID,
			StateVersion: updated.StateVersion,
			Data:         map[string]any{"code": info.Code},
		})
		if err != nil {
			return err
		}
		events = append(events, ev)
		final = updated
		return nil
	})
	if err != nil {
		if !errors.Is(err, errSkip) {
			s.logError("scheduler", "scheduler.commit_failure_converge_failed", err)
		}
		return
	}
	s.logWarn("scheduler", "scheduler.result_commit_failed", map[string]any{
		"task_id": cl.task.ID,
		"error":   cause.Error(),
	})
	s.publish(events...)
	if s.hooks.OnTaskFailed != nil {
		if err := s.hooks.OnTaskFailed(ctx, final, info); err != nil {
			s.logError("scheduler", "scheduler.failure_hook_failed", err)
		}
	}
}

// CancelTask 取消正在执行的尝试；槽位在确认退出后才释放。
func (s *Scheduler) CancelTask(id domain.ID) {
	s.mu.Lock()
	cancel, ok := s.running[id]
	s.mu.Unlock()
	if ok {
		cancel()
	}
	s.Notify()
}

// RunningCount 返回当前执行中的尝试数（诊断/测试用）。
func (s *Scheduler) RunningCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running)
}

// ---- 辅助 ----

func stepIDOf(task domain.Task) string {
	for _, step := range task.Steps {
		if step.Status != domain.StepStatusSucceeded {
			return step.StepID
		}
	}
	if len(task.Steps) > 0 {
		return task.Steps[len(task.Steps)-1].StepID
	}
	return "main"
}

func nodeStatus(run domain.WorkflowRun, key domain.PipelineNodeKey) domain.NodeStatus {
	for _, node := range run.Nodes {
		if node.Key == key {
			return node.Status
		}
	}
	return domain.NodeStatusPending
}

func errorInfoFor(result execadapter.Result) *domain.ErrorInfo {
	if result.TransportErr != nil {
		return &domain.ErrorInfo{
			Code:       domain.ErrorCodeWorkerExited,
			MessageKey: "task.worker_exited",
			Retryable:  true,
			ActionIDs:  []domain.ID{"retry_task"},
		}
	}
	code := domain.ErrorCodeWorkerExited
	retryable := result.Error.Retryable
	actions := []domain.ID{}
	for _, id := range result.Error.ActionIDs {
		actions = append(actions, domain.ID(id))
	}
	messageKey := "task.worker_error"
	switch result.Error.Code {
	case "MODEL_RUNTIME_UNAVAILABLE":
		code = domain.ErrorCodeModelNotInstalled
		actions = appendActionUnique(actions, "open_model_setup")
	case "CAPABILITY_UNAVAILABLE":
		// 已准入的能力在运行时不可用：按“需要安装/修复运行时”呈现。
		code = domain.ErrorCodeModelNotInstalled
		actions = appendActionUnique(actions, "open_model_setup")
	case "CAPABILITY_INVALID_PARAMS":
		code = domain.ErrorCodeInvalidRequest
	case "MEDIA_TOOL_UNAVAILABLE":
		// 固定版本媒体工具未注入/不可执行：按“需要安装/修复运行时”呈现。
		code = domain.ErrorCodeModelNotInstalled
		messageKey = "task.media_tool_unavailable"
		actions = []domain.ID{"open_engine_setup"}
	case "MEDIA_TOOL_FAILED":
		code = domain.ErrorCodeWorkerExited
		messageKey = "task.media_tool_failed"
	case "MEDIA_TOOL_TIMEOUT":
		code = domain.ErrorCodeWorkerExited
		messageKey = "task.media_tool_timeout"
		retryable = true
	case "MEDIA_TOOL_CANCELED":
		code = domain.ErrorCodeWorkerExited
		messageKey = "task.media_tool_canceled"
	case "MEDIA_TOOL_INVALID_PARAMS":
		code = domain.ErrorCodeInvalidRequest
		messageKey = "task.media_tool_invalid_params"
	case "CLONE_TIMELINE_OUT_OF_RANGE":
		code = domain.ErrorCodeInvalidRequest
		messageKey = "task.clone_timeline_out_of_range"
	case "CLONE_TIMELINE_UNKNOWN_DURATION":
		code = domain.ErrorCodeInvalidRequest
		messageKey = "task.clone_timeline_unknown_duration"
	case string(domain.ErrorCodeInvalidRequest):
		code = domain.ErrorCodeInvalidRequest
		messageKey = "task.invalid_result_artifacts"
	case string(domain.ErrorCodeCapabilityUnverified):
		code = domain.ErrorCodeCapabilityUnverified
	case string(domain.ErrorCodeModelNotInstalled):
		code = domain.ErrorCodeModelNotInstalled
	case string(domain.ErrorCodeRuntimeIncompatible):
		code = domain.ErrorCodeRuntimeIncompatible
	case string(domain.ErrorCodeInsufficientDisk):
		code = domain.ErrorCodeInsufficientDisk
	case string(domain.ErrorCodeResourceExhausted):
		code = domain.ErrorCodeResourceExhausted
	}
	if len(actions) == 0 {
		// 冻结输入导致的时间轴失败无法靠原地重试修复，不能默认推荐 retry_task。
		switch result.Error.Code {
		case "CLONE_TIMELINE_OUT_OF_RANGE", "CLONE_TIMELINE_UNKNOWN_DURATION":
		default:
			actions = []domain.ID{"retry_task"}
		}
	}
	return &domain.ErrorInfo{
		Code:       code,
		MessageKey: messageKey,
		Retryable:  retryable,
		ActionIDs:  actions,
	}
}

// appendActionUnique 追加 UI 动作但保留 Runner 已给的顺序，避免重复动作。
func appendActionUnique(actions []domain.ID, id domain.ID) []domain.ID {
	for _, existing := range actions {
		if existing == id {
			return actions
		}
	}
	return append(actions, id)
}

// ---- 事件与日志 ----

func (s *Scheduler) appendEvent(ctx context.Context, tx *store.Tx, ev domain.DomainEvent) (domain.DomainEvent, error) {
	if ev.SchemaVersion == 0 {
		ev.SchemaVersion = 1
	}
	if ev.Data == nil {
		ev.Data = map[string]any{}
	}
	seq, err := tx.AppendEvent(ctx, ev, s.Now())
	if err != nil {
		return domain.DomainEvent{}, err
	}
	ev.Seq = seq
	return ev, nil
}

func (s *Scheduler) publish(evs ...domain.DomainEvent) {
	if s.bus == nil {
		return
	}
	for _, ev := range evs {
		s.bus.Publish(ev)
	}
}

func (s *Scheduler) Now() time.Time { return s.now().UTC() }

func (s *Scheduler) logInfo(component, event string, fields map[string]any) {
	if s.log != nil {
		s.log.Info(component, event, fields)
	}
}

func (s *Scheduler) logWarn(component, event string, fields map[string]any) {
	if s.log != nil {
		s.log.Warn(component, event, fields)
	}
}

func (s *Scheduler) logError(component, event string, err error) {
	if s.log != nil {
		s.log.Error(component, event, err, nil)
	}
}
