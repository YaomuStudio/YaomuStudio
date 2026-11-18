package service

// 能力目录、受控参数 Schema 与设备清单。
//
// 能力可见性由发布准入决定，可运行性由本机安装状态决定；两者都不在这里
// 猜测 FlashCLI 参数。受控参数 Schema 只暴露已确认的产品枚举，空表示该槽位
// 当前没有额外受控参数（EngineConfig 只接受 Schema 内的键）。

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// CatalogQuerier 是能力目录读取安装状态所需的仓储子集；*store.Store 与
// *store.Tx 都实现它，保证事务内评估不会另开连接（单连接 SQLite）。
type CatalogQuerier interface {
	ListInstallations(ctx context.Context) ([]domain.Installation, error)
}

// CapabilityProvider 返回逐槽位能力目录。
//
// 实现必须只通过 q 读取持久化状态：调用方可能在写事务内调用（单连接 SQLite，
// 另开连接会死锁）。
type CapabilityProvider interface {
	Catalog(ctx context.Context, q CatalogQuerier) ([]domain.Capability, error)
}

// EngineCatalog 返回槽位准入的本地引擎目录；能力目录再叠加本机安装/选中状态。
type EngineCatalog interface {
	List(ctx context.Context, slot domain.CapabilitySlotKey) ([]ReleasePlan, error)
}

// capabilitySlots 是稳定的目录顺序（六模型 + OCR + FFmpeg）。
var capabilitySlots = []domain.CapabilitySlotKey{
	domain.CapabilitySlotASR,
	domain.CapabilitySlotTranslate,
	domain.CapabilitySlotTTS,
	domain.CapabilitySlotVoiceClone,
	domain.CapabilitySlotSubtitleErase,
	domain.CapabilitySlotSeparation,
	domain.CapabilitySlotOCR,
	domain.CapabilitySlotFFmpeg,
}

// paramCatalog 是受控参数 Schema；当前只登记已确认的产品枚举，
// 不把未经确认的模型/执行参数暴露给 UI。
func paramCatalog(slot domain.CapabilitySlotKey) []domain.ParamField {
	switch slot {
	case domain.CapabilitySlotSubtitleErase:
		return []domain.ParamField{{
			Key:      "erase_mode",
			LabelKey: "model.param.erase_mode",
			Type:     domain.ParamFieldTypeEnum,
			Options: []domain.ParamOption{
				{Value: string(domain.EraseModeDialogOnly), LabelKey: "erase.mode.dialog_only"},
				{Value: string(domain.EraseModeDialogAndArt), LabelKey: "erase.mode.dialog_and_art"},
			},
			Default: string(domain.EraseModeDialogOnly),
		}}
	case domain.CapabilitySlotOCR:
		return []domain.ParamField{{
			Key:      "ocr_language",
			LabelKey: "model.param.ocr_language",
			Type:     domain.ParamFieldTypeEnum,
			Options: []domain.ParamOption{
				{Value: string(domain.OCRLanguageChinese), LabelKey: "ocr.lang.chinese"},
				{Value: string(domain.OCRLanguageChineseTraditional), LabelKey: "ocr.lang.chinese_traditional"},
				{Value: string(domain.OCRLanguageEnglish), LabelKey: "ocr.lang.english"},
				{Value: string(domain.OCRLanguageJapanese), LabelKey: "ocr.lang.japanese"},
			},
			Default: string(domain.OCRLanguageChinese),
		}}
	default:
		return nil
	}
}

// InstallationCapabilities 由安装记录推导能力状态（生产默认实现）。
//
// MediaToolsReady 表示宿主已注入可执行的 FFmpeg/ffprobe（FFMPEG_BIN /
// FFPROBE_BIN，或桌面壳随包注入的同名绝对路径）。FFmpeg 不是模型包，没有
// 安装记录；只按实际可执行性把 ffmpeg 槽位派生为 READY，不伪造安装记录，
// 也不改变任何模型槽位。
type InstallationCapabilities struct {
	MediaToolsReady bool
	Engines         EngineCatalog
}

// NewInstallationCapabilities 返回仅由安装记录推导的能力目录（默认实现）。
func NewInstallationCapabilities() InstallationCapabilities { return InstallationCapabilities{} }

