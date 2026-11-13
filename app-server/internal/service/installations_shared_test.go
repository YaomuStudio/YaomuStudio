package service

// 共享发布安装：一次物理安装服务多个槽位、并发 join、失败/取消、删除与
// 回滚的单槽语义。使用真实 downloader/本地受控源与真实文件字节。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/downloader"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// countingSource 是固定字节的本地受控源，并记录实际 GET 次数。
func countingSource(t *testing.T, contents map[string][]byte, gets *int32, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, ok := contents[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if mu != nil {
			mu.Lock()
			*gets = *gets + 1
			mu.Unlock()
		}
		http.ServeContent(w, r, filepath.Base(r.URL.Path), time.Time{}, bytes.NewReader(content))
	}))
	t.Cleanup(server.Close)
	return server
}

// slotSequenceReleases 按槽位依次返回预置发布，再回退到固定发布；模拟升级。
type slotSequenceReleases struct {
	mu    sync.Mutex
	plans map[domain.CapabilitySlotKey][]ReleasePlan
	next  map[domain.CapabilitySlotKey]int
}

func (s *slotSequenceReleases) Resolve(_ context.Context, slot domain.CapabilitySlotKey) (ReleasePlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next == nil {
		s.next = make(map[domain.CapabilitySlotKey]int)
	}
	queue := s.plans[slot]
	index := s.next[slot]
	if index >= len(queue) {
		return ReleasePlan{}, fmt.Errorf("test catalog exhausted for slot %s", slot)
	}
	s.next[slot] = index + 1
	return queue[index], nil
}

func withCoverage(plan ReleasePlan, slots ...domain.CapabilitySlotKey) ReleasePlan {
	plan.CoveredSlots = slots
	return plan
}

func (e *installTestEnv) installShared(slot domain.CapabilitySlotKey) (domain.InstallationPlan, domain.Installation) {
	e.t.Helper()
	plan, err := e.svc.PlanInstallation(context.Background(), slot)
	if err != nil {
		e.t.Fatalf("PlanInstallation(%s) error = %v", slot, err)
	}
	resp, err := e.svc.CreateInstallation(context.Background(), Idem{}, CreateInstallationRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
	})
	if err != nil {
		e.t.Fatalf("CreateInstallation(%s) error = %v", slot, err)
	}
	inst := resp.Body.(domain.Installation)
	return plan, e.waitTerminal(inst.ID)
}

func (e *installTestEnv) installs() []domain.Installation {
	e.t.Helper()
	installs, err := e.svc.ListInstallations(context.Background())
	if err != nil {
		e.t.Fatalf("ListInstallations() error = %v", err)
	}
	return installs
}

func containsSlot(slots []domain.CapabilitySlotKey, slot domain.CapabilitySlotKey) bool {
	for _, candidate := range slots {
		if candidate == slot {
			return true
		}
	}
	return false
}

// TestSharedReleaseInstallsOnceAndAdmitsAllCoveredSlots 覆盖验收主路径：
// 目录声明 TTS/voice-clone，一次安装、一次传输、一个实体同时准入两槽；
// 由另一槽复用时不新造 Done、不改原操作 slot/plan_hash，也不新增下载。
func TestSharedReleaseInstallsOnceAndAdmitsAllCoveredSlots(t *testing.T) {
	payload := bytes.Repeat([]byte("shared-release-bytes"), 64)
	var gets int32
	var mu sync.Mutex
	server := countingSource(t, map[string][]byte{"/rel-shared/model.bin": payload}, &gets, &mu)
	plan := withCoverage(releasePlanFor(server, "rel-shared", "1.0.0",
		map[string][]byte{"model.bin": payload}), domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone)
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotTTS:        plan,
		domain.CapabilitySlotVoiceClone: plan,
	}}, newTestDownloader(server.URL))

	ttsPlan, inst := env.installShared(domain.CapabilitySlotTTS)
	if inst.Phase != domain.InstallationPhaseDone {
		t.Fatalf("installation phase = %s (%+v)", inst.Phase, inst.Error)
	}
	if !sameCapabilitySlotSet(ttsPlan.CoveredSlots, []domain.CapabilitySlotKey{domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone}) {
		t.Fatalf("plan covered_slots = %v", ttsPlan.CoveredSlots)
	}
	if ttsPlan.DownloadBytes != int64(len(payload)) {
		t.Fatalf("plan download bytes = %d, want one physical copy %d", ttsPlan.DownloadBytes, len(payload))
	}
	if !sameCapabilitySlotSet(inst.CoveredSlots, ttsPlan.CoveredSlots) ||
		!sameCapabilitySlotSet(inst.BoundSlots, ttsPlan.CoveredSlots) ||
		!sameCapabilitySlotSet(inst.CurrentSlots, ttsPlan.CoveredSlots) {
		t.Fatalf("installation projections = covered %v bound %v current %v",
			inst.CoveredSlots, inst.BoundSlots, inst.CurrentSlots)
	}
	for _, slot := range []domain.CapabilitySlotKey{domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone} {
		if got := env.capability(slot); got.State != domain.SlotStateReady {
			t.Fatalf("capability %s = %s, want READY", slot, got.State)
		}
	}
	if got := gets; got != 1 {
		t.Fatalf("physical GET count = %d, want 1", got)
	}
	if installs := env.installs(); len(installs) != 1 {
		t.Fatalf("physical records = %d, want 1", len(installs))
	}
	root := env.releaseDir(plan.Release)
	if content, err := os.ReadFile(filepath.Join(root, "model.bin")); err != nil || !bytes.Equal(content, payload) {
		t.Fatalf("installed bytes missing/mismatched: %v", err)
	}

	// 另一覆盖槽位重新 plan：指向同一物理实体，零新增下载/暂存。
	clonePlan, err := env.svc.PlanInstallation(context.Background(), domain.CapabilitySlotVoiceClone)
	if err != nil {
		t.Fatalf("PlanInstallation(voice-clone) error = %v", err)
	}
	if clonePlan.ReuseInstallationID != inst.ID {
		t.Fatalf("reuse target = %q, want %q", clonePlan.ReuseInstallationID, inst.ID)
	}
	if clonePlan.DownloadBytes != 0 || clonePlan.PeakStagingBytes != 0 || clonePlan.CacheReuseBytes != plan.InstallBytes {
		t.Fatalf("done reuse sizes = download %d peak %d reuse %d", clonePlan.DownloadBytes,
			clonePlan.PeakStagingBytes, clonePlan.CacheReuseBytes)
	}
	resp, err := env.svc.CreateInstallation(context.Background(), Idem{Key: "clone-reuse", Digest: "d1"},
		CreateInstallationRequest{PlanID: clonePlan.PlanID, PlanHash: clonePlan.PlanHash})
	if err != nil {
		t.Fatalf("CreateInstallation(voice-clone reuse) error = %v", err)
	}
	reused := resp.Body.(domain.Installation)
	if reused.ID != inst.ID {
		t.Fatalf("reuse returned id %s, want same physical id %s", reused.ID, inst.ID)
	}
	// 物理最初操作的 slot/plan_hash 不被新请求改写；新计划哈希只由自身回执绑定。
	if reused.Slot != domain.CapabilitySlotTTS {
		t.Fatalf("reused installation slot = %s, want original initiator tts", reused.Slot)
	}
	if reused.PlanHash != inst.PlanHash || reused.PlanHash == clonePlan.PlanHash {
		t.Fatalf("reused plan_hash = %s, want original %s (not new plan %s)",
			reused.PlanHash, inst.PlanHash, clonePlan.PlanHash)
	}
	if installs := env.installs(); len(installs) != 1 {
		t.Fatalf("reuse created a fake Done row: %d records", len(installs))
	}
	if got := gets; got != 1 {
		t.Fatalf("reuse performed another transfer: GET count = %d", got)
	}
}

