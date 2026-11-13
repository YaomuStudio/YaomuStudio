package scheduler

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// TestSchedulerPublishesLiveTaskProgress 守护可丢弃进度的完整链路：
// 执行中合法进度先条件写快照、再发布 seq=0 的 task.progress，
// 事件身份/版本与快照一致，进度不进入持久事件流，成功终态写 100。
func TestSchedulerPublishesLiveTaskProgress(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	openRelease := func() { releaseOnce.Do(func() { close(release) }) }
	defer openRelease()

	executor := &fakeExecutor{progress: func(report execadapter.ProgressFunc) {
		report("decoding", 1, 2)
		<-release
	}}
	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)
	progressWrites := installProgressWriteProbe(t, e)

	received, cancel := subscribeLive(t, e)
	defer cancel()

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()

	var progressEvent domain.DomainEvent
	deadline := time.After(5 * time.Second)
	for progressEvent.Type == "" {
		select {
		case ev := <-received:
			if ev.Type == "task.progress" {
				progressEvent = ev
			}
		case <-deadline:
			t.Fatal("no live task.progress event during execution")
		}
	}
	if progressEvent.Seq != 0 || progressEvent.SchemaVersion != 1 {
		t.Fatalf("progress event envelope = %+v, want seq=0 schema=1", progressEvent)
	}
	if progressEvent.ProjectID != projectID || progressEvent.TaskID != task.ID {
		t.Fatalf("progress event identity = %+v", progressEvent)
	}
	wantData := map[string]any{
		"step_id":      string(domain.NodeKeyAnalyze),
		"attempt":      1,
		"server_epoch": "11",
		"progress":     50,
	}
	if len(progressEvent.Data) != len(wantData) {
		t.Fatalf("progress data = %+v, want exactly %v", progressEvent.Data, wantData)
	}
	for key, want := range wantData {
		if got := progressEvent.Data[key]; got != want {
			t.Errorf("progress data[%q] = %v, want %v", key, got, want)
		}
	}

	// live 事件必须在快照写成功之后发布：此刻任务仍 RUNNING 且快照为 50。
	snapshot, err := e.st.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if snapshot.Status != domain.TaskStatusRunning || snapshot.Progress != 50 {
		t.Fatalf("running snapshot = %s/%d, want RUNNING/50", snapshot.Status, snapshot.Progress)
	}
	if progressEvent.StateVersion != snapshot.StateVersion {
		t.Fatalf("event state_version = %d, snapshot = %d", progressEvent.StateVersion, snapshot.StateVersion)
	}
	if got := progressWrites(); got != 1 {
		t.Fatalf("actual tasks.progress UPDATEs before terminal = %d, want 1", got)
	}
	t.Logf("measured: live progress event after tasks.progress UPDATEs=%d", progressWrites())

	openRelease()
	final := e.waitTask(task.ID, domain.TaskStatusSucceeded, 5*time.Second)
	if final.Progress != 100 {
		t.Fatalf("final progress = %d, want 100", final.Progress)
	}

	if got := progressWrites(); got != 2 {
		t.Fatalf("actual tasks.progress UPDATEs after success = %d, want 2 (progress then terminal 100)", got)
	}
	t.Logf("measured: after success tasks.progress UPDATEs=%d (progress then terminal 100)", progressWrites())

	// seq=0 是可丢弃反馈：不追加持久事件，也不进入回放。
	persisted, err := e.st.EventsAfter(context.Background(), 0, 1000)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(persisted) == 0 {
		t.Fatal("no persisted task events")
	}
	for _, ev := range persisted {
		if ev.Type == "task.progress" {
			t.Fatalf("task.progress persisted with seq %d", ev.Seq)
		}
	}
}

