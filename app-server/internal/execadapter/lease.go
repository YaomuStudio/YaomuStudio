package execadapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LeaseOwner records launch identity for diagnosis. PIDs in this file never
// authorize killing or automatically taking over an old execution.
type LeaseOwner struct {
	WorkspaceID string    `json:"workspace_id"`
	InstanceID  string    `json:"instance_id"`
	Epoch       int64     `json:"epoch"`
	CorePID     int       `json:"core_pid"`
	RunnerID    string    `json:"runner_id,omitempty"`
	LaunchID    string    `json:"launch_id"`
	CreatedAt   time.Time `json:"created_at"`
}

// WorkspaceLease survives a Core crash. An existing lease, including a partial
// or malformed owner file, blocks a new Runner until execution-tree shutdown is
// independently established. There is deliberately no PID-based stale takeover.
type WorkspaceLease struct {
	dir   string
	owner LeaseOwner
}

var ErrWorkspaceLeased = errors.New("workspace runner lease requires stop verification")

func AcquireWorkspaceLease(dataDir string, owner LeaseOwner) (*WorkspaceLease, error) {
	if err := validateLeasePlatform(); err != nil {
		return nil, err
	}
	if owner.WorkspaceID == "" || owner.InstanceID == "" || owner.Epoch <= 0 {
		return nil, fmt.Errorf("runner lease identity is required")
	}
	runtimeDir := filepath.Join(dataDir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return nil, fmt.Errorf("create runner lease parent: %w", err)
	}
	dir := filepath.Join(runtimeDir, "runner-lease")
	if err := os.Mkdir(dir, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, ErrWorkspaceLeased
		}
		return nil, fmt.Errorf("acquire runner lease: %w", err)
	}
	owner.CorePID = os.Getpid()
	owner.LaunchID = randomLaunchID()
	owner.CreatedAt = time.Now().UTC()
	lease := &WorkspaceLease{dir: dir, owner: owner}
	if err := lease.writeOwner(); err != nil {
		// A partial lease remains a safe block; never erase uncertain ownership.
		return nil, err
	}
	if err := syncLeaseDir(runtimeDir); err != nil {
		return nil, err
	}
	return lease, nil
}

func (l *WorkspaceLease) RecordRunner(identity string) error {
	if identity == "" {
		return fmt.Errorf("runner launch identity is required")
	}
	l.owner.RunnerID = identity
	return l.writeOwner()
}

func (l *WorkspaceLease) writeOwner() error {
	data, err := json.Marshal(l.owner)
	if err != nil {
		return fmt.Errorf("encode runner lease owner: %w", err)
	}
	path := filepath.Join(l.dir, "owner.json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write runner lease owner: %w", err)
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("persist runner lease owner: %w", err)
	}
	return syncLeaseDir(l.dir)
}

// ReleaseAfterStop only releases this launch's lease after positive tree-exit
// evidence. Any shutdown uncertainty deliberately survives the next Core start.
func (l *WorkspaceLease) ReleaseAfterStop(stopErr error) error {
	if stopErr != nil {
		return errors.Join(ErrWorkspaceLeased, stopErr)
	}
	path := filepath.Join(l.dir, "owner.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read runner lease owner before release: %w", err)
	}
	var current LeaseOwner
	if err := json.Unmarshal(data, &current); err != nil || current.LaunchID != l.owner.LaunchID {
		return ErrWorkspaceLeased
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove runner lease owner: %w", err)
	}
	if err := os.Remove(l.dir); err != nil {
		return fmt.Errorf("release runner lease: %w", err)
	}
	return syncLeaseDir(filepath.Dir(l.dir))
}
