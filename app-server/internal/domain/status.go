package domain

// 状态与枚举定义。
//
// 枚举取值以 contracts/openapi.yaml（v0.2，components/schemas）为唯一口径；
// 状态转换语义见 docs/initialization-and-task-lifecycle.md 与
// docs/workflow-and-review.md。新增取值必须先改契约，不能只改这里。

// RunStatus 是业务 Run（八节点计划容器）的状态。
type RunStatus string

const (
	RunStatusDraft          RunStatus = "DRAFT"
	RunStatusQueued         RunStatus = "QUEUED"
	RunStatusRunning        RunStatus = "RUNNING"
	RunStatusPauseRequested RunStatus = "PAUSE_REQUESTED"
	RunStatusPaused         RunStatus = "PAUSED"
	RunStatusWaitingReview  RunStatus = "WAITING_REVIEW"
	RunStatusPartial        RunStatus = "PARTIAL"
	RunStatusSucceeded      RunStatus = "SUCCEEDED"
	RunStatusFailed         RunStatus = "FAILED"
	RunStatusCanceled       RunStatus = "CANCELED"
)

// Valid 报告 s 是否是契约定义过的 Run 状态。
func (s RunStatus) Valid() bool {
	switch s {
	case RunStatusDraft, RunStatusQueued, RunStatusRunning, RunStatusPauseRequested,
		RunStatusPaused, RunStatusWaitingReview, RunStatusPartial, RunStatusSucceeded,
		RunStatusFailed, RunStatusCanceled:
		return true
	default:
		return false
	}
}

// Terminal 报告 s 是否为终止状态；终止状态不可再迁移（新生产需新建 Run）。
func (s RunStatus) Terminal() bool {
	switch s {
	case RunStatusPartial, RunStatusSucceeded, RunStatusFailed, RunStatusCanceled:
		return true
	default:
		return false
	}
}

// TaskStatus 是可调度任务的状态。
type TaskStatus string

const (
	TaskStatusQueued          TaskStatus = "QUEUED"
	TaskStatusWaitingResource TaskStatus = "WAITING_RESOURCE"
	TaskStatusRunning         TaskStatus = "RUNNING"
	TaskStatusPauseRequested  TaskStatus = "PAUSE_REQUESTED"
	TaskStatusPaused          TaskStatus = "PAUSED"
	TaskStatusSucceeded       TaskStatus = "SUCCEEDED"
	TaskStatusFailed          TaskStatus = "FAILED"
	TaskStatusCancelRequested TaskStatus = "CANCEL_REQUESTED"
	TaskStatusCanceled        TaskStatus = "CANCELED"
	TaskStatusInterrupted     TaskStatus = "INTERRUPTED"
)

// Valid 报告 s 是否是契约定义过的 Task 状态。
func (s TaskStatus) Valid() bool {
	switch s {
	case TaskStatusQueued, TaskStatusWaitingResource, TaskStatusRunning,
		TaskStatusPauseRequested, TaskStatusPaused, TaskStatusSucceeded, TaskStatusFailed,
		TaskStatusCancelRequested, TaskStatusCanceled, TaskStatusInterrupted:
		return true
	default:
		return false
	}
}

// Terminal 报告 s 是否为不可再变更的 Task 状态。
//
// FAILED / INTERRUPTED 不是不可变的：显式重试会回到 QUEUED（生命周期文档 §4）。
// SUCCEEDED / CANCELED 没有后续状态，重新生成是新任务。
func (s TaskStatus) Terminal() bool {
	return s == TaskStatusSucceeded || s == TaskStatusCanceled
}

// NodeStatus 是八节点中一个节点的状态。
type NodeStatus string

const (
	NodeStatusPending       NodeStatus = "pending"
	NodeStatusRunning       NodeStatus = "running"
	NodeStatusWaitingReview NodeStatus = "waiting_review"
	NodeStatusDone          NodeStatus = "done"
	NodeStatusSkipped       NodeStatus = "skipped"
	NodeStatusReused        NodeStatus = "reused"
	NodeStatusFailed        NodeStatus = "failed"
)

// Valid 报告 s 是否是契约定义过的 Node 状态。
func (s NodeStatus) Valid() bool {
	switch s {
	case NodeStatusPending, NodeStatusRunning, NodeStatusWaitingReview,
		NodeStatusDone, NodeStatusSkipped, NodeStatusReused, NodeStatusFailed:
		return true
	default:
		return false
	}
}

