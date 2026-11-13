package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/system"
)

// MediaProber 读取媒体文件的容器/编码/时长等元数据。
// 实现不得返回原始命令输出；调用方只使用安全错误与可选元数据。
type MediaProber interface {
	Probe(ctx context.Context, path string) (*domain.MediaMeta, error)
}

// AssetImportRequest 是导入请求；file_grants 由 Shell 文件选择器签发，
// Core 不接受未经授权的外部路径（当前授权形式是选择器返回的绝对路径）。
type AssetImportRequest struct {
	ProjectID  string   `json:"project_id"`
	FileGrants []string `json:"file_grants"`
	CopyMode   bool     `json:"copy_mode"`
}

// RelocateAssetRequest 是外部引用重定位请求；file_grant 必须是系统选择器签发的绝对路径。
type RelocateAssetRequest struct {
	FileGrant string `json:"file_grant"`
}

// AssetImportResult 是导入结果；长导入可携带任务 ID（当前同步导入不产生）。
type AssetImportResult struct {
	AssetIDs []domain.ID `json:"asset_ids"`
	TaskID   domain.ID   `json:"task_id,omitempty"`
}

// AssetContent 是受控内容读取所需的最小信息。
type AssetContent struct {
	Path    string
	Name    string
	Size    int64
	ModTime time.Time
}

const maxImportGrants = 128

// ImportAssets 登记用户授权的媒体文件（202）。
func (s *Service) ImportAssets(ctx context.Context, idem Idem, req AssetImportRequest) (Response, error) {
	if req.ProjectID == "" {
		return Response{}, invalidRequestf("project_id required")
	}
	if len(req.FileGrants) == 0 {
		return Response{}, invalidRequestf("file_grants required")
	}
	if len(req.FileGrants) > maxImportGrants {
		return Response{}, invalidRequestf("too many file_grants")
	}
	seen := make(map[string]struct{}, len(req.FileGrants))
	for _, grant := range req.FileGrants {
		if err := validateFileGrant(grant); err != nil {
			return Response{}, err
		}
		if _, ok := seen[grant]; ok {
			return Response{}, invalidRequestf("duplicate file grant")
		}
		seen[grant] = struct{}{}
	}

	// Serialize only imports, leaving other business writes and reads available
	// while a large source is hashed or copied.
	s.importMu.Lock()
	defer s.importMu.Unlock()
	if response, found, err := s.importReceipt(ctx, idem); found || err != nil {
		return response, err
	}
	if _, err := s.store.GetProject(ctx, req.ProjectID); err != nil {
		return Response{}, mapReference(err, "project")
	}
	prepared := make([]preparedImport, 0, len(req.FileGrants))
	intents := []*fileIntent{}
	for _, grant := range req.FileGrants {
		item, intent, err := s.prepareImport(ctx, req, grant)
		if err != nil {
			return Response{}, err
		}
		prepared = append(prepared, item)
		if intent != nil {
			intents = append(intents, intent)
		}
	}

	// 探测在写事务前完成：ffprobe 是外部进程，不占用 SQLite 写锁。
	// 单个文件探测失败只把该资产标 corrupt，不让整批导入失败。
	probes := make(map[string]importProbe, len(prepared))
	unavailable, failed := 0, 0
	for _, item := range prepared {
		outcome := s.probeForImport(ctx, item.sourceName, assetTypeForName(filepath.Base(item.sourceName)))
		probes[item.sourceName] = outcome
		if outcome.unavailable {
			unavailable++
		}
		if outcome.failed {
			failed++
		}
	}
	if unavailable > 0 {
		s.warn("assets.probe_unavailable", map[string]any{"count": unavailable})
	}
	if failed > 0 {
		s.warn("assets.probe_failed", map[string]any{"count": failed})
	}
	var events []domain.DomainEvent
	resp, err := s.write(ctx, "assets.import", idem, 202, func(ctx context.Context, tx *store.Tx) (any, error) {
		if _, err := tx.GetProject(ctx, req.ProjectID); err != nil {
			return nil, mapReference(err, "project")
		}
		now := s.Now()
		result := AssetImportResult{AssetIDs: make([]domain.ID, 0, len(prepared))}
		for _, item := range prepared {
			if err := s.createAssetRow(ctx, tx, req.ProjectID, item.id, item.sourceName,
				item.path, item.source, item.owner, item.size, item.digest,
				probes[item.sourceName], now); err != nil {
				return nil, err
			}
			result.AssetIDs = append(result.AssetIDs, item.id)
		}

		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type: "asset.imported", ProjectID: req.ProjectID,
			Data: map[string]any{"asset_ids": result.AssetIDs, "copy_mode": req.CopyMode},
		}); err != nil {
			return nil, err
		}
		return result, nil
	})
	if err != nil {
		return Response{}, err
	}
	if err := finishFileIntents(intents); err != nil && s.log != nil {
		s.log.Warn("asset", "asset.commit_journal_retained", map[string]any{"error": err.Error()})
	}
	s.publish(events...)
	return resp, nil
}

