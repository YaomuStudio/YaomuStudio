package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

const stagedSubtitlePlaceholder = "core://frozen-subtitle"

// PrepareTaskExecution is called after claim, outside any SQLite transaction.
// The persisted plan keeps asset identities; execution uses immutable task-local
// input copies so changes to an external reference cannot alter a running job.
func (s *Service) PrepareTaskExecution(ctx context.Context, task domain.Task) (map[string]any, error) {
	encoded, err := json.Marshal(task.Params)
	if err != nil {
		return nil, fmt.Errorf("encode frozen execution parameters: %w", err)
	}
	params := map[string]any{}
	if string(encoded) != "null" {
		if err := json.Unmarshal(encoded, &params); err != nil {
			return nil, fmt.Errorf("decode frozen execution parameters: %w", err)
		}
	}
	assets, err := s.store.ListProjectAssets(ctx, task.ProjectID)
	if err != nil {
		return nil, err
	}
	byID := map[domain.ID]domain.Asset{}
	byPath := map[string]domain.Asset{}
	ambiguousPaths := map[string]struct{}{}
	for _, asset := range assets {
		byID[asset.ID] = asset
		if asset.Path != "" {
			cleaned := filepath.Clean(asset.Path)
			if _, exists := byPath[cleaned]; exists {
				ambiguousPaths[cleaned] = struct{}{}
			}
			byPath[cleaned] = asset
		}
	}
	if err := s.validateFrozenCloneTimeline(ctx, task, params, byID); err != nil {
		return nil, err
	}
	inputs := append([]domain.ID{}, task.InputRevisionIDs...)
	if source, ok := params["voice_source_asset_id"].(string); ok && source != "" {
		inputs = append(inputs, source)
	}
	prepared := map[domain.ID]string{}
	for _, id := range inputs {
		asset, found := byID[id]
		if !found {
			if _, err := s.store.GetSubtitleRevision(ctx, id); err != nil {
				return nil, mapReference(err, "frozen task input")
			}
			continue
		}
		path, err := s.prepareInputAsset(ctx, task.ID, asset)
		if err != nil {
			return nil, err
		}
		prepared[id] = path
	}
	core := readCoreTaskParams(params)
	materialized := map[string]struct{}{}
	if revisionID, _ := core["subtitle_revision_id"].(string); revisionID != "" {
		path, err := s.materializeFrozenSubtitle(ctx, task, revisionID, core, "subtitle_digest")
		if err != nil {
			return nil, err
		}
		switch {
		case task.NodeKey == domain.NodeKeyTranslate:
			// 06 翻译适配器只接受物化后的源 SRT 绝对路径；不带 08 的执行计划。
			params["subtitle_url"] = path
		case params["op"] == "script":
			params["subtitle_path"] = path
		default:
			plan, ok := params["plan"].(map[string]any)
			if !ok {
				return nil, invalidRequestf("subtitle input requires an execution plan")
			}
			plan["subtitle"] = path
		}
		materialized[filepath.Clean(path)] = struct{}{}
	}
	if params["op"] == "script" {
		originalID, _ := core["original_subtitle_revision_id"].(string)
		if originalID == "" {
			return nil, invalidRequestf("clone original subtitle is missing")
		}
		originalPath, err := s.materializeFrozenSubtitle(ctx, task, originalID, core, "original_subtitle_digest")
		if err != nil {
			return nil, err
		}
		params["original_subtitle_path"] = originalPath
		materialized[filepath.Clean(originalPath)] = struct{}{}
	}
	var rewrite func(map[string]any) error
	rewrite = func(object map[string]any) error {
		for key, value := range object {
			if key == "_core" {
				continue
			}
			switch child := value.(type) {
			case map[string]any:
				if err := rewrite(child); err != nil {
					return err
				}
			case []any:
				for _, element := range child {
					if nested, ok := element.(map[string]any); ok {
						if err := rewrite(nested); err != nil {
							return err
						}
					}
				}
			case string:
				if key != "path" && key != "input" && key != "video_src" && key != "subtitle" {
					continue
				}
				asset, found := byPath[filepath.Clean(child)]
				if _, ambiguous := ambiguousPaths[filepath.Clean(child)]; ambiguous {
					return invalidRequestf("execution plan path is ambiguous")
				}
				if !found {
					if _, owned := materialized[filepath.Clean(child)]; owned {
						continue
					}
					return invalidRequestf("execution plan references an unregistered file")
				}
				path, ok := prepared[asset.ID]
				if !ok {
					path, err = s.prepareInputAsset(ctx, task.ID, asset)
					if err != nil {
						return err
					}
					prepared[asset.ID] = path
				}
				object[key] = path
			}
		}
		return nil
	}
	if err := rewrite(params); err != nil {
		return nil, err
	}
	// 逐句克隆：把已暂存的声源资产投影成适配器的 voice_path；ID 本身仍保留
	// 在 Core 参数中，由调度层在 wire 投影时剥离。
	if sourceID, ok := params["voice_source_asset_id"].(string); ok && sourceID != "" {
		if path, ok := prepared[domain.ID(sourceID)]; ok {
			params["voice_path"] = path
		}
	}
	return params, nil
}