// Terminal 报告 s 是否为节点终态。
func (s NodeStatus) Terminal() bool {
	switch s {
	case NodeStatusDone, NodeStatusSkipped, NodeStatusReused, NodeStatusFailed:
		return true
	default:
		return false
	}
}

// StepStatus 是任务步骤/尝试的状态。
type StepStatus string

const (
	StepStatusPending     StepStatus = "PENDING"
	StepStatusRunning     StepStatus = "RUNNING"
	StepStatusCommitting  StepStatus = "COMMITTING"
	StepStatusSucceeded   StepStatus = "SUCCEEDED"
	StepStatusFailed      StepStatus = "FAILED"
	StepStatusCanceled    StepStatus = "CANCELED"
	StepStatusInterrupted StepStatus = "INTERRUPTED"
)

// Valid 报告 s 是否是契约定义过的步骤状态。
func (s StepStatus) Valid() bool {
	switch s {
	case StepStatusPending, StepStatusRunning, StepStatusCommitting,
		StepStatusSucceeded, StepStatusFailed, StepStatusCanceled, StepStatusInterrupted:
		return true
	default:
		return false
	}
}

// Terminal 报告 s 是否为步骤终态。
func (s StepStatus) Terminal() bool {
	switch s {
	case StepStatusSucceeded, StepStatusFailed, StepStatusCanceled, StepStatusInterrupted:
		return true
	default:
		return false
	}
}

// PipelineNodeKey 是八节点流程的稳定键。
type PipelineNodeKey string

const (
	NodeKeyImport    PipelineNodeKey = "01_import"
	NodeKeyAnalyze   PipelineNodeKey = "02_analyze"
	NodeKeySeparate  PipelineNodeKey = "03_separate"
	NodeKeyExtract   PipelineNodeKey = "04_extract"
	NodeKeyErase     PipelineNodeKey = "05_erase"
	NodeKeyTranslate PipelineNodeKey = "06_translate"
	NodeKeyClone     PipelineNodeKey = "07_clone"
	NodeKeyMix       PipelineNodeKey = "08_mix"
)

// Valid 报告 k 是否是契约定义过的节点键。
func (k PipelineNodeKey) Valid() bool {
	switch k {
	case NodeKeyImport, NodeKeyAnalyze, NodeKeySeparate, NodeKeyExtract,
		NodeKeyErase, NodeKeyTranslate, NodeKeyClone, NodeKeyMix:
		return true
	default:
		return false
	}
}

// ReviewType 是人工审核门槛的类型。
type ReviewType string

const (
	ReviewTypeTranslation ReviewType = "translation"
	ReviewTypeDelivery    ReviewType = "delivery"
)

// Valid 报告 t 是否是契约定义过的审核类型。
func (t ReviewType) Valid() bool { return t == ReviewTypeTranslation || t == ReviewTypeDelivery }

// ReviewStatus 是审核门槛状态。
type ReviewStatus string

const (
	ReviewStatusPending  ReviewStatus = "pending"
	ReviewStatusApproved ReviewStatus = "approved"
)

// Valid 报告 s 是否是契约定义过的审核状态。
func (s ReviewStatus) Valid() bool {
	return s == ReviewStatusPending || s == ReviewStatusApproved
}

// ReviewBindingKind 是审核当前绑定来源类型（ReviewCurrentBinding.source_kind）。
type ReviewBindingKind string

const (
	ReviewBindingTranslationRevision ReviewBindingKind = "translation_revision"
	ReviewBindingDeliveryRevision    ReviewBindingKind = "delivery_revision"
	ReviewBindingDeliveryMix         ReviewBindingKind = "delivery_mix"
)

// Valid 报告 k 是否是契约定义过的绑定来源类型。
func (k ReviewBindingKind) Valid() bool {
	switch k {
	case ReviewBindingTranslationRevision, ReviewBindingDeliveryRevision, ReviewBindingDeliveryMix:
		return true
	default:
		return false
	}
}

// TaskKind 是可调度任务的类型。
type TaskKind string

const (
	TaskKindNode    TaskKind = "node"
	TaskKindSegment TaskKind = "segment"
	TaskKindExport  TaskKind = "export"
)

// Valid 报告 k 是否是契约定义过的任务类型。
func (k TaskKind) Valid() bool {
	return k == TaskKindNode || k == TaskKindSegment || k == TaskKindExport
}

// AssetType 是媒体资产类型。
type AssetType string

const (
	AssetTypeVideo      AssetType = "video"
	AssetTypeAudio      AssetType = "audio"
	AssetTypeSubtitle   AssetType = "subtitle"
	AssetTypeAudioTrack AssetType = "audio_track"
	AssetTypeImage      AssetType = "image"
	AssetTypeArchive    AssetType = "archive"
	AssetTypeOther      AssetType = "other"
)