func (s *Service) createAssetRow(
	ctx context.Context,
	tx *store.Tx,
	projectID, assetID, sourceName, locationPath, source, owner string,
	size int64,
	digest string,
	probe importProbe,
	now time.Time,
) error {
	displayName := filepath.Base(sourceName)
	status := probe.status
	if status == "" {
		status = domain.AssetStatusReady
	}
	assetType := assetTypeForName(displayName)
	role := domain.AssetRole("")
	switch assetType {
	case domain.AssetTypeVideo, domain.AssetTypeAudio:
		role = domain.AssetRoleOriginal
	case domain.AssetTypeSubtitle:
		role = domain.AssetRoleSourceSubtitle
	}
	asset := domain.Asset{
		ID:         assetID,
		ProjectID:  projectID,
		Type:       assetType,
		Role:       role,
		Name:       displayName,
		Path:       locationPath,
		SizeBytes:  size,
		SHA256:     digest,
		Meta:       probe.meta,
		Status:     status,
		ImportedAt: now,
	}
	if err := tx.CreateAsset(ctx, asset); err != nil {
		return err
	}
	return tx.AddAssetLocation(ctx, domain.AssetLocation{
		ID:        store.NewID("loc"),
		AssetID:   assetID,
		Path:      locationPath,
		Source:    source,
		Owner:     owner,
		Available: true,
		CreatedAt: now,
	})
}

// importProbe 是单个导入文件的探测结果。
type importProbe struct {
	meta   *domain.MediaMeta
	status domain.AssetStatus
	// unavailable 表示探测工具不可用（文件仍按 ready 登记，Meta 为空）。
	unavailable bool
	// failed 表示该文件探测失败（按 corrupt 登记，Meta 为空）。
	failed bool
}

// probeForImport 只探测 video/audio；其他类型不调用 ffprobe。
// 探测失败不返回错误，由调用方按状态登记并汇总告警。
func (s *Service) probeForImport(ctx context.Context, path string, assetType domain.AssetType) importProbe {
	ready := importProbe{status: domain.AssetStatusReady}
	if assetType != domain.AssetTypeVideo && assetType != domain.AssetTypeAudio {
		return ready
	}
	if s.opts.MediaProber == nil {
		ready.unavailable = true
		return ready
	}
	meta, err := s.opts.MediaProber.Probe(ctx, path)
	if err != nil {
		if errors.Is(err, system.ErrProberUnavailable) {
			ready.unavailable = true
			return ready
		}
		return importProbe{status: domain.AssetStatusCorrupt, failed: true}
	}
	return importProbe{meta: meta, status: domain.AssetStatusReady}
}

