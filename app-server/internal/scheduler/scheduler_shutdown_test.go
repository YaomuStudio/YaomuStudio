package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
)

// slowSettleExecutor 在取消后仍不退出，用于模拟关闭预算被慢取消耗尽。
type slowSettleExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (e *slowSettleExecutor) Execute(_ context.Context, req execadapter.Request, _ execadapter.ProgressFunc, _ execadapter.LogFunc) execadapter.Result {
	select {
	case <-e.started:
	default:
		close(e.started)
	}
	<-e.release
	return execadapter.Result{
		ArtifactIDs: []string{"artifact-slow"},
		Binding:     requestBinding(req),
	}
}

// TestCloseDeadlineLeavesActiveExecutionRunning 证明 sched.Close 在共享关闭预算
// 耗尽时会返回而活动执行仍在运行（槽位仍占用）。这是 main 关闭顺序修正覆盖的
// 分层边界前置条件（假慢执行器），不是真实 Core/模型慢取消复现，也不是普通任务
// 路径；断言失败时 deferred release 保证慢执行可收敛，newEnv 清理不会被拖到超时。
func TestCloseDeadlineLeavesActiveExecutionRunning(t *testing.T) {
	slow := &slowSettleExecutor{started: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	releaseSlow := func() { releaseOnce.Do(func() { close(slow.release) }) }
	// 任何断言失败都必须放行慢执行，否则它的 goroutine 会一直占着调度 wg。
	defer releaseSlow()
	e := newEnv(t, envOptions{executor: slow})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()
	select {
	case <-slow.started:
	case <-time.After(5 * time.Second):
		t.Fatal("execution did not start")
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	began := time.Now()
	e.sched.Close(closeCtx)
	if elapsed := time.Since(began); elapsed > 2*time.Second {
		t.Fatalf("close waited %s despite exhausted budget", elapsed)
	}
	if e.sched.RunningCount() != 1 {
		t.Fatalf("running executions = %d, want 1 held past deadline", e.sched.RunningCount())
	}
	if len(e.sched.slots) != 1 {
		t.Fatalf("held slots = %d, want 1", len(e.sched.slots))
	}

	// 释放慢执行，让后续终态提交与测试清理收敛。
	releaseSlow()
	done := e.waitTask(task.ID, domain.TaskStatusSucceeded, 5*time.Second)
	if done.Error != nil {
		t.Fatalf("slow execution did not settle: %+v", done.Error)
	}
}
