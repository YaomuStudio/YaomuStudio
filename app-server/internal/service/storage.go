package service

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// storageUsageTTL 是用量缓存的存活时间：避免每次读取都全盘扫描，
// 同时把界面数字的陈旧窗口限制在一分钟内；清理操作会主动失效缓存。
const storageUsageTTL = 60 * time.Second

// StorageCleanupRequest 是按类清理输入。
type StorageCleanupRequest struct {
	Category domain.StorageCategory `json:"category"`
}

// StorageUsage 返回应用数据所在分区四类占用（FR-SET-42）。
//
// 首次读取（或缓存过期/失效后）重新扫描四类目录；TTL 内返回缓存值。
// 扫描数字写入本地诊断日志（不含路径与正文）。
func (s *Service) StorageUsage(_ context.Context) (domain.StorageUsage, error) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	now := s.Now()
	if s.usageCached && now.Sub(s.usageAt) < storageUsageTTL {
		return s.usageValue, nil
	}
	usage, err := s.scanStorageUsage()
	if err != nil {
		return domain.StorageUsage{}, err
	}
	s.usageValue, s.usageAt, s.usageCached = usage, now, true
	if s.log != nil {
		s.log.Info("service", "storage.usage_scanned", map[string]any{
			"models_bytes":  usage.ModelsBytes,
			"media_bytes":   usage.MediaBytes,
			"cache_bytes":   usage.CacheBytes,
			"exports_bytes": usage.ExportsBytes,
		})
	}
	return usage, nil
}

// InvalidateStorageUsage 丢弃用量缓存；清理等改变磁盘内容的操作后调用。
func (s *Service) InvalidateStorageUsage() {
	s.usageMu.Lock()
	s.usageCached = false
	s.usageMu.Unlock()
}

// scanStorageUsage 统计四类目录的当前占用。
func (s *Service) scanStorageUsage() (domain.StorageUsage, error) {
	models, err := dirUsage(s.modelsDir())
	if err != nil {
		return domain.StorageUsage{}, err
	}
	media, err := dirUsage(s.mediaDir())
	if err != nil {
		return domain.StorageUsage{}, err
	}
	cache, err := dirUsage(s.cacheDir())
	if err != nil {
		return domain.StorageUsage{}, err
	}
	exports, err := dirUsage(s.exportsDir())
	if err != nil {
		return domain.StorageUsage{}, err
	}
	return domain.StorageUsage{
		ModelsBytes:  models,
		MediaBytes:   media,
		CacheBytes:   cache,
		ExportsBytes: exports,
	}, nil
}

// dirUsage 统计目录占用；目录不存在视为 0。
func dirUsage(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	return total, nil
}

