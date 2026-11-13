package scheduler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// TestStartPropagatesRecoveryFailure 覆盖 Core 启动前任务恢复失败：Start 必须
// 把存储错误返回给启动拥有者，且不启动调度循环；数据库事实不被改写。
func TestStartPropagatesRecoveryFailure(t *testing.T) {
	e := newEnv(t, envOptions{executor: &fakeExecutor{}})
	ctx := context.Background()
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)
	before, err := e.st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}

	// 关闭底层连接模拟恢复事务不可用；Start 不得把此失败降级为日志。
	if err := e.st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := e.sched.Start(ctx); err == nil {
		t.Fatal("Start accepted a failed task recovery")
	}
	if e.sched.ctx == nil || e.sched.ctx.Err() == nil {
		t.Fatal("failed start left a live scheduler context")
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	e.sched.Close(closeCtx)
	if e.sched.RunningCount() != 0 {
		t.Fatalf("failed start left running attempts: %d", e.sched.RunningCount())
	}

	// 恢复事务失败必须保持原事实：重新打开同一数据库核对。
	reopened, err := store.Open(ctx, filepath.Join(e.data, "yaomu.db"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	after, err := reopened.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask after failed recovery: %v", err)
	}
	if after.Status != before.Status || after.StateVersion != before.StateVersion || after.Error != nil {
		t.Fatalf("failed recovery changed task facts: before=%s/%d/%+v after=%s/%d/%+v",
			before.Status, before.StateVersion, before.Error,
			after.Status, after.StateVersion, after.Error)
	}
}
