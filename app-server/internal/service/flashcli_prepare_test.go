package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func TestBuildFlashCLIPullArgsUsesAdmittedRefAndInjectedEnv(t *testing.T) {
	args, err := buildFlashCLIPullArgs([]string{
		"-d", "yaomu", "-e", "env",
		"PYTHONPATH=/mnt/d/app/runner_src",
		"FLASHCLI_BIN=/usr/local/bin/flashcli",
		"FLASHCLI_CUDA_TAG=cu130",
		"FFMPEG_BIN=/usr/local/bin/ffmpeg",
		"/usr/local/bin/python3", "-m", "yaomustudio_runner",
	}, "tts/omnivoice_flashrt:1.0.3")
	if err != nil {
		t.Fatalf("buildFlashCLIPullArgs: %v", err)
	}
	want := []string{
		"-d", "yaomu", "-e", "env",
		"FLASHCLI_BIN=/usr/local/bin/flashcli",
		"FLASHCLI_CUDA_TAG=cu130",
		"/usr/local/bin/flashcli", "pull", "tts/omnivoice_flashrt:1.0.3",
	}
	if len(args) != len(want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %#v, want %#v", args, want)
		}
	}
}

func TestBuildFlashCLIPullArgsRejectsUnknownRef(t *testing.T) {
	_, err := buildFlashCLIPullArgs([]string{
		"-d", "yaomu", "-e", "env",
		"FLASHCLI_BIN=/usr/local/bin/flashcli",
		"FLASHCLI_CUDA_TAG=cu130",
	}, "tts/other:1.0.0")
	if err == nil {
		t.Fatal("unknown ref was accepted")
	}
}

func TestInstallationPullsAdmittedModelBeforeDone(t *testing.T) {
	content := []byte("bundle-bytes")
	server := staticSource(t, map[string][]byte{"/rel-pull/model.bin": content})
	plan := releasePlanFor(server, "rel-pull", "1.0.3", map[string][]byte{"model.bin": content})
	plan.FlashCLIRef = "tts/omnivoice_flashrt:1.0.3"
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotTTS: plan,
	}}, newTestDownloader(server.URL))
	var pulled string
	env.svc.opts.PrepareModel = func(_ context.Context, ref string) error {
		pulled = ref
		return nil
	}

	inst := env.install(domain.CapabilitySlotTTS)
	if inst.Phase != domain.InstallationPhaseDone {
		t.Fatalf("phase = %s, error = %+v", inst.Phase, inst.Error)
	}
	if pulled != plan.FlashCLIRef {
		t.Fatalf("pulled %q, want %q", pulled, plan.FlashCLIRef)
	}
	marker, err := os.ReadFile(filepath.Join(env.releaseDir(plan.Release), flashCLIPrepareMarkerName))
	if err != nil {
		t.Fatalf("read prepare marker: %v", err)
	}
	if string(marker) != plan.FlashCLIRef+"\n" {
		t.Fatalf("marker = %q", marker)
	}
}

func TestInstallationPrepareFailureDoesNotActivate(t *testing.T) {
	content := []byte("bundle-bytes")
	server := staticSource(t, map[string][]byte{"/rel-pull-fail/model.bin": content})
	plan := releasePlanFor(server, "rel-pull-fail", "1.0.3", map[string][]byte{"model.bin": content})
	plan.FlashCLIRef = "asr/qwen3_asr:1.0.1"
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotASR: plan,
	}}, newTestDownloader(server.URL))
	env.svc.opts.PrepareModel = func(context.Context, string) error {
		return errors.New("pull failed")
	}

	inst := env.install(domain.CapabilitySlotASR)
	if inst.Phase != domain.InstallationPhaseFailed {
		t.Fatalf("phase = %s, want failed", inst.Phase)
	}
	if inst.Error == nil || inst.Error.MessageKey != "installation.install_failed" {
		t.Fatalf("error = %+v", inst.Error)
	}
	if capability := env.capability(domain.CapabilitySlotASR); capability.State == domain.SlotStateReady {
		t.Fatal("failed prepare activated the slot")
	}
}

