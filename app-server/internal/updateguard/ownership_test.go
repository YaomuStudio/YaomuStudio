package updateguard

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
)

// TestCoreOwnershipExecutorBlockFailsClosed 锁定执行拥有者的保留不确定项。
func TestCoreOwnershipExecutorBlockFailsClosed(t *testing.T) {
	holder := execadapter.NewRoutingHolder()
	ownership := &CoreOwnership{Holder: holder}
	if block := ownership.ExecutorBlock(); block != ReasonExecutorStarting {
		t.Fatalf("starting holder block = %q, want executor_starting", block)
	}

	holder.MarkUnavailable()
	if block := ownership.ExecutorBlock(); block != "" {
		t.Fatalf("clean unavailable holder block = %q, want none", block)
	}

	holder.RecordBootstrapError(errors.New("retained lease"))
	if block := ownership.ExecutorBlock(); block != ReasonExecutorUnconfirmed {
		t.Fatalf("bootstrap error block = %q, want executor_unconfirmed", block)
	}
}

// TestCoreOwnershipSettleSharesHolderCompletionAndError 证明清理失败不会被
// 第二次调用洗成成功，且清理只执行一次。
func TestCoreOwnershipSettleSharesHolderCompletionAndError(t *testing.T) {
	holder := execadapter.NewRoutingHolder()
	failure := errors.New("runner stop unconfirmed")
	release := make(chan struct{})
	var calls atomic.Int32
	if !holder.Publish(&execadapter.RoutingExecutor{}, func() error {
		calls.Add(1)
		<-release
		return failure
	}) {
		t.Fatal("publish rejected")
	}
	ownership := &CoreOwnership{Holder: holder}

	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- ownership.Settle(context.Background())
		}()
	}
	// 让至少一个 Settle 进入 holder 清理；再放行，两个调用必须得到同一错误。
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if !errors.Is(err, failure) {
			t.Fatalf("settle error = %v, want shared failure", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("cleanup calls = %d, want 1", calls.Load())
	}
}

// TestCoreOwnershipSettleReportsUnsettledBootstrap 证明未收敛 bootstrap 不会
// 被当成干净关闭。
func TestCoreOwnershipSettleReportsUnsettledBootstrap(t *testing.T) {
	bootstrapDone := make(chan struct{})
	ownership := &CoreOwnership{
		Holder:        execadapter.NewRoutingHolder(),
		BootstrapDone: bootstrapDone,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := ownership.Settle(ctx); err == nil {
		t.Fatal("settle with unsettled bootstrap returned nil")
	}
	close(bootstrapDone)
}

// TestCoreOwnershipInMemoryActiveReadsNilSafely 是空组装的防御性回归。
func TestCoreOwnershipInMemoryActiveReadsNilSafely(t *testing.T) {
	var ownership *CoreOwnership
	if ownership.InMemoryActive() {
		t.Fatal("nil ownership reported active work")
	}
	if block := ownership.ExecutorBlock(); block != "" {
		t.Fatalf("nil ownership block = %q", block)
	}
	if err := ownership.Settle(context.Background()); err != nil {
		t.Fatalf("nil ownership settle: %v", err)
	}
}
