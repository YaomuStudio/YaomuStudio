package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func TestRunLifecycleAndConfigRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedRun(t, s, project.ID, "run-1")

	run, err := s.GetWorkflowRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetWorkflowRun() error = %v", err)
	}
	if run.Status != domain.RunStatusDraft || run.StateVersion != 1 {
		t.Fatalf("run = %s/%d, want DRAFT/1", run.Status, run.StateVersion)
	}
	if len(run.Nodes) != 8 || run.Nodes[5].Key != domain.NodeKeyTranslate {
		t.Fatalf("run nodes = %+v", run.Nodes)
	}
	cfg := run.Config
	if len(cfg.InputRevisionIDs) != 1 || cfg.InputRevisionIDs[0] != "rev-input-1" {
		t.Errorf("input revisions = %v", cfg.InputRevisionIDs)
	}
	if cfg.Extract == nil || cfg.Extract.Mode != domain.ExtractModeASROCR || cfg.Extract.OCRLanguage != "japanese" {
		t.Errorf("extract config = %+v", cfg.Extract)
	}
	if cfg.EraseMode != domain.EraseModeDialogOnly || !cfg.ValidateTimestamps || !cfg.AdjustDuration || !cfg.Bilingual {
		t.Errorf("run config = %+v", cfg)
	}
	if cfg.SubtitleStyle == nil || cfg.SubtitleStyle.X != 0.5 || cfg.SubtitleStyle.Align != "center" {
		t.Errorf("subtitle style = %+v", cfg.SubtitleStyle)
	}
	if len(run.Languages) != 2 || run.Languages[1] != "zh" {
		t.Errorf("languages = %v", run.Languages)
	}

	now := testTime.Add(time.Hour)
	run, err = s.UpdateRunStatus(ctx, "run-1", domain.RunStatusQueued, 1, now)
	if err != nil {
		t.Fatalf("UpdateRunStatus(QUEUED) error = %v", err)
	}
	if run.Status != domain.RunStatusQueued || run.StateVersion != 2 {
		t.Fatalf("run = %s/%d, want QUEUED/2", run.Status, run.StateVersion)
	}
	run, err = s.UpdateRunStatus(ctx, "run-1", domain.RunStatusRunning, 2, now)
	if err != nil {
		t.Fatalf("UpdateRunStatus(RUNNING) error = %v", err)
	}
	if run.Status != domain.RunStatusRunning || run.StateVersion != 3 {
		t.Fatalf("run = %s/%d, want RUNNING/3", run.Status, run.StateVersion)
	}

	if _, err := s.UpdateRunStatus(ctx, "run-1", domain.RunStatusDraft, 3, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("RUNNING -> DRAFT error = %v, want ErrInvalidTransition", err)
	}
	if _, err := s.UpdateRunStatus(ctx, "run-1", domain.RunStatusQueued, 1, now); !errors.Is(err, domain.ErrStateConflict) {
		t.Errorf("stale version error = %v, want ErrStateConflict", err)
	}
	if _, err := s.UpdateRunStatus(ctx, "missing", domain.RunStatusQueued, 1, now); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing run error = %v, want ErrNotFound", err)
	}

	// 同状态是幂等 no-op，不递增版本。
	same, err := s.UpdateRunStatus(ctx, "run-1", domain.RunStatusRunning, 3, now)
	if err != nil {
		t.Fatalf("idempotent UpdateRunStatus() error = %v", err)
	}
	if same.StateVersion != 3 {
		t.Errorf("idempotent update version = %d, want 3", same.StateVersion)
	}

	if _, err := s.UpdateRunStatus(ctx, "run-1", domain.RunStatusSucceeded, 3, now); err != nil {
		t.Fatalf("UpdateRunStatus(SUCCEEDED) error = %v", err)
	}
	if _, err := s.UpdateRunStatus(ctx, "run-1", domain.RunStatusRunning, 4, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("terminal run restart error = %v, want ErrInvalidTransition", err)
	}
}

