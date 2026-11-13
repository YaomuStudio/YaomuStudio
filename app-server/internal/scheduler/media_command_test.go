package scheduler

// 媒体集成测试的外部命令生命周期边界（test-only）。
//
// 目标：每条受控媒体命令都有独立预算；失败时给出一句话可定位的阶段/类别/
// 耗时摘要；超时、取消与输出超限不能被降级成 skip 或 dummy。
//
// 边界：
//   - 只约束本测试拉起的直接子进程；CommandContext 默认发送 Kill。
//   - WaitDelay 只处理“直接子进程已退出但继承管道未关闭”的情况。
//   - 若操作系统把进程置于不可中断等待（内核/硬件级 IO 阻塞等），
//     Process.Wait 仍可能超过预算。本 helper 不承诺强杀一定成功，
//     go test 的外层 -timeout 仍是最终诊断兜底。
//   - 失败摘要只含阶段标签、binary 基名、类别、耗时与退出码；stderr 尾部
//     仅用于测试内固定缺 codec/filter 分类与断言，不写入默认输出；不输出
//     完整 argv、环境或媒体正文。

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	// 单条命令默认预算；同时受测试剩余时间（扣除清理余量）约束。
	mediaCommandDefaultBudget = 30 * time.Second
	mediaCommandCleanupMargin = 2 * time.Second
	// 直接子进程退出后，等待继承管道关闭的上限。
	mediaCommandWaitDelay = time.Second
	// stdout（如 ffprobe JSON）上限；超限明确失败，不截断当成功。
	mediaCommandStdoutLimit = 1 << 20
	// stderr 仅保留尾部，用于测试内固定缺 codec/filter 分类与断言，
	// 不进入默认失败摘要。
	mediaCommandStderrTailLimit = 8 << 10
)

type mediaCommandFailureKind string

const (
	mediaCommandExitFailure         mediaCommandFailureKind = "exit"
	mediaCommandStartFailure        mediaCommandFailureKind = "start"
	mediaCommandTimeout             mediaCommandFailureKind = "timeout"
	mediaCommandCanceled            mediaCommandFailureKind = "canceled"
	mediaCommandStdoutLimitExceeded mediaCommandFailureKind = "stdout_limit"
	mediaCommandPipeHeld            mediaCommandFailureKind = "wait_delay_pipe_held"
	mediaCommandBudgetExhausted     mediaCommandFailureKind = "test_budget_exhausted"
)

type mediaCommandError struct {
	Stage       string
	Binary      string
	Kind        mediaCommandFailureKind
	ExitCode    int
	Unsupported bool
	StderrTail  string
	Elapsed     time.Duration
	Budget      time.Duration
	Err         error
}

func (e *mediaCommandError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "media command stage=%q binary=%q kind=%s elapsed=%s",
		e.Stage, filepath.Base(e.Binary), e.Kind, e.Elapsed.Round(time.Millisecond))
	if e.Budget > 0 {
		fmt.Fprintf(&b, " budget=%s", e.Budget)
	}
	if e.ExitCode != 0 {
		fmt.Fprintf(&b, " exit=%d", e.ExitCode)
	}
	if e.Unsupported {
		b.WriteString(" unsupported_codec_or_filter")
	}
	// 不直接格式化底层 error：start failure 的 exec/os.PathError 可能带完整
	// binary 路径或任意原文。内部 error 仍由 Unwrap 保留，供测试断言。
	return b.String()
}

func (e *mediaCommandError) Unwrap() error { return e.Err }

// hardFailure 标记不得回退为 dummy/skip 的失败类别。
func (e *mediaCommandError) hardFailure() bool {
	switch e.Kind {
	case mediaCommandTimeout,
		mediaCommandCanceled,
		mediaCommandStdoutLimitExceeded,
		mediaCommandPipeHeld,
		mediaCommandBudgetExhausted:
		return true
	}
	return false
}

type mediaCommandOptions struct {
	Stage  string
	Budget time.Duration
}

