package execadapter

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
)

// bootstrapTransport 是可控的内存 Runner 传输：测试决定何时回复 hello，
// Close/Kill 计数用于确认执行树清理只发生一次。
type bootstrapTransport struct {
	out        chan Frame
	in         chan Frame
	closed     chan struct{}
	closeOnce  sync.Once
	closeCount atomic.Int32
}

func newBootstrapTransport() *bootstrapTransport {
	return &bootstrapTransport{
		out:    make(chan Frame, 8),
		in:     make(chan Frame, 8),
		closed: make(chan struct{}),
	}
}

func (t *bootstrapTransport) Send(frame Frame) error {
	select {
	case t.out <- frame:
		return nil
	case <-t.closed:
		return io.EOF
	}
}

func (t *bootstrapTransport) Receive() (Frame, error) {
	select {
	case frame, ok := <-t.in:
		if !ok {
			return Frame{}, io.EOF
		}
		return frame, nil
	case <-t.closed:
		return Frame{}, io.EOF
	}
}

func (t *bootstrapTransport) Kill() error { return t.Close() }

func (t *bootstrapTransport) Close() error {
	t.closeOnce.Do(func() {
		t.closeCount.Add(1)
		close(t.closed)
	})
	return nil
}

func replyHello(t *testing.T, transport *bootstrapTransport, req Frame) {
	t.Helper()
	transport.in <- Frame{V: ProtocolVersion, Op: OpResult, ReqID: req.ReqID, Payload: map[string]any{
		"request_op": "hello",
		"data": map[string]any{
			"runner_version":   "bootstrap-fake",
			"protocol_version": 1,
			"adapters":         []any{},
		},
	}}
}

// autoHello 立即应答一次 hello 后退出。
func autoHello(t *testing.T, transport *bootstrapTransport) {
	t.Helper()
	go func() {
		req := <-transport.out
		if req.Op != OpHello {
			return
		}
		replyHello(t, transport, req)
	}()
}

type fakeRunnerLease struct {
	mu         sync.Mutex
	recorded   int
	releases   int
	stopErrs   []error
	releaseErr error
}

func (l *fakeRunnerLease) RecordRunner(string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recorded++
	return nil
}

func (l *fakeRunnerLease) ReleaseAfterStop(stopErr error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releases++
	l.stopErrs = append(l.stopErrs, stopErr)
	return l.releaseErr
}

func (l *fakeRunnerLease) counts() (recorded, releases int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.recorded, l.releases
}

func bootstrapTestLease() func(string, LeaseOwner) (runnerLease, error) {
	return func(string, LeaseOwner) (runnerLease, error) {
		return &fakeRunnerLease{}, nil
	}
}

func bootstrapLogger(t *testing.T) *logging.Logger {
	t.Helper()
	logger, err := logging.New(logging.Options{Path: filepath.Join(t.TempDir(), "core.log")})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	return logger
}

// TestBootstrapPublishesOnceAfterModelHello 覆盖慢 hello 成功：只在握手完成后
// 发布一次完整执行器、唤醒调度器一次，关闭时回收同一客户端。
func TestBootstrapPublishesOnceAfterModelHello(t *testing.T) {
	holder := NewRoutingHolder()
	transport := newBootstrapTransport()
	client := NewClient(transport, nil)
	autoHello(t, transport)
	var notified atomic.Int32
	opts := RunnerBootstrapOptions{
		ModelCommand:  "fake-model",
		ModelRequired: true,
		Holder:        holder,
		acquireLease:  bootstrapTestLease(),
		Notify:        func() { notified.Add(1) },
		startClient: func(context.Context, ProcessOptions) (*Client, error) {
			return client, nil
		},
		modelHelloTimeout: time.Second,
	}
	BootstrapRunners(context.Background(), context.Background(), opts)

	if holder.Readiness() != ExecutorReadinessReady {
		t.Fatalf("readiness = %s, want ready", holder.Readiness())
	}
	if notified.Load() != 1 {
		t.Fatalf("notify calls = %d, want 1", notified.Load())
	}
	if transport.closeCount.Load() != 0 {
		t.Fatalf("client closed before holder shutdown: %d", transport.closeCount.Load())
	}

	if err := holder.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if transport.closeCount.Load() != 1 {
		t.Fatalf("client close count = %d, want 1", transport.closeCount.Load())
	}
}

