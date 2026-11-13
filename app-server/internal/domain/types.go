package domain

// 领域类型定义。
//
// 字段与 contracts/openapi.yaml（v0.2）一一对应；JSON 标签使用契约的 snake_case。
// 不在契约中暴露、但本地库需要保存的字段标记为 json:"-"。
// 约定：ID 是不透明字符串；时间一律 UTC；媒体时间整数毫秒；大小字节。

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// 基础标量。
type (
	// ID 是不透明标识；不做格式假设（不解析成整数、不用数组下标）。
	ID = string
	// Millis 是媒体时间，整数毫秒。
	Millis = int64
	// Bytes 是字节数。
	Bytes = int64
)

// ErrorCode 是安全错误码（contracts/openapi.yaml ErrorCode）。
type ErrorCode string

const (
	ErrorCodeInvalidRequest         ErrorCode = "INVALID_REQUEST"
	ErrorCodeLocalAuthRequired      ErrorCode = "LOCAL_AUTH_REQUIRED"
	ErrorCodeLocalAccessDenied      ErrorCode = "LOCAL_ACCESS_DENIED"
	ErrorCodeRevisionConflict       ErrorCode = "REVISION_CONFLICT"
	ErrorCodePreflightStale         ErrorCode = "PREFLIGHT_STALE"
	ErrorCodeIdempotencyConflict    ErrorCode = "IDEMPOTENCY_CONFLICT"
	ErrorCodeModelNotInstalled      ErrorCode = "MODEL_NOT_INSTALLED"
	ErrorCodeRuntimeIncompatible    ErrorCode = "RUNTIME_INCOMPATIBLE"
	ErrorCodeCapabilityUnverified   ErrorCode = "CAPABILITY_UNVERIFIED"
	ErrorCodeInsufficientDisk       ErrorCode = "INSUFFICIENT_DISK"
	ErrorCodeCoreNotReady           ErrorCode = "CORE_NOT_READY"
	ErrorCodePersistenceUnavailable ErrorCode = "PERSISTENCE_UNAVAILABLE"
	ErrorCodeWorkerExited           ErrorCode = "WORKER_EXITED"
	ErrorCodeResourceExhausted      ErrorCode = "RESOURCE_EXHAUSTED"
	ErrorCodeEventCursorExpired     ErrorCode = "EVENT_CURSOR_EXPIRED"
	ErrorCodeNotFound               ErrorCode = "NOT_FOUND"
	ErrorCodeReviewRequired         ErrorCode = "REVIEW_REQUIRED"
)

// Valid 报告 c 是否是契约定义过的错误码。
func (c ErrorCode) Valid() bool {
	switch c {
	case ErrorCodeInvalidRequest, ErrorCodeLocalAuthRequired, ErrorCodeLocalAccessDenied,
		ErrorCodeRevisionConflict, ErrorCodePreflightStale, ErrorCodeIdempotencyConflict,
		ErrorCodeModelNotInstalled, ErrorCodeRuntimeIncompatible,
		ErrorCodeCapabilityUnverified, ErrorCodeInsufficientDisk, ErrorCodeCoreNotReady,
		ErrorCodePersistenceUnavailable, ErrorCodeWorkerExited, ErrorCodeResourceExhausted,
		ErrorCodeEventCursorExpired, ErrorCodeNotFound, ErrorCodeReviewRequired:
		return true
	default:
		return false
	}
}

// ErrorInfo 是对外安全错误信息（不含原始输出）。
type ErrorInfo struct {
	Code       ErrorCode `json:"code"`
	MessageKey string    `json:"message_key"`
	Retryable  bool      `json:"retryable"`
	ActionIDs  []ID      `json:"action_ids"`
}

// Error 是可比较的领域错误，实现 error。
//
// 哨兵错误（ErrInvalidRequest 等）通过 errors.Is 匹配；
// 包装时使用 fmt.Errorf("...: %w", ErrXxx) 保留语义。
type Error struct {
	Code       ErrorCode
	MessageKey string
	Retryable  bool
	ActionIDs  []ID
}

// Error 实现 error。
func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.MessageKey)
}

