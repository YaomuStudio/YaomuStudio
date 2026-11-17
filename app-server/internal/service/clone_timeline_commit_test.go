package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/resultcommit"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func prepareCloneCandidate(
	t *testing.T,
	env *exportTestEnv,
	task domain.Task,
) resultcommit.Prepared {
	t.Helper()
	dir := filepath.Join(env.svc.workDir(), string(task.ID), "attempt-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir attempt output: %v", err)
	}
	payload := []byte("RIFF-clone-candidate")
	path := filepath.Join(dir, "candidate.wav")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	prepared, err := env.svc.PrepareNodeResult(context.Background(), task, resultcommit.ExecutionResult{
		OutputDir: dir,
		Outputs:   []resultcommit.ExecutionOutput{{RelPath: "candidate.wav", SizeBytes: int64(len(payload))}},
	})
	if err != nil {
		t.Fatalf("PrepareNodeResult: %v", err)
	}
	return prepared
}

// commitCloneTask 走真实终态事务：RUNNING + attempt -> SUCCEEDED + 产物提交。
func commitCloneTask(t *testing.T, env *exportTestEnv, taskID domain.ID) []domain.DomainEvent {
	t.Helper()
	ctx := context.Background()
	task, err := env.store.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTask(%s): %v", taskID, err)
	}
	now := env.svc.Now()
	task, err = env.store.UpdateTaskStatus(ctx, task.ID, domain.TaskStatusRunning, task.StateVersion, now)
	if err != nil {
		t.Fatalf("UpdateTaskStatus(%s): %v", taskID, err)
	}
	if err := env.store.CreateAttempt(ctx, domain.Attempt{
		ID: store.NewID("attempt"), TaskID: task.ID, StepID: "main",
		AttemptNo: 1, Epoch: 3, Status: domain.StepStatusRunning, StartedAt: &now,
	}); err != nil {
		t.Fatalf("CreateAttempt(%s): %v", taskID, err)
	}
	prepared := prepareCloneCandidate(t, env, task)
	var events []domain.DomainEvent
	if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
		updated, err := tx.UpdateTaskResult(ctx, task.ID, domain.TaskStatusSucceeded,
			prepared.ArtifactIDs(), nil, task.StateVersion, now)
		if err != nil {
			return err
		}
		local := []domain.DomainEvent{}
		if err := prepared.Commit(ctx, tx, updated, &local); err != nil {
			return err
		}
		events = local
		return nil
	}); err != nil {
		t.Fatalf("commit clone task %s: %v", taskID, err)
	}
	return events
}