// TestConcurrentCoveredSlotPlansJoinSameInstallation 覆盖两槽并发 join：
// 同一 ID、同一进度/传输，取消作用于整个物理安装，旧当前版本不受影响。
func TestConcurrentCoveredSlotPlansJoinSameInstallation(t *testing.T) {
	payload := []byte("join-payload")
	release := domain.ReleaseRef{ReleaseID: "rel-join", Version: "1.0.0", Digest: "sha256:join"}
	dl := &gatedDownloader{entered: make(chan struct{}, 4), release: make(chan struct{})}
	plan := withCoverage(ReleasePlan{
		Release: release,
		Files: []downloader.File{{
			RelPath: "model.bin", URL: "https://releases.invalid/join/model.bin",
			SHA256: testSHA256(payload), Size: int64(len(payload)),
		}},
		DownloadBytes: int64(len(payload)),
		InstallBytes:  int64(len(payload)),
		Permissions:   []string{"filesystem_write"},
	}, domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone)
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotTTS:        plan,
		domain.CapabilitySlotVoiceClone: plan,
	}}, dl)

	firstPlan, err := env.svc.PlanInstallation(context.Background(), domain.CapabilitySlotTTS)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := env.svc.CreateInstallation(context.Background(), Idem{}, CreateInstallationRequest{
		PlanID: firstPlan.PlanID, PlanHash: firstPlan.PlanHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := resp.Body.(domain.Installation)
	select {
	case <-dl.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("installation transfer did not start")
	}

	clonePlan, err := env.svc.PlanInstallation(context.Background(), domain.CapabilitySlotVoiceClone)
	if err != nil {
		t.Fatal(err)
	}
	if clonePlan.ReuseInstallationID != first.ID {
		t.Fatalf("active join reuse target = %q, want %s", clonePlan.ReuseInstallationID, first.ID)
	}
	second, err := env.svc.CreateInstallation(context.Background(), Idem{}, CreateInstallationRequest{
		PlanID: clonePlan.PlanID, PlanHash: clonePlan.PlanHash,
	})
	if err != nil {
		t.Fatalf("join create error = %v", err)
	}
	if joined := second.Body.(domain.Installation); joined.ID != first.ID {
		t.Fatalf("join created second operation %s, want %s", joined.ID, first.ID)
	}
	if installs := env.installs(); len(installs) != 1 {
		t.Fatalf("physical records = %d, want 1", len(installs))
	}

	// 取消按物理安装 ID 影响全部覆盖槽位；两槽都回到未就绪（没有旧当前版本）。
	if _, err := env.svc.CancelInstallation(context.Background(), Idem{}, first.ID); err != nil {
		t.Fatal(err)
	}
	close(dl.release)
	env.waitTerminal(first.ID)
	for _, slot := range []domain.CapabilitySlotKey{domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone} {
		if got := env.capability(slot); got.State == domain.SlotStateReady {
			t.Fatalf("canceled shared installation left %s READY", slot)
		}
	}
}

// TestOverlappingCoveredSlotsDifferentReleaseRejected 覆盖不同发布的重叠槽位
// 并发安装拒绝（晚完成的旧版本不得静默降级新选择）。
func TestOverlappingCoveredSlotsDifferentReleaseRejected(t *testing.T) {
	first := withCoverage(ReleasePlan{
		Release: domain.ReleaseRef{ReleaseID: "rel-a", Version: "1.0.0", Digest: "sha256:a"},
		Files: []downloader.File{{
			RelPath: "model.bin", URL: "https://releases.invalid/a/model.bin",
			SHA256: strings.Repeat("a", 64), Size: 4,
		}},
	}, domain.CapabilitySlotTTS)
	second := withCoverage(ReleasePlan{
		Release: domain.ReleaseRef{ReleaseID: "rel-b", Version: "2.0.0", Digest: "sha256:b"},
		Files: []downloader.File{{
			RelPath: "model.bin", URL: "https://releases.invalid/b/model.bin",
			SHA256: strings.Repeat("b", 64), Size: 4,
		}},
	}, domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone)
	catalog := &slotSequenceReleases{plans: map[domain.CapabilitySlotKey][]ReleasePlan{
		domain.CapabilitySlotTTS:        {first, second},
		domain.CapabilitySlotVoiceClone: {second},
	}}
	dl := &gatedDownloader{entered: make(chan struct{}, 4), release: make(chan struct{})}
	env := newInstallTestEnv(t, catalog, dl)
	t.Cleanup(func() { close(dl.release) })

	planA, err := env.svc.PlanInstallation(context.Background(), domain.CapabilitySlotTTS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.CreateInstallation(context.Background(), Idem{}, CreateInstallationRequest{
		PlanID: planA.PlanID, PlanHash: planA.PlanHash,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-dl.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first installation did not start")
	}
	planB, err := env.svc.PlanInstallation(context.Background(), domain.CapabilitySlotVoiceClone)
	if err != nil {
		t.Fatal(err)
	}
	if planB.ReuseInstallationID != "" {
		t.Fatalf("different release unexpectedly reused %q", planB.ReuseInstallationID)
	}
	_, err = env.svc.CreateInstallation(context.Background(), Idem{}, CreateInstallationRequest{
		PlanID: planB.PlanID, PlanHash: planB.PlanHash,
	})
	if err == nil {
		t.Fatal("overlapping different release installation was accepted")
	}
	if code, ok := domain.CodeOf(err); !ok || code != domain.ErrorCodeRevisionConflict {
		t.Fatalf("overlap error = %v, want resource-busy conflict", err)
	}
}

// TestFailedSharedUpgradeKeepsBothCurrentVersions 覆盖失败升级不半激活：
// 新共享记录失败后，两个槽位仍选中旧 Done 实体且文件保留。
func TestFailedSharedUpgradeKeepsBothCurrentVersions(t *testing.T) {
	v1 := []byte("shared-v1")
	tampered := bytes.Repeat([]byte("x"), len(v1))
	server := staticSource(t, map[string][]byte{
		"/rel-v1/model.bin": v1,
		"/rel-v2/model.bin": tampered,
	})
	v1Plan := withCoverage(releasePlanFor(server, "rel-v1", "1.0.0", map[string][]byte{"model.bin": v1}),
		domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone)
	// v2 清单哈希按正常内容计算，源返回篡改字节 → verify 失败。
	v2Plan := withCoverage(releasePlanFor(server, "rel-v2", "2.0.0", map[string][]byte{"model.bin": v1}),
		domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone)
	catalog := &slotSequenceReleases{plans: map[domain.CapabilitySlotKey][]ReleasePlan{
		domain.CapabilitySlotTTS:        {v1Plan, v2Plan},
		domain.CapabilitySlotVoiceClone: {v2Plan},
	}}
	env := newInstallTestEnv(t, catalog, newTestDownloader(server.URL))

	_, first := env.installShared(domain.CapabilitySlotTTS)
	if first.Phase != domain.InstallationPhaseDone {
		t.Fatalf("v1 phase = %s", first.Phase)
	}
	upgrade, err := env.svc.PlanInstallation(context.Background(), domain.CapabilitySlotVoiceClone)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := env.svc.CreateInstallation(context.Background(), Idem{}, CreateInstallationRequest{
		PlanID: upgrade.PlanID, PlanHash: upgrade.PlanHash,
	})
	if err != nil {
		t.Fatalf("v2 create error = %v", err)
	}
	failed := env.waitTerminal(resp.Body.(domain.Installation).ID)
	if failed.Phase != domain.InstallationPhaseFailed {
		t.Fatalf("v2 phase = %s, want failed", failed.Phase)
	}
	if len(failed.CurrentSlots) != 0 {
		t.Fatalf("failed upgrade half-activated: current_slots = %v", failed.CurrentSlots)
	}
	for _, slot := range []domain.CapabilitySlotKey{domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone} {
		got := env.capability(slot)
		if got.State != domain.SlotStateReady || got.Version != "1.0.0" {
			t.Fatalf("capability %s after failed upgrade = %s/%s, want READY/1.0.0", slot, got.State, got.Version)
		}
	}
	if _, err := os.Stat(filepath.Join(env.releaseDir(v1Plan.Release), "model.bin")); err != nil {
		t.Fatalf("previous shared bytes removed: %v", err)
	}
}

// TestDeleteOneSharedSlotKeepsBytesAndLastReferenceCollects 覆盖删除的单槽语义、
// 最后引用回收、幂等重放不删除后来新安装。
func TestDeleteOneSharedSlotKeepsBytesAndLastReferenceCollects(t *testing.T) {
	payload := []byte("delete-shared")
	server := staticSource(t, map[string][]byte{"/rel-shared/model.bin": payload})
	plan := withCoverage(releasePlanFor(server, "rel-shared", "1.0.0", map[string][]byte{"model.bin": payload}),
		domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone)
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotTTS:        plan,
		domain.CapabilitySlotVoiceClone: plan,
	}}, newTestDownloader(server.URL))

	if _, inst := env.installShared(domain.CapabilitySlotTTS); inst.Phase != domain.InstallationPhaseDone {
		t.Fatalf("install phase = %s", inst.Phase)
	}
	root := env.releaseDir(plan.Release)
	deleteIdem := Idem{Key: "delete-tts", Digest: "d-delete"}

	if _, err := env.svc.DeleteInstalledPackage(context.Background(), deleteIdem, "tts"); err != nil {
		t.Fatalf("delete tts error = %v", err)
	}
	if got := env.capability(domain.CapabilitySlotTTS); got.State != domain.SlotStateNeedsSetup {
		t.Fatalf("deleted tts state = %s, want NEEDS_SETUP", got.State)
	}
	if got := env.capability(domain.CapabilitySlotVoiceClone); got.State != domain.SlotStateReady {
		t.Fatalf("voice-clone state after tts delete = %s, want READY", got.State)
	}
	if _, err := os.Stat(filepath.Join(root, "model.bin")); err != nil {
		t.Fatalf("shared bytes removed while another slot retains them: %v", err)
	}
	installs := env.installs()
	if len(installs) != 1 || !sameCapabilitySlotSet(installs[0].BoundSlots, []domain.CapabilitySlotKey{domain.CapabilitySlotVoiceClone}) {
		t.Fatalf("shared entity after tts detach = %+v", installs)
	}
	if !sameCapabilitySlotSet(installs[0].CoveredSlots, plan.CoveredSlots) {
		t.Fatalf("covered_slots history was rewritten: %v", installs[0].CoveredSlots)
	}

	// 重装（同精确发布）后重放旧删除：不产生第二次文件删除。
	_, reinstall := env.installShared(domain.CapabilitySlotTTS)
	if reinstall.Phase != domain.InstallationPhaseDone {
		t.Fatalf("reinstall phase = %s", reinstall.Phase)
	}
	if _, err := env.svc.DeleteInstalledPackage(context.Background(), deleteIdem, "tts"); err != nil {
		t.Fatalf("replayed delete error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "model.bin")); err != nil {
		t.Fatalf("idempotent replay removed newly installed bytes: %v", err)
	}
	if got := env.capability(domain.CapabilitySlotTTS); got.State != domain.SlotStateReady {
		t.Fatalf("tts after delete replay = %s, want READY", got.State)
	}

	// 移除最后一个引用后才回收物理目录；命名空间目录保留给清理识别。
	if _, err := env.svc.DeleteInstalledPackage(context.Background(), Idem{}, "tts"); err != nil {
		t.Fatalf("delete tts error = %v", err)
	}
	if _, err := env.svc.DeleteInstalledPackage(context.Background(), Idem{}, "voice-clone"); err != nil {
		t.Fatalf("delete voice-clone error = %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("last reference removal did not collect physical root: %v", err)
	}
	if info, err := os.Stat(filepath.Join(env.dataDir, "models", "releases")); err != nil || !info.IsDir() {
		t.Fatalf("shared releases namespace removed: %v", err)
	}
}

// TestRollbackOnlyRequestedSlot 覆盖回滚只切请求槽位，不复制 Done、不改其他槽。
func TestRollbackOnlyRequestedSlot(t *testing.T) {
	v1 := []byte("shared-rollback-v1")
	v2 := []byte("shared-rollback-v2")
	server := staticSource(t, map[string][]byte{
		"/rel-v1/model.bin": v1,
		"/rel-v2/model.bin": v2,
	})
	v1Plan := withCoverage(releasePlanFor(server, "rel-v1", "1.0.0", map[string][]byte{"model.bin": v1}),
		domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone)
	v2Plan := withCoverage(releasePlanFor(server, "rel-v2", "2.0.0", map[string][]byte{"model.bin": v2}),
		domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone)
	catalog := &slotSequenceReleases{plans: map[domain.CapabilitySlotKey][]ReleasePlan{
		domain.CapabilitySlotTTS:        {v1Plan},
		domain.CapabilitySlotVoiceClone: {v2Plan},
	}}
	env := newInstallTestEnv(t, catalog, newTestDownloader(server.URL))

	_, first := env.installShared(domain.CapabilitySlotTTS)
	if first.Phase != domain.InstallationPhaseDone {
		t.Fatalf("v1 phase = %s", first.Phase)
	}
	_, second := env.installShared(domain.CapabilitySlotVoiceClone)
	if second.Phase != domain.InstallationPhaseDone {
		t.Fatalf("v2 phase = %s", second.Phase)
	}
	resp, err := env.svc.RollbackInstalledPackage(context.Background(), Idem{}, "tts")
	if err != nil {
		t.Fatalf("rollback error = %v", err)
	}
	rolled := resp.Body.(domain.Installation)
	if rolled.ID != first.ID {
		t.Fatalf("rollback returned %s, want retained entity %s", rolled.ID, first.ID)
	}
	installs := env.installs()
	if len(installs) != 2 {
		t.Fatalf("rollback created a Done copy: %d records", len(installs))
	}
	tts := env.capability(domain.CapabilitySlotTTS)
	clone := env.capability(domain.CapabilitySlotVoiceClone)
	if tts.State != domain.SlotStateReady || tts.Version != "1.0.0" {
		t.Fatalf("tts after rollback = %s/%s", tts.State, tts.Version)
	}
	if clone.State != domain.SlotStateReady || clone.Version != "2.0.0" {
		t.Fatalf("voice-clone must keep its current version, got %s/%s", clone.State, clone.Version)
	}
	for _, plan := range []ReleasePlan{v1Plan, v2Plan} {
		if _, err := os.Stat(filepath.Join(env.releaseDir(plan.Release), "model.bin")); err != nil {
			t.Fatalf("retained version files missing: %v", err)
		}
	}
}

// gatedDownloader 让 worker 停在传输中，直到测试放行。
type gatedDownloader struct {
	entered chan struct{}
	release chan struct{}
}

func (d *gatedDownloader) Fetch(ctx context.Context, _ string, _ downloader.File, _ func(int64, int64)) error {
	select {
	case d.entered <- struct{}{}:
	default:
	}
	select {
	case <-d.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *gatedDownloader) Verify(context.Context, string, downloader.File) error { return nil }
func (d *gatedDownloader) Commit(string, downloader.File) error                  { return nil }
func (d *gatedDownloader) Cleanup(string) error                                  { return nil }

// tailDownloader 在取消后仍占用物理根，直到测试显式放行（模拟 worker 尾部）。
type tailDownloader struct {
	entered chan struct{}
	release chan struct{}
}

func (d *tailDownloader) Fetch(_ context.Context, _ string, _ downloader.File, _ func(int64, int64)) error {
	select {
	case d.entered <- struct{}{}:
	default:
	}
	<-d.release
	return nil
}

func (d *tailDownloader) Verify(context.Context, string, downloader.File) error { return nil }
func (d *tailDownloader) Commit(string, downloader.File) error                  { return nil }
func (d *tailDownloader) Cleanup(string) error                                  { return nil }

// TestCanceledWorkerTailBlocksNewWriterAndDelete 覆盖取消尾部仍拥有物理根：
// 新 writer/删除在尾部退出前被拒绝，退出后才允许。
func TestCanceledWorkerTailBlocksNewWriterAndDelete(t *testing.T) {
	payload := []byte("tail-payload")
	dl := &tailDownloader{entered: make(chan struct{}, 4), release: make(chan struct{})}
	plan := withCoverage(ReleasePlan{
		Release: domain.ReleaseRef{ReleaseID: "rel-tail", Version: "1.0.0", Digest: "sha256:tail"},
		Files: []downloader.File{{
			RelPath: "model.bin", URL: "https://releases.invalid/tail/model.bin",
			SHA256: testSHA256(payload), Size: int64(len(payload)),
		}},
	}, domain.CapabilitySlotTTS)
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotTTS: plan,
	}}, dl)
	firstPlan, err := env.svc.PlanInstallation(context.Background(), domain.CapabilitySlotTTS)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := env.svc.CreateInstallation(context.Background(), Idem{}, CreateInstallationRequest{
		PlanID: firstPlan.PlanID, PlanHash: firstPlan.PlanHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	inst := resp.Body.(domain.Installation)
	select {
	case <-dl.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("transfer did not start")
	}
	if _, err := env.svc.CancelInstallation(context.Background(), Idem{}, inst.ID); err != nil {
		t.Fatal(err)
	}
	env.waitTerminal(inst.ID)
	if !env.svc.ActiveInstallationWork() {
		t.Fatal("canceled worker tail not tracked as active owner")
	}
	if _, err := env.svc.DeleteInstalledPackage(context.Background(), Idem{}, "tts"); err == nil {
		t.Fatal("delete accepted while canceled worker tail still owns the root")
	}
	if _, err := env.svc.CleanupStorage(context.Background(), Idem{}, StorageCleanupRequest{
		Category: domain.StorageCategoryModels,
	}); err == nil {
		t.Fatal("model cleanup accepted while installer tail still owns files")
	}
	secondPlan, err := env.svc.PlanInstallation(context.Background(), domain.CapabilitySlotTTS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.CreateInstallation(context.Background(), Idem{}, CreateInstallationRequest{
		PlanID: secondPlan.PlanID, PlanHash: secondPlan.PlanHash,
	}); err == nil {
		t.Fatal("new writer accepted while canceled worker tail still owns the root")
	}

	close(dl.release)
	deadline := time.Now().Add(5 * time.Second)
	for env.svc.ActiveInstallationWork() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if env.svc.ActiveInstallationWork() {
		t.Fatal("worker tail did not exit")
	}
	if _, err := env.svc.DeleteInstalledPackage(context.Background(), Idem{}, "tts"); err != nil {
		t.Fatalf("delete after worker tail exit error = %v", err)
	}
}

// TestStorageCleanupKeepsSharedReleasesAndCountsOnce 覆盖清理理解共享命名空间。
func TestStorageCleanupKeepsSharedReleasesAndCountsOnce(t *testing.T) {
	payload := []byte("cleanup-shared")
	server := staticSource(t, map[string][]byte{"/rel-shared/model.bin": payload})
	plan := withCoverage(releasePlanFor(server, "rel-shared", "1.0.0", map[string][]byte{"model.bin": payload}),
		domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone)
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotTTS:        plan,
		domain.CapabilitySlotVoiceClone: plan,
	}}, newTestDownloader(server.URL))
	if _, inst := env.installShared(domain.CapabilitySlotTTS); inst.Phase != domain.InstallationPhaseDone {
		t.Fatalf("install phase = %s", inst.Phase)
	}
	// 一个无引用的共享目录可被回收，但有引用的实体必须保留。
	unused := filepath.Join(env.dataDir, "models", "releases", strings.Repeat("0", 64))
	if err := os.MkdirAll(unused, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unused, "stale.bin"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.CleanupStorage(context.Background(), Idem{}, StorageCleanupRequest{
		Category: domain.StorageCategoryModels,
	}); err != nil {
		t.Fatalf("CleanupStorage error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.releaseDir(plan.Release), "model.bin")); err != nil {
		t.Fatalf("cleanup removed referenced shared bytes: %v", err)
	}
	if _, err := os.Stat(unused); !os.IsNotExist(err) {
		t.Fatalf("cleanup kept unreferenced shared root: %v", err)
	}
	usage, err := env.svc.StorageUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if usage.ModelsBytes != int64(len(payload)) {
		t.Fatalf("models usage = %d, want single physical tree %d", usage.ModelsBytes, len(payload))
	}
}

// TestSharedInstallationDiscoveryAfterDetachOnlyUsesBoundRefs 覆盖已解绑槽位
// 不会因为 covered_slots 历史范围而复活。
func TestSharedInstallationDiscoveryAfterDetachOnlyUsesBoundRefs(t *testing.T) {
	payload := []byte("bound-only")
	server := staticSource(t, map[string][]byte{"/rel-shared/model.bin": payload})
	plan := withCoverage(releasePlanFor(server, "rel-shared", "1.0.0", map[string][]byte{"model.bin": payload}),
		domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone)
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotTTS:        plan,
		domain.CapabilitySlotVoiceClone: plan,
	}}, newTestDownloader(server.URL))
	_, inst := env.installShared(domain.CapabilitySlotTTS)
	if _, err := env.svc.DeleteInstalledPackage(context.Background(), Idem{}, "tts"); err != nil {
		t.Fatal(err)
	}
	stored, err := env.store.GetInstallation(context.Background(), inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if containsSlot(stored.CoveredSlots, domain.CapabilitySlotTTS) != true {
		t.Fatalf("covered history lost: %v", stored.CoveredSlots)
	}
	if containsSlot(stored.BoundSlots, domain.CapabilitySlotTTS) || containsSlot(stored.CurrentSlots, domain.CapabilitySlotTTS) {
		t.Fatalf("detached slot revived through projections: bound %v current %v", stored.BoundSlots, stored.CurrentSlots)
	}
	caps, err := env.svc.ListCapabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range caps {
		if capability.Slot == domain.CapabilitySlotTTS && capability.State == domain.SlotStateReady {
			t.Fatal("detached tts reported READY from covered_slots history")
		}
	}
}

