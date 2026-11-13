package system

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// recordingCommandRunner 记录实际传给 Runner 的 argv，并允许注入结果。
type recordingCommandRunner struct {
	name   string
	args   []string
	called bool
	out    []byte
	err    error
	run    func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (r *recordingCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.called = true
	r.name = name
	r.args = append([]string(nil), args...)
	if r.run != nil {
		return r.run(ctx, name, args...)
	}
	return r.out, r.err
}

func wslProberForTest(t *testing.T, runner CommandRunner, opts WSLMediaProbeOptions) *WSLFFprobeProber {
	t.Helper()
	if opts.Distro == "" {
		opts.Distro = "yaomu"
	}
	if opts.FFprobePath == "" {
		opts.FFprobePath = "/usr/local/bin/ffprobe"
	}
	opts.Runner = runner
	opts.LookPath = func(string) (string, error) { return "/usr/bin/wsl.exe", nil }
	prober, err := NewWSLFFprobeProber(opts)
	if err != nil {
		t.Fatalf("construct WSL prober: %v", err)
	}
	return prober
}

func TestWindowsMediaPathToWSLMount(t *testing.T) {
	longDir := strings.Repeat(`长目录 with space\`, 300)
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"backslashes and non-ASCII", `C:\Users\张三\音频 文件\a.mp4`, "/mnt/c/Users/张三/音频 文件/a.mp4"},
		{"lowercase drive", `d:\media\a.wav`, "/mnt/d/media/a.wav"},
		{"forward slashes", "E:/media/a b.wav", "/mnt/e/media/a b.wav"},
		{"drive root", `C:\`, "/mnt/c/"},
		{"drive root forward slash", "C:/", "/mnt/c/"},
		{"long path", `C:\` + longDir + "a.wav", "/mnt/c/" + strings.ReplaceAll(longDir, `\`, "/") + "a.wav"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := windowsMediaPathToWSLMount(tc.in)
			if err != nil {
				t.Fatalf("map path: %v", err)
			}
			if got != tc.want {
				t.Fatalf("mapped path = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWindowsMediaPathToWSLMountRejectsUnsafe(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"unc backslash":    `\\server\share\a.wav`,
		"unc forward":      "//server/share/a.wav",
		"device namespace": `\\.\pipe\a.wav`,
		"relative":         `media\a.wav`,
		"dot relative":     `.\a.wav`,
		"drive relative":   `C:a.wav`,
		"drive only":       `C:`,
		"posix path":       "/tmp/a.wav",
		"newline":          "C:\\media\ninject.wav",
		"nul":              "C:\\media\x00inject.wav",
		"tab":              "C:\\media\tinject.wav",
		"escape":           "C:\\media\x1binject.wav",
		"delete":           "C:\\media\x7finject.wav",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := windowsMediaPathToWSLMount(path); !errors.Is(err, errUnsupportedWSLMediaPath) {
				t.Fatalf("error = %v, want errUnsupportedWSLMediaPath", err)
			}
		})
	}
}

func TestWSLFFprobeProberFixedArgv(t *testing.T) {
	runner := &recordingCommandRunner{out: []byte(videoProbeJSON)}
	prober := wslProberForTest(t, runner, WSLMediaProbeOptions{Distro: "yaomu", FFprobePath: "/usr/local/bin/ffprobe"})

	mediaPath := `C:\媒体 & 测试\a b.mp4`
	meta, err := prober.Probe(context.Background(), mediaPath)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if meta.DurationMs != 12345 || meta.AudioTracks != 2 || meta.Width != 1920 || meta.Height != 1080 {
		t.Fatalf("unexpected meta: %+v", meta)
	}
	if runner.name != "/usr/bin/wsl.exe" {
		t.Fatalf("runner name = %q", runner.name)
	}
	wantArgs := []string{
		"-d", "yaomu",
		"--", "/usr/local/bin/ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		"/mnt/c/媒体 & 测试/a b.mp4",
	}
	if len(runner.args) != len(wantArgs) {
		t.Fatalf("argv = %q, want %q", runner.args, wantArgs)
	}
	for i := range wantArgs {
		if runner.args[i] != wantArgs[i] {
			t.Fatalf("argv[%d] = %q, want %q (full: %q)", i, runner.args[i], wantArgs[i], runner.args)
		}
	}
	// 无 shell 包装：argv[0] 是解析后的 wsl.exe，媒体路径是单个参数。
	for _, arg := range runner.args {
		switch strings.ToLower(arg) {
		case "cmd", "cmd.exe", "sh", "bash", "/bin/sh", "/bin/bash", "-c", "-lc":
			t.Fatalf("argv contains a shell wrapper: %q", runner.args)
		}
	}
}

func TestWSLFFprobeProberUsesConfiguredExe(t *testing.T) {
	runner := &recordingCommandRunner{out: []byte(audioProbeJSON)}
	var lookedUp string
	prober, err := NewWSLFFprobeProber(WSLMediaProbeOptions{
		Distro:      "yaomu",
		FFprobePath: "/usr/local/bin/ffprobe",
		WSLExe:      `C:\Windows\System32\wsl.exe`,
		Runner:      runner,
		LookPath: func(name string) (string, error) {
			lookedUp = name
			return `C:\Windows\System32\wsl.exe`, nil
		},
	})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if _, err := prober.Probe(context.Background(), `D:\audio\tone.wav`); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if lookedUp != `C:\Windows\System32\wsl.exe` {
		t.Fatalf("LookPath name = %q", lookedUp)
	}
	if runner.name != `C:\Windows\System32\wsl.exe` {
		t.Fatalf("runner name = %q", runner.name)
	}
}

func TestWSLFFprobeProberUnavailableWhenWSLMissing(t *testing.T) {
	runner := &recordingCommandRunner{out: []byte(videoProbeJSON)}
	prober, err := NewWSLFFprobeProber(WSLMediaProbeOptions{
		Distro:      "yaomu",
		FFprobePath: "/usr/local/bin/ffprobe",
		Runner:      runner,
		LookPath:    func(string) (string, error) { return "", exec.ErrNotFound },
	})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	_, err = prober.Probe(context.Background(), `C:\media\a.wav`)
	if !errors.Is(err, ErrProberUnavailable) {
		t.Fatalf("error = %v, want ErrProberUnavailable", err)
	}
	if runner.called {
		t.Fatal("runner must not be called when wsl.exe is missing")
	}
	// 工具不可用优先于单个路径映射失败，避免把“没有 wsl.exe”误报成文件损坏。
	_, err = prober.Probe(context.Background(), "/tmp/a.wav")
	if !errors.Is(err, ErrProberUnavailable) {
		t.Fatalf("unsafe path with missing wsl.exe: error = %v, want ErrProberUnavailable", err)
	}
}

func TestWSLFFprobeProberClassifiesRunErrors(t *testing.T) {
	exeErr := &exec.Error{Name: "wsl.exe", Err: errors.New("permission denied")}
	cases := []struct {
		name        string
		runErr      error
		unavailable bool
	}{
		{"cannot start", exeErr, true},
		{"binary missing", exec.ErrNotFound, true},
		{"unknown failure", errors.New("synthetic"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &recordingCommandRunner{err: tc.runErr}
			prober := wslProberForTest(t, runner, WSLMediaProbeOptions{})
			_, err := prober.Probe(context.Background(), `C:\media\a.wav`)
			if tc.unavailable {
				if !errors.Is(err, ErrProberUnavailable) {
					t.Fatalf("error = %v, want ErrProberUnavailable", err)
				}
				return
			}
			if !errors.Is(err, ErrProbeFailed) {
				t.Fatalf("error = %v, want ErrProbeFailed", err)
			}
		})
	}
}

// TestWSLExitCodeHelperProcess 是被测代码的子进程入口：只有携带 helper 环境变量
// 时才退出，普通测试运行直接返回。用真实进程生成 *exec.ExitError，避免手造
// 无法复现的 ProcessState。
func TestWSLExitCodeHelperProcess(t *testing.T) {
	mode := os.Getenv("WSL_PROBE_HELPER_MODE")
	if mode == "" {
		return
	}
	switch mode {
	case "exit":
		code, err := strconv.Atoi(os.Getenv("WSL_PROBE_HELPER_CODE"))
		if err != nil {
			os.Exit(2)
		}
		os.Exit(code)
	case "wait-for-kill":
		// 由父进程终止；Unix 上被杀进程的 ExitCode() 为 -1。
		time.Sleep(time.Minute)
		os.Exit(0)
	default:
		os.Exit(3)
	}
}

// runWSLHelperExit 启动当前测试二进制并返回真实的 *exec.ExitError。
func runWSLHelperExit(t *testing.T, mode string, code int) *exec.ExitError {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=TestWSLExitCodeHelperProcess")
	cmd.Env = append(os.Environ(),
		"WSL_PROBE_HELPER_MODE="+mode,
		"WSL_PROBE_HELPER_CODE="+strconv.Itoa(code),
	)
	if mode == "wait-for-kill" {
		if err := cmd.Start(); err != nil {
			t.Fatalf("start helper: %v", err)
		}
		if err := cmd.Process.Kill(); err != nil {
			t.Fatalf("kill helper: %v", err)
		}
		err = cmd.Wait()
	} else {
		err = cmd.Run()
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("helper mode %q did not produce *exec.ExitError: %v", mode, err)
	}
	return exitErr
}

// runWSLHelperNegativeExit 生成 ExitCode() 为 -1/0xFFFFFFFF 的真实 ExitError：
// Windows 用 os.Exit(-1) 产生 0xFFFFFFFF，Unix 用信号终止产生 -1。
func runWSLHelperNegativeExit(t *testing.T) *exec.ExitError {
	t.Helper()
	if runtime.GOOS == "windows" {
		return runWSLHelperExit(t, "exit", -1)
	}
	return runWSLHelperExit(t, "wait-for-kill", 0)
}

// TestWSLFFprobeProberExitCodeClassification 覆盖 Windows 11/yaomu 实测退出码：
// 127（镜像内命令/工具不存在）与 -1（发行版不可用；Windows 0xFFFFFFFF 归一）
// 是工具不可用；其他非零（如损坏媒体退出 1）是文件探测失败。不解析 stderr。
func TestWSLFFprobeProberExitCodeClassification(t *testing.T) {
	cases := []struct {
		code        int
		unavailable bool
	}{
		{code: 127, unavailable: true},
		{code: 1, unavailable: false},
		{code: 2, unavailable: false},
	}
	for _, tc := range cases {
		t.Run("exit_"+strconv.Itoa(tc.code), func(t *testing.T) {
			exitErr := runWSLHelperExit(t, "exit", tc.code)
			if got := exitErr.ExitCode(); got != tc.code {
				t.Fatalf("helper exit code = %d, want %d", got, tc.code)
			}
			prober := wslProberForTest(t, &recordingCommandRunner{err: exitErr}, WSLMediaProbeOptions{})
			_, err := prober.Probe(context.Background(), `C:\media\a.wav`)
			if tc.unavailable {
				if !errors.Is(err, ErrProberUnavailable) {
					t.Fatalf("exit %d: error = %v, want ErrProberUnavailable", tc.code, err)
				}
				return
			}
			if !errors.Is(err, ErrProbeFailed) {
				t.Fatalf("exit %d: error = %v, want ErrProbeFailed", tc.code, err)
			}
		})
	}

	t.Run("exit_0xFFFFFFFF", func(t *testing.T) {
		exitErr := runWSLHelperNegativeExit(t)
		if code := exitErr.ExitCode(); code != -1 && uint32(code) != 0xFFFFFFFF {
			t.Fatalf("helper exit code = %d, want -1/0xFFFFFFFF", code)
		}
		prober := wslProberForTest(t, &recordingCommandRunner{err: exitErr}, WSLMediaProbeOptions{})
		_, err := prober.Probe(context.Background(), `C:\media\a.wav`)
		if !errors.Is(err, ErrProberUnavailable) {
			t.Fatalf("error = %v, want ErrProberUnavailable", err)
		}
	})
}

func TestWSLFFprobeProberTimeoutAndCancel(t *testing.T) {
	block := func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Run("timeout", func(t *testing.T) {
		runner := &recordingCommandRunner{run: block}
		prober := wslProberForTest(t, runner, WSLMediaProbeOptions{Timeout: 30 * time.Millisecond})
		started := time.Now()
		_, err := prober.Probe(context.Background(), `C:\media\a.wav`)
		elapsed := time.Since(started)
		if !errors.Is(err, ErrProbeFailed) {
			t.Fatalf("error = %v, want ErrProbeFailed", err)
		}
		if elapsed > 2*time.Second {
			t.Fatalf("timeout did not bound the probe: %s", elapsed)
		}
	})
	t.Run("caller cancel", func(t *testing.T) {
		runner := &recordingCommandRunner{run: block}
		prober := wslProberForTest(t, runner, WSLMediaProbeOptions{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := prober.Probe(ctx, `C:\media\a.wav`)
		if !errors.Is(err, ErrProbeFailed) {
			t.Fatalf("error = %v, want ErrProbeFailed", err)
		}
	})
	// exec.CommandContext 超时/取消会杀死进程并返回 ExitCode -1 的 *ExitError；
	// 这类由本次预算终止的失败必须仍归类 timeout，不能误报工具不可用。
	t.Run("context kill with negative exit stays timeout", func(t *testing.T) {
		exitErr := runWSLHelperNegativeExit(t)
		runner := &recordingCommandRunner{run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			<-ctx.Done()
			return nil, exitErr
		}}
		prober := wslProberForTest(t, runner, WSLMediaProbeOptions{Timeout: 20 * time.Millisecond})
		_, err := prober.Probe(context.Background(), `C:\media\a.wav`)
		if !errors.Is(err, ErrProbeFailed) {
			t.Fatalf("error = %v, want ErrProbeFailed (context termination is not unavailable)", err)
		}
	})
}

func TestWSLFFprobeProberOutputLimitAndInvalidJSON(t *testing.T) {
	t.Run("output limit", func(t *testing.T) {
		runner := &recordingCommandRunner{out: make([]byte, mediaProbeOutputLimit+1)}
		prober := wslProberForTest(t, runner, WSLMediaProbeOptions{})
		if _, err := prober.Probe(context.Background(), `C:\media\a.wav`); !errors.Is(err, ErrProbeFailed) {
			t.Fatalf("error = %v, want ErrProbeFailed", err)
		}
	})
	t.Run("invalid JSON", func(t *testing.T) {
		runner := &recordingCommandRunner{out: []byte("{not-json")}
		prober := wslProberForTest(t, runner, WSLMediaProbeOptions{})
		if _, err := prober.Probe(context.Background(), `C:\media\a.wav`); !errors.Is(err, ErrProbeFailed) {
			t.Fatalf("error = %v, want ErrProbeFailed", err)
		}
	})
}

func TestWSLFFprobeProberRejectsUnsafePathsWithoutRunning(t *testing.T) {
	cases := map[string]string{
		"empty":    "",
		"unc":      `\\server\share\a.wav`,
		"relative": `media\a.wav`,
		"posix":    "/tmp/a.wav",
		"control":  "C:\\media\ninject.wav",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			runner := &recordingCommandRunner{out: []byte(videoProbeJSON)}
			prober := wslProberForTest(t, runner, WSLMediaProbeOptions{})
			_, err := prober.Probe(context.Background(), path)
			if !errors.Is(err, ErrProbeFailed) {
				t.Fatalf("error = %v, want ErrProbeFailed", err)
			}
			if runner.called {
				t.Fatal("unsafe path must be rejected before running wsl.exe")
			}
		})
	}
}

// TestWSLFFprobeProberDoesNotEchoInput 断言错误信息不泄露宿主路径或 stderr。
func TestWSLFFprobeProberDoesNotEchoInput(t *testing.T) {
	const mediaPath = `C:\Users\secret-user\private-media.wav`
	runner := &recordingCommandRunner{err: errors.New("secret-stderr-marker")}
	prober := wslProberForTest(t, runner, WSLMediaProbeOptions{})
	_, err := prober.Probe(context.Background(), mediaPath)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, secret := range []string{"secret-user", "private-media", "secret-stderr-marker"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks %q: %v", secret, err)
		}
	}
}

func TestNewWSLFFprobeProberConfigValidation(t *testing.T) {
	validDistros := []string{
		"yaomu",
		"Ubuntu-22.04",
		"Ubuntu_22.04",
		"Ubuntu.22.04",
		"a",
		strings.Repeat("a", maxWSLDistroLength),
	}
	for _, distro := range validDistros {
		if _, err := NewWSLFFprobeProber(WSLMediaProbeOptions{
			Distro:      distro,
			FFprobePath: "/usr/local/bin/ffprobe",
		}); err != nil {
			t.Fatalf("distro %q: unexpected error %v", distro, err)
		}
	}
	invalidDistros := []string{
		"",
		"   ",
		"-yaomu",
		"bad distro",
		"bad\ndistro",
		"bad/distro",
		"中文",
		strings.Repeat("a", maxWSLDistroLength+1),
	}
	for _, distro := range invalidDistros {
		if _, err := NewWSLFFprobeProber(WSLMediaProbeOptions{
			Distro:      distro,
			FFprobePath: "/usr/local/bin/ffprobe",
		}); !errors.Is(err, ErrProberUnavailable) {
			t.Fatalf("distro %q: error = %v, want ErrProberUnavailable", distro, err)
		}
	}

	validPaths := []string{
		"/usr/local/bin/ffprobe",
		"/opt/工具/ffprobe",
		"/opt/with space/ffprobe",
		"/" + strings.Repeat("a", maxWSLToolPathLength-1),
	}
	for _, path := range validPaths {
		if _, err := NewWSLFFprobeProber(WSLMediaProbeOptions{
			Distro:      "yaomu",
			FFprobePath: path,
		}); err != nil {
			t.Fatalf("tool path %q: unexpected error %v", path, err)
		}
	}
	invalidPaths := []string{
		"",
		"   ",
		"usr/bin/ffprobe",
		`C:\ffprobe.exe`,
		"/usr/bin/ff\nprobe",
		"/usr/bin/ff\x00probe",
		"/usr/bin/ff\x1bprobe",
		"/" + strings.Repeat("a", maxWSLToolPathLength),
	}
	for _, path := range invalidPaths {
		if _, err := NewWSLFFprobeProber(WSLMediaProbeOptions{
			Distro:      "yaomu",
			FFprobePath: path,
		}); !errors.Is(err, ErrProberUnavailable) {
			t.Fatalf("tool path %q: error = %v, want ErrProberUnavailable", path, err)
		}
	}
}