// NewInstallationCapabilitiesWithMediaTools 在安装记录基础上叠加宿主媒体工具
// 可用性；只有 ffmpeg 槽位会因此变为 READY。
func NewInstallationCapabilitiesWithMediaTools(mediaToolsReady bool) InstallationCapabilities {
	return InstallationCapabilities{MediaToolsReady: mediaToolsReady}
}

// WithEngines 注入 Core 侧准入引擎目录；空目录时只回退到当前安装版本。
func (s InstallationCapabilities) WithEngines(engines EngineCatalog) InstallationCapabilities {
	s.Engines = engines
	return s
}

// MediaToolsReady 报告宿主注入的媒体工具是否可执行。
//
// Runner 生产路径拒绝从 PATH 解析任意版本，因此这里只认显式绝对路径：
// ffmpeg 必须可执行；ffprobe 配置了就必须可执行（探测/媒体属性用），
// 未配置时仍允许转码/混音（Runner 会省略媒体属性，不伪造）。
func MediaToolsReady(ffmpegPath, ffprobePath string) bool {
	if !executableFile(ffmpegPath) {
		return false
	}
	if ffprobePath != "" && !executableFile(ffprobePath) {
		return false
	}
	return true
}

// executableFile 报告 path 是非空、绝对路径、存在的普通可执行文件。
func executableFile(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return false
	}
	return true
}

// slotInstallationState 汇总一个槽位的安装记录（列表最新在前）：
// latest 是最近一条任意阶段记录，active 是最近一条 done 记录（槽位当前可用版本）。
type slotInstallationState struct {
	latest    domain.Installation
	hasLatest bool
	active    domain.Installation
	hasActive bool
}

// current 返回用于展示槽位状态与版本的记录。
//
// 失败/取消的升级不激活新版本，也不能遮蔽既有可用版本（FR-MODEL-02：
// 取消安装保留旧模型与有效缓存）：有 done 记录时槽位仍以该版本 READY，
// 失败记录只体现在安装列表。进行中的安装仍展示其阶段与进度。
func (s slotInstallationState) current() domain.Installation {
	if s.hasLatest && installationInProgress(s.latest.Phase) {
		return s.latest
	}
	if s.hasActive {
		return s.active
	}
	return s.latest
}

func installationInProgress(phase domain.InstallationPhase) bool {
	switch phase {
	case domain.InstallationPhasePlanning, domain.InstallationPhaseDownloading,
		domain.InstallationPhaseVerifying, domain.InstallationPhaseInstalling:
		return true
	default:
		return false
	}
}

