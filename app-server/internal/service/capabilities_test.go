package service

// FFmpeg 槽位的生产就绪派生测试：媒体工具可执行时 READY，缺失时 NEEDS_SETUP；
// 模型槽位不受影响。

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

type fakeCatalogQuerier struct {
	installs []domain.Installation
}

func (f fakeCatalogQuerier) ListInstallations(context.Context) ([]domain.Installation, error) {
	return f.installs, nil
}

type fakeEngineCatalog struct {
	plans map[domain.CapabilitySlotKey][]ReleasePlan
}

func (f fakeEngineCatalog) List(_ context.Context, slot domain.CapabilitySlotKey) ([]ReleasePlan, error) {
	return f.plans[slot], nil
}

func writeToolFile(t *testing.T, name string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestMediaToolsReady(t *testing.T) {
	executable := writeToolFile(t, "ffmpeg", 0o755)
	plain := writeToolFile(t, "ffmpeg-plain", 0o644)
	missing := filepath.Join(t.TempDir(), "missing-ffmpeg")

	if !MediaToolsReady(executable, "") {
		t.Fatal("executable ffmpeg without ffprobe must be ready")
	}
	if !MediaToolsReady(executable, executable) {
		t.Fatal("executable ffmpeg + ffprobe must be ready")
	}
	if MediaToolsReady("", "") || MediaToolsReady(missing, "") {
		t.Fatal("empty/missing ffmpeg must not be ready")
	}
	if runtime.GOOS != "windows" && MediaToolsReady(plain, "") {
		t.Fatal("non-executable ffmpeg file must not be ready")
	}
	if MediaToolsReady(executable, missing) {
		t.Fatal("configured-but-missing ffprobe must not be ready")
	}
	// Runner 生产路径只接受绝对路径；相对值即使存在也不得派生 READY。
	if MediaToolsReady("ffmpeg", "") {
		t.Fatal("relative ffmpeg path must not be ready")
	}
}

func TestInstallationCapabilitiesDeriveFFmpegReadiness(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ready     bool
		wantState domain.SlotState
	}{
		{"media tools available", true, domain.SlotStateReady},
		{"media tools missing", false, domain.SlotStateNeedsSetup},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := NewInstallationCapabilitiesWithMediaTools(tc.ready)
			caps, err := provider.Catalog(context.Background(), fakeCatalogQuerier{})
			if err != nil {
				t.Fatalf("Catalog: %v", err)
			}
			for _, capability := range caps {
				switch capability.Slot {
				case domain.CapabilitySlotFFmpeg:
					if capability.State != tc.wantState {
						t.Fatalf("ffmpeg state = %s, want %s", capability.State, tc.wantState)
					}
				case domain.CapabilitySlotASR, domain.CapabilitySlotTranslate, domain.CapabilitySlotTTS,
					domain.CapabilitySlotVoiceClone, domain.CapabilitySlotSubtitleErase,
					domain.CapabilitySlotSeparation, domain.CapabilitySlotOCR:
					// 媒体工具可用性不能伪造/改变模型槽位。
					if capability.State != domain.SlotStateNeedsSetup {
						t.Fatalf("model slot %s state = %s, want needs_setup", capability.Slot, capability.State)
					}
				}
			}
		})
	}
}

func TestInstallationCapabilitiesKeepFFmpegInstallRecord(t *testing.T) {
	provider := NewInstallationCapabilities() // 媒体工具未注入
	caps, err := provider.Catalog(context.Background(), fakeCatalogQuerier{installs: []domain.Installation{{
		ID: "inst-ffmpeg", Slot: domain.CapabilitySlotFFmpeg,
		Phase: domain.InstallationPhaseDone,
	}}})
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	for _, capability := range caps {
		if capability.Slot == domain.CapabilitySlotFFmpeg && capability.State != domain.SlotStateReady {
			t.Fatalf("ffmpeg state with done record = %s, want ready", capability.State)
		}
	}
}