func cloneAssemblyTask(t *testing.T, env *exportTestEnv, runID domain.ID, language string) domain.Task {
	t.Helper()
	tasks, err := env.store.ListRunTasks(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	for _, task := range tasks {
		if task.Kind == domain.TaskKindNode && task.NodeKey == domain.NodeKeyClone &&
			task.Language == language && task.SegmentID == "" && task.Params["op"] == "assemble" {
			return task
		}
	}
	t.Fatalf("clone assembly task %s not found", language)
	return domain.Task{}
}

func cloneSegmentTask(t *testing.T, env *exportTestEnv, runID domain.ID, language, segmentID string) domain.Task {
	t.Helper()
	tasks, err := env.store.ListRunTasks(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	for _, task := range tasks {
		if task.Kind == domain.TaskKindSegment && task.NodeKey == domain.NodeKeyClone &&
			task.Language == language && task.SegmentID == segmentID {
			return task
		}
	}
	t.Fatalf("clone segment task %s/%s not found", language, segmentID)
	return domain.Task{}
}

// 运行中保存越界译稿后，已冻结的合法 07 成功提交不得被推进聚合回滚；
// 新译稿不得生成可执行 07/08，而是留下 INVALID_REQUEST 的可读失败。
func TestCompletedCloneCommitSurvivesInvalidCurrentRevision(t *testing.T) {
	ctx := context.Background()
	env := newCloneTimelineEnv(t)
	project := env.seedProject("clone commit")
	source, translation := seedCloneTimelineTracks(t, env, project.ID,
		&domain.MediaMeta{Container: "wav", DurationMs: 2000, AudioTracks: 1},
		[]domain.Segment{{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"}},
		[]domain.Segment{{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"}})
	response, err := env.svc.CreateWorkflowRun(ctx, Idem{}, cloneRunInput(project.ID, source.ID, true))
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := response.Body.(domain.WorkflowRun)
	tasks, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	var frozen domain.Task
	for _, task := range tasks {
		if task.Kind == domain.TaskKindSegment && task.NodeKey == domain.NodeKeyClone {
			frozen = task
			break
		}
	}
	if frozen.ID == "" {
		t.Fatal("run created no frozen 07 segment task")
	}
	if _, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, translation.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: translation.CurrentRevisionID,
		Segments:       []domain.SegmentInput{{ID: "tr-1", StartMs: 0, EndMs: 2500, Text: "你好"}},
	}); err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}

	now := env.svc.Now()
	frozen, err = env.store.UpdateTaskStatus(ctx, frozen.ID, domain.TaskStatusRunning, frozen.StateVersion, now)
	if err != nil {
		t.Fatalf("UpdateTaskStatus: %v", err)
	}
	if err := env.store.CreateAttempt(ctx, domain.Attempt{
		ID: store.NewID("attempt"), TaskID: frozen.ID, StepID: "main",
		AttemptNo: 1, Epoch: 3, Status: domain.StepStatusRunning, StartedAt: &now,
	}); err != nil {
		t.Fatalf("CreateAttempt: %v", err)
	}
	prepared := prepareCloneCandidate(t, env, frozen)

	var events []domain.DomainEvent
	err = env.store.DoTx(ctx, func(tx *store.Tx) error {
		updated, err := tx.UpdateTaskResult(ctx, frozen.ID, domain.TaskStatusSucceeded,
			prepared.ArtifactIDs(), nil, frozen.StateVersion, now)
		if err != nil {
			return err
		}
		local := []domain.DomainEvent{}
		if err := prepared.Commit(ctx, tx, updated, &local); err != nil {
			return err
		}
		events = local
		return nil
	})
	if err != nil {
		t.Fatalf("completed clone commit rolled back: %v", err)
	}

	stored, err := env.store.GetTask(ctx, frozen.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.Status != domain.TaskStatusSucceeded || len(stored.ArtifactIDs) == 0 {
		t.Fatalf("completed task lost: status=%s artifacts=%v", stored.Status, stored.ArtifactIDs)
	}
	candidates, err := env.svc.ListCandidates(ctx, "tr-1", "zh")
	if err != nil || len(candidates) == 0 {
		t.Fatalf("candidate lost: %v/%d", err, len(candidates))
	}

	final, err := env.store.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if nodeStatusOf(final, domain.NodeKeyClone) != domain.NodeStatusFailed {
		t.Fatalf("clone node = %s, want failed for invalid current revision",
			nodeStatusOf(final, domain.NodeKeyClone))
	}
	after, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks after commit: %v", err)
	}
	segmentTasks := 0
	failureKey := ""
	for _, task := range after {
		if task.Kind == domain.TaskKindSegment && task.NodeKey == domain.NodeKeyClone {
			segmentTasks++
		}
		if task.Kind == domain.TaskKindNode && task.NodeKey == domain.NodeKeyClone &&
			task.Status == domain.TaskStatusFailed && task.Error != nil {
			if task.Error.Code != domain.ErrorCodeInvalidRequest {
				t.Fatalf("clone failure code = %s, want INVALID_REQUEST", task.Error.Code)
			}
			failureKey = task.Error.MessageKey
		}
	}
	if segmentTasks != 1 {
		t.Fatalf("invalid revision created executable 07 tasks: count=%d", segmentTasks)
	}
	if failureKey != "task.clone_timeline_out_of_range" {
		t.Fatalf("readable clone failure key = %q; tasks=%+v", failureKey, after)
	}
	if !hasEvent(events, "segment.candidate_selected") || !hasEvent(events, "task.failed") {
		t.Fatalf("required events missing: %+v", eventTypes(events))
	}

	// 重复推进命中同一失败任务，不新增任务或事件。
	replayedTasks := len(after)
	if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
		var replay []domain.DomainEvent
		if err := env.svc.advancePipeline(ctx, tx, run.ID, &replay); err != nil {
			return err
		}
		if err := env.svc.advancePipeline(ctx, tx, run.ID, &replay); err != nil {
			return err
		}
		if len(replay) != 0 {
			t.Fatalf("idempotent advance emitted events: %+v", eventTypes(replay))
		}
		return nil
	}); err != nil {
		t.Fatalf("repeated advance: %v", err)
	}
	replayed, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks after replay: %v", err)
	}
	if len(replayed) != replayedTasks {
		t.Fatalf("repeated advance created tasks: %d -> %d", replayedTasks, len(replayed))
	}
}

// 关闭开关的旧 Run 仍按既有行为提交，不新增媒体门槛或失败任务。
func TestCompletedCloneCommitWithSwitchOffKeepsExistingBehavior(t *testing.T) {
	ctx := context.Background()
	env := newCloneTimelineEnv(t)
	project := env.seedProject("clone commit off")
	source, translation := seedCloneTimelineTracks(t, env, project.ID,
		&domain.MediaMeta{Container: "wav", DurationMs: 2000, AudioTracks: 1},
		[]domain.Segment{{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"}},
		[]domain.Segment{{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"}})
	response, err := env.svc.CreateWorkflowRun(ctx, Idem{}, cloneRunInput(project.ID, source.ID, false))
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := response.Body.(domain.WorkflowRun)
	if _, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, translation.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: translation.CurrentRevisionID,
		Segments:       []domain.SegmentInput{{ID: "tr-1", StartMs: 0, EndMs: 2500, Text: "你好"}},
	}); err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	tasks, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	var frozen domain.Task
	for _, task := range tasks {
		if task.Kind == domain.TaskKindSegment && task.NodeKey == domain.NodeKeyClone {
			frozen = task
			break
		}
	}
	now := env.svc.Now()
	frozen, err = env.store.UpdateTaskStatus(ctx, frozen.ID, domain.TaskStatusRunning, frozen.StateVersion, now)
	if err != nil {
		t.Fatalf("UpdateTaskStatus: %v", err)
	}
	if err := env.store.CreateAttempt(ctx, domain.Attempt{
		ID: store.NewID("attempt"), TaskID: frozen.ID, StepID: "main",
		AttemptNo: 1, Epoch: 3, Status: domain.StepStatusRunning, StartedAt: &now,
	}); err != nil {
		t.Fatalf("CreateAttempt: %v", err)
	}
	prepared := prepareCloneCandidate(t, env, frozen)
	if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
		updated, err := tx.UpdateTaskResult(ctx, frozen.ID, domain.TaskStatusSucceeded,
			prepared.ArtifactIDs(), nil, frozen.StateVersion, now)
		if err != nil {
			return err
		}
		var events []domain.DomainEvent
		return prepared.Commit(ctx, tx, updated, &events)
	}); err != nil {
		t.Fatalf("switch-off commit failed: %v", err)
	}
	after, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks after commit: %v", err)
	}
	for _, task := range after {
		if task.Kind == domain.TaskKindNode && task.NodeKey == domain.NodeKeyClone &&
			task.Status == domain.TaskStatusFailed {
			t.Fatalf("switch off created a media gate failure: %+v", task.Error)
		}
	}
}

// 当前稿越界时，同语言其他已冻结 07 单元仍需排空；只有全部旧单元收敛后才
// 允许节点失败并让 Run 进入 PARTIAL。
func TestCloneTimelineFailureWaitsForInFlightFrozenTasks(t *testing.T) {
	ctx := context.Background()
	env := newCloneTimelineEnv(t)
	project := env.seedProject("clone drain")
	source := env.seedMediaAsset(project.ID, "voice.wav", domain.AssetTypeAudio,
		env.tempFile("voice.wav", "voice-source"),
		&domain.MediaMeta{Container: "wav", DurationMs: 2000, AudioTracks: 1})
	seedServiceTrack(t, env.store, project.ID, "en", domain.TrackTypeTranscription, []domain.Segment{
		{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"},
		{ID: "src-2", StartMs: 1000, EndMs: 1900, Text: "World"},
	})
	zh := seedServiceTrack(t, env.store, project.ID, "zh", domain.TrackTypeTranslation, []domain.Segment{
		{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"},
		{ID: "tr-2", StartMs: 1000, EndMs: 1900, Text: "世界"},
	})
	in := cloneRunInput(project.ID, source.ID, true)
	response, err := env.svc.CreateWorkflowRun(ctx, Idem{}, in)
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := response.Body.(domain.WorkflowRun)

	saved, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, zh.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: zh.CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"},
			{ID: "tr-2", StartMs: 1000, EndMs: 2500, Text: "世界"},
		},
	})
	if err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	invalidRevision := saved.Body.(domain.SubtitleRevision)

	zhFirst := cloneSegmentTask(t, env, run.ID, "zh", "tr-1")
	zhSecond := cloneSegmentTask(t, env, run.ID, "zh", "tr-2")
	events := commitCloneTask(t, env, zhFirst.ID)
	if !hasEvent(events, "segment.candidate_selected") || !hasEvent(events, "task.failed") {
		t.Fatalf("commit events missing: %+v", eventTypes(events))
	}
	candidates, err := env.svc.ListCandidates(ctx, "tr-1", "zh")
	if err != nil || len(candidates) == 0 {
		t.Fatalf("first frozen candidate lost: %v/%d", err, len(candidates))
	}

	final, err := env.store.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if final.Status.Terminal() {
		t.Fatalf("run converged before frozen units drained: %s", final.Status)
	}
	if nodeStatusOf(final, domain.NodeKeyClone) == domain.NodeStatusFailed {
		t.Fatal("clone node failed while same-language frozen units are still pending")
	}
	if got := cloneSegmentTask(t, env, run.ID, "zh", "tr-2"); got.Status != domain.TaskStatusQueued {
		t.Fatalf("frozen zh task = %s, want queued", got.Status)
	}
	assertNoExecutableTaskForRevision(t, env, run.ID, invalidRevision.ID)

	commitCloneTask(t, env, zhSecond.ID)
	final, err = env.store.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if final.Status != domain.RunStatusPartial {
		t.Fatalf("run status = %s, want partial after all units drained", final.Status)
	}
	if nodeStatusOf(final, domain.NodeKeyClone) != domain.NodeStatusFailed {
		t.Fatalf("clone node = %s, want failed", nodeStatusOf(final, domain.NodeKeyClone))
	}
	assertNoExecutableTaskForRevision(t, env, run.ID, invalidRevision.ID)
	tasks, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	segments := 0
	for _, task := range tasks {
		if task.Kind == domain.TaskKindSegment && task.NodeKey == domain.NodeKeyClone {
			segments++
			if task.Status != domain.TaskStatusSucceeded {
				t.Fatalf("frozen task %s did not drain: %s", task.ID, task.Status)
			}
		}
	}
	if segments != 2 {
		t.Fatalf("frozen segment tasks = %d, want 2", segments)
	}
}

