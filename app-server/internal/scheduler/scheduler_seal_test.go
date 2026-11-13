package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// TestSchedulerIdleTracksReservations 覆盖更新准备的内存拥有者断言。
func TestSchedulerIdleTracksReservations(t *testing.T) {
	executor := &fakeExecutor{gate: make(chan struct{})}
	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	if err := e.st.DoTx(context.Background(), func(tx *store.Tx) error {
		current, err := tx.GetWorkflowRun(context.Background(), run.ID)
		if err != nil {
			return err
		}
		_, err = tx.UpdateRunStatus(context.Background(), run.ID, domain.RunStatusQueued, current.StateVersion, time.Now().UTC())
		return err
	}); err != nil {
		t.Fatalf("queue run: %v", err)
	}
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)
	startScheduler(t, e.sched, context.Background())
	if !e.sched.Idle() {
		t.Fatal("fresh scheduler reported reservations")
	}
	e.sched.Notify()
	e.waitTask(task.ID, domain.TaskStatusRunning, 5*time.Second)
	if e.sched.Idle() {
		t.Fatal("scheduler reported idle during an active execution")
	}
	close(executor.gate)
	e.waitTask(task.ID, domain.TaskStatusSucceeded, 5*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for !e.sched.Idle() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !e.sched.Idle() {
		t.Fatal("scheduler did not return to idle after execution settled")
	}
}

// TestStopReportsUnconvergedShutdown 证明 Stop 在预算耗尽时返回错误，不谎报
// 已停止。
func TestStopReportsUnconvergedShutdown(t *testing.T) {
	executor := &slowSettleExecutor{started: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	releaseSlow := func() { releaseOnce.Do(func() { close(executor.release) }) }
	t.Cleanup(releaseSlow)
	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	if err := e.st.DoTx(context.Background(), func(tx *store.Tx) error {
		current, err := tx.GetWorkflowRun(context.Background(), run.ID)
		if err != nil {
			return err
		}
		_, err = tx.UpdateRunStatus(context.Background(), run.ID, domain.RunStatusQueued, current.StateVersion, time.Now().UTC())
		return err
	}); err != nil {
		t.Fatalf("queue run: %v", err)
	}
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)
	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()
	<-executor.started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := e.sched.Stop(ctx); err == nil {
		t.Fatal("Stop reported convergence while the execution kept running")
	}
	// 放行慢执行，让终态提交与测试清理收敛。
	releaseSlow()
	e.waitTask(task.ID, domain.TaskStatusSucceeded, 5*time.Second)
}

// TestSealedStoreStopsClaimWithoutWaitingWrites 证明封闸后调度器不认领、
// 不把拒绝误写成 WAITING_RESOURCE，也不重复报 claim 失败。
func TestSealedStoreStopsClaimWithoutWaitingWrites(t *testing.T) {
	executor := &fakeExecutor{}
	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	if err := e.st.DoTx(context.Background(), func(tx *store.Tx) error {
		current, err := tx.GetWorkflowRun(context.Background(), run.ID)
		if err != nil {
			return err
		}
		_, err = tx.UpdateRunStatus(context.Background(), run.ID, domain.RunStatusQueued, current.StateVersion, time.Now().UTC())
		return err
	}); err != nil {
		t.Fatalf("queue run: %v", err)
	}
	startScheduler(t, e.sched, context.Background())

	// 测试夹具：先封闸，再用 Store 级自动提交插入一条候选行（生产创建都经
	// DoTx，不会在封闸后出现；这里只覆盖调度器在封闸事实下的写路径行为）。
	sealed, err := e.st.SealIfIdle(context.Background(), func(*store.Tx) (bool, error) {
		return true, nil
	})
	if err != nil || !sealed {
		t.Fatalf("seal = (%v, %v)", sealed, err)
	}
	task := domain.Task{
		ID: "task-sealed-candidate", ProjectID: projectID, RunID: run.ID,
		Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyAnalyze,
		Status: domain.TaskStatusQueued, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := e.st.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("seed candidate after seal: %v", err)
	}
	var beforeSeq int64
	if err := e.st.ReadTx(context.Background(), func(tx *store.Tx) error {
		var err error
		beforeSeq, err = tx.LatestEventSeq(context.Background())
		return err
	}); err != nil {
		t.Fatalf("latest event seq: %v", err)
	}

	e.sched.Notify()
	time.Sleep(200 * time.Millisecond)

	calls, _ := executor.snapshot()
	if calls != 0 {
		t.Fatalf("executor called %d times after seal", calls)
	}
	current, err := e.st.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if current.Status != domain.TaskStatusQueued {
		t.Fatalf("task status = %s after sealed claim, want QUEUED", current.Status)
	}
	if current.Error != nil {
		t.Fatalf("sealed claim wrote an error: %+v", current.Error)
	}
	var afterSeq int64
	if err := e.st.ReadTx(context.Background(), func(tx *store.Tx) error {
		var err error
		afterSeq, err = tx.LatestEventSeq(context.Background())
		return err
	}); err != nil {
		t.Fatalf("latest event seq after: %v", err)
	}
	if afterSeq != beforeSeq {
		t.Fatalf("sealed claim appended events: %d -> %d", beforeSeq, afterSeq)
	}
	// 调度循环每 tick 会短暂占用槽位尝试派发；封闸后应在边界归还，
	// 因此等待空闲，而不是在随机相位上做单次断言。
	idleDeadline := time.Now().Add(2 * time.Second)
	for !e.sched.Idle() {
		if time.Now().After(idleDeadline) {
			t.Fatal("sealed claim retained a slot")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestStopAfterSealSettlesIdleScheduler 证明空调度器在封闸后可确认停止。
func TestStopAfterSealSettlesIdleScheduler(t *testing.T) {
	e := newEnv(t, envOptions{executor: &fakeExecutor{}})
	startScheduler(t, e.sched, context.Background())
	if err := e.sched.Stop(context.Background()); err != nil {
		t.Fatalf("stop idle scheduler: %v", err)
	}
	if !e.sched.Idle() {
		t.Fatal("stopped scheduler reported reservations")
	}
}