// Catalog 实现 CapabilityProvider。
//
// 槽位当前/进行中状态由安装实体的显式引用投影决定：同一物理安装覆盖多个
// 槽位时共享同一阶段/进度；空引用数组绝不回退发起槽位（已解绑槽位不复活）。
func (s InstallationCapabilities) Catalog(ctx context.Context, q CatalogQuerier) ([]domain.Capability, error) {
	installs, err := q.ListInstallations(ctx)
	if err != nil {
		return nil, err
	}
	bySlot := make(map[domain.CapabilitySlotKey]slotInstallationState, len(capabilitySlots))
	for _, inst := range installs { // 最新在前：首个引用即 latest，首个当前 Done 即当前版本。
		for _, slot := range domain.InstallationBoundSlots(inst) {
			state := bySlot[slot]
			if !state.hasLatest {
				state.latest = inst
				state.hasLatest = true
			}
			bySlot[slot] = state
		}
		for _, slot := range domain.InstallationCurrentSlots(inst) {
			state := bySlot[slot]
			if !state.hasActive {
				state.active = inst
				state.hasActive = true
			}
			bySlot[slot] = state
		}
	}
	doneReleases := make(map[domain.CapabilitySlotKey]map[string]struct{})
	for _, inst := range installs {
		if inst.Phase != domain.InstallationPhaseDone || inst.Release == nil {
			continue
		}
		for _, slot := range domain.InstallationBoundSlots(inst) {
			if doneReleases[slot] == nil {
				doneReleases[slot] = make(map[string]struct{})
			}
			doneReleases[slot][inst.Release.ReleaseID] = struct{}{}
		}
	}
	out := make([]domain.Capability, 0, len(capabilitySlots))
	for _, slot := range capabilitySlots {
		capability := domain.Capability{Slot: slot, State: domain.SlotStateNeedsSetup, Params: paramCatalog(slot)}
		state, ok := bySlot[slot]
		capability.Engines = s.enginesFor(ctx, slot, state, doneReleases[slot])
		if !ok {
			// FFmpeg 是宿主固定版本工具而非模型包：没有安装记录时按实际
			// 可执行性派生，避免 08/预检被 NEEDS_SETUP 永久阻断。
			if slot == domain.CapabilitySlotFFmpeg && s.MediaToolsReady {
				capability.State = domain.SlotStateReady
				out = append(out, capability)
				continue
			}
			capability.ReasonCode = string(domain.ErrorCodeModelNotInstalled)
			capability.ActionIDs = []domain.ID{"open_model_setup"}
			out = append(out, capability)
			continue
		}
		inst := state.current()
		if inst.Release != nil {
			capability.EngineName = inst.Release.ReleaseID
			capability.Version = inst.Release.Version
		}
		progress := inst.Progress
		capability.Progress = &progress
		switch inst.Phase {
		case domain.InstallationPhaseDone:
			capability.State = domain.SlotStateReady
			capability.Progress = nil
		case domain.InstallationPhaseVerifying:
			capability.State = domain.SlotStateVerifying
		case domain.InstallationPhasePlanning, domain.InstallationPhaseDownloading, domain.InstallationPhaseInstalling:
			capability.State = domain.SlotStateInstalling
		case domain.InstallationPhaseFailed:
			capability.State = domain.SlotStateFailed
			capability.ReasonCode = string(domain.ErrorCodeCapabilityUnverified)
			capability.ActionIDs = []domain.ID{"retry_installation"}
			capability.Progress = nil
		case domain.InstallationPhaseCanceled:
			capability.State = domain.SlotStateNeedsSetup
			capability.ReasonCode = string(domain.ErrorCodeModelNotInstalled)
			capability.ActionIDs = []domain.ID{"open_model_setup"}
			capability.Progress = nil
		}
		out = append(out, capability)
	}
	return out, nil
}

func (s InstallationCapabilities) enginesFor(ctx context.Context, slot domain.CapabilitySlotKey, state slotInstallationState, done map[string]struct{}) []domain.EngineOption {
	selectedID := ""
	if state.hasActive && state.active.Release != nil {
		selectedID = state.active.Release.ReleaseID
	}
	plans := []ReleasePlan(nil)
	if s.Engines != nil {
		if listed, err := s.Engines.List(ctx, slot); err == nil {
			plans = listed
		}
	}
	out := make([]domain.EngineOption, 0, len(plans)+1)
	seen := make(map[string]struct{}, len(plans)+1)
	for _, plan := range plans {
		id := plan.Release.ReleaseID
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		size := plan.InstallBytes
		if size <= 0 {
			size = plan.DownloadBytes
		}
		option := domain.EngineOption{
			ID:        id,
			Name:      id,
			Version:   plan.Release.Version,
			State:     domain.SlotStateNeedsSetup,
			Installed: false,
			Selected:  id == selectedID,
			Source:    plan.Release.Channel,
			SizeBytes: size,
		}
		if done != nil {
			if _, installed := done[id]; installed {
				option.Installed = true
				option.State = domain.SlotStateReady
			}
		}
		out = append(out, option)
	}
	if state.hasActive && state.active.Release != nil {
		id := state.active.Release.ReleaseID
		if _, exists := seen[id]; id != "" && !exists {
			out = append(out, domain.EngineOption{
				ID:        id,
				Name:      id,
				Version:   state.active.Release.Version,
				State:     domain.SlotStateReady,
				Installed: true,
				Selected:  true,
			})
		}
	}
	return out
}

// MockModelCapabilities 在未安装正式包时，把指定槽位标为 mock READY。
//
// 仅覆盖仍为 NEEDS_SETUP 的槽；已有 done 安装记录的 READY/进度态不覆盖。
// 由 YAOMU_MOCK_MODEL_SLOTS（逗号分隔，如 separation,voice-clone）启用，
// 供 WSL Runner mock 适配器联调，不冒充已签名模型准入。
type MockModelCapabilities struct {
	Inner CapabilityProvider
	Slots []domain.CapabilitySlotKey
}