// mediaCommandBudget 取显式预算与“测试剩余时间减清理余量”的较小值。
func mediaCommandBudget(t *testing.T, override time.Duration) (time.Duration, error) {
	t.Helper()
	budget := mediaCommandDefaultBudget
	if override > 0 {
		budget = override
	}
	if deadline, ok := t.Deadline(); ok {
		remaining := time.Until(deadline) - mediaCommandCleanupMargin
		if remaining <= 0 {
			return 0, fmt.Errorf("test deadline reached before media command (cleanup margin %s)", mediaCommandCleanupMargin)
		}
		if remaining < budget {
			budget = remaining
		}
	}
	return budget, nil
}

// runMediaCommandBounded 同步执行受控媒体命令，返回 stdout 或可分类失败。
// 同步 Wait 保证直接子进程被回收；不使用后台遗忘 Wait 伪造有界返回。
func runMediaCommandBounded(
	t *testing.T,
	ctx context.Context,
	opts mediaCommandOptions,
	binary string,
	args ...string,
) ([]byte, *mediaCommandError) {
	t.Helper()
	stage := opts.Stage
	if stage == "" {
		stage = filepath.Base(binary)
	}
	budget, err := mediaCommandBudget(t, opts.Budget)
	if err != nil {
		return nil, &mediaCommandError{
			Stage: stage, Binary: binary, Kind: mediaCommandBudgetExhausted, Err: err,
		}
	}

	commandCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	stdout := &mediaBoundedStdout{limit: mediaCommandStdoutLimit}
	stderr := &mediaStderrTail{limit: mediaCommandStderrTailLimit}
	command := exec.CommandContext(commandCtx, binary, args...)
	command.WaitDelay = mediaCommandWaitDelay
	command.Stdout = stdout
	command.Stderr = stderr

	started := time.Now()
	runErr := command.Run()
	failure := &mediaCommandError{
		Stage:      stage,
		Binary:     binary,
		Budget:     budget,
		Elapsed:    time.Since(started),
		StderrTail: strings.TrimSpace(stderr.String()),
	}

	if runErr == nil {
		if !stdout.truncated {
			return stdout.Bytes(), nil
		}
		failure.Kind = mediaCommandStdoutLimitExceeded
		failure.Err = fmt.Errorf("stdout exceeded %d bytes", mediaCommandStdoutLimit)
		return nil, failure
	}

	switch {
	case ctx.Err() != nil:
		failure.Kind = mediaCommandCanceled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			failure.Kind = mediaCommandTimeout
		}
		failure.Err = fmt.Errorf("caller context: %w", ctx.Err())
	case commandCtx.Err() != nil:
		failure.Kind = mediaCommandCanceled
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			failure.Kind = mediaCommandTimeout
		}
		failure.Err = fmt.Errorf("command budget %s exceeded: %w", budget, commandCtx.Err())
	case stdout.truncated:
		failure.Kind = mediaCommandStdoutLimitExceeded
		failure.Err = fmt.Errorf("stdout exceeded %d bytes", mediaCommandStdoutLimit)
	case errors.Is(runErr, exec.ErrWaitDelay):
		failure.Kind = mediaCommandPipeHeld
		failure.Err = runErr
	default:
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			failure.Kind = mediaCommandExitFailure
			failure.ExitCode = exitErr.ExitCode()
			failure.Err = runErr
		} else {
			failure.Kind = mediaCommandStartFailure
			failure.Err = runErr
		}
		failure.Unsupported = mediaStderrUnsupported(failure.StderrTail)
	}
	return nil, failure
}

// runMediaCommand 保留既有媒体合成语义：明确缺编码器/滤镜时显式 skip。
func runMediaCommand(t *testing.T, stage, binary string, args ...string) []byte {
	t.Helper()
	output, failure := runMediaCommandBounded(t, context.Background(), mediaCommandOptions{Stage: stage}, binary, args...)
	if failure != nil {
		if failure.Unsupported {
			t.Skipf("MEDIA_HELPER_UNSUPPORTED: %s 缺少所需编码器/滤镜：%v", filepath.Base(binary), failure)
		}
		t.Fatalf("%v", failure)
	}
	return output
}

