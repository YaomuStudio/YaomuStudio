//go:build windows

package system

import (
	"os/exec"
	"syscall"
)

// hideWindow 避免 GUI 应用执行探测命令时弹出控制台窗口。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
