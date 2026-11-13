package api

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

// TestRelocateAssetContract 覆盖 POST /assets/{id}/relocate：
// 缺失外部引用同摘要恢复、不同摘要/非缺失/未知资产拒绝。
func TestRelocateAssetContract(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("重定位项目")

	sourceDir := t.TempDir()
	oldPath := filepath.Join(sourceDir, "episode.mp4")
	content := []byte("relocate-contract-bytes")
	if err := os.WriteFile(oldPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	imported := env.expect(env.request(http.MethodPost, "/api/v1/assets/imports", map[string]any{
		"project_id":  projectID,
		"file_grants": []string{oldPath},
		"copy_mode":   false,
	}), http.StatusAccepted)
	result := decodeInto[service.AssetImportResult](t, imported)
	assetID := result.AssetIDs[0]

	// 删除外部源文件后，读取项目把资产标为 missing。
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	project := decodeInto[domain.Project](t, env.expect(env.request(http.MethodGet, "/api/v1/projects/"+projectID, nil), http.StatusOK))
	if got := findAsset(t, project, assetID); got.Status != domain.AssetStatusMissing {
		t.Fatalf("status after delete = %s", got.Status)
	}

	// 不同内容必须拒绝，且不改变缺失状态。
	other := filepath.Join(t.TempDir(), "other.mp4")
	if err := os.WriteFile(other, []byte("different-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	mismatch := env.expect(env.request(http.MethodPost, "/api/v1/assets/"+string(assetID)+"/relocate", map[string]any{
		"file_grant": other,
	}), http.StatusBadRequest)
	if code := mismatch.code(t); code != "INVALID_REQUEST" {
		t.Fatalf("digest mismatch code = %s", code)
	}

	// 相对路径拒绝。
	relative := env.expect(env.request(http.MethodPost, "/api/v1/assets/"+string(assetID)+"/relocate", map[string]any{
		"file_grant": "relative.mp4",
	}), http.StatusBadRequest)
	if code := relative.code(t); code != "INVALID_REQUEST" {
		t.Fatalf("relative code = %s", code)
	}

	// 同内容新路径恢复 ready；asset ID 不变，内容仍可读取。
	newPath := filepath.Join(t.TempDir(), "renamed.mp4")
	if err := os.WriteFile(newPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	relocated := env.expect(env.request(http.MethodPost, "/api/v1/assets/"+string(assetID)+"/relocate", map[string]any{
		"file_grant": newPath,
	}), http.StatusOK)
	asset := decodeInto[domain.Asset](t, relocated)
	if asset.ID != assetID || asset.ProjectID != projectID {
		t.Fatalf("identity changed: %+v", asset)
	}
	if asset.Status != domain.AssetStatusReady || asset.Path != newPath {
		t.Fatalf("relocated asset = %+v", asset)
	}
	body := env.expect(env.request(http.MethodGet, "/api/v1/assets/"+string(assetID)+"/content", nil), http.StatusOK)
	if !bytes.Equal(body.Body, content) {
		t.Fatalf("content = %q", body.Body)
	}

	// 未知资产 → 404；已恢复资产再次重定位 → 400。
	env.expect(env.request(http.MethodPost, "/api/v1/assets/asset-missing/relocate", map[string]any{
		"file_grant": newPath,
	}), http.StatusNotFound)
	env.expect(env.request(http.MethodPost, "/api/v1/assets/"+string(assetID)+"/relocate", map[string]any{
		"file_grant": newPath,
	}), http.StatusBadRequest)
}

// TestRelocateCopyAssetRejected 覆盖 copy 资产不可重定位：要求重新导入。
func TestRelocateCopyAssetRejected(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("复制资产")
	source := filepath.Join(t.TempDir(), "copy.mp4")
	content := []byte("copy-relocate-bytes")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	imported := env.expect(env.request(http.MethodPost, "/api/v1/assets/imports", map[string]any{
		"project_id":  projectID,
		"file_grants": []string{source},
		"copy_mode":   true,
	}), http.StatusAccepted)
	result := decodeInto[service.AssetImportResult](t, imported)
	assetID := result.AssetIDs[0]

	reply := env.expect(env.request(http.MethodPost, "/api/v1/assets/"+string(assetID)+"/relocate", map[string]any{
		"file_grant": source,
	}), http.StatusBadRequest)
	if code := reply.code(t); code != "INVALID_REQUEST" {
		t.Fatalf("copy relocate code = %s", code)
	}
	asset, err := env.store.GetAsset(context.Background(), assetID)
	if err != nil {
		t.Fatalf("get copy asset: %v", err)
	}
	if asset.Status != domain.AssetStatusReady {
		t.Fatalf("copy asset status changed after rejected relocate: %+v", asset)
	}
}

func findAsset(t *testing.T, project domain.Project, assetID domain.ID) domain.Asset {
	t.Helper()
	for _, asset := range project.Assets {
		if asset.ID == assetID {
			return asset
		}
	}
	t.Fatalf("asset %s not found in project", assetID)
	return domain.Asset{}
}