// TestBootstrapWithoutRunnersIsUnavailable 覆盖两侧都未配置：不发布执行器，
// 收敛为 unavailable，并保持迟到发布拒绝语义。
func TestBootstrapWithoutRunnersIsUnavailable(t *testing.T) {
	holder := NewRoutingHolder()
	var notified atomic.Int32
	opts := RunnerBootstrapOptions{
		Holder: holder,
		Notify: func() { notified.Add(1) },
	}
	BootstrapRunners(context.Background(), context.Background(), opts)

	if holder.Readiness() != ExecutorReadinessUnavailable {
		t.Fatalf("readiness = %s, want unavailable", holder.Readiness())
	}
	if notified.Load() != 1 {
		t.Fatalf("notify calls = %d, want 1", notified.Load())
	}
	if holder.Publish(&RoutingExecutor{}, nil) {
		t.Fatal("unavailable holder accepted execution resources")
	}
}

// TestBootstrapModelFailureKeepsRequiredRouting 覆盖既有部分失败语义：模型侧
// 启动失败但 ModelRequired 时仍发布完整路由，模型任务得到可见失败而不是
// 永久等待；宿主媒体能力不被伪装成模型可用。
func TestBootstrapModelFailureKeepsRequiredRouting(t *testing.T) {
	holder := NewRoutingHolder()
	opts := RunnerBootstrapOptions{
		ModelCommand:  "fake-model",
		ModelRequired: true,
		Holder:        holder,
		acquireLease:  bootstrapTestLease(),
		startClient: func(context.Context, ProcessOptions) (*Client, error) {
			return nil, errors.New("model runner failed to start")
		},
	}
	BootstrapRunners(context.Background(), context.Background(), opts)

	if holder.Readiness() != ExecutorReadinessReady {
		t.Fatalf("readiness = %s, want ready with required routing", holder.Readiness())
	}
	result := holder.Execute(context.Background(), Request{Adapter: ComponentRef{ID: "flashcli"}}, nil, nil)
	if result.Error == nil || result.Error.Code != "MODEL_RUNTIME_UNAVAILABLE" {
		t.Fatalf("model execute = %+v, want MODEL_RUNTIME_UNAVAILABLE", result)
	}
	_ = holder.Close()
}

// TestBootstrapHostPartialRouteKeepsLeaseOwnership 覆盖 Host 成功/Model 失败的
// 部分路由：完整执行器一次发布，关闭时只由持有者停止宿主客户端并释放租约。
func TestBootstrapHostPartialRouteKeepsLeaseOwnership(t *testing.T) {
	holder := NewRoutingHolder()
	hostTransport := newBootstrapTransport()
	hostClient := NewClient(hostTransport, nil)
	autoHello(t, hostTransport)
	lease := &fakeRunnerLease{}
	started := make([]string, 0)
	var mu sync.Mutex
	opts := RunnerBootstrapOptions{
		DataDir:       t.TempDir(),
		LeaseOwner:    LeaseOwner{WorkspaceID: "ws", InstanceID: "inst", Epoch: 1},
		HostCommand:   "fake-host",
		ModelCommand:  "fake-model",
		ModelRequired: true,
		Holder:        holder,
		startClient: func(_ context.Context, process ProcessOptions) (*Client, error) {
			mu.Lock()
			started = append(started, process.Command)
			mu.Unlock()
			if process.Command == "fake-host" {
				return hostClient, nil
			}
			return nil, errors.New("model runner failed to start")
		},
		acquireLease: func(string, LeaseOwner) (runnerLease, error) {
			return lease, nil
		},
		hostHelloTimeout: time.Second,
	}
	BootstrapRunners(context.Background(), context.Background(), opts)

	if holder.Readiness() != ExecutorReadinessReady {
		t.Fatalf("readiness = %s, want ready", holder.Readiness())
	}
	holder.mu.Lock()
	routing := holder.executor
	holder.mu.Unlock()
	if routing == nil || routing.Host == nil || routing.Model != nil || !routing.ModelRequired {
		t.Fatalf("published routing = %+v, want host-only partial route", routing)
	}
	if recorded, releases := lease.counts(); recorded != 1 || releases != 0 {
		t.Fatalf("lease before close: recorded=%d releases=%d", recorded, releases)
	}
	mu.Lock()
	commands := append([]string(nil), started...)
	mu.Unlock()
	if len(commands) != 2 || commands[0] != "fake-host" || commands[1] != "fake-model" {
		t.Fatalf("start commands = %v, want host then model", commands)
	}

	if err := holder.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if hostTransport.closeCount.Load() != 1 {
		t.Fatalf("host close count = %d, want 1", hostTransport.closeCount.Load())
	}
	if _, releases := lease.counts(); releases != 1 {
		t.Fatalf("lease releases = %d, want 1", releases)
	}
}

