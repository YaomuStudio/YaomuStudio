package execadapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
)

// Client 是协议客户端：请求/应答按 req_id 配对，进度与心跳作为事件回调。
//
// 迟到结果不在这里判定终态：Result.Binding 交给调度层比对 epoch/attempt。
type Client struct {
	transport Transport
	log       *logging.Logger

	reqMu  sync.Mutex
	reqSeq int

	pendingMu sync.Mutex
	pending   map[string]chan Frame

	closed  chan struct{}
	errMu   sync.Mutex
	readErr error

	// lastActivity 是最近收到任意帧的时间（unix nano）；心跳丢失只用于上报，
	// 不直接判定进程死亡（docs/local-api-contract.md §6）。
	lastActivity atomic.Int64
	activeMu     sync.Mutex
	active       *Request
}

var ErrStopUnconfirmed = errors.New("runner execution tree exit is unconfirmed")

// ExecutionUnconfirmed includes detached adapter children, not just the Runner leader.
func (c *Client) ExecutionUnconfirmed() bool {
	c.activeMu.Lock()
	defer c.activeMu.Unlock()
	return c.active != nil
}

func (c *Client) executionStopped() {
	c.activeMu.Lock()
	c.active = nil
	c.activeMu.Unlock()
}

func (c *Client) Identity() string {
	if p, ok := c.transport.(*ProcessTransport); ok {
		return p.Identity()
	}
	return ""
}

// NewClient 用传输层构造客户端并启动读循环。
func NewClient(transport Transport, log *logging.Logger) *Client {
	c := &Client{
		transport: transport,
		log:       log,
		pending:   make(map[string]chan Frame),
		closed:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// NewProcessClient 拉起 Runner 子进程并返回客户端。
func NewProcessClient(ctx context.Context, opts ProcessOptions) (*Client, error) {
	transport, err := StartProcess(ctx, opts)
	if err != nil {
		return nil, err
	}
	return NewClient(transport, opts.Logger), nil
}

// IdleFor 返回距最近一次收到帧的时间；用于心跳失联上报（不改变任务状态）。
func (c *Client) IdleFor() time.Duration {
	last := c.lastActivity.Load()
	if last == 0 {
		return 0
	}
	return time.Since(time.Unix(0, last))
}

func (c *Client) nextReqID(prefix string) string {
	c.reqMu.Lock()
	defer c.reqMu.Unlock()
	c.reqSeq++
	return fmt.Sprintf("%s-%d", prefix, c.reqSeq)
}

func (c *Client) readLoop() {
	defer close(c.closed)
	for {
		frame, err := c.transport.Receive()
		if err != nil {
			c.errMu.Lock()
			if errors.Is(err, io.EOF) {
				c.readErr = fmt.Errorf("runner closed protocol stream")
			} else {
				c.readErr = err
			}
			c.errMu.Unlock()
			c.pendingMu.Lock()
			for _, ch := range c.pending {
				close(ch)
			}
			c.pending = make(map[string]chan Frame)
			c.pendingMu.Unlock()
			return
		}
		c.lastActivity.Store(time.Now().UnixNano())
		if frame.Op == OpHeartbeat {
			// 心跳只证明进程还在；丢失心跳不能直接判定进程死亡。
			continue
		}
		c.pendingMu.Lock()
		ch, ok := c.pending[frame.ReqID]
		c.pendingMu.Unlock()
		if !ok {
			continue
		}
		select {
		case ch <- frame:
		default:
			// 应答缓冲已满说明调用方异常；丢弃重复帧，等待下一次。
		}
	}
}

func (c *Client) register(reqID string) chan Frame {
	// 执行过程行可能短时成批到达；缓冲足够吸收节流后的日志突发。
	ch := make(chan Frame, 64)
	c.pendingMu.Lock()
	c.pending[reqID] = ch
	c.pendingMu.Unlock()
	return ch
}

func (c *Client) unregister(reqID string) {
	c.pendingMu.Lock()
	delete(c.pending, reqID)
	c.pendingMu.Unlock()
}

func (c *Client) closedErr() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	if c.readErr != nil {
		return c.readErr
	}
	return fmt.Errorf("runner connection closed")
}

// request 发送请求并等待对应 result/error 帧；ctx 结束时返回错误但不在这里终止进程。
func (c *Client) request(ctx context.Context, op string, reqID string, payload map[string]any) (Frame, error) {
	ch := c.register(reqID)
	defer c.unregister(reqID)
	if err := c.transport.Send(Frame{V: ProtocolVersion, Op: op, ReqID: reqID, Payload: payload}); err != nil {
		return Frame{}, err
	}
	select {
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	case <-c.closed:
		return Frame{}, c.closedErr()
	case frame, ok := <-ch:
		if !ok {
			return Frame{}, c.closedErr()
		}
		return frame, nil
	}
}

// Start 执行 hello 握手并返回 Runner 摘要。
func (c *Client) Start(ctx context.Context) (HelloInfo, error) {
	frame, err := c.request(ctx, OpHello, c.nextReqID("hello"), map[string]any{})
	if err != nil {
		return HelloInfo{}, err
	}
	if frame.Op == OpError {
		return HelloInfo{}, fmt.Errorf("runner hello rejected: %s", safeMessage(frame))
	}
	info := HelloInfo{}
	data, _ := frame.Payload["data"].(map[string]any)
	if v, ok := data["runner_version"].(string); ok {
		info.RunnerVersion = v
	}
	if v, ok := data["protocol_version"]; ok {
		info.ProtocolVersion = parseAttempt(v)
	}
	if list, ok := data["adapters"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				if id, ok := m["id"].(string); ok {
					info.Adapters = append(info.Adapters, id)
				}
			}
		}
	}
	if tools, ok := data["media_tools"].(map[string]any); ok {
		info.MediaTools = tools
	}
	return info, nil
}

