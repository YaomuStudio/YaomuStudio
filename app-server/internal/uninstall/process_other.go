//go:build !windows

package uninstall

import (
	"context"
	"os/exec"
	"time"
)

func defaultRunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

func ownedProcessRunning() (bool, error) {
	return false, nil
}
