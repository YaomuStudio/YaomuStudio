//go:build !windows

package service

import (
	"fmt"
	"os"
)

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open commit directory: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if err := firstError(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync commit directory: %w", err)
	}
	return nil
}
