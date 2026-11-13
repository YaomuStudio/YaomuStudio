package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

type executionQuerier interface {
	CatalogQuerier
	GetEngineConfig(context.Context, domain.ModelSlotKey) (domain.EngineConfig, bool, error)
}

func validRelease(ref domain.ReleaseRef) bool {
	return strings.TrimSpace(ref.ReleaseID) != "" &&
		strings.TrimSpace(ref.Version) != "" && strings.TrimSpace(ref.Digest) != ""
}

// installationAdmissible reports the MVP control-plane evidence for an installed
// model slot: a completed installation with at least one verified file and an
// immutable release reference. ProbePassed is intentionally not required here
// and is never forged by Core; real probes remain a release gate.
func installationAdmissible(inst domain.Installation) bool {
	return inst.Phase == domain.InstallationPhaseDone &&
		inst.VerifiedFiles > 0 &&
		inst.Release != nil && validRelease(*inst.Release)
}

// providerSlotReady reads the capability provider's current admission for a
// slot. FFmpeg uses this path because it is a media tool without a model
// installation record; model slots are admitted by installation evidence.
func (s *Service) providerSlotReady(ctx context.Context, q CatalogQuerier, slot domain.CapabilitySlotKey) (bool, error) {
	capabilities, err := s.opts.Capabilities.Catalog(ctx, q)
	if err != nil {
		return false, err
	}
	for _, capability := range capabilities {
		if capability.Slot == slot {
			return capability.State == domain.SlotStateReady, nil
		}
	}
	return false, nil
}

func executionUnverified() error {
	return fmt.Errorf("%w: frozen execution configuration is missing or unverified", domain.ErrCapabilityUnverified)
}

// ExecutionFixture is explicit opt-in for static test providers. Production
// InstallationCapabilities has no such method and must resolve installed locks.
func (s StaticCapabilities) ExecutionFixture(slot domain.CapabilitySlotKey) (domain.ExecutionConfig, bool) {
	cfg, ok := s.ExecutionFixtures[slot]
	return cfg, ok
}

// translateToolReady reports the injected mlstools tool only; model installations
// and mock capability states are not tool evidence.
func (s *Service) translateToolReady() bool {
	provider, ok := s.opts.Capabilities.(interface{ TranslateToolReady() bool })
	return ok && provider.TranslateToolReady()
}

func (s *Service) executionFixture(slot domain.CapabilitySlotKey) (domain.ExecutionConfig, bool) {
	provider, ok := s.opts.Capabilities.(interface {
		ExecutionFixture(domain.CapabilitySlotKey) (domain.ExecutionConfig, bool)
	})
	if !ok {
		return domain.ExecutionConfig{}, false
	}
	return provider.ExecutionFixture(slot)
}

