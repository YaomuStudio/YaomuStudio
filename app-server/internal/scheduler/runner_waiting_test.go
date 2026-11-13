package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// isolatedExecutor 模拟已确认永久隔离的执行器：派发闸门必须 fail-closed，
// 但不创建尝试、不调用 Execute。
type isolatedExecutor struct {
	fakeExecutor
	isolationErr error
}

func (e *isolatedExecutor) IsolationErr() error { return e.isolationErr }

func waitRunnerWaiting(t *testing.T, e *env, id domain.ID, timeout time.Duration) domain.Task {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last domain.Task
	for time.Now().Before(deadline) {
		task, err := e.st.GetTask(context.Background(), id)
		if err != nil {
			t.Fatalf("GetTask %s: %v", id, err)
		}
		last = task
		if task.Status == domain.TaskStatusWaitingResource {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s status=%s error=%+v, want WAITING_RESOURCE", id, last.Status, last.Error)
	return last
}

func waitingEvents(t *testing.T, e *env) []domain.DomainEvent {
	t.Helper()
	events, err := e.st.EventsAfter(context.Background(), 0, 1000)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	waiting := make([]domain.DomainEvent, 0)
	for _, ev := range events {
		if ev.Type == "task.waiting_resource" {
			waiting = append(waiting, ev)
		}
	}
	return waiting
}

func countWaitingEventsFor(events []domain.DomainEvent, id domain.ID) int {
	count := 0
	for _, ev := range events {
		if ev.TaskID == id {
			count++
		}
	}
	return count
}

func assertRunnerReason(t *testing.T, task domain.Task, code domain.ErrorCode, messageKey string) {
	t.Helper()
	if task.Error == nil {
		t.Fatalf("task %s has no waiting reason", task.ID)
	}
	if task.Error.Code != code || task.Error.MessageKey != messageKey {
		t.Fatalf("task %s error = %s/%s, want %s/%s",
			task.ID, task.Error.Code, task.Error.MessageKey, code, messageKey)
	}
	if !task.Error.Retryable {
		t.Fatalf("task %s waiting reason must be recoverable", task.ID)
	}
	if len(task.Error.ActionIDs) != 0 {
		t.Fatalf("task %s waiting reason invents actions: %v", task.ID, task.Error.ActionIDs)
	}
}

// TestNoExecutorMarksAllDispatchableTasks 覆盖 executor=nil：批量扫描所有可派发
// QUEUED 任务，写入安全等待原因与持久事件；重复 tick 不重复写库或刷事件。
func TestNoExecutorMarksAllDispatchableTasks(t *testing.T) {
	e := newEnv(t, envOptions{executor: nil, slots: 2})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	first := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)
	second := e.seedTask(projectID, run.ID, domain.NodeKeySeparate)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()

	firstWaiting := waitRunnerWaiting(t, e, first.ID, 3*time.Second)
	secondWaiting := waitRunnerWaiting(t, e, second.ID, 3*time.Second)
	assertRunnerReason(t, firstWaiting, domain.ErrorCodeCapabilityUnverified, "task.runner_unavailable")
	assertRunnerReason(t, secondWaiting, domain.ErrorCodeCapabilityUnverified, "task.runner_unavailable")

	events := waitingEvents(t, e)
	if got := countWaitingEventsFor(events, first.ID); got != 1 {
		t.Fatalf("first task waiting events = %d, want 1", got)
	}
	if got := countWaitingEventsFor(events, second.ID); got != 1 {
		t.Fatalf("second task waiting events = %d, want 1", got)
	}
	for _, ev := range events {
		if ev.Data["reason"] != "runner_unavailable" {
			t.Fatalf("waiting event reason = %v, want runner_unavailable", ev.Data["reason"])
		}
	}

	// 阻断期间不得创建新尝试；重复 tick 不得改写状态版本或重复追加事件。
	for _, task := range []domain.Task{firstWaiting, secondWaiting} {
		attempts, err := e.st.ListAttempts(context.Background(), task.ID)
		if err != nil {
			t.Fatalf("ListAttempts: %v", err)
		}
		if len(attempts) != 1 || attempts[0].Status != domain.StepStatusPending {
			t.Fatalf("task %s attempts changed while blocked: %+v", task.ID, attempts)
		}
	}
	seq, err := e.st.LatestEventSeq(context.Background())
	if err != nil {
		t.Fatalf("LatestEventSeq: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	after, err := e.st.LatestEventSeq(context.Background())
	if err != nil {
		t.Fatalf("LatestEventSeq: %v", err)
	}
	if after != seq {
		t.Fatalf("waiting reason re-wrote events on every tick: seq %d -> %d", seq, after)
	}
	for _, want := range []domain.Task{firstWaiting, secondWaiting} {
		current, err := e.st.GetTask(context.Background(), want.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if current.StateVersion != want.StateVersion {
			t.Fatalf("task %s state version moved on repeated ticks: %d -> %d",
				want.ID, want.StateVersion, current.StateVersion)
		}
	}
}

// TestIsolatedExecutorMarksQueuedTasksWithoutExecuting 覆盖永久隔离：Host/Model 侧
// 已确认隔离时，不调用 Execute、不认领，排队任务得到 WORKER_EXITED 安全原因。
func TestIsolatedExecutorMarksQueuedTasksWithoutExecuting(t *testing.T) {
	fake := &isolatedExecutor{fakeExecutor: fakeExecutor{}, isolationErr: errors.New("stop confirmed")}
	e := newEnv(t, envOptions{executor: fake, slots: 2})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	first := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)
	second := e.seedTask(projectID, run.ID, domain.NodeKeySeparate)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()

	firstWaiting := waitRunnerWaiting(t, e, first.ID, 3*time.Second)
	secondWaiting := waitRunnerWaiting(t, e, second.ID, 3*time.Second)
	assertRunnerReason(t, firstWaiting, domain.ErrorCodeWorkerExited, "task.runner_isolated")
	assertRunnerReason(t, secondWaiting, domain.ErrorCodeWorkerExited, "task.runner_isolated")

	if calls, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("isolated executor was called %d times", calls)
	}
	if !e.sched.Isolated() {
		t.Fatal("scheduler did not observe executor isolation")
	}
	if e.sched.RunningCount() != 0 || len(e.sched.slots) != 0 {
		t.Fatalf("isolation claimed work: running=%d slots=%d", e.sched.RunningCount(), len(e.sched.slots))
	}
}

// TestUnconfirmedStopMarksRemainingTasksIsolated 覆盖未确认停止：无法确认执行树
// 退出时保留执行槽，正在运行的任务不改状态，其余排队任务得到隔离等待原因。
func TestUnconfirmedStopMarksRemainingTasksIsolated(t *testing.T) {
	fake := &capacityExecutor{fakeExecutor: &fakeExecutor{result: func(execadapter.Request) execadapter.Result {
		return execadapter.Result{TransportErr: execadapter.ErrStopUnconfirmed, StopUnconfirmed: true}
	}}}
	e := newEnv(t, envOptions{executor: fake, slots: 2})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	first := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)
	second := e.seedTask(projectID, run.ID, domain.NodeKeySeparate)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()

	deadline := time.Now().Add(3 * time.Second)
	for !e.sched.Isolated() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !e.sched.Isolated() {
		t.Fatal("unknown execution did not isolate the scheduler")
	}
	if len(e.sched.slots) != 1 || e.sched.RunningCount() != 1 {
		t.Fatalf("unconfirmed execution released its slot: slots=%d running=%d",
			len(e.sched.slots), e.sched.RunningCount())
	}

	var waitingID domain.ID
	for time.Now().Before(deadline) {
		for _, id := range []domain.ID{first.ID, second.ID} {
			task, err := e.st.GetTask(context.Background(), id)
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if task.Status == domain.TaskStatusWaitingResource {
				waitingID = id
			}
		}
		if waitingID != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if waitingID == "" {
		t.Fatal("remaining queued task was not marked as executor-blocked")
	}
	runningID := first.ID
	if waitingID == first.ID {
		runningID = second.ID
	}
	waiting := waitRunnerWaiting(t, e, waitingID, 3*time.Second)
	assertRunnerReason(t, waiting, domain.ErrorCodeWorkerExited, "task.runner_isolated")
	running, err := e.st.GetTask(context.Background(), runningID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if running.Status != domain.TaskStatusRunning {
		t.Fatalf("unconfirmed execution task status = %s, want RUNNING without blocker rewrite",
			running.Status)
	}
}

// TestPreDispatchUnknownErrorUsesFixedFallback 覆盖派发前重检返回非领域错误：
// 只写固定安全描述，原始文本不进入任务响应，重复 tick 不刷事件。
func TestPreDispatchUnknownErrorUsesFixedFallback(t *testing.T) {
	e := newEnv(t, envOptions{executor: &fakeExecutor{}})
	ctx := context.Background()
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	e.sched.preDispatch = func(context.Context, *store.Tx, domain.Task) error {
		return errors.New("raw diagnostic /tmp/secret-runner.log")
	}
	if _, claimed, err := e.sched.claim(ctx, task.ID); err != nil || claimed {
		t.Fatalf("claim: claimed=%t err=%v", claimed, err)
	}
	current, err := e.st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if current.Status != domain.TaskStatusWaitingResource {
		t.Fatalf("status = %s, want WAITING_RESOURCE", current.Status)
	}
	if current.Error == nil ||
		current.Error.Code != domain.ErrorCodeCapabilityUnverified ||
		current.Error.MessageKey != "task.capability_not_ready" {
		t.Fatalf("unknown pre-dispatch error was not mapped to a fixed safe reason: %+v",
			current.Error)
	}
	if len(current.Error.ActionIDs) != 0 {
		t.Fatalf("unknown pre-dispatch error invented actions: %v", current.Error.ActionIDs)
	}

	seq, err := e.st.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("LatestEventSeq: %v", err)
	}
	if _, claimed, err := e.sched.claim(ctx, task.ID); err != nil || claimed {
		t.Fatalf("second claim: claimed=%t err=%v", claimed, err)
	}
	after, err := e.st.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("LatestEventSeq: %v", err)
	}
	if after != seq {
		t.Fatalf("same unknown reason appended another event: seq %d -> %d", seq, after)
	}
}

// TestConcurrentCancelWhileMarkingWaiting 覆盖取消与批量等待原因并发：无论 CAS
// 先后，最终 CANCELED 快照都不携带旧等待原因，也不复活已取消任务。
func TestConcurrentCancelWhileMarkingWaiting(t *testing.T) {
	e := newEnv(t, envOptions{executor: nil, slots: 2})
	ctx := context.Background()
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	tasks := make([]domain.Task, 0, 8)
	for i := 0; i < 8; i++ {
		tasks = append(tasks, e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze))
	}

	startScheduler(t, e.sched, ctx)
	e.sched.Notify()

	var wg sync.WaitGroup
	for _, task := range tasks {
		wg.Add(1)
		go func(id domain.ID) {
			defer wg.Done()
			if _, err := e.svc.CancelTask(ctx, service.Idem{}, string(id)); err != nil {
				t.Errorf("CancelTask %s: %v", id, err)
			}
		}(task.ID)
	}
	wg.Wait()

	deadline := time.Now().Add(3 * time.Second)
	for _, task := range tasks {
		for {
			current, err := e.st.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if current.Status == domain.TaskStatusCanceled {
				if current.Error != nil {
					t.Fatalf("canceled task %s kept waiting reason: %+v", task.ID, current.Error)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("task %s status = %s, want CANCELED", task.ID, current.Status)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// TestHealthyQueueDoesNotReportRunnerUnavailable 回归：正常执行器下排队任务
// 不得被误标为执行器阻断。
func TestHealthyQueueDoesNotReportRunnerUnavailable(t *testing.T) {
	fake := &fakeExecutor{}
	e := newEnv(t, envOptions{executor: fake})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()
	done := e.waitTask(task.ID, domain.TaskStatusSucceeded, 5*time.Second)
	if done.Error != nil {
		t.Fatalf("successful task kept an error: %+v", done.Error)
	}
	if events := waitingEvents(t, e); len(events) != 0 {
		t.Fatalf("healthy queue produced waiting events: %+v", events)
	}
	if e.sched.Isolated() {
		t.Fatal("healthy scheduler was isolated")
	}
	if calls, _ := fake.snapshot(); calls != 1 {
		t.Fatalf("executor calls = %d, want 1", calls)
	}
}

// TestPausedRunTaskStaysQueuedWhileExportIsMarked 覆盖 Run 闸门：暂停 Run 的模型
// 任务不因执行器阻断改状态；导出沿用交付例外，仍可标记执行器阻断。
func TestPausedRunTaskStaysQueuedWhileExportIsMarked(t *testing.T) {
	e := newEnv(t, envOptions{executor: nil})
	ctx := context.Background()
	projectID := e.seedProject()

	pausedRun := e.seedRun(projectID)
	pausedTask := e.seedTask(projectID, pausedRun.ID, domain.NodeKeyAnalyze)
	if _, err := e.st.DB().ExecContext(ctx,
		`UPDATE workflow_runs SET status = 'PAUSED', state_version = state_version + 1 WHERE id = ?`,
		pausedRun.ID); err != nil {
		t.Fatalf("pause run: %v", err)
	}

	source := e.seedMediaAsset(projectID)
	exportRun := e.seedRunWithInputs(projectID, source.ID)
	track := e.seedTrack(projectID, "zh", []domain.Segment{{ID: "seg", StartMs: 0, EndMs: 500, Text: "hello"}})
	e.seedApprovedDelivery(exportRun.ID, track)
	record := e.createExport(projectID, exportRun.ID, t.TempDir())
	if _, err := e.st.DB().ExecContext(ctx,
		`UPDATE workflow_runs SET status = 'PAUSED', state_version = state_version + 1 WHERE id = ?`,
		exportRun.ID); err != nil {
		t.Fatalf("pause export run: %v", err)
	}

	startScheduler(t, e.sched, ctx)
	e.sched.Notify()

	exportWaiting := waitRunnerWaiting(t, e, record.TaskID, 3*time.Second)
	assertRunnerReason(t, exportWaiting, domain.ErrorCodeCapabilityUnverified, "task.runner_unavailable")

	time.Sleep(100 * time.Millisecond)
	stillQueued, err := e.st.GetTask(ctx, pausedTask.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stillQueued.Status != domain.TaskStatusQueued {
		t.Fatalf("paused-run model task status = %s, want QUEUED", stillQueued.Status)
	}
	if stillQueued.Error != nil {
		t.Fatalf("paused-run model task falsely reports waiting: %+v", stillQueued.Error)
	}
}

// TestCancelWaitingTaskClearsReason 覆盖取消：CANCELED 卡片不得继续显示等待资源。
func TestCancelWaitingTaskClearsReason(t *testing.T) {
	e := newEnv(t, envOptions{executor: nil})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()
	waitRunnerWaiting(t, e, task.ID, 3*time.Second)

	if _, err := e.svc.CancelTask(context.Background(), service.Idem{}, string(task.ID)); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	current, err := e.st.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if current.Status != domain.TaskStatusCanceled {
		t.Fatalf("task status = %s, want CANCELED", current.Status)
	}
	if current.Error != nil {
		t.Fatalf("canceled task kept waiting reason: %+v", current.Error)
	}
}

// TestClaimRunningClearsWaitingReason 覆盖恢复派发：进入 RUNNING 时清除等待原因。
func TestClaimRunningClearsWaitingReason(t *testing.T) {
	e := newEnv(t, envOptions{executor: &fakeExecutor{}})
	ctx := context.Background()
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)
	current, err := e.st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}

	info := &domain.ErrorInfo{
		Code:       domain.ErrorCodeCapabilityUnverified,
		MessageKey: "task.runner_unavailable",
		Retryable:  true,
		ActionIDs:  []domain.ID{},
	}
	waiting, err := e.st.UpdateTaskResult(ctx, current.ID, domain.TaskStatusWaitingResource,
		current.ArtifactIDs, info, current.StateVersion, time.Now().UTC())
	if err != nil {
		t.Fatalf("seed waiting: %v", err)
	}
	cl, claimed, err := e.sched.claim(ctx, waiting.ID)
	if err != nil || !claimed {
		t.Fatalf("claim: claimed=%t err=%v", claimed, err)
	}
	if cl.task.Status != domain.TaskStatusRunning || cl.task.Error != nil {
		t.Fatalf("claimed task = %s error=%+v, want RUNNING without waiting reason",
			cl.task.Status, cl.task.Error)
	}
	claimedTask, err := e.st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if claimedTask.Error != nil {
		t.Fatalf("task kept waiting reason after dispatch: %+v", claimedTask.Error)
	}
}

// TestPreDispatchWaitingPersistsSafeReason 覆盖派发前动态重检：等待原因来自已知
// 安全领域错误，正常排队/失败不写原始文本；能力恢复后自动清除并派发。
func TestPreDispatchWaitingPersistsSafeReason(t *testing.T) {
	capabilities := &mutableCapabilities{}
	executor := &fakeExecutor{}
	e := newEnv(t, envOptions{executor: executor, capabilities: capabilities})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	capabilities.set(domain.CapabilitySlotASR, domain.SlotStateNeedsSetup)
	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()

	waiting := waitRunnerWaiting(t, e, task.ID, 5*time.Second)
	if waiting.Error == nil {
		t.Fatal("pre-dispatch wait lost its safe reason")
	}
	if waiting.Error.MessageKey != "task.capability_not_ready" ||
		waiting.Error.Code != domain.ErrorCodeModelNotInstalled {
		t.Fatalf("pre-dispatch wait reason = %s/%s", waiting.Error.Code, waiting.Error.MessageKey)
	}
	if len(waiting.Error.ActionIDs) == 0 {
		t.Fatal("pre-dispatch capability reason lost its existing setup action")
	}
	if calls, _ := executor.snapshot(); calls != 0 {
		t.Fatalf("executor called %d times while dispatch check failed", calls)
	}

	seq, err := e.st.LatestEventSeq(context.Background())
	if err != nil {
		t.Fatalf("LatestEventSeq: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	after, err := e.st.LatestEventSeq(context.Background())
	if err != nil {
		t.Fatalf("LatestEventSeq: %v", err)
	}
	if after != seq {
		t.Fatalf("pre-dispatch wait re-wrote events on every tick: seq %d -> %d", seq, after)
	}

	capabilities.set(domain.CapabilitySlotASR, domain.SlotStateReady)
	e.sched.Notify()
	done := e.waitTask(task.ID, domain.TaskStatusSucceeded, 5*time.Second)
	if done.Error != nil {
		t.Fatalf("recovered task kept waiting reason: %+v", done.Error)
	}
	if calls, _ := executor.snapshot(); calls != 1 {
		t.Fatalf("executor calls = %d after recovery, want 1", calls)
	}
}

// TestRestartWaitingBecomesExplicitRetry 覆盖重启：WAITING_RESOURCE 收敛为可重试
// FAILED；显式重试会清除旧等待原因。
func TestRestartWaitingBecomesExplicitRetry(t *testing.T) {
	e := newEnv(t, envOptions{executor: nil})
	ctx := context.Background()
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)
	current, err := e.st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}

	info := &domain.ErrorInfo{
		Code:       domain.ErrorCodeCapabilityUnverified,
		MessageKey: "task.runner_unavailable",
		Retryable:  true,
		ActionIDs:  []domain.ID{},
	}
	waiting, err := e.st.UpdateTaskResult(ctx, current.ID, domain.TaskStatusWaitingResource,
		current.ArtifactIDs, info, current.StateVersion, time.Now().UTC())
	if err != nil {
		t.Fatalf("seed waiting: %v", err)
	}
	if _, ok, err := e.st.LatestAttempt(ctx, task.ID); err != nil || !ok {
		t.Fatalf("LatestAttempt: found=%t err=%v", ok, err)
	}

	if err := e.sched.recoverInterrupted(ctx); err != nil {
		t.Fatalf("recoverInterrupted: %v", err)
	}
	failed, err := e.st.GetTask(ctx, waiting.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if failed.Status != domain.TaskStatusFailed {
		t.Fatalf("restart status = %s, want FAILED", failed.Status)
	}
	if failed.Error == nil || failed.Error.MessageKey != "task.worker_lost_on_restart" {
		t.Fatalf("restart reason = %+v, want task.worker_lost_on_restart", failed.Error)
	}

	resp, err := e.svc.RetryTask(ctx, service.Idem{}, string(task.ID))
	if err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	retried := resp.Body.(domain.Task)
	if retried.Status != domain.TaskStatusQueued {
		t.Fatalf("retried status = %s, want QUEUED", retried.Status)
	}
	if retried.Error != nil {
		t.Fatalf("retry kept stale waiting reason: %+v", retried.Error)
	}
}
