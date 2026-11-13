//go:build darwin

package system

import (
	"context"
	"strconv"
	"strings"
)

// probeHostInfo 在 macOS 读取内存总量、已用估算与 CPU/Apple 芯片名。
// 命令固定、不经过 shell；单项失败只让对应字段缺失。
func probeHostInfo(ctx context.Context, cfg probeConfig, _ *WSLStatus) HostInfo {
	var host HostInfo
	if sysctl, err := cfg.lookPath("sysctl"); err == nil {
		if out, err := runProbe(ctx, cfg, sysctl, "-n", "hw.memsize"); err == nil {
			if total, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil && total > 0 {
				host.Memory.TotalBytes = total
				host.Memory.HasTotal = true
			}
		}
		if out, err := runProbe(ctx, cfg, sysctl, "-n", "machdep.cpu.brand_string"); err == nil {
			host.CPUName = strings.TrimSpace(string(out))
		}
	}
	if vmStat, err := cfg.lookPath("vm_stat"); err == nil {
		if out, err := runProbe(ctx, cfg, vmStat); err == nil {
			if used, ok := parseVMStat(out); ok {
				if host.Memory.HasTotal && used > host.Memory.TotalBytes {
					used = host.Memory.TotalBytes
				}
				host.Memory.UsedBytes = used
				host.Memory.HasUsed = true
			}
		}
	}
	return host
}
