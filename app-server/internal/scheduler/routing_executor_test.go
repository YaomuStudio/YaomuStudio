package scheduler

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
)

// routingRunnerTransport 是 RoutingExecutor 调度回归的内存传输：
// Send 把帧交给假 Runner，Receive 阻塞到有应答或传输关闭（Kill/Close 可唤醒）。
type routingRunnerTransport struct {
	out     chan execadapter.Frame
	in      chan execadapter.Frame
	done    chan struct{}
	once    sync.Once
	kills   atomic.Int32
	sendErr error
}

func newRoutingRunnerTransport() *routingRunnerTransport {
	return &routingRunnerTransport{
		out:  make(chan execadapter.Frame, 16),
		in:   make(chan execadapter.Frame, 16),
		done: make(chan struct{}),
	}
}

func (t *routingRunnerTransport) Send(frame execadapter.Frame) error {
	if t.sendErr != nil {
		return t.sendErr
	}
	select {
	case t.out <- frame:
		return nil
	case <-t.done:
		return errors.New("routing test transport closed")
	}
}

func (t *routingRunnerTransport) Receive() (execadapter.Frame, error) {
	select {
	case frame := <-t.in:
		return frame, nil
	case <-t.done:
		return execadapter.Frame{}, io.EOF
	}
}

func (t *routingRunnerTransport) Kill() error {
	t.kills.Add(1)
	t.close()
	return nil
}

func (t *routingRunnerTransport) Close() error {
	t.close()
	return nil
}

func (t *routingRunnerTransport) close() {
	t.once.Do(func() { close(t.done) })
}

func (t *routingRunnerTransport) deliver(frame execadapter.Frame) {
	select {
	case t.in <- frame:
	case <-t.done:
	}
}

// routingFakeRunner 模拟 Runner：并发统计在途 execute、受控阻塞后回执带完整绑定。
// 取消确认回显请求绑定并报告 exited=false/active=true，Kill 次数由传输记录。
type routingFakeRunner struct {
	tr      *routingRunnerTransport
	started chan string
	release chan struct{}
	done    chan struct{}
	wg      sync.WaitGroup

	mu        sync.Mutex
	active    int
	maxActive int
	executes  int
}

func newRoutingFakeRunner(tr *routingRunnerTransport) *routingFakeRunner {
	return &routingFakeRunner{
		tr:      tr,
		started: make(chan string, 4),
		release: make(chan struct{}, 4),
		done:    make(chan struct{}),
	}
}

func (f *routingFakeRunner) run(ctx context.Context) {
	defer close(f.done)
	defer f.wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.tr.done:
			return
		case req := <-f.tr.out:
			switch req.Op {
			case execadapter.OpExecute:
				f.wg.Add(1)
				go func() {
					defer f.wg.Done()
					f.execute(ctx, req)
				}()
			case execadapter.OpCancel:
				f.cancel(req)
			}
		}
	}
}

func (f *routingFakeRunner) execute(ctx context.Context, req execadapter.Frame) {
	taskID, _ := req.Payload["task_id"].(string)
	f.mu.Lock()
	f.active++
	f.executes++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	finished := false
	finish := func() {
		if finished {
			return
		}
		finished = true
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}
	// 取消/关闭路径也必须恰好释放一次在途计数。
	defer finish()
	select {
	case f.started <- taskID:
	default:
	}
	select {
	case <-f.release:
	case <-f.tr.done:
		return
	case <-ctx.Done():
		return
	}
	// 在发布结果帧前结束在途计数：Client 收到结果后可能立即派发下一任务，
	// 若延迟到本 goroutine 返回后再减，健康串行执行会被误计为重叠。
	finish()
	f.tr.deliver(execadapter.Frame{
		V:     execadapter.ProtocolVersion,
		Op:    execadapter.OpResult,
		ReqID: req.ReqID,
		Payload: map[string]any{
			"request_op": "execute",
			"status":     "ok",
			"data":       map[string]any{"artifact_ids": []any{"asset-" + taskID}},
			"binding": map[string]any{
				"workspace":    req.Payload["workspace"],
				"server_epoch": req.Payload["server_epoch"],
				"task_id":      req.Payload["task_id"],
				"step_id":      req.Payload["step_id"],
				"attempt":      req.Payload["attempt"],
			},
		},
	})
}

