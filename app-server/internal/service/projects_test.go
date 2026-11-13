package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// TestCreateProjectCreatesDerivedDirectory 覆盖 Project.directory：
// 创建项目时在 <dataDir>/media/<project_id> 建立应用管理的目录并返回。
func TestCreateProjectCreatesDerivedDirectory(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()

	resp, err := env.svc.CreateProject(ctx, Idem{}, CreateProjectRequest{Name: "目录项目"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	project, ok := resp.Body.(domain.Project)
	if !ok {
		t.Fatalf("response body = %T, want domain.Project", resp.Body)
	}
	want := filepath.Join(env.dataDir, "media", project.ID)
	if project.Directory != want {
		t.Fatalf("directory = %q, want %q", project.Directory, want)
	}
	info, err := os.Stat(project.Directory)
	if err != nil {
		t.Fatalf("project directory missing: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("project directory is not a directory: %s", project.Directory)
	}

	// 读取路径返回同一派生目录；该字段不持久化，但必须稳定。
	got, err := env.svc.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Directory != want {
		t.Fatalf("GetProject directory = %q, want %q", got.Directory, want)
	}
}

// TestGetProjectBackfillsLegacyDirectory 覆盖旧项目（无 directory 字段）读取时补齐目录。
func TestGetProjectBackfillsLegacyDirectory(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("旧项目")
	want := filepath.Join(env.dataDir, "media", project.ID)

	// 直接经 store 登记的项目没有经过 CreateProject，模拟旧库。
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("precondition: directory should not exist yet, stat err = %v", err)
	}

	got, err := env.svc.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Directory != want {
		t.Fatalf("directory = %q, want %q", got.Directory, want)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("legacy project directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("legacy project directory is not a directory: %s", want)
	}
}

// TestCopyModeImportLandsUnderProjectDirectory 确认复制模式素材落在 Project.directory。
func TestCopyModeImportLandsUnderProjectDirectory(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()

	created, err := env.svc.CreateProject(ctx, Idem{}, CreateProjectRequest{Name: "复制导入"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	project := created.Body.(domain.Project)
	source := env.tempFile("source.mp4", "media-bytes")

	imported, err := env.svc.ImportAssets(ctx, Idem{}, AssetImportRequest{
		ProjectID:  project.ID,
		FileGrants: []string{source},
		CopyMode:   true,
	})
	if err != nil {
		t.Fatalf("ImportAssets: %v", err)
	}
	result := imported.Body.(AssetImportResult)
	if len(result.AssetIDs) != 1 {
		t.Fatalf("asset ids = %+v", result.AssetIDs)
	}

	got, err := env.svc.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if len(got.Assets) != 1 {
		t.Fatalf("assets = %+v", got.Assets)
	}
	assetPath := got.Assets[0].Path
	if filepath.Dir(assetPath) != project.Directory {
		t.Fatalf("copied asset path = %q, want parent %q", assetPath, project.Directory)
	}
	if _, err := os.Stat(assetPath); err != nil {
		t.Fatalf("copied asset missing: %v", err)
	}
}

// TestUpdateProjectNameAndEditVersion 覆盖重命名、版本递增、冲突与校验。
func TestUpdateProjectNameAndEditVersion(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()

	created, err := env.svc.CreateProject(ctx, Idem{}, CreateProjectRequest{Name: "旧名称"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	project := created.Body.(domain.Project)

	updated, err := env.svc.UpdateProject(ctx, Idem{}, project.ID, UpdateProjectRequest{
		Name:        "  新名称  ",
		EditVersion: project.EditVersion,
	})
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	renamed := updated.Body.(domain.Project)
	if renamed.Name != "新名称" {
		t.Fatalf("name = %q, want 新名称", renamed.Name)
	}
	if renamed.EditVersion != project.EditVersion+1 {
		t.Fatalf("edit_version = %d, want %d", renamed.EditVersion, project.EditVersion+1)
	}
	if renamed.Directory != project.Directory {
		t.Fatalf("directory = %q, want %q", renamed.Directory, project.Directory)
	}

	// 旧版本号必须被拒绝，且库内名称不变。
	_, err = env.svc.UpdateProject(ctx, Idem{}, project.ID, UpdateProjectRequest{
		Name:        "过期更新",
		EditVersion: project.EditVersion,
	})
	if code, ok := domain.CodeOf(err); !ok || code != domain.ErrorCodeRevisionConflict {
		t.Fatalf("stale update error = %v, want REVISION_CONFLICT", err)
	}
	got, err := env.svc.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Name != "新名称" {
		t.Fatalf("name after stale update = %q, want 新名称", got.Name)
	}

	// 空名、超长名与控制字符均拒绝。
	for _, name := range []string{"   ", strings.Repeat("长", 31), "bad\x00name"} {
		_, err := env.svc.UpdateProject(ctx, Idem{}, project.ID, UpdateProjectRequest{
			Name:        name,
			EditVersion: got.EditVersion,
		})
		if code, ok := domain.CodeOf(err); !ok || code != domain.ErrorCodeInvalidRequest {
			t.Fatalf("name %q error = %v, want INVALID_REQUEST", name, err)
		}
	}

	// 未知项目返回 NOT_FOUND。
	_, err = env.svc.UpdateProject(ctx, Idem{}, "missing", UpdateProjectRequest{Name: "不存在"})
	if code, ok := domain.CodeOf(err); !ok || code != domain.ErrorCodeNotFound {
		t.Fatalf("missing project error = %v, want NOT_FOUND", err)
	}
}
