// Package uninstall implements the narrow Windows uninstall helper.
//
// It is deliberately not a second business writer: it reads the completed
// installation records, inventories app-owned data and performs only the
// explicitly selected cleanup. It never unregisters WSL, removes a bundle,
// venv or runtime, or deletes the whole userData root.
package uninstall

import (
	"context"
	"fmt"
	"time"
)

const SchemaVersion = 1

// Options are the helper inputs. FlashCLIRoot is a test-only injection; the
// production CLI resolves the WSL home itself.
type Options struct {
	UserDataDir  string
	InstallDir   string
	DocumentsDir string
	Personal     bool
	Models       bool
	Snapshot     string
	FlashCLIRoot string
	WSLDistro    string
	Now          func() time.Time
	RunCommand   func(ctx context.Context, name string, args ...string) ([]byte, error)
	ProcessCheck func() (bool, error)
}

// Result is the stable INI/JSON summary consumed by NSIS and tests.
type Result struct {
	SchemaVersion       int
	Status              string
	PersonalBytes       int64
	ModelsBytes         int64
	ModelsDownloadBytes int64
	ModelsKnown         bool
	ModelsSizeDisplay   string
	Snapshot            string
	BackupStatus        string
	BackupPath          string
	PersonalDeleted     bool
	ModelsDeleted       bool
	PersonalRequested   bool
	ModelsRequested     bool
	MessageKey          string
}

// Failure carries a stable helper exit code and user-facing message key.
type Failure struct {
	Code       int
	MessageKey string
	Err        error
}

func (f *Failure) Error() string {
	if f == nil || f.Err == nil {
		return "uninstall helper failed"
	}
	return f.Err.Error()
}

func failure(code int, key string, err error) *Failure {
	return &Failure{Code: code, MessageKey: key, Err: err}
}

type fileTarget struct {
	Path    string
	Size    int64
	ModTime int64
}

type inventoryState struct {
	personalFiles     []fileTarget
	personalBytes     int64
	personalUnknown   bool
	modelFiles        []fileTarget
	modelRoot         string
	modelsBytes       int64
	modelsDownload    int64
	modelsKnown       bool
	modelsSizeDisplay string
	snapshot          string
}

func (s *inventoryState) result() Result {
	status := "ok"
	if s.personalUnknown || !s.modelsKnown {
		status = "unknown"
	}
	if s.personalBytes == 0 && s.modelsBytes == 0 {
		status = "no_targets"
	}
	key := ""
	if s.personalUnknown || !s.modelsKnown {
		key = "uninstall-error-models-unknown"
	}
	return Result{
		SchemaVersion:       SchemaVersion,
		Status:              status,
		PersonalBytes:       s.personalBytes,
		ModelsBytes:         s.modelsBytes,
		ModelsDownloadBytes: s.modelsDownload,
		ModelsKnown:         s.modelsKnown,
		ModelsSizeDisplay:   s.modelsSizeDisplay,
		Snapshot:            s.snapshot,
		MessageKey:          key,
	}
}

func (o *Options) now() time.Time {
	if o != nil && o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *Options) runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	if o != nil && o.RunCommand != nil {
		return o.RunCommand(ctx, name, args...)
	}
	return defaultRunCommand(ctx, name, args...)
}

func (o *Options) processCheck() (bool, error) {
	if o != nil && o.ProcessCheck != nil {
		return o.ProcessCheck()
	}
	return ownedProcessRunning()
}

func formatBytes(n int64) string {
	if n < 0 {
		return ""
	}
	const unit = int64(1024)
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := unit, 0
	for value := n / unit; value >= unit && exp < 5; value /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
