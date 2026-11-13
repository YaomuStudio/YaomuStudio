package system

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const testGPUCSV = "NVIDIA GeForce RTX 3060, 555.99, 12288, 512\n"

type fakeResponse struct {
	out   []byte
	err   error
	block bool
}

type fakeRunner struct {
	mu        sync.Mutex
	responses map[string]fakeResponse
	calls     []string
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, key)
	resp, ok := f.responses[key]
	f.mu.Unlock()
	if !ok {
		return nil, errors.New("unexpected command: " + key)
	}
	if resp.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return resp.out, resp.err
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func lookPathFor(paths map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		if path, ok := paths[name]; ok {
			return path, nil
		}
		return "", errors.New("not found")
	}
}

func hasCheck(checks []Check, code, severity string) bool {
	for _, check := range checks {
		if check.Code == code && check.Severity == severity {
			return true
		}
	}
	return false
}

func TestProbeWindowsWithWSLCUDA(t *testing.T) {
	const (
		nvidia = `C:\Windows\System32\nvidia-smi.exe`
		wsl    = `C:\Windows\System32\wsl.exe`
	)
	runner := &fakeRunner{responses: map[string]fakeResponse{
		nvidia + " " + strings.Join(nvidiaSMIQueryArgs(), " "): {out: []byte(testGPUCSV)},
		wsl + " -e uname -r": {out: []byte("5.15.153.1-microsoft-standard-WSL2\n")},
		wsl + " -e nvidia-smi " + strings.Join(nvidiaSMIQueryArgs(), " "): {out: []byte(testGPUCSV)},
	}}
	snapshot := ProbeEnvironment(context.Background(), ProbeOptions{
		Platform: "windows",
		Runner:   runner,
		LookPath: lookPathFor(map[string]string{
			"nvidia-smi": nvidia,
			"wsl.exe":    wsl,
		}),
	})

	if snapshot.Platform != "windows" {
		t.Fatalf("platform = %q", snapshot.Platform)
	}
	if snapshot.WSL == nil || !snapshot.WSL.Available || snapshot.WSL.Version != 2 {
		t.Fatalf("WSL = %+v, want available v2", snapshot.WSL)
	}
	if snapshot.WSL.CUDAAvailable == nil || !*snapshot.WSL.CUDAAvailable {
		t.Fatalf("WSL CUDA = %v, want true", snapshot.WSL.CUDAAvailable)
	}
	if len(snapshot.GPUs) != 1 {
		t.Fatalf("GPUs = %+v, want 1", snapshot.GPUs)
	}
	gpu := snapshot.GPUs[0]
	if gpu.Name != "NVIDIA GeForce RTX 3060" || gpu.DriverVersion != "555.99" {
		t.Fatalf("GPU = %+v", gpu)
	}
	if gpu.MemoryTotalBytes != 12288*1024*1024 || gpu.MemoryUsedBytes != 512*1024*1024 {
		t.Fatalf("GPU memory = %d/%d", gpu.MemoryUsedBytes, gpu.MemoryTotalBytes)
	}
	if !hasCheck(snapshot.Checks, "WSL2_AVAILABLE", CheckPass) {
		t.Fatalf("checks = %+v, want WSL2 pass", snapshot.Checks)
	}
	if !hasCheck(snapshot.Checks, "WSL_CUDA_AVAILABLE", CheckPass) {
		t.Fatalf("checks = %+v, want WSL CUDA pass", snapshot.Checks)
	}
	if !hasCheck(snapshot.Checks, "CUDA_AVAILABLE", CheckPass) {
		t.Fatalf("checks = %+v, want CUDA pass", snapshot.Checks)
	}
}

func TestProbeWindowsWithoutWSL(t *testing.T) {
	const nvidia = `C:\Windows\System32\nvidia-smi.exe`
	runner := &fakeRunner{responses: map[string]fakeResponse{
		nvidia + " " + strings.Join(nvidiaSMIQueryArgs(), " "): {out: []byte(testGPUCSV)},
	}}
	snapshot := ProbeEnvironment(context.Background(), ProbeOptions{
		Platform: "windows",
		Runner:   runner,
		LookPath: lookPathFor(map[string]string{"nvidia-smi": nvidia}),
	})

	if snapshot.WSL == nil || snapshot.WSL.Available {
		t.Fatalf("WSL = %+v, want unavailable", snapshot.WSL)
	}
	if len(snapshot.GPUs) != 1 {
		t.Fatalf("GPUs = %+v, want host GPU still visible", snapshot.GPUs)
	}
	if !hasCheck(snapshot.Checks, "WSL2_AVAILABLE", CheckBlocker) {
		t.Fatalf("checks = %+v, want WSL blocker", snapshot.Checks)
	}
	if !hasCheck(snapshot.Checks, "CUDA_AVAILABLE", CheckPass) {
		t.Fatalf("checks = %+v, want CUDA pass", snapshot.Checks)
	}
}