// TestRunImportedTranslationTracksRoundTrip 守护 custom Run 冻结译稿列的读写：
// 非 nil map（含空 map）与旧 Run 的 nil 必须可区分，否则旧库升级后会误切到冻结语义。
func TestRunImportedTranslationTracksRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")

	create := func(id string, tracks map[string]domain.TranslationTrackRef) {
		t.Helper()
		run := domain.WorkflowRun{
			ID:             id,
			ProjectID:      project.ID,
			Name:           id,
			Template:       "custom_pipeline",
			Status:         domain.RunStatusQueued,
			SourceLanguage: "en",
			Languages:      []string{"ja", "zh"},
			Config: domain.RunConfig{
				InputRevisionIDs:          []domain.ID{"asset-1"},
				ImportedTranslationTracks: tracks,
			},
			CreatedAt: testTime,
			UpdatedAt: testTime,
		}
		if err := s.CreateWorkflowRun(ctx, run); err != nil {
			t.Fatalf("CreateWorkflowRun(%s) error = %v", id, err)
		}
	}
	create("run-frozen", map[string]domain.TranslationTrackRef{
		"zh": {TrackID: "track-zh", RevisionID: "rev-zh"},
	})
	create("run-empty", map[string]domain.TranslationTrackRef{})
	create("run-legacy", nil)

	frozen, err := s.GetWorkflowRun(ctx, "run-frozen")
	if err != nil {
		t.Fatalf("GetWorkflowRun(frozen) error = %v", err)
	}
	want := domain.TranslationTrackRef{TrackID: "track-zh", RevisionID: "rev-zh"}
	if got := frozen.Config.ImportedTranslationTracks["zh"]; got != want {
		t.Errorf("frozen zh ref = %+v, want %+v", got, want)
	}

	empty, err := s.GetWorkflowRun(ctx, "run-empty")
	if err != nil {
		t.Fatalf("GetWorkflowRun(empty) error = %v", err)
	}
	if empty.Config.ImportedTranslationTracks == nil {
		t.Error("empty map read back as nil, want non-nil empty map")
	}
	if len(empty.Config.ImportedTranslationTracks) != 0 {
		t.Errorf("empty map = %+v, want empty", empty.Config.ImportedTranslationTracks)
	}

	legacy, err := s.GetWorkflowRun(ctx, "run-legacy")
	if err != nil {
		t.Fatalf("GetWorkflowRun(legacy) error = %v", err)
	}
	if legacy.Config.ImportedTranslationTracks != nil {
		t.Errorf("legacy nil map = %+v, want nil", legacy.Config.ImportedTranslationTracks)
	}
}

func TestRunNodeReviewGate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedRun(t, s, project.ID, "run-1")

	if err := s.UpdateRunNode(ctx, "run-1", domain.NodeKeyImport, domain.NodeStatusRunning, 10, "probing"); err != nil {
		t.Fatalf("UpdateRunNode(01 running) error = %v", err)
	}
	if err := s.UpdateRunNode(ctx, "run-1", domain.NodeKeyImport, domain.NodeStatusDone, 100, ""); err != nil {
		t.Fatalf("UpdateRunNode(01 done) error = %v", err)
	}
	if err := s.UpdateRunNode(ctx, "run-1", domain.NodeKeyErase, domain.NodeStatusWaitingReview, 50, ""); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("non-review node waiting error = %v, want ErrInvalidTransition", err)
	}
	if err := s.UpdateRunNode(ctx, "run-1", domain.NodeKeyTranslate, domain.NodeStatusRunning, 40, ""); err != nil {
		t.Fatalf("UpdateRunNode(06 running) error = %v", err)
	}
	if err := s.UpdateRunNode(ctx, "run-1", domain.NodeKeyTranslate, domain.NodeStatusDone, 100, ""); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("06 done before review error = %v, want ErrInvalidTransition", err)
	}
	if err := s.UpdateRunNode(ctx, "run-1", domain.NodeKeyTranslate, domain.NodeStatusWaitingReview, 80, "waiting"); err != nil {
		t.Fatalf("UpdateRunNode(06 waiting_review) error = %v", err)
	}
	// 同状态更新进度是允许的。
	if err := s.UpdateRunNode(ctx, "run-1", domain.NodeKeyTranslate, domain.NodeStatusWaitingReview, 90, ""); err != nil {
		t.Fatalf("UpdateRunNode progress error = %v", err)
	}
	if err := s.UpdateRunNode(ctx, "run-1", domain.NodeKeyTranslate, domain.NodeStatusDone, 100, ""); err != nil {
		t.Fatalf("UpdateRunNode(06 done) error = %v", err)
	}
	if err := s.UpdateRunNode(ctx, "run-1", domain.NodeKeyMix, domain.NodeStatusRunning, 101, ""); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Errorf("invalid progress error = %v, want ErrInvalidRequest", err)
	}

	run, err := s.GetWorkflowRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetWorkflowRun() error = %v", err)
	}
	statuses := map[domain.PipelineNodeKey]domain.NodeStatus{}
	for _, node := range run.Nodes {
		statuses[node.Key] = node.Status
	}
	if statuses[domain.NodeKeyImport] != domain.NodeStatusDone {
		t.Errorf("01 status = %s, want done", statuses[domain.NodeKeyImport])
	}
	if statuses[domain.NodeKeyTranslate] != domain.NodeStatusDone {
		t.Errorf("06 status = %s, want done", statuses[domain.NodeKeyTranslate])
	}
}

