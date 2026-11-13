// Package logging 是本地诊断日志：JSON 行、按大小轮转、写入前脱敏。
//
// 边界（docs/logging-and-privacy.md）：
//   - 默认不记录媒体/字幕正文、模型输入输出全文、令牌、密码、Cookie 或完整环境变量；
//   - 原始子进程输出只在有上限、脱敏后进入本地诊断；
//   - 诊断文件不可写时应用继续运行，但明确标记日志降级，不谎称记录成功。
package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 默认保留策略（可调整默认，不是实测需求）。
const (
	DefaultMaxBytes int64 = 5 * 1024 * 1024
	DefaultBackups        = 3
	// maxFieldLen 是单个日志字段的上限，防止异常栈/输出撑爆日志。
	maxFieldLen = 512
)

// sensitiveKeyFragments 是永不落盘的字段名片段（小写匹配）。
var sensitiveKeyFragments = []string{
	"token", "secret", "password", "passwd", "authorization", "cookie",
	"credential", "apikey", "api_key", "private_key", "instance_token",
	"bearer",
}

// bodyLikeKeyFragments 是媒体/字幕/模型输入输出正文的字段名片段。
// 诊断日志只记录 ID、大小、原因码与耗时，不记录正文。
var bodyLikeKeyFragments = []string{
	"subtitle", "transcript", "media", "audio_bytes", "text_body",
	"model_output", "model_input", "payload_body", "file_content", "raw_output",
}

// Logger 是并发安全的 JSON 行日志；轮转按 MaxBytes 滚动。
type Logger struct {
	mu        sync.Mutex
	path      string
	backups   int
	maxBytes  int64
	file      *os.File
	size      int64
	fallback  bool
	now       func() time.Time
	component string
}

// Options 是日志构造参数。
type Options struct {
	// Path 是日志文件路径；目录不存在时自动创建。
	Path string
	// MaxBytes 是单文件大小上限；<=0 使用默认 5 MiB。
	MaxBytes int64
	// Backups 是轮转保留份数；<0 表示不轮转，0 使用默认 3。
	Backups int
	// Component 是该 logger 的默认组件名。
	Component string
	// Fallback 是文件不可写时的输出目标；为空时写 os.Stderr。
	Fallback io.Writer
	// Now 便于测试注入时间。
	Now func() time.Time
}

// New 打开日志文件。文件不可用时返回一个写 Fallback 的降级 logger 与错误，
// 调用方可继续启动（诊断失败不阻断业务，但必须知道日志未落盘）。
func New(opts Options) (*Logger, error) {
	l := &Logger{
		path:      opts.Path,
		backups:   opts.Backups,
		maxBytes:  opts.MaxBytes,
		now:       opts.Now,
		component: opts.Component,
	}
	if l.backups == 0 {
		l.backups = DefaultBackups
	}
	if l.maxBytes <= 0 {
		l.maxBytes = DefaultMaxBytes
	}
	if l.now == nil {
		l.now = func() time.Time { return time.Now().UTC() }
	}
	if opts.Path == "" {
		l.fallback = true
		return l, fmt.Errorf("logging: empty log path; using fallback output")
	}
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o755); err != nil {
		l.fallback = true
		l.file = nil
		return l, fmt.Errorf("logging: create log dir: %w", err)
	}
	file, err := os.OpenFile(opts.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		l.fallback = true
		return l, fmt.Errorf("logging: open log file: %w", err)
	}
	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		l.fallback = true
		return l, fmt.Errorf("logging: stat log file: %w", statErr)
	}
	l.file = file
	l.size = info.Size()
	return l, nil
}

// Degraded 报告日志是否处于降级（写 stderr）状态。
func (l *Logger) Degraded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fallback || l.file == nil
}

// Path 返回日志文件路径（降级时仍返回配置路径，便于界面提示）。
func (l *Logger) Path() string { return l.path }

// Close 关闭文件。
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Field 是一组结构化字段的便捷构造。
func Field(key string, value any) map[string]any { return map[string]any{key: value} }

// Info 记录 info 级事件。
func (l *Logger) Info(component, event string, fields map[string]any) {
	l.write("info", component, event, fields)
}

// Warn 记录 warn 级事件。
func (l *Logger) Warn(component, event string, fields map[string]any) {
	l.write("warn", component, event, fields)
}

// Error 记录 error 级事件；err 只记录脱敏后的错误文本。
func (l *Logger) Error(component, event string, err error, fields map[string]any) {
	merged := Sanitize(fields)
	if err != nil {
		merged["error"] = truncate(RedactText(err.Error()))
	}
	l.write("error", component, event, merged)
}