func TestProbeWindowsWSLWithoutCUDA(t *testing.T) {
	const (
		nvidia = `C:\Windows\System32\nvidia-smi.exe`
		wsl    = `C:\Windows\System32\wsl.exe`
	)
	runner := &fakeRunner{responses: map[string]fakeResponse{
		nvidia + " " + strings.Join(nvidiaSMIQueryArgs(), " "): {out: []byte(testGPUCSV)},
		wsl + " -e uname -r": {out: []byte("5.15.153.1-microsoft-standard-WSL2\n")},
		wsl + " -e nvidia-smi " + strings.Join(nvidiaSMIQueryArgs(), " "): {err: errors.New("nvidia-smi not found")},
	}}
	snapshot := ProbeEnvironment(context.Background(), ProbeOptions{
		Platform: "windows",
		Runner:   runner,
		LookPath: lookPathFor(map[string]string{"nvidia-smi": nvidia, "wsl.exe": wsl}),
	})

	if snapshot.WSL == nil || snapshot.WSL.CUDAAvailable == nil || *snapshot.WSL.CUDAAvailable {
		t.Fatalf("WSL = %+v, want CUDA false", snapshot.WSL)
	}
	if len(snapshot.GPUs) != 1 || snapshot.GPUs[0].Name != "NVIDIA GeForce RTX 3060" {
		t.Fatalf("GPUs = %+v, want host fallback", snapshot.GPUs)
	}
	if !hasCheck(snapshot.Checks, "WSL_CUDA_AVAILABLE", CheckWarning) {
		t.Fatalf("checks = %+v, want WSL CUDA warning", snapshot.Checks)
	}
}

func TestProbeWindowsWSLNvidiaSMIFallback(t *testing.T) {
	const (
		nvidia = `C:\Windows\System32\nvidia-smi.exe`
		wsl    = `C:\Windows\System32\wsl.exe`
	)
	runner := &fakeRunner{responses: map[string]fakeResponse{
		nvidia + " " + strings.Join(nvidiaSMIQueryArgs(), " "): {out: []byte(testGPUCSV)},
		wsl + " -e uname -r": {out: []byte("6.18.54.1-microsoft-standard-WSL2\n")},
		wsl + " -e nvidia-smi " + strings.Join(nvidiaSMIQueryArgs(), " "): {
			err: errors.New("execvpe(nvidia-smi) failed"),
		},
		wsl + " -e /usr/lib/wsl/lib/nvidia-smi " + strings.Join(nvidiaSMIQueryArgs(), " "): {
			out: []byte(testGPUCSV),
		},
	}}
	snapshot := ProbeEnvironment(context.Background(), ProbeOptions{
		Platform: "windows",
		Runner:   runner,
		LookPath: lookPathFor(map[string]string{"nvidia-smi": nvidia, "wsl.exe": wsl}),
	})

	if snapshot.WSL == nil || snapshot.WSL.CUDAAvailable == nil || !*snapshot.WSL.CUDAAvailable {
		t.Fatalf("WSL = %+v, want CUDA true via fixed path", snapshot.WSL)
	}
	if len(snapshot.GPUs) != 1 || snapshot.GPUs[0].Name != "NVIDIA GeForce RTX 3060" {
		t.Fatalf("GPUs = %+v", snapshot.GPUs)
	}
	if !hasCheck(snapshot.Checks, "WSL_CUDA_AVAILABLE", CheckPass) {
		t.Fatalf("checks = %+v, want WSL CUDA pass", snapshot.Checks)
	}
}

