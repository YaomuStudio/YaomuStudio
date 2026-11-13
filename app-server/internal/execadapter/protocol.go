// Package execadapter 是 Core 与 Runner 之间的执行适配层（stdio 协议帧 v1）。
//
// 依据：docs/local-api-contract.md §6 与 contracts/openapi.yaml 的
// x-execution-protocol；帧结构以 runner/src/yaomustudio_runner/protocol.py 为对端。
//
// 边界：
//   - 只跑本应用拥有的进程；取消超时后只终止该进程树；
//   - stdout 只解析有大小上限的协议帧，stderr 经受控摘要过滤后进本地诊断；
//   - 不构造 FlashCLI 参数，不解释 bundle 内容。
package execadapter

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/executionlog"
)

// ProtocolVersion 是当前执行协议版本。
const ProtocolVersion = 1

// MaxFrameBytes 是单帧上限，与 Runner 侧一致（1 MiB）。
const MaxFrameBytes = 1024 * 1024

// 请求帧操作（Core -> Runner）。
const (
	OpHello    = "hello"
	OpProbe    = "probe"
	OpExecute  = "execute"
	OpCancel   = "cancel"
	OpShutdown = "shutdown"
)

// 事件/应答帧操作（Runner -> Core）。
const (
	OpHeartbeat = "heartbeat"
	OpProgress  = "progress"
	OpLog       = "log"
	OpResult    = "result"
	OpError     = "error"
)

// MaxLogLineChars 与 Runner / executionlog 策略一致：单行执行过程文本上限。
const MaxLogLineChars = executionlog.MaxLineChars

// Frame 是一帧协议消息。
type Frame struct {
	V       int            `json:"v"`
	Op      string         `json:"op"`
	ReqID   string         `json:"req_id"`
	Payload map[string]any `json:"payload"`
}

// ComponentRef 是模型/适配器/运行时的精确版本引用。
type ComponentRef struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

// Request 是绑定一次执行的完整请求。
type Request struct {
	Workspace     string
	ServerEpoch   int64
	TaskID        string
	StepID        string
	Attempt       int
	Model         ComponentRef
	Adapter       ComponentRef
	Runtime       ComponentRef
	Params        map[string]any
	InputAssetIDs []string
	OutputDir     string
}

// Payload 把请求编码为 execute 帧载荷（字段与协议对端一致）。
func (r Request) Payload() map[string]any {
	assets := r.InputAssetIDs
	if assets == nil {
		assets = []string{}
	}
	params := make(map[string]any, len(r.Params))
	for key, value := range r.Params {
		if key != "_core" {
			params[key] = value
		}
	}
	return map[string]any{
		"workspace":       r.Workspace,
		"server_epoch":    strconv.FormatInt(r.ServerEpoch, 10),
		"task_id":         r.TaskID,
		"step_id":         r.StepID,
		"attempt":         r.Attempt,
		"model":           map[string]any{"id": r.Model.ID, "digest": r.Model.Digest},
		"adapter":         map[string]any{"id": r.Adapter.ID, "digest": r.Adapter.Digest},
		"runtime":         map[string]any{"id": r.Runtime.ID, "digest": r.Runtime.Digest},
		"params":          params,
		"input_asset_ids": assets,
		"output_dir":      r.OutputDir,
	}
}

// Binding 是执行结果携带的绑定信息，用于丢弃迟到结果。
type Binding struct {
	Workspace   string
	ServerEpoch int64
	TaskID      string
	StepID      string
	Attempt     int
}

// Matches requires every execution identity field. Zero/missing values are never
// a wildcard: an unbound result must not reach artifact preparation.
func (b Binding) Matches(req Request) bool {
	return b.Workspace == req.Workspace && b.Workspace != "" &&
		b.ServerEpoch == req.ServerEpoch && b.ServerEpoch > 0 &&
		b.TaskID == req.TaskID && b.TaskID != "" &&
		b.StepID == req.StepID && b.StepID != "" &&
		b.Attempt == req.Attempt && b.Attempt > 0
}

// ExecError 是 Runner 返回的安全错误（原始输出不进这里）。
type ExecError struct {
	Code      string
	Message   string
	Retryable bool
	ActionIDs []string
}

// Output 是适配器回传的一个产物描述。
//
// RelPath 只允许相对 attempt 输出目录；Core 必须校验后再落盘到用户授权目录。
type Output struct {
	RelPath   string
	SizeBytes int64
	// Media 是可选媒体属性（ffprobe 结果摘要）；工具缺失时为空。
	Media map[string]any
}