// ProbeResult 是一次运行时探针结果；失败时 Error 携带 Runner 的安全错误码。
type ProbeResult struct {
	OK    bool
	Data  map[string]any
	Error *ExecError
}

// Probe 请求运行时探针（绑定已准入 runtime）。当前 Runner 门控未接入真实
// bundle，会返回 MODEL_RUNTIME_UNAVAILABLE；本方法只透传，不解释参数。
func (c *Client) Probe(ctx context.Context, runtime ComponentRef) (ProbeResult, error) {
	payload := map[string]any{
		"runtime": map[string]any{"id": runtime.ID, "digest": runtime.Digest},
	}
	frame, err := c.request(ctx, OpProbe, c.nextReqID("probe"), payload)
	if err != nil {
		return ProbeResult{}, err
	}
	if frame.Op == OpError {
		return ProbeResult{Error: errorFromFrame(frame)}, nil
	}
	result := ProbeResult{OK: true, Data: map[string]any{}}
	if data, ok := frame.Payload["data"].(map[string]any); ok {
		result.Data = data
	}
	return result, nil
}

// Execute 发送 execute 并等待终端结果；progress 帧通过回调上报（可为 nil）。
// log 帧是可丢弃执行过程行（可为 nil），只在绑定匹配当前请求时透传。
func (c *Client) Execute(ctx context.Context, req Request, progress ProgressFunc, logf LogFunc) Result {
	c.activeMu.Lock()
	if c.active != nil {
		c.activeMu.Unlock()
		return Result{TransportErr: fmt.Errorf("runner execution is already active"), StopUnconfirmed: true}
	}
	c.active = &req
	c.activeMu.Unlock()
	reqID := c.nextReqID("exec")
	ch := c.register(reqID)
	defer c.unregister(reqID)
	if err := c.transport.Send(Frame{V: ProtocolVersion, Op: OpExecute, ReqID: reqID, Payload: req.Payload()}); err != nil {
		return Result{TransportErr: fmt.Errorf("send execute: %w", err)}
	}
	for {
		select {
		case <-ctx.Done():
			return Result{TransportErr: ctx.Err()}
		case <-c.closed:
			return Result{TransportErr: c.closedErr()}
		case frame, ok := <-ch:
			if !ok {
				return Result{TransportErr: c.closedErr()}
			}
			switch frame.Op {
			case OpProgress:
				if progress != nil {
					if current, total, ok := parseProgressFrame(frame.Payload, req); ok {
						stage, _ := frame.Payload["stage"].(string)
						progress(stage, current, total)
					}
				}
			case OpLog:
				if logf != nil && logBindingMatches(frame.Payload, req) {
					if stream, line, seq, ok := parseLog(frame.Payload); ok {
						logf(stream, line, seq)
					}
				}
			case OpResult:
				result := Result{
					ArtifactIDs: artifacts(frame.Payload),
					Outputs:     parseOutputs(frame.Payload),
					OutputDir:   req.OutputDir,
					Binding:     parseBinding(frame.Payload),
				}
				if result.Binding.Matches(req) {
					c.executionStopped()
				}
				return result
			case OpError:
				result := Result{
					Error:     errorFromFrame(frame),
					OutputDir: req.OutputDir,
					Binding:   parseBinding(frame.Payload),
				}
				if result.Binding.Matches(req) && result.Error.Code != "MEDIA_TOOL_STOP_UNCONFIRMED" {
					c.executionStopped()
				}
				return result
			}
		}
	}
}

