package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// CreateWorkflowRunInput 是预检与创建 Run 的共同输入（contracts/openapi.yaml）。
type CreateWorkflowRunInput struct {
	ProjectID            string                 `json:"project_id"`
	Name                 string                 `json:"name"`
	Template             string                 `json:"template"`
	Features             []string               `json:"features"`
	SourceLanguage       string                 `json:"source_language"`
	TargetLanguages      []string               `json:"target_languages"`
	InputRevisionIDs     []string               `json:"input_revision_ids"`
	Extract              *domain.ExtractOptions `json:"extract"`
	ReuseAudioTrackID    string                 `json:"reuse_audio_track_id"`
	ReuseSubtitleTrackID string                 `json:"reuse_subtitle_track_id"`
	EraseMode            domain.EraseMode       `json:"erase_mode"`
	ValidateTimestamps   *bool                  `json:"validate_timestamps"`
	AdjustDuration       *bool                  `json:"adjust_duration"`
	Bilingual            *bool                  `json:"bilingual"`
	SubtitleStyle        *domain.SubtitleStyle  `json:"subtitle_style"`
	// ImportedTranslationTracks 是恢复预检时从 RunConfig 带入的冻结译稿；
	// 创建请求不暴露该字段（json:"-"），由 Core 在事务内自行冻结。
	ImportedTranslationTracks map[string]domain.TranslationTrackRef `json:"-"`
}

// defaultTemplate 是八节点完整译制模板。
const defaultTemplate = "full_pipeline"

// preflightRulesVersion 参与计划摘要；规则变化时旧计划自动失效。
const preflightRulesVersion = 1

func (in CreateWorkflowRunInput) template() string {
	if strings.TrimSpace(in.Template) == "" {
		return defaultTemplate
	}
	return strings.TrimSpace(in.Template)
}

func boolValue(ptr *bool) bool {
	return ptr != nil && *ptr
}

// runConfigFromInput 把契约输入折叠为持久化的冻结配置。
func runConfigFromInput(in CreateWorkflowRunInput) domain.RunConfig {
	cfg := domain.RunConfig{
		Extract:              in.Extract,
		ReuseAudioTrackID:    in.ReuseAudioTrackID,
		ReuseSubtitleTrackID: in.ReuseSubtitleTrackID,
		EraseMode:            in.EraseMode,
		ValidateTimestamps:   boolValue(in.ValidateTimestamps),
		AdjustDuration:       boolValue(in.AdjustDuration),
		Bilingual:            boolValue(in.Bilingual),
		SubtitleStyle:        in.SubtitleStyle,
	}
	for _, id := range in.InputRevisionIDs {
		if id != "" {
			cfg.InputRevisionIDs = append(cfg.InputRevisionIDs, id)
		}
	}
	return cfg
}

// validateRunInput 做不依赖本机能力的静态校验（失败返回 INVALID_REQUEST）。
// normalizeRunSourceLanguage 允许客户端省略源语言（自动识别）；Core 归一为 und。
func normalizeRunSourceLanguage(in CreateWorkflowRunInput) CreateWorkflowRunInput {
	in.SourceLanguage = strings.TrimSpace(in.SourceLanguage)
	if in.SourceLanguage == "" {
		in.SourceLanguage = "und"
	}
	return in
}

func validateRunInput(in CreateWorkflowRunInput) error {
	if strings.TrimSpace(in.ProjectID) == "" {
		return invalidRequestf("project_id required")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return invalidRequestf("name required")
	}
	if len([]rune(name)) > 30 {
		return invalidRequestf("name exceeds 30 characters")
	}
	// 源语言可省略；归一发生在 Preflight/CreateWorkflowRun 入口（validateRunInput 取值传递）。
	tpl, err := pipelineTemplateForInput(in)
	if err != nil {
		return err
	}
	if tpl.RequiresTargetLanguages && len(in.TargetLanguages) == 0 {
		return invalidRequestf("target_languages must have at least one entry")
	}
	seen := make(map[string]struct{}, len(in.TargetLanguages))
	for _, lang := range in.TargetLanguages {
		if strings.TrimSpace(lang) == "" {
			return invalidRequestf("target_languages contains an empty entry")
		}
		if _, ok := seen[lang]; ok {
			return invalidRequestf("duplicate target language %q", lang)
		}
		seen[lang] = struct{}{}
	}
	if tpl.ID == templateToolExtract {
		if in.Extract == nil {
			return invalidRequestf("extract required for %s", tpl.ID)
		}
	}
	if tpl.ID == templateToolExtract || tpl.ID == templateToolSeparate ||
		tpl.ID == templateToolErase || tpl.ID == templateToolTranslate ||
		tpl.ID == templateVoiceClone {
		if len(in.InputRevisionIDs) == 0 {
			return invalidRequestf("input_revision_ids required for %s", tpl.ID)
		}
	}
	if (tpl.ID == templateToolSeparate || tpl.ID == templateToolErase) &&
		(in.ReuseAudioTrackID != "" || in.ReuseSubtitleTrackID != "") {
		return invalidRequestf("reuse tracks are not supported for %s", tpl.ID)
	}
	if tpl.ID == templateToolErase {
		if in.Extract == nil || in.Extract.Mode != domain.ExtractModeOCR {
			return invalidRequestf("extract mode ocr required for %s", tpl.ID)
		}
		if in.EraseMode == "" {
			return invalidRequestf("erase_mode required for %s", tpl.ID)
		}
	}
	if tpl.ID == templateVoiceClone {
		if len(in.InputRevisionIDs) != 1 {
			return invalidRequestf("%s requires exactly one voice source asset", tpl.ID)
		}
	}
	if tpl.ID == templateCustomPipeline {
		if len(in.InputRevisionIDs) != 1 {
			return invalidRequestf("%s requires exactly one media asset", tpl.ID)
		}
		if customNeedsLanguages(in.Features) && len(in.TargetLanguages) == 0 {
			return invalidRequestf("target_languages required for selected custom features")
		}
		if hasCustomFeature(in.Features, featureErase) {
			if in.EraseMode == "" {
				return invalidRequestf("erase_mode required for %s", tpl.ID)
			}
			if in.Extract == nil || in.Extract.Mode != domain.ExtractModeOCR {
				return invalidRequestf("extract mode ocr required for custom erase")
			}
		}
	}
	cfg := runConfigFromInput(in)
	if err := domain.ValidateRunConfig(cfg); err != nil {
		return err
	}
	return nil
}

