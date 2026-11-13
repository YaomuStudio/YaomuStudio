package scheduler

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/resultcommit"
)

// TestExecutionResultProjectionMatchesExecutorResult 证明调度器在 PrepareResult /
// OnTaskSucceeded 钩子边界注入的窄视图确实来自真实 execadapter.Result，
// 且产物、输出目录与 attempt 绑定逐字段一致。
func TestExecutionResultProjectionMatchesExecutorResult(t *testing.T) {
	media := map[string]any{"duration_ms": float64(1200), "role": "vocals"}
	var seen execadapter.Request
	fake := &fakeExecutor{result: func(req execadapter.Request) execadapter.Result {
		seen = req
		return execadapter.Result{
			Outputs:     []execadapter.Output{{RelPath: "out.wav", SizeBytes: 42, Media: media}},
			OutputDir:   req.OutputDir,
			Binding:     requestBinding(req),
			ArtifactIDs: []string{"protocol-only-artifact"},
		}
	}}
	e := newEnv(t, envOptions{executor: fake})
	task := e.seedTask(e.seedProject(), "", domain.NodeKeyAnalyze)

	var preparedView resultcommit.ExecutionResult
	e.sched.hooks.PrepareResult = func(_ context.Context, _ domain.Task, result resultcommit.ExecutionResult) (PreparedResult, error) {
		preparedView = result
		return nil, nil
	}
	var successView resultcommit.ExecutionResult
	e.sched.hooks.OnTaskSucceeded = func(_ context.Context, _ domain.Task, result resultcommit.ExecutionResult) error {
		successView = result
		return nil
	}

	cl, claimed, err := e.sched.claim(context.Background(), task.ID)
	if err != nil || !claimed {
		t.Fatalf("claim: %t %v", claimed, err)
	}
	e.sched.execute(context.Background(), cl)

	wantDir := filepath.Join(e.data, "work", string(task.ID), "attempt-1")
	if seen.OutputDir != wantDir {
		t.Fatalf("fixture output dir = %q, want %q", seen.OutputDir, wantDir)
	}
	views := map[string]resultcommit.ExecutionResult{
		"PrepareResult":   preparedView,
		"OnTaskSucceeded": successView,
	}
	for name, view := range views {
		if len(view.Outputs) != 1 {
			t.Fatalf("%s outputs = %+v, want exactly one", name, view.Outputs)
		}
		output := view.Outputs[0]
		if output.RelPath != "out.wav" || output.SizeBytes != 42 {
			t.Fatalf("%s output = %+v, want out.wav/42", name, output)
		}
		if output.Media["role"] != "vocals" || output.Media["duration_ms"] != float64(1200) {
			t.Fatalf("%s media = %+v, want executor media projection", name, output.Media)
		}
		if view.OutputDir != seen.OutputDir {
			t.Fatalf("%s output dir = %q, want %q", name, view.OutputDir, seen.OutputDir)
		}
		wantBinding := resultcommit.ExecutionBinding{
			TaskID: seen.TaskID, StepID: seen.StepID, Attempt: seen.Attempt,
		}
		if view.Binding != wantBinding {
			t.Fatalf("%s binding = %+v, want %+v", name, view.Binding, wantBinding)
		}
	}
}