// runMediaCommandStrict 用于解码/探测：任何失败都是测试失败，不降级为 skip。
func runMediaCommandStrict(t *testing.T, stage, binary string, args ...string) []byte {
	t.Helper()
	output, failure := runMediaCommandBounded(t, context.Background(), mediaCommandOptions{Stage: stage}, binary, args...)
	if failure != nil {
		t.Fatalf("%v", failure)
	}
	return output
}

func mediaStderrUnsupported(stderrTail string) bool {
	return strings.Contains(stderrTail, "Unknown encoder") || strings.Contains(stderrTail, "No such filter")
}

// mediaBoundedStdout 只保留前 limit 字节，超出时仍持续排空管道并标记截断。
type mediaBoundedStdout struct {
	limit     int
	buffer    []byte
	written   int
	truncated bool
}

func (w *mediaBoundedStdout) Write(p []byte) (int, error) {
	if room := w.limit - len(w.buffer); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		w.buffer = append(w.buffer, p[:room]...)
	}
	w.written += len(p)
	if w.written > w.limit {
		w.truncated = true
	}
	return len(p), nil
}

func (w *mediaBoundedStdout) Bytes() []byte { return w.buffer }

// mediaStderrTail 只保留最近 limit 字节，仅用于固定缺 codec/filter 分类
// 与测试断言，不进入默认失败摘要。
type mediaStderrTail struct {
	limit int
	data  []byte
}

func (w *mediaStderrTail) Write(p []byte) (int, error) {
	if len(p) >= w.limit {
		w.data = append(w.data[:0], p[len(p)-w.limit:]...)
		return len(p), nil
	}
	if overflow := len(w.data) + len(p) - w.limit; overflow > 0 {
		w.data = append(w.data[:0], w.data[overflow:]...)
	}
	w.data = append(w.data, p...)
	return len(p), nil
}

func (w *mediaStderrTail) String() string { return string(w.data) }

const mediaCommandHelperToken = "yaomu-media-command-helper"

const (
	mediaHelperModeSuccess     = "success"
	mediaHelperModeExit3       = "exit3"
	mediaHelperModeUnsupported = "unsupported"
	mediaHelperModeSleep       = "sleep"
	mediaHelperModeFlood       = "flood"
	mediaHelperModePipeHold    = "pipe_hold"
	mediaHelperModePipeChild   = "pipe_child"
)

// mediaCommandHelperArgs 构造自举子进程参数；模式走 flag.Args()，
// 不向真实媒体命令扩散额外环境变量。
func mediaCommandHelperArgs(mode string, extra ...string) []string {
	args := []string{"-test.run=^TestMediaCommandHelperProcess$", "--", mediaCommandHelperToken, mode}
	return append(args, extra...)
}