func TestProbeWindowsWSLPartialGPUParse(t *testing.T) {
	const (
		nvidia = `C:\Windows\System32\nvidia-smi.exe`
		wsl    = `C:\Windows\System32\wsl.exe`
	)
	runner := &fakeRunner{responses: map[string]fakeResponse{
		nvidia + " " + strings.Join(nvidiaSMIQueryArgs(), " "): {out: []byte(testGPUCSV)},
		wsl + " -e uname -r": {out: []byte("6.18.54.1-microsoft-standard-WSL2\n")},
		wsl + " -e nvidia-smi " + strings.Join(nvidiaSMIQueryArgs(), " "): {
			out: []byte(testGPUCSV + "bad-row\n"),
		},
	}}
	snapshot := ProbeEnvironment(context.Background(), ProbeOptions{
		Platform: "windows",
		Runner:   runner,
		LookPath: lookPathFor(map[string]string{"nvidia-smi": nvidia, "wsl.exe": wsl}),
	})

	if snapshot.WSL == nil || snapshot.WSL.CUDAAvailable == nil || !*snapshot.WSL.CUDAAvailable {
		t.Fatalf("WSL = %+v, want CUDA true with partial rows", snapshot.WSL)
	}
	if !hasCheck(snapshot.Checks, "WSL_CUDA_PROBE_PARTIAL", CheckWarning) {
		t.Fatalf("checks = %+v, want WSL CUDA partial warning", snapshot.Checks)
	}
	if hasCheck(snapshot.Checks, "WSL_CUDA_PROBE_FAILED", CheckWarning) {
		t.Fatalf("checks = %+v, partial parse must not be reported as total failure", snapshot.Checks)
	}
}

func TestProbeNoGPU(t *testing.T) {
	runner := &fakeRunner{}
	snapshot := ProbeEnvironment(context.Background(), ProbeOptions{
		Platform: "windows",
		Runner:   runner,
		LookPath: lookPathFor(nil),
	})
	if len(snapshot.GPUs) != 0 {
		t.Fatalf("GPUs = %+v, want none", snapshot.GPUs)
	}
	if !hasCheck(snapshot.Checks, "CUDA_AVAILABLE", CheckWarning) {
		t.Fatalf("checks = %+v, want CUDA warning", snapshot.Checks)
	}
	if runner.callCount() != 0 {
		t.Fatalf("runner calls = %v, want none when commands are missing", runner.calls)
	}
}