// freezeExecutionConfig never resolves a production release from a display version.
// Model slots require an installed, immutable release with verified files (or an
// explicit static test fixture). ProbePassed is not an MVP gate and is not forged.
// FFmpeg is a media-tool exception: it has no model installation record and is
// frozen by provider readiness with an empty model lock and cpu device.
func (s *Service) freezeExecutionConfig(
	ctx context.Context,
	q executionQuerier,
	in CreateWorkflowRunInput,
	tpl pipelineTemplate,
) (map[domain.CapabilitySlotKey]domain.ExecutionConfig, error) {
	extractNeeded := tpl.Has(domain.NodeKeyExtract) &&
		!(in.ReuseSubtitleTrackID != "" && !tpl.Has(domain.NodeKeyErase))
	translationNeeded := tpl.Has(domain.NodeKeyTranslate)
	if tpl.ID == templateCustomPipeline {
		translationNeeded = false
		for _, language := range in.TargetLanguages {
			if _, ok := in.ImportedTranslationTracks[language]; !ok {
				translationNeeded = tpl.Has(domain.NodeKeyTranslate)
			}
		}
		if !tpl.Has(domain.NodeKeyErase) {
			extractNeeded = extractNeeded && translationNeeded
		}
	}
	installs, err := q.ListInstallations(ctx)
	if err != nil {
		return nil, err
	}
	active := map[domain.CapabilitySlotKey]domain.Installation{}
	for _, installation := range installs {
		if installation.Phase != domain.InstallationPhaseDone {
			continue
		}
		// 冻结只看当前槽位引用，不看发起槽位或历史 covered_slots。
		for _, slot := range domain.InstallationCurrentSlots(installation) {
			if _, exists := active[slot]; !exists {
				active[slot] = installation
			}
		}
	}
	frozen := map[domain.CapabilitySlotKey]domain.ExecutionConfig{}
	for _, slot := range requiredSlots(in, tpl, extractNeeded, translationNeeded) {
		if fixture, ok := s.executionFixture(slot); ok {
			if !validRelease(fixture.ModelLock) || fixture.DeviceID == "" || fixture.DeviceID == "auto" || fixture.ContextRadius < 0 {
				return nil, executionUnverified()
			}
			copied, err := cloneExecutionConfig(fixture)
			if err != nil {
				return nil, err
			}
			frozen[slot] = copied
			continue
		}
		if slot == domain.CapabilitySlotFFmpeg {
			// FFmpeg is always a tool: provider readiness is the admission fact.
			ready, err := s.providerSlotReady(ctx, q, slot)
			if err != nil {
				return nil, err
			}
			if !ready {
				return nil, executionUnverified()
			}
			tool, err := cloneExecutionConfig(domain.ExecutionConfig{
				DeviceID: "cpu", Params: map[string]any{},
			})
			if err != nil {
				return nil, err
			}
			frozen[slot] = tool
			continue
		}
		installation, ok := active[slot]
		if slot == domain.CapabilitySlotTranslate && (!ok || !installationAdmissible(installation)) {
			// Translate may be a model installation (future local model) or the
			// mlstools tool. With no admissible installation, tool readiness is
			// the admission fact; the frozen lock stays empty and offline gate
			// marks translate as non-offline production.
			if !s.translateToolReady() {
				return nil, executionUnverified()
			}
			tool, err := cloneExecutionConfig(domain.ExecutionConfig{
				DeviceID: "cpu", Params: map[string]any{},
			})
			if err != nil {
				return nil, err
			}
			frozen[slot] = tool
			continue
		}
		if !ok || !installationAdmissible(installation) {
			return nil, executionUnverified()
		}
		cfg := domain.ExecutionConfig{ModelLock: *installation.Release, Params: map[string]any{}}
		if modelSlot := domain.ModelSlotKey(slot); modelSlot.Valid() {
			engine, _, err := q.GetEngineConfig(ctx, modelSlot)
			if err != nil {
				return nil, err
			}
			if err := validateEngineValues(modelSlot, engine.Values); err != nil {
				return nil, err
			}
			cfg.DeviceID = engine.DeviceID
			for key, value := range engine.Values {
				cfg.Params[key] = value
			}
		}
		cfg.DeviceID, err = s.freezeDevice(ctx, cfg.DeviceID, slot)
		if err != nil {
			return nil, err
		}
		frozen[slot], err = cloneExecutionConfig(cfg)
		if err != nil {
			return nil, err
		}
	}
	return frozen, nil
}

func cloneExecutionConfig(cfg domain.ExecutionConfig) (domain.ExecutionConfig, error) {
	if cfg.Params == nil {
		cfg.Params = map[string]any{}
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return domain.ExecutionConfig{}, fmt.Errorf("%w: encode execution configuration: %v", domain.ErrInvalidRequest, err)
	}
	var result domain.ExecutionConfig
	if err := json.Unmarshal(encoded, &result); err != nil {
		return domain.ExecutionConfig{}, fmt.Errorf("decode execution configuration: %w", err)
	}
	return result, nil
}

func (s *Service) freezeDevice(ctx context.Context, configured string, slot domain.CapabilitySlotKey) (string, error) {
	if slot == domain.CapabilitySlotFFmpeg {
		return "cpu", nil
	}
	devices, err := s.opts.Devices.List(ctx)
	if err != nil {
		return "", err
	}
	if configured != "" && configured != "auto" {
		for _, device := range devices {
			if device.ID == configured {
				return configured, nil
			}
		}
		return "", fmt.Errorf("%w: configured execution device is unavailable", domain.ErrRuntimeIncompatible)
	}
	for _, device := range devices {
		if device.Recommended {
			return device.ID, nil
		}
	}
	for _, device := range devices {
		if device.Kind == domain.DeviceKindCPU {
			return device.ID, nil
		}
	}
	return "", fmt.Errorf("%w: no execution device is available", domain.ErrRuntimeIncompatible)
}

