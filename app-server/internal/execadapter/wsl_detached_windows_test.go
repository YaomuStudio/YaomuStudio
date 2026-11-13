//go:build windows

package execadapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestWSLKillKeepsExecutionUnconfirmed covers the Windows ownership gap: the
// Core Job Object contains Windows processes only. Terminating wsl.exe cannot
// prove the WSL2 Linux tree stopped, so Client.Kill must keep the execution
// slot unconfirmed. In this yaomu image the WSL service may reap the detached
// child when the Job terminates the launcher; that observation is logged, not
// assumed. A separate witness probe kills wsl.exe outside the Job and shows the
// detached child can survive.
//
// This test is opt-in on a real Windows/WSL host and only ever signals the one
// PID recorded by this launch; it never scans by process name:
//
//	YAOMU_WSL_TEST=1 [YAOMU_WSL_TEST_DISTRO=yaomu] go test ./internal/execadapter -run TestWSLKill -v
func TestWSLKillKeepsExecutionUnconfirmed(t *testing.T) {
	if os.Getenv("YAOMU_WSL_TEST") != "1" {
		t.Skip("set YAOMU_WSL_TEST=1 to run against a real WSL distro")
	}
	if _, err := exec.LookPath("wsl.exe"); err != nil {
		t.Skipf("wsl.exe not found: %v", err)
	}
	distro := strings.TrimSpace(os.Getenv("YAOMU_WSL_TEST_DISTRO"))
	if distro == "" {
		distro = "yaomu"
	}
	if err := exec.Command("wsl.exe", "-d", distro, "--", "/bin/true").Run(); err != nil {
		t.Skipf("WSL distro %q is not usable: %v", distro, err)
	}
	if err := exec.Command("wsl.exe", "-d", distro, "--", "/bin/bash", "-c", "command -v setsid >/dev/null").Run(); err != nil {
		t.Skipf("WSL distro %q does not provide setsid", distro)
	}

	dir := t.TempDir()
	pidPath := filepath.Join(dir, "detached.pid")
	witnessReadyPath := filepath.Join(dir, "witness.ready")
	// Keep a second WSL session alive. WSL may tear down a distro when its last
	// client exits; without a witness that teardown would reap the detached
	// child and mask the real ownership gap. A production Core can also have
	// other WSL sessions, so it cannot rely on distro teardown for confirmation.
	witness := exec.Command("wsl.exe", "-d", distro, "--", "/bin/sh", "-c",
		fmt.Sprintf("echo ready > \"%s\"; exec /bin/sleep 300", WindowsToWSL(witnessReadyPath)))
	if err := witness.Start(); err != nil {
		t.Fatalf("start WSL witness session: %v", err)
	}
	witnessDeadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(witnessReadyPath); err == nil {
			break
		}
		if time.Now().After(witnessDeadline) {
			_ = witness.Process.Kill()
			_, _ = witness.Process.Wait()
			t.Fatalf("WSL witness session did not become ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
	// $$ belongs to the detached shell; exec keeps the same PID for sleep. The
	// outer shell waits so wsl.exe stays alive until its Job is terminated.
	script := fmt.Sprintf("setsid /bin/sh -c 'echo $$ > \"%s\"; exec /bin/sleep 300' >/dev/null 2>&1 & wait",
		WindowsToWSL(pidPath))

	transport, err := StartProcess(context.Background(), ProcessOptions{
		Command: "wsl.exe",
		Args:    []string{"-d", distro, "--", "/bin/bash", "-c", script},
	})
	if err != nil {
		_ = witness.Process.Kill()
		_, _ = witness.Process.Wait()
		t.Fatalf("start wsl.exe transport: %v", err)
	}

	pid := 0
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			if pid == 0 {
				if data, err := os.ReadFile(pidPath); err == nil {
					pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
				}
			}
			if pid > 0 {
				_ = exec.Command("wsl.exe", "-d", distro, "--", "/bin/sh", "-c", "kill -KILL "+strconv.Itoa(pid)).Run()
			}
		}
		if witness.Process != nil {
			_ = witness.Process.Kill()
			_, _ = witness.Process.Wait()
		}
		_ = transport.Close()
	})

	pid = waitWSLDetachedPID(t, pidPath, 30*time.Second)
	if transport.CoversDetachedChildren() {
		t.Fatal("wsl.exe process tree must not claim coverage of WSL2 Linux descendants")
	}
	assertWSLPIDAlive(t, distro, pid, 30*time.Second)

	client := NewClient(transport, nil)
	// No terminal frame arrived for this execution; the slot must stay
	// reserved after the Windows-only process tree is killed.
	client.activeMu.Lock()
	client.active = &Request{TaskID: "task-wsl-detached", StepID: "step-1", Attempt: 1}
	client.activeMu.Unlock()

	if err := client.Kill(); !errors.Is(err, ErrStopUnconfirmed) {
		t.Fatalf("Client.Kill() = %v, want ErrStopUnconfirmed", err)
	}
	if !client.ExecutionUnconfirmed() {
		t.Fatal("Client.Kill cleared the active execution after killing only wsl.exe")
	}
	// The safety invariant is "unconfirmed", not "the child certainly lives":
	// the Job may make WSL service reap the detached process in some images.
	alive, probeErr := wslPIDState(distro, pid)
	switch {
	case probeErr != nil:
		t.Logf("detached child state after Job kill is unreadable (%v); execution remains unconfirmed", probeErr)
		// Best-effort cleanup of the exact recorded PID; state is unknown.
		_ = exec.Command("wsl.exe", "-d", distro, "--", "/bin/sh", "-c", "kill -KILL "+strconv.Itoa(pid)).Run()
	case alive:
		t.Logf("detached child %d survived the Job kill; execution remains unconfirmed", pid)
	default:
		t.Logf("WSL service reaped detached child %d after Job kill; Core still has no positive tree-exit proof", pid)
	}
	if alive {
		// Cleanup is limited to the PID this launch recorded.
		if err := exec.Command("wsl.exe", "-d", distro, "--", "/bin/sh", "-c", "kill -KILL "+strconv.Itoa(pid)).Run(); err != nil {
			t.Fatalf("kill detached Linux child %d: %v", pid, err)
		}
		waitWSLPIDGone(t, distro, pid, 10*time.Second)
	}
	cleaned = true
}

