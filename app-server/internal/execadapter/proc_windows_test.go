//go:build windows

package execadapter

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func windowsHelperOptions(t *testing.T, mode, marker string) ProcessOptions {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return ProcessOptions{
		Command: binary,
		Args:    []string{"-test.run=^TestWindowsJobHelper$", "--", mode, marker},
		Env:     append(os.Environ(), "YAOMU_JOB_HELPER=1"),
	}
}

func waitWindowsMarker(path string) ([]byte, error) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("owned child marker did not appear")
}

func assertWindowsOwnedChildExited(t *testing.T, marker string) {
	t.Helper()
	data, err := waitWindowsMarker(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err == windows.ERROR_INVALID_PARAMETER {
		return // process object has already been destroyed
	}
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	state, err := windows.WaitForSingleObject(handle, 5000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("owned job child still active: state=%d err=%v", state, err)
	}
}

func TestWindowsJobConfirmsDescendantsAfterRunnerExit(t *testing.T) {
	if unsafe.Sizeof(jobAccounting{}) != 48 {
		t.Fatal("unexpected Windows job accounting layout")
	}
	marker := filepath.Join(t.TempDir(), "child.pid")
	client, err := NewProcessClient(context.Background(), windowsHelperOptions(t, "runner", marker))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	result := (&Executor{Client: client}).Execute(context.Background(), testRequest(), nil, nil)
	if result.TransportErr == nil || result.StopUnconfirmed {
		t.Fatalf("owned Job did not confirm whole-tree exit: %+v", result)
	}
	assertWindowsOwnedChildExited(t, marker)
}

func TestWindowsJobClosesOnCoreCrash(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child.pid")
	opts := windowsHelperOptions(t, "core", marker)
	command := exec.Command(opts.Command, opts.Args...)
	command.Env = opts.Env
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Core crash helper failed: %v %s", err, output)
	}
	assertWindowsOwnedChildExited(t, marker)
}

func TestWindowsWorkspaceLeaseAllowsCleanFirstLaunch(t *testing.T) {
	lease, err := AcquireWorkspaceLease(t.TempDir(), LeaseOwner{
		WorkspaceID: "workspace", InstanceID: "instance", Epoch: 1,
	})
	if err != nil {
		t.Fatalf("clean Windows workspace was blocked: %v", err)
	}
	if err := lease.ReleaseAfterStop(nil); err != nil {
		t.Fatal(err)
	}
}

// This subprocess fixture starts a descendant only after reading its first
// execute frame. Core exits without defers in crash mode, exercising kernel
// KILL_ON_JOB_CLOSE rather than user-space cleanup.
func TestWindowsJobHelper(t *testing.T) {
	if os.Getenv("YAOMU_JOB_HELPER") != "1" {
		return
	}
	mode, marker := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	switch mode {
	case "core":
		transport, err := StartProcess(context.Background(), windowsHelperOptions(t, "runner", marker))
		if err != nil {
			t.Fatal(err)
		}
		if err := transport.Send(Frame{V: 1, Op: OpExecute, ReqID: "execute", Payload: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
		if _, err := waitWindowsMarker(marker); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	case "runner":
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		opts := windowsHelperOptions(t, "child", marker)
		child := exec.Command(opts.Command, opts.Args...)
		child.Env = opts.Env
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	case "child":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	default:
		t.Fatalf("unknown job helper mode %q", mode)
	}
}