func executionSlot(run domain.WorkflowRun, node domain.PipelineNodeKey) domain.CapabilitySlotKey {
	if node == domain.NodeKeyAnalyze || node == domain.NodeKeyExtract {
		if run.Config.Extract != nil && run.Config.Extract.Mode == domain.ExtractModeOCR {
			return domain.CapabilitySlotOCR
		}
	}
	slot, _ := nodeSlotKey(node)
	return slot
}

// toolCapabilitySlot reports tool capabilities (FFmpeg always; translate when
// it has no model lock). Tool capabilities have no model installation record
// and freeze an empty model lock with cpu.
func toolCapabilitySlot(slot domain.CapabilitySlotKey) bool {
	return slot == domain.CapabilitySlotFFmpeg || slot == domain.CapabilitySlotTranslate
}

// toolExecutionLock distinguishes the tool form (empty lock) from a model form
// (valid installation lock) for slots that support both.
func toolExecutionLock(slot domain.CapabilitySlotKey, lock domain.ReleaseRef) bool {
	if slot == domain.CapabilitySlotFFmpeg {
		return true
	}
	return slot == domain.CapabilitySlotTranslate && lock == (domain.ReleaseRef{})
}

// frozenExecution refuses to silently upgrade a legacy Run to the current installation.
// Tool capabilities are the only slots that may carry an empty model lock; device stays cpu.
func frozenExecution(run domain.WorkflowRun, node domain.PipelineNodeKey) (domain.ExecutionConfig, error) {
	slot := executionSlot(run, node)
	cfg, ok := run.Config.Execution[slot]
	if !ok || cfg.DeviceID == "" || cfg.DeviceID == "auto" || cfg.ContextRadius < 0 {
		return domain.ExecutionConfig{}, executionUnverified()
	}
	if toolExecutionLock(slot, cfg.ModelLock) {
		// Old Runs with a valid frozen tool lock stay admissible and keep that
		// release; new Runs have an empty lock. Partially filled locks are invalid.
		if cfg.DeviceID != "cpu" || (cfg.ModelLock != (domain.ReleaseRef{}) && !validRelease(cfg.ModelLock)) {
			return domain.ExecutionConfig{}, executionUnverified()
		}
	} else if !validRelease(cfg.ModelLock) {
		return domain.ExecutionConfig{}, executionUnverified()
	}
	return cloneExecutionConfig(cfg)
}

func coreTaskParams(params map[string]any) map[string]any {
	core, ok := params["_core"].(map[string]any)
	if !ok {
		core = map[string]any{}
		params["_core"] = core
	}
	return core
}

func readCoreTaskParams(params map[string]any) map[string]any {
	core, _ := params["_core"].(map[string]any)
	return core
}

func freezeTaskParams(run domain.WorkflowRun, node domain.PipelineNodeKey, params map[string]any) (map[string]any, *domain.ReleaseRef, error) {
	cfg, err := frozenExecution(run, node)
	if err != nil {
		return nil, nil, err
	}
	frozen := map[string]any{}
	for key, value := range cfg.Params {
		frozen[key] = value
	}
	for key, value := range params {
		frozen[key] = value
	}
	frozen["device_id"] = cfg.DeviceID
	core := coreTaskParams(frozen)
	core["execution_slot"] = string(executionSlot(run, node))
	core["execution_config"] = cfg
	core["execution_dependencies"] = run.Config.Execution
	// Normalize nested structs into JSON objects before hashing. Persistence decodes
	// them as maps; using that same representation keeps retry digests stable.
	encoded, err := json.Marshal(frozen)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: encode frozen task parameters: %v", domain.ErrInvalidRequest, err)
	}
	if err := json.Unmarshal(encoded, &frozen); err != nil {
		return nil, nil, fmt.Errorf("decode frozen task parameters: %w", err)
	}
	lock := cfg.ModelLock
	if toolExecutionLock(executionSlot(run, node), lock) && !validRelease(lock) {
		// New production Runs freeze an empty tool lock; old valid locks remain.
		return frozen, nil, nil
	}
	return frozen, &lock, nil
}

