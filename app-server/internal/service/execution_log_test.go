package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionLogReadsLatestAttemptTail(t *testing.T) {
	svc := New(Options{DataDir: t.TempDir()})
	dir := filepath.Join(svc.cacheDir(), "execution-logs", "task-log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("attempt-1.log", `{"at":"2026-11-05T12:00:00Z","stream":"runner","line":"使用 mock 分离执行器","seq":1}
{"at":"2026-11-05T12:00:01Z","stream":"runner","line":"mock 分离完成","seq":2}
`)
	write("attempt-2.log", `{"at":"2026-11-05T12:01:00Z","stream":"runner","line":"启动 FlashCLI 分离","seq":1}
{"at":"2026-11-05T12:01:01Z","stream":"stderr","line":"虚构字幕：这是不该落盘的一句台词","seq":2}
{"at":"2026-11-05T12:01:02Z","stream":"runner","line":"FlashCLI 分离完成","seq":3}
{"at":"2026-11-05T12:01:03Z","stream":"stderr","line":"FLASHCLI_TOKEN=虚构密钥 /mnt/d/private/ref.wav","seq":4}
`)

	latest, err := svc.ExecutionLog(context.Background(), "task-log", 0, 10)
	if err != nil {
		t.Fatalf("ExecutionLog latest: %v", err)
	}
	if latest.Attempt != 2 || len(latest.Lines) != 2 || latest.Lines[1].Line != "FlashCLI 分离完成" {
		t.Fatalf("latest = %+v", latest)
	}
	for _, line := range latest.Lines {
		if line.Line == "" || strings.Contains(line.Line, "虚构字幕") ||
			strings.Contains(line.Line, "虚构密钥") || strings.Contains(line.Line, "/mnt/d/private") {
			t.Fatalf("raw text replayed: %+v", latest.Lines)
		}
	}

	one, err := svc.ExecutionLog(context.Background(), "task-log", 1, 1)
	if err != nil {
		t.Fatalf("ExecutionLog attempt 1: %v", err)
	}
	if one.Attempt != 1 || len(one.Lines) != 1 || one.Lines[0].Line != "mock 分离完成" {
		t.Fatalf("attempt 1 tail = %+v", one)
	}

	missing, err := svc.ExecutionLog(context.Background(), "task-missing", 0, 10)
	if err != nil {
		t.Fatalf("ExecutionLog missing: %v", err)
	}
	if missing.Attempt != 0 || missing.Lines == nil || len(missing.Lines) != 0 {
		t.Fatalf("missing = %+v", missing)
	}

	if _, err := svc.ExecutionLog(context.Background(), "../escape", 0, 10); err == nil {
		t.Fatal("path traversal task id accepted")
	}
}

func TestExecutionLogReplayDropsLegacyRawLines(t *testing.T) {
	svc := New(Options{DataDir: t.TempDir()})
	dir := filepath.Join(svc.cacheDir(), "execution-logs", "task-legacy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"at":"2026-11-05T12:00:00Z","stream":"stderr","line":"虚构字幕：这是不该落盘的一句台词","seq":1}
{"at":"2026-11-05T12:00:01Z","stream":"stderr","line":"FLASHCLI_TOKEN=虚构密钥","seq":2}
{"at":"2026-11-05T12:00:02Z","stream":"stderr","line":"/mnt/d/private/ref.wav 已加载","seq":3}
`
	if err := os.WriteFile(filepath.Join(dir, "attempt-1.log"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	result, err := svc.ExecutionLog(context.Background(), "task-legacy", 1, 10)
	if err != nil {
		t.Fatalf("ExecutionLog: %v", err)
	}
	if len(result.Lines) != 0 {
		t.Fatalf("legacy raw lines replayed: %+v", result.Lines)
	}
	// 回放过滤不删除、不改写用户已有 cache 文件。
	data, err := os.ReadFile(filepath.Join(dir, "attempt-1.log"))
	if err != nil {
		t.Fatalf("read legacy file: %v", err)
	}
	if string(data) != body {
		t.Fatal("replay filter rewrote the cache file")
	}
}
