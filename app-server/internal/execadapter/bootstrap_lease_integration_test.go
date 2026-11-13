package execadapter

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestBootstrapModelOnlyLeaseWithRealRunner covers the packaged WSL route with
// a real local Runner process (macOS/Linux): a model-only bootstrap must create
// the workspace lease and remove it only after the Runner close is confirmed.
func TestBootstrapModelOnlyLeaseWithRealRunner(t *testing.T) {
	if testing.Short() {
		t.Skip("real runner integration")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 unavailable: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	runnerSrc := filepath.Join(repoRoot, "runner", "src")
	if _, err := os.Stat(filepath.Join(runnerSrc, "yaomustudio_runner")); err != nil {
		t.Skipf("runner src missing: %v", err)
	}
	t.Setenv("PYTHONPATH", runnerSrc)

	dataDir := t.TempDir()
	holder := NewRoutingHolder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		BootstrapRunners(ctx, ctx, RunnerBootstrapOptions{
			DataDir:           dataDir,
			LeaseOwner:        LeaseOwner{WorkspaceID: "ws-real", InstanceID: "inst-real", Epoch: 1},
			ModelCommand:      python,
			ModelArgs:         []string{"-m", "yaomustudio_runner"},
			ModelRequired:     true,
			Holder:            holder,
			modelHelloTimeout: 20 * time.Second,
		})
	}()

	deadline := time.Now().Add(30 * time.Second)
	for holder.Readiness() != ExecutorReadinessReady && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if holder.Readiness() != ExecutorReadinessReady {
		_ = holder.Close()
		<-done
		t.Fatalf("readiness = %s, want ready", holder.Readiness())
	}
	ownerPath := filepath.Join(dataDir, "runtime", "runner-lease", "owner.json")
	data, err := os.ReadFile(ownerPath)
	if err != nil {
		_ = holder.Close()
		<-done
		t.Fatalf("read runner lease: %v", err)
	}
	var owner LeaseOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		t.Fatalf("decode runner lease: %v", err)
	}
	if owner.RunnerID == "" || owner.WorkspaceID != "ws-real" {
		t.Fatalf("lease owner = %+v, want recorded runner and workspace", owner)
	}

	if err := holder.Close(); err != nil {
		t.Fatalf("holder close: %v", err)
	}
	<-done
	if _, err := os.Stat(ownerPath); !os.IsNotExist(err) {
		t.Fatalf("lease survived confirmed Runner close: %v", err)
	}
}