// TestBootstrapSecondInitializationIsRecycled 覆盖重复初始化：第二次初始化
// 不得替换已发布执行器；它创建的客户端由本次初始化自行回收，不留守卫进程。
func TestBootstrapSecondInitializationIsRecycled(t *testing.T) {
	holder := NewRoutingHolder()
	firstTransport := newBootstrapTransport()
	firstClient := NewClient(firstTransport, nil)
	autoHello(t, firstTransport)
	firstOpts := RunnerBootstrapOptions{
		ModelCommand:  "fake-model",
		ModelRequired: true,
		Holder:        holder,
		acquireLease:  bootstrapTestLease(),
		startClient: func(context.Context, ProcessOptions) (*Client, error) {
			return firstClient, nil
		},
		modelHelloTimeout: time.Second,
	}
	BootstrapRunners(context.Background(), context.Background(), firstOpts)
	if holder.Readiness() != ExecutorReadinessReady {
		t.Fatalf("readiness after first bootstrap = %s, want ready", holder.Readiness())
	}
	holder.mu.Lock()
	firstRouting := holder.executor
	holder.mu.Unlock()

	secondTransport := newBootstrapTransport()
	secondClient := NewClient(secondTransport, nil)
	autoHello(t, secondTransport)
	secondOpts := RunnerBootstrapOptions{
		ModelCommand:  "fake-model",
		ModelRequired: true,
		Holder:        holder,
		acquireLease:  bootstrapTestLease(),
		startClient: func(context.Context, ProcessOptions) (*Client, error) {
			return secondClient, nil
		},
		modelHelloTimeout: time.Second,
	}
	BootstrapRunners(context.Background(), context.Background(), secondOpts)

	holder.mu.Lock()
	current := holder.executor
	holder.mu.Unlock()
	if current != firstRouting {
		t.Fatal("second initialization replaced the published executor")
	}
	if secondTransport.closeCount.Load() != 1 {
		t.Fatalf("second initialization leaked its client: close count %d", secondTransport.closeCount.Load())
	}
	if firstTransport.closeCount.Load() != 0 {
		t.Fatalf("second initialization closed the active client: %d", firstTransport.closeCount.Load())
	}
	_ = holder.Close()
	if firstTransport.closeCount.Load() != 1 {
		t.Fatalf("holder close did not stop the active client: %d", firstTransport.closeCount.Load())
	}
}

// TestBootstrapHelloTimeoutClosesClient 覆盖 hello 超时：有界失败、回收客户端、
// 标记不可用，且不把 starting 当作永久隔离。
func TestBootstrapHelloTimeoutClosesClient(t *testing.T) {
	holder := NewRoutingHolder()
	transport := newBootstrapTransport()
	client := NewClient(transport, nil)
	var notified atomic.Int32
	opts := RunnerBootstrapOptions{
		ModelCommand:  "fake-model",
		ModelRequired: false,
		Holder:        holder,
		acquireLease:  bootstrapTestLease(),
		Notify:        func() { notified.Add(1) },
		startClient: func(context.Context, ProcessOptions) (*Client, error) {
			return client, nil
		},
		modelHelloTimeout: 40 * time.Millisecond,
	}
	started := time.Now()
	BootstrapRunners(context.Background(), context.Background(), opts)
	elapsed := time.Since(started)

	if elapsed < 40*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("hello timeout elapsed = %s, want bounded timeout", elapsed)
	}
	if holder.Readiness() != ExecutorReadinessUnavailable {
		t.Fatalf("readiness = %s, want unavailable", holder.Readiness())
	}
	if transport.closeCount.Load() != 1 {
		t.Fatalf("timed-out client close count = %d, want 1", transport.closeCount.Load())
	}
	if notified.Load() != 1 {
		t.Fatalf("notify calls = %d, want 1", notified.Load())
	}
}

