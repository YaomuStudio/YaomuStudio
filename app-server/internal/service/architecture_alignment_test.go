package service

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func TestCreateTaskContractRejectsExportKind(t *testing.T) {
	err := validateCreateTaskRequest(CreateTaskRequest{
		ProjectID: "project", Kind: domain.TaskKindExport,
	})
	if !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("export create-task request error = %v", err)
	}
}

// The expected behavior comes from data-and-storage section 2: late model
// results remain historical and must not replace the active human revision.
func TestArchitectureAuditLateResultPreservesHuman(t *testing.T) {
	e := newExportTestEnv(t)
	ctx := context.Background()
	project := e.seedProject("audit")
	var original domain.SubtitleRevision
	err := e.store.DoTx(ctx, func(tx *store.Tx) error {
		var err error
		original, err = e.svc.upsertTrackRevision(ctx, tx, project.ID, "en", domain.TrackTypeTranscription, "", []domain.SegmentInput{{StartMs: 0, EndMs: 1000, Text: "original"}}, e.svc.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := e.svc.SaveSubtitleRevision(ctx, Idem{}, original.TrackID, SaveSubtitleRevisionRequest{
		BaseRevisionID: original.ID,
		Segments:       []domain.SegmentInput{{ID: original.Segments[0].ID, StartMs: 0, EndMs: 1000, Text: "human-approved"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	human := saved.Body.(domain.SubtitleRevision)
	prepared := &preparedNodeResult{
		svc: e.svc, now: e.svc.Now(), trackType: domain.TrackTypeTranscription, trackLanguage: "en",
		segments: []domain.SegmentInput{{StartMs: 0, EndMs: 1000, Text: "late-model-result"}},
	}
	err = e.store.DoTx(ctx, func(tx *store.Tx) error {
		var events []domain.DomainEvent
		_, err := e.svc.commitSubtitleTrack(ctx, tx, domain.Task{
			ID: "late-task", ProjectID: project.ID, RunID: "pending-run", Kind: domain.TaskKindNode,
			NodeKey: domain.NodeKeyExtract, Language: "en", InputRevisionIDs: []string{original.ID},
		}, prepared, &events)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	track, err := e.store.GetSubtitleTrack(ctx, original.TrackID)
	if err != nil {
		t.Fatal(err)
	}
	if track.CurrentRevisionID != human.ID {
		t.Fatalf("late result replaced active human revision: current_author=%s current_text=%q", currentRevision(track).AuthorType, currentRevision(track).Segments[0].Text)
	}
}

func TestSubtitleSnapshotAdvancesOnlyMatchingBaseline(t *testing.T) {
	e := newExportTestEnv(t)
	ctx := context.Background()
	project := e.seedProject("snapshot")
	var original domain.SubtitleRevision
	err := e.store.DoTx(ctx, func(tx *store.Tx) error {
		var err error
		original, err = e.svc.upsertTrackRevision(ctx, tx, project.ID, "en",
			domain.TrackTypeTranscription, "",
			[]domain.SegmentInput{{StartMs: 0, EndMs: 1000, Text: "original"}}, e.svc.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared := &preparedNodeResult{
		svc: e.svc, now: e.svc.Now(), trackType: domain.TrackTypeTranscription, trackLanguage: "en",
		segments: []domain.SegmentInput{{
			ID: original.Segments[0].ID, StartMs: 0, EndMs: 1000, Text: "new model result",
		}},
	}
	var saved domain.SubtitleRevision
	err = e.store.DoTx(ctx, func(tx *store.Tx) error {
		var events []domain.DomainEvent
		var err error
		saved, err = e.svc.commitSubtitleTrack(ctx, tx, domain.Task{
			ID: "matching-task", ProjectID: project.ID, RunID: "run", Kind: domain.TaskKindNode,
			NodeKey: domain.NodeKeyExtract, Language: "en",
			Params: map[string]any{"_core": map[string]any{
				"output_snapshot": true, "output_track_id": original.TrackID,
				"output_base_revision_id": original.ID,
			}},
		}, prepared, &events)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	track, err := e.store.GetSubtitleTrack(ctx, original.TrackID)
	if err != nil {
		t.Fatal(err)
	}
	if track.CurrentRevisionID != saved.ID || currentRevision(track).Segments[0].Text != "new model result" {
		t.Fatalf("matching baseline did not advance: current=%s saved=%s", track.CurrentRevisionID, saved.ID)
	}
}

// A referenced asset must preserve its imported content identity even when
// the external file changes (data-and-storage section 4).
func TestArchitectureAuditRejectsMutatedReference(t *testing.T) {
	e := newExportTestEnv(t)
	ctx := context.Background()
	project := e.seedProject("audit")
	source := e.tempFile("source.wav", "AAAA")
	resp, err := e.svc.ImportAssets(ctx, Idem{}, AssetImportRequest{ProjectID: project.ID, FileGrants: []string{source}})
	if err != nil {
		t.Fatal(err)
	}
	id := resp.Body.(AssetImportResult).AssetIDs[0]
	if err := os.WriteFile(source, []byte("BBBB"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := e.svc.AssetContentFor(ctx, id)
	if err != nil {
		return
	}
	data, err := os.ReadFile(content.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "AAAA" {
		t.Fatalf("unchanged asset ID served altered external content: %q", data)
	}
}
