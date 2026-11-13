package service

import (
	"context"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func seedFrozenCloneRun(t *testing.T) (*exportTestEnv, domain.WorkflowRun, domain.SubtitleTrack) {
	t.Helper()
	env := newExportTestEnv(t)
	env.svc.opts.PipelineOrchestration = true
	env.svc.opts.Capabilities = StaticCapabilities{ExecutionFixtures: testExecutionFixtures()}
	project := env.seedProject("sentence freezing")
	source := env.seedMediaAsset(project.ID, "reference.wav", domain.AssetTypeAudio,
		env.tempFile("reference.wav", "reference-audio"), &domain.MediaMeta{AudioTracks: 1})
	seedServiceTrack(t, env.store, project.ID, "en", domain.TrackTypeTranscription, []domain.Segment{
		{ID: "source-1", StartMs: 0, EndMs: 900, Text: "Hello"},
		{ID: "source-2", StartMs: 1000, EndMs: 1900, Text: "World"},
	})
	track := env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "sentence-1", StartMs: 0, EndMs: 900, Text: "你好"},
		{ID: "sentence-2", StartMs: 1000, EndMs: 1900, Text: "世界"},
	})
	response, err := env.svc.CreateWorkflowRun(context.Background(), Idem{}, CreateWorkflowRunInput{
		ProjectID: project.ID, Name: "frozen clone", Template: templateVoiceClone,
		SourceLanguage: "en", TargetLanguages: []string{"zh"}, InputRevisionIDs: []string{source.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	return env, response.Body.(domain.WorkflowRun), track
}

func TestAdvancePipelineOnlyCreatesChangedSentence(t *testing.T) {
	env, run, track := seedFrozenCloneRun(t)
	ctx := context.Background()
	before, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 2 {
		t.Fatalf("initial tasks = %d", len(before))
	}
	response, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, track.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: track.CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "sentence-1", StartMs: 0, EndMs: 900, Text: "修改的第一句"},
			{ID: "sentence-2", StartMs: 1000, EndMs: 1900, Text: "世界"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	revision := response.Body.(domain.SubtitleRevision)
	if revision.ID == track.CurrentRevisionID {
		t.Fatal("revision did not change")
	}
	var events []domain.DomainEvent
	if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
		if err := env.svc.advancePipeline(ctx, tx, run.ID, &events); err != nil {
			return err
		}
		return env.svc.advancePipeline(ctx, tx, run.ID, &events)
	}); err != nil {
		t.Fatal(err)
	}
	after, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, task := range after {
		counts[task.SegmentID]++
		if task.ModelLock == nil || task.Params["language"] != "zh" || taskSegmentCacheKey(task) == "" {
			t.Fatalf("unfrozen task = %+v", task)
		}
	}
	if len(after) != 3 || counts["sentence-1"] != 2 || counts["sentence-2"] != 1 {
		t.Fatalf("sentence edit rebuilt unrelated work: tasks=%d counts=%v", len(after), counts)
	}
}

