//go:build !windows

package execadapter

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func leaseOwner() LeaseOwner {
	return LeaseOwner{WorkspaceID: "workspace-test", InstanceID: "instance-test", Epoch: 2}
}

func TestWorkspaceLeaseRequiresConfirmedStopBeforeTakeover(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireWorkspaceLease(dir, leaseOwner())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.RecordRunner("runner:123:owned-launch"); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireWorkspaceLease(dir, leaseOwner()); !errors.Is(err, ErrWorkspaceLeased) {
		t.Fatalf("second Core acquired active Runner lease: %v", err)
	}
	if err := first.ReleaseAfterStop(ErrStopUnconfirmed); !errors.Is(err, ErrWorkspaceLeased) {
		t.Fatalf("unknown stop released lease: %v", err)
	}
	if _, err := AcquireWorkspaceLease(dir, leaseOwner()); !errors.Is(err, ErrWorkspaceLeased) {
		t.Fatalf("restart bypassed retained lease: %v", err)
	}
	if err := first.ReleaseAfterStop(nil); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireWorkspaceLease(dir, leaseOwner())
	if err != nil {
		t.Fatalf("confirmed exit did not permit next Core: %v", err)
	}
	if err := second.ReleaseAfterStop(nil); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceLeaseNeverGuessesPartialOrChangedOwner(t *testing.T) {
	for _, content := range []string{"", "{bad-json", `{"core_pid":1,"launch_id":"another-launch"}`} {
		t.Run(content, func(t *testing.T) {
			dir := t.TempDir()
			lease, err := AcquireWorkspaceLease(dir, leaseOwner())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(lease.dir, "owner.json"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := lease.ReleaseAfterStop(nil); !errors.Is(err, ErrWorkspaceLeased) {
				t.Fatalf("changed lease owner was released: %v", err)
			}
			if _, err := AcquireWorkspaceLease(dir, leaseOwner()); !errors.Is(err, ErrWorkspaceLeased) {
				t.Fatalf("unknown old PID/owner was taken over: %v", err)
			}
		})
	}
}