func TestTaskLifecycleAndAttempts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedRun(t, s, project.ID, "run-1")

	task := domain.Task{
		ID:        "task-1",
		ProjectID: project.ID,
		RunID:     "run-1",
		Kind:      domain.TaskKindSegment,
		NodeKey:   domain.NodeKeyClone,
		SegmentID: "seg-1",
		Language:  "ja",
		Label:     "句级配音",
		Status:    domain.TaskStatusQueued,
		Steps: []domain.TaskStep{
			{StepID: "step-1", Status: domain.StepStatusPending},
			{StepID: "step-2", Status: domain.StepStatusPending},
		},
		InputRevisionIDs: []domain.ID{"srev-1"},
		InputDigest:      "input-abc",
		Params:           map[string]any{"speed": 1.0, "pitch": 0},
		ModelLock:        &domain.ReleaseRef{ReleaseID: "tts-ja", Version: "1.0.0", Digest: "digest"},
		CreatedAt:        testTime,
		UpdatedAt:        testTime,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	got, err := s.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if got.Status != domain.TaskStatusQueued || got.StateVersion != 1 || len(got.Steps) != 2 {
		t.Fatalf("task = %+v", got)
	}
	if got.Language != "ja" || got.InputDigest != "input-abc" {
		t.Errorf("task internal fields = %q/%q", got.Language, got.InputDigest)
	}
	if got.Params["speed"] != 1.0 {
		t.Errorf("params = %v", got.Params)
	}
	if got.ModelLock == nil || got.ModelLock.ReleaseID != "tts-ja" {
		t.Errorf("model lock = %+v", got.ModelLock)
	}
	if got.ArtifactIDs == nil || len(got.ArtifactIDs) != 0 {
		t.Errorf("artifact ids = %v, want empty non-nil", got.ArtifactIDs)
	}

	list, err := s.ListProjectTasks(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListProjectTasks() error = %v", err)
	}
	if len(list) != 1 || list[0].Steps != nil {
		t.Errorf("task list should omit steps: %+v", list)
	}

	now := testTime.Add(time.Hour)
	got, err = s.UpdateTaskStatus(ctx, "task-1", domain.TaskStatusRunning, 1, now)
	if err != nil {
		t.Fatalf("UpdateTaskStatus(RUNNING) error = %v", err)
	}
	if got.Status != domain.TaskStatusRunning || got.StateVersion != 2 {
		t.Fatalf("task = %s/%d, want RUNNING/2", got.Status, got.StateVersion)
	}
	if _, err := s.UpdateTaskStatus(ctx, "task-1", domain.TaskStatusQueued, 2, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("RUNNING -> QUEUED error = %v, want ErrInvalidTransition", err)
	}
	if _, err := s.UpdateTaskStatus(ctx, "task-1", domain.TaskStatusSucceeded, 1, now); !errors.Is(err, domain.ErrStateConflict) {
		t.Errorf("stale task version error = %v, want ErrStateConflict", err)
	}
	if err := s.SetTaskProgress(ctx, "task-1", 42, now); err != nil {
		t.Fatalf("SetTaskProgress() error = %v", err)
	}
	got, err = s.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if got.Progress != 42 || got.StateVersion != 2 {
		t.Errorf("progress/version = %d/%d, want 42/2 (progress must not bump version)", got.Progress, got.StateVersion)
	}
	steps := []domain.TaskStep{
		{StepID: "step-1", Status: domain.StepStatusSucceeded, AttemptNo: 1, Epoch: 7},
		{StepID: "step-2", Status: domain.StepStatusPending},
	}
	if err := s.SetTaskSteps(ctx, "task-1", steps, now); err != nil {
		t.Fatalf("SetTaskSteps() error = %v", err)
	}
	got, err = s.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if got.Steps[0].Status != domain.StepStatusSucceeded || got.Steps[0].Epoch != 7 {
		t.Errorf("steps = %+v", got.Steps)
	}

	// 尝试编号单调且唯一。
	started := testTime
	attempt1 := domain.Attempt{
		ID: "attempt-1", TaskID: "task-1", StepID: "step-1", AttemptNo: 1, Epoch: 7,
		ExecutorID: "runner-1", Status: domain.StepStatusRunning, StartedAt: &started,
	}
	if err := s.CreateAttempt(ctx, attempt1); err != nil {
		t.Fatalf("CreateAttempt() error = %v", err)
	}
	if err := s.CreateAttempt(ctx, attempt1); !errors.Is(err, domain.ErrStateConflict) {
		t.Errorf("duplicate attempt error = %v, want ErrStateConflict", err)
	}
	finished := testTime.Add(time.Minute)
	attempt2 := domain.Attempt{
		ID: "attempt-2", TaskID: "task-1", StepID: "step-1", AttemptNo: 2, Epoch: 8,
		Status: domain.StepStatusSucceeded, StartedAt: &started, FinishedAt: &finished,
	}
	if err := s.CreateAttempt(ctx, attempt2); err != nil {
		t.Fatalf("CreateAttempt(2) error = %v", err)
	}
	attempts, err := s.ListAttempts(ctx, "task-1")
	if err != nil {
		t.Fatalf("ListAttempts() error = %v", err)
	}
	if len(attempts) != 2 || attempts[1].AttemptNo != 2 || attempts[1].FinishedAt == nil {
		t.Fatalf("attempts = %+v", attempts)
	}
	if next := domain.NextAttemptNo(attempts); next != 3 {
		t.Errorf("NextAttemptNo() = %d, want 3", next)
	}

	// 产物提交。
	commit := domain.ArtifactCommit{
		ID: "commit-1", AttemptID: "attempt-2", AssetIDs: []domain.ID{"asset-1"},
		Status: domain.CommitStatusCommitted, CommittedAt: &finished,
	}
	if err := s.CreateArtifactCommit(ctx, commit); err != nil {
		t.Fatalf("CreateArtifactCommit() error = %v", err)
	}
	commits, err := s.ListArtifactCommits(ctx, "attempt-2")
	if err != nil {
		t.Fatalf("ListArtifactCommits() error = %v", err)
	}
	if len(commits) != 1 || commits[0].AssetIDs[0] != "asset-1" || commits[0].CommittedAt == nil {
		t.Errorf("commits = %+v", commits)
	}

	if _, err := s.UpdateTaskStatus(ctx, "task-1", domain.TaskStatusSucceeded, 2, now); err != nil {
		t.Fatalf("UpdateTaskStatus(SUCCEEDED) error = %v", err)
	}
	if _, err := s.UpdateTaskStatus(ctx, "task-1", domain.TaskStatusRunning, 3, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("terminal task restart error = %v, want ErrInvalidTransition", err)
	}
}

