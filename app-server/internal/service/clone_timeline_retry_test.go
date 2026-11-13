package service

import (
	"context"
	"errors"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func seedCloneTimelineFailureRun(t *testing.T) (*exportTestEnv, domain.WorkflowRun, domain.Task) {
	t.Helper()
	ctx := context.Background()
	env := newCloneTimelineEnv(t)
	project := env.seedProject("clone retry")
	source := env.seedMediaAsset(project.ID, "voice.wav", domain.AssetTypeAudio,
		env.tempFile("voice.wav", "voice-source"),
		&domain.MediaMeta{Container: "wav", DurationMs: 2000, AudioTracks: 1})
	seedServiceTrack(t, env.store, project.ID, "en", domain.TrackTypeTranscription, []domain.Segment{
		{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"},
		{ID: "src-2", StartMs: 1000, EndMs: 1900, Text: "World"},
	})
	translation := seedServiceTrack(t, env.store, project.ID, "zh", domain.TrackTypeTranslation, []domain.Segment{
		{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"},
		{ID: "tr-2", StartMs: 1000, EndMs: 1900, Text: "世界"},
	})
	response, err := env.svc.CreateWorkflowRun(ctx, Idem{}, cloneRunInput(project.ID, source.ID, true))
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := response.Body.(domain.WorkflowRun)
	if _, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, translation.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: translation.CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"},
			{ID: "tr-2", StartMs: 1000, EndMs: 2500, Text: "世界"},
		},
	}); err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	commitCloneTask(t, env, cloneSegmentTask(t, env, run.ID, "zh", "tr-1").ID)
	final, err := env.store.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if final.Status.Terminal() {
		t.Fatalf("setup run converged: %s", final.Status)
	}
	tasks, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	for _, task := range tasks {
		if task.Kind == domain.TaskKindNode && task.NodeKey == domain.NodeKeyClone &&
			task.Status == domain.TaskStatusFailed && task.Error != nil {
			return env, final, task
		}
	}
	t.Fatal("setup created no readable clone failure task")
	return nil, domain.WorkflowRun{}, domain.Task{}
}

func assertRetryRejectedWithoutMutation(t *testing.T, env *exportTestEnv, task domain.Task, want error) {
	t.Helper()
	ctx := context.Background()
	attemptsBefore, err := env.store.ListAttempts(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListAttempts before: %v", err)
	}
	eventsBefore, err := env.store.EventsAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("EventsAfter before: %v", err)
	}
	runBefore, err := env.store.GetWorkflowRun(ctx, task.RunID)
	if err != nil {
		t.Fatalf("GetWorkflowRun before: %v", err)
	}

	_, err = env.svc.RetryTask(ctx, Idem{}, task.ID)
	var expected, actual *domain.Error
	if !errors.As(want, &expected) || !errors.As(err, &actual) ||
		actual.Code != domain.ErrorCodeRevisionConflict || actual.MessageKey != expected.MessageKey {
		t.Fatalf("RetryTask error = %v, want REVISION_CONFLICT with key from %v", err, want)
	}

	after, err := env.store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask after: %v", err)
	}
	if after.Status != task.Status || after.StateVersion != task.StateVersion {
		t.Fatalf("rejected retry mutated task: %s/%d -> %s/%d",
			task.Status, task.StateVersion, after.Status, after.StateVersion)
	}
	if after.Error == nil || after.Error.Code != task.Error.Code || after.Error.MessageKey != task.Error.MessageKey {
		t.Fatalf("rejected retry lost error: %+v -> %+v", task.Error, after.Error)
	}
	attemptsAfter, err := env.store.ListAttempts(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListAttempts after: %v", err)
	}
	if len(attemptsAfter) != len(attemptsBefore) {
		t.Fatalf("rejected retry created attempts: %d -> %d", len(attemptsBefore), len(attemptsAfter))
	}
	eventsAfter, err := env.store.EventsAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("EventsAfter after: %v", err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("rejected retry emitted events: %d -> %d", len(eventsBefore), len(eventsAfter))
	}
	runAfter, err := env.store.GetWorkflowRun(ctx, task.RunID)
	if err != nil {
		t.Fatalf("GetWorkflowRun after: %v", err)
	}
	if runAfter.Status != runBefore.Status || runAfter.StateVersion != runBefore.StateVersion {
		t.Fatalf("rejected retry mutated run: %s/%d -> %s/%d",
			runBefore.Status, runBefore.StateVersion, runAfter.Status, runAfter.StateVersion)
	}
}

// 冻结克隆失败不能在非终态 Run 中原地重试：拒绝且不改任务/attempt/事件/运行。
func TestRetryCloneTimelineFailureRequiresNewRun(t *testing.T) {
	env, _, failure := seedCloneTimelineFailureRun(t)
	assertRetryRejectedWithoutMutation(t, env, failure, domain.ErrCloneTimelineOutOfRange)
	if failure.Error.MessageKey != "task.clone_timeline_out_of_range" {
		t.Fatalf("setup failure key = %q", failure.Error.MessageKey)
	}
}

// 真实冻结 segment 失败（运行期记录的安全错误）同样拒绝原地重试。
func TestRetryFrozenSegmentTimelineFailureRequiresNewRun(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		wantErr error
	}{
		{name: "out of range", key: "task.clone_timeline_out_of_range", wantErr: domain.ErrCloneTimelineOutOfRange},
		{name: "unknown duration", key: "task.clone_timeline_unknown_duration", wantErr: domain.ErrCloneTimelineUnknownDuration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			env := newCloneTimelineEnv(t)
			project := env.seedProject("segment retry")
			source := env.seedMediaAsset(project.ID, "voice.wav", domain.AssetTypeAudio,
				env.tempFile("voice.wav", "voice-source"),
				&domain.MediaMeta{Container: "wav", DurationMs: 2000, AudioTracks: 1})
			seedServiceTrack(t, env.store, project.ID, "en", domain.TrackTypeTranscription,
				[]domain.Segment{{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"}})
			seedServiceTrack(t, env.store, project.ID, "zh", domain.TrackTypeTranslation,
				[]domain.Segment{{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"}})
			response, err := env.svc.CreateWorkflowRun(ctx, Idem{}, cloneRunInput(project.ID, source.ID, true))
			if err != nil {
				t.Fatalf("CreateWorkflowRun: %v", err)
			}
			run := response.Body.(domain.WorkflowRun)
			task := cloneSegmentTask(t, env, run.ID, "zh", "tr-1")
			now := env.svc.Now()
			task, err = env.store.UpdateTaskStatus(ctx, task.ID, domain.TaskStatusRunning, task.StateVersion, now)
			if err != nil {
				t.Fatalf("UpdateTaskStatus: %v", err)
			}
			if err := env.store.CreateAttempt(ctx, domain.Attempt{
				ID: store.NewID("attempt"), TaskID: task.ID, StepID: "main",
				AttemptNo: 1, Epoch: 3, Status: domain.StepStatusRunning, StartedAt: &now,
			}); err != nil {
				t.Fatalf("CreateAttempt: %v", err)
			}
			task, err = env.store.UpdateTaskResult(ctx, task.ID, domain.TaskStatusFailed, nil,
				&domain.ErrorInfo{
					Code: domain.ErrorCodeInvalidRequest, MessageKey: tc.key,
					Retryable: false, ActionIDs: []domain.ID{},
				}, task.StateVersion, now)
			if err != nil {
				t.Fatalf("UpdateTaskResult: %v", err)
			}
			assertRetryRejectedWithoutMutation(t, env, task, tc.wantErr)
		})
	}
}