// TestBootstrapCanceledBeforeHelloDoesNotPublish 覆盖 Core 先退出：取消启动
// 上下文后不发布、回收已创建的客户端，且不向已关闭持有者写入健康状态。
func TestBootstrapCanceledBeforeHelloDoesNotPublish(t *testing.T) {
	holder := NewRoutingHolder()
	startupCtx, cancel := context.WithCancel(context.Background())
	transport := newBootstrapTransport()
	client := NewClient(transport, nil)
	opts := RunnerBootstrapOptions{
		ModelCommand:  "fake-model",
		ModelRequired: false,
		Holder:        holder,
		acquireLease:  bootstrapTestLease(),
		startClient: func(context.Context, ProcessOptions) (*Client, error) {
			cancel()
			return client, nil
		},
		modelHelloTimeout: time.Second,
	}
	BootstrapRunners(startupCtx, context.Background(), opts)

	if holder.Readiness() != ExecutorReadinessUnavailable {
		t.Fatalf("readiness = %s, want unavailable after cancellation", holder.Readiness())
	}
	if transport.closeCount.Load() != 1 {
		t.Fatalf("canceled client close count = %d, want 1", transport.closeCount.Load())
	}
}

// TestBootstrapLateHelloAfterCloseIsRecycled 覆盖关闭后的迟到 hello：晚到成功
// 不得向已关闭的持有者安装执行器，初始化方仍负责回收客户端。
func TestBootstrapLateHelloAfterCloseIsRecycled(t *testing.T) {
	holder := NewRoutingHolder()
	transport := newBootstrapTransport()
	client := NewClient(transport, nil)
	started := make(chan struct{})
	opts := RunnerBootstrapOptions{
		ModelCommand:  "fake-model",
		ModelRequired: true,
		Holder:        holder,
		acquireLease:  bootstrapTestLease(),
		startClient: func(context.Context, ProcessOptions) (*Client, error) {
			close(started)
			return client, nil
		},
		modelHelloTimeout: 5 * time.Second,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		BootstrapRunners(context.Background(), context.Background(), opts)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("bootstrap did not start the client")
	}
	if err := holder.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// hello 在持有者关闭后才成功返回，模拟晚到结果。
	req := <-transport.out
	replyHello(t, transport, req)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("bootstrap did not converge after late hello")
	}
	if holder.Readiness() != ExecutorReadinessClosed {
		t.Fatalf("readiness = %s, want closed", holder.Readiness())
	}
	if transport.closeCount.Load() != 1 {
		t.Fatalf("late client close count = %d, want 1", transport.closeCount.Load())
	}
}

// toggleTransport wraps the in-memory transport and can fail Send/Kill after
// hello, without swapping the Client's transport while its read loop is active.
type toggleTransport struct {
	base *bootstrapTransport
	fail atomic.Bool
}

func (t *toggleTransport) Send(frame Frame) error {
	if t.fail.Load() {
		return errors.New("send failed")
	}
	return t.base.Send(frame)
}
func (t *toggleTransport) Receive() (Frame, error) { return t.base.Receive() }
func (t *toggleTransport) Kill() error {
	if t.fail.Load() {
		return errors.New("kill failed")
	}
	return t.base.Kill()
}
func (t *toggleTransport) Close() error { return t.base.Close() }

