//go:build windows

package system

import (
	"syscall"
	"unsafe"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceEx = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// Statfs 返回 path 所在卷的容量快照。
//
// 只要求 GOOS=windows 编译通过；当前没有 Windows 实测环境，行为未验证
// （FR-SET-20 的 Windows 分支在测试机上确认后再取消此标注）。
func Statfs(path string) (DiskSpace, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return DiskSpace{}, err
	}
	var availableToCaller, total, totalFree uint64
	ok, _, callErr := procGetDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&availableToCaller)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if ok == 0 {
		return DiskSpace{}, callErr
	}
	return DiskSpace{
		TotalBytes:     int64(total),
		FreeBytes:      int64(totalFree),
		AvailableBytes: int64(availableToCaller),
	}, nil
}
