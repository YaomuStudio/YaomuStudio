package execadapter

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubExec struct {
	name string
	last Request
}

func (s *stubExec) Execute(_ context.Context, req Request, _ ProgressFunc, _ LogFunc) Result {
	s.last = req
	return Result{
		OutputDir: req.OutputDir,
		Binding:   Binding{TaskID: req.TaskID, Attempt: req.Attempt},
	}
}

// wrap stub as Executor-compatible via adapter — RoutingExecutor needs *Executor.
// Use a thin test double implementing the same Execute path by embedding Client nil
// is hard; instead test routing helpers and path translation, and a local router.

type routeCapture struct {
	host  *stubExec
	model *stubExec
	req   bool
}

func (r routeCapture) run(adapterID string, req Request) Result {
	// Mirror RoutingExecutor decision: host media only wins when a host runner exists.
	if isHostMediaAdapter(adapterID) && r.host != nil {
		return r.host.Execute(context.Background(), req, nil, nil)
	}
	if r.model != nil {
		tr := translateRequestForWSL(req)
		res := r.model.Execute(context.Background(), tr, nil, nil)
		return translateResultFromWSL(res)
	}
	if isHostMediaAdapter(adapterID) {
		return (&RoutingExecutor{ModelRequired: r.req}).Execute(context.Background(), req, nil, nil)
	}
	if r.req {
		return (&RoutingExecutor{ModelRequired: true, Host: &Executor{}}).Execute(context.Background(), req, nil, nil)
	}
	return r.host.Execute(context.Background(), req, nil, nil)
}

func TestWindowsToWSLAndBack(t *testing.T) {
	in := `D:\yaomustudio\work\task-1\attempt-1`
	wsl := WindowsToWSL(in)
	if !strings.HasPrefix(wsl, "/mnt/d/") {
		t.Fatalf("wsl path = %q", wsl)
	}
	if !strings.Contains(wsl, "yaomustudio/work/task-1/attempt-1") {
		t.Fatalf("wsl path missing tail: %q", wsl)
	}
	back := WSLToWindows(wsl)
	if !strings.EqualFold(back[:2], "D:") {
		t.Fatalf("back = %q", back)
	}
}

func TestTranslateRequestParams(t *testing.T) {
	req := Request{
		OutputDir: `C:\data\out`,
		Params: map[string]any{
			"input":  `C:\media\a.wav`,
			"nested": map[string]any{"ref": `E:\ref\b.wav`},
			"label":  "keep",
		},
	}
	out := translateRequestForWSL(req)
	if out.OutputDir != "/mnt/c/data/out" && !strings.HasPrefix(out.OutputDir, "/mnt/c/") {
		t.Fatalf("output_dir = %q", out.OutputDir)
	}
	if got, _ := out.Params["input"].(string); !strings.HasPrefix(got, "/mnt/c/") {
		t.Fatalf("input = %q", got)
	}
	nested := out.Params["nested"].(map[string]any)
	if got, _ := nested["ref"].(string); !strings.HasPrefix(got, "/mnt/e/") {
		t.Fatalf("ref = %q", got)
	}
	if out.Params["label"] != "keep" {
		t.Fatalf("label mutated")
	}
}

func TestRoutingHostVsModelPaths(t *testing.T) {
	host := &stubExec{name: "host"}
	model := &stubExec{name: "model"}
	r := routeCapture{host: host, model: model}

	_ = r.run("ffmpeg", Request{Adapter: ComponentRef{ID: "ffmpeg"}, OutputDir: `D:\out`})
	if host.last.Adapter.ID != "ffmpeg" {
		t.Fatal("ffmpeg should hit host")
	}
	if strings.HasPrefix(host.last.OutputDir, "/mnt/") {
		t.Fatal("host path must stay windows")
	}

	_ = r.run("flashcli", Request{
		Adapter:   ComponentRef{ID: "flashcli"},
		OutputDir: `D:\work\a`,
		Params:    map[string]any{"path": `D:\in.wav`},
	})
	if model.last.Adapter.ID != "flashcli" {
		t.Fatal("flashcli should hit model")
	}
	if !strings.HasPrefix(model.last.OutputDir, "/mnt/d/") {
		t.Fatalf("model output_dir = %q", model.last.OutputDir)
	}
}

func TestRoutingFFmpegFallsBackToModelWithoutHost(t *testing.T) {
	model := &stubExec{name: "model"}
	r := routeCapture{model: model}

	_ = r.run("ffmpeg", Request{
		Adapter:   ComponentRef{ID: "ffmpeg"},
		OutputDir: `D:\work\out`,
		Params:    map[string]any{"input": `D:\media\in.wav`},
	})
	if model.last.Adapter.ID != "ffmpeg" {
		t.Fatal("ffmpeg should fall back to model when host runner is absent")
	}
	if !strings.HasPrefix(model.last.OutputDir, "/mnt/d/") {
		t.Fatalf("model output_dir = %q", model.last.OutputDir)
	}
	if got, _ := model.last.Params["input"].(string); !strings.HasPrefix(got, "/mnt/d/") {
		t.Fatalf("model input = %q", got)
	}
}

func TestRoutingModelRequiredUnavailable(t *testing.T) {
	res := (&RoutingExecutor{ModelRequired: true, Host: &Executor{}}).Execute(
		context.Background(),
		Request{Adapter: ComponentRef{ID: "flashcli"}},
		nil,
		nil,
	)
	if res.Error == nil || res.Error.Code != "MODEL_RUNTIME_UNAVAILABLE" {
		t.Fatalf("result = %+v", res)
	}
}

