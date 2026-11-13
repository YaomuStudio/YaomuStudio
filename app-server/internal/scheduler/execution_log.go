package scheduler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/events"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/executionlog"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
)

const (
	executionLogDirName   = "execution-logs"
	executionLogMaxBytes  = 2 << 20
	executionLogEventType = "task.execution_log"
)

type executionLogRecord struct {
	At     string `json:"at"`
	Stream string `json:"stream"`
	Line   string `json:"line"`
	Seq    int    `json:"seq"`
}

// executionLogReporter 把可丢弃执行过程行写入 cache 并发布实时事件。
//
// 日志不是成功条件：写入失败只记本地警告，不改变任务终态。
type executionLogReporter struct {
	bus     *events.Bus
	log     *logging.Logger
	taskID  domain.ID
	attempt int
	now     func() time.Time

	mu        sync.Mutex
	file      *os.File
	written   int64
	seq       int
	truncated bool
	closed    bool
}

func newExecutionLogReporter(
	dataDir string,
	bus *events.Bus,
	log *logging.Logger,
	taskID domain.ID,
	attempt int,
	now func() time.Time,
) *executionLogReporter {
	reporter := &executionLogReporter{
		bus:     bus,
		log:     log,
		taskID:  taskID,
		attempt: attempt,
		now:     now,
	}
	if dataDir == "" {
		return reporter
	}
	name, ok := safeExecutionLogName(string(taskID))
	if !ok {
		reporter.warn("invalid task id for execution log")
		return reporter
	}
	dir := filepath.Join(dataDir, "cache", executionLogDirName, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		reporter.warn("create execution log dir failed: " + err.Error())
		return reporter
	}
	file, err := os.OpenFile(
		filepath.Join(dir, fmt.Sprintf("attempt-%d.log", attempt)),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		reporter.warn("open execution log failed: " + err.Error())
		return reporter
	}
	reporter.file = file
	if info, statErr := file.Stat(); statErr == nil {
		reporter.written = info.Size()
	}
	return reporter
}

func (r *executionLogReporter) report(stream, line string, seq int) {
	if r == nil {
		return
	}
	cleaned, ok := executionlog.Sanitize(stream, line)
	if !ok {
		// 未知自由文本不写 cache、不发布 SSE；过滤不改变任务终态。
		return
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	if seq <= 0 || seq <= r.seq {
		r.seq++
		seq = r.seq
	} else {
		r.seq = seq
	}
	notice := ""
	noticeSeq := 0
	if r.file != nil && !r.truncated {
		record := executionLogRecord{
			At:     r.now().UTC().Format(time.RFC3339Nano),
			Stream: stream,
			Line:   cleaned,
			Seq:    seq,
		}
		data, err := json.Marshal(record)
		if err != nil {
			r.warn("encode execution log failed: " + err.Error())
			r.truncated = true
		} else {
			data = append(data, '\n')
			if r.written+int64(len(data)) > executionLogMaxBytes {
				r.truncated = true
				notice = executionlog.FileCapNotice
				// 上限提示与触发行各自占用独立的 attempt-local seq：同号会被 UI 去重丢掉一行。
				// 保持既有“先提示、后触发行”的发布顺序，因此提示取当前序号、触发行顺延；
				// 后续来源 seq <= r.seq 时会继续递增，不会与这两条消息碰撞。
				noticeSeq = seq
				r.seq++
				seq = r.seq
			} else if _, writeErr := r.file.Write(data); writeErr != nil {
				r.warn("write execution log failed: " + writeErr.Error())
				r.truncated = true
			} else {
				r.written += int64(len(data))
			}
		}
	}
	// 发布与 Close 由同一把锁排序：Close 返回后不再发布；已排队的 live 仍可能稍后送达。
	if notice != "" {
		r.publish("runner", notice, noticeSeq)
	}
	r.publish(stream, cleaned, seq)
	r.mu.Unlock()
}

func (r *executionLogReporter) publish(stream, line string, seq int) {
	if r.bus == nil {
		return
	}
	r.bus.PublishLive(domain.DomainEvent{
		SchemaVersion: 1,
		Type:          executionLogEventType,
		TaskID:        r.taskID,
		Data: map[string]any{
			"task_id": string(r.taskID),
			"attempt": r.attempt,
			"stream":  stream,
			"line":    line,
			"seq":     seq,
		},
	})
}

func (r *executionLogReporter) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
}

func (r *executionLogReporter) warn(message string) {
	if r.log != nil {
		r.log.Warn("scheduler", "scheduler.execution_log", map[string]any{
			"task_id": string(r.taskID),
			"attempt": r.attempt,
			"error":   message,
		})
	}
}

// safeExecutionLogName 限制任务目录名，避免路径分隔符或上级目录逃逸。
func safeExecutionLogName(id string) (string, bool) {
	if id == "" || len(id) > 200 || id == "." || id == ".." {
		return "", false
	}
	if strings.ContainsAny(id, `/\`) {
		return "", false
	}
	return id, true
}
