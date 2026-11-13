package service

// 安装链路服务级测试：使用 httptest 本地假分发源验证真实下载语义
// （成功激活、断点/取消、篡改哈希、失败升级保留旧版本、回滚、事件序列、
// READY 联动、重启对账）。不访问外网，不使用 FlashCLI 参数。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/downloader"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func testSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sequenceReleases 按顺序返回预置发布，模拟 v1 → v2 的升级。
type sequenceReleases struct {
	mu    sync.Mutex
	plans []ReleasePlan
	next  int
}

func (s *sequenceReleases) Resolve(_ context.Context, _ domain.CapabilitySlotKey) (ReleasePlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next >= len(s.plans) {
		return ReleasePlan{}, fmt.Errorf("test catalog exhausted")
	}
	plan := s.plans[s.next]
	s.next++
	return plan, nil
}

type installTestEnv struct {
	t       *testing.T
	svc     *Service
	store   *store.Store
	dataDir string
}

func newInstallTestEnv(t *testing.T, releases ReleaseCatalog, dl Downloader) *installTestEnv {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dataDir, "yaomu.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := New(Options{
		Store:        st,
		WorkspaceID:  "ws-install",
		DataDir:      dataDir,
		Capabilities: NewInstallationCapabilities(),
		Releases:     releases,
		Downloader:   dl,
	})
	return &installTestEnv{t: t, svc: svc, store: st, dataDir: dataDir}
}

func newTestDownloader(prefixes ...string) *downloader.Client {
	approved := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		approved = append(approved, prefix+"/")
	}
	return downloader.New(downloader.Options{
		Prefixes:         approved,
		RetryBaseDelay:   time.Millisecond,
		ProgressInterval: time.Millisecond,
	})
}

func (e *installTestEnv) create(slot domain.CapabilitySlotKey) domain.Installation {
	e.t.Helper()
	plan, err := e.svc.PlanInstallation(context.Background(), slot)
	if err != nil {
		e.t.Fatalf("PlanInstallation() error = %v", err)
	}
	resp, err := e.svc.CreateInstallation(context.Background(), Idem{}, CreateInstallationRequest{
		PlanID:   plan.PlanID,
		PlanHash: plan.PlanHash,
	})
	if err != nil {
		e.t.Fatalf("CreateInstallation() error = %v", err)
	}
	inst, ok := resp.Body.(domain.Installation)
	if !ok {
		e.t.Fatalf("CreateInstallation body = %T", resp.Body)
	}
	return inst
}

func (e *installTestEnv) install(slot domain.CapabilitySlotKey) domain.Installation {
	e.t.Helper()
	return e.waitTerminal(e.create(slot).ID)
}