func (f *routingFakeRunner) cancel(req execadapter.Frame) {
	data := map[string]any{
		"exited":  false,
		"active":  true,
		"task_id": req.Payload["task_id"],
		"attempt": req.Payload["attempt"],
	}
	if stepID, ok := req.Payload["step_id"]; ok {
		data["step_id"] = stepID
	}
	f.tr.deliver(execadapter.Frame{
		V:     execadapter.ProtocolVersion,
		Op:    execadapter.OpResult,
		ReqID: req.ReqID,
		Payload: map[string]any{
			"request_op": "cancel",
			"status":     "ok",
			"data":       data,
		},
	})
}

func (f *routingFakeRunner) snapshot() (executes, maxActive int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.executes, f.maxActive
}

// startRoutingFakeClient 启动内存假 Runner 并注册有界关闭/join。
func startRoutingFakeClient(t *testing.T) (*execadapter.Client, *routingFakeRunner, *routingRunnerTransport) {
	t.Helper()
	tr := newRoutingRunnerTransport()
	client := execadapter.NewClient(tr, nil)
	runner := newRoutingFakeRunner(tr)
	ctx, cancel := context.WithCancel(context.Background())
	go runner.run(ctx)
	t.Cleanup(func() {
		cancel()
		tr.close()
		_ = client.Close()
		select {
		case <-runner.done:
		case <-time.After(5 * time.Second):
			t.Errorf("routing fake runner did not stop")
		}
	})
	return client, runner, tr
}

// isolatedRoutingExecutor 构造一个已确认停止、已永久隔离的 Executor：
// 发送失败 → 取消未确认 → Kill 成功确认进程树退出（StopUnconfirmed=false）。
func isolatedRoutingExecutor(t *testing.T) *execadapter.Executor {
	t.Helper()
	tr := newRoutingRunnerTransport()
	tr.sendErr = errors.New("injected runner stream failure")
	client := execadapter.NewClient(tr, nil)
	t.Cleanup(func() { _ = client.Close() })
	executor := &execadapter.Executor{Client: client}
	result := executor.Execute(context.Background(), execadapter.Request{
		Workspace:   "ws-sched",
		ServerEpoch: 11,
		TaskID:      "isolation-probe",
		StepID:      "analyze",
		Attempt:     1,
	}, nil, nil)
	if result.TransportErr == nil {
		t.Fatalf("probe execution unexpectedly succeeded: %+v", result)
	}
	if err := executor.IsolationErr(); err == nil {
		t.Fatal("confirmed-stop failure did not isolate the executor")
	}
	if client.ExecutionUnconfirmed() {
		t.Fatal("probe isolation must be confirmed; StopUnconfirmed stays false")
	}
	return executor
}

// TestRoutingExecutorCapacityTightensHeavySlots 回归：包装层必须声明单执行容量，
// 提高重型槽配置也不能向同一 Runner 重叠派发。
func TestRoutingExecutorCapacityTightensHeavySlots(t *testing.T) {
	e := newEnv(t, envOptions{executor: &execadapter.RoutingExecutor{}, slots: 3})
	if got := cap(e.sched.slots); got != 1 {
		t.Fatalf("effective heavy slots = %d, want 1 for a single Runner", got)
	}
	if got := len(e.sched.slots); got != 0 {
		t.Fatalf("fresh scheduler already holds %d reservations", got)
	}
}

