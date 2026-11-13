package scheduler

// 守护 markBlockedTasks 的最小优化：同原因等待在 Run 闸门之前跳过，且同一
// 事务内按 Run 缓存闸门结果。行为要求不变：
//   - 首次阻断仍为每个可派发任务写入 WAITING_RESOURCE 与事件；
//   - 稳态同原因不写状态、不追加事件、不递增状态版本；
//   - 不允许派发的 Run 中节点任务保持原状态；导出不随 Run 暂停；
//   - 不会因为缓存把暂停 Run 的节点任务放行。

import (
	"context"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func TestMarkBlockedTasksRunGateCacheAndSameReasonSkip(t *testing.T) {
	e := newEnv(t, envOptions{executor: nil})
	ctx := context.Background()
	projectID := e.seedProject()
	runningRun := e.seedRun(projectID)
	pausedRun := e.seedRun(projectID)

	// 把第二个 Run 置为 PAUSED（合法两步迁移）。
	current, err := e.st.GetWorkflowRun(ctx, pausedRun.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	requested, err := e.st.UpdateRunStatus(ctx, pausedRun.ID, domain.RunStatusPauseRequested,
		current.StateVersion, time.Now().UTC())
	if err != nil {
		t.Fatalf("pause request: %v", err)
	}
	if _, err := e.st.UpdateRunStatus(ctx, pausedRun.ID, domain.RunStatusPaused,
		requested.StateVersion, time.Now().UTC()); err != nil {
		t.Fatalf("pause run: %v", err)
	}

	base := time.Now().UTC()
	seed := func(id string, runID domain.ID, kind domain.TaskKind) domain.Task {
		t.Helper()
		task := domain.Task{
			ID:           domain.ID(id),
			ProjectID:    projectID,
			RunID:        runID,
			Kind:         kind,
			Status:       domain.TaskStatusQueued,
			StateVersion: 1,
			CreatedAt:    base,
			UpdatedAt:    base,
		}
		if kind == domain.TaskKindNode {
			task.NodeKey = domain.NodeKeyAnalyze
		}
		if err := e.st.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask %s: %v", id, err)
		}
		return task
	}

	first := seed("gate-first", runningRun.ID, domain.TaskKindNode)
	second := seed("gate-second", runningRun.ID, domain.TaskKindNode)
	pausedNode := seed("gate-paused-node", pausedRun.ID, domain.TaskKindNode)
	pausedExport := seed("gate-paused-export", pausedRun.ID, domain.TaskKindExport)

	block := runnerUnavailableBlock()
	e.sched.markBlockedTasks(ctx, block)

	waitingIDs := []domain.ID{first.ID, second.ID, pausedExport.ID}
	for _, id := range waitingIDs {
		current, err := e.st.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("GetTask %s: %v", id, err)
		}
		if current.Status != domain.TaskStatusWaitingResource {
			t.Fatalf("task %s status = %s, want WAITING_RESOURCE", id, current.Status)
		}
		assertRunnerReason(t, current, domain.ErrorCodeCapabilityUnverified, "task.runner_unavailable")
	}

	kept, err := e.st.GetTask(ctx, pausedNode.ID)
	if err != nil {
		t.Fatalf("GetTask paused node: %v", err)
	}
	if kept.Status != domain.TaskStatusQueued || kept.Error != nil {
		t.Fatalf("paused run node task changed: status=%s error=%+v", kept.Status, kept.Error)
	}

	events := waitingEvents(t, e)
	if got := countWaitingEventsFor(events, pausedNode.ID); got != 0 {
		t.Fatalf("paused run node task events = %d, want 0", got)
	}
	for _, id := range waitingIDs {
		if got := countWaitingEventsFor(events, id); got != 1 {
			t.Fatalf("task %s waiting events = %d, want 1", id, got)
		}
	}

	versions := make(map[domain.ID]int64)
	for _, id := range append(append([]domain.ID{}, waitingIDs...), pausedNode.ID) {
		task, err := e.st.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("GetTask version %s: %v", id, err)
		}
		versions[id] = task.StateVersion
	}
	seq, err := e.st.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("LatestEventSeq: %v", err)
	}

	// 稳态重复扫描：即使 Run 随后暂停，同原因任务也不得重写或补事件。
	currentRun, err := e.st.GetWorkflowRun(ctx, runningRun.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun running: %v", err)
	}
	requested, err = e.st.UpdateRunStatus(ctx, runningRun.ID, domain.RunStatusPauseRequested,
		currentRun.StateVersion, time.Now().UTC())
	if err != nil {
		t.Fatalf("pause running request: %v", err)
	}
	if _, err := e.st.UpdateRunStatus(ctx, runningRun.ID, domain.RunStatusPaused,
		requested.StateVersion, time.Now().UTC()); err != nil {
		t.Fatalf("pause running: %v", err)
	}
	e.sched.markBlockedTasks(ctx, block)

	for _, id := range waitingIDs {
		task, err := e.st.GetTask(ctx, id)
		if err != nil {
			t.Fatalf("GetTask after steady %s: %v", id, err)
		}
		if task.StateVersion != versions[id] {
			t.Fatalf("task %s state_version = %d, want %d", id, task.StateVersion, versions[id])
		}
		if task.Status != domain.TaskStatusWaitingResource {
			t.Fatalf("task %s status after steady = %s, want WAITING_RESOURCE", id, task.Status)
		}
	}
	pausedAfter, err := e.st.GetTask(ctx, pausedNode.ID)
	if err != nil {
		t.Fatalf("GetTask paused node after steady: %v", err)
	}
	if pausedAfter.StateVersion != versions[pausedNode.ID] ||
		pausedAfter.Status != domain.TaskStatusQueued || pausedAfter.Error != nil {
		t.Fatalf("paused run node changed after steady: status=%s version=%d error=%+v",
			pausedAfter.Status, pausedAfter.StateVersion, pausedAfter.Error)
	}
	after, err := e.st.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("LatestEventSeq after steady: %v", err)
	}
	if after != seq {
		t.Fatalf("steady scan appended events: seq %d -> %d", seq, after)
	}
}