func (e *installTestEnv) waitTerminal(id domain.ID) domain.Installation {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		inst, err := e.svc.GetInstallation(context.Background(), id)
		if err != nil {
			e.t.Fatalf("GetInstallation(%s) error = %v", id, err)
		}
		if inst.Phase.Terminal() {
			return inst
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("installation %s stuck in %s", id, inst.Phase)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *installTestEnv) capability(slot domain.CapabilitySlotKey) domain.Capability {
	e.t.Helper()
	caps, err := e.svc.ListCapabilities(context.Background())
	if err != nil {
		e.t.Fatalf("ListCapabilities() error = %v", err)
	}
	for _, capability := range caps {
		if capability.Slot == slot {
			return capability
		}
	}
	e.t.Fatalf("capability %s not found", slot)
	return domain.Capability{}
}

// releaseDir 返回新共享布局 `models/releases/<完整ReleaseRef哈希>`。
func (e *installTestEnv) releaseDir(release domain.ReleaseRef) string {
	e.t.Helper()
	key, err := domain.ReleaseStorageKey(release)
	if err != nil {
		e.t.Fatalf("ReleaseStorageKey() error = %v", err)
	}
	return filepath.Join(e.dataDir, "models", "releases", key)
}

// seedVerifiedInstallation 直接构造一个已完成、文件已落位的新布局安装实体，
// 用于安装准入/回滚等不经过传输的定向测试（仍写真实文件与冻结清单）。
func seedVerifiedInstallation(
	t *testing.T,
	st *store.Store,
	dataDir string,
	id string,
	slot domain.CapabilitySlotKey,
	release domain.ReleaseRef,
	covered []domain.CapabilitySlotKey,
	files map[string][]byte,
	createdAt time.Time,
) domain.Installation {
	t.Helper()
	key, err := domain.ReleaseStorageKey(release)
	if err != nil {
		t.Fatalf("ReleaseStorageKey(%s): %v", release.ReleaseID, err)
	}
	root := filepath.Join(dataDir, "models", "releases", key)
	relPaths := make([]string, 0, len(files))
	for relPath := range files {
		relPaths = append(relPaths, relPath)
	}
	sort.Strings(relPaths)
	manifest := make([]domain.InstallationFile, 0, len(files))
	for _, relPath := range relPaths {
		content := files[relPath]
		path := filepath.Join(root, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", relPath, err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
		manifest = append(manifest, domain.InstallationFile{
			RelPath: relPath,
			SHA256:  testSHA256(content),
			Size:    int64(len(content)),
		})
	}
	if len(covered) == 0 {
		covered = []domain.CapabilitySlotKey{slot}
	}
	inst := domain.Installation{
		ID:            id,
		Slot:          slot,
		CoveredSlots:  covered,
		Phase:         domain.InstallationPhaseDone,
		Progress:      100,
		VerifiedFiles: len(files),
		Release:       &release,
		StorageKey:    key,
		Manifest:      manifest,
		PathOwner:     "app",
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
	if err := st.CreateInstallation(context.Background(), inst); err != nil {
		t.Fatalf("CreateInstallation(%s): %v", id, err)
	}
	return inst
}

// seedInstallationFiles 写入最终文件并构造可恢复的安装记录（不经过传输）。
// 用于模拟“文件已原子落位，但 done 事务未写入”的崩溃窗口。
func seedInstallationFiles(
	t *testing.T,
	st *store.Store,
	dataDir string,
	id string,
	slot domain.CapabilitySlotKey,
	release domain.ReleaseRef,
	files map[string][]byte,
	phase domain.InstallationPhase,
	verified int,
	createdAt time.Time,
) domain.Installation {
	t.Helper()
	key, err := domain.ReleaseStorageKey(release)
	if err != nil {
		t.Fatalf("ReleaseStorageKey(%s): %v", release.ReleaseID, err)
	}
	root := filepath.Join(dataDir, "models", "releases", key)
	relPaths := make([]string, 0, len(files))
	for relPath := range files {
		relPaths = append(relPaths, relPath)
	}
	sort.Strings(relPaths)
	manifest := make([]domain.InstallationFile, 0, len(files))
	for _, relPath := range relPaths {
		content := files[relPath]
		path := filepath.Join(root, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", relPath, err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
		manifest = append(manifest, domain.InstallationFile{
			RelPath: relPath,
			SHA256:  testSHA256(content),
			Size:    int64(len(content)),
		})
	}
	inst := domain.Installation{
		ID:            id,
		Slot:          slot,
		CoveredSlots:  []domain.CapabilitySlotKey{slot},
		Phase:         phase,
		Progress:      100,
		VerifiedFiles: verified,
		Release:       &release,
		StorageKey:    key,
		Manifest:      manifest,
		PathOwner:     "app",
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
	if err := st.CreateInstallation(context.Background(), inst); err != nil {
		t.Fatalf("CreateInstallation(%s): %v", id, err)
	}
	return inst
}

// eventLabels 把安装事件压成可断言的标签；phase_changed 带目标阶段。
func (e *installTestEnv) eventLabels() []string {
	e.t.Helper()
	events, err := e.store.EventsAfter(context.Background(), 0, 1000)
	if err != nil {
		e.t.Fatalf("EventsAfter() error = %v", err)
	}
	labels := make([]string, 0, len(events))
	for _, event := range events {
		if !strings.HasPrefix(event.Type, "installation.") {
			continue
		}
		if event.Type == "installation.phase_changed" {
			phase, _ := event.Data["phase"].(string)
			labels = append(labels, "phase_changed:"+phase)
			continue
		}
		labels = append(labels, event.Type)
	}
	return labels
}

func (e *installTestEnv) eventLabelsFor(id domain.ID) []string {
	e.t.Helper()
	events, err := e.store.EventsAfter(context.Background(), 0, 1000)
	if err != nil {
		e.t.Fatalf("EventsAfter() error = %v", err)
	}
	out := make([]string, 0, len(events))
	for _, event := range events {
		eventID, _ := event.Data["installation_id"].(string)
		if eventID != string(id) {
			continue
		}
		if event.Type == "installation.phase_changed" {
			phase, _ := event.Data["phase"].(string)
			out = append(out, "phase_changed:"+phase)
			continue
		}
		out = append(out, event.Type)
	}
	return out
}

func assertSubsequence(t *testing.T, got []string, want ...string) {
	t.Helper()
	index := 0
	for _, label := range got {
		if index < len(want) && label == want[index] {
			index++
		}
	}
	if index != len(want) {
		t.Fatalf("event sequence %v does not contain %v", got, want)
	}
}

func assertNoPartialFiles(t *testing.T, root string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".part") {
			t.Fatalf("residual partial file %s", path)
		}
		return nil
	})
}

func waitNoPartialFiles(t *testing.T, root string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		residual := false
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".part") {
				residual = true
			}
			return nil
		})
		if !residual {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("partial files still present under %s", root)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func releasePlanFor(server *httptest.Server, releaseID, version string, files map[string][]byte) ReleasePlan {
	manifest := make([]downloader.File, 0, len(files))
	var total int64
	for relPath, content := range files {
		manifest = append(manifest, downloader.File{
			RelPath: relPath,
			URL:     server.URL + "/" + releaseID + "/" + filepath.Base(relPath),
			SHA256:  testSHA256(content),
			Size:    int64(len(content)),
		})
		total += int64(len(content))
	}
	return ReleasePlan{
		Release:       domain.ReleaseRef{ReleaseID: releaseID, Version: version, Digest: "digest-" + releaseID},
		Files:         manifest,
		DownloadBytes: total,
		InstallBytes:  total,
		Permissions:   []string{"filesystem_write"},
		License:       domain.LicenseSummary{LicenseID: "Apache-2.0", LicenseName: "Apache License 2.0"},
	}
}

// staticSource 按 URL 路径返回固定内容。
func staticSource(t *testing.T, contents map[string][]byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, ok := contents[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, filepath.Base(r.URL.Path), time.Time{}, bytes.NewReader(content))
	}))
	t.Cleanup(server.Close)
	return server
}