func waitWSLDetachedPID(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("detached Linux child PID was not written to %s", path)
	return 0
}

func wslPIDState(distro string, pid int) (bool, error) {
	probe := fmt.Sprintf("if kill -0 %d 2>/dev/null; then echo alive; else echo gone; fi", pid)
	out, err := exec.Command("wsl.exe", "-d", distro, "--", "/bin/sh", "-c", probe).Output()
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(string(out)) {
	case "alive":
		return true, nil
	case "gone":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected WSL PID probe output %q", out)
	}
}

func assertWSLPIDAlive(t *testing.T, distro string, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		alive, err := wslPIDState(distro, pid)
		switch {
		case err != nil:
			// The distro may still be restarting after its wsl.exe session was
			// terminated; an unusable probe is not evidence the process stopped.
			lastErr = err
		case alive:
			return
		default:
			t.Fatalf("detached Linux child %d was stopped by killing the wsl.exe Job", pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("could not verify detached Linux child %d is alive: %v", pid, lastErr)
}

func waitWSLPIDGone(t *testing.T, distro string, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		alive, err := wslPIDState(distro, pid)
		if err == nil && !alive {
			return
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("detached Linux child %d did not exit after SIGKILL: %v", pid, lastErr)
}

// TestWSLRunnerLauncherCrashKeepsExecutionUnconfirmed starts a real WSL Runner,
// kills its wsl.exe launcher outside Core's Job, and records whether the Linux
// Runner leader survives. The safety invariant is the same either way: Core has
// no positive proof that detached execution children stopped, so the slot must
// remain unconfirmed. A witness keeps the distro alive; only the exact Runner
// PID is signalled during cleanup.
func TestWSLRunnerLauncherCrashKeepsExecutionUnconfirmed(t *testing.T) {
	if os.Getenv("YAOMU_WSL_TEST") != "1" {
		t.Skip("set YAOMU_WSL_TEST=1 to run against a real WSL distro")
	}
	if _, err := exec.LookPath("wsl.exe"); err != nil {
		t.Skipf("wsl.exe not found: %v", err)
	}
	distro := strings.TrimSpace(os.Getenv("YAOMU_WSL_TEST_DISTRO"))
	if distro == "" {
		distro = "yaomu"
	}
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	runnerSrc := filepath.Join(repoRoot, "runner", "src")
	if _, err := os.Stat(filepath.Join(runnerSrc, "yaomustudio_runner")); err != nil {
		t.Skipf("runner src missing: %v", err)
	}

	dir := t.TempDir()
	witness := startWSLWitness(t, distro, dir)
	_ = witness
	runnerPidPath := filepath.Join(dir, "runner.pid")
	// The shell writes its own PID and then execs Python, so the recorded PID is
	// the real Linux Runner leader, not the Windows wsl.exe launcher.
	runnerScript := fmt.Sprintf("echo $$ > '%s'; exec env PYTHONPATH='%s' python3 -B -P -s -m yaomustudio_runner",
		WindowsToWSL(runnerPidPath), WindowsToWSL(runnerSrc))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, err := NewProcessClient(ctx, ProcessOptions{
		Command: "wsl.exe",
		Args:    []string{"-d", distro, "--", "/bin/sh", "-c", runnerScript},
	})
	if err != nil {
		t.Fatalf("start real WSL runner: %v", err)
	}
	runnerPid := 0
	runnerCleaned := false
	t.Cleanup(func() {
		if !runnerCleaned {
			if runnerPid == 0 {
				if data, err := os.ReadFile(runnerPidPath); err == nil {
					runnerPid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
				}
			}
			if runnerPid > 0 {
				_ = exec.Command("wsl.exe", "-d", distro, "--", "/bin/sh", "-c", "kill -KILL "+strconv.Itoa(runnerPid)).Run()
			}
		}
		_ = client.Close()
	})

	helloCtx, helloCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer helloCancel()
	if _, err := client.Start(helloCtx); err != nil {
		t.Fatalf("runner hello: %v", err)
	}
	runnerPid = waitWSLDetachedPID(t, runnerPidPath, 30*time.Second)
	assertWSLPIDAlive(t, distro, runnerPid, 20*time.Second)

	transport, ok := client.transport.(*ProcessTransport)
	if !ok {
		t.Fatalf("transport type = %T, want *ProcessTransport", client.transport)
	}
	// Kill the launcher outside Core's Job Object, simulating an external crash.
	if err := transport.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill wsl.exe launcher: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	aliveAfterCrash, probeErr := wslPIDState(distro, runnerPid)
	switch {
	case probeErr != nil:
		t.Logf("runner PID %d state after launcher kill is unreadable: %v", runnerPid, probeErr)
	case aliveAfterCrash:
		t.Logf("runner PID %d survived the launcher kill", runnerPid)
	default:
		t.Logf("WSL reaped runner PID %d with the launcher; detached children remain unprovable", runnerPid)
	}

	client.activeMu.Lock()
	client.active = &Request{TaskID: "task-runner-crash", StepID: "step-1", Attempt: 1}
	client.activeMu.Unlock()
	killErr := client.Kill()
	if !errors.Is(killErr, ErrStopUnconfirmed) && !client.ExecutionUnconfirmed() {
		t.Fatalf("launcher crash cleared the active slot: killErr=%v", killErr)
	}
	if !client.ExecutionUnconfirmed() {
		t.Fatalf("Client.Kill cleared active execution after launcher crash: %v", killErr)
	}
	if aliveAfterCrash {
		if err := exec.Command("wsl.exe", "-d", distro, "--", "/bin/sh", "-c", "kill -KILL "+strconv.Itoa(runnerPid)).Run(); err != nil {
			t.Fatalf("kill recorded runner PID %d: %v", runnerPid, err)
		}
		waitWSLPIDGone(t, distro, runnerPid, 10*time.Second)
	}
	runnerCleaned = true
}

func startWSLWitness(t *testing.T, distro, dir string) *exec.Cmd {
	t.Helper()
	readyPath := filepath.Join(dir, "witness.ready")
	witness := exec.Command("wsl.exe", "-d", distro, "--", "/bin/sh", "-c",
		fmt.Sprintf("echo ready > \"%s\"; exec /bin/sleep 300", WindowsToWSL(readyPath)))
	if err := witness.Start(); err != nil {
		t.Fatalf("start WSL witness: %v", err)
	}
	t.Cleanup(func() {
		if witness.Process != nil {
			_ = witness.Process.Kill()
			_, _ = witness.Process.Wait()
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			return witness
		}
		if time.Now().After(deadline) {
			t.Fatalf("WSL witness did not become ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestWSLExternalLauncherKillKeepsExecutionUnconfirmed kills wsl.exe with
// TerminateProcess outside Core's Job Object. This image may still reap the
// detached child when the launcher is a Job member; that observation is logged.
// The safety invariant is that Core cannot confirm the tree and must keep the
// execution unconfirmed. A separate non-Job witness probe records survival.
func TestWSLExternalLauncherKillKeepsExecutionUnconfirmed(t *testing.T) {
	if os.Getenv("YAOMU_WSL_TEST") != "1" {
		t.Skip("set YAOMU_WSL_TEST=1 to run against a real WSL distro")
	}
	if _, err := exec.LookPath("wsl.exe"); err != nil {
		t.Skipf("wsl.exe not found: %v", err)
	}
	distro := strings.TrimSpace(os.Getenv("YAOMU_WSL_TEST_DISTRO"))
	if distro == "" {
		distro = "yaomu"
	}
	if err := exec.Command("wsl.exe", "-d", distro, "--", "/bin/bash", "-c", "command -v setsid >/dev/null").Run(); err != nil {
		t.Skipf("WSL distro %q does not provide setsid", distro)
	}

	dir := t.TempDir()
	pidPath := filepath.Join(dir, "detached-external.pid")
	witness := startWSLWitness(t, distro, dir)
	_ = witness
	script := fmt.Sprintf("setsid /bin/sh -c 'echo $$ > \"%s\"; exec /bin/sleep 300' >/dev/null 2>&1 & wait",
		WindowsToWSL(pidPath))
	transport, err := StartProcess(context.Background(), ProcessOptions{
		Command: "wsl.exe",
		Args:    []string{"-d", distro, "--", "/bin/bash", "-c", script},
	})
	if err != nil {
		t.Fatalf("start wsl.exe transport: %v", err)
	}
	pid := 0
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			if pid == 0 {
				if data, err := os.ReadFile(pidPath); err == nil {
					pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
				}
			}
			if pid > 0 {
				_ = exec.Command("wsl.exe", "-d", distro, "--", "/bin/sh", "-c", "kill -KILL "+strconv.Itoa(pid)).Run()
			}
		}
		_ = transport.Close()
	})

	pid = waitWSLDetachedPID(t, pidPath, 30*time.Second)
	assertWSLPIDAlive(t, distro, pid, 30*time.Second)
	// Terminate the launcher outside Core's Job, as an OS/antivirus crash would.
	if err := transport.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill wsl.exe launcher: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	alive, probeErr := wslPIDState(distro, pid)
	if probeErr != nil {
		t.Fatalf("probe detached child after external launcher kill: %v", probeErr)
	}
	if alive {
		t.Logf("detached child %d survived the external launcher kill; execution remains unconfirmed", pid)
	} else {
		t.Logf("WSL reaped detached child %d after external launcher kill; Core still cannot confirm", pid)
	}

	client := NewClient(transport, nil)
	client.activeMu.Lock()
	client.active = &Request{TaskID: "task-external-crash", StepID: "step-1", Attempt: 1}
	client.activeMu.Unlock()
	killErr := client.Kill()
	if !errors.Is(killErr, ErrStopUnconfirmed) && !client.ExecutionUnconfirmed() {
		t.Fatalf("external launcher crash cleared the active slot: killErr=%v", killErr)
	}
	if !client.ExecutionUnconfirmed() {
		t.Fatalf("Client.Kill cleared active execution after external launcher crash: %v", killErr)
	}
	// The Job termination may reap the child afterwards; clean it only if alive.
	if aliveAfter, err := wslPIDState(distro, pid); err == nil && aliveAfter {
		if err := exec.Command("wsl.exe", "-d", distro, "--", "/bin/sh", "-c", "kill -KILL "+strconv.Itoa(pid)).Run(); err != nil {
			t.Fatalf("kill detached child %d: %v", pid, err)
		}
		waitWSLPIDGone(t, distro, pid, 10*time.Second)
	}
	cleaned = true
}

// TestDetachedChildrenUncoveredFlagForcesFalse verifies the model route's
// explicit uncovered flag, so wrapping wsl.exe behind cmd.exe cannot fail open
// merely because the launcher basename is not wsl.exe.
func TestDetachedChildrenUncoveredFlagForcesFalse(t *testing.T) {
	transport, err := StartProcess(context.Background(), ProcessOptions{
		Command:                   "cmd.exe",
		Args:                      []string{"/c", "exit", "0"},
		DetachedChildrenUncovered: true,
	})
	if err != nil {
		t.Fatalf("start cmd.exe transport: %v", err)
	}
	if transport.CoversDetachedChildren() {
		t.Fatal("explicit DetachedChildrenUncovered was ignored")
	}
	_ = transport.Close()
}
