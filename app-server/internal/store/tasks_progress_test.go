package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

const progressTestEpoch int64 = 7

// seedRunningTaskForProgress 建立 QUEUED→RUNNING 的任务与一个 RUNNING 尝试。
func seedRunningTaskForProgress(t *testing.T, s *Store) (domain.Task, domain.Attempt) {
	t.Helper()
	ctx := context.Background()
	project := seedProject(t, s, "project-progress")
	task := domain.Task{
		ID:          "task-progress",
		ProjectID:   project.ID,
		Kind:        domain.TaskKindNode,
		NodeKey:     domain.NodeKeyAnalyze,
		Status:      domain.TaskStatusQueued,
		ArtifactIDs: []domain.ID{},
		Steps: []domain.TaskStep{{
			StepID: "main", Status: domain.StepStatusPending,
			AttemptNo: 1, Epoch: progressTestEpoch,
		}},
		CreatedAt: testTime,
		UpdatedAt: testTime,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	attempt := domain.Attempt{
		ID: "attempt-1", TaskID: task.ID, StepID: "main",
		AttemptNo: 1, Epoch: progressTestEpoch,
		Status: domain.StepStatusRunning, StartedAt: &testTime,
	}
	if err := s.CreateAttempt(ctx, attempt); err != nil {
		t.Fatalf("CreateAttempt() error = %v", err)
	}
	queued, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	running, err := s.UpdateTaskResult(ctx, task.ID, domain.TaskStatusRunning,
		queued.ArtifactIDs, nil, queued.StateVersion, testTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("UpdateTaskResult(RUNNING) error = %v", err)
	}
	return running, attempt
}

func progressUpdate(task domain.Task, attempt domain.Attempt, value int) ProgressUpdate {
	return ProgressUpdate{
		TaskID: task.ID, AttemptID: attempt.ID, StepID: attempt.StepID,
		AttemptNo: attempt.AttemptNo, Epoch: attempt.Epoch,
		Progress: value, Now: testTime.Add(2 * time.Minute),
	}
}

func mustGetTask(t *testing.T, s *Store, id domain.ID) domain.Task {
	t.Helper()
	task, err := s.GetTask(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTask(%s) error = %v", id, err)
	}
	return task
}

// TestUpdateTaskProgressConditionalSnapshot 守护单事务条件更新：
// 只接受最新 RUNNING 尝试的严格递增进度，stale/no-op 不是错误，
// 且更新不递增状态版本。
func TestUpdateTaskProgressConditionalSnapshot(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	task, attempt := seedRunningTaskForProgress(t, s)

	result, err := s.UpdateTaskProgress(ctx, progressUpdate(task, attempt, 10))
	if err != nil {
		t.Fatalf("UpdateTaskProgress(10) error = %v", err)
	}
	if !result.Applied || result.StateVersion != task.StateVersion {
		t.Fatalf("first update = %+v, want applied at version %d", result, task.StateVersion)
	}
	got := mustGetTask(t, s, task.ID)
	if got.Progress != 10 {
		t.Fatalf("progress = %d, want 10", got.Progress)
	}
	if got.StateVersion != task.StateVersion {
		t.Fatalf("state version = %d, want %d (progress must not bump version)", got.StateVersion, task.StateVersion)
	}

	// 重复与倒退都是 no-op：不写快照、不报错。
	for _, value := range []int{10, 5} {
		result, err = s.UpdateTaskProgress(ctx, progressUpdate(task, attempt, value))
		if err != nil {
			t.Fatalf("UpdateTaskProgress(%d) error = %v", value, err)
		}
		if result.Applied {
			t.Fatalf("UpdateTaskProgress(%d) applied, want no-op", value)
		}
	}
	if got := mustGetTask(t, s, task.ID); got.Progress != 10 {
		t.Fatalf("progress after no-ops = %d, want 10", got.Progress)
	}

	result, err = s.UpdateTaskProgress(ctx, progressUpdate(task, attempt, 20))
	if err != nil || !result.Applied {
		t.Fatalf("UpdateTaskProgress(20) = %+v, %v", result, err)
	}
	if got := mustGetTask(t, s, task.ID); got.Progress != 20 {
		t.Fatalf("progress = %d, want 20", got.Progress)
	}

	// 绑定四元组任一不匹配都不能命中。
	mismatches := []ProgressUpdate{
		func() ProgressUpdate { u := progressUpdate(task, attempt, 30); u.AttemptID = "attempt-other"; return u }(),
		func() ProgressUpdate { u := progressUpdate(task, attempt, 30); u.StepID = "other-step"; return u }(),
		func() ProgressUpdate { u := progressUpdate(task, attempt, 30); u.AttemptNo = 2; return u }(),
		func() ProgressUpdate {
			u := progressUpdate(task, attempt, 30)
			u.Epoch = progressTestEpoch + 1
			return u
		}(),
	}
	for _, update := range mismatches {
		result, err = s.UpdateTaskProgress(ctx, update)
		if err != nil {
			t.Fatalf("mismatched update error = %v", err)
		}
		if result.Applied {
			t.Fatalf("mismatched update applied: %+v", update)
		}
	}
	if got := mustGetTask(t, s, task.ID); got.Progress != 20 {
		t.Fatalf("progress after mismatches = %d, want 20", got.Progress)
	}

	// 非法输入是调用错误，不是静默 no-op。
	if _, err := s.UpdateTaskProgress(ctx, progressUpdate(task, attempt, 101)); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Errorf("progress 101 error = %v, want ErrInvalidRequest", err)
	}
	incomplete := progressUpdate(task, attempt, 30)
	incomplete.AttemptID = ""
	if _, err := s.UpdateTaskProgress(ctx, incomplete); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Errorf("incomplete binding error = %v, want ErrInvalidRequest", err)
	}
}