// TestInstallationDownloadsFilesAndActivates 覆盖成功下载、落位、READY 与事件序列。
func TestInstallationDownloadsFilesAndActivates(t *testing.T) {
	contentA := bytes.Repeat([]byte("a"), 32*1024)
	contentB := []byte("weights-payload-v1")
	server := staticSource(t, map[string][]byte{
		"/rel-v1/model.bin":   contentA,
		"/rel-v1/weights.bin": contentB,
	})
	plan := releasePlanFor(server, "rel-v1", "1.0.0", map[string][]byte{
		"model/model.bin":           contentA,
		"model/weights/weights.bin": contentB,
	})
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotASR: plan,
	}}, newTestDownloader(server.URL))

	inst := env.install(domain.CapabilitySlotASR)
	if inst.Phase != domain.InstallationPhaseDone {
		t.Fatalf("phase = %s, error = %+v", inst.Phase, inst.Error)
	}
	if inst.Progress != 100 || inst.VerifiedFiles != 2 {
		t.Fatalf("installation = %+v", inst)
	}
	if !inst.ProbePassed {
		t.Fatalf("verified installation should be ready for execution: %+v", inst)
	}
	for name, want := range map[string][]byte{
		"model/model.bin":           contentA,
		"model/weights/weights.bin": contentB,
	} {
		got, err := os.ReadFile(filepath.Join(env.releaseDir(plan.Release), name))
		if err != nil {
			t.Fatalf("read installed %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("installed %s content mismatch", name)
		}
	}
	assertNoPartialFiles(t, filepath.Join(env.dataDir, "models"))

	capability := env.capability(domain.CapabilitySlotASR)
	if capability.State != domain.SlotStateReady || capability.Version != "1.0.0" {
		t.Fatalf("capability = %s/%s", capability.State, capability.Version)
	}

	labels := env.eventLabels()
	assertSubsequence(t, labels,
		"installation.created",
		"phase_changed:downloading",
		"installation.progress",
		"installation.downloaded",
		"phase_changed:verifying",
		"phase_changed:installing",
		"phase_changed:done",
		"installation.completed",
	)
}

// TestInstallationHashMismatchFailsWithoutActivation 覆盖篡改哈希：FAILED、不落位、不 READY。
func TestInstallationHashMismatchFailsWithoutActivation(t *testing.T) {
	good := []byte("expected-payload")
	tampered := []byte("tampered-payload")
	if len(good) != len(tampered) {
		t.Fatalf("test content sizes must match: %d != %d", len(good), len(tampered))
	}
	server := staticSource(t, map[string][]byte{"/rel-v2/model.bin": tampered})
	plan := releasePlanFor(server, "rel-v2", "2.0.0", map[string][]byte{"model.bin": good})
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotASR: plan,
	}}, newTestDownloader(server.URL))

	inst := env.install(domain.CapabilitySlotASR)
	if inst.Phase != domain.InstallationPhaseFailed {
		t.Fatalf("phase = %s, want failed", inst.Phase)
	}
	if inst.Error == nil || inst.Error.MessageKey != "installation.verify_failed" || !inst.Retryable {
		t.Fatalf("installation error = %+v retryable=%v", inst.Error, inst.Retryable)
	}
	if capability := env.capability(domain.CapabilitySlotASR); capability.State == domain.SlotStateReady {
		t.Fatalf("capability must not be READY after failed verification: %+v", capability)
	}
	dir := filepath.Join(env.dataDir, "models", "asr", "rel-v2")
	if _, err := os.Stat(filepath.Join(dir, "model.bin")); !os.IsNotExist(err) {
		t.Fatalf("tampered file must not be committed: %v", err)
	}
	assertNoPartialFiles(t, filepath.Join(env.dataDir, "models"))
	assertSubsequence(t, env.eventLabelsFor(inst.ID),
		"phase_changed:downloading",
		"phase_changed:verifying",
		"phase_changed:failed",
		"installation.failed",
	)
}

