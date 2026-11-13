package execadapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
)

// memTransport 是内存传输：out 是客户端发出的帧，in 是对端应答/事件帧。
type memTransport struct {
	out    chan Frame
	in     chan Frame
	closed chan struct{}
	once   sync.Once
}

func newMemTransport() *memTransport {
	return &memTransport{
		out:    make(chan Frame, 16),
		in:     make(chan Frame, 16),
		closed: make(chan struct{}),
	}
}

func (m *memTransport) Send(f Frame) error { m.out <- f; return nil }

func (m *memTransport) Receive() (Frame, error) {
	f, ok := <-m.in
	if !ok {
		return Frame{}, errEOF
	}
	return f, nil
}

func (m *memTransport) Kill() error  { m.once.Do(func() { close(m.closed) }); return nil }
func (m *memTransport) Close() error { _ = m.Kill(); return nil }

var errEOF = &eofError{}

type eofError struct{}

func (*eofError) Error() string { return "eof" }

// fakeRunner 按协议响应请求，可注入 execute 结果与进度帧载荷。
type fakeRunner struct {
	t              *testing.T
	tr             *memTransport
	progressFrames []map[string]any
	logs           bool
	holdExec       bool
	execute        func(req Frame) Frame
	probe          func(req Frame) Frame
	cancel         func(req Frame) Frame
}

func (f *fakeRunner) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req, ok := <-f.tr.out:
			if !ok {
				return
			}
			switch req.Op {
			case OpHello:
				f.send(Frame{V: 1, Op: OpResult, ReqID: req.ReqID, Payload: map[string]any{
					"request_op": "hello",
					"status":     "ok",
					"data": map[string]any{
						"runner_version":   "fake-1",
						"protocol_version": 1,
						"adapters":         []any{map[string]any{"id": "fake-adapter"}},
					},
				}})
			case OpExecute:
				if f.holdExec {
					continue
				}
				for _, payload := range f.progressFrames {
					f.send(Frame{V: 1, Op: OpProgress, ReqID: req.ReqID, Payload: payload})
				}
				if f.logs {
					f.send(Frame{V: 1, Op: OpLog, ReqID: req.ReqID, Payload: map[string]any{
						"server_epoch": "7", "task_id": req.Payload["task_id"],
						"step_id": req.Payload["step_id"], "attempt": req.Payload["attempt"],
						"stream": "stderr", "line": "flashcli starting", "seq": 1,
					}})
					// 绑定不匹配的迟到行必须被丢弃。
					f.send(Frame{V: 1, Op: OpLog, ReqID: req.ReqID, Payload: map[string]any{
						"server_epoch": "7", "task_id": "other-task",
						"step_id": req.Payload["step_id"], "attempt": req.Payload["attempt"],
						"stream": "stderr", "line": "stale line", "seq": 2,
					}})
					// 超长行必须被丢弃。
					f.send(Frame{V: 1, Op: OpLog, ReqID: req.ReqID, Payload: map[string]any{
						"server_epoch": "7", "task_id": req.Payload["task_id"],
						"step_id": req.Payload["step_id"], "attempt": req.Payload["attempt"],
						"stream": "stderr", "line": strings.Repeat("x", MaxLogLineChars+1), "seq": 3,
					}})
				}
				if f.execute != nil {
					f.send(f.execute(req))
				} else {
					f.send(Frame{V: 1, Op: OpResult, ReqID: req.ReqID, Payload: map[string]any{
						"request_op": "execute",
						"status":     "ok",
						"data":       map[string]any{"artifact_ids": []any{"asset-out"}},
						"binding": map[string]any{
							"workspace":    "ws",
							"server_epoch": "7",
							"task_id":      req.Payload["task_id"],
							"step_id":      req.Payload["step_id"],
							"attempt":      req.Payload["attempt"],
						},
					}})
				}
			case OpProbe:
				if f.probe != nil {
					f.send(f.probe(req))
				} else {
					f.send(Frame{V: 1, Op: OpResult, ReqID: req.ReqID, Payload: map[string]any{
						"request_op": "probe", "status": "ok",
						"data": map[string]any{"probe": "ok"},
					}})
				}
			case OpCancel:
				if f.cancel != nil {
					f.send(f.cancel(req))
					continue
				}
				f.send(Frame{V: 1, Op: OpResult, ReqID: req.ReqID, Payload: map[string]any{
					"request_op": "cancel", "status": "ok",
					"data": map[string]any{
						"exited": true, "active": false, "task_id": req.Payload["task_id"],
						"attempt": req.Payload["attempt"], "step_id": req.Payload["step_id"],
					},
				}})
			case OpShutdown:
				f.send(Frame{V: 1, Op: OpResult, ReqID: req.ReqID, Payload: map[string]any{
					"request_op": "shutdown", "status": "ok",
				}})
				return
			}
		}
	}
}