// RelocateAsset 把缺失的外部引用指向同内容的新路径并恢复 ready。
//
// 只允许 external_reference + missing；新文件摘要必须等于导入时的 SHA-256，
// 避免把不同内容静默接到既有轨道/任务。copy/generated 资产要求重新导入。
func (s *Service) RelocateAsset(ctx context.Context, idem Idem, assetID string, req RelocateAssetRequest) (Response, error) {
	if assetID == "" {
		return Response{}, invalidRequestf("asset id required")
	}
	grant := strings.TrimSpace(req.FileGrant)
	if err := validateFileGrant(grant); err != nil {
		return Response{}, err
	}

	// 事务外做文件读取与解析：哈希/探测是重 IO，不占用写事务。
	// 这里只读取不可变字段（类型/摘要），可变状态在事务内复核。
	asset, err := s.store.GetAsset(ctx, assetID)
	if err != nil {
		return Response{}, err
	}
	info, err := os.Stat(grant)
	if err != nil || !info.Mode().IsRegular() {
		return Response{}, invalidRequestf("file grant is not a readable regular file")
	}
	if strings.TrimSpace(asset.SHA256) == "" {
		return Response{}, invalidRequestf("asset %s has no recorded digest; re-import it instead", assetID)
	}
	digest, err := hashFile(grant)
	if err != nil {
		return Response{}, invalidRequestf("file grant is not readable")
	}
	if !strings.EqualFold(digest, asset.SHA256) {
		return Response{}, invalidRequestf("file content does not match asset %s", assetID)
	}

	meta := asset.Meta
	if asset.Type == domain.AssetTypeVideo || asset.Type == domain.AssetTypeAudio {
		if s.opts.MediaProber == nil {
			s.warn("assets.relocate_probe_unavailable", map[string]any{"asset_type": string(asset.Type)})
		} else {
			probed, probeErr := s.opts.MediaProber.Probe(ctx, grant)
			switch {
			case probeErr == nil:
				meta = probed
			case errors.Is(probeErr, system.ErrProberUnavailable):
				// 工具不可用不是内容问题：同摘要文件沿用原 meta，不伪造新值。
				s.warn("assets.relocate_probe_unavailable", map[string]any{"asset_type": string(asset.Type)})
			default:
				return Response{}, invalidRequestf("media probe failed for the new file")
			}
		}
	}

	var events []domain.DomainEvent
	resp, err := s.write(ctx, "assets.relocate", idem, 200, func(ctx context.Context, tx *store.Tx) (any, error) {
		current, err := tx.GetAsset(ctx, assetID)
		if err != nil {
			return nil, err
		}
		if current.Status != domain.AssetStatusMissing {
			return nil, invalidRequestf(
				"asset %s is %s; only missing external references can be relocated", assetID, current.Status)
		}
		locations, err := tx.ListAssetLocations(ctx, assetID)
		if err != nil {
			return nil, err
		}
		var target *domain.AssetLocation
		for i := range locations {
			if locations[i].Source == domain.LocationSourceExternalReference {
				target = &locations[i]
				break
			}
		}
		if target == nil {
			return nil, invalidRequestf(
				"asset %s has no external reference; re-import it instead", assetID)
		}
		if err := tx.RelocateAssetLocation(ctx, target.ID, grant); err != nil {
			return nil, err
		}
		if err := tx.SetAssetMeta(ctx, assetID, meta, info.Size()); err != nil {
			return nil, err
		}
		if err := tx.SetAssetStatus(ctx, assetID, domain.AssetStatusReady); err != nil {
			return nil, err
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type:      "asset.relocated",
			ProjectID: current.ProjectID,
			Data:      map[string]any{"asset_id": assetID},
		}); err != nil {
			return nil, err
		}
		return tx.GetAsset(ctx, assetID)
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	return resp, nil
}

// AssetContentFor 返回资产可读取的位置；只认已登记且存在的文件。
func (s *Service) AssetContentFor(ctx context.Context, id string) (AssetContent, error) {
	if id == "" {
		return AssetContent{}, invalidRequestf("asset id required")
	}
	asset, err := s.store.GetAsset(ctx, id)
	if err != nil {
		return AssetContent{}, err
	}
	locations, err := s.store.ListAssetLocations(ctx, id)
	if err != nil {
		return AssetContent{}, err
	}
	var identityErr error
	for _, location := range locations {
		// available 只是上次刷新的事实快照；这里仍以实际 stat 为准，
		// 让文件恢复后无需等待下一次项目刷新即可读取。
		info, statErr := os.Stat(location.Path)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		candidate := asset
		candidate.Path = location.Path
		if err := s.verifyContentIdentity(candidate, info); err != nil {
			identityErr = err
			continue
		}
		return AssetContent{
			Path:    location.Path,
			Name:    asset.Name,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}, nil
	}
	if identityErr != nil {
		return AssetContent{}, identityErr
	}
	return AssetContent{}, notFoundf("asset %s has no readable file", id)
}

// validateFileGrant 校验授权路径：绝对路径、无控制字符、长度有界。
func validateFileGrant(grant string) error {
	if grant == "" {
		return invalidRequestf("empty file grant")
	}
	if len(grant) > 4096 {
		return invalidRequestf("file grant too long")
	}
	if strings.ContainsRune(grant, 0) || strings.ContainsAny(grant, "\r\n") {
		return invalidRequestf("file grant contains control characters")
	}
	if !filepath.IsAbs(grant) {
		return invalidRequestf("file grant must be an absolute path")
	}
	clean := filepath.Clean(grant)
	if clean == string(filepath.Separator) {
		return invalidRequestf("file grant cannot be a filesystem root")
	}
	return nil
}

// assetTypeForName 按扩展名归类；无法判断时归为 other（不靠内容嗅探伪造元数据）。
func assetTypeForName(name string) domain.AssetType {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4", ".mkv", ".mov", ".avi", ".webm", ".flv", ".m4v", ".mpeg", ".mpg":
		return domain.AssetTypeVideo
	case ".wav", ".mp3", ".flac", ".aac", ".m4a", ".ogg", ".opus":
		return domain.AssetTypeAudio
	case ".srt", ".ass", ".vtt", ".ssa":
		return domain.AssetTypeSubtitle
	case ".png", ".jpg", ".jpeg", ".webp", ".bmp", ".tiff":
		return domain.AssetTypeImage
	case ".zip", ".7z", ".rar", ".tar", ".gz", ".tgz":
		return domain.AssetTypeArchive
	default:
		return domain.AssetTypeOther
	}
}

// hashFile 流式计算文件摘要（sha256 hex）。
func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open asset: %w", err)
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("hash asset: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// copyWithDigest 把用户授权文件复制进应用拥有的媒体目录，返回目标路径与摘要。
// 先写同目录临时文件再 rename，避免把半成品登记为可用资产。
func copyWithDigest(source, destination string) (string, string, error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return "", "", fmt.Errorf("create media dir: %w", err)
	}
	src, err := os.Open(source)
	if err != nil {
		return "", "", invalidRequestf("file grant is not readable: %v", err)
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".import-*")
	if err != nil {
		return "", "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), src); err != nil {
		cleanup()
		return "", "", fmt.Errorf("copy asset: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return "", "", fmt.Errorf("sync asset: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", "", fmt.Errorf("close asset temp: %w", err)
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		_ = os.Remove(tmpPath)
		return "", "", fmt.Errorf("commit asset copy: %w", err)
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return "", "", err
	}
	return destination, hex.EncodeToString(hasher.Sum(nil)), nil
}