// TestInstallationCancelCleansTemporaryFiles 覆盖下载中取消：清理 .part、保留旧版本、不激活。
func TestInstallationCancelCleansTemporaryFiles(t *testing.T) {
	v1 := []byte("previous-version-payload")
	serverV1 := staticSource(t, map[string][]byte{"/rel-v1/model.bin": v1})
	v1Plan := releasePlanFor(serverV1, "rel-v1", "1.0.0", map[string][]byte{"model.bin": v1})

	started := make(chan struct{})
	var startedOnce sync.Once
	v2 := bytes.Repeat([]byte("v2"), 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rel-v2/model.bin" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(v2)))
		_, _ = w.Write(v2[:4])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		startedOnce.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	v2Plan := releasePlanFor(server, "rel-v2", "2.0.0", map[string][]byte{"model.bin": v2})
	// 假源静态返回 v2 内容；清单哈希按 v2 内容计算。
	env := newInstallTestEnv(t, &sequenceReleases{plans: []ReleasePlan{v1Plan, v2Plan}},
		newTestDownloader(serverV1.URL, server.URL))

	first := env.install(domain.CapabilitySlotASR)
	if first.Phase != domain.InstallationPhaseDone {
		t.Fatalf("v1 installation phase = %s", first.Phase)
	}

	second := env.create(domain.CapabilitySlotASR)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("v2 download did not start")
	}
	if capability := env.capability(domain.CapabilitySlotASR); capability.State == domain.SlotStateReady {
		t.Fatalf("downloading record must not be READY: %+v", capability)
	}

	// 并发取消：两个请求同时到达，终态后再次取消也必须幂等返回。
	var cancelWG sync.WaitGroup
	cancelErrs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		cancelWG.Add(1)
		go func() {
			defer cancelWG.Done()
			_, err := env.svc.CancelInstallation(context.Background(), Idem{}, string(second.ID))
			cancelErrs <- err
		}()
	}
	cancelWG.Wait()
	close(cancelErrs)
	for err := range cancelErrs {
		if err != nil {
			t.Fatalf("CancelInstallation() error = %v", err)
		}
	}
	canceled := env.waitTerminal(second.ID)
	if canceled.Phase != domain.InstallationPhaseCanceled {
		t.Fatalf("phase = %s, want canceled", canceled.Phase)
	}

	waitNoPartialFiles(t, filepath.Join(env.dataDir, "models"))
	oldFile, err := os.ReadFile(filepath.Join(env.releaseDir(v1Plan.Release), "model.bin"))
	if err != nil || !bytes.Equal(oldFile, v1) {
		t.Fatalf("previous version files were altered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.releaseDir(v2Plan.Release), "model.bin")); !os.IsNotExist(err) {
		t.Fatalf("canceled download must not commit files: %v", err)
	}
	labels := env.eventLabelsFor(second.ID)
	for _, label := range labels {
		if label == "installation.completed" || label == "installation.downloaded" {
			t.Fatalf("canceled installation emitted %s: %v", label, labels)
		}
	}
}

// TestInstallationRollbackReturnsToPreviousVersion 覆盖 done 后回滚到上一保留版本。
func TestInstallationRollbackReturnsToPreviousVersion(t *testing.T) {
	v1 := []byte("release-one")
	v2 := []byte("release-two")
	server := staticSource(t, map[string][]byte{
		"/rel-v1/model.bin": v1,
		"/rel-v2/model.bin": v2,
	})
	plans := []ReleasePlan{
		releasePlanFor(server, "rel-v1", "1.0.0", map[string][]byte{"model.bin": v1}),
		releasePlanFor(server, "rel-v2", "2.0.0", map[string][]byte{"model.bin": v2}),
	}
	env := newInstallTestEnv(t, &sequenceReleases{plans: plans}, newTestDownloader(server.URL))

	if inst := env.install(domain.CapabilitySlotASR); inst.Phase != domain.InstallationPhaseDone {
		t.Fatalf("v1 phase = %s", inst.Phase)
	}
	if inst := env.install(domain.CapabilitySlotASR); inst.Phase != domain.InstallationPhaseDone {
		t.Fatalf("v2 phase = %s", inst.Phase)
	}
	if capability := env.capability(domain.CapabilitySlotASR); capability.Version != "2.0.0" {
		t.Fatalf("version after v2 = %s", capability.Version)
	}

	resp, err := env.svc.RollbackInstalledPackage(context.Background(), Idem{}, "asr")
	if err != nil {
		t.Fatalf("RollbackInstalledPackage() error = %v", err)
	}
	rolled, ok := resp.Body.(domain.Installation)
	if !ok {
		t.Fatalf("rollback body = %T", resp.Body)
	}
	if rolled.Phase != domain.InstallationPhaseDone || rolled.Release == nil || rolled.Release.Version != "1.0.0" {
		t.Fatalf("rollback = %+v", rolled)
	}
	if capability := env.capability(domain.CapabilitySlotASR); capability.State != domain.SlotStateReady || capability.Version != "1.0.0" {
		t.Fatalf("capability after rollback = %s/%s", capability.State, capability.Version)
	}
	// 两个版本目录都保留，回滚不需要重新下载。
	for _, plan := range plans {
		want := map[string][]byte{"rel-v1": v1, "rel-v2": v2}[plan.Release.ReleaseID]
		got, err := os.ReadFile(filepath.Join(env.releaseDir(plan.Release), "model.bin"))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("version %s files missing/changed: %v", plan.Release.ReleaseID, err)
		}
	}
	if !containsLabel(env.eventLabels(), "installation.rollback") {
		t.Fatalf("missing installation.rollback event: %v", env.eventLabels())
	}
}