func (f *fakeRunner) send(frame Frame) { f.tr.in <- frame }

func newFakeClient(t *testing.T) (*Client, *fakeRunner) {
	t.Helper()
	tr := newMemTransport()
	runner := &fakeRunner{t: t, tr: tr}
	logger, err := logging.New(logging.Options{Path: filepath.Join(t.TempDir(), "core.log")})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	client := NewClient(tr, logger)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
	})
	go runner.run(ctx)
	return client, runner
}

func testRequest() Request {
	return Request{
		Workspace:     "ws",
		ServerEpoch:   7,
		TaskID:        "task-1",
		StepID:        "step-1",
		Attempt:       2,
		Model:         ComponentRef{ID: "model", Digest: "sha-model"},
		Adapter:       ComponentRef{ID: "fake-adapter", Digest: "sha-adapter"},
		Runtime:       ComponentRef{ID: "runtime", Digest: "sha-runtime"},
		Params:        map[string]any{"temperature": 0.1},
		InputAssetIDs: []string{"asset-in"},
		OutputDir:     "/tmp/out/attempt-2",
	}
}

func TestClientHelloExecuteCancelShutdown(t *testing.T) {
	client, runner := newFakeClient(t)
	runner.progressFrames = []map[string]any{{
		"server_epoch": "7", "task_id": "task-1", "step_id": "step-1", "attempt": 2,
		"stage": "decoding", "current": 5, "total": 10,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hello, err := client.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if hello.RunnerVersion != "fake-1" || hello.ProtocolVersion != 1 {
		t.Fatalf("hello = %+v", hello)
	}

	progressed := make(chan string, 1)
	res := client.Execute(ctx, testRequest(), func(stage string, current, total int) {
		if current != 5 || total != 10 {
			t.Errorf("progress = %d/%d", current, total)
		}
		select {
		case progressed <- stage:
		default:
		}
	}, nil)
	if res.TransportErr != nil {
		t.Fatalf("Execute() transport error = %v", res.TransportErr)
	}
	if len(res.ArtifactIDs) != 1 || res.ArtifactIDs[0] != "asset-out" {
		t.Fatalf("artifacts = %v", res.ArtifactIDs)
	}
	if res.Binding.TaskID != "task-1" || res.Binding.Attempt != 2 || res.Binding.ServerEpoch != 7 {
		t.Fatalf("binding = %+v", res.Binding)
	}
	select {
	case stage := <-progressed:
		if stage != "decoding" {
			t.Fatalf("stage = %q", stage)
		}
	case <-time.After(time.Second):
		t.Fatal("no progress callback")
	}

	probe, err := client.Probe(ctx, ComponentRef{ID: "runtime", Digest: "sha-runtime"})
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if !probe.OK || probe.Error != nil {
		t.Fatalf("probe = %+v", probe)
	}

	cancelRes, err := client.Cancel(ctx, "task-1", 2, "step-1")
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if !cancelRes.Exited {
		t.Fatalf("cancel = %+v, want exited", cancelRes)
	}

	if err := client.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestClientExecuteReceivesBoundLogFrames(t *testing.T) {
	client, runner := newFakeClient(t)
	runner.logs = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var logs []struct {
		stream string
		line   string
		seq    int
	}
	res := client.Execute(ctx, testRequest(), nil, func(stream, line string, seq int) {
		logs = append(logs, struct {
			stream string
			line   string
			seq    int
		}{stream, line, seq})
	})
	if res.TransportErr != nil {
		t.Fatalf("Execute() transport error = %v", res.TransportErr)
	}
	if len(logs) != 1 {
		t.Fatalf("logs = %+v, want only the bound line", logs)
	}
	if logs[0].stream != "stderr" || logs[0].line != "flashcli starting" || logs[0].seq != 1 {
		t.Fatalf("log = %+v", logs[0])
	}
}

func TestClientExecuteErrorFrame(t *testing.T) {
	client, runner := newFakeClient(t)
	runner.execute = func(req Frame) Frame {
		return Frame{V: 1, Op: OpError, ReqID: req.ReqID, Payload: map[string]any{
			"code":       "MODEL_RUNTIME_UNAVAILABLE",
			"message":    "bundle not admitted",
			"retryable":  false,
			"action_ids": []any{"open_model_setup"},
			"binding": map[string]any{
				"workspace": "ws", "task_id": "task-1", "step_id": "step-1", "attempt": 2, "server_epoch": "7",
			},
		}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res := client.Execute(ctx, testRequest(), nil, nil)
	if res.Error == nil {
		t.Fatalf("Error = nil, want runner error")
	}
	if res.Error.Code != "MODEL_RUNTIME_UNAVAILABLE" || len(res.Error.ActionIDs) != 1 {
		t.Fatalf("error = %+v", res.Error)
	}
	if res.Binding.Attempt != 2 {
		t.Fatalf("binding = %+v", res.Binding)
	}
}

// TestProcessTransportWithFakeRunnerScript 用真实子进程验证 stdio 传输：
// 假 Runner 脚本只实现协议帧，不涉及任何 FlashCLI 参数。
func TestProcessTransportWithFakeRunnerScript(t *testing.T) {
	python := strings.TrimSpace(os.Getenv("PYTHON_BIN"))
	if python == "" {
		found, err := exec.LookPath("python3")
		if err != nil {
			t.Skip("python3 not available; skipping subprocess transport test")
		}
		python = found
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake_runner.py")
	body := `
import json, sys
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    req = json.loads(line)
    op, rid = req["op"], req["req_id"]
    if op == "hello":
        resp = {"v":1,"op":"result","req_id":rid,"payload":{"request_op":"hello","status":"ok",
            "data":{"runner_version":"script-1","protocol_version":1,"adapters":[]}}}
    elif op == "execute":
        p = req["payload"]
        print(json.dumps({"v":1,"op":"progress","req_id":rid,"payload":{
            "server_epoch":p["server_epoch"],"task_id":p["task_id"],"step_id":p["step_id"],
            "attempt":p["attempt"],"stage":"step","current":1,"total":2}}), flush=True)
        resp = {"v":1,"op":"result","req_id":rid,"payload":{"request_op":"execute","status":"ok",
            "data":{"artifact_ids":["asset-1"]},
            "binding":{"workspace":p["workspace"],"server_epoch":p["server_epoch"],
                       "task_id":p["task_id"],"step_id":p["step_id"],"attempt":p["attempt"]}}}
    elif op == "probe":
        resp = {"v":1,"op":"error","req_id":rid,"payload":{"code":"MODEL_RUNTIME_UNAVAILABLE","message":"gated","retryable":False}}
    elif op == "cancel":
        resp = {"v":1,"op":"result","req_id":rid,"payload":{"request_op":"cancel","status":"ok",
            "data":{"exited":True,"active":False,"task_id":req["payload"]["task_id"],
                    "attempt":req["payload"]["attempt"],"step_id":req["payload"].get("step_id")}}}
    elif op == "shutdown":
        print(json.dumps({"v":1,"op":"result","req_id":rid,"payload":{"request_op":"shutdown","status":"ok"}}), flush=True)
        break
    else:
        resp = {"v":1,"op":"error","req_id":rid,"payload":{"code":"MODEL_RUNTIME_UNAVAILABLE","message":"gated","retryable":False}}
    print(json.dumps(resp), flush=True)
`
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatalf("write fake runner: %v", err)
	}

	logger, err := logging.New(logging.Options{Path: filepath.Join(t.TempDir(), "core.log")})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := NewProcessClient(ctx, ProcessOptions{
		Command: python,
		Args:    []string{"-I", script},
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("NewProcessClient() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	hello, err := client.Start(ctx)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if hello.RunnerVersion != "script-1" {
		t.Fatalf("hello = %+v", hello)
	}

	var stages []string
	res := client.Execute(ctx, testRequest(), func(stage string, current, total int) {
		stages = append(stages, stage)
	}, nil)
	if res.TransportErr != nil {
		t.Fatalf("Execute() error = %v", res.TransportErr)
	}
	if len(res.ArtifactIDs) != 1 || res.ArtifactIDs[0] != "asset-1" {
		t.Fatalf("artifacts = %v", res.ArtifactIDs)
	}
	if res.Binding.Attempt != 2 {
		t.Fatalf("binding = %+v", res.Binding)
	}
	if len(stages) != 1 || stages[0] != "step" {
		t.Fatalf("stages = %v", stages)
	}
	probe, err := client.Probe(ctx, ComponentRef{ID: "runtime", Digest: "sha-runtime"})
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if probe.Error == nil || probe.Error.Code != "MODEL_RUNTIME_UNAVAILABLE" {
		t.Fatalf("gated probe = %+v", probe)
	}
	cancelRes, err := client.Cancel(ctx, "task-1", 2, "")
	if err != nil || !cancelRes.Exited {
		t.Fatalf("Cancel() = %+v, %v", cancelRes, err)
	}
	if err := client.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	// 子进程收到 shutdown 后自行退出；Close 不应挂起。
	done := make(chan struct{})
	go func() { _ = client.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("client.Close() hung")
	}
}

func TestFrameValidationRejectsOversizeAndBadVersion(t *testing.T) {
	big := strings.Repeat("x", MaxFrameBytes)
	if _, err := encodeFrame(Frame{V: 1, Op: OpExecute, ReqID: "r", Payload: map[string]any{"p": big}}); err == nil {
		t.Fatal("encodeFrame() accepted oversize frame")
	}
	if _, err := decodeFrame([]byte(`{"v":2,"op":"hello","req_id":"r","payload":{}}`)); err == nil {
		t.Fatal("decodeFrame() accepted unsupported version")
	}
}

// TestExecutorKillsRunnerWhenCancelUnconfirmed 覆盖“取消已应答但未确认退出”的兜底：
// 必须终止进程树后才返回，避免调度层把资源槽交给第二个任务。
func TestExecutorKillsRunnerWhenCancelUnconfirmed(t *testing.T) {
	client, runner := newFakeClient(t)
	runner.holdExec = true
	runner.cancel = func(req Frame) Frame {
		return Frame{V: 1, Op: OpResult, ReqID: req.ReqID, Payload: map[string]any{
			"request_op": "cancel", "status": "ok",
			"data": map[string]any{"exited": false, "active": true},
		}}
	}
	executor := &Executor{Client: client}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() { done <- executor.Execute(ctx, testRequest(), nil, nil) }()

	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case res := <-done:
		if res.TransportErr == nil {
			t.Fatalf("result = %+v, want transport error after cancel", res)
		}
		_ = res
	case <-time.After(5 * time.Second):
		t.Fatal("Execute() did not return after cancel")
	}
	select {
	case <-runner.tr.closed:
	default:
		t.Fatal("runner process tree was not killed after unconfirmed cancel")
	}
}