// TestMediaCommandHelperProcess 是媒体命令测试的自举子进程入口；
// 普通测试运行（无位置参数）时跳过。
func TestMediaCommandHelperProcess(t *testing.T) {
	args := flag.Args()
	if len(args) < 2 || args[0] != mediaCommandHelperToken {
		t.Skip("media command helper process only")
	}
	switch args[1] {
	case mediaHelperModeSuccess:
		fmt.Fprint(os.Stdout, "helper-stdout-ok")
		os.Exit(0)
	case mediaHelperModeExit3:
		fmt.Fprint(os.Stdout, "stdout-before-failure")
		fmt.Fprint(os.Stderr, "helper simulated failure")
		os.Exit(3)
	case mediaHelperModeUnsupported:
		fmt.Fprint(os.Stderr, "Unknown encoder 'yaomu-missing-encoder'")
		os.Exit(1)
	case mediaHelperModeSleep:
		// 自限 10s：即使预算终止机制失效，本测试拥有的进程也会自行结束。
		time.Sleep(10 * time.Second)
		os.Exit(0)
	case mediaHelperModeFlood:
		chunk := []byte(strings.Repeat("x", 64<<10))
		remaining := mediaCommandStdoutLimit + 4096
		for remaining > 0 {
			write := len(chunk)
			if write > remaining {
				write = remaining
			}
			if _, err := os.Stdout.Write(chunk[:write]); err != nil {
				os.Exit(4)
			}
			remaining -= write
		}
		os.Exit(0)
	case mediaHelperModePipeHold:
		if len(args) < 3 {
			fmt.Fprint(os.Stderr, "pipe_hold mode requires marker path")
			os.Exit(8)
		}
		fmt.Fprint(os.Stdout, "helper-stdout-before-pipe-hold")
		grandchild := exec.Command(os.Args[0], mediaCommandHelperArgs(mediaHelperModePipeChild, args[2])...)
		// 孙进程继承 stdout 管道：直接子进程退出后管道仍被持有。
		grandchild.Stdout = os.Stdout
		grandchild.Stderr = os.Stderr
		if err := grandchild.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "start pipe grandchild: %v", err)
			os.Exit(5)
		}
		// 不等待孙进程：它自限完成工作后退出；marker 只记录“自限工作已完成”，
		// 不是进程退出证据。测试自有的写端关闭确认见 owned-pipe 用例。
		os.Exit(0)
	case mediaHelperModePipeChild:
		if len(args) < 3 {
			os.Exit(9)
		}
		time.Sleep(3 * time.Second)
		if err := os.WriteFile(args[2], []byte("grandchild-work-done"), 0o600); err != nil {
			os.Exit(6)
		}
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode %q", args[1])
		os.Exit(7)
	}
}

