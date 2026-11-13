package execadapter

// 更新准备要求“清理未确认不得被重复 Close 洗成成功”，并保留 bootstrap/
// 租约获取错误。这些测试用受控替身覆盖共享完成与锁存语义。

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRoutingHolderConcurrentCloseSharesFailure 证明第二个 Close 在首次清理
// 未完成/失败时不会返回 nil。
func TestRoutingHolderConcurrentCloseSharesFailure(t *testing.T) {
	holder := NewRoutingHolder()
	release := make(chan struct{})
	failure := errors.New("runner stop unconfirmed")
	var calls atomic.Int32
	if !holder.Publish(&RoutingExecutor{}, func() error {
		calls.Add(1)
		<-release
		return failure
	}) {
		t.Fatal("publish rejected")
	}

	first := make(chan error, 1)
	go func() { first <- holder.Close() }()
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	// 首次清理仍在进行：第二个 Close 必须等待同一完成，不能提前报成功。
	second := make(chan error, 1)
	go func() { second <- holder.Close() }()
	select {
	case err := <-second:
		t.Fatalf("second Close returned before first cleanup finished: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-first; !errors.Is(err, failure) {
		t.Fatalf("first Close = %v, want failure", err)
	}
	if err := <-second; !errors.Is(err, failure) {
		t.Fatalf("second Close = %v, want shared failure", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("cleanup calls = %d, want 1", calls.Load())
	}
}

// TestRoutingHolderRetainsBootstrapError 证明锁存错误不会被 unavailable/closed
// 读数或重复 Close 覆盖。
func TestRoutingHolderRetainsBootstrapError(t *testing.T) {
	holder := NewRoutingHolder()
	leaseErr := errors.New("workspace lease retained")
	holder.RecordBootstrapError(leaseErr)
	if !errors.Is(holder.BootstrapErr(), leaseErr) {
		t.Fatal("bootstrap error not retained")
	}
	holder.MarkUnavailable()
	if !errors.Is(holder.Err(), leaseErr) {
		t.Fatal("unavailable readiness hid the retained bootstrap error")
	}
	if err := holder.Close(); !errors.Is(err, leaseErr) {
		t.Fatalf("Close = %v, want retained bootstrap error", err)
	}
	if err := holder.Close(); !errors.Is(err, leaseErr) {
		t.Fatalf("second Close = %v, want retained bootstrap error", err)
	}
	// 迟到错误也保持锁存（不能因已关闭而丢失）。
	late := errors.New("late cleanup failure")
	holder.RecordBootstrapError(late)
	if !errors.Is(holder.Err(), late) || !errors.Is(holder.Err(), leaseErr) {
		t.Fatalf("late bootstrap error not retained: %v", holder.Err())
	}
}

// TestBootstrapRetainsLeaseAcquisitionFailure 覆盖未知租约拥有者：不能把
// unavailable 当成干净关闭。
func TestBootstrapRetainsLeaseAcquisitionFailure(t *testing.T) {
	holder := NewRoutingHolder()
	BootstrapRunners(context.Background(), context.Background(), RunnerBootstrapOptions{
		DataDir:     t.TempDir(),
		LeaseOwner:  LeaseOwner{WorkspaceID: "ws", InstanceID: "instance", Epoch: 1},
		HostCommand: "fake-runner",
		Holder:      holder,
		acquireLease: func(string, LeaseOwner) (runnerLease, error) {
			return nil, ErrWorkspaceLeased
		},
	})
	if holder.Readiness() != ExecutorReadinessUnavailable {
		t.Fatalf("readiness = %s, want unavailable", holder.Readiness())
	}
	if !errors.Is(holder.BootstrapErr(), ErrWorkspaceLeased) {
		t.Fatalf("bootstrap error = %v, want ErrWorkspaceLeased", holder.BootstrapErr())
	}
}

// TestBootstrapRetainsLeaseReleaseFailure 覆盖 hello 失败后的租约回收失败：
// 必须锁存，而不是只写日志。
func TestBootstrapRetainsLeaseReleaseFailure(t *testing.T) {
	holder := NewRoutingHolder()
	releaseErr := errors.New("lease release unconfirmed")
	lease := &fakeRunnerLease{releaseErr: releaseErr}
	var mu sync.Mutex
	BootstrapRunners(context.Background(), context.Background(), RunnerBootstrapOptions{
		DataDir:     t.TempDir(),
		LeaseOwner:  LeaseOwner{WorkspaceID: "ws", InstanceID: "instance", Epoch: 1},
		HostCommand: "fake-runner",
		Holder:      holder,
		acquireLease: func(string, LeaseOwner) (runnerLease, error) {
			mu.Lock()
			defer mu.Unlock()
			return lease, nil
		},
		startClient: func(context.Context, ProcessOptions) (*Client, error) {
			return NewClient(newBootstrapTransport(), nil), nil
		},
		hostHelloTimeout: 10 * time.Millisecond,
	})
	if !errors.Is(holder.BootstrapErr(), releaseErr) {
		t.Fatalf("bootstrap error = %v, want release failure", holder.BootstrapErr())
	}
	if err := holder.Close(); !errors.Is(err, releaseErr) {
		t.Fatalf("Close = %v, want retained release failure", err)
	}
}