func TestRecoverInstallationWithoutPrepareMarkerStaysRetryable(t *testing.T) {
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotASR: {
			Release:     domain.ReleaseRef{ReleaseID: "rel-recover-pull", Version: "1.0.1", Digest: "digest-recover-pull"},
			FlashCLIRef: "asr/qwen3_asr:1.0.1",
		},
	}}, newTestDownloader())
	release := domain.ReleaseRef{ReleaseID: "rel-recover-pull", Version: "1.0.1", Digest: "digest-recover-pull"}
	inst := seedInstallationFiles(
		t, env.store, env.dataDir, "inst-recover-pull", domain.CapabilitySlotASR,
		release, map[string][]byte{"model.bin": []byte("verified-model")},
		domain.InstallationPhaseInstalling, 1, time.Now().UTC(),
	)

	if err := env.svc.RecoverInstallations(context.Background()); err != nil {
		t.Fatalf("RecoverInstallations: %v", err)
	}
	recovered, err := env.svc.GetInstallation(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if recovered.Phase != domain.InstallationPhaseFailed {
		t.Fatalf("phase = %s, want failed", recovered.Phase)
	}
	if recovered.Error == nil || recovered.Error.MessageKey != "installation.interrupted_on_restart" {
		t.Fatalf("error = %+v", recovered.Error)
	}
}

func TestBuildFlashCLIRemoveArgsUsesBundleClean(t *testing.T) {
	args, err := buildFlashCLIRemoveArgs([]string{
		"-d", "yaomu", "-e", "env",
		"FLASHCLI_BIN=/usr/local/bin/flashcli",
		"FLASHCLI_CUDA_TAG=cu130",
		"/usr/local/bin/python3", "-m", "yaomustudio_runner",
	}, "asr/qwen3_asr:1.0.1")
	if err != nil {
		t.Fatalf("buildFlashCLIRemoveArgs: %v", err)
	}
	want := []string{
		"-d", "yaomu", "-e", "env",
		"FLASHCLI_BIN=/usr/local/bin/flashcli",
		"FLASHCLI_CUDA_TAG=cu130",
		"/usr/local/bin/flashcli", "bundle", "clean", "asr/qwen3_asr:1.0.1", "--all", "--yes",
	}
	if len(args) != len(want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %#v, want %#v", args, want)
		}
	}
}

func TestParseFlashCLIModelsListUsesWeightsColumn(t *testing.T) {
	cached, err := parseFlashCLIModelsList(strings.Join([]string{
		"REF\tBUNDLE\tBUNDLE SIZE\tRUNTIME\tRUNTIME SIZE\tWEIGHTS\tWEIGHTS SIZE\tDIR",
		"tts/omnivoice_flashrt:1.0.3\tcached\t1\truntime\t1\tcached\t1\t~/.flashcli/models/omnivoice",
		"asr/qwen3_asr:1.0.1\tcached\t1\truntime\t1\tmissing\t0\t~/.flashcli/models/qwen",
		"other/model:1.0.0\tcached\t1\truntime\t1\tcached\t1\t~/.flashcli/models/other",
	}, "\n"))
	if err != nil {
		t.Fatalf("parseFlashCLIModelsList: %v", err)
	}
	if !cached["tts/omnivoice_flashrt:1.0.3"] || cached["asr/qwen3_asr:1.0.1"] {
		t.Fatalf("cached = %#v", cached)
	}
	if _, ok := cached["other/model:1.0.0"]; ok {
		t.Fatal("unknown ref was treated as download evidence")
	}
	empty, err := parseFlashCLIModelsList("No cached presets. Run flashcli run or flashcli bundle sync.\n")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty list = %#v, err = %v", empty, err)
	}
	aligned, err := parseFlashCLIModelsList(strings.Join([]string{
		"REF                          BUNDLE  BUNDLE SIZE  WEIGHTS  WEIGHTS SIZE  DIR",
		"xym/melband-roformer:1.0.10  cached  6.5 MiB      missing  410 B         ~/.flashcli/models/store/melband",
		"tts/omnivoice_flashrt:1.0.3  cached  6.5 MiB      cached   1.2 GiB       ~/.flashcli/models/store/omnivoice",
	}, "\n"))
	if err != nil {
		t.Fatalf("aligned models list: %v", err)
	}
	if aligned["xym/melband-roformer:1.0.10"] || !aligned["tts/omnivoice_flashrt:1.0.3"] {
		t.Fatalf("aligned cached = %#v", aligned)
	}
}