func TestSegmentWorkProjectsCloneTextAndReferenceWindow(t *testing.T) {
	env, run, track := seedFrozenCloneRun(t)
	ctx := context.Background()
	if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
		snap, err := env.svc.loadPipelineSnapshot(ctx, tx, run.ID)
		if err != nil {
			return err
		}
		work, err := env.svc.segmentWorkForRevision(ctx, tx, snap, *currentRevision(track), "zh")
		if err != nil {
			return err
		}
		first := work["sentence-1"].Params
		if first["text"] != "你好" || first["original_text"] != "Hello" {
			t.Fatalf("sentence-1 text projection = %+v", first)
		}
		if first["start_ms"] != domain.Millis(0) || first["end_ms"] != domain.Millis(900) {
			t.Fatalf("sentence-1 reference window = %+v", first)
		}
		if first["out"] != "candidate.wav" {
			t.Fatalf("sentence-1 out = %v", first["out"])
		}
		second := work["sentence-2"].Params
		if second["text"] != "世界" || second["original_text"] != "World" {
			t.Fatalf("sentence-2 text projection = %+v", second)
		}
		if second["start_ms"] != domain.Millis(1000) || second["end_ms"] != domain.Millis(1900) {
			t.Fatalf("sentence-2 reference window = %+v", second)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSegmentWorkIncludesSourceModelTimeParametersAndContext(t *testing.T) {
	env, run, track := seedFrozenCloneRun(t)
	ctx := context.Background()
	other := env.seedMediaAsset(run.ProjectID, "another.wav", domain.AssetTypeAudio,
		env.tempFile("another.wav", "another-reference"), &domain.MediaMeta{AudioTracks: 1})
	cases := []struct {
		name   string
		mutate func(*domain.WorkflowRun, *domain.SubtitleRevision)
	}{
		{name: "reference source", mutate: func(run *domain.WorkflowRun, _ *domain.SubtitleRevision) {
			run.Config.InputRevisionIDs = []string{other.ID}
		}},
		{name: "model release", mutate: func(run *domain.WorkflowRun, _ *domain.SubtitleRevision) {
			cfg := run.Config.Execution[domain.CapabilitySlotVoiceClone]
			cfg.ModelLock.Digest = "new-model-digest"
			run.Config.Execution[domain.CapabilitySlotVoiceClone] = cfg
		}},
		{name: "duration parameters", mutate: func(run *domain.WorkflowRun, _ *domain.SubtitleRevision) { run.Config.AdjustDuration = true }},
		{name: "time window", mutate: func(_ *domain.WorkflowRun, revision *domain.SubtitleRevision) { revision.Segments[0].EndMs = 800 }},
		{name: "declared neighbor context", mutate: func(run *domain.WorkflowRun, revision *domain.SubtitleRevision) {
			cfg := run.Config.Execution[domain.CapabilitySlotVoiceClone]
			cfg.ContextRadius = 1
			run.Config.Execution[domain.CapabilitySlotVoiceClone] = cfg
			revision.Segments[1].ContentDigest = "changed-neighbor"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
				snap, err := env.svc.loadPipelineSnapshot(ctx, tx, run.ID)
				if err != nil {
					return err
				}
				revision := *currentRevision(track)
				before, err := env.svc.segmentWorkForRevision(ctx, tx, snap, revision, "zh")
				if err != nil {
					return err
				}
				revision.Segments = append([]domain.Segment{}, revision.Segments...)
				tc.mutate(snap.run, &revision)
				after, err := env.svc.segmentWorkForRevision(ctx, tx, snap, revision, "zh")
				if err != nil {
					return err
				}
				if before["sentence-1"].CacheKey == after["sentence-1"].CacheKey {
					t.Fatal("frozen dependency change reused prior sentence")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGenerateCandidatesMatchesAutomaticFrozenWorkAndRetryDigest(t *testing.T) {
	env, run, _ := seedFrozenCloneRun(t)
	ctx := context.Background()
	before, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var automatic domain.Task
	for _, task := range before {
		if task.SegmentID == "sentence-1" {
			automatic = task
		}
	}
	response, err := env.svc.GenerateCandidates(ctx, Idem{}, "sentence-1", CreateCandidatesRequest{Language: "zh"})
	if err != nil {
		t.Fatal(err)
	}
	job := response.Body.(SegmentCandidateJob)
	generated, err := env.store.GetTask(ctx, job.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if generated.ID == automatic.ID || generated.Params["language"] != "zh" || generated.ModelLock == nil {
		t.Fatalf("generated task = %+v", generated)
	}
	if taskSegmentCacheKey(generated) != taskSegmentCacheKey(automatic) || generated.InputDigest != automatic.InputDigest {
		t.Fatalf("manual and automatic work diverged: %s/%s vs %s/%s", generated.InputDigest, taskSegmentCacheKey(generated), automatic.InputDigest, taskSegmentCacheKey(automatic))
	}
	if err := verifyTaskInputDigest(generated); err != nil {
		t.Fatal(err)
	}
}

func verifyTaskInputDigest(task domain.Task) error {
	digest, err := taskInputDigest(CreateTaskRequest{
		RunID: task.RunID, Kind: task.Kind, NodeKey: task.NodeKey, SegmentID: task.SegmentID,
		Language: task.Language, InputRevisionIDs: task.InputRevisionIDs, Params: task.Params, ModelLock: task.ModelLock,
	})
	if err != nil {
		return err
	}
	if digest != task.InputDigest {
		return domain.ErrRevisionConflict
	}
	return nil
}

func TestFreezeSubtitleOutputRecordsAbsentAndExistingTargets(t *testing.T) {
	cases := []struct {
		name             string
		tracks           []domain.SubtitleTrack
		wantID, wantBase string
	}{
		{name: "absent output"},
		{name: "current output", tracks: []domain.SubtitleTrack{{ID: "translation", Language: "zh", Type: domain.TrackTypeTranslation, CurrentRevisionID: "before-start"}}, wantID: "translation", wantBase: "before-start"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{}
			freezeSubtitleOutput(&pipelineSnapshot{tracks: tc.tracks}, domain.NodeKeyTranslate, "zh", params)
			core := coreTaskParams(params)
			if core["output_snapshot"] != true || core["output_base_revision_id"] != tc.wantBase {
				t.Fatalf("output snapshot = %+v", core)
			}
			id, _ := core["output_track_id"].(string)
			if id == "" || tc.wantID != "" && id != tc.wantID {
				t.Fatalf("output target = %q", id)
			}
		})
	}
}
