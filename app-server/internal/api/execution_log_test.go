package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func TestTaskExecutionLogEndpoint(t *testing.T) {
	env := newTestEnv(t)
	taskID := "task-log-1"
	dir := filepath.Join(env.dataDir, "cache", "execution-logs", taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"at":"2026-11-05T12:00:00Z","stream":"runner","line":"使用 mock 分离执行器","seq":1}
{"at":"2026-11-05T12:00:01Z","stream":"runner","line":"FlashCLI 分离完成","seq":2}
`
	if err := os.WriteFile(filepath.Join(dir, "attempt-1.log"), []byte(body), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	res := env.expect(
		env.request(http.MethodGet, "/api/v1/tasks/"+taskID+"/execution-log?tail=1", nil),
		http.StatusOK,
	)
	log := decodeInto[domain.ExecutionLog](t, res)
	if log.TaskID != domain.ID(taskID) || log.Attempt != 1 || len(log.Lines) != 1 {
		t.Fatalf("log = %+v", log)
	}
	if log.Lines[0].Line != "FlashCLI 分离完成" || log.Lines[0].Stream != "runner" || log.Lines[0].Seq != 2 {
		t.Fatalf("line = %+v", log.Lines[0])
	}

	usage := decodeInto[domain.StorageUsage](
		t,
		env.expect(env.request(http.MethodGet, "/api/v1/storage/usage", nil), http.StatusOK),
	)
	if usage.CacheBytes < int64(len(body)) {
		t.Fatalf("cache bytes = %d, want at least %d", usage.CacheBytes, len(body))
	}

	// cache 清理覆盖 execution-logs；清理后端点返回空列表，不影响应用。
	env.expect(
		env.request(http.MethodPost, "/api/v1/storage/cleanup", map[string]any{"category": "cache"}),
		http.StatusOK,
	)
	cleaned := decodeInto[domain.ExecutionLog](t, env.expect(
		env.request(http.MethodGet, "/api/v1/tasks/"+taskID+"/execution-log", nil),
		http.StatusOK,
	))
	if cleaned.Attempt != 0 || len(cleaned.Lines) != 0 {
		t.Fatalf("log after cleanup = %+v", cleaned)
	}

	empty := env.expect(
		env.request(http.MethodGet, "/api/v1/tasks/task-missing/execution-log", nil),
		http.StatusOK,
	)
	missing := decodeInto[domain.ExecutionLog](t, empty)
	if missing.Attempt != 0 || len(missing.Lines) != 0 {
		t.Fatalf("missing = %+v", missing)
	}

	env.expect(
		env.request(http.MethodGet, "/api/v1/tasks/"+taskID+"/execution-log?tail=0", nil),
		http.StatusBadRequest,
	)
}