// TestBootstrapModelOnlyAcquiresAndReleasesLease covers the packaged WSL route:
// a model-only bootstrap must still take the workspace lease and release it only
// after the model client's Close is confirmed.
func TestBootstrapModelOnlyAcquiresAndReleasesLease(t *testing.T) {
	holder := NewRoutingHolder()
	transport := newBootstrapTransport()
	client := NewClient(transport, nil)
	autoHello(t, transport)
	lease := &fakeRunnerLease{}
	var modelOptions ProcessOptions
	opts := RunnerBootstrapOptions{
		ModelCommand:  "fake-model",
		ModelRequired: true,
		Holder:        holder,
		acquireLease:  func(string, LeaseOwner) (runnerLease, error) { return lease, nil },
		startClient: func(_ context.Context, options ProcessOptions) (*Client, error) {
			modelOptions = options
			return client, nil
		},
		modelHelloTimeout: time.Second,
	}
	BootstrapRunners(context.Background(), context.Background(), opts)
	if holder.Readiness() != ExecutorReadinessReady {
		t.Fatalf("readiness = %s, want ready", holder.Readiness())
	}
	// The model route must be explicitly marked uncovered; wrapper launchers
	// (cmd.exe /c wsl.exe, forwarding exe) cannot fail open via basename.
	if !modelOptions.DetachedChildrenUncovered {
		t.Fatal("model route did not set DetachedChildrenUncovered")
	}
	if recorded, releases := lease.counts(); recorded != 1 || releases != 0 {
		t.Fatalf("lease before close: recorded=%d releases=%d", recorded, releases)
	}
	if err := holder.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, releases := lease.counts(); releases != 1 {
		t.Fatalf("lease releases = %d, want 1", releases)
	}
	lease.mu.Lock()
	stopErr := lease.stopErrs[len(lease.stopErrs)-1]
	lease.mu.Unlock()
	if stopErr != nil {
		t.Fatalf("confirmed model stop passed error to lease: %v", stopErr)
	}
}

// TestBootstrapLeaseConflictDoesNotStartModelRunner makes a stale workspace
// block both Runner routes before any client starts.
func TestBootstrapLeaseConflictDoesNotStartModelRunner(t *testing.T) {
	holder := NewRoutingHolder()
	leaseErr := errors.New("old workspace execution unconfirmed")
	var started atomic.Bool
	opts := RunnerBootstrapOptions{
		HostCommand:   "fake-host",
		ModelCommand:  "fake-model",
		ModelRequired: true,
		Holder:        holder,
		acquireLease:  func(string, LeaseOwner) (runnerLease, error) { return nil, leaseErr },
		startClient: func(context.Context, ProcessOptions) (*Client, error) {
			started.Store(true)
			return nil, errors.New("must not start")
		},
	}
	BootstrapRunners(context.Background(), context.Background(), opts)
	if started.Load() {
		t.Fatal("runner started without a workspace lease")
	}
	if !errors.Is(holder.Err(), leaseErr) {
		t.Fatalf("holder error = %v, want lease conflict", holder.Err())
	}
}

// TestBootstrapUnconfirmedModelCloseRetainsLease verifies that an unconfirmed
// model client close is passed to ReleaseAfterStop rather than silently
// releasing the workspace lease.
func TestBootstrapUnconfirmedModelCloseRetainsLease(t *testing.T) {
	holder := NewRoutingHolder()
	base := newBootstrapTransport()
	transport := &toggleTransport{base: base}
	client := NewClient(transport, nil)
	autoHello(t, base)
	lease := &fakeRunnerLease{}
	opts := RunnerBootstrapOptions{
		ModelCommand:      "fake-model",
		ModelRequired:     true,
		Holder:            holder,
		acquireLease:      func(string, LeaseOwner) (runnerLease, error) { return lease, nil },
		startClient:       func(context.Context, ProcessOptions) (*Client, error) { return client, nil },
		modelHelloTimeout: time.Second,
	}
	BootstrapRunners(context.Background(), context.Background(), opts)
	transport.fail.Store(true)
	client.activeMu.Lock()
	client.active = &Request{TaskID: "task-unconfirmed", StepID: "step-1", Attempt: 1}
	client.activeMu.Unlock()
	closeErr := holder.Close()
	if !errors.Is(closeErr, ErrStopUnconfirmed) {
		t.Fatalf("holder close = %v, want ErrStopUnconfirmed", closeErr)
	}
	lease.mu.Lock()
	stopErr := lease.stopErrs[len(lease.stopErrs)-1]
	releases := lease.releases
	lease.mu.Unlock()
	if releases != 1 {
		t.Fatalf("lease release attempts = %d, want exactly 1", releases)
	}
	if stopErr == nil || !errors.Is(stopErr, ErrStopUnconfirmed) {
		t.Fatalf("lease stop error = %v, want ErrStopUnconfirmed", stopErr)
	}
}