// Result 是一次执行的结果。
type Result struct {
	ArtifactIDs []string
	Outputs     []Output
	Error       *ExecError
	Binding     Binding
	// OutputDir 是本次尝试的专属输出目录（Core 分配）；产物路径以它为根校验。
	OutputDir string
	// TransportErr 表示连接/进程层失败（进程退出、帧损坏、握手失败）。
	TransportErr error
	// StopUnconfirmed keeps the executor and its resource reservations isolated.
	// A broken transport or a dead Runner leader alone cannot prove media children stopped.
	StopUnconfirmed bool
}

// ProgressFunc 接收可丢弃的进度帧（落库节流由调度层做）。
type ProgressFunc func(stage string, current, total int)

// LogFunc 接收可丢弃的执行过程行；line 已由 Runner 脱敏，seq 为 attempt 内单调序号。
type LogFunc func(stream, line string, seq int)

// HelloInfo 是 hello 应答摘要。
type HelloInfo struct {
	RunnerVersion   string
	ProtocolVersion int
	Adapters        []string
	MediaTools      map[string]any
}

// CancelResult 是一次取消确认。
type CancelResult struct {
	Exited bool
	Active bool
}

func encodeFrame(f Frame) ([]byte, error) {
	if f.V == 0 {
		f.V = ProtocolVersion
	}
	if f.V != ProtocolVersion {
		return nil, fmt.Errorf("unsupported protocol version %d", f.V)
	}
	if f.Op == "" || f.ReqID == "" {
		return nil, fmt.Errorf("frame op/req_id required")
	}
	if f.Payload == nil {
		f.Payload = map[string]any{}
	}
	body, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("encode frame: %w", err)
	}
	if len(body) > MaxFrameBytes {
		return nil, fmt.Errorf("frame exceeds %d bytes", MaxFrameBytes)
	}
	return append(body, '\n'), nil
}

func decodeFrame(line []byte) (Frame, error) {
	if len(line) > MaxFrameBytes+2 {
		return Frame{}, fmt.Errorf("frame exceeds %d bytes", MaxFrameBytes)
	}
	var f Frame
	if err := json.Unmarshal(line, &f); err != nil {
		return Frame{}, fmt.Errorf("decode frame: %w", err)
	}
	if f.V != ProtocolVersion {
		return Frame{}, fmt.Errorf("unsupported protocol version %d", f.V)
	}
	if f.Op == "" {
		return Frame{}, fmt.Errorf("frame op required")
	}
	if f.Payload == nil {
		f.Payload = map[string]any{}
	}
	return f, nil
}

// parseBinding 从帧载荷读取绑定信息；缺失字段保持零值。
func parseBinding(payload map[string]any) Binding {
	var b Binding
	if raw, ok := payload["binding"].(map[string]any); ok {
		payload = raw
	}
	if v, ok := payload["workspace"].(string); ok {
		b.Workspace = v
	}
	if v, ok := payload["server_epoch"]; ok {
		b.ServerEpoch = parseEpoch(v)
	}
	if v, ok := payload["task_id"].(string); ok {
		b.TaskID = v
	}
	if v, ok := payload["step_id"].(string); ok {
		b.StepID = v
	}
	if v, ok := payload["attempt"]; ok {
		b.Attempt = parseAttempt(v)
	}
	return b
}

func parseEpoch(value any) int64 {
	switch v := value.(type) {
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			return n
		}
	case float64:
		if !math.IsNaN(v) && !math.IsInf(v, 0) && math.Trunc(v) == v && v > 0 && v < math.MaxInt64 {
			return int64(v)
		}
	case int:
		return int64(v)
	case int64:
		return v
	}
	return 0
}

