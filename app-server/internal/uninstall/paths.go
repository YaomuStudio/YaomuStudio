package uninstall

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func absPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func within(root, path string) (string, bool) {
	rootAbs, err := absPath(root)
	if err != nil {
		return "", false
	}
	pathAbs, err := absPath(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return pathAbs, true
}

// checkNoReparse verifies every component from root to path without following
// links. A missing target is allowed; the caller decides whether that is
// unknown or simply absent.
func checkNoReparse(root, path string) error {
	rootAbs, err := absPath(root)
	if err != nil {
		return err
	}
	pathAbs, ok := within(rootAbs, path)
	if !ok {
		return fmt.Errorf("path escapes root")
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil {
		return err
	}
	current := rootAbs
	if link, err := isReparsePoint(current); err != nil {
		return err
	} else if link {
		return fmt.Errorf("root is a reparse point")
	}
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		link, err := isReparsePoint(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if link {
			return fmt.Errorf("reparse point refused")
		}
	}
	return nil
}

func walkFiles(root string) ([]fileTarget, error) {
	rootAbs, err := absPath(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(rootAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if link, err := isReparsePoint(rootAbs); err != nil {
		return nil, err
	} else if link {
		return nil, fmt.Errorf("reparse point refused")
	}
	if !info.IsDir() {
		return []fileTarget{{Path: rootAbs, Size: info.Size(), ModTime: info.ModTime().UnixNano()}}, nil
	}
	var files []fileTarget
	var visit func(string) error
	visit = func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			if link, err := isReparsePoint(path); err != nil {
				return err
			} else if link {
				return fmt.Errorf("reparse point refused: %s", path)
			}
			if entry.IsDir() {
				if err := visit(path); err != nil {
					return err
				}
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("non-regular file refused: %s", path)
			}
			files = append(files, fileTarget{Path: path, Size: info.Size(), ModTime: info.ModTime().UnixNano()})
		}
		return nil
	}
	if err := visit(rootAbs); err != nil {
		return nil, err
	}
	return files, nil
}

func removeTree(root string) error {
	rootAbs, err := absPath(root)
	if err != nil {
		return err
	}
	info, err := os.Lstat(rootAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if link, err := isReparsePoint(rootAbs); err != nil {
		return err
	} else if link {
		return fmt.Errorf("reparse point refused")
	}
	if !info.IsDir() {
		return os.Remove(rootAbs)
	}
	entries, err := os.ReadDir(rootAbs)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(rootAbs, entry.Name())
		if link, err := isReparsePoint(path); err != nil {
			return err
		} else if link {
			return fmt.Errorf("reparse point refused: %s", path)
		}
		if entry.IsDir() {
			if err := removeTree(path); err != nil {
				return err
			}
			continue
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return os.Remove(rootAbs)
}

func dedupeFiles(files []fileTarget) []fileTarget {
	byIdentity := make(map[string]fileTarget, len(files))
	for _, file := range files {
		key, err := fileIdentity(file.Path)
		if err != nil {
			key = "path:" + file.Path
		}
		if prev, ok := byIdentity[key]; !ok || file.Size > prev.Size {
			byIdentity[key] = file
		}
	}
	out := make([]fileTarget, 0, len(byIdentity))
	for _, file := range byIdentity {
		out = append(out, file)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func sumFiles(files []fileTarget) int64 {
	var total int64
	for _, file := range files {
		total += file.Size
	}
	return total
}
