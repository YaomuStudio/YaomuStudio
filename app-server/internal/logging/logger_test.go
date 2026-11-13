package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

func TestLoggerRedactsSecretsAndBodies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.log")
	logger, err := New(Options{Path: path, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	token := "unit-test-instance-token"
	logger.Info("core", "test.event", map[string]any{
		"request_id":      "req-1",
		"token":           token,
		"authorization":   "Bearer " + token,
		"env":             map[string]any{"YAOMU_INSTANCE_TOKEN": token},
		"subtitle_text":   "这是一句不应落盘的字幕正文",
		"model_output":    strings.Repeat("x", 1000),
		"note":            "Bearer " + token,
		"api_key=greykey": "ok",
	})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	text := string(raw)
	for _, forbidden := range []string{token, "不应落盘的字幕正文", strings.Repeat("x", 600)} {
		if strings.Contains(text, forbidden) {
			t.Errorf("log contains forbidden content %q\nlog=%s", forbidden, text)
		}
	}
	for _, wanted := range []string{"req-1", "test.event", "[redacted]"} {
		if !strings.Contains(text, wanted) {
			t.Errorf("log missing %q\nlog=%s", wanted, text)
		}
	}
}

func TestLoggerRotatesBySize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.log")
	logger, err := New(Options{Path: path, MaxBytes: 200, Backups: 2})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	for i := 0; i < 20; i++ {
		logger.Info("core", "rotate.event", map[string]any{"n": i})
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated log missing: %v", err)
	}
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Fatal("backup count exceeded")
	}
}

func TestLoggerFallsBackWhenPathUnwritable(t *testing.T) {
	// 把目录本身当文件路径，打开必然失败。
	dir := t.TempDir()
	logger, err := New(Options{Path: dir})
	if err == nil {
		t.Fatal("New() error = nil, want open failure")
	}
	if !logger.Degraded() {
		t.Fatal("Degraded() = false, want true")
	}
	// 降级 logger 不应 panic。
	logger.Info("core", "fallback", map[string]any{"k": "v"})
}

func TestRedactText(t *testing.T) {
	out := RedactText("run token=abc123 ok\nBearer xyz")
	if strings.Contains(out, "abc123") || strings.Contains(out, "xyz") {
		t.Fatalf("RedactText() leaked secret: %q", out)
	}
	if strings.Contains(out, "\n") {
		t.Fatalf("RedactText() kept newline: %q", out)
	}
	pathOut := RedactText("open /Users/someone/Private/media/file.wav failed")
	if strings.Contains(pathOut, "/Users/someone") {
		t.Fatalf("RedactText() leaked home path: %q", pathOut)
	}
	if !strings.Contains(pathOut, "[path]") {
		t.Fatalf("RedactText() did not mark path: %q", pathOut)
	}
}
