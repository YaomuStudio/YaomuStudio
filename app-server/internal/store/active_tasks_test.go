package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// TestCountActiveTasksExcludesRetryableHistory 覆盖 P2-05 的状态口径：
// 只有仍会调度/执行/恢复的任务阻塞清理；历史 FAILED/INTERRUPTED 不阻塞。
func TestCountActiveTasksExcludesRetryableHistory(t *testing.T) {
	s := newTestStore(t)
	project := seedProject(t, s, "proj-active")
	other := seedProject(t, s, "proj-other")
	ctx := context.Background()

	statuses := []domain.TaskStatus{
		domain.TaskStatusQueued,
		domain.TaskStatusWaitingResource,
		domain.TaskStatusRunning,
		domain.TaskStatusPauseRequested,
		domain.TaskStatusPaused,
		domain.TaskStatusCancelRequested,
		domain.TaskStatusSucceeded,
		domain.TaskStatusFailed,
		domain.TaskStatusCanceled,
		domain.TaskStatusInterrupted,
	}
	for i, status := range statuses {
		task := domain.Task{
			ID:        domain.ID(fmt.Sprintf("task-active-%02d", i)),
			ProjectID: project.ID,
			Kind:      domain.TaskKindNode,
			Status:    status,
			CreatedAt: testTime,
			UpdatedAt: testTime,
		}
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask(%s): %v", status, err)
		}
	}
	if err := s.CreateTask(ctx, domain.Task{
		ID:        "task-other-queued",
		ProjectID: other.ID,
		Kind:      domain.TaskKindNode,
		Status:    domain.TaskStatusQueued,
		CreatedAt: testTime,
		UpdatedAt: testTime,
	}); err != nil {
		t.Fatalf("CreateTask(other): %v", err)
	}

	projectActive, err := s.CountActiveTasks(ctx, project.ID)
	if err != nil {
		t.Fatalf("CountActiveTasks(project): %v", err)
	}
	if projectActive != 6 {
		t.Fatalf("project active tasks = %d, want 6", projectActive)
	}
	globalActive, err := s.CountActiveTasks(ctx, "")
	if err != nil {
		t.Fatalf("CountActiveTasks(global): %v", err)
	}
	if globalActive != 7 {
		t.Fatalf("global active tasks = %d, want 7", globalActive)
	}
}
