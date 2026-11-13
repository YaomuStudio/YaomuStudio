package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// TestTaskSnapshotExposesWaitingReason 回归：执行器阻断原因写入既有 error 字段，
// GET /tasks/{id} 快照可直接读取，客户端不需要按超时猜测。
func TestTaskSnapshotExposesWaitingReason(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	projectID := env.seedProject("等待原因")
	created := env.expect(env.request(http.MethodPost, "/api/v1/tasks", map[string]any{
		"project_id": projectID,
		"kind":       "node",
		"node_key":   "02_analyze",
	}), http.StatusAccepted)
	task := decodeInto[domain.Task](t, created)
	current, err := env.store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	info := &domain.ErrorInfo{
		Code:       domain.ErrorCodeCapabilityUnverified,
		MessageKey: "task.runner_unavailable",
		Retryable:  true,
		ActionIDs:  []domain.ID{},
	}
	if _, err := env.store.UpdateTaskResult(ctx, current.ID, domain.TaskStatusWaitingResource,
		current.ArtifactIDs, info, current.StateVersion, time.Now().UTC()); err != nil {
		t.Fatalf("UpdateTaskResult: %v", err)
	}

	snapshot := decodeInto[domain.Task](t,
		env.expect(env.request(http.MethodGet, "/api/v1/tasks/"+string(task.ID), nil), http.StatusOK))
	if snapshot.Status != domain.TaskStatusWaitingResource {
		t.Fatalf("snapshot status = %s, want WAITING_RESOURCE", snapshot.Status)
	}
	if snapshot.Error == nil ||
		snapshot.Error.Code != domain.ErrorCodeCapabilityUnverified ||
		snapshot.Error.MessageKey != "task.runner_unavailable" {
		t.Fatalf("snapshot waiting reason = %+v", snapshot.Error)
	}
	if len(snapshot.Error.ActionIDs) != 0 {
		t.Fatalf("snapshot invents recovery actions: %v", snapshot.Error.ActionIDs)
	}
}