func TestMediaCommandHelperReturnsStdout(t *testing.T) {
	output, failure := runMediaCommandBounded(t, context.Background(),
		mediaCommandOptions{Stage: "helper_success"},
		os.Args[0], mediaCommandHelperArgs(mediaHelperModeSuccess)...)
	if failure != nil {
		t.Fatalf("helper success: %v", failure)
	}
	if got, want := string(output), "helper-stdout-ok"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestMediaCommandHelperClassifiesExitFailure(t *testing.T) {
	_, failure := runMediaCommandBounded(t, context.Background(),
		mediaCommandOptions{Stage: "helper_exit"},
		os.Args[0], mediaCommandHelperArgs(mediaHelperModeExit3)...)
	if failure == nil {
		t.Fatal("expected a non-zero exit failure")
	}
	if failure.Kind != mediaCommandExitFailure || failure.ExitCode != 3 {
		t.Fatalf("failure = %+v, want exit failure with code 3", failure)
	}
	if failure.Unsupported {
		t.Fatalf("plain exit failure must not be classified unsupported: %+v", failure)
	}
	if !strings.Contains(failure.StderrTail, "helper simulated failure") {
		t.Fatalf("stderr tail = %q", failure.StderrTail)
	}
	if !strings.Contains(failure.Error(), `stage="helper_exit"`) {
		t.Fatalf("failure summary missing stage: %v", failure)
	}
	if strings.Contains(failure.Error(), "helper simulated failure") {
		t.Fatalf("default failure summary must not print raw stderr: %v", failure)
	}
}

func TestMediaCommandHelperMarksUnsupportedCodec(t *testing.T) {
	_, failure := runMediaCommandBounded(t, context.Background(),
		mediaCommandOptions{Stage: "helper_unsupported"},
		os.Args[0], mediaCommandHelperArgs(mediaHelperModeUnsupported)...)
	if failure == nil {
		t.Fatal("expected a codec/filter failure")
	}
	if !failure.Unsupported {
		t.Fatalf("failure = %+v, want unsupported codec/filter", failure)
	}
	if failure.Kind != mediaCommandExitFailure {
		t.Fatalf("kind = %s, want exit", failure.Kind)
	}
	if failure.hardFailure() {
		t.Fatalf("unsupported codec/filter must stay a skip-class failure: %+v", failure)
	}
	if strings.Contains(failure.Error(), "Unknown encoder") {
		t.Fatalf("default failure summary must not print raw stderr: %v", failure)
	}
}

func TestMediaCommandStartFailureOmitsRawPath(t *testing.T) {
	privateDir := filepath.Join(t.TempDir(), "private-origin-dir")
	binary := filepath.Join(privateDir, "missing-media-tool")
	_, failure := runMediaCommandBounded(t, context.Background(),
		mediaCommandOptions{Stage: "helper_missing_binary"}, binary)
	if failure == nil {
		t.Fatal("expected a start failure for a nonexistent binary")
	}
	if failure.Kind != mediaCommandStartFailure {
		t.Fatalf("kind = %s, want start: %v", failure.Kind, failure)
	}
	// Windows 的 exec.LookPath 对不存在文件返回 *exec.Error（内部可能是
	// ErrNotFound），Unix 可能返回 *os.PathError；两者都证明底层启动错误
	// 仍经 Unwrap 可达，且默认摘要不泄露路径/原文。
	var pathErr *os.PathError
	var execErr *exec.Error
	if !errors.As(failure, &pathErr) && !errors.As(failure, &execErr) {
		t.Fatal("internal start error must stay reachable through Unwrap")
	}
	summary := failure.Error()
	if strings.Contains(summary, "private-origin-dir") || strings.Contains(summary, privateDir) {
		t.Fatalf("default summary leaked the full binary path: %s", summary)
	}
	if strings.Contains(summary, "no such file") {
		t.Fatalf("default summary leaked raw underlying error text: %s", summary)
	}
	if !strings.Contains(summary, "missing-media-tool") || !strings.Contains(summary, "kind=start") {
		t.Fatalf("summary missing safe binary basename/kind: %s", summary)
	}
}

func TestMediaCommandHelperHonorsCommandBudget(t *testing.T) {
	const budget = 300 * time.Millisecond
	started := time.Now()
	_, failure := runMediaCommandBounded(t, context.Background(),
		mediaCommandOptions{Stage: "helper_timeout", Budget: budget},
		os.Args[0], mediaCommandHelperArgs(mediaHelperModeSleep)...)
	elapsed := time.Since(started)
	if failure == nil {
		t.Fatal("expected the command budget to expire")
	}
	if failure.Kind != mediaCommandTimeout {
		t.Fatalf("kind = %s, want timeout: %v", failure.Kind, failure)
	}
	if failure.Stage != "helper_timeout" {
		t.Fatalf("stage = %q, want helper_timeout", failure.Stage)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("timeout was not bounded: elapsed=%s", elapsed)
	}
	if !strings.Contains(failure.Error(), "kind=timeout") || !strings.Contains(failure.Error(), "helper_timeout") {
		t.Fatalf("failure summary missing stage/timeout: %v", failure)
	}
}

func TestMediaCommandHelperHonorsCallerCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(300*time.Millisecond, cancel)
	defer timer.Stop()

	_, failure := runMediaCommandBounded(t, ctx,
		mediaCommandOptions{Stage: "helper_cancel"},
		os.Args[0], mediaCommandHelperArgs(mediaHelperModeSleep)...)
	if failure == nil {
		t.Fatal("expected cancellation to stop the command")
	}
	if failure.Kind != mediaCommandCanceled {
		t.Fatalf("kind = %s, want canceled: %v", failure.Kind, failure)
	}
	if !failure.hardFailure() {
		t.Fatalf("cancellation must not fall back to dummy/skip: %+v", failure)
	}
}

func TestMediaCommandHelperRejectsOversizedStdout(t *testing.T) {
	_, failure := runMediaCommandBounded(t, context.Background(),
		mediaCommandOptions{Stage: "helper_flood"},
		os.Args[0], mediaCommandHelperArgs(mediaHelperModeFlood)...)
	if failure == nil {
		t.Fatal("expected stdout over the limit to fail")
	}
	if failure.Kind != mediaCommandStdoutLimitExceeded {
		t.Fatalf("kind = %s, want stdout_limit: %v", failure.Kind, failure)
	}
	if !failure.hardFailure() {
		t.Fatalf("stdout overflow must not fall back to dummy/skip: %+v", failure)
	}
}

