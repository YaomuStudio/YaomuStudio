package system

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	probeCommandTimeout = 4 * time.Second
	probeTotalTimeout   = 6 * time.Second
	probeOutputLimit    = 64 * 1024
)

// 环境检查严重级别；与 domain.PreflightSeverity 的取值保持一致。
const (
	CheckPass    = "pass"
	CheckWarning = "warning"
	CheckBlocker = "blocker"
)

var (
	errProbeUnavailable = errors.New("probe command unavailable")
	errProbeOutputLimit = errors.New("probe output exceeds limit")
)

// CommandRunner 执行固定命令；实现不得经过 shell，也不得拼接任意用户输入。
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ProbeOptions 是环境探测的可注入依赖；测试可替换平台、命令执行与路径查找。
type ProbeOptions struct {
	Platform       string
	Runner         CommandRunner
	LookPath       func(string) (string, error)
	CommandTimeout time.Duration
	TotalTimeout   time.Duration
	// WSLDistro 是执行侧镜像发行版；为空时使用 WSL 默认发行版。
	WSLDistro string
	// WSLRuntimeReady 由桌面壳执行镜像自带 check-runtime 后传入；nil 表示未探测。
	// 客户端不再检查宿主 FFmpeg，媒体工具由镜像提供。
	WSLRuntimeReady *bool
}

// GPUInfo 是 NVIDIA 探测结果；内存单位为字节。
type GPUInfo struct {
	Name             string
	DriverVersion    string
	MemoryTotalBytes int64
	MemoryUsedBytes  int64
}

// WSLStatus 是 Windows 宿主上的 WSL 探测结果。
type WSLStatus struct {
	Available     bool
	Version       int
	CUDAAvailable *bool
}

// Check 是环境检查项；message_key 由 UI 本地化。
type Check struct {
	Code       string
	Severity   string
	MessageKey string
	ActionIDs  []string
}

// EnvironmentSnapshot 是一次只读探测的快照。
type EnvironmentSnapshot struct {
	Platform string
	WSL      *WSLStatus
	GPUs     []GPUInfo
	Host     HostInfo
	Checks   []Check
}

type probeConfig struct {
	runner    CommandRunner
	lookPath  func(string) (string, error)
	timeout   time.Duration
	wslDistro string
}

