package service

import (
	"path/filepath"
	"strings"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// 应用数据目录布局（docs/data-and-storage.md §3 / FR-SET-42）：
// 模型包、媒体库、缓存、导出各占一类，用量与清理按类别统计。
func (s *Service) dataDir() string { return s.opts.DataDir }

func (s *Service) modelsDir() string { return filepath.Join(s.dataDir(), "models") }

// releasesDirName 是共享发布实体的物理根命名空间（不按槽位复制文件）。
const releasesDirName = "releases"

// releasesDir 是共享发布根；旧版 models/<slot>/<release> 目录继续原位保留。
func (s *Service) releasesDir() string { return filepath.Join(s.modelsDir(), releasesDirName) }

// releaseRoot 返回完整 ReleaseRef 摘要对应的新布局目录。
func (s *Service) releaseRoot(storageKey string) string {
	return filepath.Join(s.releasesDir(), storageKey)
}

// legacyInstallationDir 是旧版按槽位布局的目录 `models/<slot>/<release>`。
// 只用于读取/清理历史记录，新安装不再写入该布局。
func (s *Service) legacyInstallationDir(slot domain.CapabilitySlotKey, release domain.ReleaseRef) string {
	segment := "release"
	for _, candidate := range []string{release.ReleaseID, release.Version} {
		if safePathSegment(candidate) {
			segment = candidate
			break
		}
	}
	return filepath.Join(s.modelsDir(), string(slot), segment)
}

func safePathSegment(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	return !strings.ContainsAny(value, "/\\\x00")
}

func (s *Service) mediaDir() string { return filepath.Join(s.dataDir(), "media") }

// projectDir 是应用管理的项目媒体目录（Project.directory 的派生值）。
func (s *Service) projectDir(projectID string) string {
	return filepath.Join(s.mediaDir(), projectID)
}

func (s *Service) cacheDir() string { return filepath.Join(s.dataDir(), "cache") }

func (s *Service) exportsDir() string { return filepath.Join(s.dataDir(), "exports") }

func (s *Service) workDir() string { return filepath.Join(s.dataDir(), "work") }
