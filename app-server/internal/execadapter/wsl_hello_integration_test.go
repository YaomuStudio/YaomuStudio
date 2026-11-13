package execadapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 本机有 WSL + python3 时验证 wsl.exe 拉起 Runner hello；否则 Skip。
func TestWSLRunnerHelloIntegration(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("WSL integration is Windows-only")
	}
	if _, err := exec.LookPath("wsl.exe"); err != nil {
		t.Skip("wsl.exe not found")
	}
	if err := exec.Command("wsl.exe", "-e", "true").Run(); err != nil {
		t.Skipf("wsl not usable: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	runnerSrc := filepath.Join(repoRoot, "runner", "src")
	if _, err := os.Stat(filepath.Join(runnerSrc, "yaomustudio_runner")); err != nil {
		t.Skipf("runner src missing: %v", err)
	}
	wslSrc := WindowsToWSL(runnerSrc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, err := NewProcessClient(ctx, ProcessOptions{
		Command: "wsl.exe",
		Args:    []string{"-e", "env", "PYTHONPATH=" + wslSrc, "python3", "-m", "yaomustudio_runner"},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = client.Close() }()

	helloCtx, helloCancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer helloCancel()
	info, err := client.Start(helloCtx)
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	joined := strings.Join(info.Adapters, ",")
	if !strings.Contains(joined, "separate") || !strings.Contains(joined, "voice_clone") {
		t.Fatalf("adapters = %v", info.Adapters)
	}
}