// ProbeEnvironment 探测本机执行环境；命令失败、超时与解析失败都转成检查项，
// 不返回致命错误，也不伪造设备。
func ProbeEnvironment(ctx context.Context, opts ProbeOptions) EnvironmentSnapshot {
	platform := opts.Platform
	if platform == "" {
		platform = runtime.GOOS
	}
	timeout := opts.CommandTimeout
	if timeout <= 0 {
		timeout = probeCommandTimeout
	}
	totalTimeout := opts.TotalTimeout
	if totalTimeout <= 0 {
		totalTimeout = probeTotalTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()
	ctx = probeCtx
	cfg := probeConfig{
		runner:    opts.Runner,
		lookPath:  opts.LookPath,
		timeout:   timeout,
		wslDistro: strings.TrimSpace(opts.WSLDistro),
	}
	if cfg.runner == nil {
		cfg.runner = ExecRunner{}
	}
	if cfg.lookPath == nil {
		cfg.lookPath = exec.LookPath
	}

	snapshot := EnvironmentSnapshot{Platform: platform}
	var gpuErr error
	var wslCUDAErr error

	switch platform {
	case "windows":
		// 宿主 GPU 与 WSL 状态互不依赖，并发探测；WSL CUDA 依赖 WSL 状态。
		type gpuResult struct {
			gpus []GPUInfo
			err  error
		}
		hostCh := make(chan gpuResult, 1)
		wslCh := make(chan *WSLStatus, 1)
		go func() {
			gpus, err := probeNvidiaSMI(ctx, cfg)
			hostCh <- gpuResult{gpus: gpus, err: err}
		}()
		go func() {
			wslCh <- probeWSL(ctx, cfg)
		}()

		host := <-hostCh
		snapshot.WSL = <-wslCh
		gpuErr = host.err
		snapshot.GPUs = host.gpus
		if snapshot.WSL != nil && snapshot.WSL.Available {
			wslGPUs, err := probeWSLNvidiaSMI(ctx, cfg)
			wslCUDAErr = err
			cuda := len(wslGPUs) > 0
			snapshot.WSL.CUDAAvailable = &cuda
			if cuda {
				snapshot.GPUs = wslGPUs
				// 执行侧 GPU 已确认可用；宿主探测失败不再是有效阻断。
				gpuErr = nil
			}
		}
	case "linux":
		gpus, err := probeNvidiaSMI(ctx, cfg)
		snapshot.GPUs = gpus
		gpuErr = err
	case "darwin":
		// macOS 由 service 追加 MPS；不调用 NVIDIA 命令。
	}

	// 平台覆盖只用于测试其他平台的 GPU/WSL 分支；本机指标必须与运行时平台一致，
	// 避免在模拟平台上读取错误的本机信息。
	if platform == runtime.GOOS {
		snapshot.Host = probeHostInfo(ctx, cfg, snapshot.WSL)
	}
	snapshot.Checks = buildEnvironmentChecks(platform, snapshot, gpuErr, wslCUDAErr, opts)
	return snapshot
}

// probeNvidiaSMI 在宿主执行 nvidia-smi；路径不存在时返回 errProbeUnavailable。
func probeNvidiaSMI(ctx context.Context, cfg probeConfig) ([]GPUInfo, error) {
	path, err := cfg.lookPath("nvidia-smi")
	if err != nil {
		return nil, errProbeUnavailable
	}
	out, err := runProbe(ctx, cfg, path, nvidiaSMIQueryArgs()...)
	if err != nil {
		return nil, err
	}
	return parseNvidiaSMICSV(out)
}

// wslTargetArgs 在配置了镜像发行版时固定探测目标，避免落到用户默认发行版。
func wslTargetArgs(cfg probeConfig, args ...string) []string {
	if cfg.wslDistro == "" {
		return args
	}
	return append([]string{"-d", cfg.wslDistro}, args...)
}

// probeWSL 通过 uname 判断指定的 WSL 镜像是否可用及主版本；失败视为不可用，不阻塞应用。
func probeWSL(ctx context.Context, cfg probeConfig) *WSLStatus {
	path, err := cfg.lookPath("wsl.exe")
	if err != nil {
		return &WSLStatus{Available: false}
	}
	out, err := runProbe(ctx, cfg, path, wslTargetArgs(cfg, "-e", "uname", "-r")...)
	if err != nil {
		return &WSLStatus{Available: false}
	}
	kernel := strings.ToLower(strings.TrimSpace(string(out)))
	version := 0
	switch {
	case strings.Contains(kernel, "wsl2"), strings.Contains(kernel, "microsoft-standard"):
		version = 2
	case kernel != "":
		version = 1
	}
	return &WSLStatus{Available: true, Version: version}
}

// probeWSLNvidiaSMI 在默认 WSL 发行版内执行 nvidia-smi，验证 CUDA 透传。
// 部分发行版只把工具放在 /usr/lib/wsl/lib/nvidia-smi，因此先试 PATH，再试固定路径。
func probeWSLNvidiaSMI(ctx context.Context, cfg probeConfig) ([]GPUInfo, error) {
	path, err := cfg.lookPath("wsl.exe")
	if err != nil {
		return nil, errProbeUnavailable
	}
	gpus, directErr := runWSLNvidiaSMI(ctx, cfg, path, "nvidia-smi")
	if len(gpus) > 0 {
		return gpus, directErr
	}
	fallbackGPUs, fallbackErr := runWSLNvidiaSMI(ctx, cfg, path, "/usr/lib/wsl/lib/nvidia-smi")
	if len(fallbackGPUs) > 0 {
		return fallbackGPUs, fallbackErr
	}
	if directErr != nil {
		return nil, directErr
	}
	return nil, fallbackErr
}

func runWSLNvidiaSMI(ctx context.Context, cfg probeConfig, wslPath, smiPath string) ([]GPUInfo, error) {
	args := wslTargetArgs(cfg, "-e", smiPath)
	args = append(args, nvidiaSMIQueryArgs()...)
	out, err := runProbe(ctx, cfg, wslPath, args...)
	if err != nil {
		return nil, err
	}
	return parseNvidiaSMICSV(out)
}

func nvidiaSMIQueryArgs() []string {
	return []string{
		"--query-gpu=name,driver_version,memory.total,memory.used",
		"--format=csv,noheader,nounits",
	}
}

func runProbe(ctx context.Context, cfg probeConfig, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	out, err := cfg.runner.Run(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	if len(out) > probeOutputLimit {
		return nil, errProbeOutputLimit
	}
	return out, nil
}

// parseNvidiaSMICSV 解析 nvidia-smi CSV；部分行损坏时保留有效行并返回错误，
// 让调用方既能展示设备，也能报告 partial warning。
func parseNvidiaSMICSV(out []byte) ([]GPUInfo, error) {
	lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	gpus := make([]GPUInfo, 0, len(lines))
	invalid := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 4 {
			invalid++
			continue
		}
		// 从右解析固定列，兼容显卡名中可能出现的逗号。
		usedField := fields[len(fields)-1]
		totalField := fields[len(fields)-2]
		driver := strings.TrimSpace(fields[len(fields)-3])
		name := strings.TrimSpace(strings.Join(fields[:len(fields)-3], ","))
		totalMiB, totalErr := parseMiB(totalField)
		usedMiB, usedErr := parseMiB(usedField)
		if name == "" || totalErr != nil || usedErr != nil || totalMiB < 0 || usedMiB < 0 {
			invalid++
			continue
		}
		gpus = append(gpus, GPUInfo{
			Name:             name,
			DriverVersion:    driver,
			MemoryTotalBytes: totalMiB * 1024 * 1024,
			MemoryUsedBytes:  usedMiB * 1024 * 1024,
		})
	}
	if len(gpus) == 0 {
		return nil, fmt.Errorf("no valid GPU rows")
	}
	if invalid > 0 {
		return gpus, fmt.Errorf("%d invalid GPU rows", invalid)
	}
	return gpus, nil
}

func parseMiB(value string) (int64, error) {
	value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "MiB"))
	return strconv.ParseInt(value, 10, 64)
}