// preflightPlan 是一次评估的计划摘要输入。
type preflightPlan struct {
	RulesVersion   int
	ProjectID      string
	Name           string
	Template       string
	Features       []string
	SourceLanguage string
	Languages      []string
	InputRevisions []string
	ExtractMode    string
	OCRLanguage    string
	EraseMode      string
	ReuseAudio     string
	ReuseSubtitle  string
	Validate       bool
	Adjust         bool
	Bilingual      bool
	Style          *domain.SubtitleStyle
	Capabilities   []string
	Execution      map[domain.CapabilitySlotKey]domain.ExecutionConfig
}

// preflightQuerier 是预检读取所需的仓储子集；*store.Store 与 *store.Tx 都实现。
type preflightQuerier interface {
	CatalogQuerier
	GetProject(ctx context.Context, id domain.ID) (domain.Project, error)
	GetAsset(ctx context.Context, id domain.ID) (domain.Asset, error)
	GetSubtitleTrack(ctx context.Context, id domain.ID) (domain.SubtitleTrack, error)
	ListProjectTracks(ctx context.Context, projectID domain.ID) ([]domain.SubtitleTrack, error)
	CountRunningTasks(ctx context.Context) (int, error)
}

// Preflight 评估任务/运行计划（只读，200）。
func (s *Service) Preflight(ctx context.Context, in CreateWorkflowRunInput) (domain.PreflightResult, error) {
	in = normalizeRunSourceLanguage(in)
	if err := validateRunInput(in); err != nil {
		return domain.PreflightResult{}, err
	}
	return s.preflightWith(ctx, s.store, in, false)
}

