//go:build windows

package execadapter

// Windows mkdir/file Sync persist the lease across process crashes. Directory
// flushing/power-loss durability needs a Windows filesystem test; do not claim it.
func syncLeaseDir(string) error { return nil }

func validateLeasePlatform() error { return nil }