// Info 返回可安全对外暴露的错误信息；action_ids 至少是空数组（契约要求数组）。
func (e *Error) Info() ErrorInfo {
	actionIDs := e.ActionIDs
	if actionIDs == nil {
		actionIDs = []ID{}
	}
	return ErrorInfo{
		Code:       e.Code,
		MessageKey: e.MessageKey,
		Retryable:  e.Retryable,
		ActionIDs:  actionIDs,
	}
}

// 领域哨兵错误。错误码语义见 docs/local-api-contract.md §4。
var (
	// ErrInvalidRequest 表示参数/状态输入不合法（INVALID_REQUEST）。
	ErrInvalidRequest = &Error{Code: ErrorCodeInvalidRequest, MessageKey: "error.invalid_request"}
	// ErrInvalidTransition 表示状态机不允许该转换（INVALID_REQUEST）。
	ErrInvalidTransition = &Error{Code: ErrorCodeInvalidRequest, MessageKey: "error.invalid_transition"}
	// ErrRevisionConflict 表示 revision 摘要/基准不一致（REVISION_CONFLICT）。
	ErrRevisionConflict = &Error{Code: ErrorCodeRevisionConflict, MessageKey: "error.revision_conflict"}
	// ErrStateConflict 表示乐观并发状态版本不一致（REVISION_CONFLICT 语义）。
	ErrStateConflict = &Error{Code: ErrorCodeRevisionConflict, MessageKey: "error.state_conflict"}
	// ErrIdempotencyConflict 表示同一幂等键对应不同请求摘要（IDEMPOTENCY_CONFLICT）。
	ErrIdempotencyConflict = &Error{Code: ErrorCodeIdempotencyConflict, MessageKey: "error.idempotency_conflict"}
	// ErrNotFound 表示资源不存在（NOT_FOUND）。
	ErrNotFound = &Error{Code: ErrorCodeNotFound, MessageKey: "error.not_found"}
	// ErrEventCursorExpired 表示事件历史已被保留策略裁剪（EVENT_CURSOR_EXPIRED）。
	ErrEventCursorExpired = &Error{Code: ErrorCodeEventCursorExpired, MessageKey: "error.event_cursor_expired"}
	// ErrPreflightStale 表示提交时预检条件已变化（PREFLIGHT_STALE）。
	ErrPreflightStale = &Error{Code: ErrorCodePreflightStale, MessageKey: "error.preflight_stale"}
	// ErrModelNotInstalled 表示所需模型/运行时尚未安装（MODEL_NOT_INSTALLED）。
	ErrModelNotInstalled = &Error{Code: ErrorCodeModelNotInstalled, MessageKey: "error.model_not_installed"}
	// ErrRuntimeIncompatible 表示运行时/设备不兼容（RUNTIME_INCOMPATIBLE）。
	ErrRuntimeIncompatible = &Error{Code: ErrorCodeRuntimeIncompatible, MessageKey: "error.runtime_incompatible"}
	// ErrCapabilityUnverified 表示后端适配数据缺失或能力未验证（CAPABILITY_UNVERIFIED）。
	ErrCapabilityUnverified = &Error{Code: ErrorCodeCapabilityUnverified, MessageKey: "error.capability_unverified"}
	// ErrInsufficientDisk 表示所需文件系统空间不足（INSUFFICIENT_DISK）。
	ErrInsufficientDisk = &Error{Code: ErrorCodeInsufficientDisk, MessageKey: "error.insufficient_disk"}
	// ErrCoreNotReady 表示核心尚未就绪（CORE_NOT_READY）。
	ErrCoreNotReady = &Error{Code: ErrorCodeCoreNotReady, MessageKey: "error.core_not_ready"}
	// ErrPersistenceUnavailable 表示持久化不可用（PERSISTENCE_UNAVAILABLE）。
	ErrPersistenceUnavailable = &Error{Code: ErrorCodePersistenceUnavailable, MessageKey: "error.persistence_unavailable"}
	// ErrWorkerExited 表示已接受任务的执行器异常退出（WORKER_EXITED）。
	ErrWorkerExited = &Error{Code: ErrorCodeWorkerExited, MessageKey: "error.worker_exited"}
	// ErrResourceExhausted 表示资源槽/并发上限已满（RESOURCE_EXHAUSTED）。
	ErrResourceExhausted = &Error{Code: ErrorCodeResourceExhausted, MessageKey: "error.resource_exhausted"}
	// ErrLocalAuthRequired 表示缺少实例令牌（LOCAL_AUTH_REQUIRED）。
	ErrLocalAuthRequired = &Error{Code: ErrorCodeLocalAuthRequired, MessageKey: "error.local_auth_required"}
	// ErrLocalAccessDenied 表示令牌有效但来源/授权不符（LOCAL_ACCESS_DENIED）。
	ErrLocalAccessDenied = &Error{Code: ErrorCodeLocalAccessDenied, MessageKey: "error.local_access_denied"}
)

