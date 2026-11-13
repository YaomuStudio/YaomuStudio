package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func TestReviewGateApproval(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedRun(t, s, project.ID, "run-1")

	gate := domain.ReviewGate{
		ID:             "review-1",
		RunID:          "run-1",
		Type:           domain.ReviewTypeTranslation,
		Language:       "ja",
		RevisionDigest: "digest-1",
		Status:         domain.ReviewStatusPending,
	}
	if err := s.CreateReviewGate(ctx, gate); err != nil {
		t.Fatalf("CreateReviewGate() error = %v", err)
	}
	if err := s.CreateReviewGate(ctx, domain.ReviewGate{
		ID: "review-bad", RunID: "run-1", Type: domain.ReviewTypeTranslation,
		RevisionDigest: "d", Status: domain.ReviewStatusApproved,
	}); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Errorf("approved-at-create error = %v, want ErrInvalidRequest", err)
	}

	now := testTime.Add(time.Hour)
	if _, err := s.ApproveReview(ctx, "review-1", "other-digest", domain.ReviewTypeTranslation, now); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Errorf("digest mismatch error = %v, want ErrRevisionConflict", err)
	}
	if _, err := s.ApproveReview(ctx, "review-1", "digest-1", domain.ReviewTypeDelivery, now); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Errorf("type mismatch error = %v, want ErrInvalidRequest", err)
	}
	// 冲突后仍保持 pending，旧审核不能放行新内容。
	stored, err := s.GetReviewGate(ctx, "review-1")
	if err != nil {
		t.Fatalf("GetReviewGate() error = %v", err)
	}
	if stored.Status != domain.ReviewStatusPending || stored.ConfirmedAt != nil {
		t.Fatalf("review after conflict = %+v", stored)
	}

	approved, err := s.ApproveReview(ctx, "review-1", "digest-1", domain.ReviewTypeTranslation, now)
	if err != nil {
		t.Fatalf("ApproveReview() error = %v", err)
	}
	if approved.Status != domain.ReviewStatusApproved || approved.ConfirmedAt == nil {
		t.Fatalf("approved = %+v", approved)
	}
	if !approved.ConfirmedAt.Equal(now) || approved.ConfirmedAt.Location() != time.UTC {
		t.Errorf("confirmed_at = %v, want UTC %v", approved.ConfirmedAt, now)
	}
	if _, err := s.ApproveReview(ctx, "review-1", "digest-1", domain.ReviewTypeTranslation, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("second approval error = %v, want ErrInvalidTransition", err)
	}
	if _, err := s.ApproveReview(ctx, "missing", "digest-1", domain.ReviewTypeTranslation, now); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing review error = %v, want ErrNotFound", err)
	}

	gates, err := s.ListRunReviews(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListRunReviews() error = %v", err)
	}
	if len(gates) != 1 || gates[0].ID != "review-1" || gates[0].Language != "ja" {
		t.Errorf("reviews = %+v", gates)
	}
}

func TestApproveReviewRejectsTerminalRun(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedRun(t, s, project.ID, "run-1")

	gate := domain.ReviewGate{
		ID: "review-terminal", RunID: "run-1", Type: domain.ReviewTypeDelivery,
		RevisionDigest: "digest-1", Status: domain.ReviewStatusPending,
	}
	if err := s.CreateReviewGate(ctx, gate); err != nil {
		t.Fatalf("CreateReviewGate() error = %v", err)
	}
	now := testTime.Add(time.Hour)
	for i, status := range []domain.RunStatus{
		domain.RunStatusQueued, domain.RunStatusRunning, domain.RunStatusSucceeded,
	} {
		if _, err := s.UpdateRunStatus(ctx, "run-1", status, int64(i+1), now); err != nil {
			t.Fatalf("UpdateRunStatus(%s) error = %v", status, err)
		}
	}
	// 终止 Run 拒绝审核放行（docs/local-api-contract.md §8）。
	if _, err := s.ApproveReview(ctx, gate.ID, "digest-1", domain.ReviewTypeDelivery, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("terminal run approval error = %v, want ErrInvalidTransition", err)
	}
	stored, err := s.GetReviewGate(ctx, gate.ID)
	if err != nil {
		t.Fatalf("GetReviewGate() error = %v", err)
	}
	if stored.Status != domain.ReviewStatusPending || stored.ConfirmedAt != nil {
		t.Errorf("review after rejected approval = %+v", stored)
	}
}

