package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/scheduler"
)

// This fixture controls execution feedback, not model inference or artifact quality.
// The HTTP router, authentication, scheduler, SQLite and SSE bus are real.
type progressHTTPExecution struct {
	request  execadapter.Request
	report   execadapter.ProgressFunc
	complete chan struct{}
}

type progressHTTPExecutor struct {
	started chan progressHTTPExecution
}

func (e *progressHTTPExecutor) Execute(
	ctx context.Context,
	req execadapter.Request,
	progress execadapter.ProgressFunc,
	_ execadapter.LogFunc,
) execadapter.Result {
	execution := progressHTTPExecution{
		request: req, report: progress, complete: make(chan struct{}),
	}
	select {
	case e.started <- execution:
	case <-ctx.Done():
		return execadapter.Result{TransportErr: ctx.Err()}
	}
	select {
	case <-execution.complete:
		return execadapter.Result{Binding: execadapter.Binding{
			Workspace: req.Workspace, ServerEpoch: req.ServerEpoch,
			TaskID: req.TaskID, StepID: req.StepID, Attempt: req.Attempt,
		}}
	case <-ctx.Done():
		return execadapter.Result{TransportErr: ctx.Err()}
	}
}

func openProgressHTTPStream(t *testing.T, env *testEnv, after int64) (*bufio.Scanner, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	url := fmt.Sprintf("%s/api/v1/events?after_seq=%d", env.server.URL, after)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(tokenHeader, env.token)
	resp, err := env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	closeStream := func() {
		cancel()
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close SSE response: %v", err)
		}
	}
	t.Cleanup(closeStream)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("SSE status/content type = %d/%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	return bufio.NewScanner(resp.Body), closeStream
}

func nextProgressHTTPEvent(t *testing.T, scanner *bufio.Scanner, expected string) domain.DomainEvent {
	t.Helper()
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "id:") {
			t.Fatal("SSE unexpectedly wrote an id field; seq=0 must not become a replay cursor")
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event domain.DomainEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatalf("decode SSE event: %v", err)
		}
		if event.Type != expected {
			t.Fatalf("SSE event = %q, want %q", event.Type, expected)
		}
		return event
	}
	t.Fatalf("SSE ended while waiting for %s: %v", expected, scanner.Err())
	return domain.DomainEvent{}
}

func TestTaskProgressHTTPStreamAndSnapshotRecovery(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("进度反馈验证")
	task := decodeInto[domain.Task](t, env.expect(env.request(http.MethodPost, "/api/v1/tasks", map[string]any{
		"project_id": projectID, "kind": "node", "node_key": "02_analyze",
	}), http.StatusAccepted))

	var clock atomic.Int64
	clock.Store(time.Now().UTC().UnixNano())
	executor := &progressHTTPExecutor{started: make(chan progressHTTPExecution, 1)}
	sched := scheduler.New(scheduler.Options{
		Store: env.store, Bus: env.bus, Log: env.logger, Executor: executor,
		WorkspaceID: "ws-test", Epoch: 7, WorkDir: filepath.Join(env.dataDir, "work"),
		Now: func() time.Time { return time.Unix(0, clock.Load()).UTC() },
	})
	// No artifact hooks or model provider: only the feedback/state transport is under test.
	env.svc.SetNotifier(sched)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		sched.Close(ctx)
	})

	stream, disconnect := openProgressHTTPStream(t, env, 0)
	nextProgressHTTPEvent(t, stream, "project.created")
	nextProgressHTTPEvent(t, stream, "task.created")
	nextProgressHTTPEvent(t, stream, "task.queued")
	if err := sched.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	sched.Notify()
	var execution progressHTTPExecution
	select {
	case execution = <-executor.started:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not start the queued task")
	}
	started := nextProgressHTTPEvent(t, stream, "task.started")
	execution.report("private stage must not be published", 42, 100)
	progress := nextProgressHTTPEvent(t, stream, "task.progress")
	wantData := map[string]any{
		"step_id": execution.request.StepID, "attempt": float64(execution.request.Attempt),
		"server_epoch": "7", "progress": float64(42),
	}
	if progress.SchemaVersion != 1 || progress.Seq != 0 || progress.StateVersion != started.StateVersion {
		t.Fatalf("progress envelope = %+v, started = %+v", progress, started)
	}
	if progress.StateVersion < 1 || progress.TaskID != task.ID || progress.ProjectID != projectID {
		t.Fatalf("progress identity = %+v", progress)
	}
	if !reflect.DeepEqual(progress.Data, wantData) {
		t.Fatalf("progress data = %#v, want exact safe fields %#v", progress.Data, wantData)
	}

	readTask := func() domain.Task {
		return decodeInto[domain.Task](t,
			env.expect(env.request(http.MethodGet, "/api/v1/tasks/"+task.ID, nil), http.StatusOK))
	}
	readSnapshot := func() domain.Snapshot {
		return decodeInto[domain.Snapshot](t,
			env.expect(env.request(http.MethodGet, "/api/v1/snapshot?project_id="+projectID, nil), http.StatusOK))
	}
	current := readTask()
	if current.Status != domain.TaskStatusRunning || current.Progress != 42 {
		t.Fatalf("task after feedback = %s/%d, want RUNNING/42", current.Status, current.Progress)
	}
	snapshot := readSnapshot()
	if snapshot.EventSeq != started.Seq || len(snapshot.Tasks) != 1 || snapshot.Tasks[0].Progress != 42 {
		t.Fatalf("feedback changed persistent cursor or snapshot diverged: %+v", snapshot)
	}

	disconnect()
	clock.Add(int64(300 * time.Millisecond))
	execution.report("another private stage", 90, 100)
	recovered := readSnapshot()
	if recovered.EventSeq != started.Seq || len(recovered.Tasks) != 1 || recovered.Tasks[0].Progress != 90 {
		t.Fatalf("disconnected snapshot did not recover progress without cursor growth: %+v", recovered)
	}

	reconnected, _ := openProgressHTTPStream(t, env, recovered.EventSeq)
	// A persistent barrier proves stream handoff without sleeps or repeated live injection.
	env.seedProject("重连屏障")
	barrier := nextProgressHTTPEvent(t, reconnected, "project.created")
	if barrier.Seq <= recovered.EventSeq {
		t.Fatal("persistent cursor did not advance for a real business event")
	}
	close(execution.complete)
	terminal := nextProgressHTTPEvent(t, reconnected, "task.succeeded")
	if terminal.TaskID != task.ID || terminal.Seq <= barrier.Seq || terminal.StateVersion <= progress.StateVersion {
		t.Fatalf("terminal event = %+v", terminal)
	}
	current = readTask()
	if current.Status != domain.TaskStatusSucceeded || current.Progress != 100 {
		t.Fatalf("terminal snapshot = %s/%d, want SUCCEEDED/100", current.Status, current.Progress)
	}
	clock.Add(int64(time.Second))
	execution.report("late callback", 95, 100)
	final := readSnapshot()
	if final.EventSeq != terminal.Seq || len(final.Tasks) != 1 || final.Tasks[0].Progress != 100 {
		t.Fatalf("late callback changed terminal snapshot/cursor: %+v", final)
	}
	t.Log("HTTP/SSE: live=42, disconnected snapshot=90, terminal=100; progress never advances/replays the cursor")
}