func TestTaskRetryFlow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	task := domain.Task{
		ID: "task-retry", ProjectID: project.ID, Kind: domain.TaskKindNode,
		NodeKey: domain.NodeKeyClone, Status: domain.TaskStatusQueued,
		InputDigest: "same-input", CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	failed, err := s.UpdateTaskStatus(ctx, "task-retry", domain.TaskStatusFailed, 1, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("UpdateTaskStatus(FAILED) error = %v", err)
	}
	prepared, err := domain.PrepareTaskRetry(failed, "same-input")
	if err != nil {
		t.Fatalf("PrepareTaskRetry() error = %v", err)
	}
	if _, err := domain.PrepareTaskRetry(failed, "changed-input"); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Errorf("retry with changed input error = %v, want ErrRevisionConflict", err)
	}
	requeued, err := s.UpdateTaskStatus(ctx, "task-retry", prepared.Status, failed.StateVersion, testTime.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("UpdateTaskStatus(retry QUEUED) error = %v", err)
	}
	if requeued.Status != domain.TaskStatusQueued {
		t.Errorf("retried task status = %s, want QUEUED", requeued.Status)
	}
	// QUEUED 不可再次原地重试（需先失败/中断）。
	if _, err := domain.PrepareTaskRetry(requeued, "same-input"); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("PrepareTaskRetry(QUEUED) error = %v, want ErrInvalidTransition", err)
	}
}