// preflightWith 在指定仓储上评估；includeBusy 决定是否检查动态资源占用。
func (s *Service) preflightWith(ctx context.Context, q preflightQuerier, in CreateWorkflowRunInput, includeBusy bool) (domain.PreflightResult, error) {
	in = normalizeRunSourceLanguage(in)
	if err := validateRunInput(in); err != nil {
		return domain.PreflightResult{}, err
	}
	project, err := q.GetProject(ctx, in.ProjectID)
	if err != nil {
		return domain.PreflightResult{}, mapReference(err, "project")
	}
	issues := make([]domain.PreflightIssue, 0, 8)

	tpl, err := pipelineTemplateForInput(in)
	if err != nil {
		return domain.PreflightResult{}, err
	}

	// 磁盘空间是独立于能力状态的准入事实：输入大小参与估算，永久性不足
	// 为 blocker，低于健康线为 warning（FR-SET-22）。
	issues = append(issues, s.diskPreflightIssues(ctx, q, in)...)

	if in.ReuseAudioTrackID != "" {
		asset, err := q.GetAsset(ctx, in.ReuseAudioTrackID)
		if err != nil || asset.Type != domain.AssetTypeAudioTrack || asset.ProjectID != project.ID {
			issues = append(issues, domain.PreflightIssue{
				Severity:   domain.PreflightBlocker,
				Code:       "REUSE_ASSET_INVALID",
				MessageKey: "preflight.reuse_asset_invalid",
				ActionIDs:  []domain.ID{"reselect_reuse_audio"},
			})
		}
	}
	if in.ReuseSubtitleTrackID != "" {
		track, err := q.GetSubtitleTrack(ctx, in.ReuseSubtitleTrackID)
		valid := err == nil && track.ProjectID == project.ID
		if valid && tpl.ID == templateCustomPipeline {
			valid = track.Type == domain.TrackTypeTranscription &&
				track.Language == in.SourceLanguage &&
				currentRevision(track) != nil
		}
		if !valid {
			issues = append(issues, domain.PreflightIssue{
				Severity:   domain.PreflightBlocker,
				Code:       "REUSE_TRACK_INVALID",
				MessageKey: "preflight.reuse_track_invalid",
				ActionIDs:  []domain.ID{"reselect_reuse_subtitle"},
			})
		}
	}

	if tpl.ID == templateToolExtract {
		if in.Extract == nil {
			issues = append(issues, domain.PreflightIssue{
				Severity:   domain.PreflightBlocker,
				Code:       "EXTRACT_OPTIONS_REQUIRED",
				MessageKey: "preflight.extract_options_required",
				ActionIDs:  []domain.ID{},
			})
		} else if in.Extract.Mode != domain.ExtractModeASR && in.Extract.OCRLanguage == "" {
			issues = append(issues, domain.PreflightIssue{
				Severity:   domain.PreflightBlocker,
				Code:       "OCR_LANGUAGE_REQUIRED",
				MessageKey: "preflight.ocr_language_required",
				ActionIDs:  []domain.ID{"choose_ocr_language"},
			})
		}
	}
	if tpl.ID == templateToolErase && in.Extract != nil && in.Extract.OCRLanguage == "" {
		issues = append(issues, domain.PreflightIssue{
			Severity:   domain.PreflightBlocker,
			Code:       "OCR_LANGUAGE_REQUIRED",
			MessageKey: "preflight.ocr_language_required",
			ActionIDs:  []domain.ID{"choose_ocr_language"},
		})
	}
	if tpl.ID == templateToolExtract || tpl.ID == templateToolSeparate ||
		tpl.ID == templateToolErase || tpl.ID == templateToolTranslate ||
		tpl.ID == templateVoiceClone {
		allowVideo := tpl.ID != templateToolTranslate && tpl.ID != templateVoiceClone
		allowAudio := tpl.ID != templateToolErase && tpl.ID != templateToolTranslate
		allowAudioTrack := tpl.ID == templateToolExtract || tpl.ID == templateVoiceClone
		allowSubtitle := tpl.ID == templateToolTranslate
		for _, inputID := range in.InputRevisionIDs {
			asset, err := q.GetAsset(ctx, inputID)
			valid := err == nil && asset.ProjectID == project.ID &&
				((allowVideo && asset.Type == domain.AssetTypeVideo) ||
					(allowAudio && asset.Type == domain.AssetTypeAudio) ||
					(allowAudioTrack && asset.Type == domain.AssetTypeAudioTrack) ||
					(allowSubtitle && asset.Type == domain.AssetTypeSubtitle))
			if !valid {
				issues = append(issues, domain.PreflightIssue{
					Severity:   domain.PreflightBlocker,
					Code:       "INPUT_ASSET_INVALID",
					MessageKey: "preflight.input_asset_invalid",
					ActionIDs:  []domain.ID{"reselect_input_media"},
				})
			}
		}
	}

	if tpl.ID == templateCustomPipeline {
		assetID := in.InputRevisionIDs[0]
		asset, err := q.GetAsset(ctx, assetID)
		allowVideo := true
		allowAudio := !hasCustomFeature(in.Features, featureErase) && !hasCustomFeature(in.Features, featureMix)
		valid := err == nil && asset.ProjectID == project.ID &&
			((allowVideo && asset.Type == domain.AssetTypeVideo) ||
				(allowAudio && asset.Type == domain.AssetTypeAudio))
		if !valid {
			issues = append(issues, domain.PreflightIssue{
				Severity:   domain.PreflightBlocker,
				Code:       "INPUT_ASSET_INVALID",
				MessageKey: "preflight.input_asset_invalid",
				ActionIDs:  []domain.ID{"reselect_input_media"},
			})
		}
		if hasCustomFeature(in.Features, featureErase) &&
			(in.Extract == nil || in.Extract.OCRLanguage == "") {
			issues = append(issues, domain.PreflightIssue{
				Severity:   domain.PreflightBlocker,
				Code:       "OCR_LANGUAGE_REQUIRED",
				MessageKey: "preflight.ocr_language_required",
				ActionIDs:  []domain.ID{"choose_ocr_language"},
			})
		}
	}
	if tpl.ID == templateVoiceClone {
		tracks, err := q.ListProjectTracks(ctx, project.ID)
		if err != nil {
			return domain.PreflightResult{}, err
		}
		if !hasCurrentTrack(tracks, in.SourceLanguage, domain.TrackTypeTranscription) {
			issues = append(issues, domain.PreflightIssue{
				Severity:   domain.PreflightBlocker,
				Code:       "SOURCE_TRACK_REQUIRED",
				MessageKey: "preflight.source_track_required",
				ActionIDs:  []domain.ID{"import_source_subtitle"},
			})
		}
		for _, language := range in.TargetLanguages {
			if hasCurrentTrack(tracks, language, domain.TrackTypeTranslation) {
				continue
			}
			issues = append(issues, domain.PreflightIssue{
				Severity:   domain.PreflightBlocker,
				Code:       "TRANSLATION_TRACK_REQUIRED",
				MessageKey: "preflight.translation_track_required",
				ActionIDs:  []domain.ID{"import_translated_subtitle"},
			})
		}
		timelineIssues, err := s.cloneTimelinePreflightIssues(ctx, q, project.ID, in, tracks)
		if err != nil {
			return domain.PreflightResult{}, err
		}
		issues = append(issues, timelineIssues...)
	}

	capabilities, err := s.opts.Capabilities.Catalog(ctx, q)
	if err != nil {
		return domain.PreflightResult{}, err
	}
	states := make(map[domain.CapabilitySlotKey]domain.Capability, len(capabilities))
	stateNames := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		states[capability.Slot] = capability
		stateNames = append(stateNames, fmt.Sprintf("%s=%s", capability.Slot, capability.State))
	}

	extractNeeded := tpl.Has(domain.NodeKeyExtract) &&
		!(in.ReuseSubtitleTrackID != "" && !tpl.Has(domain.NodeKeyErase))
	translationNeeded := tpl.Has(domain.NodeKeyTranslate)
	if tpl.ID == templateCustomPipeline {
		translationNeeded = false
		if tpl.Has(domain.NodeKeyTranslate) {
			if in.ImportedTranslationTracks != nil {
				// 恢复：只认创建时冻结的译稿；项目内后来新增的轨不改变计划。
				for _, language := range in.TargetLanguages {
					if _, ok := in.ImportedTranslationTracks[language]; !ok {
						translationNeeded = true
						break
					}
				}
			} else {
				tracks, err := q.ListProjectTracks(ctx, project.ID)
				if err != nil {
					return domain.PreflightResult{}, err
				}
				for _, language := range in.TargetLanguages {
					if !hasCurrentTrack(tracks, language, domain.TrackTypeTranslation) {
						translationNeeded = true
						break
					}
				}
			}
		}
		if tpl.Has(domain.NodeKeyExtract) && !tpl.Has(domain.NodeKeyErase) {
			extractNeeded = in.ReuseSubtitleTrackID == "" && translationNeeded
		}
	}
	required := requiredSlots(in, tpl, extractNeeded, translationNeeded)
	for _, slot := range required {
		capability, ok := states[slot]
		if ok && capability.State == domain.SlotStateReady {
			continue
		}
		code := capability.ReasonCode
		if code == "" {
			code = string(domain.ErrorCodeModelNotInstalled)
		}
		actions := capability.ActionIDs
		if len(actions) == 0 {
			actions = []domain.ID{"open_model_setup"}
		}
		issues = append(issues, domain.PreflightIssue{
			Severity:   domain.PreflightBlocker,
			Code:       code,
			MessageKey: "preflight." + strings.ToLower(strings.ReplaceAll(code, "-", "_")),
			ActionIDs:  actions,
		})
	}

	if tpl.ID != templateToolExtract && tpl.ID != templateToolSeparate &&
		tpl.ID != templateToolErase && tpl.ID != templateToolTranslate &&
		in.Extract != nil && in.Extract.Mode != domain.ExtractModeASR && in.Extract.OCRLanguage == "" {
		issues = append(issues, domain.PreflightIssue{
			Severity:   domain.PreflightWarning,
			Code:       "OCR_LANGUAGE_UNSET",
			MessageKey: "preflight.ocr_language_unset",
			ActionIDs:  []domain.ID{"choose_ocr_language"},
		})
	}

	if includeBusy {
		running, err := q.CountRunningTasks(ctx)
		if err != nil {
			return domain.PreflightResult{}, err
		}
		slots := s.heavySlotCount()
		if running >= slots {
			issues = append(issues, domain.PreflightIssue{
				Severity:   domain.PreflightWarning,
				Code:       "RESOURCE_BUSY",
				MessageKey: "preflight.resource_busy",
				ActionIDs:  []domain.ID{},
			})
		}
	}

	plan := preflightPlan{
		RulesVersion:   preflightRulesVersion,
		ProjectID:      project.ID,
		Name:           strings.TrimSpace(in.Name),
		Template:       in.template(),
		Features:       append([]string{}, tpl.Features...),
		SourceLanguage: in.SourceLanguage,
		Languages:      append([]string{}, in.TargetLanguages...),
		InputRevisions: append([]string{}, in.InputRevisionIDs...),
		ReuseAudio:     in.ReuseAudioTrackID,
		ReuseSubtitle:  in.ReuseSubtitleTrackID,
		Validate:       boolValue(in.ValidateTimestamps),
		Adjust:         boolValue(in.AdjustDuration),
		Bilingual:      boolValue(in.Bilingual),
		Style:          in.SubtitleStyle,
		Capabilities:   stateNames,
	}
	if s.opts.PipelineOrchestration {
		freezeInput := in
		if tpl.ID == templateCustomPipeline && freezeInput.ImportedTranslationTracks == nil {
			tracks, err := q.ListProjectTracks(ctx, project.ID)
			if err != nil {
				return domain.PreflightResult{}, err
			}
			freezeInput.ImportedTranslationTracks = freezeImportedTranslationTracks(in.TargetLanguages, tracks)
		}
		if executionQ, ok := q.(executionQuerier); ok {
			plan.Execution, err = s.freezeExecutionConfig(ctx, executionQ, freezeInput, tpl)
			if err != nil {
				code, domainError := domain.CodeOf(err)
				if !domainError {
					return domain.PreflightResult{}, err
				}
				issues = append(issues, domain.PreflightIssue{
					Severity: domain.PreflightBlocker, Code: string(code),
					MessageKey: "preflight.execution_config_unverified", ActionIDs: []domain.ID{"open_model_setup"},
				})
			}
		} else {
			return domain.PreflightResult{}, executionUnverified()
		}
	}
	if in.Extract != nil {
		plan.ExtractMode = string(in.Extract.Mode)
		plan.OCRLanguage = string(in.Extract.OCRLanguage)
	}
	plan.EraseMode = string(in.EraseMode)
	hash, err := digestOf(plan)
	if err != nil {
		return domain.PreflightResult{}, err
	}
	if issues == nil {
		issues = []domain.PreflightIssue{}
	}
	return domain.PreflightResult{
		PlanID:   "plan_" + hash[:16],
		PlanHash: hash,
		Issues:   issues,
	}, nil
}