// CodeOf 提取出错错误的安全错误码；非领域错误返回 false。
func CodeOf(err error) (ErrorCode, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code, true
	}
	return "", false
}

// MediaMeta 是媒体元数据。
type MediaMeta struct {
	// Role 是节点适配器声明的音频职责（vocals/background），不根据文件名或顺序推断。
	Role        string `json:"role,omitempty"`
	Container   string `json:"container"`
	Codec       string `json:"codec,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	DurationMs  Millis `json:"duration_ms"`
	AudioTracks int    `json:"audio_tracks"`
	SampleRate  int    `json:"sample_rate,omitempty"`
}

// AssetLocation 是资产在某个文件系统位置上的登记（原件引用与应用副本分开标记）。
type AssetLocation struct {
	ID        ID
	AssetID   ID
	Path      string
	Source    string // external_reference / managed_copy
	Owner     string // user / app
	Available bool
	CreatedAt time.Time
}

// 资产位置来源与拥有者取值（内部约定，非对外契约枚举）。
const (
	LocationSourceExternalReference = "external_reference"
	LocationSourceManagedCopy       = "managed_copy"
	LocationSourceGenerated         = "generated"

	LocationOwnerUser = "user"
	LocationOwnerApp  = "app"
)

// Asset 是媒体资产。文件本身不进 SQLite；Path 仅用于展示，读取必须经 Core 授权。
type Asset struct {
	ID         ID          `json:"id"`
	ProjectID  ID          `json:"project_id"`
	Type       AssetType   `json:"type"`
	Role       AssetRole   `json:"role,omitempty"`
	Language   string      `json:"language,omitempty"`
	RunID      ID          `json:"run_id,omitempty"`
	Name       string      `json:"name"`
	Path       string      `json:"path,omitempty"`
	SizeBytes  Bytes       `json:"size_bytes"`
	SHA256     string      `json:"sha256,omitempty"`
	Meta       *MediaMeta  `json:"meta,omitempty"`
	Status     AssetStatus `json:"status"`
	ImportedAt time.Time   `json:"imported_at"`
}

// Segment 是一个稳定片段。ID 不使用数组下标；拆分/合并保留 Lineage。
type Segment struct {
	ID              ID     `json:"id"`
	StartMs         Millis `json:"start_ms"`
	EndMs           Millis `json:"end_ms"`
	Text            string `json:"text"`
	Speaker         string `json:"speaker,omitempty"`
	ContentRevision int    `json:"content_revision"`
	ContentDigest   string `json:"content_digest"`
	Lineage         []ID   `json:"lineage,omitempty"`
}

// SegmentInput 是保存字幕修订时的句段输入。
type SegmentInput struct {
	ID      ID     `json:"id,omitempty"`
	StartMs Millis `json:"start_ms"`
	EndMs   Millis `json:"end_ms"`
	Text    string `json:"text"`
	Speaker string `json:"speaker,omitempty"`
	Lineage []ID   `json:"lineage,omitempty"`
}

// SubtitleRevision 是轨道的一次不可变 revision。
type SubtitleRevision struct {
	ID               ID             `json:"id"`
	TrackID          ID             `json:"track_id"`
	ParentRevisionID ID             `json:"parent_revision_id,omitempty"`
	AuthorType       AuthorType     `json:"author_type"`
	Segments         []Segment      `json:"segments"`
	InputRevisionID  ID             `json:"input_revision_id,omitempty"`
	Style            *SubtitleStyle `json:"style,omitempty"`
	EraseBlocks      []EraseBlock   `json:"erase_blocks,omitempty"`
	RevisionDigest   string         `json:"revision_digest,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
}

