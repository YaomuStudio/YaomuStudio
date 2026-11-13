package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

func runnerUnavailableInfo() *domain.ErrorInfo {
	return &domain.ErrorInfo{
		Code:       domain.ErrorCodeCapabilityUnverified,
		MessageKey: "task.runner_unavailable",
		Retryable:  true,
		ActionIDs:  []domain.ID{},
	}
}

// updateTaskErrorForTest 通过既有 CAS 更新写入等待原因（测试夹具）。
func updateTaskErrorForTest(t *testing.T, e *env, id domain.ID, status domain.TaskStatus, info *domain.ErrorInfo) domain.Task {
	t.Helper()
	ctx := context.Background()
	current, err := e.st.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	updated, err := e.st.UpdateTaskResult(ctx, id, status, current.ArtifactIDs, info,
		current.StateVersion, time.Now().UTC())
	if err != nil {
		t.Fatalf("UpdateTaskResult: %v", err)
	}
	return updated
}

// seedPausedRunWithExport 创建带源媒体、已放行交付的 Run、一个模型任务和一条导出。
func seedPausedRunWithExport(t *testing.T, e *env) (domain.WorkflowRun, domain.Task, domain.ExportRecord) {
	t.Helper()
	projectID := e.seedProject()
	asset := e.seedMediaAsset(projectID)
	run := e.seedRunWithInputs(projectID, asset.ID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)
	track := e.seedTrack(projectID, "zh", []domain.Segment{{ID: "seg", StartMs: 0, EndMs: 500, Text: "hello"}})
	e.seedApprovedDelivery(run.ID, track)
	record := e.createExport(projectID, run.ID, t.TempDir())
	return run, task, record
}

// TestImmediatePauseClearsWaitingReasonAndKeepsExport 覆盖请求暂停时的服务内收敛：
// WAITING_RESOURCE 任务挂起到 PAUSED 且不保留等待原因；导出保持交付例外。
func TestImmediatePauseClearsWaitingReasonAndKeepsExport(t *testing.T) {
	e := newEnv(t, envOptions{executor: nil})
	ctx := context.Background()
	run, task, record := seedPausedRunWithExport(t, e)
	updateTaskErrorForTest(t, e, task.ID, domain.TaskStatusWaitingResource, runnerUnavailableInfo())

	if _, err := e.svc.PauseWorkflowRun(ctx, service.Idem{}, run.ID); err != nil {
		t.Fatalf("PauseWorkflowRun: %v", err)
	}
	currentRun, err := e.st.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if currentRun.Status != domain.RunStatusPaused {
		t.Fatalf("run status = %s, want PAUSED", currentRun.Status)
	}
	pausedTask, err := e.st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if pausedTask.Status != domain.TaskStatusPaused {
		t.Fatalf("waiting task status = %s, want PAUSED", pausedTask.Status)
	}
	if pausedTask.Error != nil {
		t.Fatalf("paused task kept waiting reason: %+v", pausedTask.Error)
	}
	exportTask, err := e.st.GetTask(ctx, record.TaskID)
	if err != nil {
		t.Fatalf("GetTask export: %v", err)
	}
	if exportTask.Status == domain.TaskStatusPaused {
		t.Fatal("export task was parked by Run pause; delivery exception lost")
	}
	if exportTask.Status != domain.TaskStatusQueued {
		t.Fatalf("export status = %s, want QUEUED delivery exception", exportTask.Status)
	}
}

