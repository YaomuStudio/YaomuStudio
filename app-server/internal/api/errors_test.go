package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
)

// 客户端超时/断开导致的取消不是服务端故障：必须记 warn 事件而不是
// api.error，否则用户按提示查日志只会看到误导性的 CORE_NOT_READY 错误。
func TestCanceledRequestLogsDistinctWarnEvent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "core.log")
	logger, err := logging.New(logging.Options{Path: logPath})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	srv := New(Config{Log: logger})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow-runs", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	srv.writeError(rec, req, context.Canceled)

	if err := logger.Close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("log lines = %d, want 1: %s", len(lines), raw)
	}
	line := lines[0]
	if !strings.Contains(line, `"event":"api.request_canceled"`) {
		t.Fatalf("log line missing api.request_canceled: %s", line)
	}
	if !strings.Contains(line, `"level":"warn"`) {
		t.Fatalf("log line is not warn: %s", line)
	}
	if strings.Contains(line, `"event":"api.error"`) {
		t.Fatalf("canceled request logged as api.error: %s", line)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// 非取消类错误保持 error 级别与原有事件名，不改变既有诊断口径。
func TestFailedRequestStillLogsErrorEvent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "core.log")
	logger, err := logging.New(logging.Options{Path: logPath})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	srv := New(Config{Log: logger})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow-runs", nil)
	rec := httptest.NewRecorder()
	srv.writeError(rec, req, os.ErrPermission)

	if err := logger.Close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(raw), `"event":"api.error"`) {
		t.Fatalf("log missing api.error: %s", raw)
	}
}
