package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func testBool(value bool) *bool { return &value }

func newCloneTimelineEnv(t *testing.T) *exportTestEnv {
	t.Helper()
	env := newExportTestEnv(t)
	env.svc.opts.PipelineOrchestration = true
	env.svc.opts.Capabilities = StaticCapabilities{ExecutionFixtures: testExecutionFixtures()}
	return env
}

func seedCloneTimelineTracks(
	t *testing.T,
	env *exportTestEnv,
	projectID domain.ID,
	meta *domain.MediaMeta,
	sourceSegments, translationSegments []domain.Segment,
) (domain.Asset, domain.SubtitleTrack) {
	t.Helper()
	source := env.seedMediaAsset(projectID, "voice.wav", domain.AssetTypeAudio,
		env.tempFile("voice.wav", "voice-source"), meta)
	seedServiceTrack(t, env.store, projectID, "en", domain.TrackTypeTranscription, sourceSegments)
	translation := seedServiceTrack(t, env.store, projectID, "zh", domain.TrackTypeTranslation, translationSegments)
	return source, translation
}

func cloneRunInput(projectID domain.ID, sourceID domain.ID, validate bool) CreateWorkflowRunInput {
	return CreateWorkflowRunInput{
		ProjectID:          projectID,
		Name:               "clone timeline",
		Template:           templateVoiceClone,
		SourceLanguage:     "en",
		TargetLanguages:    []string{"zh"},
		InputRevisionIDs:   []string{sourceID},
		ValidateTimestamps: testBool(validate),
	}
}

