package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

type preparedImport struct {
	id, sourceName, path, source, owner, digest string
	size                                        int64
}

func (s *Service) importReceipt(ctx context.Context, idem Idem) (Response, bool, error) {
	if idem.Key == "" {
		return Response{}, false, nil
	}
	receipt, err := s.store.GetReceipt(ctx, s.opts.WorkspaceID, "assets.import", idem.Key)
	if errors.Is(err, domain.ErrNotFound) {
		return Response{}, false, nil
	}
	if err != nil {
		return Response{}, false, err
	}
	if receipt.RequestDigest != idem.Digest {
		return Response{}, false, domain.ErrIdempotencyConflict
	}
	var envelope receiptEnvelope
	if err := json.Unmarshal(receipt.ResponseJSON, &envelope); err != nil {
		return Response{}, false, fmt.Errorf("decode import receipt: %w", err)
	}
	return Response{Status: envelope.Status, Body: envelope.Body}, true, nil
}

func (s *Service) prepareImport(
	ctx context.Context,
	request AssetImportRequest,
	grant string,
) (preparedImport, *fileIntent, error) {
	if err := ctx.Err(); err != nil {
		return preparedImport{}, nil, err
	}
	before, err := os.Stat(grant)
	if err != nil || !before.Mode().IsRegular() {
		return preparedImport{}, nil, invalidRequestf("file grant is not a readable regular file")
	}
	prepared := preparedImport{
		id: store.NewID("asset"), sourceName: grant, path: grant,
		source: domain.LocationSourceExternalReference, owner: domain.LocationOwnerUser,
		size: before.Size(),
	}
	var intent *fileIntent
	if request.CopyMode {
		prepared.path = filepath.Join(s.mediaDir(), request.ProjectID, prepared.id+filepath.Ext(grant))
		intent, err = s.beginFileIntent(prepared.id, prepared.path, true)
		if err != nil {
			return preparedImport{}, nil, err
		}
		_, prepared.digest, err = copyWithDigest(grant, prepared.path)
		prepared.source = domain.LocationSourceManagedCopy
		prepared.owner = domain.LocationOwnerApp
	} else {
		prepared.digest, err = hashFile(grant)
	}
	if err != nil {
		return preparedImport{}, nil, err
	}
	after, err := os.Stat(grant)
	if err != nil || !sameFileVersion(before, after) {
		return preparedImport{}, nil, changedAssetError()
	}
	if err := ctx.Err(); err != nil {
		return preparedImport{}, nil, err
	}
	return prepared, intent, nil
}

func changedAssetError() error {
	return &domain.Error{
		Code: domain.ErrorCodeRevisionConflict, MessageKey: "asset.content_changed",
		Retryable: false, ActionIDs: []domain.ID{"reimport_asset"},
	}
}

func sameFileVersion(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func verifyAssetIdentity(asset domain.Asset) error {
	info, err := os.Stat(asset.Path)
	if err != nil || !info.Mode().IsRegular() {
		return changedAssetError()
	}
	if asset.Status != "" && asset.Status != domain.AssetStatusReady {
		return changedAssetError()
	}
	// Legacy rows can lack a digest. New imports and results always have one.
	if asset.SHA256 == "" {
		return nil
	}
	if info.Size() != int64(asset.SizeBytes) {
		return changedAssetError()
	}
	digest, err := hashFile(asset.Path)
	if err != nil {
		return err
	}
	after, err := os.Stat(asset.Path)
	if err != nil || !sameFileVersion(info, after) || digest != asset.SHA256 {
		return changedAssetError()
	}
	return nil
}

type verifiedAssetIdentity struct {
	path   string
	digest string
	info   os.FileInfo
}

// Playback Range requests reuse validation only while the registered file
// identity, size and modification time remain unchanged.
func (s *Service) verifyContentIdentity(asset domain.Asset, info os.FileInfo) error {
	s.assetIdentityMu.Lock()
	previous, found := s.assetIdentities[asset.ID]
	s.assetIdentityMu.Unlock()
	if found && previous.path == asset.Path && previous.digest == asset.SHA256 &&
		sameFileVersion(previous.info, info) && asset.Status == domain.AssetStatusReady {
		return nil
	}
	if err := verifyAssetIdentity(asset); err != nil {
		return err
	}
	s.assetIdentityMu.Lock()
	defer s.assetIdentityMu.Unlock()
	if s.assetIdentities == nil {
		s.assetIdentities = map[domain.ID]verifiedAssetIdentity{}
	}
	s.assetIdentities[asset.ID] = verifiedAssetIdentity{path: asset.Path, digest: asset.SHA256, info: info}
	return nil
}