// TestDeleteRejectsNonterminalRunFrozenLock 覆盖可推进 Run 的精确冻结锁保护。
func TestDeleteRejectsNonterminalRunFrozenLock(t *testing.T) {
	payload := []byte("run-lock")
	server := staticSource(t, map[string][]byte{"/rel-asr/model.bin": payload})
	plan := releasePlanFor(server, "rel-asr", "1.0.0", map[string][]byte{"model.bin": payload})
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotASR: plan,
	}}, newTestDownloader(server.URL))
	_, inst := env.installShared(domain.CapabilitySlotASR)
	if inst.Release == nil {
		t.Fatal("installed record missing release")
	}
	now := time.Now().UTC()
	project := domain.Project{
		ID: store.NewID("proj"), WorkspaceID: "ws-install", Name: "run-lock",
		SchemaVersion: 1, EditVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := env.store.CreateProject(context.Background(), project); err != nil {
		t.Fatalf("CreateProject error = %v", err)
	}
	run := domain.WorkflowRun{
		ID: "run-lock", ProjectID: project.ID, Name: "lock", Template: "tool_translate",
		Status: domain.RunStatusQueued, SourceLanguage: "und", Languages: []string{},
		Config: domain.RunConfig{Execution: map[domain.CapabilitySlotKey]domain.ExecutionConfig{
			domain.CapabilitySlotASR: {ModelLock: *inst.Release, DeviceID: "cpu", Params: map[string]any{}},
		}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := env.store.CreateWorkflowRun(context.Background(), run); err != nil {
		t.Fatalf("CreateWorkflowRun error = %v", err)
	}
	if _, err := env.svc.DeleteInstalledPackage(context.Background(), Idem{}, "asr"); err == nil {
		t.Fatal("delete accepted while a nonterminal run freezes the release")
	}
	if code, ok := domain.CodeOf(func() error {
		_, err := env.svc.DeleteInstalledPackage(context.Background(), Idem{}, "asr")
		return err
	}()); !ok || code != domain.ErrorCodeRevisionConflict {
		t.Fatalf("protected delete error code = %v, want conflict", code)
	}
}

// TestDeleteAfterDetachRejectsHistoricalRetryDispatch 覆盖历史失败任务显式
// 删除安装后，重试派发仍按缺失精确锁失败，不替换为新版本。
func TestDeleteAfterDetachRejectsHistoricalRetryDispatch(t *testing.T) {
	payload := []byte("retained-lock")
	server := staticSource(t, map[string][]byte{"/rel-v1/model.bin": payload})
	v1 := releasePlanFor(server, "rel-v1", "1.0.0", map[string][]byte{"model.bin": payload})
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotVoiceClone: v1,
	}}, newTestDownloader(server.URL))
	env.svc.opts.Capabilities = InstallationCapabilities{}
	_, inst := env.installShared(domain.CapabilitySlotVoiceClone)
	if inst.Phase != domain.InstallationPhaseDone || inst.Release == nil {
		t.Fatalf("installation = %+v", inst)
	}
	run := domain.WorkflowRun{Config: domain.RunConfig{Execution: map[domain.CapabilitySlotKey]domain.ExecutionConfig{
		domain.CapabilitySlotVoiceClone: {ModelLock: *inst.Release, DeviceID: "cpu", Params: map[string]any{}},
	}}}
	task := frozenTask(t, run, domain.NodeKeyClone, domain.TaskKindNode, map[string]any{"language": "zh"})
	if err := env.svc.CheckTaskDispatchable(context.Background(), env.store, task); err != nil {
		t.Fatalf("dispatch with retained evidence error = %v", err)
	}
	if _, err := env.svc.DeleteInstalledPackage(context.Background(), Idem{}, "voice-clone"); err != nil {
		t.Fatalf("delete error = %v", err)
	}
	if err := env.svc.CheckTaskDispatchable(context.Background(), env.store, task); err == nil {
		t.Fatal("historical task dispatch was re-admitted after explicit deletion")
	}
}

