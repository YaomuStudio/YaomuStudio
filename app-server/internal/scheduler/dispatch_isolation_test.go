package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// confirmedStopExecutor 第一次执行时在返回前把自身置为永久隔离（已确认停止），
// 用于覆盖隔离释放槽后，后续派发不得再认领排队任务。
type confirmedStopExecutor struct {
	mu           sync.Mutex
	isolationErr error
	calls        int
	started      chan domain.ID
	executed     chan struct{}
}

func (e *confirmedStopExecutor) IsolationErr() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.isolationErr
}

func (e *confirmedStopExecutor) Execute(_ context.Context, req execadapter.Request, _ execadapter.ProgressFunc, _ execadapter.LogFunc) execadapter.Result {
	e.mu.Lock()
	e.calls++
	call := e.calls
	if call == 1 {
		e.isolationErr = errors.New("runner stream lost after confirmed stop")
	}
	e.mu.Unlock()
	if call == 1 {
		select {
		case e.started <- domain.ID(req.TaskID):
		default:
		}
	} else {
		select {
		case e.executed <- struct{}{}:
		default:
		}
	}
	return execadapter.Result{TransportErr: errors.New("runner stopped"), Binding: requestBinding(req)}
}

func (e *confirmedStopExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// probeIsolationExecutor 在第二次隔离探测时进入隔离：用于验证同一 dispatch
// 调用在入口检查之后、认领之前会再次复查。
type probeIsolationExecutor struct {
	mu     sync.Mutex
	probes int
	calls  int
}

func (e *probeIsolationExecutor) IsolationErr() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.probes++
	if e.probes >= 2 {
		return errors.New("isolated after entry probe")
	}
	return nil
}

func (e *probeIsolationExecutor) Execute(_ context.Context, req execadapter.Request, _ execadapter.ProgressFunc, _ execadapter.LogFunc) execadapter.Result {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return execadapter.Result{TransportErr: errors.New("executor isolated"), Binding: requestBinding(req)}
}

func (e *probeIsolationExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// TestDispatchRechecksIsolationBeforeClaiming 回归：入口检查通不过不代表整轮
// 可派发；同一 dispatch 调用在取候选/认领前必须复查隔离，不得调用执行器。
func TestDispatchRechecksIsolationBeforeClaiming(t *testing.T) {
	exec := &probeIsolationExecutor{}
	e := newEnv(t, envOptions{executor: exec, slots: 1})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()

	waiting := waitRunnerWaiting(t, e, task.ID, 3*time.Second)
	assertRunnerReason(t, waiting, domain.ErrorCodeWorkerExited, "task.runner_isolated")
	if calls := exec.callCount(); calls != 0 {
		t.Fatalf("executor was called %d times after isolation was only visible after entry", calls)
	}
	if !e.sched.Isolated() {
		t.Fatal("scheduler did not record rechecked isolation")
	}
}

// TestConfirmedStopIsolationBlocksLaterDispatch 回归：一次执行确认停止并隔离后，
// 后续派发不得再认领排队任务；剩余任务转 WAITING_RESOURCE。本用例不保证隔离
// 与第二次认领发生在同一次 dispatch 调用中。
func TestConfirmedStopIsolationBlocksLaterDispatch(t *testing.T) {
	exec := &confirmedStopExecutor{
		started:  make(chan domain.ID, 1),
		executed: make(chan struct{}, 1),
	}
	e := newEnv(t, envOptions{executor: exec, slots: 1})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	first := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)
	second := e.seedTask(projectID, run.ID, domain.NodeKeySeparate)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()

	var startedID domain.ID
	select {
	case startedID = <-exec.started:
	case <-time.After(3 * time.Second):
		t.Fatal("executor did not start the first task")
	}
	e.waitTask(startedID, domain.TaskStatusFailed, 5*time.Second)

	otherID := first.ID
	if startedID == first.ID {
		otherID = second.ID
	}
	waiting := waitRunnerWaiting(t, e, otherID, 3*time.Second)
	assertRunnerReason(t, waiting, domain.ErrorCodeWorkerExited, "task.runner_isolated")
	if calls := exec.callCount(); calls != 1 {
		t.Fatalf("isolated executor was called %d times; queued task was over-claimed", calls)
	}
	select {
	case <-exec.executed:
		t.Fatal("second task reached the isolated executor after confirmed-stop isolation")
	default:
	}
	if !e.sched.Isolated() {
		t.Fatal("scheduler did not record executor isolation")
	}
}

// flippableExecutor 的隔离状态由测试在屏障处显式切换。
type flippableExecutor struct {
	mu           sync.Mutex
	isolationErr error
	calls        int
}

func (e *flippableExecutor) IsolationErr() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.isolationErr
}

func (e *flippableExecutor) setIsolated(err error) {
	e.mu.Lock()
	e.isolationErr = err
	e.mu.Unlock()
}

func (e *flippableExecutor) Execute(_ context.Context, req execadapter.Request, _ execadapter.ProgressFunc, _ execadapter.LogFunc) execadapter.Result {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	return execadapter.Result{TransportErr: errors.New("executor isolated"), Binding: requestBinding(req)}
}

func (e *flippableExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// TestIsolationDuringClaimPreventsRunning 回归：派发前重检（claim 内）期间执行器
// 才进入隔离时，任务不得被置为 RUNNING 或调用 Execute，而应转 WAITING_RESOURCE。
func TestIsolationDuringClaimPreventsRunning(t *testing.T) {
	exec := &flippableExecutor{}
	e := newEnv(t, envOptions{executor: exec, slots: 1})
	ctx := context.Background()
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	entered := make(chan struct{})
	release := make(chan struct{})
	e.sched.preDispatch = func(context.Context, *store.Tx, domain.Task) error {
		close(entered)
		<-release
		return nil
	}

	done := make(chan struct{})
	go func() {
		e.sched.dispatch(ctx)
		close(done)
	}()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("dispatch did not reach the pre-dispatch barrier")
	}
	exec.setIsolated(errors.New("isolation observed during claim"))
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("dispatch did not return after isolation during claim")
	}

	waiting := waitRunnerWaiting(t, e, task.ID, 3*time.Second)
	assertRunnerReason(t, waiting, domain.ErrorCodeWorkerExited, "task.runner_isolated")
	if calls := exec.callCount(); calls != 0 {
		t.Fatalf("isolated executor was called %d times after claim-time isolation", calls)
	}
	attempts, err := e.st.ListAttempts(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Status != domain.StepStatusPending {
		t.Fatalf("claim-time isolation still claimed the task: %+v", attempts)
	}
	if !e.sched.Isolated() {
		t.Fatal("scheduler did not record claim-time isolation")
	}
}
