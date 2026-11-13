//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
)

// TestShutdownRunnerResourcesOrdersClientCloseBeforeProcessCancel 锁定关闭顺序：
// 已发布客户端必须先正常关闭，再等待未发布 bootstrap 回收，最后取消进程树
// 生命周期上下文。先取消进程 ctx 会让活动 Execute 断流并保留租约。
func TestShutdownRunnerResourcesOrdersClientCloseBeforeProcessCancel(t *testing.T) {
	holder := execadapter.NewRoutingHolder()
	var mu sync.Mutex
	var order []string
	record := func(step string) {
		mu.Lock()
		order = append(order, step)
		mu.Unlock()
	}
	closed := make(chan struct{})
	if !holder.Publish(&execadapter.RoutingExecutor{}, func() error {
		record("close")
		close(closed)
		return nil
	}) {
		t.Fatal("publish rejected")
	}
	bootstrapDone := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseBootstrap := func() {
		releaseOnce.Do(func() { close(release) })
	}
	bootstrapExited := make(chan struct{})
	go func() {
		defer close(bootstrapExited)
		<-release
		record("bootstrap")
		close(bootstrapDone)
	}()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		shutdownRunnerResources(shutdownCtx, holder, bootstrapDone, func() {
			record("cancel")
		}, nil)
	}()
	// 失败或提前终止也要放行并 join 两个 helper，避免 goroutine 与通道遗留。
	t.Cleanup(func() {
		releaseBootstrap()
		waitHelperExit(t, finished, "shutdown helper")
		waitHelperExit(t, bootstrapExited, "bootstrap helper")
	})

	// close 已观测屏障：清理必须已执行，而 shutdown 仍在等待未发布 bootstrap。
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("holder cleanup was not observed before bootstrap release")
	}
	select {
	case <-finished:
		t.Fatal("shutdown finished before the unpublished bootstrap was released")
	default:
	}

	releaseBootstrap()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not converge")
	}
	<-bootstrapExited

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	want := []string{"close", "bootstrap", "cancel"}
	if len(got) != len(want) {
		t.Fatalf("shutdown order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("shutdown order = %v, want %v", got, want)
		}
	}
}

// waitHelperExit 在清理路径 join 测试 helper goroutine；超时用 Errorf 暴露，
// 不在 cleanup 中 Fatal，保证其余清理仍会执行。
func waitHelperExit(t *testing.T, done <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Errorf("%s did not exit during cleanup", name)
	}
}

// TestShutdownRunnerResourcesBoundsBootstrapWait 覆盖 bootstrap 不收敛时的
// 有界等待：仍调用进程生命周期取消，不无限阻塞。
func TestShutdownRunnerResourcesBoundsBootstrapWait(t *testing.T) {
	holder := execadapter.NewRoutingHolder()
	shutdownCtx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := false
	shutdownRunnerResources(shutdownCtx, holder, make(chan struct{}), func() {
		canceled = true
	}, nil)
	if !canceled {
		t.Fatal("bounded shutdown did not cancel the process context")
	}
}

type shutdownFixture struct {
	command string
	marker  string
}

// newShutdownFixture 生成真实 Runner 子进程夹具：hello 应答；execute 只写
// marker 并保持活动；cancel 以 exited=true 确认，让 Client.Close 正常收敛。
func newShutdownFixture(t *testing.T) shutdownFixture {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "shutdown-runner.py")
	marker := filepath.Join(dir, "execute.started")
	body := `import json, sys
marker = sys.argv[1]
for line in sys.stdin:
    request = json.loads(line)
    op = request.get("op")
    if op == "hello":
        frame = {"v": 1, "op": "result", "req_id": request.get("req_id"), "payload": {
            "request_op": "hello",
            "data": {"runner_version": "shutdown-fixture", "protocol_version": 1,
                     "adapters": [{"id": "ffmpeg"}], "media_tools": {}},
        }}
        sys.stdout.write(json.dumps(frame) + "\n")
        sys.stdout.flush()
    elif op == "execute":
        with open(marker, "w", encoding="utf-8") as handle:
            handle.write("started")
    elif op == "cancel":
        payload = request.get("payload") or {}
        frame = {"v": 1, "op": "result", "req_id": request.get("req_id"), "payload": {
            "request_op": "cancel",
            "data": {"task_id": payload.get("task_id"), "attempt": payload.get("attempt"),
                     "step_id": payload.get("step_id"), "exited": True, "active": False},
        }}
        sys.stdout.write(json.dumps(frame) + "\n")
        sys.stdout.flush()
`
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	wrapper := filepath.Join(dir, "shutdown-runner.sh")
	shell := fmt.Sprintf("#!/bin/sh\nexec %q -I %q %q\n", python, script, marker)
	if err := os.WriteFile(wrapper, []byte(shell), 0o700); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
	return shutdownFixture{command: wrapper, marker: marker}
}