func containsLabel(labels []string, want string) bool {
	for _, label := range labels {
		if label == want {
			return true
		}
	}
	return false
}

// TestFailedUpgradeKeepsPreviousVersionReady 覆盖失败升级不遮蔽既有可用版本：
// 新版本校验失败后槽位仍显示旧版本 READY（FR-MODEL-02：失败不激活、旧版本保留）。
func TestFailedUpgradeKeepsPreviousVersionReady(t *testing.T) {
	v1 := []byte("release-one-payload")
	tampered := bytes.Repeat([]byte("x"), len(v1))
	server := staticSource(t, map[string][]byte{
		"/rel-v1/model.bin": v1,
		"/rel-v2/model.bin": tampered,
	})
	v1Plan := releasePlanFor(server, "rel-v1", "1.0.0", map[string][]byte{"model.bin": v1})
	// v2 清单哈希按 v1 内容计算，假源返回篡改内容 → verifying 失败且不激活。
	v2Plan := releasePlanFor(server, "rel-v2", "2.0.0", map[string][]byte{"model.bin": v1})
	env := newInstallTestEnv(t, &sequenceReleases{plans: []ReleasePlan{v1Plan, v2Plan}},
		newTestDownloader(server.URL))

	if inst := env.install(domain.CapabilitySlotASR); inst.Phase != domain.InstallationPhaseDone {
		t.Fatalf("v1 phase = %s", inst.Phase)
	}
	failed := env.install(domain.CapabilitySlotASR)
	if failed.Phase != domain.InstallationPhaseFailed {
		t.Fatalf("v2 phase = %s, want failed", failed.Phase)
	}
	// 失败记录是最新记录，但当前版本只由 done 记录决定。
	capability := env.capability(domain.CapabilitySlotASR)
	if capability.State != domain.SlotStateReady || capability.Version != "1.0.0" {
		t.Fatalf("capability after failed upgrade = %s/%s, want READY/1.0.0", capability.State, capability.Version)
	}
	// 旧版本文件保持可用，失败记录仍可在安装列表读到 retryable。
	if _, err := os.Stat(filepath.Join(env.releaseDir(v1Plan.Release), "model.bin")); err != nil {
		t.Fatalf("previous version files missing: %v", err)
	}
	installs, err := env.svc.ListInstallations(context.Background())
	if err != nil {
		t.Fatalf("ListInstallations() error = %v", err)
	}
	if len(installs) == 0 || installs[0].ID != failed.ID || !installs[0].Retryable {
		t.Fatalf("failed record not surfaced first/retryable: %+v", installs)
	}
}

