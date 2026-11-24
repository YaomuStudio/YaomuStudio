package uninstall

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type backupSource struct {
	Root string
	Name string
}

func backupExports(installDir, userData, documents string, now time.Time) (string, string, error) {
	sources := []backupSource{
		{Root: filepath.Join(installDir, "outputs"), Name: "program-outputs"},
		{Root: filepath.Join(userData, "core-data", "exports"), Name: "core-exports"},
	}
	type plannedFile struct {
		Source string
		Target string
		Size   int64
	}
	var files []plannedFile
	for _, source := range sources {
		info, err := os.Lstat(source.Root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "failed", "", err
		}
		if !info.IsDir() {
			return "failed", "", fmt.Errorf("export source is not a directory")
		}
		if err := checkNoReparse(source.Root, source.Root); err != nil {
			return "failed", "", err
		}
		found, err := walkFiles(source.Root)
		if err != nil {
			return "failed", "", err
		}
		for _, file := range found {
			rel, err := filepath.Rel(source.Root, file.Path)
			if err != nil || rel == ".." {
				return "failed", "", fmt.Errorf("export path escaped source")
			}
			files = append(files, plannedFile{
				Source: file.Path,
				Target: filepath.Join(source.Name, rel),
				Size:   file.Size,
			})
		}
	}
	if len(files) == 0 {
		return "none", "", nil
	}
	if err := os.MkdirAll(documents, 0o755); err != nil {
		return "failed", "", err
	}
	parent := filepath.Join(documents, "YaomuStudio Exports")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "failed", "", err
	}
	stamp := now.Format("20060102-150405")
	final := filepath.Join(parent, stamp)
	for index := 2; ; index++ {
		if _, err := os.Lstat(final); os.IsNotExist(err) {
			break
		}
		final = filepath.Join(parent, fmt.Sprintf("%s-%d", stamp, index))
	}
	temp := filepath.Join(parent, fmt.Sprintf(".partial-%s-%d", stamp, os.Getpid()))
	if err := os.RemoveAll(temp); err != nil {
		return "failed", "", err
	}
	if err := os.Mkdir(temp, 0o755); err != nil {
		return "failed", "", err
	}
	cleanup := func() {
		_ = os.RemoveAll(temp)
	}
	for _, file := range files {
		target := filepath.Join(temp, file.Target)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			cleanup()
			return "failed", "", err
		}
		if err := copyVerified(file.Source, target, file.Size); err != nil {
			cleanup()
			return "failed", "", err
		}
	}
	if err := os.Rename(temp, final); err != nil {
		cleanup()
		return "failed", "", err
	}
	return "ok", final, nil
}

func copyVerified(source, target string, expected int64) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	sourceHash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(out, sourceHash), in)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != expected {
		return fmt.Errorf("backup size mismatch")
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		return err
	}
	if targetInfo.Size() != expected {
		return fmt.Errorf("backup target size mismatch")
	}
	actual, err := sha256File(target)
	if err != nil {
		return err
	}
	if actual != hex.EncodeToString(sourceHash.Sum(nil)) {
		return fmt.Errorf("backup hash mismatch")
	}
	return nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