func TestUpdateTaskResult(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")

	if err := s.CreateTask(ctx, domain.Task{
		ID: "task-result", ProjectID: project.ID, Kind: domain.TaskKindNode,
		NodeKey: domain.NodeKeyImport, Status: domain.TaskStatusQueued,
		CreatedAt: testTime, UpdatedAt: testTime,
	}); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	now := testTime.Add(time.Hour)
	if _, err := s.UpdateTaskStatus(ctx, "task-result", domain.TaskStatusRunning, 1, now); err != nil {
		t.Fatalf("UpdateTaskStatus(RUNNING) error = %v", err)
	}
	got, err := s.UpdateTaskResult(ctx, "task-result", domain.TaskStatusSucceeded,
		[]domain.ID{"asset-1", "asset-2"}, nil, 2, now)
	if err != nil {
		t.Fatalf("UpdateTaskResult() error = %v", err)
	}
	if got.Status != domain.TaskStatusSucceeded || got.StateVersion != 3 {
		t.Fatalf("task = %s/%d, want SUCCEEDED/3", got.Status, got.StateVersion)
	}
	if len(got.ArtifactIDs) != 2 || got.ArtifactIDs[1] != "asset-2" || got.Error != nil {
		t.Errorf("task result = %+v", got)
	}
	// 同终态重复提交是幂等回放：不改产物、不递增版本（终态不可变）。
	replayed, err := s.UpdateTaskResult(ctx, "task-result", domain.TaskStatusSucceeded,
		[]domain.ID{"asset-overwrite"}, nil, 3, now)
	if err != nil {
		t.Fatalf("duplicate terminal UpdateTaskResult() error = %v", err)
	}
	if replayed.StateVersion != 3 || len(replayed.ArtifactIDs) != 2 || replayed.ArtifactIDs[1] != "asset-2" {
		t.Errorf("duplicate terminal result mutated task: %+v", replayed)
	}
	if _, err := s.UpdateTaskResult(ctx, "task-result", domain.TaskStatusFailed, nil, nil, 2, now); !errors.Is(err, domain.ErrStateConflict) {
		t.Errorf("stale result error = %v, want ErrStateConflict", err)
	}
	if _, err := s.UpdateTaskResult(ctx, "task-result", domain.TaskStatusRunning, nil, nil, 3, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("terminal result error = %v, want ErrInvalidTransition", err)
	}

	// 失败结果同时持久化安全错误信息。
	if err := s.CreateTask(ctx, domain.Task{
		ID: "task-fail", ProjectID: project.ID, Kind: domain.TaskKindNode,
		NodeKey: domain.NodeKeyClone, Status: domain.TaskStatusQueued,
		CreatedAt: testTime, UpdatedAt: testTime,
	}); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if _, err := s.UpdateTaskStatus(ctx, "task-fail", domain.TaskStatusRunning, 1, now); err != nil {
		t.Fatalf("UpdateTaskStatus(RUNNING) error = %v", err)
	}
	failed, err := s.UpdateTaskResult(ctx, "task-fail", domain.TaskStatusFailed, nil,
		&domain.ErrorInfo{Code: domain.ErrorCodeResourceExhausted, MessageKey: "task.oom", Retryable: true, ActionIDs: []domain.ID{"retry"}},
		2, now)
	if err != nil {
		t.Fatalf("UpdateTaskResult(failed) error = %v", err)
	}
	if failed.Error == nil || failed.Error.Code != domain.ErrorCodeResourceExhausted || !failed.Error.Retryable {
		t.Errorf("failed task error = %+v", failed.Error)
	}
}