// hasCurrentTrack 报告项目内是否存在指定语言/类型且有 current revision 的轨。
func hasCurrentTrack(tracks []domain.SubtitleTrack, language string, typ domain.TrackType) bool {
	for _, track := range tracks {
		if track.Language == language && track.Type == typ && currentRevision(track) != nil {
			return true
		}
	}
	return false
}

// requiredSlots 返回该计划需要的槽位（先发布准入，再按本机状态判定）。
// extractNeeded 由预检按导入轨/擦除节点计算：04 被复用时不再要求 ASR/OCR。
func requiredSlots(in CreateWorkflowRunInput, tpl pipelineTemplate, extractNeeded, translationNeeded bool) []domain.CapabilitySlotKey {
	mode := domain.ExtractModeASR
	if in.Extract != nil && in.Extract.Mode != "" {
		mode = in.Extract.Mode
	}
	if tpl.ID == templateCustomPipeline {
		slots := make([]domain.CapabilitySlotKey, 0, 6)
		if tpl.Has(domain.NodeKeySeparate) {
			slots = append(slots, domain.CapabilitySlotSeparation)
		}
		if tpl.Has(domain.NodeKeyErase) {
			slots = append(slots, domain.CapabilitySlotOCR, domain.CapabilitySlotSubtitleErase)
		}
		if extractNeeded {
			if tpl.Has(domain.NodeKeyErase) {
				slots = append(slots, domain.CapabilitySlotOCR)
			} else if mode != domain.ExtractModeOCR {
				slots = append(slots, domain.CapabilitySlotASR)
			}
			if mode != domain.ExtractModeASR {
				slots = append(slots, domain.CapabilitySlotOCR)
			}
		}
		if translationNeeded {
			slots = append(slots, domain.CapabilitySlotTranslate)
		}
		if tpl.Has(domain.NodeKeyClone) {
			// 克隆模型自身包含语音合成，只校验 voice-clone 槽；
			// 通用 TTS 槽未就绪不影响克隆/专属链路。
			slots = append(slots, domain.CapabilitySlotVoiceClone)
		}
		if extractNeeded || tpl.Has(domain.NodeKeySeparate) ||
			tpl.Has(domain.NodeKeyErase) || tpl.Has(domain.NodeKeyMix) {
			slots = append(slots, domain.CapabilitySlotFFmpeg)
		}
		return dedupeSlots(slots)
	}
	if tpl.ID == templateToolExtract {
		slots := []domain.CapabilitySlotKey{domain.CapabilitySlotFFmpeg}
		if mode != domain.ExtractModeOCR {
			slots = append(slots, domain.CapabilitySlotASR)
		}
		if mode != domain.ExtractModeASR {
			slots = append(slots, domain.CapabilitySlotOCR)
		}
		return slots
	}
	if tpl.ID == templateToolSeparate {
		return []domain.CapabilitySlotKey{domain.CapabilitySlotFFmpeg, domain.CapabilitySlotSeparation}
	}
	if tpl.ID == templateToolErase {
		return []domain.CapabilitySlotKey{
			domain.CapabilitySlotFFmpeg,
			domain.CapabilitySlotOCR,
			domain.CapabilitySlotSubtitleErase,
		}
	}
	if tpl.ID == templateToolTranslate {
		return []domain.CapabilitySlotKey{domain.CapabilitySlotTranslate}
	}
	if tpl.ID == templateVoiceClone {
		// 克隆模型内置合成能力，不依赖独立 TTS 槽。
		return []domain.CapabilitySlotKey{domain.CapabilitySlotVoiceClone}
	}
	slots := []domain.CapabilitySlotKey{
		domain.CapabilitySlotFFmpeg,
		domain.CapabilitySlotTranslate,
		domain.CapabilitySlotVoiceClone,
	}
	if mode != domain.ExtractModeOCR {
		slots = append(slots, domain.CapabilitySlotASR)
	}
	if mode != domain.ExtractModeASR {
		slots = append(slots, domain.CapabilitySlotOCR)
	}
	if tpl.Has(domain.NodeKeyErase) {
		slots = append(slots, domain.CapabilitySlotSubtitleErase)
	}
	if in.ReuseAudioTrackID == "" {
		slots = append(slots, domain.CapabilitySlotSeparation)
	}
	return slots
}