func TestApproveReviewAllowedWhilePaused(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedRun(t, s, project.ID, "run-1")

	gate := domain.ReviewGate{
		ID: "review-paused", RunID: "run-1", Type: domain.ReviewTypeTranslation,
		RevisionDigest: "digest-1", Status: domain.ReviewStatusPending,
	}
	if err := s.CreateReviewGate(ctx, gate); err != nil {
		t.Fatalf("CreateReviewGate() error = %v", err)
	}
	now := testTime.Add(time.Hour)
	for i, status := range []domain.RunStatus{
		domain.RunStatusQueued, domain.RunStatusRunning,
		domain.RunStatusPauseRequested, domain.RunStatusPaused,
	} {
		if _, err := s.UpdateRunStatus(ctx, "run-1", status, int64(i+1), now); err != nil {
			t.Fatalf("UpdateRunStatus(%s) error = %v", status, err)
		}
	}
	// PAUSED 允许保存审核记录，只是不派发新任务。
	approved, err := s.ApproveReview(ctx, gate.ID, "digest-1", domain.ReviewTypeTranslation, now)
	if err != nil {
		t.Fatalf("paused run approval error = %v", err)
	}
	if approved.Status != domain.ReviewStatusApproved {
		t.Errorf("review status = %s, want approved", approved.Status)
	}
}

