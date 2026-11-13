package execadapter

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testWorkspaceLeaseOwner(instance string) LeaseOwner {
	return LeaseOwner{WorkspaceID: "workspace-dual", InstanceID: instance, Epoch: 1}
}

// Two independent dataDirs must not share a lease, and one workspace's release
// must not unblock or disturb another workspace.
func TestWorkspaceLeasesForIndependentDataDirsDoNotInterfere(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	leaseA, err := AcquireWorkspaceLease(dirA, testWorkspaceLeaseOwner("instance-a"))
	if err != nil {
		t.Fatalf("acquire first dataDir lease: %v", err)
	}
	leaseB, err := AcquireWorkspaceLease(dirB, testWorkspaceLeaseOwner("instance-b"))
	if err != nil {
		t.Fatalf("second dataDir was blocked by another workspace: %v", err)
	}
	if _, err := AcquireWorkspaceLease(dirA, testWorkspaceLeaseOwner("instance-a2")); !errors.Is(err, ErrWorkspaceLeased) {
		t.Fatalf("same dataDir acquired a second lease: %v", err)
	}
	if err := leaseA.ReleaseAfterStop(nil); err != nil {
		t.Fatalf("release confirmed first lease: %v", err)
	}
	if _, err := AcquireWorkspaceLease(dirB, testWorkspaceLeaseOwner("instance-b2")); !errors.Is(err, ErrWorkspaceLeased) {
		t.Fatalf("releasing one dataDir lease affected another: %v", err)
	}
	reacquired, err := AcquireWorkspaceLease(dirA, testWorkspaceLeaseOwner("instance-a3"))
	if err != nil {
		t.Fatalf("reacquire released dataDir: %v", err)
	}
	if err := reacquired.ReleaseAfterStop(nil); err != nil {
		t.Fatalf("release reacquired lease: %v", err)
	}
	if err := leaseB.ReleaseAfterStop(nil); err != nil {
		t.Fatalf("release second dataDir lease: %v", err)
	}
}

// A partial, malformed, or foreign owner file is never taken over by PID or
// launch identity inference.
func TestWorkspaceLeaseDoesNotTakeOverUnknownOwner(t *testing.T) {
	for _, content := range []string{"", "{bad-json", `{"core_pid":1,"launch_id":"another-launch"}`} {
		t.Run(content, func(t *testing.T) {
			dir := t.TempDir()
			lease, err := AcquireWorkspaceLease(dir, testWorkspaceLeaseOwner("instance-owner"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(lease.dir, "owner.json"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := lease.ReleaseAfterStop(nil); !errors.Is(err, ErrWorkspaceLeased) {
				t.Fatalf("changed lease owner was released: %v", err)
			}
			if _, err := AcquireWorkspaceLease(dir, testWorkspaceLeaseOwner("instance-next")); !errors.Is(err, ErrWorkspaceLeased) {
				t.Fatalf("unknown old PID/owner was taken over: %v", err)
			}
		})
	}
}