func taskExecutionSlot(task domain.Task) domain.CapabilitySlotKey {
	if core, ok := task.Params["_core"].(map[string]any); ok {
		if raw, ok := core["execution_slot"].(string); ok {
			return domain.CapabilitySlotKey(raw)
		}
	}
	if task.Kind == domain.TaskKindSegment {
		// 新句级任务由 freezeTaskParams 冻结 voice-clone 执行槽；旧任务没有
		// 冻结槽时保持升级前的 TTS 回退，避免用 voice-clone 重检其 TTS 冻结依赖。
		return domain.CapabilitySlotTTS
	}
	slot, _ := nodeSlotKey(task.NodeKey)
	return slot
}

func (s *Service) checkTaskExecutionLock(ctx context.Context, q CatalogQuerier, task domain.Task) error {
	slot := taskExecutionSlot(task)
	if !slot.Valid() {
		return executionUnverified()
	}
	lock := domain.ReleaseRef{}
	if task.ModelLock != nil {
		// A non-nil lock must be complete; for FFmpeg this means only nil or a
		// full legacy lock is usable. Never normalize a partial lock into nil.
		if !validRelease(*task.ModelLock) {
			return executionUnverified()
		}
		lock = *task.ModelLock
	} else if !toolCapabilitySlot(slot) {
		// Model tasks must carry the frozen immutable release. Tool capabilities
		// intentionally have no model lock.
		return executionUnverified()
	}
	dependencies := map[domain.CapabilitySlotKey]domain.ExecutionConfig{
		slot: {ModelLock: lock},
	}
	frozenDependencies := false
	if core, ok := task.Params["_core"].(map[string]any); ok {
		if raw, exists := core["execution_dependencies"]; exists {
			encoded, err := json.Marshal(raw)
			if err != nil {
				return executionUnverified()
			}
			if err := json.Unmarshal(encoded, &dependencies); err != nil {
				return executionUnverified()
			}
			cfg, exists := dependencies[slot]
			if !exists || cfg.ModelLock != lock || cfg.DeviceID != task.Params["device_id"] {
				return executionUnverified()
			}
			for dependencySlot, cfg := range dependencies {
				if cfg.DeviceID == "" || cfg.DeviceID == "auto" || cfg.ContextRadius < 0 {
					return executionUnverified()
				}
				if toolExecutionLock(dependencySlot, cfg.ModelLock) && cfg.DeviceID != "cpu" {
					return executionUnverified()
				}
			}
			frozenDependencies = true
		}
	}
	if toolExecutionLock(slot, lock) && !validRelease(lock) && !frozenDependencies {
		// A lockless tool task must carry frozen dependency metadata; without it
		// there is no frozen device/dependency configuration to verify.
		return executionUnverified()
	}
	installations, err := q.ListInstallations(ctx)
	if err != nil {
		return err
	}
	// An older installed release remains admissible: changing the active version
	// must not substitute its lock for the immutable version frozen in this task.
	for dependencySlot, cfg := range dependencies {
		if !dependencySlot.Valid() {
			return executionUnverified()
		}
		if toolExecutionLock(dependencySlot, cfg.ModelLock) {
			// Tool capabilities are admitted by current provider readiness, not by a
			// model installation record. Empty locks are the new form; legacy valid
			// locks stay untouched but still require the tool to be ready.
			if cfg.ModelLock != (domain.ReleaseRef{}) && !validRelease(cfg.ModelLock) {
				return executionUnverified()
			}
			if dependencySlot == domain.CapabilitySlotTranslate {
				if !s.translateToolReady() {
					return executionUnverified()
				}
				continue
			}
			ready, err := s.providerSlotReady(ctx, q, dependencySlot)
			if err != nil {
				return err
			}
			if !ready {
				return executionUnverified()
			}
			continue
		}
		if !validRelease(cfg.ModelLock) {
			return executionUnverified()
		}
		if fixture, ok := s.executionFixture(dependencySlot); ok && fixture.ModelLock == cfg.ModelLock {
			continue
		}
		admitted := false
		for _, installation := range installations {
			// 派发按仍保留的槽位引用 + 精确完整锁；不能用历史覆盖范围复活已解绑槽位。
			if domain.InstallationBoundSlot(installation, dependencySlot) && installationAdmissible(installation) &&
				*installation.Release == cfg.ModelLock {
				admitted = true
				break
			}
		}
		if !admitted {
			return executionUnverified()
		}
	}
	return nil
}
