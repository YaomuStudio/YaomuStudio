package service

import (
	"context"
	"errors"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func (s *Service) saveTaskSubtitleResult(
	ctx context.Context,
	tx *store.Tx,
	task domain.Task,
	prepared *preparedNodeResult,
) (domain.SubtitleRevision, error) {
	metadata, _ := task.Params["_core"].(map[string]any)
	trackID, _ := metadata["output_track_id"].(string)
	baseID, _ := metadata["output_base_revision_id"].(string)
	frozen, _ := metadata["output_snapshot"].(bool)
	var target *domain.SubtitleTrack
	if trackID != "" {
		track, err := tx.GetSubtitleTrack(ctx, trackID)
		if errors.Is(err, domain.ErrNotFound) && frozen && baseID == "" {
			tracks, readErr := tx.ListProjectTracks(ctx, task.ProjectID)
			if readErr != nil {
				return domain.SubtitleRevision{}, readErr
			}
			if existing := pickPipelineTrack(tracks, prepared.trackLanguage, prepared.trackType); existing != nil {
				return tx.AppendHistoricalSubtitleRevision(ctx, existing.ID, "", store.RevisionSave{
					AuthorType: domain.AuthorTypeSystem, Segments: prepared.segments,
					InputRevisionID: prepared.sourceRevisionID,
				}, prepared.now)
			}
			return s.createTaskSubtitleTrack(ctx, tx, task, prepared, trackID)
		}
		if err != nil {
			return domain.SubtitleRevision{}, err
		}
		if track.ProjectID != task.ProjectID || track.Language != prepared.trackLanguage || track.Type != prepared.trackType {
			return domain.SubtitleRevision{}, invalidRequestf("subtitle output target does not match task")
		}
		target = &track
	} else {
		tracks, err := tx.ListProjectTracks(ctx, task.ProjectID)
		if err != nil {
			return domain.SubtitleRevision{}, err
		}
		target = pickPipelineTrack(tracks, prepared.trackLanguage, prepared.trackType)
	}
	if target == nil {
		return s.upsertTrackRevision(
			ctx, tx, task.ProjectID, prepared.trackLanguage, prepared.trackType,
			prepared.sourceRevisionID, prepared.segments, prepared.now,
		)
	}
	input := store.RevisionSave{
		AuthorType: domain.AuthorTypeSystem, Segments: prepared.segments,
		InputRevisionID: prepared.sourceRevisionID,
	}
	// A legacy task has no trustworthy output baseline. Preserve its result as
	// history whenever a target already exists, including an initial-empty race.
	if !frozen || trackID == "" || target.CurrentRevisionID != baseID {
		return tx.AppendHistoricalSubtitleRevision(ctx, target.ID, baseID, input, prepared.now)
	}
	return tx.SaveSubtitleRevision(ctx, target.ID, baseID, input, prepared.now)
}

func (s *Service) createTaskSubtitleTrack(
	ctx context.Context,
	tx *store.Tx,
	task domain.Task,
	prepared *preparedNodeResult,
	trackID domain.ID,
) (domain.SubtitleRevision, error) {
	revisionID := store.NewID("srev")
	segments := make([]domain.Segment, 0, len(prepared.segments))
	for _, input := range prepared.segments {
		id := input.ID
		if id == "" {
			id = store.NewID("seg")
		}
		segments = append(segments, domain.Segment{
			ID: id, StartMs: input.StartMs, EndMs: input.EndMs, Text: input.Text,
			Speaker: input.Speaker, Lineage: input.Lineage,
		})
	}
	revision := domain.SubtitleRevision{
		ID: revisionID, TrackID: trackID, AuthorType: domain.AuthorTypeSystem,
		InputRevisionID: prepared.sourceRevisionID, Segments: segments, CreatedAt: prepared.now,
	}
	if err := tx.CreateSubtitleTrack(ctx, domain.SubtitleTrack{
		ID: trackID, ProjectID: task.ProjectID, Language: prepared.trackLanguage,
		Type: prepared.trackType, CurrentRevisionID: revisionID,
		Revisions: []domain.SubtitleRevision{revision}, CreatedAt: prepared.now, UpdatedAt: prepared.now,
	}); err != nil {
		return domain.SubtitleRevision{}, err
	}
	return tx.GetSubtitleRevision(ctx, revisionID)
}