// Valid 报告 t 是否是契约定义过的资产类型。
func (t AssetType) Valid() bool {
	switch t {
	case AssetTypeVideo, AssetTypeAudio, AssetTypeSubtitle, AssetTypeAudioTrack,
		AssetTypeImage, AssetTypeArchive, AssetTypeOther:
		return true
	default:
		return false
	}
}

// AssetRole 是产物语义角色（可选；旧资产为空，按 AssetType 降级）。
type AssetRole string

const (
	AssetRoleOriginal           AssetRole = "original"
	AssetRoleVoice              AssetRole = "voice"
	AssetRoleAmbience           AssetRole = "ambience"
	AssetRoleCleanVideo         AssetRole = "clean_video"
	AssetRoleSourceSubtitle     AssetRole = "source_subtitle"
	AssetRoleTranslatedSubtitle AssetRole = "translated_subtitle"
	AssetRoleDubAudio           AssetRole = "dub_audio"
	AssetRoleTranslatedVideo    AssetRole = "translated_video"
	AssetRoleFinalVideo         AssetRole = "final_video"
)

// Valid 报告 r 是否是契约定义过的资产角色；空值表示未标注。
func (r AssetRole) Valid() bool {
	switch r {
	case "", AssetRoleOriginal, AssetRoleVoice, AssetRoleAmbience, AssetRoleCleanVideo,
		AssetRoleSourceSubtitle, AssetRoleTranslatedSubtitle, AssetRoleDubAudio,
		AssetRoleTranslatedVideo, AssetRoleFinalVideo:
		return true
	default:
		return false
	}
}

// AssetStatus 是资产可用状态。
type AssetStatus string

const (
	AssetStatusImporting AssetStatus = "importing"
	AssetStatusReady     AssetStatus = "ready"
	AssetStatusMissing   AssetStatus = "missing"
	AssetStatusCorrupt   AssetStatus = "corrupt"
)

// Valid 报告 s 是否是契约定义过的资产状态。
func (s AssetStatus) Valid() bool {
	switch s {
	case AssetStatusImporting, AssetStatusReady, AssetStatusMissing, AssetStatusCorrupt:
		return true
	default:
		return false
	}
}

// TrackType 是字幕轨类型。
type TrackType string

const (
	TrackTypeTranscription TrackType = "transcription"
	TrackTypeTranslation   TrackType = "translation"
	TrackTypeCorrection    TrackType = "correction"
)

// Valid 报告 t 是否是契约定义过的字幕轨类型。
func (t TrackType) Valid() bool {
	return t == TrackTypeTranscription || t == TrackTypeTranslation || t == TrackTypeCorrection
}

// AuthorType 是 revision 的作者类型。
type AuthorType string

const (
	AuthorTypeSystem AuthorType = "system"
	AuthorTypeUser   AuthorType = "user"
)

// Valid 报告 t 是否是契约定义过的作者类型。
func (t AuthorType) Valid() bool { return t == AuthorTypeSystem || t == AuthorTypeUser }

// CapabilitySlotKey 是能力槽位键（六类模型 + OCR 辅助 + FFmpeg）。
type CapabilitySlotKey string

const (
	CapabilitySlotASR           CapabilitySlotKey = "asr"
	CapabilitySlotTranslate     CapabilitySlotKey = "translate"
	CapabilitySlotTTS           CapabilitySlotKey = "tts"
	CapabilitySlotVoiceClone    CapabilitySlotKey = "voice-clone"
	CapabilitySlotSubtitleErase CapabilitySlotKey = "subtitle-erase"
	CapabilitySlotSeparation    CapabilitySlotKey = "separation"
	CapabilitySlotOCR           CapabilitySlotKey = "ocr"
	CapabilitySlotFFmpeg        CapabilitySlotKey = "ffmpeg"
)

// Valid 报告 k 是否是契约定义过的能力槽位。
func (k CapabilitySlotKey) Valid() bool {
	switch k {
	case CapabilitySlotASR, CapabilitySlotTranslate, CapabilitySlotTTS,
		CapabilitySlotVoiceClone, CapabilitySlotSubtitleErase, CapabilitySlotSeparation,
		CapabilitySlotOCR, CapabilitySlotFFmpeg:
		return true
	default:
		return false
	}
}

// ModelSlotKey 是可配置的六类模型槽位。
type ModelSlotKey string