// Catalog 实现 CapabilityProvider。
func (m MockModelCapabilities) Catalog(ctx context.Context, q CatalogQuerier) ([]domain.Capability, error) {
	inner := m.Inner
	if inner == nil {
		inner = NewInstallationCapabilities()
	}
	caps, err := inner.Catalog(ctx, q)
	if err != nil {
		return nil, err
	}
	if len(m.Slots) == 0 {
		return caps, nil
	}
	want := make(map[domain.CapabilitySlotKey]struct{}, len(m.Slots))
	for _, slot := range m.Slots {
		want[slot] = struct{}{}
	}
	out := make([]domain.Capability, len(caps))
	copy(out, caps)
	for i := range out {
		if _, ok := want[out[i].Slot]; !ok {
			continue
		}
		if out[i].State != domain.SlotStateNeedsSetup {
			continue
		}
		out[i].State = domain.SlotStateReady
		out[i].EngineName = "runner-mock"
		out[i].Version = "mock"
		out[i].ReasonCode = ""
		out[i].ActionIDs = nil
		out[i].Progress = nil
	}
	return out, nil
}

// TranslateToolReady 透传内层工具能力；mock READY 不构成工具就绪证据。
func (m MockModelCapabilities) TranslateToolReady() bool {
	if provider, ok := m.Inner.(interface{ TranslateToolReady() bool }); ok {
		return provider.TranslateToolReady()
	}
	return false
}

// ParseMockModelSlots 解析 YAOMU_MOCK_MODEL_SLOTS。
func ParseMockModelSlots(raw string) []domain.CapabilitySlotKey {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]domain.CapabilitySlotKey, 0, len(parts))
	seen := make(map[domain.CapabilitySlotKey]struct{}, len(parts))
	for _, part := range parts {
		slot := domain.CapabilitySlotKey(strings.TrimSpace(part))
		if !slot.Valid() {
			continue
		}
		if _, ok := seen[slot]; ok {
			continue
		}
		seen[slot] = struct{}{}
		out = append(out, slot)
	}
	return out
}

// HostMediaCapabilities 覆盖 `ffmpeg` 槽位状态。
//
// FFmpeg 不走模型安装记录，也不看 WSL 镜像自检。桌面壳把随包 ffmpeg 的绝对
// 路径注入 FFMPEG_BIN；这里只检查该文件存在，不启子进程，也不为此启动 WSL。
type HostMediaCapabilities struct {
	Inner CapabilityProvider
	// FFmpegPath 默认读 FFMPEG_BIN；测试可注入。
	FFmpegPath func() string
	// MLSToolsPath 默认读 YAOMU_WSL_MLSTOOLS_BIN；translate 是工具能力，
	// 由桌面壳探测镜像路径后注入，不要求模型安装记录。
	MLSToolsPath func() string
	// Platform 默认 runtime.GOOS；Windows 不检查可执行位。
	Platform string
}

// Catalog 实现 CapabilityProvider。
func (h HostMediaCapabilities) Catalog(ctx context.Context, q CatalogQuerier) ([]domain.Capability, error) {
	inner := h.Inner
	if inner == nil {
		inner = NewInstallationCapabilities()
	}
	caps, err := inner.Catalog(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Capability, len(caps))
	copy(out, caps)
	ffmpeg := h.ffmpegCapability()
	ffmpegApplied := false
	translateIndex := -1
	for i := range out {
		switch out[i].Slot {
		case domain.CapabilitySlotFFmpeg:
			out[i] = ffmpeg
			ffmpegApplied = true
		case domain.CapabilitySlotTranslate:
			translateIndex = i
		}
	}
	if !ffmpegApplied {
		out = append(out, ffmpeg)
	}
	// 工具注入优先于未安装状态；但已有 READY 的模型安装不被空工具路径覆盖。
	translate := h.translateCapability()
	translateReady := translate.State == domain.SlotStateReady
	switch {
	case translateIndex >= 0:
		if translateReady || out[translateIndex].State != domain.SlotStateReady {
			out[translateIndex] = translate
		}
	case translateReady:
		out = append(out, translate)
	default:
		out = append(out, translate)
	}
	return out, nil
}

