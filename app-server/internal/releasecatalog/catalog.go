// Package releasecatalog 加载产品侧准入的发布目录（SHA-256 锁）。
//
// 这不是签名 ReleaseManifest：清单由产品本地锁定 CDN 字节摘要生成，
// 用于接通安装下载路径；正式签名/密钥流水线落地前不得对外宣称已签名准入。
package releasecatalog

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/downloader"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

//go:embed catalog.json
var embeddedCatalog []byte

const (
	kindProductCatalog = "yaomu_product_release_catalog"
	schemaVersion      = 1
)

type fileDoc struct {
	SchemaVersion        int          `json:"schema_version"`
	Kind                 string       `json:"kind"`
	AdmittedAt           string       `json:"admitted_at,omitempty"`
	DistributionPrefixes []string     `json:"distribution_prefixes"`
	Note                 string       `json:"note,omitempty"`
	Releases             []releaseDoc `json:"releases"`
}

type releaseDoc struct {
	Slots            []string              `json:"slots"`
	Release          domain.ReleaseRef     `json:"release"`
	DownloadBytes    int64                 `json:"download_bytes"`
	CacheReuseBytes  int64                 `json:"cache_reuse_bytes"`
	InstallBytes     int64                 `json:"install_bytes"`
	PeakStagingBytes int64                 `json:"peak_staging_bytes"`
	Permissions      []string              `json:"permissions"`
	License          domain.LicenseSummary `json:"license"`
	RequiresRestart  bool                  `json:"requires_restart"`
	FlashCLIRef      string                `json:"flashcli_ref,omitempty"`
	Files            []downloader.File     `json:"files"`
}

// Catalog 是按槽位解析的产品发布目录，并携带批准的分发源前缀。
type Catalog struct {
	bySlot   map[domain.CapabilitySlotKey]service.ReleasePlan
	Prefixes []string
	Note     string
}

// Resolve 实现 service.ReleaseCatalog。
func (c Catalog) Resolve(_ context.Context, slot domain.CapabilitySlotKey) (service.ReleasePlan, error) {
	if c.bySlot == nil {
		return service.ReleasePlan{}, &domain.Error{
			Code:       domain.ErrorCodeModelNotInstalled,
			MessageKey: "installation.release_not_configured",
			Retryable:  false,
			ActionIDs:  []domain.ID{"open_model_setup"},
		}
	}
	plan, ok := c.bySlot[slot]
	if !ok {
		return service.ReleasePlan{}, &domain.Error{
			Code:       domain.ErrorCodeModelNotInstalled,
			MessageKey: "installation.release_not_configured",
			Retryable:  false,
			ActionIDs:  []domain.ID{"open_model_setup"},
		}
	}
	return plan, nil
}

// FlashCLIRefFor 返回与已安装发布匹配的已准入引用。
func (c Catalog) FlashCLIRefFor(slot domain.CapabilitySlotKey, release domain.ReleaseRef) string {
	plan, ok := c.bySlot[slot]
	if !ok || plan.Release.ReleaseID != release.ReleaseID || plan.Release.Version != release.Version {
		return ""
	}
	return plan.FlashCLIRef
}

// List 返回槽位准入的引擎目录；同一槽位当前只准入一个发布。
func (c Catalog) List(_ context.Context, slot domain.CapabilitySlotKey) ([]service.ReleasePlan, error) {
	plan, ok := c.bySlot[slot]
	if !ok {
		return nil, nil
	}
	return []service.ReleasePlan{plan}, nil
}

// LoadEmbedded 加载内嵌的产品 interim 目录。
func LoadEmbedded() (Catalog, error) {
	return Parse(embeddedCatalog)
}

// LoadFile 从磁盘加载目录；空路径回退到内嵌目录。
func LoadFile(path string) (Catalog, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return LoadEmbedded()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Catalog{}, fmt.Errorf("release catalog: read %s: %w", path, err)
	}
	return Parse(raw)
}