// TestRecoveryUsesSharedAndLegacyRoots 覆盖重启清理同时理解新/旧布局。
func TestRecoveryUsesSharedAndLegacyRoots(t *testing.T) {
	env := newInstallTestEnv(t, StaticReleases{}, newTestDownloader())
	ctx := context.Background()
	now := time.Now().UTC()
	sharedRelease := domain.ReleaseRef{ReleaseID: "rel-shared", Version: "1.0.0", Digest: "sha256:shared"}
	shared := domain.Installation{
		ID: "inst-shared", Slot: domain.CapabilitySlotTTS, Phase: domain.InstallationPhaseDownloading,
		Progress: 30, Release: &sharedRelease, StorageKey: mustStorageKey(t, sharedRelease),
		CoveredSlots: []domain.CapabilitySlotKey{domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone},
		CreatedAt:    now, UpdatedAt: now,
	}
	legacyRelease := domain.ReleaseRef{ReleaseID: "rel-legacy", Version: "1.0.0", Digest: "sha256:legacy"}
	legacy := domain.Installation{
		ID: "inst-legacy", Slot: domain.CapabilitySlotASR, Phase: domain.InstallationPhaseVerifying,
		Progress: 60, Release: &legacyRelease, CreatedAt: now, UpdatedAt: now,
	}
	for _, inst := range []domain.Installation{shared, legacy} {
		if err := env.store.CreateInstallation(ctx, inst); err != nil {
			t.Fatalf("CreateInstallation(%s) error = %v", inst.ID, err)
		}
	}
	for _, dir := range []string{
		filepath.Join(env.dataDir, "models", "releases", shared.StorageKey),
		filepath.Join(env.dataDir, "models", "asr", "rel-legacy"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "model.bin.part"), []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.svc.RecoverInstallations(ctx); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		filepath.Join(env.dataDir, "models", "releases", shared.StorageKey),
		filepath.Join(env.dataDir, "models", "asr", "rel-legacy"),
	} {
		if _, err := os.Stat(filepath.Join(dir, "model.bin.part")); !os.IsNotExist(err) {
			t.Fatalf("partial file survived recovery in %s: %v", dir, err)
		}
	}
	for _, id := range []domain.ID{shared.ID, legacy.ID} {
		recovered, err := env.svc.GetInstallation(ctx, string(id))
		if err != nil || recovered.Phase != domain.InstallationPhaseFailed || !recovered.Retryable {
			t.Fatalf("recovered %s = %+v, err = %v", id, recovered, err)
		}
	}
}