// TestSchedulerPauseSettlesWaitingResourceAndClearsReason 覆盖调度轮询收敛：
// 服务内因执行中任务未立即收敛时，Run 在安全边界由调度器收敛；此时
// WAITING_RESOURCE 模型任务必须挂起并清因，导出仍不随 Run 暂停。
func TestSchedulerPauseSettlesWaitingResourceAndClearsReason(t *testing.T) {
	capabilities := &mutableCapabilities{}
	executor := &fakeExecutor{gate: make(chan struct{})}
	e := newEnv(t, envOptions{executor: executor, capabilities: capabilities, slots: 2})
	ctx := context.Background()

	projectID := e.seedProject()
	source := e.seedMediaAsset(projectID)
	run := e.seedRunWithInputs(projectID, source.ID)
	runningTask := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)
	waitingTask := e.seedTask(projectID, run.ID, domain.NodeKeySeparate)
	track := e.seedTrack(projectID, "zh", []domain.Segment{{ID: "seg", StartMs: 0, EndMs: 500, Text: "hello"}})
	e.seedApprovedDelivery(run.ID, track)

	// 创建完成后让分离能力缺失：ASR 任务继续运行，Separation 任务等待。
	capabilities.set(domain.CapabilitySlotSeparation, domain.SlotStateNeedsSetup)

	startScheduler(t, e.sched, ctx)
	e.sched.Notify()

	// ASR 就绪、Separation 未就绪：一个任务运行中，另一个进入 WAITING_RESOURCE。
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, err := e.st.GetTask(ctx, runningTask.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		other, err := e.st.GetTask(ctx, waitingTask.ID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if current.Status == domain.TaskStatusRunning &&
			other.Status == domain.TaskStatusWaitingResource {
			if other.Error == nil ||
				other.Error.Code != domain.ErrorCodeModelNotInstalled ||
				other.Error.MessageKey != "task.capability_not_ready" {
				t.Fatalf("waiting reason = %+v, want MODEL_NOT_INSTALLED/task.capability_not_ready", other.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("setup statuses: running=%s waiting=%s", current.Status, other.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 先让 ASR/Separation 完成派发观察，再排入导出：同刻创建的任务在
	// Windows 上由随机 id 排序，导出若参与首批派发会占掉槽位。
	record := e.createExport(projectID, run.ID, t.TempDir())

	// 服务内暂停因有执行中任务不会立即收敛；安全边界由调度轮询完成。
	if _, err := e.svc.PauseWorkflowRun(ctx, service.Idem{}, run.ID); err != nil {
		t.Fatalf("PauseWorkflowRun: %v", err)
	}
	close(executor.gate)
	e.waitTask(runningTask.ID, domain.TaskStatusSucceeded, 5*time.Second)

	deadline = time.Now().Add(3 * time.Second)
	for {
		currentRun, err := e.st.GetWorkflowRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetWorkflowRun: %v", err)
		}
		if currentRun.Status == domain.RunStatusPaused {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run status = %s, want PAUSED", currentRun.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	pausedTask, err := e.st.GetTask(ctx, waitingTask.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if pausedTask.Status != domain.TaskStatusPaused {
		t.Fatalf("waiting task status = %s, want PAUSED", pausedTask.Status)
	}
	if pausedTask.Error != nil {
		t.Fatalf("paused task kept waiting reason: %+v", pausedTask.Error)
	}
	exportTask, err := e.st.GetTask(ctx, record.TaskID)
	if err != nil {
		t.Fatalf("GetTask export: %v", err)
	}
	if exportTask.Status == domain.TaskStatusPaused {
		t.Fatal("export task was parked by Run pause; delivery exception lost")
	}
}

// TestResumeRequeuesPausedTaskWithoutStaleReason 覆盖恢复：PAUSED 任务重新排队时
// 清除遗留等待原因，不把旧阻断带入 QUEUED 快照。
func TestResumeRequeuesPausedTaskWithoutStaleReason(t *testing.T) {
	e := newEnv(t, envOptions{executor: nil})
	ctx := context.Background()
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	if _, err := e.svc.PauseWorkflowRun(ctx, service.Idem{}, run.ID); err != nil {
		t.Fatalf("PauseWorkflowRun: %v", err)
	}
	paused, err := e.st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if paused.Status != domain.TaskStatusPaused {
		t.Fatalf("task status = %s, want PAUSED", paused.Status)
	}
	// 模拟旧版本/旧路径留下的 PAUSED + 等待原因。
	updateTaskErrorForTest(t, e, task.ID, domain.TaskStatusPaused, runnerUnavailableInfo())

	if _, err := e.svc.ResumeWorkflowRun(ctx, service.Idem{}, run.ID); err != nil {
		t.Fatalf("ResumeWorkflowRun: %v", err)
	}
	requeued, err := e.st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if requeued.Status != domain.TaskStatusQueued {
		t.Fatalf("task status = %s, want QUEUED", requeued.Status)
	}
	if requeued.Error != nil {
		t.Fatalf("resumed task kept stale waiting reason: %+v", requeued.Error)
	}
}