func parseAttempt(value any) int {
	switch v := value.(type) {
	case float64:
		if !math.IsNaN(v) && !math.IsInf(v, 0) && math.Trunc(v) == v && v > 0 && v < math.MaxInt {
			return int(v)
		}
	case int:
		return v
	case int64:
		return int(v)
	case string:
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return 0
}

// artifacts 从 result 帧的 data.artifact_ids 读取产物清单。
func artifacts(payload map[string]any) []string {
	data, ok := payload["data"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := data["artifact_ids"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parseOutputs 从 result 帧的 data.outputs 读取产物清单。
//
// 只解析结构，不校验路径是否逃逸：那属于控制面（service）的责任。
func parseOutputs(payload map[string]any) []Output {
	data, ok := payload["data"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := data["outputs"].([]any)
	if !ok {
		return nil
	}
	out := make([]Output, 0, len(raw))
	for _, item := range raw {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		relPath, _ := record["rel_path"].(string)
		if relPath == "" {
			continue
		}
		output := Output{RelPath: relPath}
		switch size := record["size_bytes"].(type) {
		case float64:
			output.SizeBytes = int64(size)
		case int64:
			output.SizeBytes = size
		case int:
			output.SizeBytes = int64(size)
		}
		if media, ok := record["media"].(map[string]any); ok {
			output.Media = media
		}
		out = append(out, output)
	}
	return out
}

// parseLog 读取 log 帧载荷；只接受白名单 stream 与非空、≤512 字符的行。
func parseLog(payload map[string]any) (stream, line string, seq int, ok bool) {
	stream, _ = payload["stream"].(string)
	if stream != "stderr" && stream != "stdout" && stream != "runner" {
		return "", "", 0, false
	}
	line, _ = payload["line"].(string)
	if line == "" || utf8.RuneCountInString(line) > MaxLogLineChars {
		return "", "", 0, false
	}
	seq = parseAttempt(payload["seq"])
	return stream, line, seq, true
}

// logBindingMatches 丢弃迟到或未绑定的过程行；缺失绑定字段一律不接收。
func logBindingMatches(payload map[string]any, req Request) bool {
	taskID, _ := payload["task_id"].(string)
	if taskID != req.TaskID || taskID == "" {
		return false
	}
	if parseAttempt(payload["attempt"]) != req.Attempt {
		return false
	}
	if epoch := parseEpoch(payload["server_epoch"]); epoch != req.ServerEpoch {
		return false
	}
	if stepID, ok := payload["step_id"].(string); ok && stepID != req.StepID {
		return false
	}
	return true
}

// progressBindingMatches 要求进度帧的顶层 task/step/attempt/epoch 与当前请求
// 精确一致。进度帧不携带 workspace，因此不复用 Result.Binding.Matches。
// 零值/缺失字段永远不是通配：未绑定的进度不能写进另一尝试的快照。
func progressBindingMatches(payload map[string]any, req Request) bool {
	if req.TaskID == "" || req.StepID == "" || req.Attempt <= 0 || req.ServerEpoch <= 0 {
		return false
	}
	taskID, _ := payload["task_id"].(string)
	if taskID != req.TaskID {
		return false
	}
	stepID, _ := payload["step_id"].(string)
	if stepID != req.StepID {
		return false
	}
	if parseAttempt(payload["attempt"]) != req.Attempt {
		return false
	}
	return parseEpoch(payload["server_epoch"]) == req.ServerEpoch
}

// maxSafeJSONInteger 是 JSON 数字解码为 float64 后仍能精确表示的最大整数。
//
// 超过它的相邻整数会折叠成同一个 float64（例如 2^53 与 2^53+1），
// 可能把 current<total 伪造成 current==total 的 100%；协议计数只支持到该边界。
const maxSafeJSONInteger = 1<<53 - 1

// parseProgressCount 只接受合法的非负 JSON 整数。
//
// 缺失/小数/负数/溢出返回 false，不能像 parseAttempt 那样回退为 0，
// 否则会伪造出 0/total 的假进度。float64（JSON 解码结果）另受
// 2^53-1 安全整数上限约束；本地 int/int64 精确值不受此限。
func parseProgressCount(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v {
			return 0, false
		}
		if v < 0 || v > float64(math.MaxInt) || v > maxSafeJSONInteger {
			return 0, false
		}
		n := int(v)
		// float64(math.MaxInt) 在 64 位平台会舍入到 2^63；转换越界后必须拒绝，
		// 不能把溢出结果当成合法计数。
		if n < 0 || float64(n) != v {
			return 0, false
		}
		return n, true
	case int:
		if v < 0 {
			return 0, false
		}
		return v, true
	case int64:
		if v < 0 || v > int64(math.MaxInt) {
			return 0, false
		}
		return int(v), true
	default:
		return 0, false
	}
}

// parseProgressFrame 校验进度帧的绑定与计数；total<=0 或 current>total
// 视为整帧非法，不推断百分比。
func parseProgressFrame(payload map[string]any, req Request) (current, total int, ok bool) {
	if !progressBindingMatches(payload, req) {
		return 0, 0, false
	}
	current, ok = parseProgressCount(payload["current"])
	if !ok {
		return 0, 0, false
	}
	total, ok = parseProgressCount(payload["total"])
	if !ok {
		return 0, 0, false
	}
	if total <= 0 || current > total {
		return 0, 0, false
	}
	return current, total, true
}
