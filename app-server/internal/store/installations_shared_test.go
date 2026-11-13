package store

// schema10 共享安装实体与槽位引用投影的仓储测试。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func sharedTestRelease(id string) domain.ReleaseRef {
	return domain.ReleaseRef{ReleaseID: id, Version: "1.0.0", Digest: "sha256:" + id, Channel: "test"}
}

func TestSharedInstallationBindingsAndProjections(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	release := sharedTestRelease("shared")
	key, err := domain.ReleaseStorageKey(release)
	if err != nil {
		t.Fatal(err)
	}
	manifest := []domain.InstallationFile{{
		RelPath: "model.bin", SHA256: strings.Repeat("a", 64), Size: 4,
	}}
	covered := []domain.CapabilitySlotKey{domain.CapabilitySlotVoiceClone, domain.CapabilitySlotTTS}
	inst := domain.Installation{
		ID: "inst-shared", Slot: domain.CapabilitySlotTTS, CoveredSlots: covered,
		Phase: domain.InstallationPhasePlanning, Release: &release, StorageKey: key,
		Manifest: manifest, PathOwner: "app", CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := s.CreateInstallation(ctx, inst); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}
	got, err := s.GetInstallation(ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if !sameSlots(got.CoveredSlots, []domain.CapabilitySlotKey{domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone}) {
		t.Fatalf("covered_slots = %v", got.CoveredSlots)
	}
	if !sameSlots(got.BoundSlots, got.CoveredSlots) {
		t.Fatalf("bound_slots = %v", got.BoundSlots)
	}
	if len(got.CurrentSlots) != 0 {
		t.Fatalf("pending record current_slots = %v, want empty", got.CurrentSlots)
	}
	if got.StorageKey != key || len(got.Manifest) != 1 || got.Manifest[0].SHA256 != manifest[0].SHA256 {
		t.Fatalf("internal identity lost: key=%q manifest=%+v", got.StorageKey, got.Manifest)
	}

	// 同 storage_key 的第二个非终态/Done 实体被唯一索引拒绝。
	duplicate := inst
	duplicate.ID = "inst-shared-2"
	duplicate.CreatedAt = testTime.Add(time.Minute)
	duplicate.UpdatedAt = duplicate.CreatedAt
	if err := s.CreateInstallation(ctx, duplicate); err == nil ||
		!strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("duplicate active storage key error = %v", err)
	}

	// 完成时同一事务激活全部覆盖槽位；当前每槽唯一。
	now := testTime.Add(2 * time.Minute)
	if err := s.DoTx(ctx, func(tx *Tx) error {
		return tx.SetCurrentBindings(ctx, inst.ID, covered, now)
	}); err != nil {
		t.Fatalf("SetCurrentBindings: %v", err)
	}
	got, err = s.GetInstallation(ctx, inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameSlots(got.CurrentSlots, []domain.CapabilitySlotKey{domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone}) {
		t.Fatalf("current_slots = %v", got.CurrentSlots)
	}

	// 解除一个槽位的全部引用：实体保留、另一槽位仍在。
	var detached int64
	if err := s.DoTx(ctx, func(tx *Tx) error {
		var err error
		detached, err = tx.DeleteSlotBindings(ctx, domain.CapabilitySlotTTS)
		return err
	}); err != nil {
		t.Fatalf("DeleteSlotBindings: %v", err)
	}
	if detached != 1 {
		t.Fatalf("detached bindings = %d, want 1", detached)
	}
	got, err = s.GetInstallation(ctx, inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameSlots(got.BoundSlots, []domain.CapabilitySlotKey{domain.CapabilitySlotVoiceClone}) ||
		!sameSlots(got.CurrentSlots, []domain.CapabilitySlotKey{domain.CapabilitySlotVoiceClone}) {
		t.Fatalf("projections after detach = bound %v current %v", got.BoundSlots, got.CurrentSlots)
	}
	if !sameSlots(got.CoveredSlots, []domain.CapabilitySlotKey{domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone}) {
		t.Fatalf("covered history rewritten by detach: %v", got.CoveredSlots)
	}
}

func TestLegacyInstallationProjectionAndDirectDoneSelection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	release := sharedTestRelease("legacy")
	older := domain.Installation{
		ID: "inst-older", Slot: domain.CapabilitySlotASR, Phase: domain.InstallationPhaseDone,
		VerifiedFiles: 1, Release: &release, PathOwner: "app",
		CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := s.CreateInstallation(ctx, older); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInstallation(ctx, older.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameSlots(got.CoveredSlots, []domain.CapabilitySlotKey{domain.CapabilitySlotASR}) ||
		got.StorageKey != "" || len(got.Manifest) != 0 {
		t.Fatalf("legacy projection fabricated shared identity: %+v", got)
	}
	if !sameSlots(got.CurrentSlots, []domain.CapabilitySlotKey{domain.CapabilitySlotASR}) {
		t.Fatalf("direct Done creation did not set current: %v", got.CurrentSlots)
	}

	newer := older
	newer.ID = "inst-newer"
	newer.CreatedAt = testTime.Add(time.Minute)
	newer.UpdatedAt = newer.CreatedAt
	if err := s.CreateInstallation(ctx, newer); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetInstallation(ctx, newer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameSlots(got.CurrentSlots, []domain.CapabilitySlotKey{domain.CapabilitySlotASR}) {
		t.Fatalf("newer direct Done current = %v", got.CurrentSlots)
	}
	previous, err := s.GetInstallation(ctx, older.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(previous.CurrentSlots) != 0 || !sameSlots(previous.BoundSlots, []domain.CapabilitySlotKey{domain.CapabilitySlotASR}) {
		t.Fatalf("older done should stay retained: current %v bound %v", previous.CurrentSlots, previous.BoundSlots)
	}
	list, err := s.ListInstallations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != newer.ID {
		t.Fatalf("list order/records = %+v", list)
	}
}

func sameSlots(a, b []domain.CapabilitySlotKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
