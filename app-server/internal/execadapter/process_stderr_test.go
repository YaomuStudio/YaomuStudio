//go:build !windows

package execadapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
)

func TestProcessTransportDropsRunnerStderrContent(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	logPath := filepath.Join(t.TempDir(), "core.log")
	logger, err := logging.New(logging.Options{Path: logPath})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	script := `import sys
sys.stderr.write("FlashCLI 分离完成\n")
sys.stderr.write("虚构字幕：这是不该落盘的一句台词\n")
sys.stderr.write("FLASHCLI_TOKEN=虚构密钥 /mnt/d/private/ref.wav\n")
sys.stderr.write("CUDA_VISIBLE_DEVICES=0\n")
sys.stderr.flush()
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport, err := StartProcess(ctx, ProcessOptions{
		Command: python,
		Args:    []string{"-I", "-c", script},
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	// 等脚本自然退出，确保 stderr 已写入管道后再关闭；立刻 Kill 会抢在
	// Python 解释器启动前终止进程，测试就失去观测对象。
	exitDeadline := time.Now().Add(5 * time.Second)
	for !transport.Exited() && time.Now().Before(exitDeadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !transport.Exited() {
		t.Fatal("stderr fixture did not exit")
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	var text string
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(logPath)
		if readErr != nil {
			t.Fatalf("read log: %v", readErr)
		}
		text = string(data)
		if strings.Contains(text, "runner.stderr_suppressed") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = logger.Close()

	for _, raw := range []string{"虚构字幕", "虚构密钥", "CUDA_VISIBLE_DEVICES", "/mnt/d/private"} {
		if strings.Contains(text, raw) {
			t.Fatalf("runner stderr content persisted (%q): %s", raw, text)
		}
	}
	if !strings.Contains(text, "FlashCLI 分离完成") {
		t.Fatalf("controlled runner line was dropped: %s", text)
	}
	if !strings.Contains(text, "runner.stderr_suppressed") {
		t.Fatalf("suppression was not reported: %s", text)
	}
}
