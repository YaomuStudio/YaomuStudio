package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/resultcommit"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

// TestExportRetryAfterFailureFinalizesRecord 覆盖 P1-05：
// 导出首次失败后重试成功，导出记录必须从 FAILED 收敛为 SUCCEEDED，
// 产物、Asset 与 ArtifactCommit 都要落在新 Attempt 上。
func TestExportRetryAfterFailureFinalizesRecord(t *testing.T) {
	payload := append([]byte("ID3"), make([]byte, 64)...)

	executor := &fakeExecutor{result: func(req execadapter.Request) execadapter.Result {
		binding := execadapter.Binding{
			Workspace:   req.Workspace,
			ServerEpoch: req.ServerEpoch,
			TaskID:      req.TaskID,
			StepID:      req.StepID,
			Attempt:     req.Attempt,
		}
		if req.Attempt == 1 {
			return execadapter.Result{
				Error: &execadapter.ExecError{
					Code:      "WORKER_EXITED",
					Message:   "simulated first-attempt failure",
					Retryable: true,
				},
				Binding: binding,
			}
		}
		if err := os.MkdirAll(req.OutputDir, 0o755); err != nil {
			t.Errorf("mkdir attempt output: %v", err)
		}
		if err := os.WriteFile(filepath.Join(req.OutputDir, "out.mp3"), payload, 0o600); err != nil {
			t.Errorf("write attempt output: %v", err)
		}
		return execadapter.Result{
			OutputDir: req.OutputDir,
			Outputs: []execadapter.Output{{
				RelPath:   "out.mp3",
				SizeBytes: int64(len(payload)),
			}},
			Binding: binding,
		}
	}}

	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	source := e.seedMediaAsset(projectID)
	run := e.seedRunWithInputs(projectID, source.ID)
	track := e.seedTrack(projectID, "zh", []domain.Segment{{
		ID: "seg-1", StartMs: 0, EndMs: 500, Text: "你好",
	}})
	e.seedApprovedDelivery(run.ID, track)

	outDir := t.TempDir()
	record := e.createExport(projectID, run.ID, outDir)
	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()

	e.waitTask(record.TaskID, domain.TaskStatusFailed, 5*time.Second)
	failed := waitExportStatus(t, e, record.ID, domain.TaskStatusFailed, 5*time.Second)
	if failed.OutputPath != "" {
		t.Fatalf("failed export has output path %q", failed.OutputPath)
	}

	resp, err := e.svc.RetryTask(context.Background(), service.Idem{}, string(record.TaskID))
	if err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	retried := resp.Body.(domain.Task)
	if retried.Status != domain.TaskStatusQueued || retried.Progress != 0 {
		t.Fatalf("retried task = status %s progress %d, want QUEUED/0", retried.Status, retried.Progress)
	}

	succeeded := e.waitTask(record.TaskID, domain.TaskStatusSucceeded, 10*time.Second)
	updated := waitExportStatus(t, e, record.ID, domain.TaskStatusSucceeded, 10*time.Second)
	if updated.OutputPath == "" {
		t.Fatal("succeeded export has empty output path")
	}
	if filepath.Dir(updated.OutputPath) != outDir {
		t.Fatalf("output path %s is outside grant %s", updated.OutputPath, outDir)
	}
	landed, err := os.ReadFile(updated.OutputPath)
	if err != nil {
		t.Fatalf("read landed output: %v", err)
	}
	if string(landed) != string(payload) {
		t.Fatalf("landed output = %d bytes, want %d", len(landed), len(payload))
	}

	attempts, err := e.st.ListAttempts(context.Background(), record.TaskID)
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2: %+v", len(attempts), attempts)
	}
	if attempts[0].Status != domain.StepStatusFailed || attempts[1].Status != domain.StepStatusSucceeded {
		t.Fatalf("attempt statuses = %s/%s, want failed/succeeded",
			attempts[0].Status, attempts[1].Status)
	}
	firstCommits, err := e.st.ListArtifactCommits(context.Background(), attempts[0].ID)
	if err != nil || len(firstCommits) != 0 {
		t.Fatalf("failed attempt commits = %+v err=%v, want none", firstCommits, err)
	}
	secondCommits, err := e.st.ListArtifactCommits(context.Background(), attempts[1].ID)
	if err != nil || len(secondCommits) != 1 || len(secondCommits[0].AssetIDs) != 1 {
		t.Fatalf("succeeded attempt commits = %+v err=%v, want 1 asset", secondCommits, err)
	}
	asset, err := e.st.GetAsset(context.Background(), secondCommits[0].AssetIDs[0])
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	if asset.Type != domain.AssetTypeAudio || asset.Path != updated.OutputPath {
		t.Fatalf("export asset = %+v, want audio at %s", asset, updated.OutputPath)
	}

	// 同一成功 Attempt 的重复回调必须幂等：不重复登记、不改 output path。
	dupResult := resultcommit.ExecutionResult{
		OutputDir: filepath.Join(e.data, "work", string(record.TaskID), "attempt-2"),
		Outputs:   []resultcommit.ExecutionOutput{{RelPath: "out.mp3", SizeBytes: int64(len(payload))}},
		Binding: resultcommit.ExecutionBinding{
			TaskID: string(record.TaskID),
			StepID: attempts[1].StepID, Attempt: attempts[1].AttemptNo,
		},
	}
	if err := e.svc.OnTaskSucceeded(context.Background(), succeeded, dupResult); err != nil {
		t.Fatalf("duplicate OnTaskSucceeded: %v", err)
	}
	afterDup, err := e.st.GetExport(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("GetExport after duplicate: %v", err)
	}
	if afterDup.Status != domain.TaskStatusSucceeded || afterDup.OutputPath != updated.OutputPath {
		t.Fatalf("export changed by duplicate callback: %+v", afterDup)
	}
	afterDupCommits, err := e.st.ListArtifactCommits(context.Background(), attempts[1].ID)
	if err != nil || len(afterDupCommits) != 1 {
		t.Fatalf("commits after duplicate = %+v err=%v, want 1", afterDupCommits, err)
	}

	events, err := e.st.EventsAfter(context.Background(), 0, 1000)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	seen := map[string]int{}
	for _, event := range events {
		seen[event.Type]++
	}
	for _, want := range []string{"export.failed", "task.queued", "export.succeeded"} {
		if seen[want] == 0 {
			t.Fatalf("event %s missing; counts=%v", want, seen)
		}
	}
}

func waitExportStatus(t *testing.T, e *env, exportID domain.ID, want domain.TaskStatus, timeout time.Duration) domain.ExportRecord {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last domain.ExportRecord
	for time.Now().Before(deadline) {
		record, err := e.st.GetExport(context.Background(), exportID)
		if err != nil {
			t.Fatalf("GetExport: %v", err)
		}
		last = record
		if record.Status == want {
			return record
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("export %s status = %s, want %s", exportID, last.Status, want)
	return last
}