func TestTransactionalTaskEventReceipt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	now := testTime

	err := s.DoTx(ctx, func(tx *Tx) error {
		task := domain.Task{
			ID: "task-tx", ProjectID: project.ID, Kind: domain.TaskKindNode,
			NodeKey: domain.NodeKeyImport, Status: domain.TaskStatusQueued,
			CreatedAt: testTime, UpdatedAt: testTime,
		}
		if err := tx.CreateTask(ctx, task); err != nil {
			return err
		}
		if _, err := tx.AppendEvent(ctx, domain.DomainEvent{
			Type: "task.created", ProjectID: project.ID, TaskID: "task-tx", StateVersion: 1,
			Data: map[string]any{"status": "QUEUED"},
		}, now); err != nil {
			return err
		}
		receipt, created, err := tx.PutReceipt(ctx, Receipt{
			WorkspaceID: "ws-1", Operation: "POST /tasks", Key: "idem-1",
			RequestDigest: "digest-1", ResponseJSON: []byte(`{"id":"task-tx"}`), CreatedAt: now,
		})
		if err != nil {
			return err
		}
		if !created || receipt.Key != "idem-1" {
			t.Errorf("receipt = %+v created=%v", receipt, created)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("DoTx() error = %v", err)
	}
	if _, err := s.GetTask(ctx, "task-tx"); err != nil {
		t.Fatalf("task not committed: %v", err)
	}
	seq, err := s.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("LatestEventSeq() error = %v", err)
	}
	if seq != 1 {
		t.Errorf("latest event seq = %d, want 1", seq)
	}

	// 回滚：业务写入、事件与回执都必须消失。
	rollbackErr := errors.New("injected failure")
	err = s.DoTx(ctx, func(tx *Tx) error {
		if err := tx.CreateTask(ctx, domain.Task{
			ID: "task-rollback", ProjectID: project.ID, Kind: domain.TaskKindNode,
			NodeKey: domain.NodeKeyImport, Status: domain.TaskStatusQueued,
			CreatedAt: testTime, UpdatedAt: testTime,
		}); err != nil {
			return err
		}
		if _, err := tx.AppendEvent(ctx, domain.DomainEvent{Type: "task.created"}, now); err != nil {
			return err
		}
		if _, _, err := tx.PutReceipt(ctx, Receipt{
			WorkspaceID: "ws-1", Operation: "POST /tasks", Key: "idem-rollback",
			RequestDigest: "digest-r", ResponseJSON: []byte(`{}`), CreatedAt: now,
		}); err != nil {
			return err
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("DoTx() error = %v, want rollback error", err)
	}
	if _, err := s.GetTask(ctx, "task-rollback"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("rolled back task error = %v, want ErrNotFound", err)
	}
	seq, err = s.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("LatestEventSeq() error = %v", err)
	}
	if seq != 1 {
		t.Errorf("latest event seq after rollback = %d, want 1", seq)
	}
	if _, err := s.GetReceipt(ctx, "ws-1", "POST /tasks", "idem-rollback"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("rolled back receipt error = %v, want ErrNotFound", err)
	}
}