func buildEnvironmentChecks(
	platform string,
	snapshot EnvironmentSnapshot,
	gpuErr error,
	wslCUDAErr error,
	opts ProbeOptions,
) []Check {
	checks := make([]Check, 0, 6)

	if platform == "windows" {
		if snapshot.WSL != nil && snapshot.WSL.Available && snapshot.WSL.Version >= 2 {
			checks = append(checks, newCheck("WSL2_AVAILABLE", CheckPass, "environment.wsl_ready"))
		} else {
			checks = append(checks, newCheck("WSL2_AVAILABLE", CheckBlocker, "environment.wsl_unavailable", "install_wsl"))
		}
		if opts.WSLRuntimeReady != nil {
			if *opts.WSLRuntimeReady {
				checks = append(checks, newCheck("WSL_RUNTIME_READY", CheckPass, "environment.wsl_runtime_ready"))
			} else {
				checks = append(checks, newCheck(
					"WSL_RUNTIME_READY",
					CheckBlocker,
					"environment.wsl_runtime_unavailable",
					"recheck_environment",
				))
			}
		}
		if snapshot.WSL != nil && snapshot.WSL.Available {
			if snapshot.WSL.CUDAAvailable != nil && *snapshot.WSL.CUDAAvailable {
				checks = append(checks, newCheck("WSL_CUDA_AVAILABLE", CheckPass, "environment.wsl_cuda_ready"))
			} else {
				checks = append(checks, newCheck("WSL_CUDA_AVAILABLE", CheckWarning, "environment.wsl_cuda_unavailable", "install_gpu_driver"))
			}
		}
		if wslCUDAErr != nil && !errors.Is(wslCUDAErr, errProbeUnavailable) {
			if snapshot.WSL != nil && snapshot.WSL.CUDAAvailable != nil && *snapshot.WSL.CUDAAvailable {
				checks = append(checks, newCheck("WSL_CUDA_PROBE_PARTIAL", CheckWarning, "environment.wsl_cuda_probe_partial"))
			} else {
				checks = append(checks, newCheck("WSL_CUDA_PROBE_FAILED", CheckWarning, "environment.wsl_cuda_probe_failed"))
			}
		}
	}

	if platform != "darwin" {
		if len(snapshot.GPUs) > 0 {
			checks = append(checks, newCheck("CUDA_AVAILABLE", CheckPass, "environment.cuda_ready"))
		} else {
			checks = append(checks, newCheck("CUDA_AVAILABLE", CheckWarning, "environment.cuda_unavailable"))
		}
		if gpuErr != nil && !errors.Is(gpuErr, errProbeUnavailable) {
			if len(snapshot.GPUs) > 0 {
				checks = append(checks, newCheck("GPU_PROBE_PARTIAL", CheckWarning, "environment.gpu_probe_partial"))
			} else {
				checks = append(checks, newCheck("GPU_PROBE_FAILED", CheckWarning, "environment.gpu_probe_failed"))
			}
		}
	}

	// 只有真实探测过本机指标的平台才报告缺失；测试里的平台覆盖不应产生本机告警。
	if platform == runtime.GOOS &&
		(snapshot.Host.CPUName == "" || !snapshot.Host.Memory.HasTotal || !snapshot.Host.Memory.HasUsed) {
		checks = append(checks, newCheck(
			"HOST_METRICS_UNAVAILABLE",
			CheckWarning,
			"environment.host_metrics_unavailable",
		))
	}

	return checks
}

func newCheck(code, severity, messageKey string, actionIDs ...string) Check {
	if actionIDs == nil {
		actionIDs = []string{}
	}
	return Check{
		Code:       code,
		Severity:   severity,
		MessageKey: messageKey,
		ActionIDs:  actionIDs,
	}
}
