package domain

// 对外响应 DTO。
//
// 这些类型只承载 contracts/openapi.yaml（v0.2）中定义的结构，不含业务规则；
// 语义编排在 internal/service，HTTP 编解码在 internal/api。
// 字段名使用契约的 snake_case；可选字段用 omitempty，数组字段由调用方归一为 []。

import "time"

// SystemInfo 是 GET /api/v1/system 的响应。
type SystemInfo struct {
	AppID           string `json:"app_id"`
	AppVersion      string `json:"app_version,omitempty"`
	ProtocolVersion string `json:"protocol_version"`
	InstanceID      string `json:"instance_id"`
	WorkspaceID     string `json:"workspace_id"`
	ServerEpoch     int64  `json:"server_epoch"`
	CoreReady       bool   `json:"core_ready"`
}

// DeviceKind 是计算设备类型（FR-SET-40）。
type DeviceKind string

const (
	DeviceKindCPU  DeviceKind = "cpu"
	DeviceKindCUDA DeviceKind = "cuda"
	DeviceKindMPS  DeviceKind = "mps"
)

// Valid 报告 k 是否是契约定义过的设备类型。
func (k DeviceKind) Valid() bool {
	return k == DeviceKindCPU || k == DeviceKindCUDA || k == DeviceKindMPS
}

// DeviceInfo 是可用计算设备（FR-SET-40）。
type DeviceInfo struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Kind             DeviceKind `json:"kind"`
	Recommended      bool       `json:"recommended"`
	Selected         bool       `json:"selected"`
	MemoryTotalBytes *int64     `json:"memory_total_bytes,omitempty"`
	MemoryUsedBytes  *int64     `json:"memory_used_bytes,omitempty"`
	InUse            bool       `json:"in_use"`
	DriverVersion    string     `json:"driver_version,omitempty"`
}

// EnvironmentInfo 是本机执行环境快照（FR-SET-23/40）。
type EnvironmentInfo struct {
	Platform string             `json:"platform"`
	WSL      *WSLStatus         `json:"wsl,omitempty"`
	Devices  []DeviceInfo       `json:"devices"`
	Checks   []EnvironmentCheck `json:"checks"`
}

// WSLStatus 是 Windows 宿主上的 WSL 探测结果；其他平台省略。
type WSLStatus struct {
	Available     bool  `json:"available"`
	Version       int   `json:"version,omitempty"`
	CUDAAvailable *bool `json:"cuda_available,omitempty"`
}

// EnvironmentCheck 是只读环境检查项。
type EnvironmentCheck struct {
	Code       string            `json:"code"`
	Severity   PreflightSeverity `json:"severity"`
	MessageKey string            `json:"message_key"`
	ActionIDs  []ID              `json:"action_ids"`
}

// EngineConfig 是六槽位受控配置。
type EngineConfig struct {
	Slot      ModelSlotKey   `json:"slot"`
	DeviceID  string         `json:"device_id,omitempty"`
	Values    map[string]any `json:"values,omitempty"`
	UpdatedAt *time.Time     `json:"updated_at,omitempty"`
}

// LicenseSummary 是安装计划中的许可摘要。
type LicenseSummary struct {
	LicenseID   string   `json:"license_id"`
	LicenseName string   `json:"license_name"`
	NoticeKeys  []string `json:"notice_keys,omitempty"`
}