// Cancel 发送 cancel 并等待确认；超时后终止本应用拥有的进程树。
func (c *Client) Cancel(ctx context.Context, taskID string, attempt int, stepID string) (CancelResult, error) {
	payload := map[string]any{"task_id": taskID, "attempt": attempt}
	if stepID != "" {
		payload["step_id"] = stepID
	}
	cancelCtx, cancel := context.WithTimeout(ctx, cancelTimeout)
	defer cancel()
	frame, err := c.request(cancelCtx, OpCancel, c.nextReqID("cancel"), payload)
	if err != nil {
		// 未确认退出前不把资源槽交给第二个任务：终止进程树并重新确认。
		if killErr := c.Kill(); killErr != nil {
			return CancelResult{}, errors.Join(err, killErr)
		}
		return CancelResult{Exited: true}, nil
	}
	data, _ := frame.Payload["data"].(map[string]any)
	requestOp, _ := frame.Payload["request_op"].(string)
	valid := frame.Op == OpResult && requestOp == OpCancel &&
		data["task_id"] == taskID && parseAttempt(data["attempt"]) == attempt
	if stepID != "" {
		valid = valid && data["step_id"] == stepID
	}
	if !valid {
		return CancelResult{}, fmt.Errorf("runner cancel confirmation has invalid binding")
	}
	result := CancelResult{}
	if v, ok := data["exited"].(bool); ok {
		result.Exited = v
	}
	if v, ok := data["active"].(bool); ok {
		result.Active = v
	}
	if result.Exited {
		c.executionStopped()
	}
	return result, nil
}

// Shutdown 请求 Runner 停止主循环。
func (c *Client) Shutdown(ctx context.Context) error {
	frame, err := c.request(ctx, OpShutdown, c.nextReqID("shutdown"), map[string]any{})
	if err != nil {
		return err
	}
	if frame.Op == OpError {
		return fmt.Errorf("runner shutdown rejected: %s", safeMessage(frame))
	}
	return nil
}

// Close 关闭传输并终止子进程。
func (c *Client) Close() error {
	c.activeMu.Lock()
	active := c.active
	c.activeMu.Unlock()
	var stopErr error
	if active != nil {
		result, err := c.Cancel(context.Background(), active.TaskID, active.Attempt, active.StepID)
		stopErr = err
		if err == nil && !result.Exited {
			stopErr = c.Kill()
		}
	}
	closeErr := c.transport.Close()
	if c.ExecutionUnconfirmed() {
		return errors.Join(stopErr, closeErr, ErrStopUnconfirmed)
	}
	return errors.Join(stopErr, closeErr)
}

// Kill 终止本应用拥有的 Runner 进程树并等待退出确认。
func (c *Client) Kill() error {
	if err := c.transport.Kill(); err != nil {
		return err
	}
	process, native := c.transport.(*ProcessTransport)
	if native && !process.CoversDetachedChildren() && c.ExecutionUnconfirmed() {
		// FFmpeg/adapter children can own independent process groups. On
		// Windows the Job Object covers only Windows processes: terminating
		// wsl.exe does not reach Linux descendants inside WSL2. Killing only
		// the launcher cannot confirm the execution tree stopped after stream
		// loss, so keep the slot and lease isolated.
		return ErrStopUnconfirmed
	}
	c.executionStopped()
	return nil
}

// Exited 报告底层进程是否已退出（本地观察，不等于业务终态）。
func (c *Client) Exited() bool {
	if p, ok := c.transport.(*ProcessTransport); ok {
		return p.Exited()
	}
	return false // closing a protocol stream is not process-exit evidence
}

const cancelTimeout = 10 * time.Second

func safeMessage(frame Frame) string {
	msg, _ := frame.Payload["message"].(string)
	if len(msg) > 200 {
		msg = msg[:200]
	}
	if msg == "" {
		msg = "unspecified runner error"
	}
	return msg
}

func errorFromFrame(frame Frame) *ExecError {
	e := &ExecError{Message: safeMessage(frame)}
	if code, ok := frame.Payload["code"].(string); ok {
		e.Code = code
	}
	if retryable, ok := frame.Payload["retryable"].(bool); ok {
		e.Retryable = retryable
	}
	if list, ok := frame.Payload["action_ids"].([]any); ok {
		for _, item := range list {
			if s, ok := item.(string); ok {
				e.ActionIDs = append(e.ActionIDs, s)
			}
		}
	}
	return e
}