// TestUpdateTaskProgressRejectsOldAttemptAndNonRunningTask 守护重试/取消竞态：
// 非最新尝试与离开 RUNNING 的任务不能被迟到的进度覆写。
func TestUpdateTaskProgressRejectsOldAttemptAndNonRunningTask(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	task, attempt := seedRunningTaskForProgress(t, s)

	if result, err := s.UpdateTaskProgress(ctx, progressUpdate(task, attempt, 15)); err != nil || !result.Applied {
		t.Fatalf("first update = %+v, %v", result, err)
	}

	// 新尝试成为最新 RUNNING：旧尝试的进度即使更大也必须被丢弃。
	newer := domain.Attempt{
		ID: "attempt-2", TaskID: task.ID, StepID: "main",
		AttemptNo: 2, Epoch: progressTestEpoch,
		Status: domain.StepStatusRunning, StartedAt: &testTime,
	}
	if err := s.CreateAttempt(ctx, newer); err != nil {
		t.Fatalf("CreateAttempt(2) error = %v", err)
	}
	result, err := s.UpdateTaskProgress(ctx, progressUpdate(task, attempt, 40))
	if err != nil {
		t.Fatalf("old attempt update error = %v", err)
	}
	if result.Applied {
		t.Fatal("old attempt progress applied after newer attempt")
	}
	if result, err = s.UpdateTaskProgress(ctx, progressUpdate(task, newer, 40)); err != nil || !result.Applied {
		t.Fatalf("new attempt update = %+v, %v", result, err)
	}

	// 取消请求使任务离开 RUNNING：后续进度不能改变快照。
	current := mustGetTask(t, s, task.ID)
	canceled, err := s.UpdateTaskStatus(ctx, task.ID, domain.TaskStatusCancelRequested, current.StateVersion, testTime.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("UpdateTaskStatus(CANCEL_REQUESTED) error = %v", err)
	}
	result, err = s.UpdateTaskProgress(ctx, progressUpdate(canceled, newer, 60))
	if err != nil {
		t.Fatalf("canceled task update error = %v", err)
	}
	if result.Applied {
		t.Fatal("progress applied after cancel request")
	}
	if got := mustGetTask(t, s, task.ID); got.Progress != 40 {
		t.Fatalf("progress after cancel = %d, want 40", got.Progress)
	}
}

