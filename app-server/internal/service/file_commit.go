package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"os"
	"path/filepath"
)

// fileIntent survives the filesystem/SQLite commit window. User destinations
// are retained as explicit orphans; recovery never guesses ownership there.
type fileIntent struct {
	Version int    `json:"version"`
	AssetID string `json:"asset_id"`
	Path    string `json:"path"`
	Managed bool   `json:"managed"`
	journal string
}

func (s *Service) beginFileIntent(assetID, destination string, managed bool) (*fileIntent, error) {
	if !safePathSegment(assetID) || !filepath.IsAbs(destination) {
		return nil, invalidRequestf("invalid file commit intent")
	}
	directory := filepath.Join(s.dataDir(), "file-commits")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create file commit journal: %w", err)
	}
	intent := &fileIntent{
		Version: 1, AssetID: assetID, Path: destination, Managed: managed,
		journal: filepath.Join(directory, assetID+".json"),
	}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return nil, fmt.Errorf("encode file commit intent: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".intent-*")
	if err != nil {
		return nil, fmt.Errorf("create file commit intent: %w", err)
	}
	defer os.Remove(temporary.Name())
	_, writeErr := temporary.Write(encoded)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if err := firstError(writeErr, syncErr, closeErr); err != nil {
		return nil, fmt.Errorf("write file commit intent: %w", err)
	}
	if err := os.Rename(temporary.Name(), intent.journal); err != nil {
		return nil, fmt.Errorf("publish file commit intent: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return nil, err
	}
	return intent, nil
}

func finishFileIntents(intents []*fileIntent) error {
	var result error
	for _, intent := range intents {
		if intent == nil {
			continue
		}
		if err := os.Remove(intent.journal); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, fmt.Errorf("remove file commit intent: %w", err))
		}
	}
	return result
}

// RecoverArtifactFiles runs before dispatch. It reconciles durable intents and
// marks missing or altered committed files corrupt before downstream can use them.
func (s *Service) RecoverArtifactFiles(ctx context.Context) error {
	root := filepath.Join(s.dataDir(), "file-commits")
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read file commit journal: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		journal := filepath.Join(root, entry.Name())
		if err := s.recoverFileIntent(ctx, journal); err != nil {
			return err
		}
	}
	assets, err := s.store.AssetsForIntegrityCheck(ctx)
	if err != nil {
		return err
	}
	for _, asset := range assets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if asset.Status != domain.AssetStatusReady {
			continue
		}
		if err := verifyAssetIdentity(asset); err != nil {
			if err := s.store.SetAssetStatus(ctx, asset.ID, domain.AssetStatusCorrupt); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) recoverFileIntent(ctx context.Context, journal string) error {
	info, err := os.Lstat(journal)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 16*1024 {
		return fmt.Errorf("invalid file commit journal")
	}
	encoded, err := os.ReadFile(journal)
	if err != nil {
		return err
	}
	var intent fileIntent
	if err := json.Unmarshal(encoded, &intent); err != nil {
		return fmt.Errorf("decode file commit journal: %w", err)
	}
	if intent.Version != 1 || !safePathSegment(intent.AssetID) || !filepath.IsAbs(intent.Path) {
		return fmt.Errorf("invalid file commit journal identity")
	}
	asset, err := s.store.GetAsset(ctx, intent.AssetID)
	if err == nil {
		// SQLite is the authority, including the registered path. Never trust a
		// journal to replace an asset's location or its content identity.
		if err := verifyAssetIdentity(asset); err != nil {
			if err := s.store.SetAssetStatus(ctx, asset.ID, domain.AssetStatusCorrupt); err != nil {
				return err
			}
		}
		return os.Remove(journal)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if !intent.Managed {
		// Retain user-directory files and the manifest for explicit recovery.
		return os.Rename(journal, journal+".orphan")
	}
	expectedName := intent.AssetID + filepath.Ext(intent.Path)
	if !pathWithin(s.mediaDir(), intent.Path) || filepath.Base(intent.Path) != expectedName {
		return fmt.Errorf("managed file commit is outside its asset location")
	}
	if _, err := os.Lstat(intent.Path); errors.Is(err, os.ErrNotExist) {
		return os.Remove(journal)
	} else if err != nil {
		return err
	}
	if err := rejectSymlinkPath(s.mediaDir(), intent.Path); err != nil {
		return fmt.Errorf("unsafe orphan file commit: %w", err)
	}
	if err := os.Remove(intent.Path); err != nil {
		return fmt.Errorf("remove uncommitted managed file: %w", err)
	}
	return os.Remove(journal)
}
