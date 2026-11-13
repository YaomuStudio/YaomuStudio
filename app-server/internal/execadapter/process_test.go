//go:build !windows

package execadapter

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProcessStreamLossCannotReleaseDetachedExecutionLease(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	lease, err := AcquireWorkspaceLease(dir, leaseOwner())
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "broken_runner.py")
	marker := filepath.Join(dir, "child.pid")
	body := `import json, subprocess, sys, time
for line in sys.stdin:
    request = json.loads(line)
    if request["op"] == "execute":
        child = subprocess.Popen([sys.executable, "-I", "-c", "import time; time.sleep(2)"],
                                 start_new_session=True, stdin=subprocess.DEVNULL,
                                 stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        with open(sys.argv[1], "w") as marker:
            marker.write(str(child.pid))
        print("invalid-protocol-frame", flush=True)
        time.sleep(5)
`
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := NewProcessClient(ctx, ProcessOptions{Command: python, Args: []string{"-I", script, marker}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := lease.RecordRunner(client.Identity()); err != nil {
		t.Fatal(err)
	}
	executor := &Executor{Client: client}
	result := executor.Execute(ctx, testRequest(), nil, nil)
	if !result.StopUnconfirmed || !errors.Is(result.TransportErr, ErrStopUnconfirmed) {
		t.Fatalf("lost stream falsely confirmed detached tree exit: %+v", result)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("test detached child was not still alive: %v", err)
	}
	if err := lease.ReleaseAfterStop(client.Close()); !errors.Is(err, ErrWorkspaceLeased) {
		t.Fatalf("uncertain shutdown released workspace lease: %v", err)
	}
	if _, err := AcquireWorkspaceLease(dir, leaseOwner()); !errors.Is(err, ErrWorkspaceLeased) {
		t.Fatalf("restart overlapped old detached execution: %v", err)
	}
	// The test child has a bounded two-second lifetime. No PID from a stale
	// persisted lease is ever signaled or used for takeover.
}