func TestInstallationCapabilitiesExposeAdmittedEngines(t *testing.T) {
	release := domain.ReleaseRef{
		ReleaseID: "engine-a",
		Version:   "1.2.0",
		Digest:    "sha256:abc",
		Channel:   "stable",
	}
	plan := ReleasePlan{
		Release:       release,
		InstallBytes:  1024,
		DownloadBytes: 2048,
	}
	provider := NewInstallationCapabilities().WithEngines(fakeEngineCatalog{
		plans: map[domain.CapabilitySlotKey][]ReleasePlan{
			domain.CapabilitySlotASR: {plan},
		},
	})
	caps, err := provider.Catalog(context.Background(), fakeCatalogQuerier{})
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	var asr domain.Capability
	for _, capability := range caps {
		if capability.Slot == domain.CapabilitySlotASR {
			asr = capability
		}
	}
	if len(asr.Engines) != 1 {
		t.Fatalf("asr engines = %#v", asr.Engines)
	}
	if engine := asr.Engines[0]; engine.ID != "engine-a" || engine.Installed || engine.Selected || engine.State != domain.SlotStateNeedsSetup || engine.SizeBytes != 1024 {
		t.Fatalf("uninstalled engine = %#v", engine)
	}

	installed, err := provider.Catalog(context.Background(), fakeCatalogQuerier{installs: []domain.Installation{{
		ID: "inst-a", Slot: domain.CapabilitySlotASR,
		Phase: domain.InstallationPhaseDone, Release: &release,
	}}})
	if err != nil {
		t.Fatalf("Catalog with install: %v", err)
	}
	for _, capability := range installed {
		if capability.Slot != domain.CapabilitySlotASR {
			continue
		}
		if len(capability.Engines) != 1 {
			t.Fatalf("installed engines = %#v", capability.Engines)
		}
		engine := capability.Engines[0]
		if !engine.Installed || !engine.Selected || engine.State != domain.SlotStateReady {
			t.Fatalf("installed engine = %#v", engine)
		}
	}
}

func TestCheckTaskDispatchableForMixUsesMediaTools(t *testing.T) {
	fixtures := map[domain.CapabilitySlotKey]domain.ExecutionConfig{
		domain.CapabilitySlotFFmpeg: testExecutionFixtures()[domain.CapabilitySlotFFmpeg],
	}
	run := domain.WorkflowRun{Config: domain.RunConfig{Execution: fixtures}}
	params, lock, err := freezeTaskParams(run, domain.NodeKeyMix, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	task := domain.Task{Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyMix, Params: params, ModelLock: lock}
	frozen := testExecutionFixtures()[domain.CapabilitySlotFFmpeg].ModelLock
	admitted := fakeCatalogQuerier{installs: []domain.Installation{{
		Slot: domain.CapabilitySlotFFmpeg, Phase: domain.InstallationPhaseDone,
		ProbePassed: true, Release: &frozen,
	}}}
	ready := &Service{opts: Options{Capabilities: NewInstallationCapabilitiesWithMediaTools(true)}}
	if err := ready.CheckTaskDispatchable(context.Background(), admitted, task); err != nil {
		t.Fatalf("mix dispatch with ready media tools: %v", err)
	}
	blocked := &Service{opts: Options{Capabilities: NewInstallationCapabilities()}}
	if err := blocked.CheckTaskDispatchable(context.Background(), fakeCatalogQuerier{}, task); err == nil {
		t.Fatal("mix dispatch without media tools must be blocked")
	}
}

func TestHostMediaCapabilitiesTranslateUsesInjectedToolPath(t *testing.T) {
	q := fakeCatalogQuerier{}
	find := func(caps []domain.Capability) domain.Capability {
		for _, capability := range caps {
			if capability.Slot == domain.CapabilitySlotTranslate {
				return capability
			}
		}
		t.Fatal("translate capability missing")
		return domain.Capability{}
	}

	ready, err := HostMediaCapabilities{
		Inner: NewInstallationCapabilities(), ImageRuntime: true,
		MLSToolsPath: func() string { return "/usr/local/bin/mlstools" },
	}.Catalog(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	capability := find(ready)
	if capability.State != domain.SlotStateReady || capability.EngineName != "mlstools" {
		t.Fatalf("translate with injected path = %+v, want READY/mlstools", capability)
	}

	for name, path := range map[string]string{
		"missing":  "",
		"relative": "usr/local/bin/mlstools",
		"windows":  `C:\mlstools.exe`,
		"control":  "/usr/local/bin/mlstools\n--bad",
		"overlong": "/" + string(make([]byte, 5000)),
	} {
		caps, err := HostMediaCapabilities{
			Inner: NewInstallationCapabilities(), ImageRuntime: true,
			MLSToolsPath: func() string { return path },
		}.Catalog(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		capability := find(caps)
		if capability.State != domain.SlotStateNeedsSetup ||
			capability.ReasonCode != string(domain.ErrorCodeCapabilityUnverified) {
			t.Fatalf("%s: translate = %+v, want NEEDS_SETUP/unverified", name, capability)
		}
	}
}