func TestWeightProgressUsesDeclaredSizeAndBytesOnDisk(t *testing.T) {
	root := t.TempDir()
	bundleDir := filepath.Join(root, "bundles", "melband-roformer", "1.0.10")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bundle := `{
      "defaults": {
        "weights": {"repo": "org/melband"},
        "extra_weights": {"aligner": {"repo": "org/other"}}
      }
    }`
	if err := os.WriteFile(filepath.Join(bundleDir, "flashcli-bundle.json"), []byte(bundle), 0o644); err != nil {
		t.Fatal(err)
	}
	modelDir := filepath.Join(root, "models", "store", "melband")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
      "repo": "org/melband",
      "files": [
        {"path": "weights.bin", "size": 1000},
        {"path": "config.json", "size": 4},
        {"path": "../secret", "size": 50}
      ]
    }`
	if err := os.WriteFile(filepath.Join(modelDir, ".flashcli_download.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "weights.bin.incomplete"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "config.json"), []byte("abcd"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "models", "store", "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, ".flashcli_download.json"), []byte(`{"repo":"org/skip","files":[{"path":"a","size":999}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	done, total, err := weightProgressAt(root, "xym/melband-roformer:1.0.10")
	if err != nil {
		t.Fatal(err)
	}
	if done != 14 || total != 1004 {
		t.Fatalf("progress = %d/%d, want 14/1004", done, total)
	}
	missingDone, missingTotal, err := weightProgressAt(root, "asr/qwen3_asr:1.0.1")
	if err != nil || missingDone != 0 || missingTotal != 0 {
		t.Fatalf("missing bundle = %d/%d, err %v", missingDone, missingTotal, err)
	}
}

func TestInstallationProgressUsesWeightBytes(t *testing.T) {
	content := []byte("bundle-bytes")
	server := staticSource(t, map[string][]byte{"/rel-weight/model.bin": content})
	plan := releasePlanFor(server, "rel-weight", "1.0.10", map[string][]byte{"model.bin": content})
	plan.FlashCLIRef = "xym/melband-roformer:1.0.10"
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotSeparation: plan,
	}}, newTestDownloader(server.URL))
	releasePrepare := make(chan struct{})
	env.svc.opts.PrepareModel = func(ctx context.Context, _ string) error {
		select {
		case <-releasePrepare:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	env.svc.opts.WeightProgress = func(context.Context, string) (int64, int64, error) {
		return 400, 1000, nil
	}
	env.svc.opts.WeightProgressInterval = 15 * time.Millisecond
	created := env.create(domain.CapabilitySlotSeparation)
	deadline := time.Now().Add(5 * time.Second)
	var seen domain.Installation
	for time.Now().Before(deadline) {
		current, err := env.svc.GetInstallation(context.Background(), string(created.ID))
		if err != nil {
			t.Fatal(err)
		}
		seen = current
		if current.Phase == domain.InstallationPhaseInstalling && current.Progress == 40 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if seen.Phase != domain.InstallationPhaseInstalling || seen.Progress != 40 {
		t.Fatalf("while weights download, phase=%s progress=%d", seen.Phase, seen.Progress)
	}
	close(releasePrepare)
	done := env.waitTerminal(created.ID)
	if done.Phase != domain.InstallationPhaseDone || done.Progress != 100 {
		t.Fatalf("finished phase=%s progress=%d", done.Phase, done.Progress)
	}
}

func TestInstallationWeightProgressNeverMovesBackwards(t *testing.T) {
	content := []byte("bundle-bytes")
	server := staticSource(t, map[string][]byte{"/rel-weight-reset/model.bin": content})
	plan := releasePlanFor(server, "rel-weight-reset", "1.0.10", map[string][]byte{"model.bin": content})
	plan.FlashCLIRef = "xym/melband-roformer:1.0.10"
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotSeparation: plan,
	}}, newTestDownloader(server.URL))
	releasePrepare := make(chan struct{})
	env.svc.opts.PrepareModel = func(ctx context.Context, _ string) error {
		select {
		case <-releasePrepare:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var calls atomic.Int32
	secondCall := make(chan struct{})
	env.svc.opts.WeightProgress = func(context.Context, string) (int64, int64, error) {
		switch calls.Add(1) {
		case 1:
			return 400, 1000, nil
		case 2:
			close(secondCall)
			return 0, 1000, nil
		default:
			return 500, 1000, nil
		}
	}
	env.svc.opts.WeightProgressInterval = 15 * time.Millisecond
	created := env.create(domain.CapabilitySlotSeparation)
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, err := env.svc.GetInstallation(context.Background(), string(created.ID))
		if err != nil {
			t.Fatal(err)
		}
		if current.Phase == domain.InstallationPhaseInstalling && current.Progress == 40 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never reached 40: phase=%s progress=%d", current.Phase, current.Progress)
		}
		time.Sleep(15 * time.Millisecond)
	}
	select {
	case <-secondCall:
	case <-time.After(2 * time.Second):
		t.Fatal("weight progress was not sampled twice")
	}
	// The second sample dropped to 0 (simulating a stale/partial weight directory).
	// The visible installation progress must not move backwards.
	time.Sleep(30 * time.Millisecond)
	current, err := env.svc.GetInstallation(context.Background(), string(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	if current.Phase != domain.InstallationPhaseInstalling || current.Progress < 40 {
		t.Fatalf("progress moved backwards: phase=%s progress=%d", current.Phase, current.Progress)
	}
	close(releasePrepare)
	done := env.waitTerminal(created.ID)
	if done.Phase != domain.InstallationPhaseDone || done.Progress != 100 {
		t.Fatalf("finished phase=%s progress=%d", done.Phase, done.Progress)
	}
}

func TestInstallationPullUsesStoredPlanWhenMemoryIsEmpty(t *testing.T) {
	content := []byte("bundle-bytes")
	server := staticSource(t, map[string][]byte{"/rel-stored/model.bin": content})
	plan := releasePlanFor(server, "rel-stored", "1.0.3", map[string][]byte{"model.bin": content})
	plan.FlashCLIRef = "tts/omnivoice_flashrt:1.0.3"
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotTTS: plan,
	}}, newTestDownloader(server.URL))
	var pulled string
	env.svc.opts.PrepareModel = func(_ context.Context, ref string) error {
		pulled = ref
		return nil
	}
	planned, err := env.svc.PlanInstallation(context.Background(), domain.CapabilitySlotTTS)
	if err != nil {
		t.Fatalf("PlanInstallation: %v", err)
	}
	env.svc.flashRefMu.Lock()
	env.svc.flashRefs = nil
	env.svc.flashRefMu.Unlock()
	resp, err := env.svc.CreateInstallation(context.Background(), Idem{}, CreateInstallationRequest{
		PlanID: planned.PlanID, PlanHash: planned.PlanHash,
	})
	if err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}
	created, ok := resp.Body.(domain.Installation)
	if !ok {
		t.Fatalf("body = %T", resp.Body)
	}
	inst := env.waitTerminal(created.ID)
	if inst.Phase != domain.InstallationPhaseDone {
		t.Fatalf("phase = %s, error = %+v", inst.Phase, inst.Error)
	}
	if pulled != plan.FlashCLIRef {
		t.Fatalf("pulled %q, want %q", pulled, plan.FlashCLIRef)
	}
}

