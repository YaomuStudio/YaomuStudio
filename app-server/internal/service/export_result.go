package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/resultcommit"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

type preparedExportResult struct {
	svc       *Service
	record    domain.ExportRecord
	assets    []domain.Asset
	locations []domain.AssetLocation
	intents   []*fileIntent
}

func (p *preparedExportResult) ArtifactIDs() []domain.ID {
	ids := make([]domain.ID, 0, len(p.assets))
	for _, asset := range p.assets {
		ids = append(ids, asset.ID)
	}
	return ids
}

func (s *Service) prepareExportResult(
	ctx context.Context,
	task domain.Task,
	result resultcommit.ExecutionResult,
) (resultcommit.Prepared, error) {
	record, err := s.store.GetExportByTask(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if len(result.Outputs) == 0 {
		return nil, fmt.Errorf("export produced no outputs")
	}
	taskRoot := filepath.Join(s.workDir(), task.ID)
	if result.OutputDir == "" || !pathWithin(taskRoot, result.OutputDir) {
		return nil, invalidRequestf("export output dir is outside task work dir")
	}
	if err := rejectSymlinkPath(s.workDir(), result.OutputDir); err != nil {
		return nil, invalidRequestf("export output dir contains symlink: %v", err)
	}
	project, err := s.store.GetProject(ctx, record.ProjectID)
	if err != nil {
		return nil, err
	}
	language, _ := task.Params["language"].(string)
	base := s.exportBaseName(project.Name, language, record.NamingTemplate, s.Now())
	prepared := &preparedExportResult{
		svc: s, record: record,
		assets: []domain.Asset{}, locations: []domain.AssetLocation{}, intents: []*fileIntent{},
	}
	for _, output := range result.Outputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := validateAttemptRelPath(output.RelPath); err != nil {
			return nil, invalidRequestf("%v", err)
		}
		source := filepath.Join(result.OutputDir, filepath.FromSlash(output.RelPath))
		if err := rejectSymlinkPath(result.OutputDir, source); err != nil {
			return nil, invalidRequestf("export output path is not owned by attempt: %v", err)
		}
		info, err := os.Lstat(source)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			return nil, fmt.Errorf("export output is missing, empty or not a regular file")
		}
		if output.SizeBytes > 0 && output.SizeBytes != info.Size() {
			return nil, fmt.Errorf("export output size differs from manifest")
		}
		if err := validateMediaHeader(source, record.Format); err != nil {
			return nil, err
		}
		assetID := store.NewID("asset")
		var intent *fileIntent
		landed, size, digest, err := s.landOutputTracked(
			source, record.OutputDirGrant, base, exportExtension(record.Format),
			func(destination string) error {
				var err error
				intent, err = s.beginFileIntent(assetID, destination, false)
				return err
			},
		)
		if err != nil {
			if errors.Is(err, domain.ErrInvalidRequest) {
				return nil, err
			}
			return nil, errors.Join(domain.ErrInsufficientDisk, err)
		}
		asset := exportAsset(record.ProjectID, record.RunID, record.Kind, landed, size, digest, s.Now())
		asset.ID = assetID
		prepared.assets = append(prepared.assets, asset)
		prepared.locations = append(prepared.locations, domain.AssetLocation{
			ID: store.NewID("loc"), AssetID: asset.ID, Path: landed,
			Source: domain.LocationSourceGenerated, Owner: domain.LocationOwnerApp,
			Available: true, CreatedAt: s.Now(),
		})
		prepared.intents = append(prepared.intents, intent)
	}
	return prepared, nil
}

func (p *preparedExportResult) Commit(
	ctx context.Context,
	tx *store.Tx,
	task domain.Task,
	events *[]domain.DomainEvent,
) error {
	for i, asset := range p.assets {
		if err := tx.CreateAsset(ctx, asset); err != nil {
			return err
		}
		if err := tx.AddAssetLocation(ctx, p.locations[i]); err != nil {
			return err
		}
	}
	attempt, found, err := tx.LatestAttempt(ctx, task.ID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("export task has no attempt")
	}
	now := p.svc.Now()
	if err := tx.CreateArtifactCommit(ctx, domain.ArtifactCommit{
		ID: store.NewID("commit"), AttemptID: attempt.ID, AssetIDs: p.ArtifactIDs(),
		Status: domain.CommitStatusCommitted, CommittedAt: &now,
	}); err != nil {
		return err
	}
	if err := tx.UpdateExportStatus(ctx, p.record.ID, domain.TaskStatusSucceeded, p.assets[0].Path, now); err != nil {
		return err
	}
	return p.svc.stageEvent(ctx, tx, events, domain.DomainEvent{
		Type: "export.succeeded", ProjectID: p.record.ProjectID, TaskID: task.ID,
		Data: map[string]any{
			"export_id": p.record.ID, "format": p.record.Format,
			"output_path": p.assets[0].Path, "artifact_ids": p.ArtifactIDs(),
		},
	})
}

func (p *preparedExportResult) AfterCommit() error {
	return finishFileIntents(p.intents)
}