func TestMediaCommandHelperBoundsInheritedStdoutPipe(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pipe-grandchild-work-done")
	started := time.Now()
	_, failure := runMediaCommandBounded(t, context.Background(),
		mediaCommandOptions{Stage: "helper_pipe_hold"},
		os.Args[0], mediaCommandHelperArgs(mediaHelperModePipeHold, marker)...)
	elapsed := time.Since(started)

	markerDeadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(markerDeadline) {
			t.Fatalf("pipe grandchild did not complete self-limited work (marker %s missing); marker is work-done evidence, not process-exit evidence", marker)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if failure == nil {
		t.Fatal("expected inherited stdout pipe to hit WaitDelay")
	}
	if failure.Kind != mediaCommandPipeHeld {
		t.Fatalf("kind = %s, want wait_delay_pipe_held: %v", failure.Kind, failure)
	}
	if elapsed < mediaCommandWaitDelay/2 {
		t.Fatalf("WaitDelay returned implausibly early: elapsed=%s", elapsed)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("inherited pipe was not bounded: elapsed=%s", elapsed)
	}
}

// TestMediaCommandHelperOwnedPipeClosesWhenGrandchildExits 用测试自己持有的管道
// 确认继承 stdout 的孙进程在自限工作结束后释放写端：helper 启动后父测试关闭
// 自己的写端副本，只保留读端；读到 EOF 说明最后的写端持有者已关闭该 fd。
// 该证据只覆盖本测试自有 helper/孙进程的 fd 生命周期，不做 PID 操作，也不
// 宣称通用进程树回收；marker 仍只是“自限工作已完成”的协作证据。
func TestMediaCommandHelperOwnedPipeClosesWhenGrandchildExits(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pipe-grandchild-work-done")
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	// 在任何 spawn 之前设置读截止；平台不支持时先关闭两端再 skip，
	// 不留下自限孙进程活到临时目录删除之后。
	if err := readEnd.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		readEnd.Close()
		writeEnd.Close()
		t.Skipf("owned pipe read deadline unsupported on this platform: %v", err)
	}
	defer readEnd.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], mediaCommandHelperArgs(mediaHelperModePipeHold, marker)...)
	command.Stdout = writeEnd
	started := time.Now()
	if err := command.Start(); err != nil {
		writeEnd.Close()
		t.Fatalf("start pipe helper: %v", err)
	}
	// 父测试关闭自己的写端副本；此后只剩 helper（立即退出）与其孙进程持有写端。
	if err := writeEnd.Close(); err != nil {
		t.Fatalf("close parent write end: %v", err)
	}
	// 直接子进程（本测试自有的 helper）同步回收。
	if err := command.Wait(); err != nil {
		t.Fatalf("wait pipe helper: %v", err)
	}

	data, readErr := io.ReadAll(readEnd)
	elapsed := time.Since(started)
	if readErr != nil {
		t.Fatalf("owned pipe did not reach EOF: %v (elapsed=%s)", readErr, elapsed)
	}
	if got, want := string(data), "helper-stdout-before-pipe-hold"; got != want {
		t.Fatalf("owned pipe stdout = %q, want %q", got, want)
	}
	if elapsed < 2*time.Second {
		t.Fatalf("owned pipe EOF returned before the self-limited grandchild work could finish: %s", elapsed)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("self-limited work marker missing after owned pipe EOF: %v", err)
	}
}

func TestMediaCommandFailureHardness(t *testing.T) {
	cases := []struct {
		kind mediaCommandFailureKind
		hard bool
	}{
		{mediaCommandExitFailure, false},
		{mediaCommandStartFailure, false},
		{mediaCommandTimeout, true},
		{mediaCommandCanceled, true},
		{mediaCommandStdoutLimitExceeded, true},
		{mediaCommandPipeHeld, true},
		{mediaCommandBudgetExhausted, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			failure := &mediaCommandError{Kind: tc.kind}
			if got := failure.hardFailure(); got != tc.hard {
				t.Fatalf("hardFailure(%s) = %v, want %v", tc.kind, got, tc.hard)
			}
		})
	}
}