// manualClock 是可注入的单调测试时钟（reporter 节流按接受时刻推进）。
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// seedProgressClaim 建立一个 RUNNING 任务与最新的 RUNNING 尝试，并返回调度 claim。
func seedProgressClaim(t *testing.T, e *env) (*claim, domain.Attempt) {
	t.Helper()
	ctx := context.Background()
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)
	queued, err := e.st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	running, err := e.st.UpdateTaskResult(ctx, task.ID, domain.TaskStatusRunning,
		queued.ArtifactIDs, nil, queued.StateVersion, time.Now().UTC())
	if err != nil {
		t.Fatalf("UpdateTaskResult(RUNNING): %v", err)
	}
	attempts, err := e.st.ListAttempts(ctx, task.ID)
	if err != nil || len(attempts) == 0 {
		t.Fatalf("ListAttempts = %v, %v", attempts, err)
	}
	attempt := attempts[len(attempts)-1]
	started := time.Now().UTC()
	if err := e.st.UpdateAttempt(ctx, attempt.ID, domain.StepStatusRunning, &started, nil); err != nil {
		t.Fatalf("UpdateAttempt(RUNNING): %v", err)
	}
	attempt.Status = domain.StepStatusRunning
	return &claim{task: running, attempt: attempt, projectID: projectID}, attempt
}

// appendTestBarrier 用持久事件确认订阅已注册：收到该 seq 即证明 Stream
// 已完成注册/补读并进入投递路径，替代固定睡眠判就绪。
func appendTestBarrier(t *testing.T, e *env) domain.DomainEvent {
	t.Helper()
	ev := domain.DomainEvent{Type: "test.progress_barrier", Data: map[string]any{}}
	err := e.st.DoTx(context.Background(), func(tx *store.Tx) error {
		seq, err := tx.AppendEvent(context.Background(), ev, time.Now().UTC())
		if err != nil {
			return err
		}
		ev.Seq = seq
		return nil
	})
	if err != nil {
		t.Fatalf("append progress subscription barrier: %v", err)
	}
	e.bus.Publish(ev)
	return ev
}

// subscribeLive 订阅总线并用持久屏障等待订阅生效，返回实时事件通道。
func subscribeLive(t *testing.T, e *env) (<-chan domain.DomainEvent, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	received := make(chan domain.DomainEvent, 256)
	go func() {
		_ = e.bus.Stream(ctx, 0, func(ev domain.DomainEvent) error {
			select {
			case received <- ev:
				return nil
			case <-ctx.Done():
				return nil
			}
		})
	}()
	barrier := appendTestBarrier(t, e)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-received:
			if ev.Seq == barrier.Seq && ev.Type == barrier.Type {
				return received, cancel
			}
		case <-deadline:
			cancel()
			t.Fatal("progress stream subscription barrier not observed")
			return nil, nil
		}
	}
}

// waitProgressEvents 用截止时间等待指定数量的 task.progress（条件等待，非固定睡眠）。
func waitProgressEvents(t *testing.T, ch <-chan domain.DomainEvent, want int) []domain.DomainEvent {
	t.Helper()
	deadline := time.After(3 * time.Second)
	got := make([]domain.DomainEvent, 0, want)
	for len(got) < want {
		select {
		case ev := <-ch:
			if ev.Type == taskProgressEventType {
				got = append(got, ev)
			}
		case <-deadline:
			t.Fatalf("waiting for %d progress events, got %d: %+v", want, len(got), got)
		}
	}
	return got
}

// expectNoProgressEvents 在有限窗口内确认没有额外进度事件（负向断言需要静默窗口）。
func expectNoProgressEvents(t *testing.T, ch <-chan domain.DomainEvent, wait time.Duration) {
	t.Helper()
	deadline := time.After(wait)
	for {
		select {
		case ev := <-ch:
			if ev.Type == taskProgressEventType {
				t.Fatalf("unexpected progress event: %+v", ev)
			}
		case <-deadline:
			return
		}
	}
}

// installProgressWriteProbe 用测试隔离 SQLite trigger 真实统计 tasks.progress
// 的 UPDATE 次数；不修改生产代码、共享 schema 或其他测试数据库。
func installProgressWriteProbe(t *testing.T, e *env) func() int {
	t.Helper()
	db := e.st.DB()
	if _, err := db.Exec(`CREATE TABLE test_progress_writes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		progress INTEGER NOT NULL
	)`); err != nil {
		t.Fatalf("create progress write probe: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER test_progress_write_probe
		AFTER UPDATE OF progress ON tasks
		WHEN OLD.progress <> NEW.progress
		BEGIN
			INSERT INTO test_progress_writes (progress) VALUES (NEW.progress);
		END`); err != nil {
		t.Fatalf("create progress write trigger: %v", err)
	}
	return func() int {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM test_progress_writes`).Scan(&count); err != nil {
			t.Fatalf("count progress writes: %v", err)
		}
		return count
	}
}

