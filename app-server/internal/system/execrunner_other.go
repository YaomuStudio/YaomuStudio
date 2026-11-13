//go:build !windows

package system

import "os/exec"

func hideWindow(_ *exec.Cmd) {}
