package updateguard

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

const (
	testWorkspace = "ws-update"
	testInstance  = "instance-update"
	testEpoch     = int64(3)
)

var testTime = time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)

// fakeOwnership 是可控内存拥有者替身；不冒充真实执行器。
type fakeOwnership struct {
	mu       sync.Mutex
	inMemory bool
	block    string
	settle   func(context.Context) error
	calls    int
}

func (o *fakeOwnership) InMemoryActive() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.inMemory
}

func (o *fakeOwnership) ExecutorBlock() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.block
}

func (o *fakeOwnership) Settle(ctx context.Context) error {
	o.mu.Lock()
	o.calls++
	fn := o.settle
	o.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return nil
}

func (o *fakeOwnership) callCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls
}

func newGuardEnv(t *testing.T, ownership Ownership, budget time.Duration) (*Guard, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "yaomu.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	guard := New(Options{
		Store:         st,
		Ownership:     ownership,
		WorkspaceID:   testWorkspace,
		InstanceID:    testInstance,
		ServerEpoch:   testEpoch,
		Budget:        budget,
		CleanupBudget: budget,
	})
	return guard, st
}

func request(mode, operation string) Request {
	return Request{
		Mode:        mode,
		OperationID: operation,
		WorkspaceID: testWorkspace,
		InstanceID:  testInstance,
		ServerEpoch: testEpoch,
	}
}

