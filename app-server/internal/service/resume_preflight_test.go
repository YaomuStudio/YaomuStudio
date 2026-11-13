package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// TestResumePreflightUsesFrozenTranslationTracks 守护恢复预检：
// 新 custom Run 创建时冻结的是空译稿 map；之后项目内出现 translation 轨，
// 也不能让 resume 误判“无需 06/translate 槽位”。
func TestResumePreflightUsesFrozenTranslationTracks(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "yaomu.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logger, err := logging.New(logging.Options{Path: filepath.Join(t.TempDir(), "core.log")})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	caps := &StaticCapabilities{States: map[domain.CapabilitySlotKey]domain.SlotState{}}
	svc := New(Options{
		Store: st, Log: logger, WorkspaceID: "ws-resume", ServerEpoch: 1,
		DataDir: dataDir, Capabilities: caps, Devices: SystemDevices{},
	})
	if err := svc.EnsureStorageDirs(); err != nil {
		t.Fatalf("storage dirs: %v", err)
	}

	projectResp, err := svc.CreateProject(ctx, Idem{}, CreateProjectRequest{Name: "恢复预检"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	project := projectResp.Body.(domain.Project)
	now := time.Now().UTC()
	asset := domain.Asset{
		ID: store.NewID("asset"), ProjectID: project.ID, Type: domain.AssetTypeAudio,
		Name: "voice.wav", SizeBytes: 16, Status: domain.AssetStatusReady, ImportedAt: now,
	}
	if err := st.CreateAsset(ctx, asset); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	source := seedServiceTrack(t, st, project.ID, "en", domain.TrackTypeTranscription, []domain.Segment{
		{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"},
	})

	runResp, err := svc.CreateWorkflowRun(ctx, Idem{}, CreateWorkflowRunInput{
		ProjectID:            string(project.ID),
		Name:                 "恢复预检 Run",
		Template:             "custom_pipeline",
		Features:             []string{featureClone},
		SourceLanguage:       "en",
		TargetLanguages:      []string{"zh"},
		InputRevisionIDs:     []string{string(asset.ID)},
		ReuseSubtitleTrackID: string(source.ID),
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := runResp.Body.(domain.WorkflowRun)
	if run.Config.ImportedTranslationTracks == nil || len(run.Config.ImportedTranslationTracks) != 0 {
		t.Fatalf("frozen translation tracks = %#v, want non-nil empty", run.Config.ImportedTranslationTracks)
	}
	if _, err := svc.PauseWorkflowRun(ctx, Idem{}, string(run.ID)); err != nil {
		t.Fatalf("PauseWorkflowRun: %v", err)
	}

	// 创建后项目内出现译稿；恢复预检不应把它当成 Run 的冻结输入。
	seedServiceTrack(t, st, project.ID, "zh", domain.TrackTypeTranslation, []domain.Segment{
		{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"},
	})
	caps.States[domain.CapabilitySlotTranslate] = domain.SlotStateNeedsSetup

	_, err = svc.ResumeWorkflowRun(ctx, Idem{}, string(run.ID))
	if !errors.Is(err, domain.ErrPreflightStale) {
		t.Fatalf("ResumeWorkflowRun error = %v, want PREFLIGHT_STALE", err)
	}
	reloaded, err := st.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if reloaded.Status != domain.RunStatusPaused {
		t.Fatalf("run status = %s, want PAUSED after blocked resume", reloaded.Status)
	}
}

func seedServiceTrack(
	t *testing.T,
	st *store.Store,
	projectID domain.ID,
	language string,
	trackType domain.TrackType,
	segments []domain.Segment,
) domain.SubtitleTrack {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	revisionID := store.NewID("srev")
	track := domain.SubtitleTrack{
		ID: store.NewID("track"), ProjectID: projectID, Language: language, Type: trackType,
		CurrentRevisionID: revisionID, CreatedAt: now, UpdatedAt: now,
		Revisions: []domain.SubtitleRevision{{
			ID: revisionID, AuthorType: domain.AuthorTypeSystem, Segments: segments, CreatedAt: now,
		}},
	}
	if err := st.CreateSubtitleTrack(ctx, track); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	built, err := st.GetSubtitleTrack(ctx, track.ID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	return built
}