// CleanupStorage 按类清理应用拥有、无引用、无活跃 writer 的文件（200）。
//
// 存在活跃任务时拒绝（409）：不删除可能仍被执行的输入/输出。安装清理与
// 其他安装生命周期决策共用同一排他边界，避免删除与安装 worker 互踩。
func (s *Service) CleanupStorage(ctx context.Context, idem Idem, req StorageCleanupRequest) (Response, error) {
	if !req.Category.Valid() {
		return Response{}, invalidRequestf("invalid storage category %q", req.Category)
	}
	s.installationLifecycleMu.Lock()
	defer s.installationLifecycleMu.Unlock()
	var events []domain.DomainEvent
	resp, err := s.write(ctx, "storage.cleanup", idem, 200, func(ctx context.Context, tx *store.Tx) (any, error) {
		active, err := tx.CountActiveTasks(ctx, "")
		if err != nil {
			return nil, err
		}
		if active > 0 {
			return nil, errResourceBusy("active tasks still reference files")
		}
		if req.Category == domain.StorageCategoryModels && s.ActiveInstallationWork() {
			return nil, errResourceBusy("installation workers still own model files")
		}
		freed, err := s.cleanupCategory(ctx, tx, req.Category)
		if err != nil {
			return nil, err
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type: "storage.cleaned",
			Data: map[string]any{"category": req.Category, "freed_bytes": freed},
		}); err != nil {
			return nil, err
		}
		return domain.StorageCleanupResult{Category: req.Category, FreedBytes: freed}, nil
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	s.InvalidateStorageUsage()
	return resp, nil
}

// cleanupCategory 只删除应用拥有且无引用的文件，返回实际释放字节数。
func (s *Service) cleanupCategory(ctx context.Context, tx *store.Tx, category domain.StorageCategory) (int64, error) {
	switch category {
	case domain.StorageCategoryCache:
		return removeUnreferenced(s.cacheDir(), nil)
	case domain.StorageCategoryMedia:
		locations, err := tx.ListAllAssetLocations(ctx)
		if err != nil {
			return 0, err
		}
		keep := make(map[string]struct{}, len(locations))
		for _, location := range locations {
			if location.Source == domain.LocationSourceManagedCopy {
				keep[filepath.Clean(location.Path)] = struct{}{}
			}
		}
		return removeUnreferenced(s.mediaDir(), keep)
	case domain.StorageCategoryExports:
		records, err := tx.ListExports(ctx, "")
		if err != nil {
			return 0, err
		}
		keep := make(map[string]struct{}, len(records))
		for _, record := range records {
			if record.OutputPath != "" {
				keep[filepath.Clean(record.OutputPath)] = struct{}{}
			}
		}
		return removeUnreferenced(s.exportsDir(), keep)
	case domain.StorageCategoryModels:
		installs, err := tx.ListInstallations(ctx)
		if err != nil {
			return 0, err
		}
		keepSlots := make(map[string]struct{})
		keepKeys := make(map[string]struct{})
		for _, inst := range installs {
			// 已完成或仍在写入的实体都保留；旧的失败/取消历史可按原语义清理。
			if inst.Phase == domain.InstallationPhaseDone || !inst.Phase.Terminal() {
				if inst.StorageKey != "" {
					keepKeys[inst.StorageKey] = struct{}{}
				} else {
					keepSlots[string(inst.Slot)] = struct{}{}
				}
			}
		}
		return removeUnreferencedModels(s.modelsDir(), keepSlots, keepKeys)
	default:
		return 0, invalidRequestf("invalid storage category %q", category)
	}
}

// removeUnreferenced 删除 keep 之外的文件；目录保留。错误不阻断其余删除。
func removeUnreferenced(root string, keep map[string]struct{}) (int64, error) {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var freed int64
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		clean := filepath.Clean(path)
		if keep != nil {
			if _, ok := keep[clean]; ok {
				return nil
			}
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		if removeErr := os.Remove(path); removeErr == nil {
			freed += info.Size()
		}
		return nil
	})
	return freed, nil
}

// removeUnreferencedModels 清理模型目录：保留旧版槽位目录与共享发布实体根。
//
// 共享发布命名空间 `models/releases` 不是槽位目录，永不整体删除；其中每个
// 物理实体按 storage_key 引用保留。旧版 `models/<slot>` 目录按原布局判断。
// 用量本来只遍历一次单物理树，不因共享引用重复计数。
func removeUnreferencedModels(root string, keepSlots, keepKeys map[string]struct{}) (int64, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var freed int64
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if entry.Name() == releasesDirName {
			usage, err := removeUnreferencedReleases(dir, keepKeys)
			if err != nil {
				continue
			}
			freed += usage
			continue
		}
		if _, ok := keepSlots[entry.Name()]; ok {
			continue
		}
		usage, _ := dirUsage(dir)
		if err := os.RemoveAll(dir); err == nil {
			freed += usage
		}
	}
	return freed, nil
}

// removeUnreferencedReleases 只删除没有实体的共享发布根，保留命名空间本身。
func removeUnreferencedReleases(root string, keepKeys map[string]struct{}) (int64, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var freed int64
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, ok := keepKeys[entry.Name()]; ok {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		usage, _ := dirUsage(dir)
		if err := os.RemoveAll(dir); err == nil {
			freed += usage
		}
	}
	return freed, nil
}

// ensureStorageDirs 创建四类目录（启动时调用；失败不阻断启动）。
func (s *Service) EnsureStorageDirs() error {
	for _, dir := range []string{s.modelsDir(), s.mediaDir(), s.cacheDir(), s.exportsDir(), s.workDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}