func TestCandidateSelection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedAsset(t, s, project.ID, "asset-audio-1", domain.AssetTypeAudioTrack)

	candidates := []domain.AudioCandidate{
		{
			ID: "cand-1", ProjectID: project.ID, SegmentID: "seg-1", Language: "ja",
			Revision: 1, ModelVersion: "tts-1.0", AudioAssetID: "asset-audio-1",
			IsRecommended: true, CreatedAt: testTime,
		},
		{
			ID: "cand-2", ProjectID: project.ID, SegmentID: "seg-1", Language: "ja",
			Revision: 1, ModelVersion: "tts-1.1", AudioAssetID: "asset-audio-1",
			CreatedAt: testTime.Add(time.Minute),
		},
	}
	for _, c := range candidates {
		if err := s.CreateAudioCandidate(ctx, c); err != nil {
			t.Fatalf("CreateAudioCandidate() error = %v", err)
		}
	}

	// 候选音频必须属于同一项目，且资产必须存在。
	other := seedProject(t, s, "project-2")
	seedAsset(t, s, other.ID, "asset-other", domain.AssetTypeAudioTrack)
	crossProject := candidates[0]
	crossProject.ID = "cand-cross"
	crossProject.ProjectID = project.ID
	crossProject.AudioAssetID = "asset-other"
	if err := s.CreateAudioCandidate(ctx, crossProject); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Errorf("cross-project candidate error = %v, want ErrInvalidRequest", err)
	}
	missingAsset := candidates[0]
	missingAsset.ID = "cand-missing-asset"
	missingAsset.AudioAssetID = "missing-asset"
	if err := s.CreateAudioCandidate(ctx, missingAsset); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing asset candidate error = %v, want ErrNotFound", err)
	}

	list, err := s.ListAudioCandidates(ctx, "seg-1", "ja", nil)
	if err != nil {
		t.Fatalf("ListAudioCandidates() error = %v", err)
	}
	if len(list) != 2 || !list[0].IsRecommended {
		t.Fatalf("candidates = %+v", list)
	}
	rev := 2
	list, err = s.ListAudioCandidates(ctx, "seg-1", "ja", &rev)
	if err != nil {
		t.Fatalf("ListAudioCandidates(rev 2) error = %v", err)
	}
	if len(list) != 0 {
		t.Errorf("revision filter returned %d candidates, want 0", len(list))
	}

	stale := domain.CandidateSelection{
		SegmentID: "seg-1", Language: "ja", CandidateID: "cand-1", Revision: 2, SelectedAt: testTime,
	}
	if err := s.SaveCandidateSelection(ctx, stale); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Errorf("stale revision selection error = %v, want ErrRevisionConflict", err)
	}
	wrongSegment := domain.CandidateSelection{
		SegmentID: "seg-other", Language: "ja", CandidateID: "cand-1", Revision: 1, SelectedAt: testTime,
	}
	if err := s.SaveCandidateSelection(ctx, wrongSegment); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Errorf("wrong segment selection error = %v, want ErrRevisionConflict", err)
	}
	missing := domain.CandidateSelection{
		SegmentID: "seg-1", Language: "ja", CandidateID: "missing", Revision: 1, SelectedAt: testTime,
	}
	if err := s.SaveCandidateSelection(ctx, missing); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing candidate selection error = %v, want ErrNotFound", err)
	}

	selection := domain.CandidateSelection{
		SegmentID: "seg-1", Language: "ja", CandidateID: "cand-1", Revision: 1, SelectedAt: testTime,
	}
	if err := s.SaveCandidateSelection(ctx, selection); err != nil {
		t.Fatalf("SaveCandidateSelection() error = %v", err)
	}
	stored, err := s.GetCandidateSelection(ctx, "seg-1", "ja")
	if err != nil {
		t.Fatalf("GetCandidateSelection() error = %v", err)
	}
	if stored.CandidateID != "cand-1" || stored.Revision != 1 {
		t.Errorf("selection = %+v", stored)
	}

	// 同句段/语言重新选择是 upsert。
	selection.CandidateID = "cand-2"
	selection.SelectedAt = testTime.Add(time.Minute)
	if err := s.SaveCandidateSelection(ctx, selection); err != nil {
		t.Fatalf("SaveCandidateSelection(upsert) error = %v", err)
	}
	stored, err = s.GetCandidateSelection(ctx, "seg-1", "ja")
	if err != nil {
		t.Fatalf("GetCandidateSelection() error = %v", err)
	}
	if stored.CandidateID != "cand-2" {
		t.Errorf("selection after upsert = %+v", stored)
	}
	if _, err := s.GetCandidateSelection(ctx, "seg-1", "zh"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing selection error = %v, want ErrNotFound", err)
	}

	// 删除项目级联删除候选与选择。
	if err := s.DeleteProject(ctx, project.ID); err != nil {
		t.Fatalf("DeleteProject() error = %v", err)
	}
	for _, table := range []string{"audio_candidates", "candidate_selections"} {
		var count int
		if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("table %s has %d rows after project delete", table, count)
		}
	}
}

func TestReviewGateCreatedAtRoundtrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-created-at")
	seedRun(t, s, project.ID, "run-1")

	createdAt := testTime.Add(30 * time.Minute)
	if err := s.CreateReviewGate(ctx, domain.ReviewGate{
		ID: "review-created", RunID: "run-1", Type: domain.ReviewTypeDelivery,
		RevisionDigest: "digest-created", Status: domain.ReviewStatusPending,
		CreatedAt: &createdAt,
	}); err != nil {
		t.Fatalf("CreateReviewGate() error = %v", err)
	}
	got, err := s.GetReviewGate(ctx, "review-created")
	if err != nil {
		t.Fatalf("GetReviewGate() error = %v", err)
	}
	if got.CreatedAt == nil || !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("created_at = %v, want %v", got.CreatedAt, createdAt)
	}

	// 旧记录没有 created_at 时保持 nil，读取不报错。
	if err := s.CreateReviewGate(ctx, domain.ReviewGate{
		ID: "review-legacy", RunID: "run-1", Type: domain.ReviewTypeTranslation,
		RevisionDigest: "digest-legacy", Status: domain.ReviewStatusPending,
	}); err != nil {
		t.Fatalf("CreateReviewGate(legacy) error = %v", err)
	}
	legacy, err := s.GetReviewGate(ctx, "review-legacy")
	if err != nil {
		t.Fatalf("GetReviewGate(legacy) error = %v", err)
	}
	if legacy.CreatedAt != nil {
		t.Fatalf("legacy created_at = %v, want nil", legacy.CreatedAt)
	}
}