// 单个语言当前稿越界不影响其他语言的合法冻结任务排空。
func TestCloneTimelineFailureDoesNotBlockOtherLanguages(t *testing.T) {
	ctx := context.Background()
	env := newCloneTimelineEnv(t)
	project := env.seedProject("clone other language")
	source := env.seedMediaAsset(project.ID, "voice.wav", domain.AssetTypeAudio,
		env.tempFile("voice.wav", "voice-source"),
		&domain.MediaMeta{Container: "wav", DurationMs: 2000, AudioTracks: 1})
	seedServiceTrack(t, env.store, project.ID, "en", domain.TrackTypeTranscription, []domain.Segment{
		{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"},
		{ID: "src-2", StartMs: 1000, EndMs: 1900, Text: "World"},
	})
	zh := seedServiceTrack(t, env.store, project.ID, "zh", domain.TrackTypeTranslation, []domain.Segment{
		{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"},
		{ID: "tr-2", StartMs: 1000, EndMs: 1900, Text: "世界"},
	})
	seedServiceTrack(t, env.store, project.ID, "ja", domain.TrackTypeTranslation, []domain.Segment{
		{ID: "ja-1", StartMs: 0, EndMs: 900, Text: "こんにちは"},
	})
	in := cloneRunInput(project.ID, source.ID, true)
	in.TargetLanguages = []string{"zh", "ja"}
	response, err := env.svc.CreateWorkflowRun(ctx, Idem{}, in)
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := response.Body.(domain.WorkflowRun)
	if _, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, zh.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: zh.CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"},
			{ID: "tr-2", StartMs: 1000, EndMs: 2500, Text: "世界"},
		},
	}); err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	commitCloneTask(t, env, cloneSegmentTask(t, env, run.ID, "zh", "tr-1").ID)
	commitCloneTask(t, env, cloneSegmentTask(t, env, run.ID, "zh", "tr-2").ID)

	final, err := env.store.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if final.Status.Terminal() {
		t.Fatalf("run converged before other language drained: %s", final.Status)
	}
	if nodeStatusOf(final, domain.NodeKeyClone) == domain.NodeStatusFailed {
		t.Fatal("clone node failed while other language is still pending")
	}
	jaTask := cloneSegmentTask(t, env, run.ID, "ja", "ja-1")
	if jaTask.Status != domain.TaskStatusQueued {
		t.Fatalf("other language task = %s, want queued", jaTask.Status)
	}

	commitCloneTask(t, env, jaTask.ID)
	commitCloneTask(t, env, cloneAssemblyTask(t, env, run.ID, "ja").ID)
	final, err = env.store.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if final.Status != domain.RunStatusPartial {
		for _, node := range final.Nodes {
			t.Logf("node %s=%s", node.Key, node.Status)
		}
		if tasks, listErr := env.store.ListRunTasks(ctx, run.ID); listErr == nil {
			for _, task := range tasks {
				op, _ := task.Params["op"].(string)
				t.Logf("task node=%s lang=%s seg=%s status=%s op=%s", task.NodeKey, task.Language, task.SegmentID, task.Status, op)
			}
		}
		t.Fatalf("run status = %s, want partial", final.Status)
	}
	if nodeStatusOf(final, domain.NodeKeyClone) != domain.NodeStatusFailed {
		t.Fatalf("clone node = %s, want failed", nodeStatusOf(final, domain.NodeKeyClone))
	}
}

