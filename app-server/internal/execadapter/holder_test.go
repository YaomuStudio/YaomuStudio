package execadapter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// TestRoutingHolderRequiresPublishedExecutor 覆盖 starting 门禁：未发布前不
// 转交执行、不伪装隔离；unavailable 仍是可恢复等待而非永久隔离。
func TestRoutingHolderRequiresPublishedExecutor(t *testing.T) {
	holder := NewRoutingHolder()
	if holder.Readiness() != ExecutorReadinessStarting {
		t.Fatalf("readiness = %s, want starting", holder.Readiness())
	}
	if holder.Capacity() != 1 {
		t.Fatalf("capacity = %d, want 1", holder.Capacity())
	}
	if holder.IsolationErr() != nil {
		t.Fatal("starting holder reported permanent isolation")
	}
	if holder.Identity() != "" {
		t.Fatalf("starting holder has identity %q", holder.Identity())
	}
	result := holder.Execute(context.Background(), Request{}, nil, nil)
	if result.Error == nil || result.Error.Code != "CAPABILITY_UNVERIFIED" {
		t.Fatalf("execute before publish = %+v, want fail-closed capability error", result)
	}

	holder.MarkUnavailable()
	if holder.Readiness() != ExecutorReadinessUnavailable {
		t.Fatalf("readiness = %s, want unavailable", holder.Readiness())
	}
	if holder.IsolationErr() != nil {
		t.Fatal("unavailable holder was treated as permanent isolation")
	}
}

// TestRoutingHolderPublishesOnceAndCleansOnce 覆盖一次发布与关闭所有权：
// 首个完整执行器生效，后续发布被拒绝；关闭只清理一次且关闭后拒绝执行。
func TestRoutingHolderPublishesOnceAndCleansOnce(t *testing.T) {
	holder := NewRoutingHolder()
	var cleanupCalls atomic.Int32
	cleanup := func() error {
		cleanupCalls.Add(1)
		return nil
	}
	routing := &RoutingExecutor{ModelRequired: true}
	if !holder.Publish(routing, cleanup) {
		t.Fatal("first publish was rejected")
	}
	if holder.Readiness() != ExecutorReadinessReady {
		t.Fatalf("readiness = %s, want ready", holder.Readiness())
	}
	// 委托已发布路由：ModelRequired 且无 Model 时应返回既有可见不可用错误，
	// 而不是持有者的“尚未就绪”错误。
	result := holder.Execute(context.Background(), Request{Adapter: ComponentRef{ID: "flashcli"}}, nil, nil)
	if result.Error == nil || result.Error.Code != "MODEL_RUNTIME_UNAVAILABLE" {
		t.Fatalf("published execute = %+v, want MODEL_RUNTIME_UNAVAILABLE", result)
	}

	var rejectedCleanup atomic.Int32
	rejected := &RoutingExecutor{}
	if holder.Publish(rejected, func() error {
		rejectedCleanup.Add(1)
		return nil
	}) {
		t.Fatal("second publish replaced the immutable executor")
	}
	if rejectedCleanup.Load() != 0 {
		t.Fatal("holder took ownership of a rejected publish")
	}

	if err := holder.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if holder.Readiness() != ExecutorReadinessClosed {
		t.Fatalf("readiness = %s, want closed", holder.Readiness())
	}
	if cleanupCalls.Load() != 1 {
		t.Fatalf("cleanup calls = %d, want 1", cleanupCalls.Load())
	}
	if err := holder.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if cleanupCalls.Load() != 1 {
		t.Fatalf("second close re-ran cleanup: %d", cleanupCalls.Load())
	}
	result = holder.Execute(context.Background(), Request{}, nil, nil)
	if result.Error == nil {
		t.Fatal("closed holder delegated execution")
	}
}

// TestRoutingHolderCloseRejectsLatePublish 覆盖关闭后的迟到发布：持有者不接管
// 资源，由初始化拥有者自行回收。
func TestRoutingHolderCloseRejectsLatePublish(t *testing.T) {
	holder := NewRoutingHolder()
	if err := holder.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	var cleanupCalls atomic.Int32
	if holder.Publish(&RoutingExecutor{}, func() error {
		cleanupCalls.Add(1)
		return nil
	}) {
		t.Fatal("closed holder accepted a late publish")
	}
	if cleanupCalls.Load() != 0 {
		t.Fatal("closed holder took ownership of a late publish")
	}
	if holder.Readiness() != ExecutorReadinessClosed {
		t.Fatalf("readiness = %s, want closed", holder.Readiness())
	}
}

// TestRoutingHolderDelegatesPermanentIsolation 覆盖隔离透传：已发布执行器的
// 永久隔离必须继续 fail-closed，不能被持有者的就绪状态掩盖。
func TestRoutingHolderDelegatesPermanentIsolation(t *testing.T) {
	holder := NewRoutingHolder()
	exec := &Executor{}
	isolated := errors.New("execution tree stop unconfirmed")
	exec.isolate(isolated)
	if !holder.Publish(&RoutingExecutor{Host: exec}, nil) {
		t.Fatal("publish rejected")
	}
	if err := holder.IsolationErr(); !errors.Is(err, isolated) {
		t.Fatalf("isolation = %v, want delegated error", err)
	}
}

// TestRoutingHolderPublishCloseRaceCleansOnce 并发发布与关闭：只有一个清理
// 所有者，迟到发布由初始化方回收，不泄漏也不双重取消。
func TestRoutingHolderPublishCloseRaceCleansOnce(t *testing.T) {
	for i := 0; i < 80; i++ {
		holder := NewRoutingHolder()
		var cleanupCalls atomic.Int32
		cleanup := func() error {
			cleanupCalls.Add(1)
			return nil
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if !holder.Publish(&RoutingExecutor{}, cleanup) {
				// 迟到发布：与 BootstrapRunners 相同，初始化拥有者回收。
				_ = cleanup()
			}
		}()
		go func() {
			defer wg.Done()
			_ = holder.Close()
		}()
		wg.Wait()
		_ = holder.Close()
		if cleanupCalls.Load() != 1 {
			t.Fatalf("iteration %d cleanup calls = %d, want 1", i, cleanupCalls.Load())
		}
		if holder.Readiness() != ExecutorReadinessClosed {
			t.Fatalf("iteration %d readiness = %s, want closed", i, holder.Readiness())
		}
	}
}
