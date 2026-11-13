package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// TestCleanupStorageIgnoresRetryableHistory 覆盖 P2-05：
// 只有历史 FAILED/INTERRUPTED 时清理可执行；QUEUED 任务仍必须阻塞。
func TestCleanupStorageIgnoresRetryableHistory(t *testing.T) {
	for _, status := range []domain.TaskStatus{domain.TaskStatusFailed, domain.TaskStatusInterrupted} {
		t.Run(string(status), func(t *testing.T) {
			env := newExportTestEnv(t)
			project := env.seedProject("cleanup-history")
			seedTaskWithStatus(t, env, project.ID, status)

			if _, err := env.svc.CleanupStorage(context.Background(), Idem{},
				StorageCleanupRequest{Category: domain.StorageCategoryCache}); err != nil {
				t.Fatalf("CleanupStorage with %s history: %v", status, err)
			}
		})
	}

	t.Run("queued still blocks", func(t *testing.T) {
		env := newExportTestEnv(t)
		project := env.seedProject("cleanup-queued")
		seedTaskWithStatus(t, env, project.ID, domain.TaskStatusQueued)

		_, err := env.svc.CleanupStorage(context.Background(), Idem{},
			StorageCleanupRequest{Category: domain.StorageCategoryCache})
		assertResourceBusy(t, err)
	})
}

// TestDeleteProjectIgnoresRetryableHistory 覆盖 P2-05：
// 仅有失败历史时项目可删除；有排队任务时仍拒绝。
func TestDeleteProjectIgnoresRetryableHistory(t *testing.T) {
	t.Run("failed history", func(t *testing.T) {
		env := newExportTestEnv(t)
		project := env.seedProject("delete-history")
		seedTaskWithStatus(t, env, project.ID, domain.TaskStatusFailed)

		if _, err := env.svc.DeleteProject(context.Background(), Idem{}, string(project.ID)); err != nil {
			t.Fatalf("DeleteProject with failed history: %v", err)
		}
		if _, err := env.store.GetProject(context.Background(), project.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("project still exists after delete: %v", err)
		}
	})

	t.Run("queued still blocks", func(t *testing.T) {
		env := newExportTestEnv(t)
		project := env.seedProject("delete-queued")
		seedTaskWithStatus(t, env, project.ID, domain.TaskStatusQueued)

		_, err := env.svc.DeleteProject(context.Background(), Idem{}, string(project.ID))
		assertResourceBusy(t, err)
		if _, err := env.store.GetProject(context.Background(), project.ID); err != nil {
			t.Fatalf("project removed despite active task: %v", err)
		}
	})
}

// TestRetryFailedTaskRestoresCleanupGuard 验证重试进入 QUEUED 后重新阻塞清理。
func TestRetryFailedTaskRestoresCleanupGuard(t *testing.T) {
	env := newExportTestEnv(t)
	project := env.seedProject("retry-cleanup")
	task := seedTaskWithStatus(t, env, project.ID, domain.TaskStatusFailed)

	resp, err := env.svc.RetryTask(context.Background(), Idem{}, string(task.ID))
	if err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	retried := resp.Body.(domain.Task)
	if retried.Status != domain.TaskStatusQueued {
		t.Fatalf("retried task status = %s, want QUEUED", retried.Status)
	}
	_, err = env.svc.CleanupStorage(context.Background(), Idem{},
		StorageCleanupRequest{Category: domain.StorageCategoryCache})
	assertResourceBusy(t, err)
}

func seedTaskWithStatus(t *testing.T, env *exportTestEnv, projectID domain.ID, status domain.TaskStatus) domain.Task {
	t.Helper()
	now := time.Now().UTC()
	task := domain.Task{
		ID:        store.NewID("task"),
		ProjectID: projectID,
		Kind:      domain.TaskKindNode,
		Status:    status,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := env.store.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask(%s): %v", status, err)
	}
	built, err := env.store.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTask(%s): %v", status, err)
	}
	return built
}

func assertResourceBusy(t *testing.T, err error) {
	t.Helper()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.MessageKey != "error.resource_busy" {
		t.Fatalf("error = %v, want error.resource_busy", err)
	}
}
