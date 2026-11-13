//go:build linux

package system

import (
	"context"
	"os"
)

// probeHostInfo 在 Linux 读取 /proc/meminfo 与 /proc/cpuinfo；只读文件
// 读取失败时返回空字段，不执行外部命令。
func probeHostInfo(_ context.Context, _ probeConfig, _ *WSLStatus) HostInfo {
	var host HostInfo
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		host.Memory = parseLinuxMeminfo(data)
	}
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		host.CPUName = parseProcCPUInfo(data)
	}
	return host
}