func dedupeSlots(slots []domain.CapabilitySlotKey) []domain.CapabilitySlotKey {
	seen := make(map[domain.CapabilitySlotKey]struct{}, len(slots))
	out := make([]domain.CapabilitySlotKey, 0, len(slots))
	for _, slot := range slots {
		if _, ok := seen[slot]; ok {
			continue
		}
		seen[slot] = struct{}{}
		out = append(out, slot)
	}
	return out
}

// normalizeRun 补齐 Run 响应的派生字段：数组不为 nil，并暴露创建时冻结的输入资产 ID。
// input_asset_ids 不单独持久化，始终由 Config.InputRevisionIDs 派生。
func normalizeRun(run *domain.WorkflowRun) {
	if run.Nodes == nil {
		run.Nodes = []domain.PipelineNodeState{}
	}
	if run.Languages == nil {
		run.Languages = []string{}
	}
	if run.ModeType == "" {
		run.ModeType = modeTypeForRun(run.Template, run.Config)
	}
	run.Params = runParamsFor(run)
	run.InputAssetIDs = append([]domain.ID{}, run.Config.InputRevisionIDs...)
	if run.InputAssetIDs == nil {
		run.InputAssetIDs = []domain.ID{}
	}
}

// modeTypeForRun 把模板/配置映射为卡片类型；空模板按 full_pipeline 处理。
func modeTypeForRun(template string, cfg domain.RunConfig) string {
	switch canonicalTemplateID(template) {
	case templateFullPipeline:
		return "full_pipeline"
	case templateCustomPipeline:
		return "custom_workflow"
	case templateToolErase:
		if cfg.EraseMode == domain.EraseModeDialogAndArt {
			return "subtitle_erase_dialog_and_art"
		}
		return "subtitle_erase_dialog"
	case templateToolSeparate:
		return "audio_separate"
	case templateVoiceClone:
		return "high_sentiment_tts_clone"
	case templateToolTranslate:
		return "subtitle_translate"
	case templateToolExtract:
		if cfg.Extract == nil {
			return ""
		}
		switch cfg.Extract.Mode {
		case domain.ExtractModeOCR:
			return "subtitle_extract_ocr"
		case domain.ExtractModeASROCR:
			return "subtitle_extract_asr_ocr"
		default:
			return "subtitle_extract_asr"
		}
	default:
		return ""
	}
}