func TestDeleteInstalledPackageRemovesFlashCLICache(t *testing.T) {
	content := []byte("bundle-bytes")
	server := staticSource(t, map[string][]byte{"/rel-delete/model.bin": content})
	plan := releasePlanFor(server, "rel-delete", "1.0.1", map[string][]byte{"model.bin": content})
	plan.FlashCLIRef = "asr/qwen3_asr:1.0.1"
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotASR: plan,
	}}, newTestDownloader(server.URL))
	env.svc.opts.PrepareModel = func(context.Context, string) error { return nil }
	var removed string
	env.svc.opts.RemoveModel = func(_ context.Context, ref string) error {
		removed = ref
		return nil
	}
	inst := env.install(domain.CapabilitySlotASR)
	if inst.Phase != domain.InstallationPhaseDone {
		t.Fatalf("phase = %s", inst.Phase)
	}
	if _, err := env.svc.DeleteInstalledPackage(context.Background(), Idem{}, "asr"); err != nil {
		t.Fatalf("DeleteInstalledPackage: %v", err)
	}
	if removed != plan.FlashCLIRef {
		t.Fatalf("removed %q, want %q", removed, plan.FlashCLIRef)
	}
}

func TestCapabilitiesRequireFlashCLIWeightsEvidence(t *testing.T) {
	content := []byte("bundle-bytes")
	server := staticSource(t, map[string][]byte{"/rel-evidence/model.bin": content})
	plan := releasePlanFor(server, "rel-evidence", "1.0.10", map[string][]byte{"model.bin": content})
	plan.FlashCLIRef = "xym/melband-roformer:1.0.10"
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotSeparation: plan,
	}}, newTestDownloader(server.URL))
	env.svc.opts.PrepareModel = func(context.Context, string) error { return nil }
	if inst := env.install(domain.CapabilitySlotSeparation); inst.Phase != domain.InstallationPhaseDone {
		t.Fatalf("phase = %s", inst.Phase)
	}
	env.svc.opts.ListCachedModels = func(context.Context) (map[string]bool, error) {
		return map[string]bool{}, nil
	}
	if capability := env.capability(domain.CapabilitySlotSeparation); capability.State == domain.SlotStateReady {
		t.Fatal("missing flashcli weights still reported ready")
	}
	env.svc.opts.ListCachedModels = func(context.Context) (map[string]bool, error) {
		return map[string]bool{plan.FlashCLIRef: true}, nil
	}
	if capability := env.capability(domain.CapabilitySlotSeparation); capability.State != domain.SlotStateReady {
		t.Fatalf("cached weights state = %s", capability.State)
	}
	env.svc.prepareMu.Lock()
	env.svc.preparing = map[string]struct{}{plan.FlashCLIRef: struct{}{}}
	env.svc.prepareMu.Unlock()
	env.svc.opts.ListCachedModels = func(context.Context) (map[string]bool, error) {
		return map[string]bool{}, nil
	}
	if capability := env.capability(domain.CapabilitySlotSeparation); capability.State != domain.SlotStateInstalling {
		t.Fatalf("in-progress prepare state = %s", capability.State)
	}
	if _, err := env.svc.PlanInstallation(context.Background(), domain.CapabilitySlotSeparation); err == nil {
		t.Fatal("plan was accepted while flashcli prepare is running")
	}
}

