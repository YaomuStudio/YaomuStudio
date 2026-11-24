package uninstall

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// personalCandidates is a whitelist. Unknown userData entries are never
// removed. Model packages, runtimes, the WSL tree and the root itself are
// deliberately absent.
func personalCandidates(userData string) []string {
	names := []string{
		"desktop-preferences.json",
		"onboarding.json",
		"core-data/media",
		"core-data/cache",
		"core-data/exports",
		"core-data/work",
		"core-data/file-commits",
		"core-data/logs",
		"Local Storage",
		"Session Storage",
		"Cache",
		"Code Cache",
		"GPUCache",
		"Network",
		"blob_storage",
		"Service Worker",
		"core-data/yaomu.db-wal",
		"core-data/yaomu.db-shm",
		"core-data/yaomu.db",
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, filepath.Join(userData, filepath.FromSlash(name)))
	}
	return out
}

func inventoryPersonal(userData string) ([]fileTarget, int64, bool) {
	var files []fileTarget
	unknown := false
	for _, candidate := range personalCandidates(userData) {
		found, err := walkFiles(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			unknown = true
			continue
		}
		files = append(files, found...)
	}
	files = dedupeFiles(files)
	return files, sumFiles(files), unknown
}

func deletePersonalFiles(userData string, files []fileTarget) (bool, error) {
	ordered := append([]fileTarget{}, files...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return !isDatabaseFile(ordered[i].Path) && isDatabaseFile(ordered[j].Path)
	})
	deleted := false
	for _, file := range ordered {
		if err := checkNoReparse(userData, file.Path); err != nil {
			return deleted, err
		}
		info, err := os.Lstat(file.Path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return deleted, err
		}
		if !info.Mode().IsRegular() {
			return deleted, fmt.Errorf("personal target is no longer a regular file")
		}
		if info.Size() != file.Size || info.ModTime().UnixNano() != file.ModTime {
			return deleted, fmt.Errorf("personal target changed")
		}
		if err := os.Remove(file.Path); err != nil {
			return deleted, err
		}
		deleted = true
	}
	for _, candidate := range personalCandidates(userData) {
		_ = os.Remove(candidate) // Only empty directories; never recurses.
	}
	return deleted, nil
}

func isDatabaseFile(path string) bool {
	base := filepath.Base(path)
	return strings.HasPrefix(base, "yaomu.db")
}