// TestTaskProgressSnapshotFollowsStateTransitions 守护快照与既有状态事务的一致性：
// 认领/重试清 0、成功写 100、失败/取消保留最后合法值。
func TestTaskProgressSnapshotFollowsStateTransitions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	task, attempt := seedRunningTaskForProgress(t, s)
	if task.Progress != 0 {
		t.Fatalf("claim progress = %d, want 0", task.Progress)
	}

	if result, err := s.UpdateTaskProgress(ctx, progressUpdate(task, attempt, 42)); err != nil || !result.Applied {
		t.Fatalf("progress update = %+v, %v", result, err)
	}
	current := mustGetTask(t, s, task.ID)

	// 失败保留最后合法进度。
	failed, err := s.UpdateTaskResult(ctx, task.ID, domain.TaskStatusFailed,
		current.ArtifactIDs, nil, current.StateVersion, testTime.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("UpdateTaskResult(FAILED) error = %v", err)
	}
	if failed.Progress != 42 {
		t.Fatalf("failed progress = %d, want 42", failed.Progress)
	}
	if err := s.UpdateAttempt(ctx, attempt.ID, domain.StepStatusFailed, nil, nil); err != nil {
		t.Fatalf("UpdateAttempt(FAILED) error = %v", err)
	}

	// 显式重试清 0，重新认领仍为 0。
	retried, err := s.UpdateTaskResult(ctx, task.ID, domain.TaskStatusQueued,
		failed.ArtifactIDs, nil, failed.StateVersion, testTime.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("UpdateTaskResult(QUEUED) error = %v", err)
	}
	if retried.Progress != 0 {
		t.Fatalf("retry progress = %d, want 0", retried.Progress)
	}
	// 直接 Store 断言（fixture 驱动）：即使 QUEUED 快照被旧通用写入改成非零，
	// 认领事务仍会把快照清 0；真实 RetryTask→claim 链路在 scheduler 测试验证。
	if err := s.SetTaskProgress(ctx, task.ID, 35, testTime.Add(4*time.Minute+time.Second)); err != nil {
		t.Fatalf("SetTaskProgress(QUEUED fixture) error = %v", err)
	}
	claimed, err := s.UpdateTaskResult(ctx, task.ID, domain.TaskStatusRunning,
		retried.ArtifactIDs, nil, retried.StateVersion, testTime.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("UpdateTaskResult(RUNNING) error = %v", err)
	}
	if claimed.Progress != 0 {
		t.Fatalf("re-claim progress = %d, want 0", claimed.Progress)
	}

	second := domain.Attempt{
		ID: "attempt-2", TaskID: task.ID, StepID: "main",
		AttemptNo: 2, Epoch: progressTestEpoch,
		Status: domain.StepStatusRunning, StartedAt: &testTime,
	}
	if err := s.CreateAttempt(ctx, second); err != nil {
		t.Fatalf("CreateAttempt(2) error = %v", err)
	}
	if result, err := s.UpdateTaskProgress(ctx, progressUpdate(claimed, second, 77)); err != nil || !result.Applied {
		t.Fatalf("second attempt progress = %+v, %v", result, err)
	}

	succeeded, err := s.UpdateTaskResult(ctx, task.ID, domain.TaskStatusSucceeded,
		claimed.ArtifactIDs, nil, claimed.StateVersion, testTime.Add(6*time.Minute))
	if err != nil {
		t.Fatalf("UpdateTaskResult(SUCCEEDED) error = %v", err)
	}
	if succeeded.Progress != 100 {
		t.Fatalf("success progress = %d, want 100", succeeded.Progress)
	}
	// 终态后的进度不能回退成功快照。
	result, err := s.UpdateTaskProgress(ctx, progressUpdate(succeeded, second, 90))
	if err != nil {
		t.Fatalf("terminal progress update error = %v", err)
	}
	if result.Applied {
		t.Fatal("progress applied after success")
	}
	if got := mustGetTask(t, s, task.ID); got.Progress != 100 {
		t.Fatalf("terminal progress = %d, want 100", got.Progress)
	}
}