func seedProject(t *testing.T, st *store.Store) {
	t.Helper()
	if err := st.CreateProject(context.Background(), domain.Project{
		ID: "project-update", WorkspaceID: testWorkspace, Name: "更新准备项目",
		SchemaVersion: 1, EditVersion: 1, CreatedAt: testTime, UpdatedAt: testTime,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
}

func seedTask(t *testing.T, st *store.Store, id string, status domain.TaskStatus) {
	t.Helper()
	err := st.DoTx(context.Background(), func(tx *store.Tx) error {
		return tx.CreateTask(context.Background(), domain.Task{
			ID: domain.ID(id), ProjectID: "project-update", Kind: domain.TaskKindNode,
			NodeKey: domain.NodeKeyAnalyze, Status: status, CreatedAt: testTime, UpdatedAt: testTime,
		})
	})
	if err != nil {
		t.Fatalf("create task %s: %v", id, err)
	}
}

func seedRun(t *testing.T, st *store.Store, id string, status domain.RunStatus) {
	t.Helper()
	err := st.DoTx(context.Background(), func(tx *store.Tx) error {
		return tx.CreateWorkflowRun(context.Background(), domain.WorkflowRun{
			ID: domain.ID(id), ProjectID: "project-update", Name: "更新准备 Run",
			Template: "full_pipeline", Status: status, SourceLanguage: "en",
			Languages: []string{"zh"}, CreatedAt: testTime, UpdatedAt: testTime,
		})
	})
	if err != nil {
		t.Fatalf("create run %s: %v", id, err)
	}
}

func seedAttempt(t *testing.T, st *store.Store, taskID string, status domain.StepStatus) {
	t.Helper()
	err := st.DoTx(context.Background(), func(tx *store.Tx) error {
		return tx.CreateAttempt(context.Background(), domain.Attempt{
			ID: "attempt-" + domain.ID(taskID), TaskID: domain.ID(taskID), StepID: "main",
			AttemptNo: 1, Epoch: testEpoch, Status: status,
		})
	})
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}
}

func seedInstallation(t *testing.T, st *store.Store, id string, phase domain.InstallationPhase) {
	t.Helper()
	err := st.DoTx(context.Background(), func(tx *store.Tx) error {
		return tx.CreateInstallation(context.Background(), domain.Installation{
			ID: domain.ID(id), Slot: domain.CapabilitySlotASR, Phase: phase,
			CreatedAt: testTime, UpdatedAt: testTime,
		})
	})
	if err != nil {
		t.Fatalf("create installation: %v", err)
	}
}

// TestCheckIdleIsReadOnlyAndPrepareSealsOnce 覆盖 check 无许可、prepare 仅封闸
// 一次、同一操作复用内存证明、其他操作要求重启。
func TestCheckIdleIsReadOnlyAndPrepareSealsOnce(t *testing.T) {
	ownership := &fakeOwnership{}
	guard, st := newGuardEnv(t, ownership, time.Second)
	ctx := context.Background()

	check, err := guard.Handle(ctx, request(ModeCheck, "op-1"))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if check.State != StateIdle || check.Reason != "" {
		t.Fatalf("check = %+v, want idle", check)
	}
	if st.Sealed() {
		t.Fatal("check sealed the store")
	}
	if ownership.callCount() != 0 {
		t.Fatal("check ran cleanup")
	}

	prepared, err := guard.Handle(ctx, request(ModePrepare, "op-1"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared.State != StatePrepared || prepared.Reason != "" {
		t.Fatalf("prepare = %+v, want prepared", prepared)
	}
	if !st.Sealed() || !guard.Sealed() {
		t.Fatal("prepare did not seal the store")
	}
	if ownership.callCount() != 1 {
		t.Fatalf("settle calls = %d, want 1", ownership.callCount())
	}

	again, err := guard.Handle(ctx, request(ModePrepare, "op-1"))
	if err != nil || again.State != StatePrepared {
		t.Fatalf("repeat prepare same op = %+v, %v, want prepared", again, err)
	}
	if ownership.callCount() != 1 {
		t.Fatalf("repeat prepare re-ran cleanup: %d", ownership.callCount())
	}

	other, err := guard.Handle(ctx, request(ModePrepare, "op-2"))
	if err != nil {
		t.Fatalf("different op prepare: %v", err)
	}
	if other.State != StateBlocked || other.Reason != ReasonRestartRequired {
		t.Fatalf("different op = %+v, want blocked/restart_required", other)
	}
	if ownership.callCount() != 1 {
		t.Fatal("different operation re-ran cleanup")
	}

	blockedCheck, err := guard.Handle(ctx, request(ModeCheck, "op-2"))
	if err != nil || blockedCheck.State != StateBlocked || blockedCheck.Reason != ReasonRestartRequired {
		t.Fatalf("check after seal = %+v, %v, want blocked/restart_required", blockedCheck, err)
	}
	if err := st.DoTx(ctx, func(tx *store.Tx) error { return nil }); !errors.Is(err, store.ErrSealed) {
		t.Fatalf("business write after prepared = %v, want ErrSealed", err)
	}
}

// TestCheckAndPrepareBlockedMatrix 逐项锁定显式活跃集与内存拥有者阻断。
func TestCheckAndPrepareBlockedMatrix(t *testing.T) {
	cases := []struct {
		name    string
		seed    func(*testing.T, *store.Store)
		own     *fakeOwnership
		want    string
		blocked bool
	}{
		{
			name: "queued task", want: ReasonActiveWork, blocked: true,
			seed: func(t *testing.T, st *store.Store) { seedTask(t, st, "task-queued", domain.TaskStatusQueued) },
		},
		{
			name: "waiting resource task", want: ReasonActiveWork, blocked: true,
			seed: func(t *testing.T, st *store.Store) {
				seedTask(t, st, "task-waiting", domain.TaskStatusWaitingResource)
			},
		},
		{
			name: "paused task", want: ReasonActiveWork, blocked: true,
			seed: func(t *testing.T, st *store.Store) { seedTask(t, st, "task-paused", domain.TaskStatusPaused) },
		},
		{
			name: "waiting review run", want: ReasonActiveWork, blocked: true,
			seed: func(t *testing.T, st *store.Store) {
				seedRun(t, st, "run-review", domain.RunStatusWaitingReview)
			},
		},
		{
			name: "pending attempt", want: ReasonActiveWork, blocked: true,
			seed: func(t *testing.T, st *store.Store) {
				seedTask(t, st, "task-attempt", domain.TaskStatusSucceeded)
				seedAttempt(t, st, "task-attempt", domain.StepStatusPending)
			},
		},
		{
			name: "downloading installation", want: ReasonActiveWork, blocked: true,
			seed: func(t *testing.T, st *store.Store) {
				seedInstallation(t, st, "inst-active", domain.InstallationPhaseDownloading)
			},
		},
		{
			name: "historical failed task only", want: "", blocked: false,
			seed: func(t *testing.T, st *store.Store) { seedTask(t, st, "task-failed", domain.TaskStatusFailed) },
		},
		{
			name: "historical interrupted task only", want: "", blocked: false,
			seed: func(t *testing.T, st *store.Store) {
				seedTask(t, st, "task-interrupted", domain.TaskStatusInterrupted)
			},
		},
		{
			name: "done installation only", want: "", blocked: false,
			seed: func(t *testing.T, st *store.Store) {
				seedInstallation(t, st, "inst-done", domain.InstallationPhaseDone)
			},
		},
		{
			name: "scheduler reservation", want: ReasonActiveWork, blocked: true,
			own: &fakeOwnership{inMemory: true},
		},
		{
			name: "executor starting", want: ReasonExecutorStarting, blocked: true,
			own: &fakeOwnership{block: ReasonExecutorStarting},
		},
		{
			name: "executor unconfirmed", want: ReasonExecutorUnconfirmed, blocked: true,
			own: &fakeOwnership{block: ReasonExecutorUnconfirmed},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			own := tc.own
			if own == nil {
				own = &fakeOwnership{}
			}
			guard, st := newGuardEnv(t, own, time.Second)
			seedProject(t, st)
			if tc.seed != nil {
				tc.seed(t, st)
			}
			ctx := context.Background()
			check, err := guard.Handle(ctx, request(ModeCheck, "op-check"))
			if err != nil {
				t.Fatalf("check: %v", err)
			}
			prepare, err := guard.Handle(ctx, request(ModePrepare, "op-prepare"))
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			if !tc.blocked {
				if check.State != StateIdle || check.Reason != "" {
					t.Fatalf("check = %+v, want idle", check)
				}
				if prepare.State != StatePrepared {
					t.Fatalf("prepare = %+v, want prepared", prepare)
				}
				return
			}
			if st.Sealed() {
				t.Fatal("blocked preparation sealed the store")
			}
			if own.callCount() != 0 {
				t.Fatal("blocked preparation ran cleanup")
			}
			if check.State != StateBlocked || check.Reason != tc.want {
				t.Fatalf("check = %+v, want blocked/%s", check, tc.want)
			}
			if prepare.State != StateBlocked || prepare.Reason != tc.want {
				t.Fatalf("prepare = %+v, want blocked/%s", prepare, tc.want)
			}
		})
	}
}

// TestPrepareRequestInFlight 证明普通 handler/事务在飞时快速返回，且不封闸。
func TestPrepareRequestInFlight(t *testing.T) {
	ownership := &fakeOwnership{}
	guard, st := newGuardEnv(t, ownership, time.Second)
	seedProject(t, st)
	ctx := context.Background()
	if !guard.BeginRequest() {
		t.Fatal("request admission rejected before sealing")
	}
	result, err := guard.Handle(ctx, request(ModePrepare, "op-in-flight"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if result.State != StateBlocked || result.Reason != ReasonRequestInFlight {
		t.Fatalf("prepare with in-flight request = %+v, want request_in_flight", result)
	}
	if st.Sealed() {
		t.Fatal("request_in_flight sealed the store")
	}
	if ownership.callCount() != 0 {
		t.Fatal("request_in_flight ran cleanup")
	}
	guard.EndRequest()
	result, err = guard.Handle(ctx, request(ModePrepare, "op-in-flight"))
	if err != nil || result.State != StatePrepared {
		t.Fatalf("prepare after release = %+v, %v, want prepared", result, err)
	}
}

// TestPrepareIdentityAndShapeValidation 锁定坏形状 400、身份不符 409。
func TestPrepareIdentityAndShapeValidation(t *testing.T) {
	guard, _ := newGuardEnv(t, &fakeOwnership{}, time.Second)
	ctx := context.Background()

	badShape := []Request{
		{Mode: "unknown", OperationID: "op", WorkspaceID: testWorkspace, InstanceID: testInstance, ServerEpoch: testEpoch},
		{Mode: ModePrepare, OperationID: "", WorkspaceID: testWorkspace, InstanceID: testInstance, ServerEpoch: testEpoch},
		{Mode: ModePrepare, OperationID: "op", WorkspaceID: testWorkspace, InstanceID: testInstance, ServerEpoch: 0},
		{Mode: ModePrepare, OperationID: "op", WorkspaceID: testWorkspace, InstanceID: testInstance, ServerEpoch: maxSafeInteger + 1},
	}
	for _, req := range badShape {
		if _, err := guard.Handle(ctx, req); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("shape %+v error = %v, want INVALID_REQUEST", req, err)
		}
	}
	tooLong := string(make([]byte, maxIDLength+1))
	if _, err := guard.Handle(ctx, Request{
		Mode: ModePrepare, OperationID: tooLong, WorkspaceID: testWorkspace,
		InstanceID: testInstance, ServerEpoch: testEpoch,
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("overlong operation error = %v, want INVALID_REQUEST", err)
	}

	mismatched := request(ModePrepare, "op")
	mismatched.WorkspaceID = "ws-other"
	if _, err := guard.Handle(ctx, mismatched); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("identity mismatch error = %v, want REVISION_CONFLICT", err)
	}
	mismatchedEpoch := request(ModePrepare, "op")
	mismatchedEpoch.ServerEpoch = testEpoch + 1
	if _, err := guard.Handle(ctx, mismatchedEpoch); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("epoch mismatch error = %v, want REVISION_CONFLICT", err)
	}
	var domainErr *domain.Error
	if !errors.As(ErrIdentityMismatch, &domainErr) || domainErr.Code != domain.ErrorCodeRevisionConflict {
		t.Fatalf("identity error code = %v, want REVISION_CONFLICT", domainErr)
	}
}

// TestCleanupFailureStaysSealedAndSharesError 证明清理失败后不解封、不伪
// prepared，重复/其他操作共享同一失败。
func TestCleanupFailureStaysSealedAndSharesError(t *testing.T) {
	failure := errors.New("runner lease retained")
	ownership := &fakeOwnership{settle: func(context.Context) error { return failure }}
	guard, st := newGuardEnv(t, ownership, time.Second)
	seedProject(t, st)
	ctx := context.Background()

	result, err := guard.Handle(ctx, request(ModePrepare, "op-fail"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if result.State != StateBlocked || result.Reason != ReasonRestartRequired {
		t.Fatalf("failed cleanup = %+v, want blocked/restart_required", result)
	}
	if !st.Sealed() {
		t.Fatal("failed cleanup reopened the store")
	}
	if err := st.DoTx(ctx, func(tx *store.Tx) error { return nil }); !errors.Is(err, store.ErrSealed) {
		t.Fatalf("business write after failed cleanup = %v, want ErrSealed", err)
	}
	if ownership.callCount() != 1 {
		t.Fatalf("settle calls = %d, want 1", ownership.callCount())
	}

	again, err := guard.Handle(ctx, request(ModePrepare, "op-fail"))
	if err != nil || again.State != StateBlocked || again.Reason != ReasonRestartRequired {
		t.Fatalf("repeat same op = %+v, %v, want blocked/restart_required", again, err)
	}
	other, err := guard.Handle(ctx, request(ModePrepare, "op-other"))
	if err != nil || other.Reason != ReasonRestartRequired {
		t.Fatalf("other op = %+v, %v, want restart_required", other, err)
	}
	if ownership.callCount() != 1 {
		t.Fatalf("repeated cleanup after failure: %d", ownership.callCount())
	}
}

// TestCleanupTimeoutKeepsOwnershipAndLaterJoins 覆盖有界等待超时：不伪成功、
// 不重复清理；清理在后台完成后同一操作可读取真实 prepared。
func TestCleanupTimeoutKeepsOwnershipAndLaterJoins(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	ownership := &fakeOwnership{settle: func(ctx context.Context) error {
		close(started)
		<-release
		return nil
	}}
	guard, st := newGuardEnv(t, ownership, 80*time.Millisecond)
	seedProject(t, st)
	ctx := context.Background()

	result, err := guard.Handle(ctx, request(ModePrepare, "op-timeout"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if result.State != StateBlocked || result.Reason != ReasonExecutorUnconfirmed {
		t.Fatalf("timed-out cleanup = %+v, want blocked/executor_unconfirmed", result)
	}
	if !st.Sealed() {
		t.Fatal("timed-out cleanup reopened the store")
	}
	<-started
	if ownership.callCount() != 1 {
		t.Fatalf("settle calls = %d, want 1", ownership.callCount())
	}
	close(release)

	deadline := time.Now().Add(5 * time.Second)
	for {
		joined, err := guard.Handle(ctx, request(ModePrepare, "op-timeout"))
		if err != nil {
			t.Fatalf("join prepare: %v", err)
		}
		if joined.State == StatePrepared {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cleanup did not converge, last result = %+v", joined)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ownership.callCount() != 1 {
		t.Fatalf("late join re-ran cleanup: %d", ownership.callCount())
	}
}

// TestCheckStorageFailureDoesNotReportIdle 证明存储不可用 fail-closed：
// 不能把读取失败当成零活跃或伪造 idle/prepared。
func TestCheckStorageFailureDoesNotReportIdle(t *testing.T) {
	guard, st := newGuardEnv(t, &fakeOwnership{}, time.Second)
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	ctx := context.Background()
	if result, err := guard.Handle(ctx, request(ModeCheck, "op-storage")); err == nil {
		t.Fatalf("check with closed store = %+v, want error", result)
	}
	if result, err := guard.Handle(ctx, request(ModePrepare, "op-storage")); err == nil {
		t.Fatalf("prepare with closed store = %+v, want error", result)
	}
}

// TestFailedPrepareLeavesBusinessFactsUnchanged 证明阻断的 prepare 无业务行/
// 事件/回执副作用。
func TestFailedPrepareLeavesBusinessFactsUnchanged(t *testing.T) {
	ownership := &fakeOwnership{}
	guard, st := newGuardEnv(t, ownership, time.Second)
	seedProject(t, st)
	seedTask(t, st, "task-active", domain.TaskStatusPaused)
	ctx := context.Background()

	var (
		beforeVersion int64
		beforeEvents  int64
		beforeReceipt int
	)
	if err := st.ReadTx(ctx, func(tx *store.Tx) error {
		task, err := tx.GetTask(ctx, "task-active")
		if err != nil {
			return err
		}
		beforeVersion = task.StateVersion
		beforeEvents, err = tx.LatestEventSeq(ctx)
		return err
	}); err != nil {
		t.Fatalf("snapshot before: %v", err)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM receipts`).Scan(&beforeReceipt); err != nil {
		t.Fatalf("count receipts before: %v", err)
	}

	result, err := guard.Handle(ctx, request(ModePrepare, "op-blocked"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if result.State != StateBlocked || result.Reason != ReasonActiveWork {
		t.Fatalf("blocked prepare = %+v", result)
	}

	if err := st.ReadTx(ctx, func(tx *store.Tx) error {
		task, err := tx.GetTask(ctx, "task-active")
		if err != nil {
			return err
		}
		if task.Status != domain.TaskStatusPaused || task.StateVersion != beforeVersion {
			t.Fatalf("task changed: status=%s version=%d, want PAUSED/%d", task.Status, task.StateVersion, beforeVersion)
		}
		afterEvents, err := tx.LatestEventSeq(ctx)
		if err != nil {
			return err
		}
		if afterEvents != beforeEvents {
			t.Fatalf("event seq changed: %d -> %d", beforeEvents, afterEvents)
		}
		return nil
	}); err != nil {
		t.Fatalf("snapshot after: %v", err)
	}
	var afterReceipt int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM receipts`).Scan(&afterReceipt); err != nil {
		t.Fatalf("count receipts after: %v", err)
	}
	if afterReceipt != beforeReceipt {
		t.Fatalf("receipt count changed: %d -> %d", beforeReceipt, afterReceipt)
	}
}