// Parse 解析目录 JSON。
func Parse(raw []byte) (Catalog, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return Catalog{}, fmt.Errorf("release catalog: empty document")
	}
	var doc fileDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Catalog{}, fmt.Errorf("release catalog: decode: %w", err)
	}
	if doc.SchemaVersion != schemaVersion {
		return Catalog{}, fmt.Errorf("release catalog: unsupported schema_version %d", doc.SchemaVersion)
	}
	if doc.Kind != kindProductCatalog {
		return Catalog{}, fmt.Errorf("release catalog: unexpected kind %q", doc.Kind)
	}
	if len(doc.Releases) == 0 {
		return Catalog{}, fmt.Errorf("release catalog: no releases")
	}

	bySlot := make(map[domain.CapabilitySlotKey]service.ReleasePlan)
	// declarations 记录同一完整 ReleaseRef 的已声明范围/文件清单；
	// 同身份的矛盾声明必须拒绝，不能静默合并成更宽的范围。
	declarations := make(map[string]service.ReleasePlan)
	for i, rel := range doc.Releases {
		if len(rel.Slots) == 0 {
			return Catalog{}, fmt.Errorf("release catalog: releases[%d] has no slots", i)
		}
		if rel.Release.ReleaseID == "" || rel.Release.Version == "" || rel.Release.Digest == "" {
			return Catalog{}, fmt.Errorf("release catalog: releases[%d] missing release identity", i)
		}
		if len(rel.Files) == 0 {
			return Catalog{}, fmt.Errorf("release catalog: releases[%d] has no files", i)
		}
		for j, file := range rel.Files {
			if err := downloader.ValidateFile(file); err != nil {
				return Catalog{}, fmt.Errorf("release catalog: releases[%d].files[%d]: %w", i, j, err)
			}
		}
		rawSlots := make([]domain.CapabilitySlotKey, 0, len(rel.Slots))
		seenSlots := make(map[domain.CapabilitySlotKey]struct{}, len(rel.Slots))
		for _, slotRaw := range rel.Slots {
			slot := domain.CapabilitySlotKey(strings.TrimSpace(slotRaw))
			if !slot.Valid() {
				return Catalog{}, fmt.Errorf("release catalog: releases[%d] unknown slot %q", i, slotRaw)
			}
			if _, exists := seenSlots[slot]; exists {
				return Catalog{}, fmt.Errorf("release catalog: releases[%d] duplicate slot %q", i, slotRaw)
			}
			seenSlots[slot] = struct{}{}
			rawSlots = append(rawSlots, slot)
		}
		covered := domain.SortCapabilitySlots(rawSlots)
		perms := rel.Permissions
		if perms == nil {
			perms = []string{}
		}
		flashRef := strings.TrimSpace(rel.FlashCLIRef)
		if flashRef != "" && !domain.AdmittedFlashCLIRef(flashRef) {
			return Catalog{}, fmt.Errorf("release catalog: releases[%d] flashcli_ref %q is not admitted", i, flashRef)
		}
		plan := service.ReleasePlan{
			Release:          rel.Release,
			CoveredSlots:     covered,
			Files:            append([]downloader.File(nil), rel.Files...),
			DownloadBytes:    rel.DownloadBytes,
			CacheReuseBytes:  rel.CacheReuseBytes,
			InstallBytes:     rel.InstallBytes,
			PeakStagingBytes: rel.PeakStagingBytes,
			Permissions:      perms,
			License:          rel.License,
			RequiresRestart:  rel.RequiresRestart,
			FlashCLIRef:      flashRef,
		}
		key, err := domain.ReleaseStorageKey(rel.Release)
		if err != nil {
			return Catalog{}, fmt.Errorf("release catalog: releases[%d]: %w", i, err)
		}
		if existing, seen := declarations[key]; seen {
			if !sameCoverage(existing.CoveredSlots, plan.CoveredSlots) || !sameFiles(existing.Files, plan.Files) || existing.FlashCLIRef != plan.FlashCLIRef {
				return Catalog{}, fmt.Errorf(
					"release catalog: releases[%d] repeats release %q with conflicting coverage or file manifest",
					i, rel.Release.ReleaseID)
			}
		} else {
			declarations[key] = plan
		}
		if plan.DownloadBytes <= 0 {
			var sum int64
			for _, f := range plan.Files {
				sum += f.Size
			}
			plan.DownloadBytes = sum
			if plan.InstallBytes <= 0 {
				plan.InstallBytes = sum
			}
			if plan.PeakStagingBytes <= 0 {
				plan.PeakStagingBytes = sum
			}
		}
		for _, slot := range covered {
			if _, exists := bySlot[slot]; exists {
				return Catalog{}, fmt.Errorf("release catalog: duplicate slot %q", slot)
			}
			bySlot[slot] = plan
		}
	}

	prefixes := make([]string, 0, len(doc.DistributionPrefixes))
	for _, p := range doc.DistributionPrefixes {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			prefixes = append(prefixes, trimmed)
		}
	}
	return Catalog{bySlot: bySlot, Prefixes: prefixes, Note: doc.Note}, nil
}

func sameCoverage(a, b []domain.CapabilitySlotKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameFiles(a, b []downloader.File) bool {
	if len(a) != len(b) {
		return false
	}
	byPath := make(map[string]downloader.File, len(a))
	for _, file := range a {
		byPath[file.RelPath] = file
	}
	for _, file := range b {
		other, ok := byPath[file.RelPath]
		if !ok || other.SHA256 != file.SHA256 || other.Size != file.Size {
			// URL 可随镜像源不同，但相对路径/大小/SHA-256 必须一致。
			return false
		}
	}
	return true
}
