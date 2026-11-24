//go:build windows

package uninstall

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func defaultRunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

func ownedProcessRunning() (bool, error) {
	for _, name := range []string{"YaomuStudio.exe", "yaomu-core.exe"} {
		out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+name, "/NH").Output()
		if err != nil {
			return false, fmt.Errorf("tasklist: %w", err)
		}
		if strings.Contains(strings.ToLower(string(out)), strings.ToLower(name)) {
			return true, nil
		}
	}
	return false, nil
}
