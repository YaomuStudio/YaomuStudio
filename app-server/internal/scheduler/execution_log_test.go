package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/events"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/executionlog"
)

func TestExecutionLogReporterWritesFileAndPublishesLiveEvent(t *testing.T) {
	e := newEnv(t, envOptions{executor: &fakeExecutor{}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan domain.DomainEvent, 4)
	go func() {
		_ = e.bus.Stream(ctx, 0, func(ev domain.DomainEvent) error {
			events <- ev
			return nil
		})
	}()
	// 等订阅注册完成；Stream 先注册再补读，之后才进入实时 select。
	time.Sleep(50 * time.Millisecond)

	reporter := newExecutionLogReporter(e.data, e.bus, e.log, "task-log", 2, func() time.Time {
		return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	})
	reporter.report("runner", "FlashCLI 分离完成", 7)
	reporter.Close()

	path := filepath.Join(e.data, "cache", "execution-logs", "task-log", "attempt-2.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var record struct {
		At     string `json:"at"`
		Stream string `json:"stream"`
		Line   string `json:"line"`
		Seq    int    `json:"seq"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &record); err != nil {
		t.Fatalf("decode log: %v (%q)", err, data)
	}
	if record.Stream != "runner" || record.Line != "FlashCLI 分离完成" || record.Seq != 7 {
		t.Fatalf("record = %+v", record)
	}
	if record.At != "2026-11-05T12:00:00Z" {
		t.Fatalf("at = %q", record.At)
	}

	select {
	case ev := <-events:
		if ev.Type != executionLogEventType || ev.Seq != 0 || ev.TaskID != "task-log" {
			t.Fatalf("event = %+v", ev)
		}
		if ev.Data["line"] != "FlashCLI 分离完成" || ev.Data["attempt"] != 2 || ev.Data["seq"] != 7 {
			t.Fatalf("event data = %+v", ev.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no live execution log event")
	}
}

func TestExecutionLogReporterDropsFreeFormText(t *testing.T) {
	e := newEnv(t, envOptions{executor: &fakeExecutor{}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan domain.DomainEvent, 4)
	go func() {
		_ = e.bus.Stream(ctx, 0, func(ev domain.DomainEvent) error {
			events <- ev
			return nil
		})
	}()
	time.Sleep(50 * time.Millisecond)

	reporter := newExecutionLogReporter(e.data, e.bus, e.log, "task-raw", 1, time.Now)
	reporter.report("stderr", "虚构字幕：这是不该落盘的一句台词", 1)
	reporter.report("stderr", "FLASHCLI_TOKEN=虚构密钥 /mnt/d/private/ref.wav", 2)
	reporter.report("stderr", "CUDA_VISIBLE_DEVICES=0", 3)
	reporter.Close()

	path := filepath.Join(e.data, "cache", "execution-logs", "task-raw", "attempt-1.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("free-form text persisted: %q", data)
	}
	select {
	case ev := <-events:
		t.Fatalf("free-form text published: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestExecutionLogReporterStopsAtSizeCap(t *testing.T) {
	e := newEnv(t, envOptions{executor: &fakeExecutor{}})
	dir := filepath.Join(e.data, "cache", "execution-logs", "task-cap")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "attempt-1.log")
	existing := strings.Repeat("x", executionLogMaxBytes)
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	reporter := newExecutionLogReporter(e.data, nil, e.log, "task-cap", 1, time.Now)
	reporter.report("runner", "FlashCLI 分离完成", 1)
	reporter.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(data) != executionLogMaxBytes {
		t.Fatalf("log size = %d, want %d", len(data), executionLogMaxBytes)
	}
}

// waitForLiveSubscription 以可丢弃实时事件作就绪屏障：总线只有在订阅注册后
// 才会投递，未就绪时重新发送，避免用固定 sleep 猜调度时机。
func waitForLiveSubscription(t *testing.T, bus *events.Bus, received <-chan domain.DomainEvent) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	retry := time.NewTicker(5 * time.Millisecond)
	defer retry.Stop()
	for {
		bus.PublishLive(domain.DomainEvent{Type: "test.ready", Data: map[string]any{}})
		select {
		case <-received:
			return
		case <-retry.C:
		case <-deadline.C:
			t.Fatal("event subscription was not established")
			return
		}
	}
}

func waitForExecutionLogs(t *testing.T, received <-chan domain.DomainEvent, want int) []domain.DomainEvent {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	logs := make([]domain.DomainEvent, 0, want)
	others := 0
	for len(logs) < want {
		select {
		case ev := <-received:
			if ev.Type == executionLogEventType {
				logs = append(logs, ev)
			} else {
				others++
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for %d execution log events, got %d (others %d): %+v",
				want, len(logs), others, logs)
		}
	}
	return logs
}

func TestExecutionLogReporterCapNoticeAndLineUseDistinctSeq(t *testing.T) {
	e := newEnv(t, envOptions{executor: &fakeExecutor{}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan domain.DomainEvent, 16)
	go func() {
		_ = e.bus.Stream(ctx, 0, func(ev domain.DomainEvent) error {
			select {
			case received <- ev:
			case <-ctx.Done():
			}
			return nil
		})
	}()
	waitForLiveSubscription(t, e.bus, received)

	dir := filepath.Join(e.data, "cache", "execution-logs", "task-cap-seq")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "attempt-1.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", executionLogMaxBytes)), 0o600); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	reporter := newExecutionLogReporter(e.data, e.bus, e.log, "task-cap-seq", 1, time.Now)
	reporter.report("runner", "FlashCLI 分离完成", 7)
	// 后续来源 seq 与提示/触发行已用序号重叠时也必须继续递增。
	reporter.report("runner", "启动 FlashCLI 分离", 8)
	reporter.Close()

	logs := waitForExecutionLogs(t, received, 3)
	if logs[0].Data["line"] != executionlog.FileCapNotice {
		t.Fatalf("first line = %v, want file cap notice", logs[0].Data["line"])
	}
	if logs[1].Data["line"] != "FlashCLI 分离完成" {
		t.Fatalf("second line = %v, want triggering line", logs[1].Data["line"])
	}
	if logs[2].Data["line"] != "启动 FlashCLI 分离" {
		t.Fatalf("third line = %v", logs[2].Data["line"])
	}
	seqs := make([]int, len(logs))
	for i, ev := range logs {
		seq, ok := ev.Data["seq"].(int)
		if !ok || seq <= 0 {
			t.Fatalf("event %d seq = %#v", i, ev.Data["seq"])
		}
		seqs[i] = seq
	}
	if seqs[0] == seqs[1] {
		t.Fatalf("cap notice and triggering line share seq %d; UI dedup would drop one", seqs[0])
	}
	if seqs[0] >= seqs[1] || seqs[1] >= seqs[2] {
		t.Fatalf("seqs not independently increasing: %v", seqs)
	}
}

func TestExecutionLogReporterDoesNotPublishAfterClose(t *testing.T) {
	e := newEnv(t, envOptions{executor: &fakeExecutor{}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan domain.DomainEvent, 16)
	go func() {
		_ = e.bus.Stream(ctx, 0, func(ev domain.DomainEvent) error {
			select {
			case received <- ev:
			case <-ctx.Done():
			}
			return nil
		})
	}()
	waitForLiveSubscription(t, e.bus, received)

	reporter := newExecutionLogReporter(e.data, e.bus, e.log, "task-closed", 1, time.Now)
	reporter.Close()
	reporter.report("runner", "FlashCLI 分离完成", 1)

	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case ev := <-received:
			if ev.Type == executionLogEventType {
				t.Fatalf("event published after Close: %+v", ev)
			}
		case <-deadline:
			return
		}
	}
}

func TestSafeExecutionLogNameRejectsTraversal(t *testing.T) {
	for _, id := range []string{"", ".", "..", "a/b", `a\b`, strings.Repeat("a", 201)} {
		if _, ok := safeExecutionLogName(id); ok {
			t.Errorf("safeExecutionLogName(%q) accepted", id)
		}
	}
	if name, ok := safeExecutionLogName("task-123"); !ok || name != "task-123" {
		t.Fatalf("safeExecutionLogName(task-123) = %q, %v", name, ok)
	}
}