// TestLegacyRollbackUsesPersistedPlanManifest 覆盖旧记录回滚：无冻结清单时
// 使用其原始计划清单验证文件；证据缺失返回可读冲突而不伪成功。
func TestLegacyRollbackUsesPersistedPlanManifest(t *testing.T) {
	env := newInstallTestEnv(t, StaticReleases{}, newTestDownloader())
	ctx := context.Background()
	now := env.svc.Now()
	releaseV1 := domain.ReleaseRef{ReleaseID: "legacy-v1", Version: "1.0.0", Digest: "sha256:legacy-v1"}
	releaseV2 := domain.ReleaseRef{ReleaseID: "legacy-v2", Version: "2.0.0", Digest: "sha256:legacy-v2"}
	content := []byte("legacy-translate-model")
	file := downloader.File{
		RelPath: "model.bin", URL: "https://releases.invalid/legacy/model.bin",
		SHA256: testSHA256(content), Size: int64(len(content)),
	}
	for i, release := range []domain.ReleaseRef{releaseV1, releaseV2} {
		createdAt := now.Add(time.Duration(i) * time.Minute)
		inst := domain.Installation{
			ID: "legacy-" + release.ReleaseID, Slot: domain.CapabilitySlotTranslate,
			Phase: domain.InstallationPhaseDone, Progress: 100, VerifiedFiles: 1,
			Release: &release, PlanID: "plan-" + release.ReleaseID, PathOwner: "app",
			CreatedAt: createdAt, UpdatedAt: createdAt,
		}
		if err := env.store.CreateInstallation(ctx, inst); err != nil {
			t.Fatalf("CreateInstallation(%s): %v", inst.ID, err)
		}
		dir := filepath.Join(env.dataDir, "models", "translate", release.ReleaseID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "model.bin"), content, 0o644); err != nil {
			t.Fatal(err)
		}
		stored := storedPlan{
			Slot: domain.CapabilitySlotTranslate, PlanHash: "hash-" + release.ReleaseID,
			CreatedAt: createdAt,
			Plan: domain.InstallationPlan{
				PlanID: "plan-" + release.ReleaseID, PlanHash: "hash-" + release.ReleaseID,
				Slot: domain.CapabilitySlotTranslate, Release: release,
				CoveredSlots: []domain.CapabilitySlotKey{domain.CapabilitySlotTranslate},
			},
			Files: []downloader.File{file},
		}
		raw, err := json.Marshal(stored)
		if err != nil {
			t.Fatal(err)
		}
		if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
			return tx.SetMeta(ctx, installationPlanMetaPrefix+stored.Plan.PlanID, string(raw))
		}); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := env.svc.RollbackInstalledPackage(ctx, Idem{}, "translate")
	if err != nil {
		t.Fatalf("legacy rollback error = %v", err)
	}
	rolled := resp.Body.(domain.Installation)
	if rolled.ID != "legacy-legacy-v1" {
		t.Fatalf("legacy rollback selected %s, want retained v1", rolled.ID)
	}
	if got := env.capability(domain.CapabilitySlotTranslate); got.Version != "1.0.0" {
		t.Fatalf("legacy rollback version = %s", got.Version)
	}
	if installs := env.installs(); len(installs) != 2 {
		t.Fatalf("legacy rollback created a Done copy: %d", len(installs))
	}

	// 另一个旧槽位缺少任何清单证据：返回可读冲突，不伪验证。
	unverified := domain.ReleaseRef{ReleaseID: "legacy-unverified", Version: "1.0.0", Digest: "sha256:unverified"}
	unverifiedCurrent := domain.ReleaseRef{ReleaseID: "legacy-current", Version: "2.0.0", Digest: "sha256:current"}
	for i, release := range []domain.ReleaseRef{unverified, unverifiedCurrent} {
		createdAt := now.Add(time.Duration(i) * time.Minute)
		inst := domain.Installation{
			ID: "asr-" + release.ReleaseID, Slot: domain.CapabilitySlotASR,
			Phase: domain.InstallationPhaseDone, Progress: 100, VerifiedFiles: 1,
			Release: &release, PathOwner: "app", CreatedAt: createdAt, UpdatedAt: createdAt,
		}
		if err := env.store.CreateInstallation(ctx, inst); err != nil {
			t.Fatal(err)
		}
	}
	_, err = env.svc.RollbackInstalledPackage(ctx, Idem{}, "asr")
	if err == nil {
		t.Fatal("rollback without manifest evidence succeeded")
	}
	if code, ok := domain.CodeOf(err); !ok || code != domain.ErrorCodeCapabilityUnverified {
		t.Fatalf("unverified legacy rollback error = %v", err)
	}
	if got := env.capability(domain.CapabilitySlotASR); got.Version != "2.0.0" {
		t.Fatalf("failed rollback changed current version: %s", got.Version)
	}
}

// TestLegacyPlanWithoutFrozenCoverageIsStale 覆盖旧无覆盖计划必须重新 plan，
// 不能用今天的目录悄悄扩大已同意范围。
func TestLegacyPlanWithoutFrozenCoverageIsStale(t *testing.T) {
	env := newInstallTestEnv(t, StaticReleases{}, newTestDownloader())
	ctx := context.Background()
	release := domain.ReleaseRef{ReleaseID: "legacy-plan", Version: "1.0.0", Digest: "sha256:legacy-plan"}
	stored := storedPlan{
		Slot: domain.CapabilitySlotTTS, PlanHash: "legacy-hash", CreatedAt: env.svc.Now(),
		Plan: domain.InstallationPlan{
			PlanID: "plan_legacy", PlanHash: "legacy-hash", Slot: domain.CapabilitySlotTTS,
			Release: release,
			// 旧计划没有冻结覆盖范围。
			CoveredSlots: nil,
		},
		Files: []downloader.File{{
			RelPath: "model.bin", URL: "https://releases.invalid/legacy/model.bin",
			SHA256: testSHA256([]byte("x")), Size: 1,
		}},
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
		return tx.SetMeta(ctx, installationPlanMetaPrefix+"plan_legacy", string(raw))
	}); err != nil {
		t.Fatal(err)
	}
	_, err = env.svc.CreateInstallation(ctx, Idem{}, CreateInstallationRequest{
		PlanID: "plan_legacy", PlanHash: "legacy-hash",
	})
	if err == nil {
		t.Fatal("legacy plan without frozen coverage was accepted")
	}
	if !errors.Is(err, domain.ErrPreflightStale) {
		t.Fatalf("legacy plan error = %v, want PREFLIGHT_STALE", err)
	}
	if installs := env.installs(); len(installs) != 0 {
		t.Fatalf("stale legacy plan created installation records: %+v", installs)
	}
}

// TestRollbackRejectsActiveWorkAndKeepsState 覆盖回滚必须与删除同口径保护
// 活动 Task / 可推进 Run 的精确冻结引用；拒绝时零 binding/event/receipt 副作用。
func TestRollbackRejectsActiveWorkAndKeepsState(t *testing.T) {
	for _, name := range []string{"active-task", "nonterminal-run"} {
		t.Run(name, func(t *testing.T) {
			env := newInstallTestEnv(t, StaticReleases{}, newTestDownloader())
			ctx := context.Background()
			now := env.svc.Now()
			v1 := sharedTestReleaseForTest("rollback-shared-v1")
			v2 := sharedTestReleaseForTest("rollback-shared-v2")
			covered := []domain.CapabilitySlotKey{domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone}
			seedVerifiedInstallation(t, env.store, env.dataDir, "rollback-v1",
				domain.CapabilitySlotTTS, v1, covered,
				map[string][]byte{"model.bin": []byte("rollback-v1")}, now.Add(-2*time.Minute))
			seedVerifiedInstallation(t, env.store, env.dataDir, "rollback-v2",
				domain.CapabilitySlotTTS, v2, covered,
				map[string][]byte{"model.bin": []byte("rollback-v2")}, now.Add(-time.Minute))
			if got := env.capability(domain.CapabilitySlotTTS); got.Version != "2.0.0" {
				t.Fatalf("baseline current version = %s", got.Version)
			}

			project := domain.Project{
				ID: store.NewID("proj"), WorkspaceID: "ws-install", Name: "rollback-work",
				SchemaVersion: 1, EditVersion: 1, CreatedAt: now, UpdatedAt: now,
			}
			if err := env.store.CreateProject(ctx, project); err != nil {
				t.Fatalf("CreateProject: %v", err)
			}
			switch name {
			case "active-task":
				if err := env.store.CreateTask(ctx, domain.Task{
					ID: "task-active", ProjectID: project.ID, Kind: domain.TaskKindNode,
					NodeKey: domain.NodeKeyClone, Status: domain.TaskStatusRunning,
					CreatedAt: now, UpdatedAt: now,
				}); err != nil {
					t.Fatalf("CreateTask: %v", err)
				}
			case "nonterminal-run":
				release := v2
				if err := env.store.CreateWorkflowRun(ctx, domain.WorkflowRun{
					ID: "run-active", ProjectID: project.ID, Name: "rollback", Template: "tool_clone",
					Status: domain.RunStatusQueued, SourceLanguage: "und", Languages: []string{},
					Config: domain.RunConfig{Execution: map[domain.CapabilitySlotKey]domain.ExecutionConfig{
						domain.CapabilitySlotTTS: {ModelLock: release, DeviceID: "cpu", Params: map[string]any{}},
					}},
					CreatedAt: now, UpdatedAt: now,
				}); err != nil {
					t.Fatalf("CreateWorkflowRun: %v", err)
				}
			}
			eventsBefore := len(env.eventLabels())
			idem := Idem{Key: "rollback-guarded", Digest: "rollback-digest"}
			if _, err := env.svc.RollbackInstalledPackage(ctx, idem, "tts"); err == nil {
				t.Fatal("rollback accepted while relevant work is active")
			} else if code, ok := domain.CodeOf(err); !ok || code != domain.ErrorCodeRevisionConflict {
				t.Fatalf("rollback error = %v, want resource-busy conflict", err)
			}
			if got := env.capability(domain.CapabilitySlotTTS); got.Version != "2.0.0" {
				t.Fatalf("rejected rollback switched current to %s", got.Version)
			}
			bindings, err := env.store.ListInstallationBindings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, binding := range bindings {
				if binding.Slot == domain.CapabilitySlotTTS && binding.Current && binding.InstallationID != "rollback-v2" {
					t.Fatalf("rejected rollback changed current binding: %+v", binding)
				}
			}
			labels := env.eventLabels()
			if len(labels) != eventsBefore {
				t.Fatalf("rejected rollback wrote events: %v", labels)
			}
			for _, label := range labels {
				if label == "installation.rollback" {
					t.Fatal("rejected rollback emitted installation.rollback")
				}
			}
			if _, err := env.store.GetReceipt(ctx, "ws-install", "installations.rollback", idem.Key); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("rejected rollback persisted a receipt: %v", err)
			}
		})
	}
}

