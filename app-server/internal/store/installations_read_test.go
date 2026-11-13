package store

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// An autocommit reader releases the single database connection between the
// installation and binding queries. Simulate a legal completion at that exact
// boundary; the public read must return either full snapshot, never a mixture.
type installationReadInterleaver struct {
	DBTX
	once   sync.Once
	mutate func(context.Context) error
	err    error
}

func (d *installationReadInterleaver) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "FROM installation_slot_bindings") {
		d.once.Do(func() { d.err = d.mutate(ctx) })
		if d.err != nil {
			return nil, d.err
		}
	}
	return d.DBTX.QueryContext(ctx, query, args...)
}

func TestInstallationReadsKeepEntityAndBindingsInOneSnapshot(t *testing.T) {
	for _, name := range []string{"detail", "list"} {
		t.Run(name, func(t *testing.T) {
			st := newTestStore(t)
			ctx := context.Background()
			now := time.Date(2026, 11, 9, 1, 0, 0, 0, time.UTC)
			ref := domain.ReleaseRef{ReleaseID: "snapshot-release", Version: "1", Digest: strings.Repeat("a", 64)}
			key, err := domain.ReleaseStorageKey(ref)
			if err != nil {
				t.Fatal(err)
			}
			slots := []domain.CapabilitySlotKey{domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone}
			inst := domain.Installation{
				ID: "inst-snapshot", Slot: domain.CapabilitySlotTTS,
				Phase: domain.InstallationPhaseInstalling, Progress: 99,
				CoveredSlots: slots, Release: &ref, StorageKey: key,
				Manifest:  []domain.InstallationFile{{RelPath: "weights.bin", SHA256: strings.Repeat("a", 64), Size: 1}},
				CreatedAt: now, UpdatedAt: now,
			}
			if err := st.DoTx(ctx, func(tx *Tx) error { return tx.CreateInstallation(ctx, inst) }); err != nil {
				t.Fatal(err)
			}
			complete := func(ctx context.Context) error {
				return st.DoTx(ctx, func(tx *Tx) error {
					if _, err := tx.UpdateInstallationPhase(
						ctx,
						inst.ID,
						domain.InstallationPhaseDone,
						100,
						1,
						nil,
						false,
						now.Add(time.Second),
					); err != nil {
						return err
					}
					return tx.SetCurrentBindings(ctx, inst.ID, slots, now.Add(time.Second))
				})
			}
			interleaver := &installationReadInterleaver{DBTX: st.DB(), mutate: complete}
			st.querier = &querier{db: interleaver}

			var got domain.Installation
			switch name {
			case "detail":
				got, err = st.GetInstallation(ctx, inst.ID)
			case "list":
				var list []domain.Installation
				list, err = st.ListInstallations(ctx)
				if err == nil && len(list) != 1 {
					t.Fatalf("list length = %d, want 1", len(list))
				}
				if len(list) == 1 {
					got = list[0]
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before := got.Phase == domain.InstallationPhaseInstalling && got.Progress == 99 && len(got.CurrentSlots) == 0
			after := got.Phase == domain.InstallationPhaseDone && got.Progress == 100 && len(got.CurrentSlots) == 2
			if !before && !after {
				t.Fatalf("mixed installation snapshot: phase=%s progress=%d current_slots=%v", got.Phase, got.Progress, got.CurrentSlots)
			}
			// A Store-level snapshot may bypass autocommit queries entirely. Complete
			// afterwards in that case, and prove the retained read was the valid old
			// snapshot while the next transaction sees the valid new one.
			interleaver.once.Do(func() { interleaver.err = complete(ctx) })
			if interleaver.err != nil {
				t.Fatal(interleaver.err)
			}
			if err := st.ReadTx(ctx, func(tx *Tx) error {
				fresh, err := tx.GetInstallation(ctx, inst.ID)
				if err != nil {
					return err
				}
				if fresh.Phase != domain.InstallationPhaseDone || len(fresh.CurrentSlots) != 2 {
					t.Errorf("completed read = %+v", fresh)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
