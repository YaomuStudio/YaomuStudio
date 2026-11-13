//go:build !windows

package execadapter

import (
	"errors"
	"fmt"
	"os"
)

func validateLeasePlatform() error { return nil }

func syncLeaseDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open runner lease directory: %w", err)
	}
	return errors.Join(dir.Sync(), dir.Close())
}