func sharedTestReleaseForTest(id string) domain.ReleaseRef {
	return domain.ReleaseRef{ReleaseID: id, Version: versionForTestRelease(id), Digest: "sha256:" + id}
}

func versionForTestRelease(id string) string {
	if strings.HasSuffix(id, "v2") {
		return "2.0.0"
	}
	return "1.0.0"
}

// TestRollbackStillWorksForInactiveSingleSlot 覆盖无活动工作时的单槽回滚保持可用。
func TestRollbackStillWorksForInactiveSingleSlot(t *testing.T) {
	env := newInstallTestEnv(t, StaticReleases{}, newTestDownloader())
	now := env.svc.Now()
	v1 := sharedTestReleaseForTest("inactive-v1")
	v2 := sharedTestReleaseForTest("inactive-v2")
	seedVerifiedInstallation(t, env.store, env.dataDir, "inactive-1",
		domain.CapabilitySlotTranslate, v1, nil,
		map[string][]byte{"model.bin": []byte("inactive-v1")}, now.Add(-2*time.Minute))
	seedVerifiedInstallation(t, env.store, env.dataDir, "inactive-2",
		domain.CapabilitySlotTranslate, v2, nil,
		map[string][]byte{"model.bin": []byte("inactive-v2")}, now.Add(-time.Minute))
	if _, err := env.svc.RollbackInstalledPackage(context.Background(), Idem{}, "translate"); err != nil {
		t.Fatalf("inactive single-slot rollback error = %v", err)
	}
	if got := env.capability(domain.CapabilitySlotTranslate); got.Version != "1.0.0" {
		t.Fatalf("inactive rollback version = %s", got.Version)
	}
}

// blockingVerifier 把文件验证停在屏障上，并统计调用次数。
type blockingVerifier struct {
	entered chan struct{}
	result  chan error
	calls   atomic.Int32
}

func newBlockingVerifier() *blockingVerifier {
	return &blockingVerifier{entered: make(chan struct{}, 4), result: make(chan error, 4)}
}

func (v *blockingVerifier) verify(ctx context.Context, _ string, _ []domain.InstallationFile) error {
	v.calls.Add(1)
	select {
	case v.entered <- struct{}{}:
	default:
	}
	select {
	case err := <-v.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (v *blockingVerifier) release(t *testing.T, err error) {
	t.Helper()
	select {
	case v.result <- err:
	case <-time.After(2 * time.Second):
		t.Fatal("verifier release not consumed")
	}
}

func (v *blockingVerifier) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-v.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("file verification did not start")
	}
}

func runWithin(t *testing.T, d time.Duration, what string, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	case <-time.After(d):
		t.Fatalf("%s exceeded %s while file verification was held", what, d)
	}
}

// setupDoneReuse 安装共享发布、解绑 tts，并返回重新 plan 的 tts 复用请求。
func setupDoneReuse(t *testing.T) (*installTestEnv, domain.InstallationPlan, domain.Installation) {
	t.Helper()
	payload := []byte("verifier-boundary")
	server := staticSource(t, map[string][]byte{"/rel-shared/model.bin": payload})
	plan := withCoverage(releasePlanFor(server, "rel-shared", "1.0.0", map[string][]byte{"model.bin": payload}),
		domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone)
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotTTS:        plan,
		domain.CapabilitySlotVoiceClone: plan,
	}}, newTestDownloader(server.URL))
	_, inst := env.installShared(domain.CapabilitySlotTTS)
	if _, err := env.svc.DeleteInstalledPackage(context.Background(), Idem{}, "tts"); err != nil {
		t.Fatalf("delete tts: %v", err)
	}
	reusePlan, err := env.svc.PlanInstallation(context.Background(), domain.CapabilitySlotTTS)
	if err != nil {
		t.Fatalf("replan tts: %v", err)
	}
	if reusePlan.ReuseInstallationID != inst.ID {
		t.Fatalf("reuse target = %q, want %s", reusePlan.ReuseInstallationID, inst.ID)
	}
	return env, reusePlan, inst
}

func assertSharedCurrent(t *testing.T, env *installTestEnv, instID domain.ID, want []domain.CapabilitySlotKey) {
	t.Helper()
	stored, err := env.store.GetInstallation(context.Background(), instID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameCapabilitySlotSet(stored.CurrentSlots, want) {
		t.Fatalf("current_slots during verification = %v, want %v", stored.CurrentSlots, want)
	}
}

func assertNoReceipt(t *testing.T, env *installTestEnv, operation, key string) {
	t.Helper()
	if _, err := env.store.GetReceipt(context.Background(), "ws-install", operation, key); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("%s receipt unexpectedly persisted: %v", operation, err)
	}
}

func assertNoRollbackEvent(t *testing.T, env *installTestEnv) {
	t.Helper()
	for _, label := range env.eventLabels() {
		if label == "installation.rollback" || label == "installation.reused" {
			t.Fatalf("verification failure emitted %s", label)
		}
	}
}

// TestReuseVerificationOutsideWriteBoundary 覆盖 Done 复用验证在全局写锁/
// 事务之外：屏障期间真实 DB 读写可完成、激活未提前；成功/失败/取消各自正确。
func TestReuseVerificationOutsideWriteBoundary(t *testing.T) {
	t.Run("success-and-replay", func(t *testing.T) {
		env, plan, inst := setupDoneReuse(t)
		// 另一个可直接取消的安装记录，用来证明验证屏障期间全局 s.write/writeMu
		// 没有被占用（不是只把 SQL 移出事务）。
		probeRelease := sharedTestReleaseForTest("verify-probe")
		if err := env.store.CreateInstallation(context.Background(), domain.Installation{
			ID: "cancel-probe", Slot: domain.CapabilitySlotASR, Phase: domain.InstallationPhasePlanning,
			Release: &probeRelease, CreatedAt: env.svc.Now(), UpdatedAt: env.svc.Now(),
		}); err != nil {
			t.Fatalf("seed cancel probe: %v", err)
		}
		verifier := newBlockingVerifier()
		env.svc.verifyFiles = verifier.verify
		idem := Idem{Key: "reuse-verify", Digest: "d-reuse"}

		type result struct {
			resp Response
			err  error
		}
		done := make(chan result, 1)
		go func() {
			resp, err := env.svc.CreateInstallation(context.Background(), idem, CreateInstallationRequest{
				PlanID: plan.PlanID, PlanHash: plan.PlanHash,
			})
			done <- result{resp: resp, err: err}
		}()
		verifier.waitEntered(t)

		// 验证被 hold 时：真实 DB 读/写、Store 快照读与 service 写路径都可完成。
		runWithin(t, 2*time.Second, "store read", func() error {
			_, err := env.store.ListInstallations(context.Background())
			return err
		})
		runWithin(t, 2*time.Second, "store write", func() error {
			return env.store.DoTx(context.Background(), func(tx *store.Tx) error {
				return tx.SetMeta(context.Background(), "verifier-boundary-probe", "1")
			})
		})
		runWithin(t, 2*time.Second, "service write", func() error {
			_, err := env.svc.CancelInstallation(context.Background(),
				Idem{Key: "probe-cancel", Digest: "probe"}, "cancel-probe")
			return err
		})
		if probe, err := env.store.GetInstallation(context.Background(), "cancel-probe"); err != nil ||
			probe.Phase != domain.InstallationPhaseCanceled {
			t.Fatalf("service write probe phase = %+v, err = %v", probe, err)
		}
		assertSharedCurrent(t, env, inst.ID, []domain.CapabilitySlotKey{domain.CapabilitySlotVoiceClone})
		assertNoReceipt(t, env, "installations.create", idem.Key)
		assertNoRollbackEvent(t, env)

		verifier.release(t, nil)
		got := <-done
		if got.err != nil {
			t.Fatalf("reuse create error = %v", got.err)
		}
		reused, ok := got.resp.Body.(domain.Installation)
		if !ok || reused.ID != inst.ID {
			t.Fatalf("reuse body = %T %+v", got.resp.Body, got.resp.Body)
		}
		if got := env.capability(domain.CapabilitySlotTTS); got.State != domain.SlotStateReady {
			t.Fatalf("tts after verified reuse = %s", got.State)
		}
		if _, err := env.store.GetReceipt(context.Background(), "ws-install", "installations.create", idem.Key); err != nil {
			t.Fatalf("successful reuse did not persist receipt: %v", err)
		}

		// 幂等重放：不再次验证文件，返回原快照。
		replayVerifier := newBlockingVerifier()
		env.svc.verifyFiles = replayVerifier.verify
		resp, err := env.svc.CreateInstallation(context.Background(), idem, CreateInstallationRequest{
			PlanID: plan.PlanID, PlanHash: plan.PlanHash,
		})
		if err != nil {
			t.Fatalf("idempotent replay error = %v", err)
		}
		if replayVerifier.calls.Load() != 0 {
			t.Fatalf("idempotent replay re-ran file verification %d times", replayVerifier.calls.Load())
		}
		raw, ok := resp.Body.(json.RawMessage)
		if !ok {
			t.Fatalf("idempotent replay body = %T", resp.Body)
		}
		var replayed domain.Installation
		if err := json.Unmarshal(raw, &replayed); err != nil || replayed.ID != inst.ID {
			t.Fatalf("replayed installation = %+v, err = %v", replayed, err)
		}
	})

	t.Run("failure", func(t *testing.T) {
		env, plan, inst := setupDoneReuse(t)
		verifier := newBlockingVerifier()
		env.svc.verifyFiles = verifier.verify
		idem := Idem{Key: "reuse-fail", Digest: "d-fail"}
		done := make(chan error, 1)
		go func() {
			_, err := env.svc.CreateInstallation(context.Background(), idem, CreateInstallationRequest{
				PlanID: plan.PlanID, PlanHash: plan.PlanHash,
			})
			done <- err
		}()
		verifier.waitEntered(t)
		assertSharedCurrent(t, env, inst.ID, []domain.CapabilitySlotKey{domain.CapabilitySlotVoiceClone})
		verifier.release(t, fmt.Errorf("injected hash mismatch"))
		err := <-done
		if err == nil {
			t.Fatal("failed verification still activated reuse")
		}
		if code, ok := domain.CodeOf(err); !ok || code != domain.ErrorCodeCapabilityUnverified {
			t.Fatalf("reuse verification error = %v", err)
		}
		assertSharedCurrent(t, env, inst.ID, []domain.CapabilitySlotKey{domain.CapabilitySlotVoiceClone})
		assertNoReceipt(t, env, "installations.create", idem.Key)
		assertNoRollbackEvent(t, env)
	})

	t.Run("context-cancel", func(t *testing.T) {
		env, plan, inst := setupDoneReuse(t)
		verifier := newBlockingVerifier()
		env.svc.verifyFiles = verifier.verify
		idem := Idem{Key: "reuse-cancel", Digest: "d-cancel"}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := env.svc.CreateInstallation(ctx, idem, CreateInstallationRequest{
				PlanID: plan.PlanID, PlanHash: plan.PlanHash,
			})
			done <- err
		}()
		verifier.waitEntered(t)
		cancel()
		err := <-done
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled verification error = %v", err)
		}
		assertSharedCurrent(t, env, inst.ID, []domain.CapabilitySlotKey{domain.CapabilitySlotVoiceClone})
		assertNoReceipt(t, env, "installations.create", idem.Key)
		assertNoRollbackEvent(t, env)
	})
}