const (
	ModelSlotASR           ModelSlotKey = "asr"
	ModelSlotTranslate     ModelSlotKey = "translate"
	ModelSlotTTS           ModelSlotKey = "tts"
	ModelSlotVoiceClone    ModelSlotKey = "voice-clone"
	ModelSlotSubtitleErase ModelSlotKey = "subtitle-erase"
	ModelSlotSeparation    ModelSlotKey = "separation"
)

// Valid 报告 k 是否是契约定义过的模型槽位。
func (k ModelSlotKey) Valid() bool {
	switch k {
	case ModelSlotASR, ModelSlotTranslate, ModelSlotTTS,
		ModelSlotVoiceClone, ModelSlotSubtitleErase, ModelSlotSeparation:
		return true
	default:
		return false
	}
}

// SlotState 是能力状态机的取值（docs/initialization-and-task-lifecycle.md §1）。
type SlotState string

const (
	SlotStateUnknown         SlotState = "UNKNOWN"
	SlotStateUnsupported     SlotState = "UNSUPPORTED"
	SlotStateUnverified      SlotState = "UNVERIFIED"
	SlotStateNeedsSetup      SlotState = "NEEDS_SETUP"
	SlotStateInstalling      SlotState = "INSTALLING"
	SlotStateVerifying       SlotState = "VERIFYING"
	SlotStateReady           SlotState = "READY"
	SlotStateNeedsAction     SlotState = "NEEDS_ACTION"
	SlotStateRestartRequired SlotState = "RESTART_REQUIRED"
	SlotStateFailed          SlotState = "FAILED"
	SlotStateDegraded        SlotState = "DEGRADED"
)

// Valid 报告 s 是否是契约定义过的能力状态。
func (s SlotState) Valid() bool {
	switch s {
	case SlotStateUnknown, SlotStateUnsupported, SlotStateUnverified,
		SlotStateNeedsSetup, SlotStateInstalling, SlotStateVerifying, SlotStateReady,
		SlotStateNeedsAction, SlotStateRestartRequired, SlotStateFailed, SlotStateDegraded:
		return true
	default:
		return false
	}
}

// InstallationPhase 是安装记录阶段。
type InstallationPhase string

const (
	InstallationPhasePlanning    InstallationPhase = "planning"
	InstallationPhaseDownloading InstallationPhase = "downloading"
	InstallationPhaseVerifying   InstallationPhase = "verifying"
	InstallationPhaseInstalling  InstallationPhase = "installing"
	InstallationPhaseDone        InstallationPhase = "done"
	InstallationPhaseFailed      InstallationPhase = "failed"
	InstallationPhaseCanceled    InstallationPhase = "canceled"
)

// Valid 报告 p 是否是契约定义过的安装阶段。
func (p InstallationPhase) Valid() bool {
	switch p {
	case InstallationPhasePlanning, InstallationPhaseDownloading,
		InstallationPhaseVerifying, InstallationPhaseInstalling,
		InstallationPhaseDone, InstallationPhaseFailed, InstallationPhaseCanceled:
		return true
	default:
		return false
	}
}

// Terminal 报告 p 是否为安装终态。
func (p InstallationPhase) Terminal() bool {
	return p == InstallationPhaseDone || p == InstallationPhaseFailed || p == InstallationPhaseCanceled
}

// PreflightSeverity 是预检结论级别。
type PreflightSeverity string

const (
	PreflightPass    PreflightSeverity = "pass"
	PreflightWarning PreflightSeverity = "warning"
	PreflightBlocker PreflightSeverity = "blocker"
)

// Valid 报告 s 是否是契约定义过的预检级别。
func (s PreflightSeverity) Valid() bool {
	return s == PreflightPass || s == PreflightWarning || s == PreflightBlocker
}

// ParamFieldType 是受控参数 Schema 的字段类型。
type ParamFieldType string

const (
	ParamFieldTypeString  ParamFieldType = "string"
	ParamFieldTypeNumber  ParamFieldType = "number"
	ParamFieldTypeBoolean ParamFieldType = "boolean"
	ParamFieldTypeEnum    ParamFieldType = "enum"
)

// Valid 报告 t 是否是契约定义过的参数字段类型。
func (t ParamFieldType) Valid() bool {
	switch t {
	case ParamFieldTypeString, ParamFieldTypeNumber, ParamFieldTypeBoolean, ParamFieldTypeEnum:
		return true
	default:
		return false
	}
}

// ExtractMode 是节点 04 的提取方式。
type ExtractMode string

