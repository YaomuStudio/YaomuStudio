package service

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/system"
)

// fakeMediaProber 返回固定元数据或指定错误；按路径后缀区分可模拟单文件失败。
type fakeMediaProber struct {
	meta     *domain.MediaMeta
	err      error
	failPath string
	calls    []string
}

func (f *fakeMediaProber) Probe(_ context.Context, path string) (*domain.MediaMeta, error) {
	f.calls = append(f.calls, path)
	if f.failPath != "" && strings.HasSuffix(path, f.failPath) {
		return nil, errors.New("synthetic probe failure")
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.meta != nil {
		copied := *f.meta
		return &copied, nil
	}
	return &domain.MediaMeta{Container: "mp4", Codec: "h264", Width: 640, Height: 360,
		DurationMs: 1500, AudioTracks: 1, SampleRate: 48000}, nil
}

func (e *exportTestEnv) importMedia(t *testing.T, projectID string, paths []string, copyMode bool) []domain.ID {
	t.Helper()
	resp, err := e.svc.ImportAssets(context.Background(), Idem{}, AssetImportRequest{
		ProjectID:  projectID,
		FileGrants: paths,
		CopyMode:   copyMode,
	})
	if err != nil {
		t.Fatalf("ImportAssets: %v", err)
	}
	return resp.Body.(AssetImportResult).AssetIDs
}

func (e *exportTestEnv) asset(t *testing.T, id domain.ID) domain.Asset {
	t.Helper()
	asset, err := e.store.GetAsset(context.Background(), id)
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	return asset
}

func (e *exportTestEnv) projectAsset(t *testing.T, projectID, assetID domain.ID) domain.Asset {
	t.Helper()
	project, err := e.svc.GetProject(context.Background(), string(projectID))
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	for _, asset := range project.Assets {
		if asset.ID == assetID {
			return asset
		}
	}
	t.Fatalf("asset %s not in project %s", assetID, projectID)
	return domain.Asset{}
}

// TestImportProbePopulatesMetaAndMarksCorrupt 覆盖导入探测：
// 成功填 meta，失败标 corrupt 且不阻断其他文件，非媒体不调用 prober。
func TestImportProbePopulatesMetaAndMarksCorrupt(t *testing.T) {
	env := newExportTestEnv(t)
	project := env.seedProject("探测")
	prober := &fakeMediaProber{failPath: "broken.mp4"}
	env.svc.opts.MediaProber = prober

	dir := t.TempDir()
	good := filepath.Join(dir, "good.mp4")
	broken := filepath.Join(dir, "broken.mp4")
	text := filepath.Join(dir, "notes.txt")
	for _, path := range []string{good, broken, text} {
		if err := os.WriteFile(path, []byte("payload-"+filepath.Base(path)), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	ids := env.importMedia(t, project.ID, []string{good, broken, text}, false)
	if len(ids) != 3 {
		t.Fatalf("asset ids = %v", ids)
	}
	if got := env.asset(t, ids[0]); got.Status != domain.AssetStatusReady || got.Meta == nil ||
		got.Meta.Codec != "h264" || got.Meta.DurationMs != 1500 {
		t.Fatalf("good asset = %+v", got)
	}
	if got := env.asset(t, ids[1]); got.Status != domain.AssetStatusCorrupt || got.Meta != nil {
		t.Fatalf("broken asset = %+v", got)
	}
	if got := env.asset(t, ids[2]); got.Status != domain.AssetStatusReady || got.Meta != nil {
		t.Fatalf("non-media asset = %+v", got)
	}
	// 非媒体文件不探测；只探测两个视频。
	if len(prober.calls) != 2 {
		t.Fatalf("prober calls = %v", prober.calls)
	}
}

// TestImportWithoutProberKeepsReadyNoMeta 覆盖 ffprobe 未配置：
// 导入仍成功，Meta 为空，不伪造容器/时长。
func TestImportWithoutProberKeepsReadyNoMeta(t *testing.T) {
	env := newExportTestEnv(t)
	project := env.seedProject("无探测")
	path := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(path, []byte("not-real-media"), 0o600); err != nil {
		t.Fatal(err)
	}
	ids := env.importMedia(t, project.ID, []string{path}, false)
	asset := env.asset(t, ids[0])
	if asset.Status != domain.AssetStatusReady || asset.Meta != nil {
		t.Fatalf("asset = %+v", asset)
	}
}

// TestGetProjectRefreshesExternalAvailability 覆盖缺失检测与恢复：
// 外部引用被删除→missing，恢复→ready；copy 资产不受原文件影响。
func TestGetProjectRefreshesExternalAvailability(t *testing.T) {
	env := newExportTestEnv(t)
	project := env.seedProject("缺失检测")
	env.svc.opts.MediaProber = &fakeMediaProber{}

	source := filepath.Join(t.TempDir(), "external.mp4")
	if err := os.WriteFile(source, []byte("same-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	ids := env.importMedia(t, project.ID, []string{source}, false)
	externalID := ids[0]

	copyIDs := env.importMedia(t, project.ID, []string{source}, true)
	copyID := copyIDs[0]

	if got := env.projectAsset(t, project.ID, externalID); got.Status != domain.AssetStatusReady {
		t.Fatalf("before delete: %+v", got)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if got := env.projectAsset(t, project.ID, externalID); got.Status != domain.AssetStatusMissing {
		t.Fatalf("after delete: %+v", got)
	}
	// copy 资产读取的是应用内副本，原文件删除不影响。
	if got := env.projectAsset(t, project.ID, copyID); got.Status != domain.AssetStatusReady {
		t.Fatalf("copy asset: %+v", got)
	}
	locations, err := env.store.ListAssetLocations(context.Background(), externalID)
	if err != nil {
		t.Fatal(err)
	}
	if len(locations) != 1 || locations[0].Available {
		t.Fatalf("locations = %+v", locations)
	}

	if err := os.WriteFile(source, []byte("same-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := env.projectAsset(t, project.ID, externalID); got.Status != domain.AssetStatusReady {
		t.Fatalf("after restore: %+v", got)
	}
}

// TestRelocateAssetSuccess 覆盖同摘要重定位：asset ID 不变，路径/meta/大小刷新，内容可读。
func TestRelocateAssetSuccess(t *testing.T) {
	env := newExportTestEnv(t)
	project := env.seedProject("重定位")
	env.svc.opts.MediaProber = &fakeMediaProber{}

	oldPath := filepath.Join(t.TempDir(), "old.mp4")
	content := []byte("original-media-bytes")
	if err := os.WriteFile(oldPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	ids := env.importMedia(t, project.ID, []string{oldPath}, false)
	assetID := ids[0]
	before := env.asset(t, assetID)
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	if got := env.projectAsset(t, project.ID, assetID); got.Status != domain.AssetStatusMissing {
		t.Fatalf("after delete: %+v", got)
	}

	newPath := filepath.Join(t.TempDir(), "renamed.mp4")
	if err := os.WriteFile(newPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	resp, err := env.svc.RelocateAsset(context.Background(), Idem{}, string(assetID), RelocateAssetRequest{FileGrant: newPath})
	if err != nil {
		t.Fatalf("RelocateAsset: %v", err)
	}
	if resp.Status != 200 {
		t.Fatalf("status = %d", resp.Status)
	}
	relocated := resp.Body.(domain.Asset)
	if relocated.ID != assetID || relocated.ProjectID != project.ID {
		t.Fatalf("identity changed: %+v", relocated)
	}
	if relocated.Status != domain.AssetStatusReady || relocated.Path != newPath {
		t.Fatalf("relocated = %+v", relocated)
	}
	if relocated.SHA256 != before.SHA256 || relocated.SizeBytes != int64(len(content)) {
		t.Fatalf("digest/size = %s/%d", relocated.SHA256, relocated.SizeBytes)
	}
	if relocated.Meta == nil || relocated.Meta.DurationMs != 1500 {
		t.Fatalf("meta = %+v", relocated.Meta)
	}
	contentRead, err := env.svc.AssetContentFor(context.Background(), string(assetID))
	if err != nil {
		t.Fatalf("AssetContentFor: %v", err)
	}
	if contentRead.Path != newPath {
		t.Fatalf("content path = %q", contentRead.Path)
	}

	// asset.relocated 事件与业务更新同事务提交。
	events, err := env.store.EventsAfter(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		if ev.Type == "asset.relocated" && ev.ProjectID == project.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("asset.relocated event not persisted")
	}
}

// TestRelocateIdempotencyReplay 覆盖成功后的幂等重放：返回持久化响应而不是 REVISION_CONFLICT。
func TestRelocateIdempotencyReplay(t *testing.T) {
	env := newExportTestEnv(t)
	project := env.seedProject("幂等")
	env.svc.opts.MediaProber = &fakeMediaProber{}
	oldPath := filepath.Join(t.TempDir(), "a.mp4")
	if err := os.WriteFile(oldPath, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	assetID := env.importMedia(t, project.ID, []string{oldPath}, false)[0]
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.GetProject(context.Background(), string(project.ID)); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(t.TempDir(), "b.mp4")
	if err := os.WriteFile(newPath, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	idem := Idem{Key: "relocate-key", Digest: "digest-1"}
	req := RelocateAssetRequest{FileGrant: newPath}
	first, err := env.svc.RelocateAsset(context.Background(), idem, string(assetID), req)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := env.svc.RelocateAsset(context.Background(), idem, string(assetID), req)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	raw, ok := second.Body.(json.RawMessage)
	if !ok {
		t.Fatalf("replay body = %T, want json.RawMessage", second.Body)
	}
	var decoded domain.Asset
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if decoded.ID != first.Body.(domain.Asset).ID || decoded.Path != newPath {
		t.Fatalf("replay asset = %+v", decoded)
	}
}

// TestRelocateRejectsInvalidRequests 覆盖摘要不一致/非缺失/copy/generated/路径/探测失败。
func TestRelocateRejectsInvalidRequests(t *testing.T) {
	newEnvWithExternal := func(t *testing.T) (*exportTestEnv, domain.Project, domain.ID, string) {
		env := newExportTestEnv(t)
		project := env.seedProject("拒绝")
		env.svc.opts.MediaProber = &fakeMediaProber{}
		path := filepath.Join(t.TempDir(), "clip.mp4")
		if err := os.WriteFile(path, []byte("content-A"), 0o600); err != nil {
			t.Fatal(err)
		}
		assetID := env.importMedia(t, project.ID, []string{path}, false)[0]
		return env, project, assetID, path
	}

	t.Run("digest mismatch", func(t *testing.T) {
		env, project, assetID, oldPath := newEnvWithExternal(t)
		if err := os.Remove(oldPath); err != nil {
			t.Fatal(err)
		}
		if _, err := env.svc.GetProject(context.Background(), string(project.ID)); err != nil {
			t.Fatal(err)
		}
		other := filepath.Join(t.TempDir(), "other.mp4")
		if err := os.WriteFile(other, []byte("content-B"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := env.svc.RelocateAsset(context.Background(), Idem{}, string(assetID), RelocateAssetRequest{FileGrant: other})
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("error = %v, want INVALID_REQUEST", err)
		}
		if got := env.asset(t, assetID); got.Status != domain.AssetStatusMissing {
			t.Fatalf("status changed on rejection: %+v", got)
		}
	})

	t.Run("not missing", func(t *testing.T) {
		env, _, assetID, path := newEnvWithExternal(t)
		_, err := env.svc.RelocateAsset(context.Background(), Idem{}, string(assetID), RelocateAssetRequest{FileGrant: path})
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("error = %v, want INVALID_REQUEST", err)
		}
	})

	t.Run("relative path", func(t *testing.T) {
		env, _, assetID, _ := newEnvWithExternal(t)
		_, err := env.svc.RelocateAsset(context.Background(), Idem{}, string(assetID), RelocateAssetRequest{FileGrant: "relative.mp4"})
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("error = %v, want INVALID_REQUEST", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		env, _, assetID, oldPath := newEnvWithExternal(t)
		if err := os.Remove(oldPath); err != nil {
			t.Fatal(err)
		}
		_, err := env.svc.RelocateAsset(context.Background(), Idem{}, string(assetID), RelocateAssetRequest{FileGrant: t.TempDir()})
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("error = %v, want INVALID_REQUEST", err)
		}
	})

	t.Run("probe failure", func(t *testing.T) {
		env, project, assetID, oldPath := newEnvWithExternal(t)
		if err := os.Remove(oldPath); err != nil {
			t.Fatal(err)
		}
		if _, err := env.svc.GetProject(context.Background(), string(project.ID)); err != nil {
			t.Fatal(err)
		}
		sameContent := filepath.Join(t.TempDir(), "same.mp4")
		if err := os.WriteFile(sameContent, []byte("content-A"), 0o600); err != nil {
			t.Fatal(err)
		}
		env.svc.opts.MediaProber = &fakeMediaProber{failPath: "same.mp4"}
		_, err := env.svc.RelocateAsset(context.Background(), Idem{}, string(assetID), RelocateAssetRequest{FileGrant: sameContent})
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("error = %v, want INVALID_REQUEST", err)
		}
		if got := env.asset(t, assetID); got.Status != domain.AssetStatusMissing {
			t.Fatalf("status changed on probe failure: %+v", got)
		}
	})

	t.Run("copy asset", func(t *testing.T) {
		env := newExportTestEnv(t)
		project := env.seedProject("copy")
		env.svc.opts.MediaProber = &fakeMediaProber{}
		source := filepath.Join(t.TempDir(), "copy.mp4")
		if err := os.WriteFile(source, []byte("copy-bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		assetID := env.importMedia(t, project.ID, []string{source}, true)[0]
		_, err := env.svc.RelocateAsset(context.Background(), Idem{}, string(assetID), RelocateAssetRequest{FileGrant: source})
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("error = %v, want INVALID_REQUEST", err)
		}
	})

	t.Run("generated asset", func(t *testing.T) {
		env := newExportTestEnv(t)
		project := env.seedProject("generated")
		now := env.svc.Now()
		assetID := "asset-generated"
		if err := env.store.CreateAsset(context.Background(), domain.Asset{
			ID: assetID, ProjectID: project.ID, Type: domain.AssetTypeVideo, Name: "out.mp4",
			SizeBytes: 3, SHA256: strings.Repeat("a", 64), Status: domain.AssetStatusMissing, ImportedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := env.store.AddAssetLocation(context.Background(), domain.AssetLocation{
			ID: "loc-generated", AssetID: assetID, Path: filepath.Join(t.TempDir(), "out.mp4"),
			Source: domain.LocationSourceGenerated, Owner: domain.LocationOwnerApp, CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "out.mp4")
		if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := env.svc.RelocateAsset(context.Background(), Idem{}, assetID, RelocateAssetRequest{FileGrant: path})
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("error = %v, want INVALID_REQUEST", err)
		}
	})

	t.Run("unknown asset", func(t *testing.T) {
		env := newExportTestEnv(t)
		path := filepath.Join(t.TempDir(), "x.mp4")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := env.svc.RelocateAsset(context.Background(), Idem{}, "asset-missing", RelocateAssetRequest{FileGrant: path})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("error = %v, want NOT_FOUND", err)
		}
	})

	t.Run("prober unavailable keeps existing meta", func(t *testing.T) {
		env, project, assetID, oldPath := newEnvWithExternal(t)
		before := env.asset(t, assetID)
		if err := os.Remove(oldPath); err != nil {
			t.Fatal(err)
		}
		if _, err := env.svc.GetProject(context.Background(), string(project.ID)); err != nil {
			t.Fatal(err)
		}
		sameContent := filepath.Join(t.TempDir(), "same.mp4")
		if err := os.WriteFile(sameContent, []byte("content-A"), 0o600); err != nil {
			t.Fatal(err)
		}
		env.svc.opts.MediaProber = &fakeMediaProber{err: system.ErrProberUnavailable}
		resp, err := env.svc.RelocateAsset(context.Background(), Idem{}, string(assetID), RelocateAssetRequest{FileGrant: sameContent})
		if err != nil {
			t.Fatalf("relocate: %v", err)
		}
		got := resp.Body.(domain.Asset)
		if got.Status != domain.AssetStatusReady {
			t.Fatalf("status = %s", got.Status)
		}
		if got.Meta == nil || before.Meta == nil || got.Meta.Codec != before.Meta.Codec {
			t.Fatalf("meta not preserved: before=%+v after=%+v", before.Meta, got.Meta)
		}
	})
}

// TestImportWithRealFFprobe 用真实 ffprobe 端到端验证导入探测：
// 合成 WAV 填 meta；同批损坏文件标 corrupt 且不阻断。工具缺失时显式跳过。
func TestImportWithRealFFprobe(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("FFPROBE_BIN"))
	if binary == "" {
		found, err := exec.LookPath("ffprobe")
		if err != nil {
			t.Skip("FFPROBE_UNAVAILABLE: 未找到 ffprobe；真实导入探测集成测试跳过（不得视为通过）")
		}
		binary = found
	}
	t.Logf("ffprobe: %s", binary)

	env := newExportTestEnv(t)
	env.svc.opts.MediaProber = system.NewFFprobeProber(binary)
	project := env.seedProject("真实探测")
	dir := t.TempDir()
	good := filepath.Join(dir, "tone.wav")
	broken := filepath.Join(dir, "broken.wav")
	if err := os.WriteFile(good, synthTestWAV(8000, 500), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(broken, []byte("not a wav"), 0o600); err != nil {
		t.Fatal(err)
	}
	ids := env.importMedia(t, project.ID, []string{good, broken}, false)
	if len(ids) != 2 {
		t.Fatalf("asset ids = %v", ids)
	}
	asset := env.asset(t, ids[0])
	if asset.Status != domain.AssetStatusReady || asset.Meta == nil {
		t.Fatalf("good asset = %+v", asset)
	}
	if asset.Meta.Container == "" || asset.Meta.Codec == "" || asset.Meta.AudioTracks != 1 ||
		asset.Meta.SampleRate != 8000 {
		t.Fatalf("meta = %+v", asset.Meta)
	}
	if asset.Meta.DurationMs < 400 || asset.Meta.DurationMs > 700 {
		t.Fatalf("duration_ms = %d", asset.Meta.DurationMs)
	}
	if got := env.asset(t, ids[1]); got.Status != domain.AssetStatusCorrupt || got.Meta != nil {
		t.Fatalf("broken asset = %+v", got)
	}
}

// synthTestWAV 生成 16-bit 单声道静音 WAV，避免测试依赖外部工具。
func synthTestWAV(sampleRate, durationMs int) []byte {
	samples := sampleRate * durationMs / 1000
	dataBytes := samples * 2
	buf := make([]byte, 44+dataBytes)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(36+dataBytes))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1)
	binary.LittleEndian.PutUint16(buf[22:24], 1)
	binary.LittleEndian.PutUint32(buf[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(buf[32:34], 2)
	binary.LittleEndian.PutUint16(buf[34:36], 16)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataBytes))
	return buf
}
