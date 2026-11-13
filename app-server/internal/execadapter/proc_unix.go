//go:build !windows

package execadapter

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

type processTree struct{ cmd *exec.Cmd }

func attachProcessTree(cmd *exec.Cmd, _ bool) (*processTree, error) {
	return &processTree{cmd: cmd}, nil
}

func (t *processTree) Kill(leaderExited bool) error {
	if leaderExited {
		return nil // do not signal a potentially reused root identity after Wait
	}
	return killProcessTree(t.cmd)
}

func (t *processTree) Exited() (bool, error)      { return processGroupExited(t.cmd) }
func (*processTree) Close() error                 { return nil }
func (*processTree) CoversDetachedChildren() bool { return false }

// configureProcessGroup 建立独立进程组，退出/取消时只清理本应用拉起的进程树。
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessTree 向本应用拥有的进程组发送 SIGKILL。
func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("kill runner process group: %w", err)
		}
	}
	return nil
}

func processGroupExited(cmd *exec.Cmd) (bool, error) {
	if cmd.Process == nil {
		return true, nil
	}
	err := syscall.Kill(-cmd.Process.Pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false, fmt.Errorf("inspect runner process group: %w", err)
	}
	return false, nil
}
