package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func TestInstallationLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	inst := domain.Installation{
		ID: "inst-1", Slot: domain.CapabilitySlotTTS, Phase: domain.InstallationPhasePlanning,
		Progress: 0, PlanID: "plan-1", PlanHash: "hash-1",
		Release:   &domain.ReleaseRef{ReleaseID: "tts-ja", Version: "1.0.0", Digest: "release-digest"},
		PathOwner: "app", CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := s.CreateInstallation(ctx, inst); err != nil {
		t.Fatalf("CreateInstallation() error = %v", err)
	}
	if err := s.CreateInstallation(ctx, domain.Installation{
		ID: "inst-bad", Slot: "bogus", Phase: domain.InstallationPhasePlanning,
		CreatedAt: testTime, UpdatedAt: testTime,
	}); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Errorf("invalid slot error = %v, want ErrInvalidRequest", err)
	}

	now := testTime.Add(time.Hour)
	got, err := s.UpdateInstallationPhase(ctx, "inst-1", domain.InstallationPhaseDownloading, 10, 0, nil, false, now)
	if err != nil {
		t.Fatalf("UpdateInstallationPhase(downloading) error = %v", err)
	}
	if got.Phase != domain.InstallationPhaseDownloading || got.Progress != 10 {
		t.Fatalf("installation = %+v", got)
	}
	if got.PlanID != "plan-1" || got.PlanHash != "hash-1" || got.Release == nil || got.PathOwner != "app" {
		t.Errorf("installation internal fields lost: %+v", got)
	}
	// 同阶段只更新进度。
	got, err = s.UpdateInstallationPhase(ctx, "inst-1", domain.InstallationPhaseDownloading, 40, 3, nil, false, now)
	if err != nil {
		t.Fatalf("UpdateInstallationPhase(progress) error = %v", err)
	}
	if got.Progress != 40 || got.VerifiedFiles != 3 {
		t.Errorf("installation progress = %+v", got)
	}
	if _, err := s.UpdateInstallationPhase(ctx, "inst-1", domain.InstallationPhaseDone, 100, 5, nil, false, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("downloading -> done error = %v, want ErrInvalidTransition", err)
	}
	if _, err := s.UpdateInstallationPhase(ctx, "inst-1", domain.InstallationPhaseVerifying, 101, 0, nil, false, now); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Errorf("invalid progress error = %v, want ErrInvalidRequest", err)
	}

	steps := []domain.InstallationPhase{
		domain.InstallationPhaseVerifying,
		domain.InstallationPhaseInstalling,
		domain.InstallationPhaseDone,
	}
	for i, phase := range steps {
		got, err = s.UpdateInstallationPhase(ctx, "inst-1", phase, 50+i*20, i+1, nil, false, now)
		if err != nil {
			t.Fatalf("UpdateInstallationPhase(%s) error = %v", phase, err)
		}
	}
	if got.Phase != domain.InstallationPhaseDone {
		t.Fatalf("final phase = %s, want done", got.Phase)
	}
	if _, err := s.UpdateInstallationPhase(ctx, "inst-1", domain.InstallationPhaseDownloading, 0, 0, nil, false, now); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("terminal -> downloading error = %v, want ErrInvalidTransition", err)
	}

	// 失败安装记录错误信息与可重试标志。
	failedInst := domain.Installation{
		ID: "inst-2", Slot: domain.CapabilitySlotASR, Phase: domain.InstallationPhasePlanning,
		CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := s.CreateInstallation(ctx, failedInst); err != nil {
		t.Fatalf("CreateInstallation() error = %v", err)
	}
	failed, err := s.UpdateInstallationPhase(ctx, "inst-2", domain.InstallationPhaseFailed, 30, 1,
		&domain.ErrorInfo{Code: domain.ErrorCodeInsufficientDisk, MessageKey: "install.no_space", Retryable: true, ActionIDs: []domain.ID{"open_storage"}},
		true, now)
	if err != nil {
		t.Fatalf("UpdateInstallationPhase(failed) error = %v", err)
	}
	if failed.Error == nil || failed.Error.Code != domain.ErrorCodeInsufficientDisk || !failed.Retryable {
		t.Errorf("failed installation = %+v", failed)
	}

	list, err := s.ListInstallations(ctx)
	if err != nil {
		t.Fatalf("ListInstallations() error = %v", err)
	}
	if len(list) != 2 {
		t.Errorf("installations count = %d, want 2", len(list))
	}
	if _, err := s.GetInstallation(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing installation error = %v, want ErrNotFound", err)
	}
	if _, err := s.UpdateInstallationPhase(ctx, "missing", domain.InstallationPhaseDownloading, 0, 0, nil, false, now); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing installation update error = %v, want ErrNotFound", err)
	}
}