func assertNoExecutableTaskForRevision(t *testing.T, env *exportTestEnv, runID, revisionID domain.ID) {
	t.Helper()
	tasks, err := env.store.ListRunTasks(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	for _, task := range tasks {
		if task.Kind == domain.TaskKindSegment && task.NodeKey == domain.NodeKeyClone &&
			len(task.InputRevisionIDs) > 0 && task.InputRevisionIDs[0] == revisionID {
			t.Fatalf("invalid revision created executable 07 task %s (%s)", task.ID, task.Status)
		}
	}
}

// 非时间轴错误不能被失败归属吞掉；可复用入口必须原样返回，交给调用方传播。
func TestEnsureCloneTimelineFailuresPropagatesNonTimelineErrors(t *testing.T) {
	ctx := context.Background()
	env := newCloneTimelineEnv(t)
	project := env.seedProject("clone ambiguous")
	source := env.seedMediaAsset(project.ID, "voice.wav", domain.AssetTypeAudio,
		env.tempFile("voice.wav", "voice-source"),
		&domain.MediaMeta{Container: "wav", DurationMs: 2000, AudioTracks: 1})
	seedServiceTrack(t, env.store, project.ID, "en", domain.TrackTypeTranscription, []domain.Segment{
		{ID: "src-a", StartMs: 1000, EndMs: 1500, Text: "A"},
		{ID: "src-b", StartMs: 1500, EndMs: 2000, Text: "B"},
	})
	translation := seedServiceTrack(t, env.store, project.ID, "zh", domain.TrackTypeTranslation, []domain.Segment{
		{ID: "tr-1", StartMs: 1000, EndMs: 1500, Text: "甲"},
	})
	response, err := env.svc.CreateWorkflowRun(ctx, Idem{}, cloneRunInput(project.ID, source.ID, false))
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := response.Body.(domain.WorkflowRun)
	if _, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, translation.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: translation.CurrentRevisionID,
		Segments:       []domain.SegmentInput{{ID: "tr-1", StartMs: 1000, EndMs: 2000, Text: "甲"}},
	}); err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	err = env.store.DoTx(ctx, func(tx *store.Tx) error {
		snap, err := env.svc.loadPipelineSnapshot(ctx, tx, run.ID)
		if err != nil {
			return err
		}
		snap.run.Config.ValidateTimestamps = true
		tpl, err := pipelineTemplateForRun(*snap.run)
		if err != nil {
			return err
		}
		var events []domain.DomainEvent
		return env.svc.ensureCloneTimelineFailures(ctx, tx, snap, tpl, &events)
	})
	if !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("non-timeline error = %v, want INVALID_REQUEST", err)
	}
	if errors.Is(err, domain.ErrCloneTimelineOutOfRange) ||
		errors.Is(err, domain.ErrCloneTimelineUnknownDuration) {
		t.Fatalf("non-timeline error misclassified: %v", err)
	}
	tasks, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	for _, task := range tasks {
		if task.Kind == domain.TaskKindNode && task.NodeKey == domain.NodeKeyClone &&
			task.Status == domain.TaskStatusFailed {
			t.Fatalf("non-timeline error recorded as clone failure: %+v", task.Error)
		}
	}
}

func hasEvent(events []domain.DomainEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func eventTypes(events []domain.DomainEvent) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}