// EraseBlock 是 revision 级擦除校正占位（provisional）。
//
// 只描述不可变 revision 中的几何与来源，不含运行期状态；05 真实擦除与
// 04 OCR 位置能力落地前不派发擦除任务。
type EraseBlock struct {
	ID        ID               `json:"id,omitempty"`
	SegmentID ID               `json:"segment_id,omitempty"`
	StartMs   Millis           `json:"start_ms"`
	EndMs     Millis           `json:"end_ms"`
	BBox      [4]int           `json:"bbox"`
	Source    EraseBlockSource `json:"source,omitempty"`
}

// SubtitleTrack 是一条字幕轨；CurrentRevisionID 做乐观并发检查。
type SubtitleTrack struct {
	ID                ID                 `json:"id"`
	ProjectID         ID                 `json:"project_id"`
	Language          string             `json:"language"`
	Type              TrackType          `json:"type"`
	CurrentRevisionID ID                 `json:"current_revision_id"`
	Revisions         []SubtitleRevision `json:"revisions"`
	CreatedAt         time.Time          `json:"-"`
	UpdatedAt         time.Time          `json:"-"`
}

// ReleaseRef 是不可变模型/运行时发布引用。
type ReleaseRef struct {
	ReleaseID string `json:"release_id"`
	Version   string `json:"version"`
	Digest    string `json:"digest"`
	Channel   string `json:"channel,omitempty"`
}

// SubtitleOutlineStyle 是字幕描边样式。
type SubtitleOutlineStyle struct {
	Color string  `json:"color,omitempty"`
	Width float64 `json:"width,omitempty"`
}

// SubtitleShadowStyle 是字幕阴影样式。
type SubtitleShadowStyle struct {
	Color string  `json:"color,omitempty"`
	Blur  float64 `json:"blur,omitempty"`
	X     float64 `json:"x,omitempty"`
	Y     float64 `json:"y,omitempty"`
}

// SubtitleStyle 是节点 08 的受控字幕样式（字段结构以契约为准）。
type SubtitleStyle struct {
	X                   float64               `json:"x"`
	Y                   float64               `json:"y"`
	Font                string                `json:"font,omitempty"`
	FontSize            float64               `json:"font_size,omitempty"`
	LineSpacing         float64               `json:"line_spacing,omitempty"`
	Bold                bool                  `json:"bold,omitempty"`
	Align               SubtitleAlign         `json:"align,omitempty"`
	Outline             *SubtitleOutlineStyle `json:"outline,omitempty"`
	Shadow              *SubtitleShadowStyle  `json:"shadow,omitempty"`
	Background          string                `json:"background,omitempty"`
	MaxTranslationLines int                   `json:"max_translation_lines,omitempty"`
}

// ExtractOptions 是节点 04 的提取配置。
type ExtractOptions struct {
	Mode        ExtractMode `json:"mode"`
	OCRLanguage OCRLanguage `json:"ocr_language,omitempty"`
	OCRQuality  OCRQuality  `json:"ocr_quality,omitempty"`
}

// TranslationTrackRef 是专属链路在创建 Run 时冻结的导入译稿轨/revision。
type TranslationTrackRef struct {
	TrackID    ID `json:"track_id"`
	RevisionID ID `json:"revision_id"`
}

// ExecutionConfig 是 Run 创建时冻结的已准入版本、设备与受控参数。
// ContextRadius 由已准入适配器声明；0 仅表示没有声明邻接输入。
type ExecutionConfig struct {
	ModelLock     ReleaseRef     `json:"model_lock"`
	DeviceID      string         `json:"device_id"`
	Params        map[string]any `json:"params"`
	ContextRadius int            `json:"context_radius"`
}

// RunConfig 是创建 Run 时的冻结配置；不直接出现在 WorkflowRun 响应中。
type RunConfig struct {
	InputRevisionIDs        []ID
	Extract                 *ExtractOptions
	ReuseAudioTrackID       ID
	ReuseSubtitleTrackID    ID
	ReuseSubtitleRevisionID ID
	EraseMode               EraseMode
	ValidateTimestamps      bool
	AdjustDuration          bool
	Bilingual               bool
	SubtitleStyle           *SubtitleStyle
	// ImportedTranslationTracks 只对 custom_pipeline 冻结；nil 表示旧 Run（走最新轨兼容）。
	ImportedTranslationTracks map[string]TranslationTrackRef
	// Execution 为 nil 的旧 Run 只读兼容；不得据此为新任务解析当前版本。
	Execution map[CapabilitySlotKey]ExecutionConfig
}