// TestRollbackVerificationOutsideWriteBoundary 覆盖回滚验证同样在事务外，
// 且成功/失败/取消不产生提前切换或副作用。
func TestRollbackVerificationOutsideWriteBoundary(t *testing.T) {
	setup := func(t *testing.T) (*installTestEnv, domain.ReleaseRef) {
		t.Helper()
		env := newInstallTestEnv(t, StaticReleases{}, newTestDownloader())
		now := env.svc.Now()
		v1 := sharedTestReleaseForTest("boundary-v1")
		v2 := sharedTestReleaseForTest("boundary-v2")
		seedVerifiedInstallation(t, env.store, env.dataDir, "boundary-1",
			domain.CapabilitySlotTranslate, v1, nil,
			map[string][]byte{"model.bin": []byte("boundary-v1")}, now.Add(-2*time.Minute))
		seedVerifiedInstallation(t, env.store, env.dataDir, "boundary-2",
			domain.CapabilitySlotTranslate, v2, nil,
			map[string][]byte{"model.bin": []byte("boundary-v2")}, now.Add(-time.Minute))
		return env, v2
	}
	runRollback := func(t *testing.T, env *installTestEnv, ctx context.Context, idem Idem) chan error {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			_, err := env.svc.RollbackInstalledPackage(ctx, idem, "translate")
			done <- err
		}()
		return done
	}

	t.Run("success-and-replay", func(t *testing.T) {
		env, current := setup(t)
		verifier := newBlockingVerifier()
		env.svc.verifyFiles = verifier.verify
		idem := Idem{Key: "rollback-verify", Digest: "d-rollback"}
		done := runRollback(t, env, context.Background(), idem)
		verifier.waitEntered(t)
		runWithin(t, 2*time.Second, "store write", func() error {
			return env.store.DoTx(context.Background(), func(tx *store.Tx) error {
				return tx.SetMeta(context.Background(), "rollback-boundary-probe", "1")
			})
		})
		if got := env.capability(domain.CapabilitySlotTranslate); got.Version != current.Version {
			t.Fatalf("rollback activated before verification: %s", got.Version)
		}
		assertNoReceipt(t, env, "installations.rollback", idem.Key)
		verifier.release(t, nil)
		if err := <-done; err != nil {
			t.Fatalf("rollback error = %v", err)
		}
		if got := env.capability(domain.CapabilitySlotTranslate); got.Version != "1.0.0" {
			t.Fatalf("verified rollback version = %s", got.Version)
		}
		replayVerifier := newBlockingVerifier()
		env.svc.verifyFiles = replayVerifier.verify
		if _, err := env.svc.RollbackInstalledPackage(context.Background(), idem, "translate"); err != nil {
			t.Fatalf("rollback replay error = %v", err)
		}
		if replayVerifier.calls.Load() != 0 {
			t.Fatalf("rollback replay re-ran verification %d times", replayVerifier.calls.Load())
		}
	})

	t.Run("failure", func(t *testing.T) {
		env, current := setup(t)
		verifier := newBlockingVerifier()
		env.svc.verifyFiles = verifier.verify
		idem := Idem{Key: "rollback-fail", Digest: "d-fail"}
		done := runRollback(t, env, context.Background(), idem)
		verifier.waitEntered(t)
		verifier.release(t, fmt.Errorf("injected corrupt file"))
		err := <-done
		if err == nil {
			t.Fatal("failed verification still changed current")
		}
		if code, ok := domain.CodeOf(err); !ok || code != domain.ErrorCodeCapabilityUnverified {
			t.Fatalf("rollback verification error = %v", err)
		}
		if got := env.capability(domain.CapabilitySlotTranslate); got.Version != current.Version {
			t.Fatalf("failed rollback switched current to %s", got.Version)
		}
		assertNoReceipt(t, env, "installations.rollback", idem.Key)
		assertNoRollbackEvent(t, env)
	})

	t.Run("context-cancel", func(t *testing.T) {
		env, current := setup(t)
		verifier := newBlockingVerifier()
		env.svc.verifyFiles = verifier.verify
		idem := Idem{Key: "rollback-cancel", Digest: "d-cancel"}
		ctx, cancel := context.WithCancel(context.Background())
		done := runRollback(t, env, ctx, idem)
		verifier.waitEntered(t)
		cancel()
		err := <-done
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled rollback error = %v", err)
		}
		if got := env.capability(domain.CapabilitySlotTranslate); got.Version != current.Version {
			t.Fatalf("canceled rollback switched current to %s", got.Version)
		}
		assertNoReceipt(t, env, "installations.rollback", idem.Key)
	})
}

