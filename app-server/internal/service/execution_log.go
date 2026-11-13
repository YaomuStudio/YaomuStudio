package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/executionlog"
)

const (
	executionLogTailDefault = 200
	executionLogTailMax     = 1000
	executionLogReadBytes   = 256 * 1024
)

// ExecutionLog 读取某次 attempt 的执行过程日志尾部。
//
// 日志只是 cache 中的可丢弃反馈：文件缺失或已被清理时返回空列表，
// 不视为任务失败，也不查询业务表。
func (s *Service) ExecutionLog(_ context.Context, taskID string, attempt, tail int) (domain.ExecutionLog, error) {
	result := domain.ExecutionLog{
		TaskID: domain.ID(taskID),
		Lines:  []domain.ExecutionLogLine{},
	}
	if !safeExecutionLogTaskID(taskID) {
		return result, invalidRequestf("invalid task id")
	}
	if attempt < 0 {
		return result, invalidRequestf("attempt must be a positive integer")
	}
	if tail <= 0 {
		tail = executionLogTailDefault
	}
	if tail > executionLogTailMax {
		tail = executionLogTailMax
	}

	dir := filepath.Join(s.cacheDir(), "execution-logs", taskID)
	path, chosen, err := selectExecutionLogFile(dir, attempt)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, err
	}
	if path == "" {
		return result, nil
	}
	lines, err := readExecutionLogTail(path, tail)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, err
	}
	result.Attempt = chosen
	result.Lines = lines
	return result, nil
}

// safeExecutionLogTaskID 只接受单段目录名，拒绝路径分隔符与上级目录。
func safeExecutionLogTaskID(taskID string) bool {
	if taskID == "" || len(taskID) > 200 || taskID == "." || taskID == ".." {
		return false
	}
	return !strings.ContainsAny(taskID, `/\`)
}

// selectExecutionLogFile 返回指定 attempt 的文件；attempt<=0 时取编号最大的文件。
func selectExecutionLogFile(dir string, attempt int) (string, int, error) {
	if attempt > 0 {
		path := filepath.Join(dir, attemptFileName(attempt))
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return "", 0, nil
			}
			return "", 0, err
		}
		return path, attempt, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", 0, err
	}
	best := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "attempt-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		raw := strings.TrimSuffix(strings.TrimPrefix(name, "attempt-"), ".log")
		parsed, convErr := strconv.Atoi(raw)
		if convErr == nil && parsed > best {
			best = parsed
		}
	}
	if best == 0 {
		return "", 0, nil
	}
	return filepath.Join(dir, attemptFileName(best)), best, nil
}

func attemptFileName(attempt int) string {
	return "attempt-" + strconv.Itoa(attempt) + ".log"
}

// readExecutionLogTail 只读文件尾部，跳过起始处的半行，按 JSONL 解析。
func readExecutionLogTail(path string, tail int) ([]domain.ExecutionLogLine, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	start := info.Size() - executionLogReadBytes
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, executionLogReadBytes))
	if err != nil {
		return nil, err
	}
	if start > 0 {
		if index := bytes.IndexByte(data, '\n'); index >= 0 {
			data = data[index+1:]
		} else {
			data = nil
		}
	}

	lines := make([]domain.ExecutionLogLine, 0, tail)
	for _, raw := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var record struct {
			At     string `json:"at"`
			Stream string `json:"stream"`
			Line   string `json:"line"`
			Seq    int    `json:"seq"`
		}
		if json.Unmarshal(raw, &record) != nil || record.Line == "" {
			continue
		}
		// 回放与写入共用正向允许策略：旧 cache 中的未知自由文本不返回，
		// 但也不删除或改写文件本身。
		cleaned, ok := executionlog.Sanitize(record.Stream, record.Line)
		if !ok {
			continue
		}
		lines = append(lines, domain.ExecutionLogLine{
			Seq:    record.Seq,
			Stream: record.Stream,
			Line:   cleaned,
			At:     record.At,
		})
	}
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return lines, nil
}
