package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// testTime 是测试用固定 UTC 时间，便于断言时间往返与排序。
var testTime = time.Date(2026, 10, 29, 9, 30, 0, 0, time.UTC)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "yaomu.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return s
}

func seedProject(t *testing.T, s *Store, id string) domain.Project {
	t.Helper()
	p := domain.Project{
		ID:            id,
		WorkspaceID:   "ws-1",
		Name:          "测试项目",
		SchemaVersion: 1,
		EditVersion:   1,
		CreatedAt:     testTime,
		UpdatedAt:     testTime,
	}
	if err := s.CreateProject(context.Background(), p); err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	return p
}

func seedAsset(t *testing.T, s *Store, projectID, id string, typ domain.AssetType) domain.Asset {
	t.Helper()
	a := domain.Asset{
		ID:         id,
		ProjectID:  projectID,
		Type:       typ,
		Name:       id + ".wav",
		SizeBytes:  1024,
		SHA256:     "sha-" + id,
		Meta:       &domain.MediaMeta{Container: "wav", DurationMs: 3000, AudioTracks: 1, SampleRate: 48000},
		Status:     domain.AssetStatusReady,
		ImportedAt: testTime,
	}
	if err := s.CreateAsset(context.Background(), a); err != nil {
		t.Fatalf("CreateAsset() error = %v", err)
	}
	return a
}

func fullNodes() []domain.PipelineNodeState {
	return []domain.PipelineNodeState{
		{Key: domain.NodeKeyImport, Status: domain.NodeStatusPending},
		{Key: domain.NodeKeyAnalyze, Status: domain.NodeStatusPending},
		{Key: domain.NodeKeySeparate, Status: domain.NodeStatusPending},
		{Key: domain.NodeKeyExtract, Status: domain.NodeStatusPending},
		{Key: domain.NodeKeyErase, Status: domain.NodeStatusPending},
		{Key: domain.NodeKeyTranslate, Status: domain.NodeStatusPending},
		{Key: domain.NodeKeyClone, Status: domain.NodeStatusPending},
		{Key: domain.NodeKeyMix, Status: domain.NodeStatusPending},
	}
}

func seedRun(t *testing.T, s *Store, projectID, id string) domain.WorkflowRun {
	t.Helper()
	run := domain.WorkflowRun{
		ID:             id,
		ProjectID:      projectID,
		Name:           "译制计划",
		Template:       "full_pipeline",
		Status:         domain.RunStatusDraft,
		SourceLanguage: "en",
		Languages:      []string{"ja", "zh"},
		Nodes:          fullNodes(),
		Config: domain.RunConfig{
			InputRevisionIDs:   []domain.ID{"rev-input-1"},
			Extract:            &domain.ExtractOptions{Mode: domain.ExtractModeASROCR, OCRLanguage: "japanese"},
			EraseMode:          domain.EraseModeDialogOnly,
			ValidateTimestamps: true,
			AdjustDuration:     true,
			Bilingual:          true,
			SubtitleStyle:      &domain.SubtitleStyle{X: 0.5, Y: 0.9, FontSize: 0.05, Align: "center"},
		},
		CreatedAt: testTime,
		UpdatedAt: testTime,
	}
	if err := s.CreateWorkflowRun(context.Background(), run); err != nil {
		t.Fatalf("CreateWorkflowRun() error = %v", err)
	}
	return run
}