// TestRoutingExecutorOptionalInterfaceContract 防止包装层再次隐藏调度器的可选
// 能力：容量、永久隔离与诊断身份必须对 Executor 和 RoutingExecutor 同时可见。
func TestRoutingExecutorOptionalInterfaceContract(t *testing.T) {
	targets := []struct {
		name string
		exec any
	}{
		{name: "Executor", exec: &Executor{}},
		{name: "RoutingExecutor", exec: &RoutingExecutor{}},
	}
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			if _, ok := target.exec.(interface{ Capacity() int }); !ok {
				t.Fatal("Capacity() int is hidden from the scheduler")
			}
			if _, ok := target.exec.(interface{ IsolationErr() error }); !ok {
				t.Fatal("IsolationErr() error is hidden from the scheduler")
			}
			if _, ok := target.exec.(interface{ Identity() string }); !ok {
				t.Fatal("Identity() string is hidden from the scheduler")
			}
		})
	}
}

// TestRoutingExecutorOptionalInterfaceNilSafety 覆盖 Nil 接收者、空配置与 nil
// Client：诊断接口不得 panic，Executor.Execute 既有取消语义不变。
func TestRoutingExecutorOptionalInterfaceNilSafety(t *testing.T) {
	var nilRouter *RoutingExecutor
	if got := nilRouter.Capacity(); got != 1 {
		t.Fatalf("nil receiver capacity = %d, want 1", got)
	}
	if err := nilRouter.IsolationErr(); err != nil {
		t.Fatalf("nil receiver isolation = %v", err)
	}
	if got := nilRouter.Identity(); got != "" {
		t.Fatalf("nil receiver identity = %q", got)
	}
	if res := nilRouter.Execute(context.Background(), Request{}, nil, nil); !errors.Is(res.TransportErr, context.Canceled) {
		t.Fatalf("nil receiver execute = %+v", res)
	}

	empty := &RoutingExecutor{}
	if got := empty.Capacity(); got != 1 {
		t.Fatalf("empty router capacity = %d, want 1", got)
	}
	if err := empty.IsolationErr(); err != nil {
		t.Fatalf("empty router isolation = %v", err)
	}
	if got := empty.Identity(); got != "" {
		t.Fatalf("empty router identity = %q", got)
	}
	if err := (&RoutingExecutor{Host: nil, Model: nil}).IsolationErr(); err != nil {
		t.Fatalf("nil children isolation = %v", err)
	}
	if got := (&RoutingExecutor{Host: nil, Model: nil}).Identity(); got != "" {
		t.Fatalf("nil children identity = %q", got)
	}

	// nil Client 可读隔离状态，但 Identity 不得解引用 Executor.Client。
	noClient := &RoutingExecutor{Host: &Executor{}, Model: &Executor{}}
	if err := noClient.IsolationErr(); err != nil {
		t.Fatalf("nil client isolation = %v", err)
	}
	if got := noClient.Identity(); got != "" {
		t.Fatalf("nil client identity = %q", got)
	}
}

// TestRoutingExecutorIsolationAggregatesBothSides 覆盖 Host/Model 任一侧隔离即
// fail-closed：两侧同时隔离时错误可用 errors.Is 分别识别。
func TestRoutingExecutorIsolationAggregatesBothSides(t *testing.T) {
	hostErr := errors.New("host runner isolated")
	modelErr := errors.New("model runner isolated")
	host := &Executor{}
	model := &Executor{}
	host.isolate(hostErr)
	model.isolate(modelErr)

	if got := (&RoutingExecutor{Host: host}).IsolationErr(); !errors.Is(got, hostErr) {
		t.Fatalf("host isolation = %v", got)
	}
	if got := (&RoutingExecutor{Model: model}).IsolationErr(); !errors.Is(got, modelErr) {
		t.Fatalf("model isolation = %v", got)
	}
	both := (&RoutingExecutor{Host: host, Model: model}).IsolationErr()
	if !errors.Is(both, hostErr) || !errors.Is(both, modelErr) {
		t.Fatalf("combined isolation = %v", both)
	}
	if got := (&RoutingExecutor{Host: &Executor{}, Model: &Executor{}}).IsolationErr(); got != nil {
		t.Fatalf("healthy routers reported isolation: %v", got)
	}
}

// TestRoutingExecutorIdentityOmitsEmptySides 覆盖诊断身份组合：非空侧按
// host:/model: 标注，空身份与 nil Client 不产生占位。
func TestRoutingExecutorIdentityOmitsEmptySides(t *testing.T) {
	host := &Executor{Client: &Client{transport: &ProcessTransport{identity: "host-runner"}}}
	model := &Executor{Client: &Client{transport: &ProcessTransport{identity: "model-runner"}}}

	if got := (&RoutingExecutor{Host: host, Model: model}).Identity(); got != "host:host-runner|model:model-runner" {
		t.Fatalf("identity = %q", got)
	}
	if got := (&RoutingExecutor{Host: host}).Identity(); got != "host:host-runner" {
		t.Fatalf("host identity = %q", got)
	}
	if got := (&RoutingExecutor{Model: model}).Identity(); got != "model:model-runner" {
		t.Fatalf("model identity = %q", got)
	}
	emptyIdentity := &Executor{Client: &Client{}}
	if got := (&RoutingExecutor{Host: emptyIdentity, Model: model}).Identity(); got != "model:model-runner" {
		t.Fatalf("empty side identity = %q", got)
	}
}