func (l *Logger) write(level, component, event string, fields map[string]any) {
	entry := make(map[string]any, len(fields)+4)
	entry["ts"] = l.now().UTC().Format(time.RFC3339Nano)
	entry["level"] = level
	if component == "" {
		component = l.component
	}
	if component != "" {
		entry["component"] = component
	}
	entry["event"] = event
	for key, value := range Sanitize(fields) {
		entry[key] = value
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		if l.fallback {
			_, _ = fmt.Fprint(os.Stderr, string(line))
		}
		return
	}
	if l.backups >= 0 && l.size+int64(len(line)) > l.maxBytes {
		if err := l.rotateLocked(); err != nil {
			// 轮转失败不静默：降级到 stderr，并继续尝试写原文件。
			_, _ = fmt.Fprintf(os.Stderr, "logging: rotate failed: %v\n", RedactText(err.Error()))
		}
	}
	n, err := l.file.Write(line)
	l.size += int64(n)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "logging: write failed: %v\n", RedactText(err.Error()))
	}
}

func (l *Logger) rotateLocked() error {
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			return err
		}
		l.file = nil
	}
	if l.backups > 0 {
		oldest := fmt.Sprintf("%s.%d", l.path, l.backups)
		_ = os.Remove(oldest)
		for i := l.backups - 1; i >= 1; i-- {
			_ = os.Rename(fmt.Sprintf("%s.%d", l.path, i), fmt.Sprintf("%s.%d", l.path, i+1))
		}
		_ = os.Rename(l.path, l.path+".1")
	} else {
		_ = os.Remove(l.path)
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		l.fallback = true
		return err
	}
	l.file = file
	l.size = 0
	return nil
}

// Sanitize 返回脱敏后的字段副本：
//   - 敏感键（令牌、密码、Cookie、完整环境变量等）整键丢弃；
//   - 正文类键（字幕/媒体/模型输入输出）整键丢弃；
//   - 字符串截断到 maxFieldLen，并做令牌文本脱敏；
//   - map/slice 递归处理。
func Sanitize(fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields))
	for key, value := range fields {
		lower := strings.ToLower(key)
		if isSensitiveKey(lower) || isBodyLikeKey(lower) {
			out[key] = "[redacted]"
			continue
		}
		out[key] = sanitizeValue(value, 0)
	}
	return out
}

func isSensitiveKey(lower string) bool {
	if lower == "env" || lower == "environment" || lower == "envvars" {
		return true
	}
	for _, fragment := range sensitiveKeyFragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

func isBodyLikeKey(lower string) bool {
	for _, fragment := range bodyLikeKeyFragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

func sanitizeValue(value any, depth int) any {
	if depth > 4 {
		return "[truncated]"
	}
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		return truncate(RedactText(v))
	case []byte:
		return fmt.Sprintf("[%d bytes]", len(v))
	case map[string]any:
		return Sanitize(v)
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, sanitizeValue(item, depth+1))
		}
		return out
	default:
		return value
	}
}

// homePathPattern 匹配用户主目录下的完整路径；本地诊断只保留“某文件失败”，
// 不默认记录完整个人目录（docs/logging-and-privacy.md §2）。
var homePathPattern = regexp.MustCompile(`(?:/Users/[^/\s"]+|/home/[^/\s"]+|/root(?:/[^\s"]*)?|[A-Za-z]:\\Users\\[^\s"]+)(?:/[^\s"']*)?`)

// RedactText 去掉文本中的 Bearer 令牌、敏感 key=value 与完整个人目录路径。
func RedactText(text string) string {
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = homePathPattern.ReplaceAllString(text, "[path]")
	words := strings.Fields(text)
	for i, word := range words {
		if strings.EqualFold(word, "bearer") && i+1 < len(words) {
			words[i+1] = "[redacted]"
			continue
		}
		lower := strings.ToLower(word)
		for _, fragment := range sensitiveKeyFragments {
			if strings.HasPrefix(lower, fragment+"=") || strings.HasPrefix(lower, fragment+":") {
				words[i] = word[:strings.IndexAny(word, "=:")+1] + "[redacted]"
				break
			}
		}
	}
	return strings.Join(words, " ")
}

func truncate(text string) string {
	if len(text) <= maxFieldLen {
		return text
	}
	return text[:maxFieldLen] + "…"
}