// runParamsFor 从冻结配置派生卡片详情所需的参数回显。
func runParamsFor(run *domain.WorkflowRun) *domain.RunParams {
	params := &domain.RunParams{
		EraseMode:          run.Config.EraseMode,
		ValidateTimestamps: run.Config.ValidateTimestamps,
		AdjustDuration:     run.Config.AdjustDuration,
		Bilingual:          run.Config.Bilingual,
		SubtitleStyle:      run.Config.SubtitleStyle,
	}
	if run.Config.Extract != nil {
		params.ExtractMode = run.Config.Extract.Mode
		params.OCRLanguage = run.Config.Extract.OCRLanguage
		if run.Config.Extract.Mode == domain.ExtractModeASR || run.Config.Extract.Mode == domain.ExtractModeASROCR {
			params.ASRLanguage = run.SourceLanguage
		}
	}
	return params
}

// normalizeRunResponse 对返回 WorkflowRun 的写操作统一补齐派生字段（pause/resume 等）。
// 幂等回执由闭包内规范化后的 Run 生成，因此重放路径本身已带 input_asset_ids。
func normalizeRunResponse(resp Response) Response {
	if run, ok := resp.Body.(domain.WorkflowRun); ok {
		normalizeRun(&run)
		resp.Body = run
	}
	return resp
}

// GetWorkflowRun 返回运行计划快照。
func (s *Service) GetWorkflowRun(ctx context.Context, id string) (domain.WorkflowRun, error) {
	if id == "" {
		return domain.WorkflowRun{}, invalidRequestf("run id required")
	}
	run, err := s.store.GetWorkflowRun(ctx, id)
	if err != nil {
		return domain.WorkflowRun{}, err
	}
	normalizeRun(&run)
	return run, nil
}