// InstallationPlan 是解析后的安装计划（绑定精确发布与同意范围）。
//
// CoveredSlots 来自已准入声明的完整覆盖范围，冻结进 plan_hash，并在用户
// 确认前完整展示；Slot 只保留为发起入口。ReuseInstallationID 指向同一精确
// 发布的进行中或已验证实体，加入/复用时不会启动第二份传输。
type InstallationPlan struct {
	PlanID              string              `json:"plan_id"`
	PlanHash            string              `json:"plan_hash"`
	Slot                CapabilitySlotKey   `json:"slot"`
	CoveredSlots        []CapabilitySlotKey `json:"covered_slots"`
	ReuseInstallationID string              `json:"reuse_installation_id,omitempty"`
	Release             ReleaseRef          `json:"release"`
	DownloadBytes       Bytes               `json:"download_bytes"`
	CacheReuseBytes     Bytes               `json:"cache_reuse_bytes"`
	InstallBytes        Bytes               `json:"install_bytes"`
	PeakStagingBytes    Bytes               `json:"peak_staging_bytes,omitempty"`
	Permissions         []string            `json:"permissions"`
	License             LicenseSummary      `json:"license"`
	RequiresRestart     bool                `json:"requires_restart,omitempty"`
}

// StorageUsage 是应用数据所在分区的四类占用（FR-SET-42）。
type StorageUsage struct {
	ModelsBytes  Bytes `json:"models_bytes"`
	MediaBytes   Bytes `json:"media_bytes"`
	CacheBytes   Bytes `json:"cache_bytes"`
	ExportsBytes Bytes `json:"exports_bytes"`
}

// StorageCleanupResult 是按类清理的结果。
type StorageCleanupResult struct {
	Category   StorageCategory `json:"category"`
	FreedBytes Bytes           `json:"freed_bytes"`
}

// StorageCategory 是存储类别。
type StorageCategory string

const (
	StorageCategoryModels  StorageCategory = "models"
	StorageCategoryMedia   StorageCategory = "media"
	StorageCategoryCache   StorageCategory = "cache"
	StorageCategoryExports StorageCategory = "exports"
)

// Valid 报告 c 是否是契约定义过的存储类别。
func (c StorageCategory) Valid() bool {
	switch c {
	case StorageCategoryModels, StorageCategoryMedia, StorageCategoryCache, StorageCategoryExports:
		return true
	default:
		return false
	}
}

// TaskList 是分页任务列表；列表不嵌入完整步骤历史。
type TaskList struct {
	Items      []Task `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// Snapshot 是恢复项目视图的单事务快照。
type Snapshot struct {
	ProjectID ID             `json:"project_id"`
	Project   ProjectSummary `json:"project"`
	Runs      []WorkflowRun  `json:"runs"`
	Tasks     []Task         `json:"tasks"`
	EventSeq  int64          `json:"event_seq"`
}

// EventCursor 是事件历史保留范围。
type EventCursor struct {
	EarliestSeq int64 `json:"earliest_seq"`
}

// LocalApiSettings 是第三方本机 API 设置（不包含明文令牌）。
type LocalApiSettings struct {
	Enabled          bool   `json:"enabled"`
	ConcurrencyLimit int    `json:"concurrency_limit"`
	TokenPresent     bool   `json:"token_present"`
	TokenMasked      string `json:"token_masked,omitempty"`
}

// LocalApiToken 是新签发的第三方本机 API 令牌；明文只在此响应出现一次。
type LocalApiToken struct {
	Token    string    `json:"token"`
	IssuedAt time.Time `json:"issued_at"`
}

// LocalApiEndpointStats 是第三方本机 API 按端点的调用统计。
type LocalApiEndpointStats struct {
	Endpoint  string `json:"endpoint"`
	Calls     int64  `json:"calls"`
	LastError string `json:"last_error,omitempty"`
}

// LocalApiStats 是第三方本机 API 统计（不含素材内容）。
type LocalApiStats struct {
	Enabled    bool                    `json:"enabled"`
	TotalCalls int64                   `json:"total_calls"`
	ByEndpoint []LocalApiEndpointStats `json:"by_endpoint"`
}

// WithActions 返回带修复动作 ID 的预检问题副本；空动作归一为 []（契约要求数组）。
func (i PreflightIssue) WithActions(actions ...ID) PreflightIssue {
	i.ActionIDs = actions
	if i.ActionIDs == nil {
		i.ActionIDs = []ID{}
	}
	return i
}