// PipelineNodeState 是八节点中一个节点的执行状态。
type PipelineNodeState struct {
	Key      PipelineNodeKey `json:"key"`
	Status   NodeStatus      `json:"status"`
	Progress int             `json:"progress"`
	Detail   string          `json:"detail,omitempty"`
}

// WorkflowRun 是一次译制/专属链路的版本化业务计划。
type WorkflowRun struct {
	ID             ID        `json:"id"`
	ProjectID      ID        `json:"project_id"`
	Name           string    `json:"name"`
	Template       string    `json:"template"`
	ModeType       string    `json:"mode_type,omitempty"`
	Status         RunStatus `json:"status"`
	StateVersion   int64     `json:"-"`
	SourceLanguage string    `json:"source_language"`
	Languages      []string  `json:"languages"`
	// InputAssetIDs 是创建时冻结的输入资产 ID，派生自 Config.InputRevisionIDs；
	// 只读响应字段，不单独持久化（见 contracts/openapi.yaml WorkflowRun）。
	InputAssetIDs []ID                `json:"input_asset_ids"`
	Nodes         []PipelineNodeState `json:"nodes"`
	Params        *RunParams          `json:"params,omitempty"`
	Config        RunConfig           `json:"-"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

// RunParams 是 Run 创建参数的只读回显；由冻结 Config 派生，不单独持久化。
type RunParams struct {
	EraseMode          EraseMode      `json:"erase_mode,omitempty"`
	ValidateTimestamps bool           `json:"validate_timestamps"`
	AdjustDuration     bool           `json:"adjust_duration"`
	Bilingual          bool           `json:"bilingual"`
	ExtractMode        ExtractMode    `json:"extract_mode,omitempty"`
	OCRLanguage        OCRLanguage    `json:"ocr_language,omitempty"`
	ASRLanguage        string         `json:"asr_language,omitempty"`
	SubtitleStyle      *SubtitleStyle `json:"subtitle_style,omitempty"`
}

// TaskStep 是任务内的步骤状态。
type TaskStep struct {
	StepID    string     `json:"step_id"`
	Status    StepStatus `json:"status"`
	AttemptNo int        `json:"attempt_no,omitempty"`
	Epoch     int64      `json:"epoch,omitempty"`
}

// Task 是可调度单元（节点/句段/导出）。
type Task struct {
	ID               ID              `json:"id"`
	ProjectID        ID              `json:"project_id"`
	RunID            ID              `json:"run_id,omitempty"`
	Kind             TaskKind        `json:"kind"`
	NodeKey          PipelineNodeKey `json:"node_key,omitempty"`
	SegmentID        ID              `json:"segment_id,omitempty"`
	Language         string          `json:"language,omitempty"`
	Label            string          `json:"label,omitempty"`
	Status           TaskStatus      `json:"status"`
	Progress         int             `json:"progress"`
	Steps            []TaskStep      `json:"steps,omitempty"`
	ArtifactIDs      []ID            `json:"artifact_ids"`
	Error            *ErrorInfo      `json:"error,omitempty"`
	StateVersion     int64           `json:"-"`
	InputRevisionIDs []ID            `json:"-"`
	InputDigest      string          `json:"-"`
	Params           map[string]any  `json:"-"`
	ModelLock        *ReleaseRef     `json:"-"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// Attempt 是一次具体执行；重试产生新的单调递增 attempt 编号。
type Attempt struct {
	ID         ID         `json:"id"`
	TaskID     ID         `json:"task_id"`
	StepID     string     `json:"step_id"`
	AttemptNo  int        `json:"attempt_no"`
	Epoch      int64      `json:"epoch"`
	ExecutorID string     `json:"executor_id,omitempty"`
	Status     StepStatus `json:"status"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// ArtifactCommit 是控制面确认后的产物登记；Status 为内部字段。
type ArtifactCommit struct {
	ID          ID           `json:"id"`
	AttemptID   ID           `json:"attempt_id"`
	AssetIDs    []ID         `json:"asset_ids"`
	Status      CommitStatus `json:"-"`
	CommittedAt *time.Time   `json:"committed_at,omitempty"`
}

// ReviewCurrentBinding 是 Core 由当前持久事实派生的只读审核绑定
// （contracts/openapi.yaml ReviewCurrentBinding）；不是独立放行授权。
type ReviewCurrentBinding struct {
	Language   string            `json:"language"`
	SourceKind ReviewBindingKind `json:"source_kind"`
	SourceID   ID                `json:"source_id"`
}

// ReviewGate 是绑定精确 revision 摘要的人工确认记录。
type ReviewGate struct {
	ID             ID           `json:"id"`
	RunID          ID           `json:"run_id"`
	Type           ReviewType   `json:"type"`
	Language       string       `json:"language,omitempty"`
	RevisionDigest string       `json:"revision_digest"`
	Status         ReviewStatus `json:"status"`
	ConfirmedAt    *time.Time   `json:"confirmed_at,omitempty"`
	CreatedAt      *time.Time   `json:"created_at,omitempty"`
	// CurrentBindings 是列表/详情的只读投影，不持久化；nil 表示该响应未投影
	// （批准回执），非 nil 空切片必须序列化为 []，避免客户端把缺失当未知。
	CurrentBindings []ReviewCurrentBinding `json:"current_bindings,omitempty"`
}

// MarshalJSON 在投影存在时始终输出 current_bindings 数组（含空数组）；
// 未投影的记录（批准响应/幂等回执）省略可选字段，而不是输出 null。
func (g ReviewGate) MarshalJSON() ([]byte, error) {
	type plain ReviewGate
	if g.CurrentBindings == nil {
		return json.Marshal(plain(g))
	}
	return json.Marshal(struct {
		plain
		CurrentBindings []ReviewCurrentBinding `json:"current_bindings"`
	}{plain: plain(g), CurrentBindings: g.CurrentBindings})
}

// AudioCandidate 是句级配音候选（文件不可变，不覆盖旧候选）。
type AudioCandidate struct {
	ID            ID        `json:"id"`
	ProjectID     ID        `json:"-"`
	SegmentID     ID        `json:"segment_id"`
	Language      string    `json:"language"`
	Revision      int       `json:"revision"`
	ModelVersion  string    `json:"model_version"`
	AudioAssetID  ID        `json:"audio_asset_id"`
	IsRecommended bool      `json:"is_recommended"`
	CreatedAt     time.Time `json:"created_at"`
}

// CandidateSelection 是句段某个语言的选用关系。
type CandidateSelection struct {
	SegmentID   ID        `json:"segment_id"`
	Language    string    `json:"language"`
	CandidateID ID        `json:"candidate_id"`
	Revision    int       `json:"revision"`
	SelectedAt  time.Time `json:"selected_at"`
}

// InstallationFile 是安装实体的内部已验证文件清单条目（不暴露 URL/路径）。
// 它与下载清单的 rel_path/sha256/size 同形，用于复用/回滚前的最终文件校验。
type InstallationFile struct {
	RelPath string `json:"rel_path"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// Installation 是一次模型/运行时安装记录，也是实际物理安装的验证实体。
//
// Slot 保留发起槽位；CoveredSlots 是创建时冻结的同意覆盖，BoundSlots 是
// 仍引用该记录的槽位（含保留历史），CurrentSlots 是当前选中该 Done 实体的
// 槽位。三个数组在列表/详情/写响应中总是显式输出（含空数组）；旧 Core 缺失
// 字段才按其单 slot 语义展示，空数组绝不回退到发起槽位。
type Installation struct {
	ID            ID                  `json:"id"`
	Slot          CapabilitySlotKey   `json:"slot"`
	CoveredSlots  []CapabilitySlotKey `json:"covered_slots"`
	BoundSlots    []CapabilitySlotKey `json:"bound_slots"`
	CurrentSlots  []CapabilitySlotKey `json:"current_slots"`
	Phase         InstallationPhase   `json:"phase"`
	Progress      int                 `json:"progress"`
	VerifiedFiles int                 `json:"verified_files,omitempty"`
	Error         *ErrorInfo          `json:"error,omitempty"`
	Retryable     bool                `json:"retryable,omitempty"`
	PlanHash      string              `json:"plan_hash,omitempty"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
	PlanID        string              `json:"-"`
	Release       *ReleaseRef         `json:"-"`
	ProbePassed   bool                `json:"-"`
	PathOwner     string              `json:"-"`
	// StorageKey 是新共享布局的物理根名（完整 ReleaseRef 的 SHA-256）；
	// 空值表示旧版按槽位布局的历史记录，不伪造共享身份。
	StorageKey string `json:"-"`
	// Manifest 是创建时冻结的文件内容清单，用于复用/回滚的最终文件校验。
	Manifest []InstallationFile `json:"-"`
}

// MarshalJSON 总是输出 covered_slots/bound_slots/current_slots 数组（含空数组），
// 不用 null 表示“未知”；旧 Core 兼容由客户端按字段缺失处理。
func (i Installation) MarshalJSON() ([]byte, error) {
	type plain Installation
	covered := i.CoveredSlots
	if covered == nil {
		covered = []CapabilitySlotKey{}
	}
	bound := i.BoundSlots
	if bound == nil {
		bound = []CapabilitySlotKey{}
	}
	current := i.CurrentSlots
	if current == nil {
		current = []CapabilitySlotKey{}
	}
	return json.Marshal(struct {
		plain
		CoveredSlots []CapabilitySlotKey `json:"covered_slots"`
		BoundSlots   []CapabilitySlotKey `json:"bound_slots"`
		CurrentSlots []CapabilitySlotKey `json:"current_slots"`
	}{plain: plain(i), CoveredSlots: covered, BoundSlots: bound, CurrentSlots: current})
}

// ParamOption 是枚举参数的选项。
type ParamOption struct {
	Value    string `json:"value"`
	LabelKey string `json:"label_key"`
}

// ParamField 是受控参数 Schema 字段。
type ParamField struct {
	Key      string         `json:"key"`
	LabelKey string         `json:"label_key"`
	Type     ParamFieldType `json:"type"`
	Options  []ParamOption  `json:"options,omitempty"`
	Min      *float64       `json:"min,omitempty"`
	Max      *float64       `json:"max,omitempty"`
	Default  any            `json:"default,omitempty"`
}

// EngineOption 是槽位支持的本地引擎；目录由 Core 返回，客户端不写死。
type EngineOption struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Version   string    `json:"version,omitempty"`
	State     SlotState `json:"state"`
	Installed bool      `json:"installed"`
	Selected  bool      `json:"selected"`
	Device    string    `json:"device,omitempty"`
	Source    string    `json:"source,omitempty"`
	SizeBytes int64     `json:"size_bytes,omitempty"`
}

// CapabilityRuntimeState 是槽位的执行占用状态。
// running 表示有任务正在执行（模型已载入或正在载入），不表示权重常驻。
type CapabilityRuntimeState string

const (
	CapabilityRuntimeIdle    CapabilityRuntimeState = "idle"
	CapabilityRuntimeRunning CapabilityRuntimeState = "running"
)

// Capability 是能力目录条目（发布准入 + 本机状态 + 输入可执行性由上层组合）。
type Capability struct {
	Slot            CapabilitySlotKey      `json:"slot"`
	State           SlotState              `json:"state"`
	EngineName      string                 `json:"engine_name,omitempty"`
	Version         string                 `json:"version,omitempty"`
	Languages       []string               `json:"languages,omitempty"`
	Params          []ParamField           `json:"params,omitempty"`
	Engines         []EngineOption         `json:"engines,omitempty"`
	ReasonCode      string                 `json:"reason_code,omitempty"`
	ActionIDs       []ID                   `json:"action_ids,omitempty"`
	Progress        *int                   `json:"progress,omitempty"`
	RuntimeState    CapabilityRuntimeState `json:"runtime_state"`
	RunningTasks    int                    `json:"running_tasks"`
	UpdateAvailable bool                   `json:"update_available"`
	UpdateVersion   string                 `json:"update_version,omitempty"`
	UpdateNotes     string                 `json:"update_notes,omitempty"`
}

// Project 是本地项目；EditVersion 做乐观并发检查。
// Directory 是应用管理的项目目录绝对路径（派生字段，不持久化）。
type Project struct {
	ID            ID              `json:"id"`
	WorkspaceID   ID              `json:"workspace_id"`
	EpisodeID     string          `json:"episode_id,omitempty"`
	Name          string          `json:"name"`
	Directory     string          `json:"directory"`
	SchemaVersion int             `json:"schema_version"`
	EditVersion   int64           `json:"edit_version"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	LastOpenedAt  *time.Time      `json:"last_opened_at,omitempty"`
	Assets        []Asset         `json:"assets"`
	Tracks        []SubtitleTrack `json:"tracks"`
	Runs          []WorkflowRun   `json:"runs"`
}

// ProjectSummary 是项目列表用的轻量摘要。
type ProjectSummary struct {
	Seq             int64      `json:"seq"`
	ID              ID         `json:"id"`
	Name            string     `json:"name"`
	EditVersion     int64      `json:"edit_version"`
	EpisodeID       string     `json:"episode_id,omitempty"`
	ModeType        string     `json:"mode_type,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	LastOpenedAt    *time.Time `json:"last_opened_at,omitempty"`
	AssetCount      int        `json:"asset_count"`
	RunCount        int        `json:"run_count"`
	LatestRunStatus RunStatus  `json:"latest_run_status,omitempty"`
	TotalDurationMs Millis     `json:"total_duration_ms,omitempty"`
	// WaitingResource 表示最近一次 Run 的当前节点任务在等待执行器/资源；
	// Run 本身仍是 RUNNING，UI 需要区分“运行中”和“等待资源”。
	WaitingResource bool `json:"waiting_resource,omitempty"`
}

// ExportRecord 是导出任务记录。
//
// 契约只暴露 id/project_id/run_id/kind/format/status/output_path/created_at；
// 输入绑定与关联任务属于本地持久化事实，用 json:"-" 保存。
type ExportRecord struct {
	ID         ID           `json:"id"`
	ProjectID  ID           `json:"project_id"`
	RunID      ID           `json:"run_id,omitempty"`
	Kind       ExportKind   `json:"kind"`
	Format     ExportFormat `json:"format"`
	Status     TaskStatus   `json:"status"`
	OutputPath string       `json:"output_path,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`

	InputRevisionID  ID        `json:"-"`
	SourceRevisionID ID        `json:"-"`
	Bilingual        bool      `json:"-"`
	OutputDirGrant   string    `json:"-"`
	NamingTemplate   string    `json:"-"`
	TaskID           ID        `json:"-"`
	UpdatedAt        time.Time `json:"-"`
}

// DomainEvent 是工作区级持久事件信封（docs/local-api-contract.md §5）。
type DomainEvent struct {
	SchemaVersion int            `json:"schema_version"`
	Seq           int64          `json:"seq"`
	Type          string         `json:"type"`
	ProjectID     ID             `json:"project_id,omitempty"`
	TaskID        ID             `json:"task_id,omitempty"`
	StateVersion  int64          `json:"state_version,omitempty"`
	Data          map[string]any `json:"data"`
}

// ExecutionLogLine 是一行受控执行过程摘要；只用于界面回放，不是业务状态。
type ExecutionLogLine struct {
	Seq    int    `json:"seq"`
	Stream string `json:"stream"`
	Line   string `json:"line"`
	At     string `json:"at,omitempty"`
}

// ExecutionLog 是某次 attempt 的日志尾部；文件缺失时 Lines 为空。
type ExecutionLog struct {
	TaskID  ID                 `json:"task_id"`
	Attempt int                `json:"attempt"`
	Lines   []ExecutionLogLine `json:"lines"`
}

// PreflightIssue 是一条预检问题。
type PreflightIssue struct {
	Severity   PreflightSeverity `json:"severity"`
	Code       string            `json:"code"`
	MessageKey string            `json:"message_key"`
	ActionIDs  []ID              `json:"action_ids"`
}

// PreflightResult 是预检结论；blocker 阻止创建，warning 需要用户确认。
type PreflightResult struct {
	PlanID   string           `json:"plan_id,omitempty"`
	PlanHash string           `json:"plan_hash,omitempty"`
	Issues   []PreflightIssue `json:"issues"`
}

// HasBlocker 报告预检是否存在阻断项。
func (r PreflightResult) HasBlocker() bool {
	for _, issue := range r.Issues {
		if issue.Severity == PreflightBlocker {
			return true
		}
	}
	return false
}

// CanProceed 报告预检是否允许继续创建（无 blocker）。
func (r PreflightResult) CanProceed() bool { return !r.HasBlocker() }
