//go:build !windows

package system

import "syscall"

// Statfs 返回 path 所在文件系统的容量快照。
//
// 使用标准库 syscall.Statfs；Darwin 本机实测，Linux 在本地 Linux 容器内
// 实测（TestStatfsProbe），其他 unix（BSD 等）走同一接口但未单独验证。
// Windows 实现见 disk_windows.go。
func Statfs(path string) (DiskSpace, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return DiskSpace{}, err
	}
	// Bsize 在 Darwin 是 uint32、Linux 是 int64，统一转 int64 后运算。
	blockSize := int64(st.Bsize)
	return DiskSpace{
		TotalBytes:     int64(st.Blocks) * blockSize,
		FreeBytes:      int64(st.Bfree) * blockSize,
		AvailableBytes: int64(st.Bavail) * blockSize,
	}, nil
}
