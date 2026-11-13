package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
)

// readinessExecutor 模拟异步初始化中的执行器：Execute 记录调用，Readiness
// 由测试切换；它用于证明调度器在 not-ready 期间不认领、不创建新尝试，
// 且 starting 不被当作永久隔离。
type readinessExecutor struct {
	fakeExecutor
	mu        sync.Mutex
	readiness execadapter.ExecutorReadiness
}

func (e *readinessExecutor) Readiness() execadapter.ExecutorReadiness {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.readiness
}

func (e *readinessExecutor) setReadiness(value execadapter.ExecutorReadiness) {
	e.mu.Lock()
	e.readiness = value
	e.mu.Unlock()
}

// TestRunnerStartingWaitsWithoutExecuting 覆盖异步启动窗口：执行器仍处于
// starting 时任务得到可恢复的 task.runner_starting 等待原因，不调用 Execute、
// 不新增尝试、不永久隔离；就绪后正常认领并清除等待原因。
func TestRunnerStartingWaitsWithoutExecuting(t *testing.T) {
	executor := &readinessExecutor{readiness: execadapter.ExecutorReadinessStarting}
	e := newEnv(t, envOptions{executor: executor})
	ctx := context.Background()
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	before, err := e.st.ListAttempts(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()

	waiting := waitRunnerWaiting(t, e, task.ID, 3*time.Second)
	assertRunnerReason(t, waiting, domain.ErrorCodeCapabilityUnverified, "task.runner_starting")
	events := waitingEvents(t, e)
	if got := countWaitingEventsFor(events, task.ID); got != 1 {
		t.Fatalf("starting waiting events = %d, want 1", got)
	}
	if events[0].Data["reason"] != "runner_starting" {
		t.Fatalf("starting event reason = %v, want runner_starting", events[0].Data["reason"])
	}
	if calls, _ := executor.snapshot(); calls != 0 {
		t.Fatalf("executor called %d times while starting", calls)
	}
	if e.sched.Isolated() {
		t.Fatal("starting readiness was treated as permanent isolation")
	}
	if e.sched.RunningCount() != 0 || len(e.sched.slots) != 0 {
		t.Fatalf("starting readiness claimed work: running=%d slots=%d",
			e.sched.RunningCount(), len(e.sched.slots))
	}
	attempts, err := e.st.ListAttempts(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(attempts) != len(before) {
		t.Fatalf("starting readiness created attempts: %d -> %d", len(before), len(attempts))
	}
	for _, attempt := range attempts {
		if attempt.Status != domain.StepStatusPending {
			t.Fatalf("starting readiness changed attempt status: %+v", attempt)
		}
	}

	executor.setReadiness(execadapter.ExecutorReadinessReady)
	e.sched.Notify()
	done := e.waitTask(task.ID, domain.TaskStatusSucceeded, 5*time.Second)
	if done.Error != nil {
		t.Fatalf("ready task kept waiting reason: %+v", done.Error)
	}
	if calls, _ := executor.snapshot(); calls != 1 {
		t.Fatalf("executor calls = %d after ready, want 1", calls)
	}
}

// TestRoutingHolderReadyAfterStartingClearsWaitingReason 使用真实持有者覆盖
// starting → ready 的调度集成：就绪后正常认领并清除旧等待原因，而不是继续
// 停留在 runner_starting。
func TestRoutingHolderReadyAfterStartingClearsWaitingReason(t *testing.T) {
	holder := execadapter.NewRoutingHolder()
	e := newEnv(t, envOptions{executor: holder})
	ctx := context.Background()
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()
	waiting := waitRunnerWaiting(t, e, task.ID, 3*time.Second)
	assertRunnerReason(t, waiting, domain.ErrorCodeCapabilityUnverified, "task.runner_starting")

	if !holder.Publish(&execadapter.RoutingExecutor{ModelRequired: true}, nil) {
		t.Fatal("holder rejected the first complete routing")
	}
	e.sched.Notify()

	// 真实路由在模型缺失时 fail-closed；只要求它已离开等待态并被终态收敛，
	// 不锁定既有错误码，避免把本测试变成执行器错误映射测试。
	current := e.waitTask(task.ID, domain.TaskStatusFailed, 5*time.Second)
	if current.Error != nil && current.Error.MessageKey == "task.runner_starting" {
		t.Fatal("ready claim kept the runner_starting waiting reason")
	}
	attempts, err := e.st.ListAttempts(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Status != domain.StepStatusFailed {
		t.Fatalf("attempts after ready claim = %+v, want one settled attempt", attempts)
	}
}

// startingIsolatedExecutor 同时报告 starting 与永久隔离，用于验证隔离优先级。
type startingIsolatedExecutor struct {
	fakeExecutor
	isolationErr error
}

func (e *startingIsolatedExecutor) IsolationErr() error { return e.isolationErr }

func (e *startingIsolatedExecutor) Readiness() execadapter.ExecutorReadiness {
	return execadapter.ExecutorReadinessStarting
}

// TestPermanentIsolationWinsOverStarting 覆盖闸门优先级：已确认永久隔离优先于
// 启动中就绪状态，任务得到隔离原因并停止新派发，而不是被当作短暂启动窗口。
func TestPermanentIsolationWinsOverStarting(t *testing.T) {
	executor := &startingIsolatedExecutor{isolationErr: errors.New("stop confirmed")}
	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()

	waiting := waitRunnerWaiting(t, e, task.ID, 3*time.Second)
	assertRunnerReason(t, waiting, domain.ErrorCodeWorkerExited, "task.runner_isolated")
	if !e.sched.Isolated() {
		t.Fatal("permanent isolation was not recorded")
	}
	if calls, _ := executor.snapshot(); calls != 0 {
		t.Fatalf("isolated starting executor called %d times", calls)
	}
}

// TestRunnerUnavailableWaitsWithoutExecuting 覆盖启动失败后的不可用状态：
// 复用既有 runner_unavailable 原因，不调用 Execute、不自动重试成健康。
func TestRunnerUnavailableWaitsWithoutExecuting(t *testing.T) {
	executor := &readinessExecutor{readiness: execadapter.ExecutorReadinessUnavailable}
	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()

	waiting := waitRunnerWaiting(t, e, task.ID, 3*time.Second)
	assertRunnerReason(t, waiting, domain.ErrorCodeCapabilityUnverified, "task.runner_unavailable")
	if calls, _ := executor.snapshot(); calls != 0 {
		t.Fatalf("unavailable executor called %d times", calls)
	}
	if e.sched.Isolated() {
		t.Fatal("unavailable readiness was treated as permanent isolation")
	}
}
