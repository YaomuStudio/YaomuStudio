//go:build !darwin && !linux && !windows

package system

import "context"

// probeHostInfo 在未适配平台返回空字段，避免探测链编译失败或伪造数值。
func probeHostInfo(_ context.Context, _ probeConfig, _ *WSLStatus) HostInfo {
	return HostInfo{}
}