func shutdownLeaseOwner() execadapter.LeaseOwner {
	return execadapter.LeaseOwner{WorkspaceID: "ws-shutdown", InstanceID: "instance-shutdown", Epoch: 1}
}

type shutdownScenario struct {
	dataDir       string
	holder        *execadapter.RoutingHolder
	cancelProcess context.CancelFunc
	execDone      chan execadapter.Result
	execExited    <-chan struct{}
	bootstrapDone <-chan struct{}
}

// startShutdownScenario 通过真实 BootstrapRunners 发布宿主执行器并启动一次
// 活动 execute，运行到 Runner 已收到 execute 为止。execCtx 模拟调度执行上下文。
// 它返回真实子进程传输 + 协议夹具（不是产品 Runner）；断言失败时由 cleanup
// 关闭持有者、取消进程 ctx 并 join execute，避免临时目录与 newEnv 清理等待
// 仍活动的执行。
func startShutdownScenario(t *testing.T, fixture shutdownFixture, execCtx context.Context) shutdownScenario {
	t.Helper()
	dataDir := t.TempDir()
	outputDir := t.TempDir()
	processCtx, cancelProcess := context.WithCancel(context.Background())
	logger, err := logging.New(logging.Options{Path: filepath.Join(t.TempDir(), "core.log")})
	if err != nil {
		cancelProcess()
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	holder := execadapter.NewRoutingHolder()
	execDone := make(chan execadapter.Result, 1)
	execExited := make(chan struct{})
	execStarted := false
	t.Cleanup(func() {
		// 失败路径同样收敛：按正常顺序关闭已发布客户端，再取消进程 ctx，
		// 最后等 execute 返回；不收窄生产清理顺序。
		if err := holder.Close(); err != nil && !errors.Is(err, execadapter.ErrStopUnconfirmed) {
			t.Errorf("cleanup holder close: %v", err)
		}
		cancelProcess()
		if execStarted {
			waitHelperExit(t, execExited, "execute helper")
		}
	})

	execadapter.BootstrapRunners(context.Background(), processCtx, execadapter.RunnerBootstrapOptions{
		DataDir:     dataDir,
		LeaseOwner:  shutdownLeaseOwner(),
		HostCommand: fixture.command,
		Logger:      logger,
		Holder:      holder,
	})
	if holder.Readiness() != execadapter.ExecutorReadinessReady {
		t.Fatalf("bootstrap readiness = %s, want ready", holder.Readiness())
	}
	bootstrapDone := make(chan struct{})
	close(bootstrapDone)
	execStarted = true
	go func() {
		defer close(execExited)
		execDone <- holder.Execute(execCtx, execadapter.Request{
			Workspace:   "ws-shutdown",
			ServerEpoch: 1,
			TaskID:      "task-shutdown",
			StepID:      "step-1",
			Attempt:     1,
			Adapter:     execadapter.ComponentRef{ID: "ffmpeg", Digest: "test"},
			OutputDir:   outputDir,
		}, nil, nil)
	}()
	waitForFile(t, fixture.marker, 5*time.Second)
	return shutdownScenario{
		dataDir:       dataDir,
		holder:        holder,
		cancelProcess: cancelProcess,
		execDone:      execDone,
		execExited:    execExited,
		bootstrapDone: bootstrapDone,
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture did not start execute within %s", timeout)
}

// TestRunnerShutdownOrderReleasesLeaseWithActiveExecution 覆盖关闭顺序的分层
// 边界时序（真实子进程传输 + 协议夹具，不是产品 Runner，也不是真实 Core
// 慢取消复现）：HTTP shutdown 与 sched.Close 共用同一 10s 预算，前者被长连接/
// 慢取消耗尽时，sched.Close 可能在活动 Execute 尚未收敛前返回。此时仍应先由
// Client.Close 向仍存活的 Runner 发送 Cancel 并取得确认，租约随后正常释放。
//
// 这是预算耗尽边界的防御性覆盖，不代表普通任务或真实 Core 上必现：正常预算内
// sched.Close 会等 Executor.Execute 取消确认完成，active 通常已清。
func TestRunnerShutdownOrderReleasesLeaseWithActiveExecution(t *testing.T) {
	fixture := newShutdownFixture(t)
	execCtx, cancelExec := context.WithCancel(context.Background())
	defer cancelExec()
	scenario := startShutdownScenario(t, fixture, execCtx)
	// 预算耗尽时序：sched.Close 已取消执行 ctx，但可能在 wg 收敛前返回。
	cancelExec()
	shutdownRunnerResources(context.Background(), scenario.holder, scenario.bootstrapDone, scenario.cancelProcess, nil)

	select {
	case result := <-scenario.execDone:
		if result.StopUnconfirmed {
			t.Fatalf("graceful close reported unconfirmed stop: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active execution did not settle after shutdown")
	}
	lease, err := execadapter.AcquireWorkspaceLease(scenario.dataDir, shutdownLeaseOwner())
	if err != nil {
		t.Fatalf("confirmed shutdown did not release workspace lease: %v", err)
	}
	if err := lease.ReleaseAfterStop(nil); err != nil {
		t.Fatalf("release reopened lease: %v", err)
	}
}

// TestRunnerShutdownOrderSettlesNormalCancelBeforeClose 覆盖常见预算内路径：
// 调度取消执行 ctx 后，Executor.Execute 用 WithoutCancel 完成 Cancel 确认并
// 清除 active；随后关闭客户端不再依赖进程 ctx 的先后。
func TestRunnerShutdownOrderSettlesNormalCancelBeforeClose(t *testing.T) {
	fixture := newShutdownFixture(t)
	execCtx, cancelExec := context.WithCancel(context.Background())
	defer cancelExec()
	scenario := startShutdownScenario(t, fixture, execCtx)

	// 等价于 sched.Close 在预算内取消执行 ctx 并等待 wg。
	cancelExec()
	select {
	case result := <-scenario.execDone:
		if result.StopUnconfirmed {
			t.Fatalf("normal cancel reported unconfirmed stop: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("normal cancel did not settle")
	}
	if err := scenario.holder.Close(); err != nil {
		t.Fatalf("close after settled cancel: %v", err)
	}
	lease, err := execadapter.AcquireWorkspaceLease(scenario.dataDir, shutdownLeaseOwner())
	if err != nil {
		t.Fatalf("settled cancel did not release workspace lease: %v", err)
	}
	if err := lease.ReleaseAfterStop(nil); err != nil {
		t.Fatalf("release reopened lease: %v", err)
	}
	scenario.cancelProcess()
}

// TestRunnerProcessLossBeforeCloseRetainsLease 是关闭顺序的永久负测，覆盖
// 预算耗尽后先取消进程生命周期的强制路径：活动 Execute 断流，Client.Close
// 只能得到 ErrStopUnconfirmed；此时租约必须保留，下一次启动不能凭 PID 夺取。
//
// 该测试证明“先 cancelProcess 再 holder.Close”会在本可尝试正常确认的边界上
// 变成未确认并保留租约（fail-closed）；它不声称普通任务在预算内也必现。
func TestRunnerProcessLossBeforeCloseRetainsLease(t *testing.T) {
	fixture := newShutdownFixture(t)
	scenario := startShutdownScenario(t, fixture, context.Background())

	// 强制路径：进程 ctx 先取消，等价于预算耗尽后先杀进程树再关闭客户端。
	scenario.cancelProcess()
	select {
	case result := <-scenario.execDone:
		if !result.StopUnconfirmed {
			t.Fatalf("process loss falsely confirmed exit: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active execution did not observe process loss")
	}
	if err := scenario.holder.Close(); !errors.Is(err, execadapter.ErrStopUnconfirmed) {
		t.Fatalf("close after process loss = %v, want ErrStopUnconfirmed", err)
	}
	if _, err := execadapter.AcquireWorkspaceLease(scenario.dataDir, shutdownLeaseOwner()); !errors.Is(err, execadapter.ErrWorkspaceLeased) {
		t.Fatalf("uncertain stop released workspace lease: %v", err)
	}
}

func TestResolveMLSToolsPathPrefersWSLThenHost(t *testing.T) {
	env := map[string]string{
		"YAOMU_WSL_MLSTOOLS_BIN": "/wsl/bin/mlstools",
		"MLSTOOLS_BIN":           "/host/bin/mlstools",
	}
	if got := resolveMLSToolsPath(func(key string) string { return env[key] }); got != "/wsl/bin/mlstools" {
		t.Fatalf("WSL path should win, got %q", got)
	}
	delete(env, "YAOMU_WSL_MLSTOOLS_BIN")
	if got := resolveMLSToolsPath(func(key string) string { return env[key] }); got != "/host/bin/mlstools" {
		t.Fatalf("host path fallback = %q", got)
	}
	delete(env, "MLSTOOLS_BIN")
	if got := resolveMLSToolsPath(func(key string) string { return env[key] }); got != "" {
		t.Fatalf("empty env = %q", got)
	}
}