// TestRecoverInstallationsFailsInterrupted 覆盖重启对账：未完成安装收敛为可重试失败，
// 清理 `.part`，不触碰已 done 记录与其他槽位。
func TestRecoverInstallationsFailsInterrupted(t *testing.T) {
	env := newInstallTestEnv(t, StaticReleases{}, newTestDownloader())
	ctx := context.Background()
	now := time.Now().UTC()

	release := domain.ReleaseRef{ReleaseID: "rel-v9", Version: "9.0.0", Digest: "digest-v9"}
	stalled := domain.Installation{
		ID:        "inst-stalled",
		Slot:      domain.CapabilitySlotASR,
		Phase:     domain.InstallationPhaseDownloading,
		Progress:  42,
		PlanID:    "plan-stalled",
		PlanHash:  "hash-stalled",
		Release:   &release,
		PathOwner: "app",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := env.store.CreateInstallation(ctx, stalled); err != nil {
		t.Fatalf("CreateInstallation() error = %v", err)
	}
	versionDir := filepath.Join(env.dataDir, "models", "asr", "rel-v9")
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versionDir, "model.bin.part"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := domain.Installation{
		ID:            "inst-done",
		Slot:          domain.CapabilitySlotTTS,
		Phase:         domain.InstallationPhaseDone,
		Progress:      100,
		VerifiedFiles: 1,
		Release:       &domain.ReleaseRef{ReleaseID: "tts-v1", Version: "1.0.0", Digest: "tts-digest"},
		PathOwner:     "app",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := env.store.CreateInstallation(ctx, done); err != nil {
		t.Fatalf("CreateInstallation(done) error = %v", err)
	}

	if err := env.svc.RecoverInstallations(ctx); err != nil {
		t.Fatalf("RecoverInstallations() error = %v", err)
	}

	recovered, err := env.svc.GetInstallation(ctx, stalled.ID)
	if err != nil {
		t.Fatalf("GetInstallation() error = %v", err)
	}
	if recovered.Phase != domain.InstallationPhaseFailed || !recovered.Retryable {
		t.Fatalf("recovered installation = %s retryable=%v", recovered.Phase, recovered.Retryable)
	}
	if recovered.Error == nil || recovered.Error.MessageKey != "installation.interrupted_on_restart" {
		t.Fatalf("recovered error = %+v", recovered.Error)
	}
	if recovered.Progress != 42 {
		t.Fatalf("recovered progress = %d, want 42", recovered.Progress)
	}
	if _, err := os.Stat(filepath.Join(versionDir, "model.bin.part")); !os.IsNotExist(err) {
		t.Fatalf("partial file survived recovery: %v", err)
	}
	assertSubsequence(t, env.eventLabelsFor(stalled.ID),
		"phase_changed:failed",
		"installation.failed",
	)
	if capability := env.capability(domain.CapabilitySlotASR); capability.State != domain.SlotStateFailed {
		t.Fatalf("asr capability after recovery = %s, want FAILED", capability.State)
	}
	// 已 done 的槽位不受影响，恢复不重复处理终态记录。
	untouched, err := env.svc.GetInstallation(ctx, done.ID)
	if err != nil || untouched.Phase != domain.InstallationPhaseDone {
		t.Fatalf("done installation changed: %+v, err = %v", untouched, err)
	}
	if capability := env.capability(domain.CapabilitySlotTTS); capability.State != domain.SlotStateReady {
		t.Fatalf("tts capability after recovery = %s, want READY", capability.State)
	}
	if err := env.svc.RecoverInstallations(ctx); err != nil {
		t.Fatalf("second RecoverInstallations() error = %v", err)
	}
	recoveredAgain, err := env.svc.GetInstallation(ctx, stalled.ID)
	if err != nil || recoveredAgain.Phase != domain.InstallationPhaseFailed {
		t.Fatalf("second recovery changed terminal record: %+v, err = %v", recoveredAgain, err)
	}
}

// TestRecoverInstallationsInstallingFiles 覆盖启动对账对 installing 阶段的处理：
// 已验证文件完整落位时补写 done；文件缺失或验证证据不完整时仍收敛为可重试失败。
func TestRecoverInstallationsInstallingFiles(t *testing.T) {
	tests := []struct {
		name           string
		verified       int
		removeFile     bool
		wantPhase      domain.InstallationPhase
		wantError      string
		wantCapability domain.SlotState
		wantCompleted  bool
	}{
		{
			name:           "verified final files complete installation",
			verified:       1,
			wantPhase:      domain.InstallationPhaseDone,
			wantCapability: domain.SlotStateReady,
			wantCompleted:  true,
		},
		{
			name:           "missing final file fails as interrupted",
			verified:       1,
			removeFile:     true,
			wantPhase:      domain.InstallationPhaseFailed,
			wantError:      "installation.interrupted_on_restart",
			wantCapability: domain.SlotStateFailed,
		},
		{
			name:           "incomplete verification evidence is not promoted",
			verified:       0,
			wantPhase:      domain.InstallationPhaseFailed,
			wantError:      "installation.interrupted_on_restart",
			wantCapability: domain.SlotStateFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newInstallTestEnv(t, StaticReleases{}, newTestDownloader())
			release := domain.ReleaseRef{
				ReleaseID: "rel-recover",
				Version:   "1.0.0",
				Digest:    "digest-recover",
			}
			files := map[string][]byte{"model.bin": []byte("verified-model")}
			inst := seedInstallationFiles(
				t, env.store, env.dataDir, "inst-recover", domain.CapabilitySlotASR,
				release, files, domain.InstallationPhaseInstalling, tt.verified, time.Now().UTC(),
			)
			if tt.removeFile {
				if err := os.Remove(filepath.Join(env.releaseDir(release), "model.bin")); err != nil {
					t.Fatalf("remove final file: %v", err)
				}
			}

			if err := env.svc.RecoverInstallations(context.Background()); err != nil {
				t.Fatalf("RecoverInstallations() error = %v", err)
			}
			recovered, err := env.svc.GetInstallation(context.Background(), inst.ID)
			if err != nil {
				t.Fatalf("GetInstallation() error = %v", err)
			}
			if recovered.Phase != tt.wantPhase {
				t.Fatalf("recovered phase = %s, want %s", recovered.Phase, tt.wantPhase)
			}
			if tt.wantError != "" {
				if recovered.Error == nil || recovered.Error.MessageKey != tt.wantError {
					t.Fatalf("recovered error = %+v, want message %q", recovered.Error, tt.wantError)
				}
			}
			if capability := env.capability(domain.CapabilitySlotASR); capability.State != tt.wantCapability {
				t.Fatalf("asr capability = %s, want %s", capability.State, tt.wantCapability)
			}
			labels := env.eventLabelsFor(inst.ID)
			if len(labels) != 2 {
				t.Fatalf("installation events = %v, want exactly two", labels)
			}
			if tt.wantCompleted {
				if !recovered.ProbePassed {
					t.Fatal("recovered installation must record probe_passed")
				}
				if !domain.ContainsCapabilitySlot(recovered.CurrentSlots, domain.CapabilitySlotASR) {
					t.Fatalf("recovered current slots = %v, want ASR", recovered.CurrentSlots)
				}
				assertSubsequence(t, labels, "phase_changed:done", "installation.completed")
				// 已完成记录再次恢复必须幂等，不重复发事件或改状态。
				if err := env.svc.RecoverInstallations(context.Background()); err != nil {
					t.Fatalf("second RecoverInstallations() error = %v", err)
				}
				again, err := env.svc.GetInstallation(context.Background(), inst.ID)
				if err != nil || again.Phase != domain.InstallationPhaseDone {
					t.Fatalf("second recovery changed done record: %+v, err = %v", again, err)
				}
				if labelsAgain := env.eventLabelsFor(inst.ID); len(labelsAgain) != 2 {
					t.Fatalf("second recovery emitted events: %v", labelsAgain)
				}
				return
			}
			assertSubsequence(t, labels, "phase_changed:failed", "installation.failed")
		})
	}
}

// TestRecoverInstallationsCommitsVerifiedPart 覆盖 commit 中途退出：最终文件是旧的
// 同尺寸副本、`.part` 才是已验证内容时，启动对账应提交 `.part` 而不是信任旧文件。
func TestRecoverInstallationsCommitsVerifiedPart(t *testing.T) {
	env := newInstallTestEnv(t, StaticReleases{}, newTestDownloader())
	release := domain.ReleaseRef{ReleaseID: "rel-part", Version: "1.0.0", Digest: "digest-part"}
	files := map[string][]byte{"model.bin": []byte("verified-model")}
	inst := seedInstallationFiles(
		t, env.store, env.dataDir, "inst-part", domain.CapabilitySlotASR,
		release, files, domain.InstallationPhaseInstalling, 1, time.Now().UTC(),
	)
	root := env.releaseDir(release)
	if err := os.WriteFile(filepath.Join(root, "model.bin"), []byte("stale-model!!!"), 0o644); err != nil {
		t.Fatalf("write stale final file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "model.bin.part"), []byte("verified-model"), 0o644); err != nil {
		t.Fatalf("write verified part file: %v", err)
	}

	if err := env.svc.RecoverInstallations(context.Background()); err != nil {
		t.Fatalf("RecoverInstallations() error = %v", err)
	}
	recovered, err := env.svc.GetInstallation(context.Background(), inst.ID)
	if err != nil || recovered.Phase != domain.InstallationPhaseDone {
		t.Fatalf("recovered = %+v, err = %v; want done", recovered, err)
	}
	content, err := os.ReadFile(filepath.Join(root, "model.bin"))
	if err != nil || string(content) != "verified-model" {
		t.Fatalf("final file = %q, err = %v; want verified content", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, "model.bin.part")); err == nil {
		t.Fatal("committed .part file must be renamed away")
	}
	if capability := env.capability(domain.CapabilitySlotASR); capability.State != domain.SlotStateReady {
		t.Fatalf("asr capability = %s, want READY", capability.State)
	}
}

// TestRecoverInstallationsKeepsPartSuffixedManifestFile 覆盖清单最终文件本身以
// `.part` 结尾的情况：成功补齐后不得把合法最终文件当临时文件清理。
func TestRecoverInstallationsKeepsPartSuffixedManifestFile(t *testing.T) {
	env := newInstallTestEnv(t, StaticReleases{}, newTestDownloader())
	release := domain.ReleaseRef{ReleaseID: "rel-dotpart", Version: "1.0.0", Digest: "digest-dotpart"}
	files := map[string][]byte{"tokenizer.part": []byte("verified-model")}
	inst := seedInstallationFiles(
		t, env.store, env.dataDir, "inst-dotpart", domain.CapabilitySlotASR,
		release, files, domain.InstallationPhaseInstalling, 1, time.Now().UTC(),
	)
	root := env.releaseDir(release)

	if err := env.svc.RecoverInstallations(context.Background()); err != nil {
		t.Fatalf("RecoverInstallations() error = %v", err)
	}
	recovered, err := env.svc.GetInstallation(context.Background(), inst.ID)
	if err != nil || recovered.Phase != domain.InstallationPhaseDone {
		t.Fatalf("recovered = %+v, err = %v; want done", recovered, err)
	}
	content, err := os.ReadFile(filepath.Join(root, "tokenizer.part"))
	if err != nil || string(content) != "verified-model" {
		t.Fatalf("part-suffixed final file = %q, err = %v; must be kept", content, err)
	}
}

// TestRecoverInstallationsRejectsInvalidPart 覆盖 `.part` 大小与清单不符：不能提交，
// 仍按中断安装收敛为可重试失败。
func TestRecoverInstallationsRejectsInvalidPart(t *testing.T) {
	env := newInstallTestEnv(t, StaticReleases{}, newTestDownloader())
	release := domain.ReleaseRef{ReleaseID: "rel-badpart", Version: "1.0.0", Digest: "digest-badpart"}
	files := map[string][]byte{"model.bin": []byte("verified-model")}
	inst := seedInstallationFiles(
		t, env.store, env.dataDir, "inst-badpart", domain.CapabilitySlotASR,
		release, files, domain.InstallationPhaseInstalling, 1, time.Now().UTC(),
	)
	root := env.releaseDir(release)
	if err := os.Remove(filepath.Join(root, "model.bin")); err != nil {
		t.Fatalf("remove final file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "model.bin.part"), []byte("short"), 0o644); err != nil {
		t.Fatalf("write invalid part file: %v", err)
	}

	if err := env.svc.RecoverInstallations(context.Background()); err != nil {
		t.Fatalf("RecoverInstallations() error = %v", err)
	}
	recovered, err := env.svc.GetInstallation(context.Background(), inst.ID)
	if err != nil || recovered.Phase != domain.InstallationPhaseFailed {
		t.Fatalf("recovered = %+v, err = %v; want failed", recovered, err)
	}
	if recovered.Error == nil || recovered.Error.MessageKey != "installation.interrupted_on_restart" {
		t.Fatalf("recovered error = %+v, want interrupted_on_restart", recovered.Error)
	}
	if _, err := os.Stat(filepath.Join(root, "model.bin.part")); err != nil {
		t.Fatalf("installing failure must keep .part for retry: %v", err)
	}
}

// TestRecoverInstallationsRejectsManifestAliases 覆盖清单路径别名：同一条物理文件
// 被规范化为不同字符串，或大小写变体在大小写不敏感文件系统上指向同一文件时，
// 不得自动完成。
func TestRecoverInstallationsRejectsManifestAliases(t *testing.T) {
	tests := []struct {
		name  string
		files map[string][]byte
	}{
		{
			name:  "dot segment aliases part staging",
			files: map[string][]byte{"x": []byte("aaa"), "./x.part": []byte("bbb")},
		},
		{
			name:  "case variant aliases",
			files: map[string][]byte{"MODEL.BIN": []byte("aaa"), "model.bin": []byte("bbb")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newInstallTestEnv(t, StaticReleases{}, newTestDownloader())
			release := domain.ReleaseRef{ReleaseID: "rel-alias", Version: "1.0.0", Digest: "digest-alias"}
			inst := seedInstallationFiles(
				t, env.store, env.dataDir, "inst-alias", domain.CapabilitySlotASR,
				release, tt.files, domain.InstallationPhaseInstalling, len(tt.files), time.Now().UTC(),
			)

			if err := env.svc.RecoverInstallations(context.Background()); err != nil {
				t.Fatalf("RecoverInstallations() error = %v", err)
			}
			recovered, err := env.svc.GetInstallation(context.Background(), inst.ID)
			if err != nil || recovered.Phase != domain.InstallationPhaseFailed {
				t.Fatalf("recovered = %+v, err = %v; want failed", recovered, err)
			}
			if recovered.Error == nil || recovered.Error.MessageKey != "installation.interrupted_on_restart" {
				t.Fatalf("recovered error = %+v, want interrupted_on_restart", recovered.Error)
			}
		})
	}
}

// TestPlanInstallationRejectsReleaseWithoutManifest 覆盖无文件清单的发布不生成计划。
func TestPlanInstallationRejectsReleaseWithoutManifest(t *testing.T) {
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotASR: {
			Release: domain.ReleaseRef{ReleaseID: "rel-empty", Version: "1.0.0", Digest: "digest"},
		},
	}}, nil)
	_, err := env.svc.PlanInstallation(context.Background(), domain.CapabilitySlotASR)
	if err == nil {
		t.Fatal("PlanInstallation() succeeded without files")
	}
	if code, ok := domain.CodeOf(err); !ok || code != domain.ErrorCodeCapabilityUnverified {
		t.Fatalf("error = %v, code = %v", err, code)
	}
}