func (h HostMediaCapabilities) ffmpegCapability() domain.Capability {
	cap := domain.Capability{
		Slot:   domain.CapabilitySlotFFmpeg,
		Params: paramCatalog(domain.CapabilitySlotFFmpeg),
	}
	pathFn := h.FFmpegPath
	if pathFn == nil {
		pathFn = func() string { return os.Getenv("FFMPEG_BIN") }
	}
	platform := h.Platform
	if platform == "" {
		platform = runtime.GOOS
	}
	path := strings.TrimSpace(pathFn())
	if path == "" {
		cap.State = domain.SlotStateNeedsSetup
		cap.ReasonCode = string(domain.ErrorCodeCapabilityUnverified)
		return cap
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		cap.State = domain.SlotStateNeedsSetup
		cap.ReasonCode = string(domain.ErrorCodeCapabilityUnverified)
		return cap
	}
	if platform != "windows" && info.Mode().Perm()&0o111 == 0 {
		cap.State = domain.SlotStateNeedsSetup
		cap.ReasonCode = string(domain.ErrorCodeCapabilityUnverified)
		return cap
	}
	cap.State = domain.SlotStateReady
	cap.EngineName = "bundled-ffmpeg"
	return cap
}

// translateCapability 是工具能力例外：translate 走镜像内 mlstools，不占
// 模型安装记录。只接受显式注入的镜像内绝对路径；未注入或非法时不伪造 READY。
// TranslateToolReady 只表示镜像内 mlstools 已被显式探测注入；不把模型安装
// 或 mock 能力目录当成工具就绪。
func (h HostMediaCapabilities) TranslateToolReady() bool {
	return h.translateCapability().State == domain.SlotStateReady
}

func (h HostMediaCapabilities) translateCapability() domain.Capability {
	cap := domain.Capability{
		Slot:   domain.CapabilitySlotTranslate,
		Params: paramCatalog(domain.CapabilitySlotTranslate),
	}
	pathFn := h.MLSToolsPath
	if pathFn == nil {
		pathFn = func() string { return os.Getenv("YAOMU_WSL_MLSTOOLS_BIN") }
	}
	path := strings.TrimSpace(pathFn())
	if path == "" || !strings.HasPrefix(path, "/") || len(path) > 4096 || hasControlChars(path) {
		cap.State = domain.SlotStateNeedsSetup
		cap.ReasonCode = string(domain.ErrorCodeCapabilityUnverified)
		cap.ActionIDs = []domain.ID{"open_model_setup"}
		return cap
	}
	cap.State = domain.SlotStateReady
	cap.EngineName = "mlstools"
	return cap
}

func hasControlChars(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// StaticCapabilities 是测试/受限环境使用的固定能力目录。
type StaticCapabilities struct {
	States map[domain.CapabilitySlotKey]domain.SlotState
	// ExecutionFixtures 是测试显式提供的不可变执行配置；空值不伪造生产锁。
	ExecutionFixtures map[domain.CapabilitySlotKey]domain.ExecutionConfig
}

// Catalog 实现 CapabilityProvider；未列出的槽位视为 READY。
func (s StaticCapabilities) Catalog(_ context.Context, _ CatalogQuerier) ([]domain.Capability, error) {
	out := make([]domain.Capability, 0, len(capabilitySlots))
	for _, slot := range capabilitySlots {
		state, ok := s.States[slot]
		if !ok {
			state = domain.SlotStateReady
		}
		capability := domain.Capability{Slot: slot, State: state, Params: paramCatalog(slot)}
		if state != domain.SlotStateReady {
			capability.ReasonCode = string(domain.ErrorCodeModelNotInstalled)
			capability.ActionIDs = []domain.ID{"open_model_setup"}
		}
		out = append(out, capability)
	}
	return out, nil
}

// ListCapabilities 返回能力目录，并标注各槽位当前的执行占用状态。
func (s *Service) ListCapabilities(ctx context.Context) ([]domain.Capability, error) {
	caps, err := s.opts.Capabilities.Catalog(ctx, s.store)
	if err != nil {
		return nil, err
	}
	if caps == nil {
		caps = []domain.Capability{}
	}
	if err := s.applyFlashCLIDownloadEvidence(ctx, caps); err != nil {
		return nil, err
	}
	if err := s.annotateCapabilityRuntime(ctx, caps); err != nil {
		return nil, err
	}
	return caps, nil
}

// annotateCapabilityRuntime 按活跃任务推导槽位 idle/running。
// 这里只表达执行占用，不声称模型权重常驻；无法推导槽位的任务忽略。
func (s *Service) annotateCapabilityRuntime(ctx context.Context, caps []domain.Capability) error {
	running, err := s.store.ListRunningTasks(ctx)
	if err != nil {
		return err
	}
	counts := make(map[domain.CapabilitySlotKey]int)
	for _, task := range running {
		slot := taskExecutionSlot(task)
		if slot.Valid() {
			counts[slot]++
		}
	}
	for i := range caps {
		caps[i].RuntimeState = domain.CapabilityRuntimeIdle
		caps[i].RunningTasks = 0
		if count := counts[caps[i].Slot]; count > 0 {
			caps[i].RuntimeState = domain.CapabilityRuntimeRunning
			caps[i].RunningTasks = count
		}
	}
	return nil
}

// ---- 设备 ----

// DeviceProvider 返回可用计算设备（FR-SET-40）。
//
// 实现必须是纯探测/内存计算：UpdateEngineConfig 会在写事务内调用它，
// 不允许再访问 store（单连接 SQLite 会死锁）。
type DeviceProvider interface {
	List(ctx context.Context) ([]domain.DeviceInfo, error)
}

// SystemDevices 是本机设备探测：CPU 总是可用；darwin/arm64 追加 MPS。
// 不做未经确认的 CUDA 探测命令；未检测到的设备不会伪造。
type SystemDevices struct{}

// List 实现 DeviceProvider。
func (SystemDevices) List(_ context.Context) ([]domain.DeviceInfo, error) {
	devices := []domain.DeviceInfo{{
		ID:    "cpu",
		Name:  "CPU",
		Kind:  domain.DeviceKindCPU,
		InUse: false,
	}}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		devices = append(devices, domain.DeviceInfo{
			ID:          "mps",
			Name:        "Apple GPU (MPS)",
			Kind:        domain.DeviceKindMPS,
			Recommended: true,
		})
	} else {
		devices[0].Recommended = true
	}
	return devices, nil
}