// TestDoneReuseWithoutPreverificationFailsSafely 覆盖 active→done 竞态：
// 事务内发现 Done 目标但事务外未验证该实体时不得激活，返回可重试冲突。
func TestDoneReuseWithoutPreverificationFailsSafely(t *testing.T) {
	env, plan, inst := setupDoneReuse(t)
	before, err := env.store.GetInstallation(context.Background(), inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	idem := Idem{Key: "unverified-done", Digest: "d-unverified"}
	_, err = env.svc.createInstallationWrite(context.Background(), idem, CreateInstallationRequest{
		PlanID: plan.PlanID, PlanHash: plan.PlanHash,
	}, &preparedCreate{})
	if !errors.Is(err, errInstallationRaced) {
		t.Fatalf("unverified Done target error = %v, want race sentinel", err)
	}
	after, err := env.store.GetInstallation(context.Background(), inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameCapabilitySlotSet(after.CurrentSlots, before.CurrentSlots) {
		t.Fatalf("unverified Done target changed current: %v -> %v", before.CurrentSlots, after.CurrentSlots)
	}
	assertNoReceipt(t, env, "installations.create", idem.Key)
	assertNoRollbackEvent(t, env)
}

// TestSharedInstallSatisfiesRealVoiceClonePreflight 覆盖 PRD AC1 的真实消费者：
// 一次 TTS 共享安装后，生产 InstallationCapabilities 的 tool_voice_clone
// 预检不再要求重复安装，冻结锁来自同一已验证 ReleaseRef。
func TestSharedInstallSatisfiesRealVoiceClonePreflight(t *testing.T) {
	ctx := context.Background()
	payload := bytes.Repeat([]byte("clone-preflight-release"), 32)
	server := staticSource(t, map[string][]byte{"/rel-shared/model.bin": payload})
	plan := withCoverage(releasePlanFor(server, "rel-shared", "1.0.0",
		map[string][]byte{"model.bin": payload}),
		domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone)

	env := newExportTestEnv(t)
	env.svc.opts.PipelineOrchestration = true
	env.svc.opts.Capabilities = NewInstallationCapabilities()
	env.svc.opts.Releases = StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotTTS:        plan,
		domain.CapabilitySlotVoiceClone: plan,
	}}
	env.svc.opts.Downloader = newTestDownloader(server.URL)

	project := env.seedProject("shared clone preflight")
	source, _ := seedCloneTimelineTracks(t, env, project.ID,
		&domain.MediaMeta{Container: "wav", DurationMs: 2000, AudioTracks: 1},
		[]domain.Segment{{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"}},
		[]domain.Segment{{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"}})
	in := cloneRunInput(project.ID, source.ID, false)

	// 安装前的正常阻断对照；旧代码失败基线另由真实安装 driver 记录。
	beforeInstall, err := env.svc.Preflight(ctx, in)
	if err != nil {
		t.Fatalf("Preflight before install: %v", err)
	}
	if !hasPreflightIssue(beforeInstall, string(domain.ErrorCodeModelNotInstalled)) &&
		!hasPreflightIssue(beforeInstall, string(domain.ErrorCodeCapabilityUnverified)) {
		t.Fatalf("pre-install clone preflight did not report missing capability: %+v", beforeInstall.Issues)
	}

	// 真实 Downloader + 本机源，从 TTS 入口安装一次共享发布（未预填 Done/probe）。
	installPlan, err := env.svc.PlanInstallation(ctx, domain.CapabilitySlotTTS)
	if err != nil {
		t.Fatalf("PlanInstallation: %v", err)
	}
	created, err := env.svc.CreateInstallation(ctx, Idem{}, CreateInstallationRequest{
		PlanID: installPlan.PlanID, PlanHash: installPlan.PlanHash,
	})
	if err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}
	inst := created.Body.(domain.Installation)
	deadline := time.Now().Add(10 * time.Second)
	for {
		inst, err = env.svc.GetInstallation(ctx, inst.ID)
		if err != nil {
			t.Fatalf("GetInstallation: %v", err)
		}
		if inst.Phase == domain.InstallationPhaseDone {
			break
		}
		if inst.Phase.Terminal() {
			t.Fatalf("shared installation phase = %s (%+v)", inst.Phase, inst.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("shared installation stuck in %s", inst.Phase)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sameCapabilitySlotSet(inst.CurrentSlots,
		[]domain.CapabilitySlotKey{domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone}) {
		t.Fatalf("one install did not activate both covered slots: %v", inst.CurrentSlots)
	}
	if installs := env.storeListInstallations(t); len(installs) != 1 {
		t.Fatalf("physical installation records = %d, want 1", len(installs))
	}

	// 安装后：克隆预检不再要求安装/准入，且没有任何其他 blocker 被过滤。
	afterInstall, err := env.svc.Preflight(ctx, in)
	if err != nil {
		t.Fatalf("Preflight after install: %v", err)
	}
	for _, issue := range afterInstall.Issues {
		if issue.Code == string(domain.ErrorCodeModelNotInstalled) ||
			issue.Code == string(domain.ErrorCodeCapabilityUnverified) {
			t.Fatalf("clone preflight still requires installation: %+v", issue)
		}
	}
	if afterInstall.HasBlocker() {
		t.Fatalf("clone preflight still blocked after shared install: %+v", afterInstall.Issues)
	}

	// 真实创建路径：冻结锁来自同一已验证 release，两个槽位都可用。
	runResp, err := env.svc.CreateWorkflowRun(ctx, Idem{}, in)
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := runResp.Body.(domain.WorkflowRun)
	stored, err := env.store.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	for _, slot := range []domain.CapabilitySlotKey{domain.CapabilitySlotVoiceClone} {
		cfg, ok := stored.Config.Execution[slot]
		if !ok || !validRelease(cfg.ModelLock) || cfg.ModelLock != plan.Release {
			t.Fatalf("frozen %s lock = %+v, want verified release %+v", slot, cfg.ModelLock, plan.Release)
		}
		if cfg.DeviceID == "" || cfg.DeviceID == "auto" {
			t.Fatalf("frozen %s device = %q", slot, cfg.DeviceID)
		}
	}
}

func (e *exportTestEnv) storeListInstallations(t *testing.T) []domain.Installation {
	t.Helper()
	installs, err := e.store.ListInstallations(context.Background())
	if err != nil {
		t.Fatalf("ListInstallations: %v", err)
	}
	return installs
}

func mustStorageKey(t *testing.T, release domain.ReleaseRef) string {
	t.Helper()
	key, err := domain.ReleaseStorageKey(release)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// TestSameReleaseIDDifferentDigestIsolatedRoots 覆盖同 release_id 的不同
// digest/版本不覆写同一目录，两个实体都可安装且互不影响。
func TestSameReleaseIDDifferentDigestIsolatedRoots(t *testing.T) {
	v1 := []byte("same-id-digest-v1")
	v2 := []byte("same-id-digest-v2")
	server := staticSource(t, map[string][]byte{
		"/a/model.bin": v1,
		"/b/model.bin": v2,
	})
	planA := ReleasePlan{
		Release: domain.ReleaseRef{ReleaseID: "rel-same", Version: "1.0.0", Digest: "sha256:aaa"},
		Files: []downloader.File{{
			RelPath: "model.bin", URL: server.URL + "/a/model.bin",
			SHA256: testSHA256(v1), Size: int64(len(v1)),
		}},
		DownloadBytes: int64(len(v1)), InstallBytes: int64(len(v1)),
	}
	planB := ReleasePlan{
		Release: domain.ReleaseRef{ReleaseID: "rel-same", Version: "2.0.0", Digest: "sha256:bbb"},
		Files: []downloader.File{{
			RelPath: "model.bin", URL: server.URL + "/b/model.bin",
			SHA256: testSHA256(v2), Size: int64(len(v2)),
		}},
		DownloadBytes: int64(len(v2)), InstallBytes: int64(len(v2)),
	}
	catalog := &slotSequenceReleases{plans: map[domain.CapabilitySlotKey][]ReleasePlan{
		domain.CapabilitySlotASR: {planA, planB},
	}}
	env := newInstallTestEnv(t, catalog, newTestDownloader(server.URL))
	if _, first := env.installShared(domain.CapabilitySlotASR); first.Phase != domain.InstallationPhaseDone {
		t.Fatalf("v1 phase = %s", first.Phase)
	}
	if _, second := env.installShared(domain.CapabilitySlotASR); second.Phase != domain.InstallationPhaseDone {
		t.Fatalf("v2 phase = %s", second.Phase)
	}
	rootA, rootB := env.releaseDir(planA.Release), env.releaseDir(planB.Release)
	if rootA == rootB {
		t.Fatal("same release_id/different digest shared one root")
	}
	for _, tc := range []struct {
		root    string
		content []byte
	}{{rootA, v1}, {rootB, v2}} {
		got, err := os.ReadFile(filepath.Join(tc.root, "model.bin"))
		if err != nil || !bytes.Equal(got, tc.content) {
			t.Fatalf("root %s content mismatch: %v", tc.root, err)
		}
	}
	if got := env.capability(domain.CapabilitySlotASR); got.Version != "2.0.0" {
		t.Fatalf("current version after upgrade = %s", got.Version)
	}
}

// TestSingleSlotSeparationInstallRegression 覆盖单槽模型（separation）仍按
// 一次物理安装准入、删除回收，不受共享模型影响。
func TestSingleSlotSeparationInstallRegression(t *testing.T) {
	payload := []byte("separation-model")
	server := staticSource(t, map[string][]byte{"/rel-sep/model.bin": payload})
	plan := releasePlanFor(server, "rel-sep", "1.0.0", map[string][]byte{"model.bin": payload})
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotSeparation: plan,
	}}, newTestDownloader(server.URL))
	planned, inst := env.installShared(domain.CapabilitySlotSeparation)
	if !sameCapabilitySlotSet(planned.CoveredSlots, []domain.CapabilitySlotKey{domain.CapabilitySlotSeparation}) {
		t.Fatalf("singleton plan covered_slots = %v", planned.CoveredSlots)
	}
	if inst.Phase != domain.InstallationPhaseDone || inst.StorageKey == "" {
		t.Fatalf("separation installation = %+v", inst)
	}
	if got := env.capability(domain.CapabilitySlotSeparation); got.State != domain.SlotStateReady {
		t.Fatalf("separation state = %s", got.State)
	}
	root := env.releaseDir(plan.Release)
	if _, err := os.Stat(filepath.Join(root, "model.bin")); err != nil {
		t.Fatalf("separation model bytes missing: %v", err)
	}
	if _, err := env.svc.DeleteInstalledPackage(context.Background(), Idem{}, "separation"); err != nil {
		t.Fatalf("delete separation error = %v", err)
	}
	if got := env.capability(domain.CapabilitySlotSeparation); got.State != domain.SlotStateNeedsSetup {
		t.Fatalf("separation state after delete = %s", got.State)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("separation root not collected: %v", err)
	}
}
