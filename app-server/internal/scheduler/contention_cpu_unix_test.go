//go:build unix

package scheduler

import (
	"syscall"
	"time"
)

// processCPU 返回进程累计 user+sys CPU 时间，用于把 tick 的墙钟耗时拆成
// CPU 执行与等待（连接队列、文件 IO、调度）。采样是进程级的，只适合同一
// 测试串行运行时解释；Windows 上不提供该指标。
func processCPU() (time.Duration, bool) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, false
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano()), true
}