const (
	ExtractModeASR    ExtractMode = "asr"
	ExtractModeOCR    ExtractMode = "ocr"
	ExtractModeASROCR ExtractMode = "asr_ocr"
)

// Valid 报告 m 是否是契约定义过的提取方式。
func (m ExtractMode) Valid() bool {
	return m == ExtractModeASR || m == ExtractModeOCR || m == ExtractModeASROCR
}

// EraseMode 是节点 05 的字幕擦除方式。
type EraseMode string

const (
	EraseModeDialogOnly   EraseMode = "dialog_only"
	EraseModeDialogAndArt EraseMode = "dialog_and_art"
)

// Valid 报告 m 是否是契约定义过的擦除方式。
func (m EraseMode) Valid() bool {
	return m == EraseModeDialogOnly || m == EraseModeDialogAndArt
}

// OCRQuality 是节点 04 OCR 的提取精度（standard/fine）。
type OCRQuality string

const (
	OCRQualityStandard OCRQuality = "standard"
	OCRQualityFine     OCRQuality = "fine"
)

func (q OCRQuality) Valid() bool {
	return q == OCRQualityStandard || q == OCRQualityFine
}

// OCRLanguage 是节点 04 OCR 的语言（契约 ExtractOptions.ocr_language，仅四种）。
type OCRLanguage string

const (
	OCRLanguageChinese            OCRLanguage = "chinese"
	OCRLanguageChineseTraditional OCRLanguage = "chinese_traditional"
	OCRLanguageEnglish            OCRLanguage = "english"
	OCRLanguageJapanese           OCRLanguage = "japanese"
)

// Valid 报告 l 是否是契约定义过的 OCR 语言。
func (l OCRLanguage) Valid() bool {
	switch l {
	case OCRLanguageChinese, OCRLanguageChineseTraditional, OCRLanguageEnglish, OCRLanguageJapanese:
		return true
	default:
		return false
	}
}

// SubtitleAlign 是字幕水平对齐（契约 SubtitleStyle.align）。
type SubtitleAlign string

const (
	SubtitleAlignLeft   SubtitleAlign = "left"
	SubtitleAlignCenter SubtitleAlign = "center"
	SubtitleAlignRight  SubtitleAlign = "right"
)

// Valid 报告 a 是否是契约定义过的字幕对齐方式。
func (a SubtitleAlign) Valid() bool {
	return a == SubtitleAlignLeft || a == SubtitleAlignCenter || a == SubtitleAlignRight
}

// EraseBlockSource 是擦除块使用的画面来源（契约 EraseBlock.source）。
type EraseBlockSource string

const (
	EraseBlockSourceClean  EraseBlockSource = "clean"
	EraseBlockSourceSource EraseBlockSource = "source"
)

// Valid 报告 s 是否是契约定义过的擦除块来源；空值表示默认 clean。
func (s EraseBlockSource) Valid() bool {
	return s == "" || s == EraseBlockSourceClean || s == EraseBlockSourceSource
}

// CommitStatus 是产物提交登记状态（内部状态，不属于对外契约枚举）。
type CommitStatus string

const (
	CommitStatusPending   CommitStatus = "pending"
	CommitStatusCommitted CommitStatus = "committed"
	CommitStatusFailed    CommitStatus = "failed"
)

// Valid 报告 s 是否是合法提交状态。
func (s CommitStatus) Valid() bool {
	return s == CommitStatusPending || s == CommitStatusCommitted || s == CommitStatusFailed
}

// ExportKind 是产品导出类型。
type ExportKind string

const (
	ExportKindVideo    ExportKind = "video"
	ExportKindAudio    ExportKind = "audio"
	ExportKindSubtitle ExportKind = "subtitle"
)

// Valid 报告 k 是否是契约定义过的导出类型。
func (k ExportKind) Valid() bool {
	return k == ExportKindVideo || k == ExportKindAudio || k == ExportKindSubtitle
}

// ExportFormat 是实际编码/容器格式。
type ExportFormat string

const (
	ExportFormatMP4H264 ExportFormat = "mp4_h264"
	ExportFormatSRT     ExportFormat = "srt"
	ExportFormatASS     ExportFormat = "ass"
	ExportFormatWAV     ExportFormat = "wav"
	ExportFormatMP3     ExportFormat = "mp3"
)

// Valid 报告 f 是否是契约定义过的导出格式。
func (f ExportFormat) Valid() bool {
	switch f {
	case ExportFormatMP4H264, ExportFormatSRT, ExportFormatASS, ExportFormatWAV, ExportFormatMP3:
		return true
	default:
		return false
	}
}
