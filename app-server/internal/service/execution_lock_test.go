package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func TestCheckTaskDispatchableRejectsMissingLock(t *testing.T) {
	env := newExportTestEnv(t)
	task := domain.Task{Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyTranslate}
	err := env.svc.CheckTaskDispatchable(context.Background(), env.store, task)
	if !errors.Is(err, domain.ErrCapabilityUnverified) {
		t.Fatalf("missing lock error = %v, want capability unverified", err)
	}
}

// 新句级任务按冻结执行槽重检（voice-clone）；旧任务没有冻结槽时保持 TTS
// 兼容回退，否则其 TTS 冻结依赖会被 voice-clone 键重检成 unverified。
func TestTaskExecutionSlotSegmentFallback(t *testing.T) {
	legacy := domain.Task{Kind: domain.TaskKindSegment, NodeKey: domain.NodeKeyClone}
	if got := taskExecutionSlot(legacy); got != domain.CapabilitySlotTTS {
		t.Fatalf("legacy segment slot = %s, want %s", got, domain.CapabilitySlotTTS)
	}
	frozen := domain.Task{
		Kind: domain.TaskKindSegment, NodeKey: domain.NodeKeyClone,
		Params: map[string]any{
			"_core": map[string]any{"execution_slot": string(domain.CapabilitySlotVoiceClone)},
		},
	}
	if got := taskExecutionSlot(frozen); got != domain.CapabilitySlotVoiceClone {
		t.Fatalf("frozen segment slot = %s, want %s", got, domain.CapabilitySlotVoiceClone)
	}
	node := domain.Task{Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyClone}
	if got := taskExecutionSlot(node); got != domain.CapabilitySlotVoiceClone {
		t.Fatalf("clone node slot = %s, want %s", got, domain.CapabilitySlotVoiceClone)
	}
}

func TestCreateTaskRejectsReservedCoreParams(t *testing.T) {
	env := newExportTestEnv(t)
	project := env.seedProject("reserved params")
	_, err := env.svc.CreateTask(context.Background(), Idem{}, CreateTaskRequest{
		ProjectID: project.ID, Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyAnalyze,
		Params: map[string]any{"_core": map[string]any{"segment_cache_key": "forged"}},
	})
	if !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("reserved Core params error = %v", err)
	}
}

func testExecutionFixtures() map[domain.CapabilitySlotKey]domain.ExecutionConfig {
	fixtures := map[domain.CapabilitySlotKey]domain.ExecutionConfig{}
	for _, slot := range capabilitySlots {
		fixtures[slot] = domain.ExecutionConfig{
			ModelLock: domain.ReleaseRef{ReleaseID: "fixture-" + string(slot), Version: "1", Digest: "fixture-digest-" + string(slot)},
			DeviceID:  "cpu", Params: map[string]any{},
		}
	}
	return fixtures
}

func testRelease(id string) domain.ReleaseRef {
	return domain.ReleaseRef{ReleaseID: id, Version: "1", Digest: "sha256:" + id}
}