func hasPreflightIssue(result domain.PreflightResult, code string) bool {
	for _, issue := range result.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func TestCloneTimelinePreflightAndCreateShareMediaRangeRule(t *testing.T) {
	validMeta := &domain.MediaMeta{Container: "wav", DurationMs: 2000, AudioTracks: 1}
	validSource := []domain.Segment{{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"}}
	cases := []struct {
		name          string
		meta          *domain.MediaMeta
		source        []domain.Segment
		translation   []domain.Segment
		validate      bool
		wantIssueCode string
		wantCreateErr bool
	}{
		{
			name: "boundary end equals duration", meta: validMeta,
			source:      []domain.Segment{{ID: "src-1", StartMs: 1000, EndMs: 2000, Text: "Hello"}},
			translation: []domain.Segment{{ID: "tr-1", StartMs: 1000, EndMs: 2000, Text: "你好"}},
			validate:    true,
		},
		{
			name: "target beyond duration", meta: validMeta, source: validSource,
			translation:   []domain.Segment{{ID: "tr-1", StartMs: 1000, EndMs: 2001, Text: "你好"}},
			validate:      true,
			wantIssueCode: "CLONE_TIMELINE_OUT_OF_RANGE", wantCreateErr: true,
		},
		{
			name: "reference beyond duration", meta: validMeta,
			source:        []domain.Segment{{ID: "src-1", StartMs: 1000, EndMs: 2200, Text: "Hello"}},
			translation:   []domain.Segment{{ID: "tr-1", StartMs: 1000, EndMs: 2000, Text: "你好"}},
			validate:      true,
			wantIssueCode: "CLONE_TIMELINE_OUT_OF_RANGE", wantCreateErr: true,
		},
		{
			name: "unknown media duration", meta: nil, source: validSource,
			translation:   []domain.Segment{{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"}},
			validate:      true,
			wantIssueCode: "CLONE_TIMELINE_UNKNOWN_DURATION", wantCreateErr: true,
		},
		{
			name: "non-positive media duration", meta: &domain.MediaMeta{Container: "wav", DurationMs: 0, AudioTracks: 1},
			source:        validSource,
			translation:   []domain.Segment{{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"}},
			validate:      true,
			wantIssueCode: "CLONE_TIMELINE_UNKNOWN_DURATION", wantCreateErr: true,
		},
		{
			name: "switch off keeps existing behavior", meta: validMeta,
			source:      []domain.Segment{{ID: "src-1", StartMs: 1000, EndMs: 2000, Text: "Hello"}},
			translation: []domain.Segment{{ID: "tr-1", StartMs: 1000, EndMs: 2500, Text: "你好"}},
			validate:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			env := newCloneTimelineEnv(t)
			project := env.seedProject("clone timeline")
			source, _ := seedCloneTimelineTracks(t, env, project.ID, tc.meta, tc.source, tc.translation)
			in := cloneRunInput(project.ID, source.ID, tc.validate)

			result, err := env.svc.Preflight(ctx, in)
			if err != nil {
				t.Fatalf("Preflight: %v", err)
			}
			if tc.wantIssueCode == "" {
				for _, issue := range result.Issues {
					if strings.HasPrefix(issue.Code, "CLONE_TIMELINE_") {
						t.Fatalf("unexpected timeline issue: %+v", issue)
					}
				}
			} else if !hasPreflightIssue(result, tc.wantIssueCode) {
				t.Fatalf("preflight issues = %+v, want %s", result.Issues, tc.wantIssueCode)
			}

			_, err = env.svc.CreateWorkflowRun(ctx, Idem{}, in)
			if tc.wantCreateErr {
				if !errors.Is(err, domain.ErrPreflightStale) {
					t.Fatalf("CreateWorkflowRun error = %v, want PREFLIGHT_STALE", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateWorkflowRun: %v", err)
			}
		})
	}
}

func TestCloneTimelinePreflightUsesReusedAudioSource(t *testing.T) {
	ctx := context.Background()
	env := newCloneTimelineEnv(t)
	project := env.seedProject("clone reuse source")
	source, _ := seedCloneTimelineTracks(t, env, project.ID,
		&domain.MediaMeta{Container: "wav", DurationMs: 2000, AudioTracks: 1},
		[]domain.Segment{{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"}},
		[]domain.Segment{{ID: "tr-1", StartMs: 1000, EndMs: 1500, Text: "你好"}})
	reuse := env.seedMediaAsset(project.ID, "reuse.wav", domain.AssetTypeAudioTrack,
		env.tempFile("reuse.wav", "reuse-source"),
		&domain.MediaMeta{Container: "wav", DurationMs: 1000, AudioTracks: 1})
	in := cloneRunInput(project.ID, source.ID, true)
	in.ReuseAudioTrackID = reuse.ID

	result, err := env.svc.Preflight(ctx, in)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !hasPreflightIssue(result, "CLONE_TIMELINE_OUT_OF_RANGE") {
		t.Fatalf("preflight did not validate the reused audio source: %+v", result.Issues)
	}
}

func runCloneSegmentWork(
	t *testing.T,
	env *exportTestEnv,
	run domain.WorkflowRun,
	track domain.SubtitleTrack,
	validate bool,
) error {
	t.Helper()
	ctx := context.Background()
	return env.store.DoTx(ctx, func(tx *store.Tx) error {
		snap, err := env.svc.loadPipelineSnapshot(ctx, tx, run.ID)
		if err != nil {
			return err
		}
		snap.run.Config.ValidateTimestamps = validate
		_, err = env.svc.segmentWorkForRevision(ctx, tx, snap, *currentRevision(track), "zh")
		return err
	})
}

func seedCloneWorkRun(
	t *testing.T,
	meta *domain.MediaMeta,
	sourceSegments, translationSegments []domain.Segment,
) (*exportTestEnv, domain.WorkflowRun, domain.SubtitleTrack) {
	t.Helper()
	ctx := context.Background()
	env := newCloneTimelineEnv(t)
	project := env.seedProject("clone work")
	source, translation := seedCloneTimelineTracks(t, env, project.ID, meta, sourceSegments, translationSegments)
	response, err := env.svc.CreateWorkflowRun(ctx, Idem{}, cloneRunInput(project.ID, source.ID, false))
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	return env, response.Body.(domain.WorkflowRun), translation
}

func TestSegmentWorkValidatesFrozenCloneTimeline(t *testing.T) {
	validMeta := &domain.MediaMeta{Container: "wav", DurationMs: 2000, AudioTracks: 1}
	t.Run("boundary end equals duration", func(t *testing.T) {
		env, run, track := seedCloneWorkRun(t, validMeta,
			[]domain.Segment{{ID: "src-1", StartMs: 1000, EndMs: 2000, Text: "Hello"}},
			[]domain.Segment{{ID: "tr-1", StartMs: 1000, EndMs: 2000, Text: "你好"}})
		if err := runCloneSegmentWork(t, env, run, track, true); err != nil {
			t.Fatalf("boundary window rejected: %v", err)
		}
	})
	t.Run("target beyond duration", func(t *testing.T) {
		env, run, track := seedCloneWorkRun(t, validMeta,
			[]domain.Segment{{ID: "src-1", StartMs: 1000, EndMs: 2000, Text: "Hello"}},
			[]domain.Segment{{ID: "tr-1", StartMs: 1000, EndMs: 2500, Text: "你好"}})
		if err := runCloneSegmentWork(t, env, run, track, true); !errors.Is(err, domain.ErrCloneTimelineOutOfRange) {
			t.Fatalf("out-of-range target error = %v", err)
		}
		if err := runCloneSegmentWork(t, env, run, track, false); err != nil {
			t.Fatalf("switch off should preserve old behavior: %v", err)
		}
	})
	t.Run("reference window beyond duration", func(t *testing.T) {
		env, run, track := seedCloneWorkRun(t, validMeta,
			[]domain.Segment{{ID: "src-1", StartMs: 1000, EndMs: 2200, Text: "Hello"}},
			[]domain.Segment{{ID: "tr-1", StartMs: 1000, EndMs: 2000, Text: "你好"}})
		if err := runCloneSegmentWork(t, env, run, track, true); !errors.Is(err, domain.ErrCloneTimelineOutOfRange) {
			t.Fatalf("reference out-of-range error = %v", err)
		}
		if err := runCloneSegmentWork(t, env, run, track, false); err != nil {
			t.Fatalf("switch off should preserve old behavior: %v", err)
		}
	})
	t.Run("unknown media duration", func(t *testing.T) {
		env, run, track := seedCloneWorkRun(t, nil,
			[]domain.Segment{{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"}},
			[]domain.Segment{{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"}})
		if err := runCloneSegmentWork(t, env, run, track, true); !errors.Is(err, domain.ErrCloneTimelineUnknownDuration) {
			t.Fatalf("unknown duration error = %v", err)
		}
		if err := runCloneSegmentWork(t, env, run, track, false); err != nil {
			t.Fatalf("switch off should not require duration: %v", err)
		}
	})
	t.Run("selected source wins over newer project media", func(t *testing.T) {
		env, run, track := seedCloneWorkRun(t, validMeta,
			[]domain.Segment{{ID: "src-1", StartMs: 1000, EndMs: 2000, Text: "Hello"}},
			[]domain.Segment{{ID: "tr-1", StartMs: 1000, EndMs: 2000, Text: "你好"}})
		env.seedMediaAsset(run.ProjectID, "newer.wav", domain.AssetTypeAudio,
			env.tempFile("newer.wav", "newer-source"),
			&domain.MediaMeta{Container: "wav", DurationMs: 500, AudioTracks: 1})
		if err := runCloneSegmentWork(t, env, run, track, true); err != nil {
			t.Fatalf("validation used newer media instead of the frozen source: %v", err)
		}
	})
}

func clonePreparedTask(
	t *testing.T,
	project domain.Project,
	source domain.Asset,
	revision domain.SubtitleRevision,
	segmentID string,
	validate bool,
	start, end float64,
) domain.Task {
	t.Helper()
	return domain.Task{
		ID:               store.NewID("task"),
		ProjectID:        project.ID,
		RunID:            store.NewID("run"),
		Kind:             domain.TaskKindSegment,
		NodeKey:          domain.NodeKeyClone,
		SegmentID:        segmentID,
		Language:         "zh",
		InputRevisionIDs: []domain.ID{revision.ID, source.ID},
		Params: map[string]any{
			"validate_timestamps":   validate,
			"voice_source_asset_id": source.ID,
			"start_ms":              start,
			"end_ms":                end,
		},
	}
}

func TestPrepareTaskExecutionRevalidatesFrozenCloneTimeline(t *testing.T) {
	ctx := context.Background()
	validMeta := &domain.MediaMeta{Container: "wav", DurationMs: 2000, AudioTracks: 1}
	seed := func(t *testing.T) (*exportTestEnv, domain.Project, domain.Asset, domain.SubtitleTrack) {
		t.Helper()
		env := newCloneTimelineEnv(t)
		project := env.seedProject("frozen clone prep")
		source, translation := seedCloneTimelineTracks(t, env, project.ID, validMeta,
			[]domain.Segment{{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"}},
			[]domain.Segment{{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"}})
		return env, project, source, translation
	}
	t.Run("frozen inputs pass", func(t *testing.T) {
		env, project, source, track := seed(t)
		task := clonePreparedTask(t, project, source, *currentRevision(track), "tr-1", true, 0, 900)
		if _, err := env.svc.PrepareTaskExecution(ctx, task); err != nil {
			t.Fatalf("PrepareTaskExecution: %v", err)
		}
	})
	t.Run("target outside frozen media", func(t *testing.T) {
		env, project, source, track := seed(t)
		response, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, track.ID, SaveSubtitleRevisionRequest{
			BaseRevisionID: track.CurrentRevisionID,
			Segments: []domain.SegmentInput{
				{ID: "tr-1", StartMs: 0, EndMs: 2500, Text: "你好"},
			},
		})
		if err != nil {
			t.Fatalf("SaveSubtitleRevision: %v", err)
		}
		revision := response.Body.(domain.SubtitleRevision)
		task := clonePreparedTask(t, project, source, revision, "tr-1", true, 0, 900)
		if _, err := env.svc.PrepareTaskExecution(ctx, task); !errors.Is(err, domain.ErrCloneTimelineOutOfRange) {
			t.Fatalf("target error = %v", err)
		}
	})
	t.Run("frozen reference window outside frozen media", func(t *testing.T) {
		env, project, source, track := seed(t)
		task := clonePreparedTask(t, project, source, *currentRevision(track), "tr-1", true, 1000, 2500)
		if _, err := env.svc.PrepareTaskExecution(ctx, task); !errors.Is(err, domain.ErrCloneTimelineOutOfRange) {
			t.Fatalf("reference error = %v", err)
		}
	})
	t.Run("unknown frozen media duration", func(t *testing.T) {
		env := newCloneTimelineEnv(t)
		project := env.seedProject("unknown duration")
		source, translation := seedCloneTimelineTracks(t, env, project.ID, nil,
			[]domain.Segment{{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"}},
			[]domain.Segment{{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"}})
		task := clonePreparedTask(t, project, source, *currentRevision(translation), "tr-1", true, 0, 900)
		if _, err := env.svc.PrepareTaskExecution(ctx, task); !errors.Is(err, domain.ErrCloneTimelineUnknownDuration) {
			t.Fatalf("unknown duration error = %v", err)
		}
	})
	t.Run("switch off keeps old tasks admissible", func(t *testing.T) {
		env, project, source, track := seed(t)
		response, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, track.ID, SaveSubtitleRevisionRequest{
			BaseRevisionID: track.CurrentRevisionID,
			Segments: []domain.SegmentInput{
				{ID: "tr-1", StartMs: 0, EndMs: 2500, Text: "你好"},
			},
		})
		if err != nil {
			t.Fatalf("SaveSubtitleRevision: %v", err)
		}
		revision := response.Body.(domain.SubtitleRevision)
		task := clonePreparedTask(t, project, source, revision, "tr-1", false, 0, 900)
		if _, err := env.svc.PrepareTaskExecution(ctx, task); err != nil {
			t.Fatalf("switch off should not add a media gate: %v", err)
		}
	})
	t.Run("exact frozen revision is used instead of latest track revision", func(t *testing.T) {
		env, project, source, track := seed(t)
		frozen := *currentRevision(track)
		response, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, track.ID, SaveSubtitleRevisionRequest{
			BaseRevisionID: track.CurrentRevisionID,
			Segments: []domain.SegmentInput{
				{ID: "tr-1", StartMs: 0, EndMs: 2500, Text: "你好"},
			},
		})
		if err != nil {
			t.Fatalf("SaveSubtitleRevision: %v", err)
		}
		latest := response.Body.(domain.SubtitleRevision)
		if latest.ID == frozen.ID {
			t.Fatal("latest revision did not change")
		}
		task := clonePreparedTask(t, project, source, frozen, "tr-1", true, 0, 900)
		if _, err := env.svc.PrepareTaskExecution(ctx, task); err != nil {
			t.Fatalf("frozen revision replaced by latest track revision: %v", err)
		}
		latestTask := clonePreparedTask(t, project, source, latest, "tr-1", true, 0, 900)
		if _, err := env.svc.PrepareTaskExecution(ctx, latestTask); !errors.Is(err, domain.ErrCloneTimelineOutOfRange) {
			t.Fatalf("latest out-of-range revision error = %v", err)
		}
	})
}

// 时间戳开关只增加媒体时长门槛，不放开字幕结构损坏：
// 保存入口始终拒绝负时间、end<=start 与重叠。
func TestSubtitleStructureValidationIsIndependentOfTimestampSwitch(t *testing.T) {
	ctx := context.Background()
	env := newCloneTimelineEnv(t)
	project := env.seedProject("structure")
	env.seedMediaAsset(project.ID, "voice.wav", domain.AssetTypeAudio,
		env.tempFile("voice.wav", "voice-source"),
		&domain.MediaMeta{Container: "wav", DurationMs: 2000, AudioTracks: 1})
	track := seedServiceTrack(t, env.store, project.ID, "zh", domain.TrackTypeTranslation,
		[]domain.Segment{{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"}})
	cases := []struct {
		name     string
		segments []domain.SegmentInput
	}{
		{name: "negative start", segments: []domain.SegmentInput{{ID: "tr-1", StartMs: -1, EndMs: 900, Text: "你好"}}},
		{name: "end before start", segments: []domain.SegmentInput{{ID: "tr-1", StartMs: 900, EndMs: 500, Text: "你好"}}},
		{name: "zero length", segments: []domain.SegmentInput{{ID: "tr-1", StartMs: 500, EndMs: 500, Text: "你好"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, track.ID, SaveSubtitleRevisionRequest{
				BaseRevisionID: track.CurrentRevisionID,
				Segments:       tc.segments,
			})
			if !errors.Is(err, domain.ErrInvalidRequest) {
				t.Fatalf("SaveSubtitleRevision error = %v, want INVALID_REQUEST", err)
			}
		})
	}
}

func TestPrepareTaskExecutionRejectsNonTranslationFrozenRevision(t *testing.T) {
	ctx := context.Background()
	env := newCloneTimelineEnv(t)
	project := env.seedProject("frozen revision guard")
	source := env.seedMediaAsset(project.ID, "voice.wav", domain.AssetTypeAudio,
		env.tempFile("voice.wav", "voice-source"),
		&domain.MediaMeta{Container: "wav", DurationMs: 2000, AudioTracks: 1})
	sourceTrack := seedServiceTrack(t, env.store, project.ID, "en", domain.TrackTypeTranscription,
		[]domain.Segment{{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"}})
	task := clonePreparedTask(t, project, source, *currentRevision(sourceTrack), "src-1", true, 0, 900)
	if _, err := env.svc.PrepareTaskExecution(ctx, task); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("non-translation frozen revision accepted: %v", err)
	}
}

func TestTaskMillisParamRejectsUnrepresentableValues(t *testing.T) {
	params := map[string]any{
		"int64":    int64(900),
		"ok":       float64(900),
		"fraction": 1.5,
		"nan":      math.NaN(),
		"inf":      math.Inf(1),
		"huge":     1e300,
		"tiny":     -1e300,
		"max":      float64(1 << 63),
	}
	cases := map[string]struct {
		want   domain.Millis
		wantOK bool
	}{
		"int64":    {900, true},
		"ok":       {900, true},
		"fraction": {}, "nan": {}, "inf": {}, "huge": {}, "tiny": {}, "max": {},
	}
	for key, want := range cases {
		got, ok := taskMillisParam(params, key)
		if ok != want.wantOK || (ok && got != want.want) {
			t.Fatalf("taskMillisParam(%q) = %d, %v; want %d, %v", key, got, ok, want.want, want.wantOK)
		}
	}
	if _, ok := taskMillisParam(params, "missing"); ok {
		t.Fatal("missing parameter accepted")
	}
}
