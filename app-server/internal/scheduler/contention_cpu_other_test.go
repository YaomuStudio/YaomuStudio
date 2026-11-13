//go:build !unix

package scheduler

import "time"

// processCPU 在非 unix 平台不可用：返回 ok=false，报告标注 CPU 未覆盖。
func processCPU() (time.Duration, bool) { return 0, false }