// ListDevices 返回设备清单，并标注当前生效/占用状态。
func (s *Service) ListDevices(ctx context.Context) ([]domain.DeviceInfo, error) {
	// 读路径允许刷新缓存；lookupDevice 在写事务内只读缓存，不能触发外部命令。
	if refresher, ok := s.opts.Devices.(DeviceRefresher); ok {
		refresher.Refresh(ctx)
	}
	devices, err := s.opts.Devices.List(ctx)
	if err != nil {
		return nil, err
	}
	return s.annotateDevices(ctx, devices)
}

// annotateDevices 标注 selected/in_use；只读 store，不触发设备探测。
func (s *Service) annotateDevices(ctx context.Context, devices []domain.DeviceInfo) ([]domain.DeviceInfo, error) {
	explicit := make(map[string]bool)
	for _, slot := range []domain.ModelSlotKey{
		domain.ModelSlotASR, domain.ModelSlotTranslate, domain.ModelSlotTTS,
		domain.ModelSlotVoiceClone, domain.ModelSlotSubtitleErase, domain.ModelSlotSeparation,
	} {
		cfg, found, err := s.store.GetEngineConfig(ctx, slot)
		if err != nil {
			return nil, err
		}
		if found && cfg.DeviceID != "" && cfg.DeviceID != "auto" {
			explicit[cfg.DeviceID] = true
		}
	}
	running, err := s.store.CountRunningTasks(ctx)
	if err != nil {
		return nil, err
	}
	for i := range devices {
		if explicit[devices[i].ID] {
			devices[i].Selected = true
		} else if len(explicit) == 0 && devices[i].Recommended {
			devices[i].Selected = true
		}
		devices[i].InUse = running > 0 && devices[i].Selected
	}
	if devices == nil {
		devices = []domain.DeviceInfo{}
	}
	return devices, nil
}

// lookupDevice 校验设备 ID：auto 或已探测到的设备。
func (s *Service) lookupDevice(ctx context.Context, id string) (bool, error) {
	if id == "" || id == "auto" {
		return true, nil
	}
	devices, err := s.opts.Devices.List(ctx)
	if err != nil {
		return false, err
	}
	for _, device := range devices {
		if device.ID == id {
			return true, nil
		}
	}
	return false, nil
}