// TestRoutingExecutorIsolationStopsDispatch 回归：Host/Model 任一侧已确认停止的
// 隔离必须透传，调度器停止新派发；排队任务转入 WAITING_RESOURCE 并携带安全原因，
// 不误报终端失败，也不认领/创建新尝试。
func TestRoutingExecutorIsolationStopsDispatch(t *testing.T) {
	sides := []struct {
		name  string
		build func(*execadapter.Executor) *execadapter.RoutingExecutor
	}{
		{name: "model", build: func(isolated *execadapter.Executor) *execadapter.RoutingExecutor {
			return &execadapter.RoutingExecutor{Model: isolated}
		}},
		{name: "host", build: func(isolated *execadapter.Executor) *execadapter.RoutingExecutor {
			return &execadapter.RoutingExecutor{Host: isolated}
		}},
	}
	for _, side := range sides {
		t.Run(side.name, func(t *testing.T) {
			isolated := isolatedRoutingExecutor(t)
			e := newEnv(t, envOptions{executor: side.build(isolated), slots: 3})
			projectID := e.seedProject()
			run := e.seedRun(projectID)
			task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

			startScheduler(t, e.sched, context.Background())
			e.sched.Notify()

			deadline := time.Now().Add(3 * time.Second)
			var current domain.Task
			for time.Now().Before(deadline) {
				got, err := e.st.GetTask(context.Background(), task.ID)
				if err != nil {
					t.Fatalf("GetTask: %v", err)
				}
				current = got
				if e.sched.Isolated() && current.Status == domain.TaskStatusWaitingResource {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !e.sched.Isolated() {
				t.Fatalf("scheduler did not observe executor isolation; task status = %s", current.Status)
			}
			if current.Status != domain.TaskStatusWaitingResource {
				t.Fatalf("task status = %s, want WAITING_RESOURCE while isolated", current.Status)
			}
			if current.Error == nil ||
				current.Error.Code != domain.ErrorCodeWorkerExited ||
				current.Error.MessageKey != "task.runner_isolated" {
				t.Fatalf("isolated task reason = %+v, want WORKER_EXITED/task.runner_isolated", current.Error)
			}
			if len(current.Error.ActionIDs) != 0 {
				t.Fatalf("isolated task invents recovery actions: %v", current.Error.ActionIDs)
			}
			attempts, err := e.st.ListAttempts(context.Background(), task.ID)
			if err != nil {
				t.Fatalf("ListAttempts: %v", err)
			}
			if len(attempts) != 1 || attempts[0].Status != domain.StepStatusPending {
				t.Fatalf("isolated scheduler claimed work: %+v", attempts)
			}
			if e.sched.RunningCount() != 0 || len(e.sched.slots) != 0 {
				t.Fatalf("isolated scheduler reserved slots: running=%d slots=%d",
					e.sched.RunningCount(), len(e.sched.slots))
			}
		})
	}
}

// TestRoutingExecutorCapacitySerializesQueuedTasks 回归：Slots=3 + 假 Runner +
// 两个排队任务，容量必须收敛为 1：依次成功、最大在途 execute 为 1、零 Kill、
// 调度器不隔离。
func TestRoutingExecutorCapacitySerializesQueuedTasks(t *testing.T) {
	client, runner, tr := startRoutingFakeClient(t)
	executor := &execadapter.RoutingExecutor{Model: &execadapter.Executor{Client: client}}
	e := newEnv(t, envOptions{executor: executor, slots: 3})
	projectID := e.seedProject()
	firstRun := e.seedRun(projectID)
	secondRun := e.seedRun(projectID)
	first := e.seedTask(projectID, firstRun.ID, domain.NodeKeyAnalyze)
	second := e.seedTask(projectID, secondRun.ID, domain.NodeKeyAnalyze)
	if first.ID == second.ID {
		t.Fatalf("seeded tasks share identity %s", first.ID)
	}

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()

	var startedTask string
	select {
	case startedTask = <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("no task started")
	}
	select {
	case other := <-runner.started:
		t.Fatalf("capacity 1 admitted overlapping execute: %s and %s", startedTask, other)
	case <-time.After(250 * time.Millisecond):
	}

	runner.release <- struct{}{}
	e.waitTask(domain.ID(startedTask), domain.TaskStatusSucceeded, 5*time.Second)

	otherID := first.ID
	if first.ID == domain.ID(startedTask) {
		otherID = second.ID
	}
	select {
	case next := <-runner.started:
		if domain.ID(next) != otherID {
			t.Fatalf("unexpected second task started: %s, want %s", next, otherID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued task did not start after the first execution finished")
	}
	runner.release <- struct{}{}
	e.waitTask(otherID, domain.TaskStatusSucceeded, 5*time.Second)

	executes, maxActive := runner.snapshot()
	if executes != 2 || maxActive != 1 {
		t.Fatalf("executes = %d, max in-flight = %d, want 2 and 1", executes, maxActive)
	}
	if kills := tr.kills.Load(); kills != 0 {
		t.Fatalf("runner was killed %d times on a healthy execution", kills)
	}
	if e.sched.Isolated() {
		t.Fatal("scheduler isolated despite healthy executions")
	}
}