// installProgressWriteFailure 让写指定百分比的 UPDATE 在 SQLite 层失败，
// 模拟真实持久化错误并保留可恢复性；返回移除函数。
func installProgressWriteFailure(t *testing.T, e *env, percent int) func() {
	t.Helper()
	db := e.st.DB()
	if _, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER test_progress_write_failure
		BEFORE UPDATE OF progress ON tasks
		WHEN NEW.progress = %d
		BEGIN
			SELECT RAISE(ABORT, 'injected progress write failure');
		END`, percent)); err != nil {
		t.Fatalf("create progress failure trigger: %v", err)
	}
	return func() {
		if _, err := db.Exec(`DROP TRIGGER test_progress_write_failure`); err != nil {
			t.Fatalf("drop progress failure trigger: %v", err)
		}
	}
}

// TestProgressReporterThrottlesAndPublishes 守护节流与快照先于发布：
// 首条合法反馈立即接受，之后仅严格递增且间隔 >=250ms 才写库并发布。
func TestProgressReporterThrottlesAndPublishes(t *testing.T) {
	e := newEnv(t, envOptions{executor: &fakeExecutor{}})
	cl, _ := seedProgressClaim(t, e)
	progressWrites := installProgressWriteProbe(t, e)
	clock := newManualClock()
	reporter := newProgressReporter(context.Background(), e.st, e.bus, e.log, cl, clock.Now)
	defer reporter.Close()
	received, cancel := subscribeLive(t, e)
	defer cancel()

	reporter.report("decoding", 1, 10)
	if got := e.mustTask(cl.task.ID); got.Progress != 10 {
		t.Fatalf("first progress = %d, want 10", got.Progress)
	}
	clock.Advance(100 * time.Millisecond)
	reporter.report("decoding", 2, 10)
	if got := e.mustTask(cl.task.ID); got.Progress != 10 {
		t.Fatalf("progress accepted before interval = %d, want 10", got.Progress)
	}
	clock.Advance(150 * time.Millisecond) // 距上次接受 250ms
	reporter.report("decoding", 2, 10)
	if got := e.mustTask(cl.task.ID); got.Progress != 20 {
		t.Fatalf("progress after interval = %d, want 20", got.Progress)
	}
	clock.Advance(300 * time.Millisecond)
	reporter.report("decoding", 2, 10) // 重复
	clock.Advance(300 * time.Millisecond)
	reporter.report("decoding", 1, 10) // 倒退
	clock.Advance(300 * time.Millisecond)
	reporter.report("decoding", 13, 10) // 非法 current>total
	clock.Advance(300 * time.Millisecond)
	reporter.report("decoding", 1, 0) // 非法 total

	if got := e.mustTask(cl.task.ID); got.Progress != 20 {
		t.Fatalf("progress after rejected reports = %d, want 20", got.Progress)
	}
	progress := waitProgressEvents(t, received, 2)
	expectNoProgressEvents(t, received, 150*time.Millisecond)
	if got := progressWrites(); got != 2 {
		t.Fatalf("actual tasks.progress UPDATEs = %d, want 2", got)
	}
	t.Logf("measured: tasks.progress UPDATEs=%d, live events=%d", progressWrites(), len(progress))
	for i, want := range []int{10, 20} {
		ev := progress[i]
		if ev.Seq != 0 || ev.SchemaVersion != 1 {
			t.Fatalf("event envelope = %+v", ev)
		}
		if ev.TaskID != cl.task.ID || ev.ProjectID != cl.projectID {
			t.Fatalf("event identity = %+v", ev)
		}
		if ev.StateVersion != cl.task.StateVersion {
			t.Fatalf("event state_version = %d, want %d", ev.StateVersion, cl.task.StateVersion)
		}
		wantData := map[string]any{
			"step_id":      cl.attempt.StepID,
			"attempt":      cl.attempt.AttemptNo,
			"server_epoch": "11",
			"progress":     want,
		}
		if len(ev.Data) != len(wantData) {
			t.Fatalf("event data = %+v, want exactly %v", ev.Data, wantData)
		}
		for key, wantValue := range wantData {
			if ev.Data[key] != wantValue {
				t.Errorf("event data[%q] = %v, want %v", key, ev.Data[key], wantValue)
			}
		}
	}
}

// TestProgressReporterRejectsStaleAttemptAndTerminalTask 守护旧尝试/取消/终态：
// 迟到回调不能写另一尝试，也不能回退终态快照。
func TestProgressReporterRejectsStaleAttemptAndTerminalTask(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, envOptions{executor: &fakeExecutor{}})
	cl, oldAttempt := seedProgressClaim(t, e)
	progressWrites := installProgressWriteProbe(t, e)
	clock := newManualClock()
	stale := newProgressReporter(context.Background(), e.st, e.bus, e.log, cl, clock.Now)
	defer stale.Close()
	received, cancel := subscribeLive(t, e)
	defer cancel()

	// 新尝试成为最新 RUNNING：旧 reporter 的进度被拒绝。
	newer := domain.Attempt{
		ID: "attempt-newer", TaskID: cl.task.ID, StepID: oldAttempt.StepID,
		AttemptNo: oldAttempt.AttemptNo + 1, Epoch: oldAttempt.Epoch,
		Status: domain.StepStatusRunning, StartedAt: &cl.task.UpdatedAt,
	}
	if err := e.st.CreateAttempt(ctx, newer); err != nil {
		t.Fatalf("CreateAttempt(newer): %v", err)
	}
	stale.report("decoding", 1, 2)
	if got := e.mustTask(cl.task.ID); got.Progress != 0 {
		t.Fatalf("stale attempt wrote progress %d, want 0", got.Progress)
	}
	if got := progressWrites(); got != 0 {
		t.Fatalf("actual tasks.progress UPDATEs after stale callback = %d, want 0", got)
	}
	t.Logf("measured: stale attempt callback -> tasks.progress UPDATEs=%d", progressWrites())

	current, err := e.st.GetTask(ctx, cl.task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	freshClaim := &claim{task: current, attempt: newer, projectID: cl.projectID}
	fresh := newProgressReporter(context.Background(), e.st, e.bus, e.log, freshClaim, clock.Now)
	defer fresh.Close()
	fresh.report("decoding", 1, 2)
	if got := e.mustTask(cl.task.ID); got.Progress != 50 {
		t.Fatalf("fresh attempt progress = %d, want 50", got.Progress)
	}
	if got := progressWrites(); got != 1 {
		t.Fatalf("actual tasks.progress UPDATEs after fresh callback = %d, want 1", got)
	}
	t.Logf("measured: fresh attempt callback -> tasks.progress UPDATEs=%d", progressWrites())

	// 取消请求：任务离开 RUNNING，后续进度不能写入。
	clock.Advance(time.Second)
	canceled, err := e.st.UpdateTaskStatus(ctx, cl.task.ID, domain.TaskStatusCancelRequested,
		current.StateVersion, time.Now().UTC())
	if err != nil {
		t.Fatalf("UpdateTaskStatus(CANCEL_REQUESTED): %v", err)
	}
	fresh.report("decoding", 2, 2)
	if got := e.mustTask(cl.task.ID); got.Progress != 50 {
		t.Fatalf("progress after cancel = %d, want 50", got.Progress)
	}

	// 成功终态写 100；之后任何迟到进度都不能回退。
	clock.Advance(time.Second)
	succeeded, err := e.st.UpdateTaskResult(ctx, cl.task.ID, domain.TaskStatusSucceeded,
		canceled.ArtifactIDs, nil, canceled.StateVersion, time.Now().UTC())
	if err != nil {
		t.Fatalf("UpdateTaskResult(SUCCEEDED): %v", err)
	}
	if succeeded.Progress != 100 {
		t.Fatalf("success progress = %d, want 100", succeeded.Progress)
	}
	fresh.report("decoding", 2, 2)
	progress := waitProgressEvents(t, received, 1)
	expectNoProgressEvents(t, received, 150*time.Millisecond)
	if len(progress) != 1 || progress[0].Data["progress"] != 50 {
		t.Fatalf("live progress = %+v, want only the accepted 50", progress)
	}
	// 终态事务自身把快照写为 100，属于合法状态写入；迟到回调不再新增写入。
	if got := progressWrites(); got != 2 {
		t.Fatalf("actual tasks.progress UPDATEs = %d, want 2 (50 then terminal 100)", got)
	}
	t.Logf("measured: after terminal success -> tasks.progress UPDATEs=%d (includes terminal 100)", progressWrites())
}

// TestProgressReporterNilBusAndFailedWrites 守护反馈失败不影响成功条件：
// nil bus 安全；写库失败/执行取消不发布与事实不符的事件。
func TestProgressReporterNilBusAndFailedWrites(t *testing.T) {
	t.Run("nil bus", func(t *testing.T) {
		e := newEnv(t, envOptions{executor: &fakeExecutor{}})
		cl, _ := seedProgressClaim(t, e)
		reporter := newProgressReporter(context.Background(), e.st, nil, e.log, cl, newManualClock().Now)
		reporter.report("decoding", 1, 2)
		reporter.Close()
		if got := e.mustTask(cl.task.ID); got.Progress != 50 {
			t.Fatalf("progress = %d, want 50", got.Progress)
		}
	})

	t.Run("canceled execution context", func(t *testing.T) {
		e := newEnv(t, envOptions{executor: &fakeExecutor{}})
		cl, _ := seedProgressClaim(t, e)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		reporter := newProgressReporter(ctx, e.st, e.bus, e.log, cl, newManualClock().Now)
		defer reporter.Close()
		received, stop := subscribeLive(t, e)
		defer stop()
		reporter.report("decoding", 1, 2)
		expectNoProgressEvents(t, received, 250*time.Millisecond)
		if got := e.mustTask(cl.task.ID); got.Progress != 0 {
			t.Fatalf("progress = %d, want 0", got.Progress)
		}
	})

	t.Run("store closed", func(t *testing.T) {
		e := newEnv(t, envOptions{executor: &fakeExecutor{}})
		cl, _ := seedProgressClaim(t, e)
		reporter := newProgressReporter(context.Background(), e.st, e.bus, e.log, cl, newManualClock().Now)
		defer reporter.Close()
		received, stop := subscribeLive(t, e)
		defer stop()
		if err := e.st.Close(); err != nil {
			t.Fatalf("Close store: %v", err)
		}
		reporter.report("decoding", 1, 2)
		expectNoProgressEvents(t, received, 250*time.Millisecond)
	})
}

// TestProgressReporterConcurrentReportsStayOrdered 守护并发回调：
// 锁内串行化后，写入/发布严格递增，最终快照为最大百分比。
func TestProgressReporterConcurrentReportsStayOrdered(t *testing.T) {
	e := newEnv(t, envOptions{executor: &fakeExecutor{}})
	cl, _ := seedProgressClaim(t, e)
	progressWrites := installProgressWriteProbe(t, e)
	received, cancel := subscribeLive(t, e)
	defer cancel()

	base := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	var ticks atomic.Int64
	clock := func() time.Time {
		return base.Add(time.Duration(ticks.Add(1)) * progressMinInterval)
	}
	reporter := newProgressReporter(context.Background(), e.st, e.bus, e.log, cl, clock)
	defer reporter.Close()

	var wg sync.WaitGroup
	for i := 1; i <= 32; i++ {
		wg.Add(1)
		go func(pct int) {
			defer wg.Done()
			reporter.report("decoding", pct, 100)
		}(i * 3)
	}
	wg.Wait()

	// 触发器给出实际条件写次数；报告调用同步完成，按该数量条件等待 live 事件。
	wantEvents := progressWrites()
	if wantEvents == 0 {
		t.Fatal("no actual progress writes")
	}
	progress := waitProgressEvents(t, received, wantEvents)
	if got := progressWrites(); got != wantEvents {
		t.Fatalf("actual tasks.progress UPDATEs = %d, live events = %d; want stable equal", got, wantEvents)
	}
	t.Logf("measured: concurrent callbacks=%d -> tasks.progress UPDATEs=%d, live events=%d", 32, wantEvents, len(progress))
	last := 0
	for _, ev := range progress {
		pct, ok := ev.Data["progress"].(int)
		if !ok || pct <= last {
			t.Fatalf("progress events not strictly increasing: last=%d event=%+v", last, ev)
		}
		last = pct
	}
	if last != 96 {
		t.Fatalf("last published progress = %d, want 96", last)
	}
	if got := e.mustTask(cl.task.ID); got.Progress != 96 {
		t.Fatalf("snapshot progress = %d, want 96", got.Progress)
	}
}

func (e *env) mustTask(id domain.ID) domain.Task {
	e.t.Helper()
	task, err := e.st.GetTask(context.Background(), id)
	if err != nil {
		e.t.Fatalf("GetTask: %v", err)
	}
	return task
}

// TestProgressPercentHandlesExtremes 守护百分比计算：合法边界精确，
// 平台整数极值不溢出，非法计数不伪造 0%。
func TestProgressPercentHandlesExtremes(t *testing.T) {
	cases := []struct {
		current int
		total   int
		want    int
		ok      bool
	}{
		{current: 0, total: 10, want: 0, ok: true},
		{current: 5, total: 10, want: 50, ok: true},
		{current: 1, total: 3, want: 33, ok: true},
		{current: 10, total: 10, want: 100, ok: true},
		{current: math.MaxInt, total: math.MaxInt, want: 100, ok: true},
		{current: math.MaxInt - 1, total: math.MaxInt, want: 99, ok: true},
		{current: 10, total: 0, ok: false},
		{current: 0, total: 0, ok: false},
		{current: -1, total: 10, ok: false},
		{current: 11, total: 10, ok: false},
		{current: 0, total: -1, ok: false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("current=%d,total=%d", tc.current, tc.total), func(t *testing.T) {
			got, ok := progressPercent(tc.current, tc.total)
			if ok != tc.ok {
				t.Errorf("progressPercent(%d,%d) ok = %v, want %v", tc.current, tc.total, ok, tc.ok)
				return
			}
			if ok && got != tc.want {
				t.Errorf("progressPercent(%d,%d) = %d, want %d", tc.current, tc.total, got, tc.want)
			}
		})
	}
}

// TestProgressReporterFloodWritesOncePerInterval 守护进度洪泛：
// 同一时间窗内大量回调只产生一次条件写与一次 live 事件。
func TestProgressReporterFloodWritesOncePerInterval(t *testing.T) {
	e := newEnv(t, envOptions{executor: &fakeExecutor{}})
	cl, _ := seedProgressClaim(t, e)
	progressWrites := installProgressWriteProbe(t, e)
	clock := newManualClock()
	reporter := newProgressReporter(context.Background(), e.st, e.bus, e.log, cl, clock.Now)
	defer reporter.Close()
	received, cancel := subscribeLive(t, e)
	defer cancel()

	// 同一时间窗内 1000 条回调只允许第一次条件写与发布。
	for i := 0; i < 1000; i++ {
		reporter.report("decoding", 100+i, 2000)
	}
	progress := waitProgressEvents(t, received, 1)
	expectNoProgressEvents(t, received, 150*time.Millisecond)
	if len(progress) != 1 {
		t.Fatalf("flood produced %d live events, want 1", len(progress))
	}
	if progress[0].Data["progress"] != 5 {
		t.Fatalf("flood event progress = %v, want 5", progress[0].Data["progress"])
	}
	if got := progressWrites(); got != 1 {
		t.Fatalf("actual tasks.progress UPDATEs after flood = %d, want 1", got)
	}
	t.Logf("measured: 1000 callbacks in one window -> tasks.progress UPDATEs=%d, live events=%d", progressWrites(), len(progress))

	// 节流窗口过后只有更高的百分比能再写一次。
	clock.Advance(progressMinInterval)
	reporter.report("decoding", 1000, 2000)
	if got := e.mustTask(cl.task.ID); got.Progress != 50 {
		t.Fatalf("progress after interval = %d, want 50", got.Progress)
	}
	progress = waitProgressEvents(t, received, 1)
	expectNoProgressEvents(t, received, 150*time.Millisecond)
	if len(progress) != 1 || progress[0].Data["progress"] != 50 {
		t.Fatalf("second window events = %+v, want one 50", progress)
	}
	if got := progressWrites(); got != 2 {
		t.Fatalf("actual tasks.progress UPDATEs after second window = %d, want 2", got)
	}
	t.Logf("measured: second window -> tasks.progress UPDATEs=%d, live events=%d", progressWrites(), len(progress))
}

// TestProgressReporterRecoversSamePercentageAfterWriteFailure 守护快照写失败后的恢复：
// 失败不发布、不推进节流标记，同一百分比在持久层恢复后仍可立即接受。
func TestProgressReporterRecoversSamePercentageAfterWriteFailure(t *testing.T) {
	e := newEnv(t, envOptions{executor: &fakeExecutor{}})
	cl, _ := seedProgressClaim(t, e)
	progressWrites := installProgressWriteProbe(t, e)
	clock := newManualClock()
	reporter := newProgressReporter(context.Background(), e.st, e.bus, e.log, cl, clock.Now)
	defer reporter.Close()
	received, cancel := subscribeLive(t, e)
	defer cancel()

	restore := installProgressWriteFailure(t, e, 42)
	reporter.report("decoding", 42, 100)
	if got := e.mustTask(cl.task.ID); got.Progress != 0 {
		t.Fatalf("snapshot after failed write = %d, want 0", got.Progress)
	}
	if got := progressWrites(); got != 0 {
		t.Fatalf("actual tasks.progress UPDATEs after failure = %d, want 0", got)
	}
	t.Logf("measured: injected write failure -> tasks.progress UPDATEs=%d", progressWrites())
	expectNoProgressEvents(t, received, 250*time.Millisecond)
	restore()

	// 同一时钟、同一百分比：失败没有推进节流标记，恢复后立即接受。
	reporter.report("decoding", 42, 100)
	if got := e.mustTask(cl.task.ID); got.Progress != 42 {
		t.Fatalf("snapshot after recovery = %d, want 42", got.Progress)
	}
	if got := progressWrites(); got != 1 {
		t.Fatalf("actual tasks.progress UPDATEs after recovery = %d, want 1", got)
	}
	t.Logf("measured: same percentage after recovery -> tasks.progress UPDATEs=%d", progressWrites())
	progress := waitProgressEvents(t, received, 1)
	expectNoProgressEvents(t, received, 150*time.Millisecond)
	if len(progress) != 1 || progress[0].Data["progress"] != 42 {
		t.Fatalf("recovered live progress = %+v, want one 42", progress)
	}
}

// TestProgressReporterBoundedDatabaseWait 守护数据库等待有界：
// 连接被占用时反馈在有界时间内失败且不发布；连接释放后同一百分比可恢复。
func TestProgressReporterBoundedDatabaseWait(t *testing.T) {
	e := newEnv(t, envOptions{executor: &fakeExecutor{}})
	cl, _ := seedProgressClaim(t, e)
	progressWrites := installProgressWriteProbe(t, e)
	clock := newManualClock()
	reporter := newProgressReporter(context.Background(), e.st, e.bus, e.log, cl, clock.Now)
	defer reporter.Close()
	received, cancel := subscribeLive(t, e)
	defer cancel()

	// 占用唯一连接，模拟持久层长时间不可用。
	tx, err := e.st.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin holding transaction: %v", err)
	}
	start := time.Now()
	reporter.report("decoding", 42, 100)
	elapsed := time.Since(start)
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback holding transaction: %v", err)
	}
	if elapsed < progressWriteTimeout-250*time.Millisecond || elapsed > progressWriteTimeout+2*time.Second {
		t.Fatalf("bounded database wait = %s, want about %s", elapsed, progressWriteTimeout)
	}
	t.Logf("measured: busy connection write wait=%s (timeout=%s)", elapsed.Round(time.Millisecond), progressWriteTimeout)
	if got := e.mustTask(cl.task.ID); got.Progress != 0 {
		t.Fatalf("snapshot after bounded failure = %d, want 0", got.Progress)
	}
	if got := progressWrites(); got != 0 {
		t.Fatalf("actual tasks.progress UPDATEs after bounded failure = %d, want 0", got)
	}
	expectNoProgressEvents(t, received, 150*time.Millisecond)

	// 连接释放后，同一百分比立即恢复接受。
	reporter.report("decoding", 42, 100)
	if got := e.mustTask(cl.task.ID); got.Progress != 42 {
		t.Fatalf("snapshot after release = %d, want 42", got.Progress)
	}
	if got := progressWrites(); got != 1 {
		t.Fatalf("actual tasks.progress UPDATEs after release = %d, want 1", got)
	}
	waitProgressEvents(t, received, 1)
}

// TestRetryTaskClearsFailedProgressAndActualClaim 守护真实受理链路（非直接 Store 调用）：
// 失败尝试留下非零快照 → service.RetryTask 真实受理并落库 0 → 调度器真实 claim 从 0 开始
// → 成功终态写 100；写频次由 SQLite trigger 实测。
func TestRetryTaskClearsFailedProgressAndActualClaim(t *testing.T) {
	var executions atomic.Int64
	secondStarted := make(chan struct{}, 1)
	releaseSecond := make(chan struct{})
	var releaseOnce sync.Once
	openSecond := func() { releaseOnce.Do(func() { close(releaseSecond) }) }
	defer openSecond()

	executor := &fakeExecutor{}
	executor.progress = func(report execadapter.ProgressFunc) {
		if executions.Load() == 0 {
			report("decoding", 1, 2) // 旧尝试留下 50% 快照
		}
	}
	executor.result = func(req execadapter.Request) execadapter.Result {
		binding := execadapter.Binding{
			Workspace: req.Workspace, ServerEpoch: req.ServerEpoch,
			TaskID: req.TaskID, StepID: req.StepID, Attempt: req.Attempt,
		}
		if executions.Add(1) == 1 {
			return execadapter.Result{
				Error: &execadapter.ExecError{
					Code: string(domain.ErrorCodeWorkerExited), Message: "controlled failure", Retryable: true,
				},
				Binding: binding, OutputDir: req.OutputDir,
			}
		}
		select {
		case secondStarted <- struct{}{}:
		default:
		}
		<-releaseSecond
		return execadapter.Result{Binding: binding, OutputDir: req.OutputDir}
	}

	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	lock := executionLockForNode(domain.NodeKeyAnalyze)
	created, err := e.svc.CreateTask(context.Background(), service.Idem{}, service.CreateTaskRequest{
		ProjectID: projectID,
		Kind:      domain.TaskKindNode,
		NodeKey:   domain.NodeKeyAnalyze,
		ModelLock: &lock,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	task := created.Body.(domain.Task)
	progressWrites := installProgressWriteProbe(t, e)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()
	failed := e.waitTask(task.ID, domain.TaskStatusFailed, 5*time.Second)
	if failed.Progress != 50 {
		t.Fatalf("failed snapshot = %s/%d, want FAILED/50", failed.Status, failed.Progress)
	}
	if got := progressWrites(); got != 1 {
		t.Fatalf("actual tasks.progress UPDATEs after failure = %d, want 1", got)
	}
	t.Logf("measured: actual failure snapshot = %s/%d, tasks.progress UPDATEs=%d",
		failed.Status, failed.Progress, progressWrites())

	retryResponse, err := e.svc.RetryTask(context.Background(), service.Idem{}, string(task.ID))
	if err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	if retryResponse.Status != 202 {
		t.Fatalf("RetryTask status = %d, want 202", retryResponse.Status)
	}
	retried := retryResponse.Body.(domain.Task)
	if retried.Status != domain.TaskStatusQueued || retried.Progress != 0 {
		t.Fatalf("RetryTask response = %s/%d, want QUEUED/0", retried.Status, retried.Progress)
	}
	// 重试事务把 50 写回 0；claim 的 0→0 不产生额外 UPDATE，计数稳定可读。
	if got := progressWrites(); got != 2 {
		t.Fatalf("actual tasks.progress UPDATEs after retry = %d, want 2 (50 then retry reset 0)", got)
	}
	t.Logf("measured: RetryTask response = %s/%d, tasks.progress UPDATEs=%d (50 then retry reset 0)",
		retried.Status, retried.Progress, progressWrites())

	select {
	case <-secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not claim the retried task")
	}
	claimed, err := e.st.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask after claim: %v", err)
	}
	if claimed.Status != domain.TaskStatusRunning || claimed.Progress != 0 {
		t.Fatalf("actual claim snapshot = %s/%d, want RUNNING/0", claimed.Status, claimed.Progress)
	}
	t.Logf("measured: actual scheduler claim snapshot = %s/%d", claimed.Status, claimed.Progress)

	openSecond()
	final := e.waitTask(task.ID, domain.TaskStatusSucceeded, 5*time.Second)
	if final.Progress != 100 {
		t.Fatalf("success snapshot = %s/%d, want SUCCEEDED/100", final.Status, final.Progress)
	}
	if got := progressWrites(); got != 3 {
		t.Fatalf("actual tasks.progress UPDATEs after success = %d, want 3 (50, retry 0, success 100)", got)
	}
	t.Logf("measured: success snapshot = %s/%d, tasks.progress UPDATEs=%d (50, retry 0, success 100)",
		final.Status, final.Progress, progressWrites())
}
