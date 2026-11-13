//go:build windows

package system

import (
	"context"
	"math"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

var (
	memoryKernel32           = syscall.NewLazyDLL("kernel32.dll")
	procGlobalMemoryStatusEx = memoryKernel32.NewProc("GlobalMemoryStatusEx")
)

// probeHostInfo 在 Windows 读取宿主内存/CPU 名；WSL2 可用时用执行侧
// /proc/meminfo 覆盖内存数值，保持与 WSL CUDA 探测同一口径。
func probeHostInfo(ctx context.Context, cfg probeConfig, wsl *WSLStatus) HostInfo {
	host := HostInfo{
		CPUName: probeProcessorName(),
		Memory:  probeHostMemory(),
	}
	if wsl == nil || !wsl.Available || wsl.Version < 2 {
		return host
	}
	wslPath, err := cfg.lookPath("wsl.exe")
	if err != nil {
		return host
	}
	args := wslTargetArgs(cfg, "-e", "cat", "/proc/meminfo", "/proc/cpuinfo")
	out, err := runProbe(ctx, cfg, wslPath, args...)
	if err != nil {
		return host
	}
	// 两个解析器只认各自字段，拼接输出不会互相误匹配。
	if memory := parseLinuxMeminfo(out); memory.HasTotal && memory.HasUsed {
		host.Memory = memory
	}
	if name := parseProcCPUInfo(out); name != "" {
		host.CPUName = name
	}
	return host
}

func probeHostMemory() MemoryInfo {
	var status memoryStatusEx
	status.Length = uint32(unsafe.Sizeof(status))
	ok, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if ok == 0 || status.TotalPhys == 0 || status.TotalPhys > math.MaxInt64 {
		return MemoryInfo{}
	}
	// AvailPhys > TotalPhys 视为不可信，只返回总量；相等时占用为 0。
	var used uint64
	hasUsed := status.AvailPhys <= status.TotalPhys
	if hasUsed {
		used = status.TotalPhys - status.AvailPhys
	}
	if used > math.MaxInt64 {
		return MemoryInfo{TotalBytes: int64(status.TotalPhys), HasTotal: true}
	}
	return MemoryInfo{
		TotalBytes: int64(status.TotalPhys),
		UsedBytes:  int64(used),
		HasTotal:   true,
		HasUsed:    hasUsed,
	}
}

func probeProcessorName() string {
	key, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`HARDWARE\DESCRIPTION\System\CentralProcessor\0`,
		registry.QUERY_VALUE,
	)
	if err != nil {
		return ""
	}
	defer key.Close()
	name, _, err := key.GetStringValue("ProcessorNameString")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(name)
}