// validateEngineValues 校验受控参数 Schema 内的键与范围。
func validateEngineValues(slot domain.ModelSlotKey, values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	fields := map[string]domain.ParamField{}
	for _, field := range paramCatalog(domain.CapabilitySlotKey(slot)) {
		fields[field.Key] = field
	}
	for key, value := range values {
		field, ok := fields[key]
		if !ok {
			return invalidRequestf("engine config key %q is not in the controlled schema for slot %s", key, slot)
		}
		switch field.Type {
		case domain.ParamFieldTypeEnum:
			text, ok := value.(string)
			if !ok {
				return invalidRequestf("engine config %q must be a string enum", key)
			}
			valid := false
			for _, option := range field.Options {
				if option.Value == text {
					valid = true
					break
				}
			}
			if !valid {
				return invalidRequestf("engine config %q value %q is not allowed", key, text)
			}
		case domain.ParamFieldTypeBoolean:
			if _, ok := value.(bool); !ok {
				return invalidRequestf("engine config %q must be boolean", key)
			}
		case domain.ParamFieldTypeNumber:
			if _, ok := value.(float64); !ok {
				return invalidRequestf("engine config %q must be number", key)
			}
		case domain.ParamFieldTypeString:
			if _, ok := value.(string); !ok {
				return invalidRequestf("engine config %q must be string", key)
			}
		}
	}
	return nil
}

// modelSlotOf 把契约路径参数转换为模型槽位；非模型槽位返回 false。
func modelSlotOf(value string) (domain.ModelSlotKey, bool) {
	slot := domain.ModelSlotKey(value)
	return slot, slot.Valid()
}

// capabilitySlotOf 把契约路径参数转换为能力槽位。
func capabilitySlotOf(value string) (domain.CapabilitySlotKey, bool) {
	slot := domain.CapabilitySlotKey(value)
	return slot, slot.Valid()
}

// CheckTaskDispatchable 在派发前重检动态条件（能力就绪状态）。
// 返回错误时调度层把任务转入 WAITING_RESOURCE，等待下一轮重检，
// 不派发执行器（docs/local-api-contract.md §3）。
func (s *Service) CheckTaskDispatchable(ctx context.Context, q CatalogQuerier, task domain.Task) error {
	if task.Kind != domain.TaskKindExport {
		if err := s.checkTaskExecutionLock(ctx, q, task); err != nil {
			return err
		}
	}
	slot := domain.CapabilitySlotFFmpeg
	switch task.Kind {
	case domain.TaskKindNode:
		if mapped, ok := nodeSlotKey(task.NodeKey); ok {
			slot = mapped
			if frozenSlot := taskExecutionSlot(task); frozenSlot.Valid() {
				slot = frozenSlot
			}
		}
	case domain.TaskKindSegment:
		// 句级任务按冻结执行槽重检；克隆句段应校验 voice-clone，
		// 仅旧任务缺少冻结信息时回退 TTS。
		slot = domain.CapabilitySlotTTS
		if frozenSlot := taskExecutionSlot(task); frozenSlot.Valid() {
			slot = frozenSlot
		}
	case domain.TaskKindExport:
		// 导出不依赖模型安装记录：FFmpeg 是宿主注入的固定版本工具，
		// 缺失时由 Runner 在执行期返回 MEDIA_TOOL_UNAVAILABLE 并映射为
		// 可读失败（PRD 失败路径），而不是无限等待安装记录。
		return nil
	}
	capabilities, err := s.opts.Capabilities.Catalog(ctx, q)
	if err != nil {
		return err
	}
	for _, capability := range capabilities {
		if capability.Slot != slot {
			continue
		}
		if capability.State == domain.SlotStateReady {
			return nil
		}
		code := domain.ErrorCodeModelNotInstalled
		if capability.State == domain.SlotStateUnverified {
			code = domain.ErrorCodeCapabilityUnverified
		}
		if capability.State == domain.SlotStateRestartRequired || capability.State == domain.SlotStateDegraded {
			code = domain.ErrorCodeCapabilityUnverified
		}
		actions := capability.ActionIDs
		if len(actions) == 0 {
			actions = []domain.ID{"open_model_setup"}
		}
		return &domain.Error{
			Code:       code,
			MessageKey: "task.capability_not_ready",
			Retryable:  true,
			ActionIDs:  actions,
		}
	}
	return &domain.Error{
		Code:       domain.ErrorCodeModelNotInstalled,
		MessageKey: "task.capability_not_ready",
		Retryable:  true,
		ActionIDs:  []domain.ID{"open_model_setup"},
	}
}
