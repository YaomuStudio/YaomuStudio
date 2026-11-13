// Package system 提供跨平台的本机系统探测；当前只包含文件系统容量探测。
package system

// DiskSpace 是文件系统分区容量快照（字节）。
//
//   - TotalBytes：分区总容量；
//   - FreeBytes：空闲容量（含仅 root/特权进程可用的部分）；
//   - AvailableBytes：当前进程可用的容量，预检与空间准入以它为准。
type DiskSpace struct {
	TotalBytes     int64
	FreeBytes      int64
	AvailableBytes int64
}