func TestProbeDarwinDoesNotRunNvidia(t *testing.T) {
	runner := &fakeRunner{}
	var lookedUp []string
	snapshot := ProbeEnvironment(context.Background(), ProbeOptions{
		Platform: "darwin",
		Runner:   runner,
		LookPath: func(name string) (string, error) {
			lookedUp = append(lookedUp, name)
			return "", errors.New("not found")
		},
	})
	if snapshot.Platform != "darwin" || len(snapshot.GPUs) != 0 || snapshot.WSL != nil {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	for _, name := range lookedUp {
		if name == "nvidia-smi" {
			t.Fatalf("nvidia-smi must not be probed on darwin; looked up %v", lookedUp)
		}
	}
	if hasCheck(snapshot.Checks, "CUDA_AVAILABLE", CheckWarning) ||
		hasCheck(snapshot.Checks, "CUDA_AVAILABLE", CheckPass) {
		t.Fatalf("checks = %+v, CUDA check must not apply on darwin", snapshot.Checks)
	}
	if runner.callCount() != 0 {
		t.Fatalf("runner calls = %v, want none", runner.calls)
	}
}

func TestProbePartialGPUParse(t *testing.T) {
	out := testGPUCSV + "bad-row\n"
	gpus, err := parseNvidiaSMICSV([]byte(out))
	if err == nil {
		t.Fatal("expected partial parse error")
	}
	if len(gpus) != 1 {
		t.Fatalf("gpus = %+v, want 1 valid row", gpus)
	}
}

func TestParseGPUNameWithComma(t *testing.T) {
	gpus, err := parseNvidiaSMICSV([]byte("NVIDIA A100, SXM4, 555.99, 40960, 1024\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(gpus) != 1 {
		t.Fatalf("gpus = %+v", gpus)
	}
	if gpus[0].Name != "NVIDIA A100, SXM4" || gpus[0].DriverVersion != "555.99" {
		t.Fatalf("gpu = %+v", gpus[0])
	}
	if gpus[0].MemoryTotalBytes != 40960*1024*1024 {
		t.Fatalf("memory = %d", gpus[0].MemoryTotalBytes)
	}
}

func TestParseMultipleGPU(t *testing.T) {
	out := testGPUCSV + "NVIDIA GeForce RTX 4090, 555.99, 24576, 0\n"
	gpus, err := parseNvidiaSMICSV([]byte(out))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(gpus) != 2 {
		t.Fatalf("gpus = %+v, want 2", gpus)
	}
	if gpus[1].Name != "NVIDIA GeForce RTX 4090" || gpus[1].MemoryTotalBytes != 24576*1024*1024 {
		t.Fatalf("second gpu = %+v", gpus[1])
	}
}

func TestProbeCommandTimeout(t *testing.T) {
	const nvidia = "/usr/bin/nvidia-smi"
	runner := &fakeRunner{responses: map[string]fakeResponse{
		nvidia + " " + strings.Join(nvidiaSMIQueryArgs(), " "): {block: true},
	}}
	start := time.Now()
	snapshot := ProbeEnvironment(context.Background(), ProbeOptions{
		Platform:       "linux",
		Runner:         runner,
		LookPath:       lookPathFor(map[string]string{"nvidia-smi": nvidia}),
		CommandTimeout: 10 * time.Millisecond,
	})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("probe took %s, want timeout bound", elapsed)
	}
	if len(snapshot.GPUs) != 0 {
		t.Fatalf("GPUs = %+v, want none", snapshot.GPUs)
	}
	if !hasCheck(snapshot.Checks, "GPU_PROBE_FAILED", CheckWarning) {
		t.Fatalf("checks = %+v, want probe failure warning", snapshot.Checks)
	}
}

func TestWSLRuntimeCheck(t *testing.T) {
	ready := true
	checks := buildEnvironmentChecks("windows", EnvironmentSnapshot{}, nil, nil, ProbeOptions{
		WSLRuntimeReady: &ready,
	})
	if !hasCheck(checks, "WSL_RUNTIME_READY", CheckPass) {
		t.Fatalf("checks = %+v, want runtime pass", checks)
	}

	notReady := false
	checks = buildEnvironmentChecks("windows", EnvironmentSnapshot{}, nil, nil, ProbeOptions{
		WSLRuntimeReady: &notReady,
	})
	if !hasCheck(checks, "WSL_RUNTIME_READY", CheckBlocker) {
		t.Fatalf("checks = %+v, want runtime blocker", checks)
	}
}

func TestNoHostFFmpegChecks(t *testing.T) {
	checks := buildEnvironmentChecks("linux", EnvironmentSnapshot{}, nil, nil, ProbeOptions{})
	for _, check := range checks {
		if strings.HasPrefix(check.Code, "FFMPEG_") || strings.HasPrefix(check.Code, "FFPROBE_") {
			t.Fatalf("unexpected host media check: %+v", check)
		}
	}
}

func TestHostMetricsCheckOnlyAfterRealProbe(t *testing.T) {
	snapshot := EnvironmentSnapshot{Host: HostInfo{}}
	checks := buildEnvironmentChecks(runtime.GOOS, snapshot, nil, nil, ProbeOptions{})
	if !hasCheck(checks, "HOST_METRICS_UNAVAILABLE", CheckWarning) {
		t.Fatalf("checks = %+v, want HOST_METRICS_UNAVAILABLE", checks)
	}

	// 平台覆盖只用于测试其他分支，没有探测本机指标时不应产生本机告警。
	simulated := "plan9"
	if simulated == runtime.GOOS {
		simulated = "windows"
	}
	checks = buildEnvironmentChecks(simulated, snapshot, nil, nil, ProbeOptions{})
	if hasCheck(checks, "HOST_METRICS_UNAVAILABLE", CheckWarning) {
		t.Fatalf("checks = %+v, simulated platform must not report host metrics", checks)
	}

	complete := EnvironmentSnapshot{Host: HostInfo{
		CPUName: "CPU",
		Memory:  MemoryInfo{TotalBytes: 1, UsedBytes: 1, HasTotal: true, HasUsed: true},
	}}
	checks = buildEnvironmentChecks(runtime.GOOS, complete, nil, nil, ProbeOptions{})
	if hasCheck(checks, "HOST_METRICS_UNAVAILABLE", CheckWarning) {
		t.Fatalf("checks = %+v, complete host metrics must not warn", checks)
	}
}

func TestWSLProbeUsesConfiguredDistro(t *testing.T) {
	const (
		nvidia = `C:\Windows\System32\nvidia-smi.exe`
		wsl    = `C:\Windows\System32\wsl.exe`
	)
	runner := &fakeRunner{responses: map[string]fakeResponse{
		nvidia + " " + strings.Join(nvidiaSMIQueryArgs(), " "):                     {out: []byte(testGPUCSV)},
		wsl + " -d yaomu -e uname -r":                                              {out: []byte("5.15.153.1-microsoft-standard-WSL2\n")},
		wsl + " -d yaomu -e nvidia-smi " + strings.Join(nvidiaSMIQueryArgs(), " "): {out: []byte(testGPUCSV)},
	}}
	snapshot := ProbeEnvironment(context.Background(), ProbeOptions{
		Platform:       "windows",
		Runner:         runner,
		LookPath:       lookPathFor(map[string]string{"nvidia-smi": nvidia, "wsl.exe": wsl}),
		CommandTimeout: time.Second,
		WSLDistro:      "yaomu",
	})
	if snapshot.WSL == nil || !snapshot.WSL.Available || snapshot.WSL.Version != 2 {
		t.Fatalf("WSL = %+v", snapshot.WSL)
	}
}
