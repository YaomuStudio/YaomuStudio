package service

// 磁盘空间预检与准入（docs/architecture.md §7、FR-SET-22）。
//
// 预检把“能力是否就绪”扩展到“分区能否容纳本次生产”：低于健康线只警告并
// 引导清理；按输入估算的必需空间超过可用空间时视为永久性不足，返回
// INSUFFICIENT_DISK blocker，创建入口据此返回 507，而不是泛化为
// PREFLIGHT_STALE。

import (
	"context"
	"math"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

const (
	// defaultDiskHealthBytes 是应用数据分区健康线：剩余 ≥ 30GiB（FR-SET-22）。
	defaultDiskHealthBytes int64 = 30 << 30
	// diskCacheFactor 把输入大小折算为缓存/中间产物估算：需求 = 输入 ×(1+2)。
	diskCacheFactor int64 = 2
)

// assetSizer 是空间估算所需的资产读取子集；*store.Store 与 *store.Tx 都实现。
type assetSizer interface {
	GetAsset(ctx context.Context, id domain.ID) (domain.Asset, error)
}

// diskHealthBytes 返回健康线；Options 注入优先。
func (s *Service) diskHealthBytes() int64 {
	if s.opts.DiskHealthBytes > 0 {
		return s.opts.DiskHealthBytes
	}
	return defaultDiskHealthBytes
}

// requiredDiskBytes 估算一次生产需要的空间：输入大小 + 缓存/中间产物。
// 输入未知（0）时不做估算，避免把“未知”当成“充足”或“不足”。
func requiredDiskBytes(inputBytes int64) int64 {
	if inputBytes <= 0 {
		return 0
	}
	if inputBytes > math.MaxInt64/(diskCacheFactor+1) {
		return math.MaxInt64
	}
	return inputBytes * (diskCacheFactor + 1)
}

// assetBytesByID 汇总输入资产的大小；缺失/不可读的引用按 0 计，
// 空间判定不替代输入校验，也不把引用错误伪装成空间不足。
func assetBytesByID(ctx context.Context, q assetSizer, ids []string) int64 {
	var total int64
	for _, id := range ids {
		if id == "" {
			continue
		}
		asset, err := q.GetAsset(ctx, id)
		if err != nil {
			continue
		}
		if asset.SizeBytes > 0 {
			total += asset.SizeBytes
		}
	}
	return total
}

// diskPreflightIssues 评估数据分区能否容纳本次运行。
func (s *Service) diskPreflightIssues(ctx context.Context, q assetSizer, in CreateWorkflowRunInput) []domain.PreflightIssue {
	required := requiredDiskBytes(assetBytesByID(ctx, q, in.InputRevisionIDs))
	return s.diskIssues("data", s.dataDir(), required)
}

// diskIssues 按分区可用空间给出 warning/blocker。
//
// 判定顺序：需求超过可用 → blocker INSUFFICIENT_DISK；否则低于健康线 →
// warning INSUFFICIENT_DISK（引导打开存储设置/清理）。探测失败只告警，
// 不把探测错误伪装成空间不足。
func (s *Service) diskIssues(scope, path string, requiredBytes int64) []domain.PreflightIssue {
	health := s.diskHealthBytes()
	space, err := s.opts.Statfs(path)
	if err != nil {
		if s.log != nil {
			s.log.Warn("service", "preflight.disk_probe_failed", map[string]any{
				"scope": scope,
				"error": err.Error(),
			})
		}
		return []domain.PreflightIssue{{
			Severity:   domain.PreflightWarning,
			Code:       "DISK_PROBE_FAILED",
			MessageKey: "preflight.disk_probe_failed",
			ActionIDs:  []domain.ID{"open_storage"},
		}}
	}
	if s.log != nil {
		s.log.Info("service", "preflight.disk_checked", map[string]any{
			"scope":           scope,
			"total_bytes":     space.TotalBytes,
			"available_bytes": space.AvailableBytes,
			"required_bytes":  requiredBytes,
			"health_bytes":    health,
		})
	}
	actions := []domain.ID{"open_storage", "cleanup_storage"}
	if requiredBytes > space.AvailableBytes {
		if s.log != nil {
			s.log.Warn("service", "preflight.disk_insufficient", map[string]any{
				"scope":           scope,
				"available_bytes": space.AvailableBytes,
				"required_bytes":  requiredBytes,
			})
		}
		return []domain.PreflightIssue{{
			Severity:   domain.PreflightBlocker,
			Code:       string(domain.ErrorCodeInsufficientDisk),
			MessageKey: "preflight.insufficient_disk",
			ActionIDs:  actions,
		}}
	}
	if space.AvailableBytes < health {
		if s.log != nil {
			s.log.Warn("service", "preflight.disk_low", map[string]any{
				"scope":           scope,
				"available_bytes": space.AvailableBytes,
				"health_bytes":    health,
			})
		}
		return []domain.PreflightIssue{{
			Severity:   domain.PreflightWarning,
			Code:       string(domain.ErrorCodeInsufficientDisk),
			MessageKey: "preflight.low_disk_space",
			ActionIDs:  actions,
		}}
	}
	return nil
}

// hasBlockerCode 报告预检结果中是否存在指定原因码的 blocker。
func hasBlockerCode(result domain.PreflightResult, code domain.ErrorCode) bool {
	for _, issue := range result.Issues {
		if issue.Severity == domain.PreflightBlocker && issue.Code == string(code) {
			return true
		}
	}
	return false
}

// errInsufficientDisk 是创建/导出被永久性空间不足阻断时的 507 错误。
func errInsufficientDisk() error {
	return &domain.Error{
		Code:       domain.ErrorCodeInsufficientDisk,
		MessageKey: "error.insufficient_disk",
		ActionIDs:  []domain.ID{"open_storage", "cleanup_storage"},
	}
}

// checkDiskSpace 在写入前确认目标分区能容纳估算需求；不足返回
// INSUFFICIENT_DISK。探测失败不阻断操作，由实际写入错误兜底。
func (s *Service) checkDiskSpace(scope, path string, requiredBytes int64) error {
	space, err := s.opts.Statfs(path)
	if err != nil {
		if s.log != nil {
			s.log.Warn("service", "disk.probe_failed", map[string]any{
				"scope": scope,
				"error": err.Error(),
			})
		}
		return nil
	}
	if s.log != nil {
		s.log.Info("service", "disk.checked", map[string]any{
			"scope":           scope,
			"total_bytes":     space.TotalBytes,
			"available_bytes": space.AvailableBytes,
			"required_bytes":  requiredBytes,
		})
	}
	if requiredBytes > space.AvailableBytes {
		if s.log != nil {
			s.log.Warn("service", "disk.insufficient", map[string]any{
				"scope":           scope,
				"available_bytes": space.AvailableBytes,
				"required_bytes":  requiredBytes,
			})
		}
		return errInsufficientDisk()
	}
	return nil
}