func TestReusePullsAgainWhenMarkedWeightsAreMissing(t *testing.T) {
	content := []byte("bundle-bytes")
	server := staticSource(t, map[string][]byte{"/rel-reprepare/model.bin": content})
	plan := releasePlanFor(server, "rel-reprepare", "1.0.1", map[string][]byte{"model.bin": content})
	plan.FlashCLIRef = "asr/qwen3_asr:1.0.1"
	env := newInstallTestEnv(t, StaticReleases{Releases: map[domain.CapabilitySlotKey]ReleasePlan{
		domain.CapabilitySlotASR: plan,
	}}, newTestDownloader(server.URL))
	var pulls atomic.Int32
	env.svc.opts.PrepareModel = func(context.Context, string) error {
		pulls.Add(1)
		return nil
	}
	if inst := env.install(domain.CapabilitySlotASR); inst.Phase != domain.InstallationPhaseDone || pulls.Load() != 1 {
		t.Fatalf("first install phase=%s pulls=%d", inst.Phase, pulls.Load())
	}
	env.svc.opts.ListCachedModels = func(context.Context) (map[string]bool, error) {
		return map[string]bool{plan.FlashCLIRef: true}, nil
	}
	if again := env.create(domain.CapabilitySlotASR); again.Phase != domain.InstallationPhaseDone {
		t.Fatalf("cached reuse phase = %s", again.Phase)
	}
	time.Sleep(50 * time.Millisecond)
	if pulls.Load() != 1 {
		t.Fatalf("cached weights were pulled again: %d", pulls.Load())
	}
	env.svc.opts.ListCachedModels = func(context.Context) (map[string]bool, error) {
		return map[string]bool{plan.FlashCLIRef: false}, nil
	}
	if again := env.create(domain.CapabilitySlotASR); again.Phase != domain.InstallationPhaseDone {
		t.Fatalf("missing reuse phase = %s", again.Phase)
	}
	deadline := time.Now().Add(2 * time.Second)
	for pulls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pulls.Load() != 2 {
		t.Fatalf("missing marked weights pulls = %d, want 2", pulls.Load())
	}
}
