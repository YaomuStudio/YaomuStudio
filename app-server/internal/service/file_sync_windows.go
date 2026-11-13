//go:build windows

package service

// Windows file data is flushed before rename. Directory handles do not expose
// the POSIX fsync contract; power-loss durability still needs platform testing.
func syncDirectory(string) error { return nil }
