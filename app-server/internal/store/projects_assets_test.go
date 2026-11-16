package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func TestMigrationCreatesBusinessTables(t *testing.T) {
	s := newTestStore(t)
	tables := []string{
		"projects", "assets", "asset_locations", "subtitle_tracks", "subtitle_revisions",
		"segments", "workflow_runs", "run_nodes", "tasks", "attempts", "artifact_commits",
		"review_gates", "audio_candidates", "candidate_selections", "installations",
		"events", "receipts",
	}
	for _, name := range tables {
		var count int
		if err := s.DB().QueryRowContext(context.Background(),
			`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count); err != nil {
			t.Fatalf("query sqlite_master for %s: %v", name, err)
		}
		if count != 1 {
			t.Errorf("table %s missing after migration", name)
		}
	}
}

func TestProjectCRUDAndSummary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedAsset(t, s, project.ID, "asset-1", domain.AssetTypeVideo)
	seedRun(t, s, project.ID, "run-1")
	seedProject(t, s, "project-2")

	got, err := s.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}
	if got.Name != project.Name || got.WorkspaceID != "ws-1" || got.EditVersion != 1 {
		t.Errorf("GetProject() = %+v", got)
	}
	if !got.CreatedAt.Equal(testTime) || got.CreatedAt.Location() != time.UTC {
		t.Errorf("created_at = %v, want UTC %v", got.CreatedAt, testTime)
	}
	if len(got.Assets) != 1 || got.Assets[0].ID != "asset-1" {
		t.Errorf("project assets = %+v", got.Assets)
	}
	if len(got.Runs) != 1 || got.Runs[0].ID != "run-1" || len(got.Runs[0].Nodes) != 8 {
		t.Errorf("project runs = %+v", got.Runs)
	}

	summaries, err := s.ListProjectSummaries(ctx, "ws-1")
	if err != nil {
		t.Fatalf("ListProjectSummaries() error = %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries count = %d, want 2", len(summaries))
	}
	var found bool
	for _, summary := range summaries {
		if summary.ID == project.ID {
			found = true
			if summary.AssetCount != 1 || summary.RunCount != 1 {
				t.Errorf("summary counts = %d/%d, want 1/1", summary.AssetCount, summary.RunCount)
			}
			if summary.LatestRunStatus != domain.RunStatusDraft {
				t.Errorf("latest run status = %s, want DRAFT", summary.LatestRunStatus)
			}
			if summary.TotalDurationMs != 3000 {
				t.Errorf("summary total duration = %d, want 3000", summary.TotalDurationMs)
			}
		}
	}
	if !found {
		t.Error("project summary not found")
	}

	touched := testTime.Add(1 * time.Hour)
	if err := s.TouchProjectOpened(ctx, project.ID, touched); err != nil {
		t.Fatalf("TouchProjectOpened() error = %v", err)
	}
	got, err = s.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}
	if got.LastOpenedAt == nil || !got.LastOpenedAt.Equal(touched) {
		t.Errorf("last_opened_at = %v, want %v", got.LastOpenedAt, touched)
	}

	if err := s.DeleteProject(ctx, project.ID); err != nil {
		t.Fatalf("DeleteProject() error = %v", err)
	}
	if _, err := s.GetProject(ctx, project.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetProject(deleted) error = %v, want ErrNotFound", err)
	}
	if err := s.DeleteProject(ctx, project.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("DeleteProject(missing) error = %v, want ErrNotFound", err)
	}
}

func TestProjectEditVersionConflict(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")

	next, err := s.BumpProjectEditVersion(ctx, project.ID, 1, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("BumpProjectEditVersion() error = %v", err)
	}
	if next != 2 {
		t.Errorf("new edit version = %d, want 2", next)
	}
	if _, err := s.BumpProjectEditVersion(ctx, project.ID, 1, testTime); !errors.Is(err, domain.ErrStateConflict) {
		t.Errorf("stale bump error = %v, want ErrStateConflict", err)
	}
	if _, err := s.BumpProjectEditVersion(ctx, "missing", 1, testTime); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing project bump error = %v, want ErrNotFound", err)
	}
}

func TestAssetLocationsAndStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedAsset(t, s, project.ID, "asset-1", domain.AssetTypeAudio)

	locations := []domain.AssetLocation{
		{
			ID: "loc-1", AssetID: "asset-1", Path: "/media/source.wav",
			Source: domain.LocationSourceExternalReference, Owner: domain.LocationOwnerUser,
			Available: true, CreatedAt: testTime,
		},
		{
			ID: "loc-2", AssetID: "asset-1", Path: "/app/data/copy.wav",
			Source: domain.LocationSourceManagedCopy, Owner: domain.LocationOwnerApp,
			Available: true, CreatedAt: testTime.Add(time.Hour),
		},
	}
	for _, loc := range locations {
		if err := s.AddAssetLocation(ctx, loc); err != nil {
			t.Fatalf("AddAssetLocation() error = %v", err)
		}
	}

	asset, err := s.GetAsset(ctx, "asset-1")
	if err != nil {
		t.Fatalf("GetAsset() error = %v", err)
	}
	if asset.Path != "/media/source.wav" {
		t.Errorf("asset path = %q, want primary location", asset.Path)
	}
	if asset.Meta == nil || asset.Meta.DurationMs != 3000 || asset.Meta.SampleRate != 48000 {
		t.Errorf("asset meta = %+v", asset.Meta)
	}

	stored, err := s.ListAssetLocations(ctx, "asset-1")
	if err != nil {
		t.Fatalf("ListAssetLocations() error = %v", err)
	}
	if len(stored) != 2 || stored[0].ID != "loc-1" || !stored[0].Available {
		t.Errorf("locations = %+v", stored)
	}

	if err := s.SetAssetLocationAvailable(ctx, "loc-1", false); err != nil {
		t.Fatalf("SetAssetLocationAvailable() error = %v", err)
	}
	stored, err = s.ListAssetLocations(ctx, "asset-1")
	if err != nil {
		t.Fatalf("ListAssetLocations() error = %v", err)
	}
	if stored[0].Available {
		t.Error("location availability not updated")
	}
	if err := s.SetAssetStatus(ctx, "asset-1", domain.AssetStatusMissing); err != nil {
		t.Fatalf("SetAssetStatus() error = %v", err)
	}
	asset, err = s.GetAsset(ctx, "asset-1")
	if err != nil {
		t.Fatalf("GetAsset() error = %v", err)
	}
	if asset.Status != domain.AssetStatusMissing {
		t.Errorf("asset status = %s, want missing", asset.Status)
	}
	if err := s.SetAssetStatus(ctx, "asset-1", "bogus"); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Errorf("invalid status error = %v, want ErrInvalidRequest", err)
	}
}

func TestDeleteProjectCascades(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedAsset(t, s, project.ID, "asset-1", domain.AssetTypeAudio)
	seedRun(t, s, project.ID, "run-1")
	track := domain.SubtitleTrack{
		ID: "track-1", ProjectID: project.ID, Language: "ja", Type: domain.TrackTypeTranscription,
		CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := s.CreateSubtitleTrack(ctx, track); err != nil {
		t.Fatalf("CreateSubtitleTrack() error = %v", err)
	}
	task := domain.Task{
		ID: "task-1", ProjectID: project.ID, RunID: "run-1", Kind: domain.TaskKindNode,
		Status: domain.TaskStatusQueued, CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}

	if err := s.DeleteProject(ctx, project.ID); err != nil {
		t.Fatalf("DeleteProject() error = %v", err)
	}
	for _, table := range []string{"assets", "asset_locations", "subtitle_tracks", "workflow_runs", "run_nodes", "tasks"} {
		var count int
		if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("table %s has %d rows after project delete, want 0", table, count)
		}
	}
}

func TestProjectSequenceAllocatedInCreationOrderAndNotReused(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first := seedProject(t, s, "project-seq-1")
	second := seedProject(t, s, "project-seq-2")

	summaries, err := s.ListProjectSummaries(ctx, "ws-1")
	if err != nil {
		t.Fatalf("ListProjectSummaries() error = %v", err)
	}
	byID := map[string]int64{}
	for _, summary := range summaries {
		byID[summary.ID] = summary.Seq
	}
	if byID[first.ID] != 1 || byID[second.ID] != 2 {
		t.Fatalf("seq = %d/%d, want 1/2", byID[first.ID], byID[second.ID])
	}

	if err := s.DeleteProject(ctx, first.ID); err != nil {
		t.Fatalf("DeleteProject() error = %v", err)
	}
	third := seedProject(t, s, "project-seq-3")
	summary, err := s.GetProjectSummary(ctx, third.ID)
	if err != nil {
		t.Fatalf("GetProjectSummary() error = %v", err)
	}
	if summary.Seq != 3 {
		t.Fatalf("reused seq = %d, want 3", summary.Seq)
	}
}
