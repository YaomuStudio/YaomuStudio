package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/resultcommit"
)

type capacityExecutor struct{ *fakeExecutor }

func (*capacityExecutor) Capacity() int { return 1 }

func requestBinding(req execadapter.Request) execadapter.Binding {
	return execadapter.Binding{
		Workspace: req.Workspace, ServerEpoch: req.ServerEpoch, TaskID: req.TaskID,
		StepID: req.StepID, Attempt: req.Attempt,
	}
}

func TestExecutorCapacityCoversAllTaskKinds(t *testing.T) {
	fake := &fakeExecutor{gate: make(chan struct{}), started: make(chan domain.ID, 2)}
	fake.result = func(req execadapter.Request) execadapter.Result {
		return execadapter.Result{
			Error: &execadapter.ExecError{Code: "MODEL_RUNTIME_UNAVAILABLE"}, Binding: requestBinding(req),
		}
	}
	e := newEnv(t, envOptions{executor: &capacityExecutor{fakeExecutor: fake}, slots: 3})
	projectID := e.seedProject()
	source := e.seedMediaAsset(projectID)
	run := e.seedRunWithInputs(projectID, source.ID)
	track := e.seedTrack(projectID, "zh", []domain.Segment{{ID: "seg", StartMs: 0, EndMs: 500, Text: "hello"}})
	e.seedApprovedDelivery(run.ID, track)
	e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)
	e.createExport(projectID, run.ID, t.TempDir())
	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()
	select {
	case <-fake.started:
	case <-time.After(3 * time.Second):
		t.Fatal("executor did not start")
	}
	select {
	case <-fake.started:
		t.Fatal("Runner capacity 1 admitted overlapping node/export")
	case <-time.After(150 * time.Millisecond):
	}
	close(fake.gate)
}

func TestResultBindingValidatedBeforePreparation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*execadapter.Binding)
	}{
		{name: "missing", change: func(b *execadapter.Binding) { *b = execadapter.Binding{} }},
		{name: "workspace", change: func(b *execadapter.Binding) { b.Workspace = "other" }},
		{name: "epoch", change: func(b *execadapter.Binding) { b.ServerEpoch++ }},
		{name: "task", change: func(b *execadapter.Binding) { b.TaskID = "other" }},
		{name: "step", change: func(b *execadapter.Binding) { b.StepID = "other" }},
		{name: "attempt", change: func(b *execadapter.Binding) { b.Attempt++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeExecutor{result: func(req execadapter.Request) execadapter.Result {
				binding := requestBinding(req)
				test.change(&binding)
				return execadapter.Result{Binding: binding, ArtifactIDs: []string{"untrusted"}}
			}}
			e := newEnv(t, envOptions{executor: fake})
			task := e.seedTask(e.seedProject(), "", domain.NodeKeyAnalyze)
			var prepared atomic.Int32
			e.sched.hooks.PrepareResult = func(context.Context, domain.Task, resultcommit.ExecutionResult) (PreparedResult, error) {
				prepared.Add(1)
				return nil, nil
			}
			cl, claimed, err := e.sched.claim(context.Background(), task.ID)
			if err != nil || !claimed {
				t.Fatalf("claim: %t %v", claimed, err)
			}
			e.sched.execute(context.Background(), cl)
			if prepared.Load() != 0 {
				t.Fatal("unbound/stale result reached file preparation")
			}
			current, err := e.st.GetTask(context.Background(), task.ID)
			if err != nil || current.Status != domain.TaskStatusFailed || len(current.ArtifactIDs) != 0 {
				t.Fatalf("untrusted result was not safely rejected: %+v err=%v", current, err)
			}
		})
	}
}

func TestTransportFailureMayCompleteWithoutBinding(t *testing.T) {
	fake := &fakeExecutor{result: func(execadapter.Request) execadapter.Result {
		return execadapter.Result{TransportErr: errors.New("protocol stream lost after confirmed exit")}
	}}
	e := newEnv(t, envOptions{executor: fake})
	task := e.seedTask(e.seedProject(), "", domain.NodeKeyAnalyze)
	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()
	e.waitTask(task.ID, domain.TaskStatusFailed, 3*time.Second)
}