func (s *Service) materializeFrozenSubtitle(
	ctx context.Context,
	task domain.Task,
	revisionID string,
	core map[string]any,
	digestKey string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	revision, err := s.store.GetSubtitleRevision(ctx, revisionID)
	if err != nil {
		return "", mapReference(err, "subtitle revision")
	}
	track, err := s.store.GetSubtitleTrack(ctx, revision.TrackID)
	if err != nil {
		return "", mapReference(err, "subtitle track")
	}
	if track.ProjectID != task.ProjectID {
		return "", invalidRequestf("subtitle revision belongs to another project")
	}
	text := renderSRT(revision.Segments)
	expected, _ := core[digestKey].(string)
	digest := sha256.Sum256([]byte(text))
	if expected == "" || expected != hex.EncodeToString(digest[:]) {
		return "", changedAssetError()
	}
	directory := filepath.Join(s.workDir(), task.ID, "inputs")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create frozen staged input directory: %w", err)
	}
	if err := rejectSymlinkPath(s.workDir(), directory); err != nil {
		return "", err
	}
	revisionName := sha256.Sum256([]byte(revision.ID))
	destination := filepath.Join(directory,
		"subtitles-"+hex.EncodeToString(revisionName[:8])+".srt")
	if _, err := os.Lstat(destination); err == nil {
		valid := rejectSymlinkPath(directory, destination) == nil
		if valid {
			digest, hashErr := hashFile(destination)
			valid = hashErr == nil && digest == expected
		}
		if valid {
			return destination, nil
		}
		if err := os.Remove(destination); err != nil {
			return "", fmt.Errorf("remove corrupt staged input: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	temporary, err := os.CreateTemp(directory, ".subtitle-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create frozen subtitle: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	_, writeErr := temporary.WriteString(text)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if err := firstError(writeErr, syncErr, closeErr); err != nil {
		return "", fmt.Errorf("write frozen subtitle: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return "", fmt.Errorf("commit frozen subtitle: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return "", err
	}
	return destination, ctx.Err()
}

func (s *Service) prepareInputAsset(ctx context.Context, taskID domain.ID, asset domain.Asset) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if asset.SHA256 == "" || asset.Status != domain.AssetStatusReady {
		return "", changedAssetError()
	}
	if !safePathSegment(taskID) || !safePathSegment(asset.ID) {
		return "", invalidRequestf("invalid execution input identity")
	}
	directory := filepath.Join(s.workDir(), taskID, "inputs")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create frozen input directory: %w", err)
	}
	if err := rejectSymlinkPath(s.workDir(), directory); err != nil {
		return "", err
	}
	destination := filepath.Join(directory, asset.ID+filepath.Ext(asset.Path))
	frozen := asset
	frozen.Path = destination
	if _, err := os.Lstat(destination); err == nil {
		valid := rejectSymlinkPath(directory, destination) == nil &&
			verifyAssetIdentity(frozen) == nil
		if valid {
			return destination, nil
		}
		if err := os.Remove(destination); err != nil {
			return "", fmt.Errorf("remove corrupt frozen input: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	_, digest, err := copyWithDigest(asset.Path, destination)
	if err != nil {
		return "", err
	}
	if digest != asset.SHA256 {
		if removeErr := os.Remove(destination); removeErr != nil {
			return "", errors.Join(changedAssetError(), removeErr)
		}
		return "", changedAssetError()
	}
	if err := verifyAssetIdentity(frozen); err != nil {
		return "", err
	}
	return destination, ctx.Err()
}
