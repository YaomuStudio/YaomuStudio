package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// TestSealIfIdleWaitsForInFlightWriteAdmission 证明封闸不会越过已在飞的
// 写事务：封闸尝试快速失败（request_in_flight 语义），事务提交后再检查时
// 必须观察到该创建。
func TestSealIfIdleWaitsForInFlightWriteAdmission(t *testing.T) {
	s := newTestStore(t)
	seedProject(t, s, "project-seal-race")
	ctx := context.Background()

	entered := make(chan struct{})
	release := make(chan struct{})
	committed := make(chan error, 1)
	go func() {
		committed <- s.DoTx(ctx, func(tx *Tx) error {
			task := baseTask("task-seal-race", "project-seal-race", domain.TaskStatusQueued)
			if err := tx.CreateTask(ctx, task); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	sealed, err := s.SealIfIdle(ctx, func(tx *Tx) (bool, error) {
		work, err := tx.ActiveWork(ctx)
		if err != nil {
			return false, err
		}
		return work.Empty(), nil
	})
	if !errors.Is(err, ErrAdmissionBusy) {
		t.Fatalf("seal while write in flight = (%v, %v), want ErrAdmissionBusy", sealed, err)
	}
	if s.Sealed() {
		t.Fatal("busy seal attempt sealed the store")
	}

	close(release)
	if err := <-committed; err != nil {
		t.Fatalf("in-flight creation: %v", err)
	}

	// 创建已提交：同一封闸检查必须观察到活跃 Task，不能封闸。
	sealed, err = s.SealIfIdle(ctx, func(tx *Tx) (bool, error) {
		work, err := tx.ActiveWork(ctx)
		if err != nil {
			return false, err
		}
		return work.Empty(), nil
	})
	if err != nil {
		t.Fatalf("second seal attempt: %v", err)
	}
	if sealed {
		t.Fatal("sealed despite an active committed task")
	}
	if s.Sealed() {
		t.Fatal("store remained sealed after blocked check")
	}
	if err := s.DoTx(ctx, func(tx *Tx) error {
		if _, err := tx.GetTask(ctx, "task-seal-race"); err != nil {
			t.Fatalf("task missing after blocked check: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("admission was not reopened after blocked check: %v", err)
	}
}

// TestSealedStoreRejectsSubsequentWritesAndClaims 证明封闸生效后所有经 DoTx
// 的创建/认领/保留策略写入被拒绝，ReadTx 仍可用于控制面证明。
func TestSealedStoreRejectsSubsequentWritesAndClaims(t *testing.T) {
	s := newTestStore(t)
	seedProject(t, s, "project-sealed")
	ctx := context.Background()

	sealed, err := s.SealIfIdle(ctx, func(tx *Tx) (bool, error) { return true, nil })
	if err != nil || !sealed {
		t.Fatalf("seal idle store = (%v, %v), want sealed", sealed, err)
	}
	if !s.Sealed() {
		t.Fatal("sealed() = false after successful seal")
	}

	createErr := s.DoTx(ctx, func(tx *Tx) error {
		return tx.CreateTask(ctx, baseTask("task-after-seal", "project-sealed", domain.TaskStatusQueued))
	})
	if !errors.Is(createErr, ErrSealed) {
		t.Fatalf("DoTx after seal = %v, want ErrSealed", createErr)
	}
	if _, err := s.PruneEventsBeforeTime(ctx, time.Now().UTC()); !errors.Is(err, ErrSealed) {
		t.Fatalf("prune after seal = %v, want ErrSealed", err)
	}
	if err := s.SetAssetStatus(ctx, "missing-asset", domain.AssetStatusMissing); !errors.Is(err, ErrSealed) {
		t.Fatalf("asset write after seal = %v, want ErrSealed", err)
	}
	if sealed, err := s.SealIfIdle(ctx, func(tx *Tx) (bool, error) { return true, nil }); !errors.Is(err, ErrSealed) || sealed {
		t.Fatalf("second seal = (%v, %v), want ErrSealed", sealed, err)
	}

	var count int
	if err := s.ReadTx(ctx, func(tx *Tx) error {
		return tx.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&count)
	}); err != nil {
		t.Fatalf("ReadTx after seal: %v", err)
	}
	if count != 0 {
		t.Fatalf("tasks after rejected writes = %d, want 0", count)
	}
}

// TestSealCheckFailureDoesNotSealOrMutate 证明失败的空闲检查不产生业务行、
// 事件或回执副作用，也不封闸。
func TestSealCheckFailureDoesNotSealOrMutate(t *testing.T) {
	s := newTestStore(t)
	seedProject(t, s, "project-check-failure")
	ctx := context.Background()
	failure := errors.New("active work query failed")

	sealed, err := s.SealIfIdle(ctx, func(tx *Tx) (bool, error) {
		return false, failure
	})
	if !errors.Is(err, failure) || sealed {
		t.Fatalf("seal with failing check = (%v, %v), want failure", sealed, err)
	}
	if s.Sealed() {
		t.Fatal("failing check sealed the store")
	}
	var (
		tasks    int
		events   int
		receipts int
	)
	if err := s.ReadTx(ctx, func(tx *Tx) error {
		for query, target := range map[string]*int{
			`SELECT COUNT(*) FROM tasks`:    &tasks,
			`SELECT COUNT(*) FROM events`:   &events,
			`SELECT COUNT(*) FROM receipts`: &receipts,
		} {
			if err := tx.db.QueryRowContext(ctx, query).Scan(target); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("count after failed check: %v", err)
	}
	if tasks != 0 || events != 0 || receipts != 0 {
		t.Fatalf("side effects after failed check: tasks=%d events=%d receipts=%d", tasks, events, receipts)
	}
	if err := s.DoTx(ctx, func(tx *Tx) error {
		return tx.CreateTask(ctx, baseTask("task-after-failed-check", "project-check-failure", domain.TaskStatusQueued))
	}); err != nil {
		t.Fatalf("admission not reopened after failed check: %v", err)
	}
}

// TestActiveWorkExplicitSets 锁定显式活跃集：FAILED/INTERRUPTED 历史 Task 与
// 终态 Run/Installation 不算活跃，WAITING_RESOURCE/PAUSED 等必须阻断。
func TestActiveWorkExplicitSets(t *testing.T) {
	s := newTestStore(t)
	seedProject(t, s, "project-active")
	ctx := context.Background()

	work, err := s.ActiveWork(ctx)
	if err != nil {
		t.Fatalf("ActiveWork empty: %v", err)
	}
	if !work.Empty() {
		t.Fatalf("empty store work = %+v", work)
	}

	// FAILED/INTERRUPTED 是可重试历史，不阻断更新。
	if err := s.DoTx(ctx, func(tx *Tx) error {
		if err := tx.CreateTask(ctx, baseTask("task-failed", "project-active", domain.TaskStatusFailed)); err != nil {
			return err
		}
		return tx.CreateTask(ctx, baseTask("task-interrupted", "project-active", domain.TaskStatusInterrupted))
	}); err != nil {
		t.Fatalf("seed historical tasks: %v", err)
	}
	if work, err = s.ActiveWork(ctx); err != nil {
		t.Fatalf("ActiveWork historical: %v", err)
	}
	if !work.Empty() {
		t.Fatalf("historical tasks counted as active: %+v", work)
	}

	// 终态 Run / Installation 同样不阻断。
	if err := s.DoTx(ctx, func(tx *Tx) error {
		return tx.CreateWorkflowRun(ctx, activeTestRun("run-succeeded", "project-active", domain.RunStatusSucceeded))
	}); err != nil {
		t.Fatalf("seed terminal run: %v", err)
	}
	if err := s.DoTx(ctx, func(tx *Tx) error {
		return tx.CreateInstallation(ctx, activeTestInstallation("inst-done", domain.InstallationPhaseDone))
	}); err != nil {
		t.Fatalf("seed terminal installation: %v", err)
	}
	if work, err = s.ActiveWork(ctx); err != nil {
		t.Fatalf("ActiveWork terminal: %v", err)
	}
	if !work.Empty() {
		t.Fatalf("terminal rows counted as active: %+v", work)
	}

	// 显式活跃集逐项阻断。
	if err := s.DoTx(ctx, func(tx *Tx) error {
		return tx.CreateWorkflowRun(ctx, activeTestRun("run-waiting-review", "project-active", domain.RunStatusWaitingReview))
	}); err != nil {
		t.Fatalf("seed waiting-review run: %v", err)
	}
	if work, err = s.ActiveWork(ctx); err != nil || work.Runs != 1 {
		t.Fatalf("active run count = %+v, err=%v, want Runs=1", work, err)
	}

	for _, status := range []domain.TaskStatus{
		domain.TaskStatusQueued, domain.TaskStatusWaitingResource, domain.TaskStatusRunning,
		domain.TaskStatusPauseRequested, domain.TaskStatusPaused, domain.TaskStatusCancelRequested,
	} {
		t.Run(string(status), func(t *testing.T) {
			s := newTestStore(t)
			seedProject(t, s, "project-active-task")
			if err := s.DoTx(ctx, func(tx *Tx) error {
				return tx.CreateTask(ctx, baseTask("task-active", "project-active-task", status))
			}); err != nil {
				t.Fatalf("seed task: %v", err)
			}
			work, err := s.ActiveWork(ctx)
			if err != nil {
				t.Fatalf("ActiveWork: %v", err)
			}
			if work.Tasks != 1 {
				t.Fatalf("task status %s counted %d, want 1", status, work.Tasks)
			}
		})
	}

	for _, status := range []domain.StepStatus{
		domain.StepStatusPending, domain.StepStatusRunning, domain.StepStatusCommitting,
	} {
		t.Run("attempt-"+string(status), func(t *testing.T) {
			s := newTestStore(t)
			seedProject(t, s, "project-active-attempt")
			ctx := context.Background()
			if err := s.DoTx(ctx, func(tx *Tx) error {
				if err := tx.CreateTask(ctx, baseTask("task-attempt", "project-active-attempt", domain.TaskStatusSucceeded)); err != nil {
					return err
				}
				return tx.CreateAttempt(ctx, domain.Attempt{
					ID: "attempt-active", TaskID: "task-attempt", StepID: "main",
					AttemptNo: 1, Epoch: 1, Status: status,
				})
			}); err != nil {
				t.Fatalf("seed attempt: %v", err)
			}
			work, err := s.ActiveWork(ctx)
			if err != nil {
				t.Fatalf("ActiveWork: %v", err)
			}
			if work.Attempts != 1 {
				t.Fatalf("attempt status %s counted %d, want 1", status, work.Attempts)
			}
		})
	}

	for _, phase := range []domain.InstallationPhase{
		domain.InstallationPhasePlanning, domain.InstallationPhaseDownloading,
		domain.InstallationPhaseVerifying, domain.InstallationPhaseInstalling,
	} {
		t.Run("installation-"+string(phase), func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			if err := s.DoTx(ctx, func(tx *Tx) error {
				return tx.CreateInstallation(ctx, activeTestInstallation("inst-active", phase))
			}); err != nil {
				t.Fatalf("seed installation: %v", err)
			}
			work, err := s.ActiveWork(ctx)
			if err != nil {
				t.Fatalf("ActiveWork: %v", err)
			}
			if work.Installations != 1 {
				t.Fatalf("installation phase %s counted %d, want 1", phase, work.Installations)
			}
		})
	}
}

// TestPruneEventsBeforeTimeRequiresAdmission 证明事件保留写入不再绕过封闸。
func TestPruneEventsBeforeTimeRequiresAdmission(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	old := testTime.UTC()
	if err := s.DoTx(ctx, func(tx *Tx) error {
		_, err := tx.AppendEvent(ctx, domain.DomainEvent{Type: "test.event"}, old.AddDate(0, 0, -60))
		return err
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	if _, err := s.SealIfIdle(ctx, func(tx *Tx) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := s.PruneEventsBeforeTime(ctx, old); !errors.Is(err, ErrSealed) {
		t.Fatalf("prune after seal = %v, want ErrSealed", err)
	}
	var events int
	if err := s.ReadTx(ctx, func(tx *Tx) error {
		return tx.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&events)
	}); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 1 {
		t.Fatalf("events after sealed prune = %d, want 1", events)
	}
}

// TestPruneEventsBeforeTimePrunesWhenAdmitted 保留正常路径不变。
func TestPruneEventsBeforeTimePrunesWhenAdmitted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	old := testTime.UTC()
	if err := s.DoTx(ctx, func(tx *Tx) error {
		_, err := tx.AppendEvent(ctx, domain.DomainEvent{Type: "test.event"}, old.AddDate(0, 0, -60))
		return err
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	removed, err := s.PruneEventsBeforeTime(ctx, old)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("pruned %d events, want 1", removed)
	}
}

func baseTask(id, projectID string, status domain.TaskStatus) domain.Task {
	return domain.Task{
		ID:        domain.ID(id),
		ProjectID: domain.ID(projectID),
		Kind:      domain.TaskKindNode,
		NodeKey:   domain.NodeKeyAnalyze,
		Status:    status,
		CreatedAt: testTime,
		UpdatedAt: testTime,
	}
}

func activeTestRun(id, projectID string, status domain.RunStatus) domain.WorkflowRun {
	return domain.WorkflowRun{
		ID:             domain.ID(id),
		ProjectID:      domain.ID(projectID),
		Name:           "update guard run",
		Template:       "full_pipeline",
		Status:         status,
		SourceLanguage: "en",
		Languages:      []string{"zh"},
		Nodes:          fullNodes(),
		CreatedAt:      testTime,
		UpdatedAt:      testTime,
	}
}

func activeTestInstallation(id string, phase domain.InstallationPhase) domain.Installation {
	return domain.Installation{
		ID:        domain.ID(id),
		Slot:      domain.CapabilitySlotASR,
		Phase:     phase,
		CreatedAt: testTime,
		UpdatedAt: testTime,
	}
}

// TestWriteGateConcurrentAdmission 是闸门计数本身的回归：共享/独占互斥、
// 独占失败不残留状态。
func TestWriteGateConcurrentAdmission(t *testing.T) {
	gate := NewWriteGate()
	if err := gate.AcquireShared(); err != nil {
		t.Fatalf("shared acquire: %v", err)
	}
	if err := gate.TryExclusive(); !errors.Is(err, ErrAdmissionBusy) {
		t.Fatalf("exclusive with shared = %v, want busy", err)
	}
	gate.ReleaseShared()
	if err := gate.TryExclusive(); err != nil {
		t.Fatalf("exclusive after release: %v", err)
	}
	if err := gate.AcquireShared(); !errors.Is(err, ErrSealed) {
		t.Fatalf("shared during exclusive = %v, want ErrSealed", err)
	}
	gate.Seal()
	gate.ReleaseExclusive()
	if !gate.Sealed() {
		t.Fatal("seal marker lost after releasing exclusive admission")
	}
	if err := gate.AcquireShared(); !errors.Is(err, ErrSealed) {
		t.Fatalf("shared after seal = %v, want ErrSealed", err)
	}
}

// TestSealIfIdleConcurrentCreatesEitherObservedOrRejected 用并发创建压测
// “先提交必被观察、封闸后必被拒绝”，不出现同时成功与封闸的中间态。
func TestSealIfIdleConcurrentCreatesEitherObservedOrRejected(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		s := newTestStore(t)
		seedProject(t, s, "project-concurrent")
		ctx := context.Background()
		var (
			wg       sync.WaitGroup
			accepted []string
			mu       sync.Mutex
		)
		start := make(chan struct{})
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				<-start
				id := string(rune('a'+n)) + "-task"
				err := s.DoTx(ctx, func(tx *Tx) error {
					return tx.CreateTask(ctx, baseTask(id, "project-concurrent", domain.TaskStatusQueued))
				})
				if err == nil {
					mu.Lock()
					accepted = append(accepted, id)
					mu.Unlock()
				} else if !errors.Is(err, ErrSealed) {
					t.Errorf("create %s: %v", id, err)
				}
			}(i)
		}
		sealedCh := make(chan bool, 1)
		go func() {
			<-start
			sealed, err := s.SealIfIdle(ctx, func(tx *Tx) (bool, error) {
				work, err := tx.ActiveWork(ctx)
				if err != nil {
					return false, err
				}
				return work.Empty(), nil
			})
			if err != nil && !errors.Is(err, ErrAdmissionBusy) {
				t.Errorf("seal: %v", err)
			}
			sealedCh <- sealed && err == nil
		}()
		close(start)
		wg.Wait()
		sealed := <-sealedCh

		var persisted int
		if err := s.ReadTx(ctx, func(tx *Tx) error {
			return tx.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE status = 'QUEUED'`).Scan(&persisted)
		}); err != nil {
			t.Fatalf("count tasks: %v", err)
		}
		if len(accepted) != persisted {
			t.Fatalf("accepted=%d persisted=%d", len(accepted), persisted)
		}
		if sealed && persisted != 0 {
			t.Fatalf("sealed while %d accepted task(s) had committed", persisted)
		}
		if !sealed && len(accepted) == 0 {
			// 没有创建与封闸竞争时也允许本轮为空；下一次尝试继续。
			continue
		}
	}
}