func TestUnconfirmedStopRetainsReservationAndBlocksDispatch(t *testing.T) {
	fake := &fakeExecutor{result: func(execadapter.Request) execadapter.Result {
		return execadapter.Result{TransportErr: execadapter.ErrStopUnconfirmed, StopUnconfirmed: true}
	}}
	e := newEnv(t, envOptions{executor: &capacityExecutor{fakeExecutor: fake}, slots: 3})
	projectID := e.seedProject()
	first := e.seedTask(projectID, "", domain.NodeKeyAnalyze)
	second := e.seedTask(projectID, "", domain.NodeKeySeparate)
	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()
	deadline := time.After(3 * time.Second)
	for !e.sched.Isolated() {
		select {
		case <-deadline:
			t.Fatal("unknown execution did not isolate the scheduler")
		case <-time.After(5 * time.Millisecond):
		}
	}
	time.Sleep(80 * time.Millisecond)
	if calls, _ := fake.snapshot(); calls != 1 {
		t.Fatalf("executor calls = %d after unknown stop", calls)
	}
	if len(e.sched.slots) != 1 || e.sched.RunningCount() != 1 {
		t.Fatal("unconfirmed execution released its resource reservation")
	}
	for _, id := range []domain.ID{first.ID, second.ID} {
		task, err := e.st.GetTask(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status.Terminal() {
			t.Fatalf("unknown execution falsely settled terminal: %+v", task)
		}
	}
}

func TestTaskPrivateMetadataDoesNotReachRunner(t *testing.T) {
	for _, kind := range []string{"model", "mix"} {
		t.Run(kind, func(t *testing.T) {
			fake := &fakeExecutor{result: func(req execadapter.Request) execadapter.Result {
				if _, exists := req.Params["_core"]; exists {
					t.Error("Core metadata leaked to Runner")
				}
				if kind == "mix" && req.Params["op"] != "transcode" {
					t.Errorf("08 plan not unpacked: %v", req.Params)
				}
				return execadapter.Result{
					Error: &execadapter.ExecError{Code: "MODEL_RUNTIME_UNAVAILABLE"}, Binding: requestBinding(req),
				}
			}}
			e := newEnv(t, envOptions{executor: fake})
			task := e.seedTask(e.seedProject(), "", domain.NodeKeyAnalyze)
			cl, claimed, err := e.sched.claim(context.Background(), task.ID)
			if err != nil || !claimed {
				t.Fatalf("claim: %t %v", claimed, err)
			}
			cl.task.Params = map[string]any{"language": "en", "_core": map[string]any{"secret": "internal"}}
			if kind == "mix" {
				cl.task.NodeKey = domain.NodeKeyMix
				cl.task.Params["plan"] = map[string]any{"op": "transcode", "out": "preview.mp4"}
			}
			e.sched.execute(context.Background(), cl)
		})
	}
}

func TestArchitectureAuditExportSuccessRequiresArtifacts(t *testing.T) {
	executor := &fakeExecutor{result: func(req execadapter.Request) execadapter.Result {
		return execadapter.Result{Binding: requestBinding(req)}
	}}
	e := newEnv(t, envOptions{executor: executor})
	ctx := context.Background()
	projectID := e.seedProject()
	source := e.seedMediaAsset(projectID)
	run := e.seedRunWithInputs(projectID, source.ID)
	track := e.seedTrack(projectID, "zh", []domain.Segment{{ID: "segment", StartMs: 0, EndMs: 500, Text: "audit"}})
	e.seedApprovedDelivery(run.ID, track)
	record := e.createExport(projectID, run.ID, t.TempDir())
	startScheduler(t, e.sched, ctx)
	e.sched.Notify()
	e.waitTask(record.TaskID, domain.TaskStatusFailed, 3*time.Second)
	waitExportStatus(t, e, record.ID, domain.TaskStatusFailed, 3*time.Second)
	events, err := e.st.EventsAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.TaskID == record.TaskID && event.Type == "task.succeeded" {
			t.Fatal("artifactless export published task.succeeded")
		}
	}
}