func seedInstallation(
	t *testing.T,
	env *exportTestEnv,
	id string,
	slot domain.CapabilitySlotKey,
	phase domain.InstallationPhase,
	verified int,
	probe bool,
	release *domain.ReleaseRef,
	createdAt time.Time,
) domain.Installation {
	t.Helper()
	inst := domain.Installation{
		ID: id, Slot: slot, Phase: phase, Progress: 100, VerifiedFiles: verified,
		ProbePassed: probe, Release: release, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := env.store.CreateInstallation(context.Background(), inst); err != nil {
		t.Fatalf("CreateInstallation(%s): %v", id, err)
	}
	return inst
}

func frozenTask(t *testing.T, run domain.WorkflowRun, node domain.PipelineNodeKey, kind domain.TaskKind, params map[string]any) domain.Task {
	t.Helper()
	taskParams, lock, err := freezeTaskParams(run, node, params)
	if err != nil {
		t.Fatalf("freezeTaskParams(%s): %v", node, err)
	}
	return domain.Task{Kind: kind, NodeKey: node, Params: taskParams, ModelLock: lock}
}

func taskCoreParams(t *testing.T, task *domain.Task) map[string]any {
	t.Helper()
	core, ok := task.Params["_core"].(map[string]any)
	if !ok {
		t.Fatalf("task params missing _core: %+v", task.Params)
	}
	return core
}

func taskDependencyParams(t *testing.T, task *domain.Task, slot domain.CapabilitySlotKey) map[string]any {
	t.Helper()
	core := taskCoreParams(t, task)
	dependencies, ok := core["execution_dependencies"].(map[string]any)
	if !ok {
		t.Fatalf("task _core missing execution_dependencies: %+v", core)
	}
	dependency, ok := dependencies[string(slot)].(map[string]any)
	if !ok {
		t.Fatalf("task execution_dependencies missing slot %s: %+v", slot, dependencies)
	}
	return dependency
}

func TestFreezeExecutionAdmitsVerifiedInstallationWithoutProbe(t *testing.T) {
	cases := []struct {
		name     string
		phase    domain.InstallationPhase
		verified int
		probe    bool
		lock     *domain.ReleaseRef
		want     bool
	}{
		{name: "no release", phase: domain.InstallationPhaseDone, verified: 1, probe: true},
		{name: "no verified files", phase: domain.InstallationPhaseDone, probe: true, lock: &domain.ReleaseRef{ReleaseID: "translate", Version: "1", Digest: "digest"}},
		{name: "empty digest", phase: domain.InstallationPhaseDone, verified: 1, probe: true, lock: &domain.ReleaseRef{ReleaseID: "translate", Version: "1"}},
		{name: "not done", phase: domain.InstallationPhaseFailed, verified: 1, probe: true, lock: &domain.ReleaseRef{ReleaseID: "translate", Version: "1", Digest: "digest"}},
		{name: "verified without probe", phase: domain.InstallationPhaseDone, verified: 1, probe: false, lock: &domain.ReleaseRef{ReleaseID: "translate", Version: "1", Digest: "digest"}, want: true},
		{name: "verified with probe", phase: domain.InstallationPhaseDone, verified: 1, probe: true, lock: &domain.ReleaseRef{ReleaseID: "translate", Version: "1", Digest: "digest"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newExportTestEnv(t)
			env.svc.opts.Capabilities = InstallationCapabilities{}
			seedInstallation(t, env, "installation", domain.CapabilitySlotTranslate, tc.phase, tc.verified, tc.probe, tc.lock, env.svc.Now())
			tpl, err := pipelineTemplateFor(templateToolTranslate)
			if err != nil {
				t.Fatal(err)
			}
			frozen, err := env.svc.freezeExecutionConfig(context.Background(), env.store, CreateWorkflowRunInput{Template: templateToolTranslate}, tpl)
			if !tc.want {
				if !errors.Is(err, domain.ErrCapabilityUnverified) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			cfg := frozen[domain.CapabilitySlotTranslate]
			if cfg.ModelLock != *tc.lock || cfg.DeviceID == "" || cfg.DeviceID == "auto" {
				t.Fatalf("config = %+v", cfg)
			}
		})
	}
}

// A newer done record without verified files must not silently fall back to an
// older valid installation: active selection stays "latest done".
func TestFreezeExecutionDoesNotFallBackToOlderDoneInstallation(t *testing.T) {
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = InstallationCapabilities{}
	now := env.svc.Now()
	older := testRelease("translate-v1")
	newer := testRelease("translate-v2")
	seedInstallation(t, env, "older", domain.CapabilitySlotTranslate, domain.InstallationPhaseDone, 2, false, &older, now.Add(-2*time.Minute))
	seedInstallation(t, env, "newer", domain.CapabilitySlotTranslate, domain.InstallationPhaseDone, 0, false, &newer, now.Add(-time.Minute))
	tpl, err := pipelineTemplateFor(templateToolTranslate)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.svc.freezeExecutionConfig(context.Background(), env.store, CreateWorkflowRunInput{Template: templateToolTranslate}, tpl)
	if !errors.Is(err, domain.ErrCapabilityUnverified) {
		t.Fatalf("freeze with invalid newest done record error = %v, want capability unverified", err)
	}
}

// A failed upgrade must not shadow the last done version.
func TestFreezeExecutionIgnoresFailedUpgradeAndUsesOlderDone(t *testing.T) {
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = InstallationCapabilities{}
	now := env.svc.Now()
	older := testRelease("translate-v1")
	failed := testRelease("translate-v2")
	seedInstallation(t, env, "older", domain.CapabilitySlotTranslate, domain.InstallationPhaseDone, 2, false, &older, now.Add(-2*time.Minute))
	seedInstallation(t, env, "failed", domain.CapabilitySlotTranslate, domain.InstallationPhaseFailed, 1, false, &failed, now.Add(-time.Minute))
	tpl, err := pipelineTemplateFor(templateToolTranslate)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := env.svc.freezeExecutionConfig(context.Background(), env.store, CreateWorkflowRunInput{Template: templateToolTranslate}, tpl)
	if err != nil {
		t.Fatalf("freeze with failed upgrade: %v", err)
	}
	if got := frozen[domain.CapabilitySlotTranslate].ModelLock; got != older {
		t.Fatalf("frozen lock = %+v, want retained done release %+v", got, older)
	}
}

// Mock catalog READY may drive display state but cannot replace installation
// evidence at execution freeze.
func TestPreflightRejectsMockReadyWithoutInstallEvidence(t *testing.T) {
	env := newExportTestEnv(t)
	env.svc.opts.PipelineOrchestration = true
	env.svc.opts.Capabilities = MockModelCapabilities{
		Inner: NewInstallationCapabilities(),
		Slots: []domain.CapabilitySlotKey{domain.CapabilitySlotTranslate},
	}
	project := env.seedProject("mock admission")
	path := env.tempFile("source.srt", "1\n00:00:00,000 --> 00:00:01,000\nHello\n")
	asset := env.seedMediaAsset(project.ID, "source.srt", domain.AssetTypeSubtitle, path, nil)
	result, err := env.svc.Preflight(context.Background(), CreateWorkflowRunInput{
		ProjectID: string(project.ID), Name: "mock admission", Template: templateToolTranslate,
		SourceLanguage: "en", TargetLanguages: []string{"zh"}, InputRevisionIDs: []string{string(asset.ID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.HasBlocker() {
		t.Fatal("mock READY must not admit a model slot without installation evidence")
	}
	found := false
	for _, issue := range result.Issues {
		if issue.Severity == domain.PreflightBlocker && issue.Code == string(domain.ErrorCodeCapabilityUnverified) {
			found = true
		}
	}
	if !found {
		t.Fatalf("preflight issues = %+v, want CAPABILITY_UNVERIFIED blocker", result.Issues)
	}
}

func TestFreezeExecutionFFmpegUsesMediaReadiness(t *testing.T) {
	ctx := context.Background()
	env := newExportTestEnv(t)
	tpl, err := pipelineTemplateForInput(CreateWorkflowRunInput{Template: templateCustomPipeline, Features: []string{featureMix}})
	if err != nil {
		t.Fatal(err)
	}
	in := CreateWorkflowRunInput{Template: templateCustomPipeline, Features: []string{featureMix}}
	env.svc.opts.Capabilities = HostMediaCapabilities{Inner: NewInstallationCapabilities(), ImageRuntime: true}
	frozen, err := env.svc.freezeExecutionConfig(ctx, env.store, in, tpl)
	if err != nil {
		t.Fatalf("freeze with ready media tools: %v", err)
	}
	cfg, ok := frozen[domain.CapabilitySlotFFmpeg]
	if !ok {
		t.Fatal("frozen execution missing ffmpeg slot")
	}
	if cfg.ModelLock != (domain.ReleaseRef{}) {
		t.Fatalf("ffmpeg model lock = %+v, want empty", cfg.ModelLock)
	}
	if cfg.DeviceID != "cpu" {
		t.Fatalf("ffmpeg device = %q, want cpu", cfg.DeviceID)
	}
	run := domain.WorkflowRun{Config: domain.RunConfig{Execution: frozen}}
	params, lock, err := freezeTaskParams(run, domain.NodeKeyMix, map[string]any{"language": "zh"})
	if err != nil {
		t.Fatal(err)
	}
	if lock != nil {
		t.Fatalf("08 model lock = %+v, want nil", lock)
	}
	if params["device_id"] != "cpu" {
		t.Fatalf("08 device = %v, want cpu", params["device_id"])
	}
	task := domain.Task{Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyMix, Params: params, ModelLock: lock}
	if err := env.svc.CheckTaskDispatchable(ctx, env.store, task); err != nil {
		t.Fatalf("ready 08 dispatch: %v", err)
	}

	env.svc.opts.Capabilities = HostMediaCapabilities{
		Inner: NewInstallationCapabilities(), ImageRuntime: false,
		FFmpegPath: func() string { return "" },
	}
	if err := env.svc.CheckTaskDispatchable(ctx, env.store, task); err == nil {
		t.Fatal("08 dispatch must recheck media readiness")
	}
	if _, err := env.svc.freezeExecutionConfig(ctx, env.store, in, tpl); !errors.Is(err, domain.ErrCapabilityUnverified) {
		t.Fatalf("freeze without media tools error = %v, want capability unverified", err)
	}
}

func TestFrozenExecutionFFmpegLockRules(t *testing.T) {
	legacy := testRelease("legacy-ffmpeg")
	cases := []struct {
		name    string
		config  *domain.ExecutionConfig
		wantErr bool
	}{
		{name: "missing config", wantErr: true},
		{name: "empty model lock", config: &domain.ExecutionConfig{DeviceID: "cpu", Params: map[string]any{}}},
		{name: "legacy valid lock", config: &domain.ExecutionConfig{ModelLock: legacy, DeviceID: "cpu", Params: map[string]any{}}},
		{name: "partial model lock", config: &domain.ExecutionConfig{ModelLock: domain.ReleaseRef{ReleaseID: "partial"}, DeviceID: "cpu"}, wantErr: true},
		{name: "automatic device", config: &domain.ExecutionConfig{DeviceID: "auto"}, wantErr: true},
		{name: "non-cpu device", config: &domain.ExecutionConfig{DeviceID: "mps"}, wantErr: true},
		{name: "negative context radius", config: &domain.ExecutionConfig{DeviceID: "cpu", ContextRadius: -1}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			execution := map[domain.CapabilitySlotKey]domain.ExecutionConfig{}
			if tc.config != nil {
				execution[domain.CapabilitySlotFFmpeg] = *tc.config
			}
			run := domain.WorkflowRun{Config: domain.RunConfig{Execution: execution}}
			cfg, err := frozenExecution(run, domain.NodeKeyMix)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrCapabilityUnverified) {
					t.Fatalf("error = %v, want capability unverified", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ModelLock != tc.config.ModelLock || cfg.DeviceID != "cpu" {
				t.Fatalf("config = %+v", cfg)
			}
		})
	}
	t.Run("legacy ffmpeg task lock preserved", func(t *testing.T) {
		run := domain.WorkflowRun{Config: domain.RunConfig{Execution: map[domain.CapabilitySlotKey]domain.ExecutionConfig{
			domain.CapabilitySlotFFmpeg: {ModelLock: legacy, DeviceID: "cpu", Params: map[string]any{}},
		}}}
		params, lock, err := freezeTaskParams(run, domain.NodeKeyMix, map[string]any{"language": "zh"})
		if err != nil {
			t.Fatal(err)
		}
		if lock == nil || *lock != legacy {
			t.Fatalf("legacy ffmpeg task lock = %+v, want %+v", lock, legacy)
		}
		if params["device_id"] != "cpu" {
			t.Fatalf("legacy ffmpeg device = %v, want cpu", params["device_id"])
		}
	})
	t.Run("model slot empty lock rejected", func(t *testing.T) {
		run := domain.WorkflowRun{Config: domain.RunConfig{Execution: map[domain.CapabilitySlotKey]domain.ExecutionConfig{
			domain.CapabilitySlotTTS: {DeviceID: "cpu", Params: map[string]any{}},
		}}}
		if _, err := frozenExecution(run, domain.NodeKeyClone); !errors.Is(err, domain.ErrCapabilityUnverified) {
			t.Fatalf("model slot empty lock error = %v, want capability unverified", err)
		}
	})
}

func TestTaskDispatchRequiresVerifiedInstallEvidence(t *testing.T) {
	release := testRelease("translate")
	partial := domain.ReleaseRef{ReleaseID: "translate", Version: "1"}
	cases := []struct {
		name        string
		phase       domain.InstallationPhase
		verified    int
		probe       bool
		instRelease *domain.ReleaseRef
		wantErr     bool
	}{
		{name: "verified without probe", phase: domain.InstallationPhaseDone, verified: 2, probe: false, instRelease: &release},
		{name: "verified with probe", phase: domain.InstallationPhaseDone, verified: 2, probe: true, instRelease: &release},
		{name: "zero verified without probe", phase: domain.InstallationPhaseDone, verified: 0, probe: false, instRelease: &release, wantErr: true},
		{name: "zero verified with probe", phase: domain.InstallationPhaseDone, verified: 0, probe: true, instRelease: &release, wantErr: true},
		{name: "failed phase retains files", phase: domain.InstallationPhaseFailed, verified: 2, probe: true, instRelease: &release, wantErr: true},
		{name: "installation without release", phase: domain.InstallationPhaseDone, verified: 2, probe: true, wantErr: true},
		{name: "installation partial release", phase: domain.InstallationPhaseDone, verified: 2, probe: true, instRelease: &partial, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newExportTestEnv(t)
			env.svc.opts.Capabilities = InstallationCapabilities{}
			seedInstallation(t, env, "translate", domain.CapabilitySlotTranslate, tc.phase, tc.verified, tc.probe, tc.instRelease, env.svc.Now())
			run := domain.WorkflowRun{Config: domain.RunConfig{Execution: map[domain.CapabilitySlotKey]domain.ExecutionConfig{
				domain.CapabilitySlotTranslate: {ModelLock: release, DeviceID: "cpu", Params: map[string]any{}},
			}}}
			task := frozenTask(t, run, domain.NodeKeyTranslate, domain.TaskKindNode, map[string]any{"language": "zh"})
			err := env.svc.CheckTaskDispatchable(context.Background(), env.store, task)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrCapabilityUnverified) {
					t.Fatalf("dispatch error = %v, want capability unverified", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("dispatch with verified install: %v", err)
			}
		})
	}
}

func TestTaskDispatchRejectsTamperedFrozenConfiguration(t *testing.T) {
	ctx := context.Background()
	release := testRelease("translate")
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = HostMediaCapabilities{Inner: NewInstallationCapabilities(), ImageRuntime: true}
	seedInstallation(t, env, "translate", domain.CapabilitySlotTranslate, domain.InstallationPhaseDone, 2, false, &release, env.svc.Now())
	run := domain.WorkflowRun{Config: domain.RunConfig{Execution: map[domain.CapabilitySlotKey]domain.ExecutionConfig{
		domain.CapabilitySlotTranslate: {ModelLock: release, DeviceID: "cpu", Params: map[string]any{}},
		domain.CapabilitySlotFFmpeg:    {DeviceID: "cpu", Params: map[string]any{}},
	}}}
	newTask := func() domain.Task {
		return frozenTask(t, run, domain.NodeKeyTranslate, domain.TaskKindNode, map[string]any{"language": "zh"})
	}
	base := newTask()
	if err := env.svc.CheckTaskDispatchable(ctx, env.store, base); err != nil {
		t.Fatalf("baseline dispatch: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*testing.T, *domain.Task)
	}{
		{name: "missing model lock", mutate: func(_ *testing.T, task *domain.Task) { task.ModelLock = nil }},
		{name: "dependency lock mismatch", mutate: func(t *testing.T, task *domain.Task) {
			taskDependencyParams(t, task, domain.CapabilitySlotTranslate)["model_lock"] = map[string]any{
				"release_id": "other", "version": "9", "digest": "sha256:other",
			}
		}},
		{name: "task device mismatch", mutate: func(_ *testing.T, task *domain.Task) { task.Params["device_id"] = "mps" }},
		{name: "sibling dependency device auto", mutate: func(t *testing.T, task *domain.Task) {
			taskDependencyParams(t, task, domain.CapabilitySlotFFmpeg)["device_id"] = "auto"
		}},
		{name: "ffmpeg dependency device not cpu", mutate: func(t *testing.T, task *domain.Task) {
			taskDependencyParams(t, task, domain.CapabilitySlotFFmpeg)["device_id"] = "mps"
		}},
		{name: "malformed dependencies", mutate: func(t *testing.T, task *domain.Task) {
			taskCoreParams(t, task)["execution_dependencies"] = "not-an-object"
		}},
		{name: "invalid execution slot", mutate: func(t *testing.T, task *domain.Task) {
			taskCoreParams(t, task)["execution_slot"] = "bogus"
		}},
		{name: "missing execution slot dependency", mutate: func(t *testing.T, task *domain.Task) {
			delete(taskCoreParams(t, task)["execution_dependencies"].(map[string]any), string(domain.CapabilitySlotTranslate))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := newTask()
			tc.mutate(t, &task)
			if err := env.svc.CheckTaskDispatchable(ctx, env.store, task); !errors.Is(err, domain.ErrCapabilityUnverified) {
				t.Fatalf("tampered dispatch error = %v, want capability unverified", err)
			}
		})
	}
}

// Mock catalog READY must not bypass dispatch evidence either.
func TestTaskDispatchRejectsMockReadyWithoutInstallEvidence(t *testing.T) {
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = MockModelCapabilities{
		Inner: NewInstallationCapabilities(),
		Slots: []domain.CapabilitySlotKey{domain.CapabilitySlotTranslate},
	}
	release := testRelease("translate")
	run := domain.WorkflowRun{Config: domain.RunConfig{Execution: map[domain.CapabilitySlotKey]domain.ExecutionConfig{
		domain.CapabilitySlotTranslate: {ModelLock: release, DeviceID: "cpu", Params: map[string]any{}},
	}}}
	task := frozenTask(t, run, domain.NodeKeyTranslate, domain.TaskKindNode, map[string]any{"language": "zh"})
	if err := env.svc.CheckTaskDispatchable(context.Background(), env.store, task); !errors.Is(err, domain.ErrCapabilityUnverified) {
		t.Fatalf("mock READY dispatch error = %v, want capability unverified", err)
	}
}

// FFmpeg accepts exactly two task-lock forms: nil, or a complete valid legacy
// lock. A non-nil partial lock must not be normalized into the nil form.
func TestTaskDispatchFFmpegLockForms(t *testing.T) {
	ctx := context.Background()
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = HostMediaCapabilities{Inner: NewInstallationCapabilities(), ImageRuntime: true}
	emptyRun := domain.WorkflowRun{Config: domain.RunConfig{Execution: map[domain.CapabilitySlotKey]domain.ExecutionConfig{
		domain.CapabilitySlotFFmpeg: {DeviceID: "cpu", Params: map[string]any{}},
	}}}
	lockless := frozenTask(t, emptyRun, domain.NodeKeyMix, domain.TaskKindNode, map[string]any{"language": "zh"})
	if lockless.ModelLock != nil {
		t.Fatalf("production FFmpeg task lock = %+v, want nil", lockless.ModelLock)
	}
	if err := env.svc.CheckTaskDispatchable(ctx, env.store, lockless); err != nil {
		t.Fatalf("lockless FFmpeg dispatch: %v", err)
	}

	partials := []struct {
		name string
		lock domain.ReleaseRef
	}{
		{name: "missing version and digest", lock: domain.ReleaseRef{ReleaseID: "legacy"}},
		{name: "missing digest", lock: domain.ReleaseRef{ReleaseID: "legacy", Version: "1"}},
		{name: "missing version", lock: domain.ReleaseRef{ReleaseID: "legacy", Digest: "sha256:legacy"}},
		{name: "only channel", lock: domain.ReleaseRef{Channel: "beta"}},
	}
	for _, tc := range partials {
		t.Run(tc.name, func(t *testing.T) {
			task := lockless
			lock := tc.lock
			task.ModelLock = &lock
			if err := env.svc.CheckTaskDispatchable(ctx, env.store, task); !errors.Is(err, domain.ErrCapabilityUnverified) {
				t.Fatalf("partial FFmpeg task lock dispatch error = %v, want capability unverified", err)
			}
		})
	}

	legacy := testRelease("legacy-ffmpeg")
	legacyRun := domain.WorkflowRun{Config: domain.RunConfig{Execution: map[domain.CapabilitySlotKey]domain.ExecutionConfig{
		domain.CapabilitySlotFFmpeg: {ModelLock: legacy, DeviceID: "cpu", Params: map[string]any{}},
	}}}
	legacyTask := frozenTask(t, legacyRun, domain.NodeKeyMix, domain.TaskKindNode, map[string]any{"language": "zh"})
	if legacyTask.ModelLock == nil || *legacyTask.ModelLock != legacy {
		t.Fatalf("legacy FFmpeg task lock = %+v, want %+v", legacyTask.ModelLock, legacy)
	}
	if err := env.svc.CheckTaskDispatchable(ctx, env.store, legacyTask); err != nil {
		t.Fatalf("legacy FFmpeg lock dispatch: %v", err)
	}
}

// Lockless FFmpeg tasks must carry frozen dependency metadata; without it there
// is no frozen device/dependency configuration to recheck.
func TestTaskDispatchFFmpegRequiresFrozenDependencies(t *testing.T) {
	ctx := context.Background()
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = HostMediaCapabilities{Inner: NewInstallationCapabilities(), ImageRuntime: true}
	cases := []struct {
		name   string
		params map[string]any
	}{
		{name: "no core params"},
		{name: "core without execution dependencies", params: map[string]any{
			"_core":     map[string]any{"execution_slot": string(domain.CapabilitySlotFFmpeg)},
			"device_id": "cpu",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := domain.Task{Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyMix, Params: tc.params}
			if err := env.svc.CheckTaskDispatchable(ctx, env.store, task); !errors.Is(err, domain.ErrCapabilityUnverified) {
				t.Fatalf("lockless FFmpeg dispatch error = %v, want capability unverified", err)
			}
		})
	}
}

// An FFmpeg task's frozen model dependencies must still carry complete valid
// locks; empty or partial sibling locks are rejected.
func TestTaskDispatchFFmpegDependencyModelLocks(t *testing.T) {
	ctx := context.Background()
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = HostMediaCapabilities{Inner: NewInstallationCapabilities(), ImageRuntime: true}
	cases := []struct {
		name string
		lock domain.ReleaseRef
	}{
		{name: "empty model lock"},
		{name: "partial model lock", lock: domain.ReleaseRef{ReleaseID: "translate"}},
		{name: "only channel model lock", lock: domain.ReleaseRef{Channel: "beta"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := domain.WorkflowRun{Config: domain.RunConfig{Execution: map[domain.CapabilitySlotKey]domain.ExecutionConfig{
				domain.CapabilitySlotFFmpeg:    {DeviceID: "cpu", Params: map[string]any{}},
				domain.CapabilitySlotTranslate: {ModelLock: tc.lock, DeviceID: "cpu", Params: map[string]any{}},
			}}}
			task := frozenTask(t, run, domain.NodeKeyMix, domain.TaskKindNode, map[string]any{"language": "zh"})
			if err := env.svc.CheckTaskDispatchable(ctx, env.store, task); !errors.Is(err, domain.ErrCapabilityUnverified) {
				t.Fatalf("FFmpeg with invalid model dependency error = %v, want capability unverified", err)
			}
		})
	}
}

// A lockless FFmpeg task must not skip frozen model dependencies: only the
// FFmpeg entry itself has the media-tool exception.
func TestFFmpegTaskStillChecksModelDependencies(t *testing.T) {
	ctx := context.Background()
	release := testRelease("translate")
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = HostMediaCapabilities{Inner: NewInstallationCapabilities(), ImageRuntime: true}
	seedInstallation(t, env, "translate", domain.CapabilitySlotTranslate, domain.InstallationPhaseDone, 2, false, &release, env.svc.Now())
	run := domain.WorkflowRun{Config: domain.RunConfig{Execution: map[domain.CapabilitySlotKey]domain.ExecutionConfig{
		domain.CapabilitySlotFFmpeg:    {DeviceID: "cpu", Params: map[string]any{}},
		domain.CapabilitySlotTranslate: {ModelLock: release, DeviceID: "cpu", Params: map[string]any{}},
	}}}
	newTask := func() domain.Task {
		return frozenTask(t, run, domain.NodeKeyMix, domain.TaskKindNode, map[string]any{"language": "zh"})
	}
	base := newTask()
	if base.ModelLock != nil {
		t.Fatalf("08 model lock = %+v, want nil", base.ModelLock)
	}
	if err := env.svc.CheckTaskDispatchable(ctx, env.store, base); err != nil {
		t.Fatalf("baseline 08 dispatch: %v", err)
	}

	tampered := newTask()
	taskDependencyParams(t, &tampered, domain.CapabilitySlotTranslate)["model_lock"] = map[string]any{
		"release_id": "other", "version": "9", "digest": "sha256:other",
	}
	if err := env.svc.CheckTaskDispatchable(ctx, env.store, tampered); !errors.Is(err, domain.ErrCapabilityUnverified) {
		t.Fatalf("08 with mismatched model dependency error = %v, want capability unverified", err)
	}

	if _, err := env.svc.DeleteInstalledPackage(ctx, Idem{}, string(domain.CapabilitySlotTranslate)); err != nil {
		t.Fatal(err)
	}
	if err := env.svc.CheckTaskDispatchable(ctx, env.store, base); !errors.Is(err, domain.ErrCapabilityUnverified) {
		t.Fatalf("08 without retained model evidence error = %v, want capability unverified", err)
	}
}

// Freezing a production Run must keep the exact installed release; a later
// version or damaged evidence must not swap the frozen lock.
func TestDispatchKeepsFrozenReleaseAndRequiresRetainedEvidence(t *testing.T) {
	ctx := context.Background()
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = InstallationCapabilities{}
	now := env.svc.Now()
	v1 := testRelease("translate-v1")
	v2 := testRelease("translate-v2")
	seedInstallation(t, env, "v1", domain.CapabilitySlotTranslate, domain.InstallationPhaseDone, 2, false, &v1, now.Add(-2*time.Minute))
	tpl, err := pipelineTemplateFor(templateToolTranslate)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := env.svc.freezeExecutionConfig(ctx, env.store, CreateWorkflowRunInput{Template: templateToolTranslate}, tpl)
	if err != nil {
		t.Fatal(err)
	}
	if got := frozen[domain.CapabilitySlotTranslate].ModelLock; got != v1 {
		t.Fatalf("frozen lock = %+v, want %+v", got, v1)
	}
	run := domain.WorkflowRun{Config: domain.RunConfig{Execution: frozen}}
	task := frozenTask(t, run, domain.NodeKeyTranslate, domain.TaskKindNode, map[string]any{"language": "zh"})

	seedInstallation(t, env, "v2", domain.CapabilitySlotTranslate, domain.InstallationPhaseDone, 2, false, &v2, now.Add(-time.Minute))
	if err := env.svc.CheckTaskDispatchable(ctx, env.store, task); err != nil {
		t.Fatalf("dispatch with retained old release: %v", err)
	}

	if _, err := env.store.UpdateInstallationPhase(ctx, "v1", domain.InstallationPhaseDone, 100, 0, nil, false, now); err != nil {
		t.Fatal(err)
	}
	if err := env.svc.CheckTaskDispatchable(ctx, env.store, task); !errors.Is(err, domain.ErrCapabilityUnverified) {
		t.Fatalf("dispatch with damaged frozen release evidence error = %v, want capability unverified", err)
	}
}

func TestFreezeExecutionAdmitsRollbackRetainedVersion(t *testing.T) {
	ctx := context.Background()
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = InstallationCapabilities{}
	now := env.svc.Now()
	v1 := testRelease("translate-v1")
	v2 := testRelease("translate-v2")
	seedVerifiedInstallation(t, env.store, env.dataDir, "v1", domain.CapabilitySlotTranslate, v1, nil,
		map[string][]byte{"model.bin": []byte("translate-model-v1")}, now.Add(-2*time.Minute))
	seedVerifiedInstallation(t, env.store, env.dataDir, "v2", domain.CapabilitySlotTranslate, v2, nil,
		map[string][]byte{"model.bin": []byte("translate-model-v2")}, now.Add(-time.Minute))
	// 回滚只切当前引用，不复制 Done；文件保留在校验通过的物理实体上。
	resp, err := env.svc.RollbackInstalledPackage(ctx, Idem{}, string(domain.CapabilitySlotTranslate))
	if err != nil {
		t.Fatalf("RollbackInstalledPackage: %v", err)
	}
	rolled, ok := resp.Body.(domain.Installation)
	if !ok || rolled.ID != "v1" || rolled.Phase != domain.InstallationPhaseDone {
		t.Fatalf("rollback body = %+v", resp.Body)
	}
	installs, err := env.store.ListInstallations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(installs) != 2 {
		t.Fatalf("rollback created a new physical record: %d", len(installs))
	}
	tpl, err := pipelineTemplateFor(templateToolTranslate)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := env.svc.freezeExecutionConfig(ctx, env.store, CreateWorkflowRunInput{Template: templateToolTranslate}, tpl)
	if err != nil {
		t.Fatalf("freeze after rollback: %v", err)
	}
	if got := frozen[domain.CapabilitySlotTranslate].ModelLock; got != v1 {
		t.Fatalf("rollback frozen lock = %+v, want %+v", got, v1)
	}
}

// Production-form admission: HostMediaCapabilities + real installation records,
// no ExecutionFixtures. A done separation install with verified files but
// ProbePassed=false must pass preflight/create and gate the 03 task.
func TestProductionProviderAdmitsToolSeparateWithoutProbe(t *testing.T) {
	ctx := context.Background()
	env := newExportTestEnv(t)
	env.svc.opts.PipelineOrchestration = true
	env.svc.opts.Capabilities = HostMediaCapabilities{Inner: NewInstallationCapabilities(), ImageRuntime: true}
	project := env.seedProject("production admission")
	path := env.tempFile("source.wav", "fixture-audio")
	asset := env.seedMediaAsset(project.ID, "source.wav", domain.AssetTypeAudio, path, nil)
	release := testRelease("separation")
	seedInstallation(t, env, "inst-separation", domain.CapabilitySlotSeparation, domain.InstallationPhaseDone, 2, false, &release, env.svc.Now())

	in := CreateWorkflowRunInput{
		ProjectID: string(project.ID), Name: "分离准入", Template: templateToolSeparate,
		SourceLanguage: "und", TargetLanguages: []string{}, InputRevisionIDs: []string{string(asset.ID)},
	}
	result, err := env.svc.Preflight(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if result.HasBlocker() {
		t.Fatalf("preflight blockers = %+v", result.Issues)
	}
	response, err := env.svc.CreateWorkflowRun(ctx, Idem{}, in)
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := response.Body.(domain.WorkflowRun)
	separation, ok := run.Config.Execution[domain.CapabilitySlotSeparation]
	if !ok || separation.ModelLock != release {
		t.Fatalf("frozen separation config = %+v", run.Config.Execution)
	}
	ffmpeg, ok := run.Config.Execution[domain.CapabilitySlotFFmpeg]
	if !ok || ffmpeg.ModelLock != (domain.ReleaseRef{}) || ffmpeg.DeviceID != "cpu" {
		t.Fatalf("frozen ffmpeg config = %+v", ffmpeg)
	}

	tasks, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var separate domain.Task
	for _, task := range tasks {
		if task.NodeKey == domain.NodeKeySeparate {
			separate = task
		}
	}
	if separate.ID == "" {
		t.Fatalf("03 task missing: %+v", tasks)
	}
	if separate.ModelLock == nil || *separate.ModelLock != release {
		t.Fatalf("03 model lock = %+v, want %+v", separate.ModelLock, release)
	}
	dependencies, ok := taskCoreParams(t, &separate)["execution_dependencies"].(map[string]any)
	if !ok {
		t.Fatal("03 task missing frozen execution dependencies")
	}
	frozenFFmpeg, ok := dependencies[string(domain.CapabilitySlotFFmpeg)].(map[string]any)
	if !ok || frozenFFmpeg["device_id"] != "cpu" {
		t.Fatalf("03 ffmpeg dependency = %+v", dependencies)
	}
	lock, ok := frozenFFmpeg["model_lock"].(map[string]any)
	if !ok || lock["release_id"] != "" {
		t.Fatalf("03 ffmpeg dependency must carry an empty model lock: %+v", frozenFFmpeg)
	}
	if err := env.svc.CheckTaskDispatchable(ctx, env.store, separate); err != nil {
		t.Fatalf("03 dispatch: %v", err)
	}

	inst, err := env.store.GetInstallation(ctx, "inst-separation")
	if err != nil {
		t.Fatal(err)
	}
	if inst.ProbePassed {
		t.Fatal("Core must not forge ProbePassed")
	}

	env.svc.opts.Capabilities = HostMediaCapabilities{
		Inner: NewInstallationCapabilities(), ImageRuntime: false,
		FFmpegPath: func() string { return "" },
	}
	if err := env.svc.CheckTaskDispatchable(ctx, env.store, separate); err == nil {
		t.Fatal("03 dispatch must recheck media readiness through the FFmpeg dependency")
	}
}

func TestFrozenExecutionDoesNotChangeWithCurrentEngineSettings(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	env.svc.opts.PipelineOrchestration = true
	env.svc.opts.Capabilities = StaticCapabilities{ExecutionFixtures: testExecutionFixtures()}
	project := env.seedProject("frozen execution")
	path := env.tempFile("source.srt", "1\n00:00:00,000 --> 00:00:01,000\nHello\n")
	asset := env.seedMediaAsset(project.ID, "source.srt", domain.AssetTypeSubtitle, path, nil)
	response, err := env.svc.CreateWorkflowRun(ctx, Idem{}, CreateWorkflowRunInput{
		ProjectID: project.ID, Name: "frozen", Template: templateToolTranslate, SourceLanguage: "en",
		TargetLanguages: []string{"zh"}, InputRevisionIDs: []string{asset.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := response.Body.(domain.WorkflowRun)
	fixtures := testExecutionFixtures()
	updated := fixtures[domain.CapabilitySlotTranslate]
	updated.ModelLock.Version = "2"
	updated.DeviceID = "gpu-after-creation"
	fixtures[domain.CapabilitySlotTranslate] = updated
	env.svc.opts.Capabilities = StaticCapabilities{ExecutionFixtures: fixtures}
	stored, err := env.store.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg := stored.Config.Execution[domain.CapabilitySlotTranslate]
	if cfg.ModelLock.Version != "1" || cfg.DeviceID != "cpu" {
		t.Fatalf("frozen configuration changed: %+v", cfg)
	}
	params, lock, err := freezeTaskParams(stored, domain.NodeKeyTranslate, map[string]any{"language": "zh"})
	if err != nil {
		t.Fatal(err)
	}
	if lock.Version != "1" || params["device_id"] != "cpu" {
		t.Fatalf("task changed frozen configuration: %+v / %+v", lock, params)
	}
	forged := domain.ReleaseRef{ReleaseID: "forged", Version: "999", Digest: "forged"}
	taskResponse, err := env.svc.CreateTask(ctx, Idem{}, CreateTaskRequest{
		ProjectID: project.ID, RunID: run.ID, Kind: domain.TaskKindNode,
		NodeKey: domain.NodeKeyTranslate, Language: "zh", ModelLock: &forged,
	})
	if err != nil {
		t.Fatal(err)
	}
	task := taskResponse.Body.(domain.Task)
	if task.ModelLock == nil || lock == nil || *task.ModelLock != *lock {
		t.Fatalf("caller lock replaced frozen Run lock: %+v", task.ModelLock)
	}
	if task.Params["device_id"] != "cpu" {
		t.Fatalf("caller task bypassed frozen execution params: %+v", task.Params)
	}
}

func TestFreezeExecutionTranslateUsesToolReadiness(t *testing.T) {
	ctx := context.Background()
	env := newExportTestEnv(t)
	tpl, err := pipelineTemplateForInput(CreateWorkflowRunInput{Template: templateToolTranslate})
	if err != nil {
		t.Fatal(err)
	}
	in := CreateWorkflowRunInput{
		Template: templateToolTranslate, SourceLanguage: "en", TargetLanguages: []string{"zh"},
	}
	env.svc.opts.Capabilities = HostMediaCapabilities{
		Inner: NewInstallationCapabilities(), ImageRuntime: true,
		MLSToolsPath: func() string { return "/usr/local/bin/mlstools" },
	}
	frozen, err := env.svc.freezeExecutionConfig(ctx, env.store, in, tpl)
	if err != nil {
		t.Fatalf("freeze with ready mlstools: %v", err)
	}
	cfg, ok := frozen[domain.CapabilitySlotTranslate]
	if !ok {
		t.Fatal("frozen execution missing translate slot")
	}
	if cfg.ModelLock != (domain.ReleaseRef{}) {
		t.Fatalf("translate model lock = %+v, want empty", cfg.ModelLock)
	}
	if cfg.DeviceID != "cpu" {
		t.Fatalf("translate device = %q, want cpu", cfg.DeviceID)
	}
	run := domain.WorkflowRun{Config: domain.RunConfig{Execution: frozen}}
	params, lock, err := freezeTaskParams(run, domain.NodeKeyTranslate, map[string]any{"language": "zh"})
	if err != nil {
		t.Fatal(err)
	}
	if lock != nil {
		t.Fatalf("06 model lock = %+v, want nil", lock)
	}
	if params["device_id"] != "cpu" {
		t.Fatalf("06 device = %v, want cpu", params["device_id"])
	}
	task := domain.Task{Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyTranslate, Params: params, ModelLock: lock}
	if err := env.svc.CheckTaskDispatchable(ctx, env.store, task); err != nil {
		t.Fatalf("ready 06 dispatch: %v", err)
	}

	env.svc.opts.Capabilities = HostMediaCapabilities{
		Inner: NewInstallationCapabilities(), ImageRuntime: true,
		MLSToolsPath: func() string { return "" },
	}
	if err := env.svc.CheckTaskDispatchable(ctx, env.store, task); err == nil {
		t.Fatal("06 dispatch must recheck mlstools readiness")
	}
	if _, err := env.svc.freezeExecutionConfig(ctx, env.store, in, tpl); !errors.Is(err, domain.ErrCapabilityUnverified) {
		t.Fatalf("freeze without mlstools error = %v, want capability unverified", err)
	}
}