// CreateWorkflowRun 创建业务计划（201）；提交时重新预检，条件变化返回 PREFLIGHT_STALE。
func (s *Service) CreateWorkflowRun(ctx context.Context, idem Idem, in CreateWorkflowRunInput) (Response, error) {
	in = normalizeRunSourceLanguage(in)
	if err := validateRunInput(in); err != nil {
		return Response{}, err
	}
	var events []domain.DomainEvent
	resp, err := s.write(ctx, "workflow-runs.create", idem, 201, func(ctx context.Context, tx *store.Tx) (any, error) {
		result, err := s.preflightWith(ctx, tx, in, true)
		if err != nil {
			return nil, err
		}
		if result.HasBlocker() {
			// 永久性空间不足是独立的 507 语义，不混同于能力/状态变化。
			if hasBlockerCode(result, domain.ErrorCodeInsufficientDisk) {
				return nil, errInsufficientDisk()
			}
			return nil, fmt.Errorf("%w: capability conditions changed", domain.ErrPreflightStale)
		}
		now := s.Now()
		tpl, err := pipelineTemplateForInput(in)
		if err != nil {
			return nil, err
		}
		cfg := runConfigFromInput(in)
		if cfg.ReuseSubtitleTrackID != "" {
			track, err := tx.GetSubtitleTrack(ctx, cfg.ReuseSubtitleTrackID)
			if err != nil {
				return nil, mapReference(err, "subtitle track")
			}
			if revision := currentRevision(track); revision != nil {
				// 冻结复用轨的 revision：专属链路擦除的 04 OCR 会 upsert 同语言轨，
				// 只存 track ID 会让 06 读到被 OCR 替换后的 current revision。
				cfg.ReuseSubtitleRevisionID = revision.ID
			}
		}
		if tpl.ID == templateCustomPipeline {
			// 新 custom Run 始终写非 nil map（可能为空），与旧 Run 的 nil 兼容路径区分；
			// 只有含 06 的模板才需要按目标语言冻结导入译稿。
			cfg.ImportedTranslationTracks = map[string]domain.TranslationTrackRef{}
			if tpl.Has(domain.NodeKeyTranslate) {
				tracks, err := tx.ListProjectTracks(ctx, domain.ID(in.ProjectID))
				if err != nil {
					return nil, err
				}
				cfg.ImportedTranslationTracks = freezeImportedTranslationTracks(in.TargetLanguages, tracks)
			}
		}
		if s.opts.PipelineOrchestration {
			freezeInput := in
			freezeInput.ImportedTranslationTracks = cfg.ImportedTranslationTracks
			cfg.Execution, err = s.freezeExecutionConfig(ctx, tx, freezeInput, tpl)
			if err != nil {
				return nil, err
			}
		}
		nodes := make([]domain.PipelineNodeState, 0, len(tpl.Nodes))
		for _, key := range tpl.Nodes {
			nodes = append(nodes, domain.PipelineNodeState{Key: key, Status: domain.NodeStatusPending})
		}
		run := domain.WorkflowRun{
			ID:             store.NewID("run"),
			ProjectID:      in.ProjectID,
			Name:           strings.TrimSpace(in.Name),
			Template:       tpl.ID,
			ModeType:       modeTypeForRun(tpl.ID, cfg),
			Status:         domain.RunStatusQueued,
			SourceLanguage: in.SourceLanguage,
			Languages:      append([]string{}, in.TargetLanguages...),
			Nodes:          nodes,
			Config:         cfg,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		if err := tx.CreateWorkflowRun(ctx, run); err != nil {
			return nil, err
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type:      "workflow.created",
			ProjectID: run.ProjectID,
			Data: map[string]any{
				"run_id":           run.ID,
				"template":         run.Template,
				"languages":        run.Languages,
				"plan_hash":        result.PlanHash,
				"preflight_issues": result.Issues,
			},
		}); err != nil {
			return nil, err
		}
		if s.opts.PipelineOrchestration {
			if err := s.advancePipeline(ctx, tx, run.ID, &events); err != nil {
				return nil, err
			}
			if updated, err := tx.GetWorkflowRun(ctx, run.ID); err == nil {
				run = updated
			}
		}
		normalizeRun(&run)
		return run, nil
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	s.notify()
	return resp, nil
}

// PauseWorkflowRun 请求暂停（202）；没有执行中任务时立即收敛到 PAUSED。
func (s *Service) PauseWorkflowRun(ctx context.Context, idem Idem, id string) (Response, error) {
	var events []domain.DomainEvent
	resp, err := s.write(ctx, "workflow-runs.pause", idem, 202, func(ctx context.Context, tx *store.Tx) (any, error) {
		run, err := tx.GetWorkflowRun(ctx, id)
		if err != nil {
			return nil, err
		}
		if run.Status.Terminal() {
			return nil, errRunClosed(run.ID, run.Status)
		}
		if domain.RunIsPaused(run.Status) {
			// 幂等回执在闭包内生成，派生字段必须先补齐，否则重放会丢 input_asset_ids。
			normalizeRun(&run)
			return run, nil
		}
		// 状态冲突按契约返回 409（pause 端点只声明 401/403/404/409/503）。
		if !domain.CanTransitionRun(run.Status, domain.RunStatusPauseRequested) {
			return nil, errConflictf(domain.ErrorCodeRevisionConflict, "error.run_not_pausable",
				"run %s is %s and cannot be paused", id, run.Status)
		}
		now := s.Now()
		requested, err := tx.UpdateRunStatus(ctx, id, domain.RunStatusPauseRequested, run.StateVersion, now)
		if err != nil {
			return nil, err
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type:         "workflow.pause_requested",
			ProjectID:    requested.ProjectID,
			StateVersion: requested.StateVersion,
			Data:         map[string]any{"run_id": requested.ID},
		}); err != nil {
			return nil, err
		}
		settled, err := s.settlePauseInTx(ctx, tx, requested, &events)
		if err != nil {
			return nil, err
		}
		// 幂等回执在闭包内生成；先补派生字段，保证首次与重放响应一致。
		normalizeRun(&settled)
		return settled, nil
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	s.notify()
	return normalizeRunResponse(resp), nil
}

// settlePauseInTx 在没有执行中任务时把 Run 收敛到 PAUSED，并挂起排队任务。
func (s *Service) settlePauseInTx(ctx context.Context, tx *store.Tx, run domain.WorkflowRun, events *[]domain.DomainEvent) (domain.WorkflowRun, error) {
	running, err := tx.ListTasksByStatuses(ctx, run.ID, domain.TaskStatusRunning, domain.TaskStatusCancelRequested)
	if err != nil {
		return domain.WorkflowRun{}, err
	}
	if len(running) > 0 {
		return run, nil
	}
	now := s.Now()
	parked, err := s.parkQueuedTasks(ctx, tx, run.ID, events, now)
	if err != nil {
		return domain.WorkflowRun{}, err
	}
	_ = parked
	paused, err := tx.UpdateRunStatus(ctx, run.ID, domain.RunStatusPaused, run.StateVersion, now)
	if err != nil {
		return domain.WorkflowRun{}, err
	}
	if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
		Type:         "workflow.paused",
		ProjectID:    paused.ProjectID,
		StateVersion: paused.StateVersion,
		Data:         map[string]any{"run_id": paused.ID},
	}); err != nil {
		return domain.WorkflowRun{}, err
	}
	return paused, nil
}

// parkQueuedTasks 把 Run 内排队/等待资源的任务收敛到 PAUSED（只登记，不派发）。
// 导出是交付动作，不随 Run 暂停；挂起时清除等待原因，与调度轮询收敛保持等价。
func (s *Service) parkQueuedTasks(ctx context.Context, tx *store.Tx, runID domain.ID, events *[]domain.DomainEvent, now time.Time) (int, error) {
	queued, err := tx.ListTasksByStatuses(ctx, runID,
		domain.TaskStatusQueued, domain.TaskStatusWaitingResource, domain.TaskStatusPauseRequested)
	if err != nil {
		return 0, err
	}
	parked := 0
	for _, task := range queued {
		if task.Kind == domain.TaskKindExport {
			continue
		}
		current := task
		if current.Status == domain.TaskStatusQueued || current.Status == domain.TaskStatusWaitingResource {
			next, err := tx.UpdateTaskResult(ctx, current.ID, domain.TaskStatusPauseRequested,
				current.ArtifactIDs, nil, current.StateVersion, now)
			if err != nil {
				return parked, err
			}
			current = next
		}
		if current.Status == domain.TaskStatusPauseRequested {
			next, err := tx.UpdateTaskResult(ctx, current.ID, domain.TaskStatusPaused,
				current.ArtifactIDs, nil, current.StateVersion, now)
			if err != nil {
				return parked, err
			}
			current = next
		}
		parked++
		_ = current
	}
	return parked, nil
}

// ResumeWorkflowRun 恢复运行（202）：重新预检后回到 QUEUED 并唤醒排队任务。
func (s *Service) ResumeWorkflowRun(ctx context.Context, idem Idem, id string) (Response, error) {
	var events []domain.DomainEvent
	resp, err := s.write(ctx, "workflow-runs.resume", idem, 202, func(ctx context.Context, tx *store.Tx) (any, error) {
		run, err := tx.GetWorkflowRun(ctx, id)
		if err != nil {
			return nil, err
		}
		if run.Status.Terminal() {
			return nil, errRunClosed(run.ID, run.Status)
		}
		if run.Status != domain.RunStatusPaused {
			// 状态冲突按契约返回 409（resume 端点只声明 401/403/404/409/503）。
			return nil, errConflictf(domain.ErrorCodeRevisionConflict, "error.run_not_paused",
				"run %s is %s, resume requires PAUSED", id, run.Status)
		}
		preflight, err := s.preflightWith(ctx, tx, runInputFromRun(run), true)
		if err != nil {
			return nil, err
		}
		if preflight.HasBlocker() {
			return nil, fmt.Errorf("%w: preflight blocked on resume", domain.ErrPreflightStale)
		}
		now := s.Now()
		updated, err := tx.UpdateRunStatus(ctx, id, domain.RunStatusQueued, run.StateVersion, now)
		if err != nil {
			return nil, err
		}
		paused, err := tx.ListTasksByStatuses(ctx, id, domain.TaskStatusPaused)
		if err != nil {
			return nil, err
		}
		for _, task := range paused {
			// 恢复排队时清除旧等待/暂停提示，避免 QUEUED 快照继续显示阻断。
			if _, err := tx.UpdateTaskResult(ctx, task.ID, domain.TaskStatusQueued,
				task.ArtifactIDs, nil, task.StateVersion, now); err != nil {
				return nil, err
			}
		}
		if s.opts.PipelineOrchestration {
			if err := s.advancePipeline(ctx, tx, updated.ID, &events); err != nil {
				return nil, err
			}
			// 推进可能把 Run 从 QUEUED 推到 RUNNING；响应用推进后的快照。
			if refreshed, err := tx.GetWorkflowRun(ctx, updated.ID); err == nil {
				updated = refreshed
			}
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type:         "workflow.resumed",
			ProjectID:    updated.ProjectID,
			StateVersion: updated.StateVersion,
			Data:         map[string]any{"run_id": updated.ID, "requeued_tasks": len(paused)},
		}); err != nil {
			return nil, err
		}
		// 幂等回执在闭包内生成；先补派生字段，保证首次与重放响应一致。
		normalizeRun(&updated)
		return updated, nil
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	s.notify()
	return normalizeRunResponse(resp), nil
}

// runInputFromRun 从已持久化的 Run 重建预检输入（恢复时重新预检）。
func runInputFromRun(run domain.WorkflowRun) CreateWorkflowRunInput {
	in := CreateWorkflowRunInput{
		ProjectID:            run.ProjectID,
		Name:                 run.Name,
		Template:             run.Template,
		SourceLanguage:       run.SourceLanguage,
		TargetLanguages:      run.Languages,
		InputRevisionIDs:     run.Config.InputRevisionIDs,
		Extract:              run.Config.Extract,
		ReuseAudioTrackID:    run.Config.ReuseAudioTrackID,
		ReuseSubtitleTrackID: run.Config.ReuseSubtitleTrackID,
		EraseMode:            run.Config.EraseMode,
		SubtitleStyle:        run.Config.SubtitleStyle,
	}
	if run.Config.ValidateTimestamps {
		v := true
		in.ValidateTimestamps = &v
	}
	if run.Config.AdjustDuration {
		v := true
		in.AdjustDuration = &v
	}
	if run.Config.Bilingual {
		v := true
		in.Bilingual = &v
	}
	if run.Template == templateCustomPipeline {
		in.Features = customFeaturesFromNodes(run.Nodes)
		in.ImportedTranslationTracks = run.Config.ImportedTranslationTracks
	}
	return in
}

// freezeImportedTranslationTracks 按“最新 current translation 轨”冻结每语种译稿；
// 没有 current 轨的语言不写入，06 会为该语言生成。
func freezeImportedTranslationTracks(languages []string, tracks []domain.SubtitleTrack) map[string]domain.TranslationTrackRef {
	frozen := make(map[string]domain.TranslationTrackRef, len(languages))
	for _, language := range languages {
		track := pickPipelineTrack(tracks, language, domain.TrackTypeTranslation)
		if track == nil {
			continue
		}
		revision := currentRevision(*track)
		if revision == nil {
			continue
		}
		frozen[language] = domain.TranslationTrackRef{TrackID: track.ID, RevisionID: revision.ID}
	}
	return frozen
}

func customNeedsLanguages(features []string) bool {
	return hasCustomFeature(features, featureTranslate) ||
		hasCustomFeature(features, featureClone) ||
		hasCustomFeature(features, featureMix)
}
