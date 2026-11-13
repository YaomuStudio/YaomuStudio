package api

// 契约漂移守护：把 contracts/openapi.yaml 与内部路由表、响应类型和枚举常量
// 做确定性对照。守护只读取契约和 Go 类型，不启动业务服务，也不把实现当作
// 契约来源；发现真实漂移时应先改契约/文档，而不是放宽本测试。
//
// 首批覆盖：
//   - 内部路由表 vs OpenAPI 的 /health/live 与全部 /api/v1 path+method；
//   - 第三方本机 API 的 /v1 路径显式列出（独立回环端口，不在本路由表）；
//   - 选定响应 schema 的 required/属性/JSON 类型类别；
//   - 选定枚举的 OpenAPI 取值集合 vs Go 常量集合；
//   - _core / ExecutionConfig / 内部执行字段不得进入公开 required/属性。
//
// 未覆盖（不宣称全 OpenAPI 覆盖）：
//   - 请求体 schema 与错误响应矩阵；
//   - UI 桥接映射（ui/src/api 的 snake/camel 转换）；
//   - SSE 帧、业务语义、值域/格式校验和动态响应；
//   - 未列入 schemaGuards/enumGuards 的 schema 与枚举（见本文件末尾清单）；
//   - Go 常量无法在运行时枚举：新增枚举常量必须同步加入 enumGuards。

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
	"gopkg.in/yaml.v3"
)

const openAPIContractPath = "../../../contracts/openapi.yaml"

type openAPIDocument struct {
	Paths      map[string]map[string]any `yaml:"paths"`
	Components struct {
		Schemas map[string]openAPISchema `yaml:"schemas"`
	} `yaml:"components"`
}

type openAPISchema struct {
	Type       string                     `yaml:"type"`
	Required   []string                   `yaml:"required"`
	Properties map[string]openAPIProperty `yaml:"properties"`
	Enum       []any                      `yaml:"enum"`
	Ref        string                     `yaml:"$ref"`
	Items      *openAPIProperty           `yaml:"items"`
	AllOf      []openAPIProperty          `yaml:"allOf"`
	OneOf      []openAPIProperty          `yaml:"oneOf"`
	AnyOf      []openAPIProperty          `yaml:"anyOf"`
}

type openAPIProperty struct {
	Type  string            `yaml:"type"`
	Ref   string            `yaml:"$ref"`
	Enum  []any             `yaml:"enum"`
	Items *openAPIProperty  `yaml:"items"`
	AllOf []openAPIProperty `yaml:"allOf"`
	OneOf []openAPIProperty `yaml:"oneOf"`
	AnyOf []openAPIProperty `yaml:"anyOf"`
}

var openAPIHTTPMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// thirdPartyLocalAPIOperations 是明确不属于本路由表的第三方本机 API。
// 它们由独立回环端口实现；新增/删除该面必须显式更新本表，防止新路径被
// 静默归入“非内部路径”而绕过路由守护。
var thirdPartyLocalAPIOperations = map[string]bool{
	"POST /v1/audio/transcriptions": true,
	"POST /v1/audio/speech":         true,
}

func loadOpenAPI(t *testing.T) openAPIDocument {
	t.Helper()
	data, err := os.ReadFile(openAPIContractPath)
	if err != nil {
		t.Fatalf("read %s: %v", openAPIContractPath, err)
	}
	var doc openAPIDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse %s: %v", openAPIContractPath, err)
	}
	if len(doc.Paths) == 0 || len(doc.Components.Schemas) == 0 {
		t.Fatalf("%s: paths/components.schemas 为空，契约结构可能已变化", openAPIContractPath)
	}
	return doc
}

func isInternalAPIPath(path string) bool {
	return path == "/health/live" || strings.HasPrefix(path, "/api/v1/")
}

func documentedOperations(doc openAPIDocument, keep func(string) bool) map[string]bool {
	operations := make(map[string]bool)
	for path, item := range doc.Paths {
		if !keep(path) {
			continue
		}
		for key := range item {
			method := strings.ToLower(key)
			if !openAPIHTTPMethods[method] {
				// path item 级 parameters/$ref/summary 不是操作。
				continue
			}
			operations[strings.ToUpper(method)+" "+path] = true
		}
	}
	return operations
}

func documentedInternalOperations(doc openAPIDocument) map[string]bool {
	return documentedOperations(doc, isInternalAPIPath)
}

func documentedThirdPartyOperations(doc openAPIDocument) map[string]bool {
	return documentedOperations(doc, func(path string) bool { return !isInternalAPIPath(path) })
}

// compareRoutes 对比路由表与契约中的 method+path 集合。
func compareRoutes(routes []routeEntry, documented map[string]bool) error {
	registered := make(map[string]bool, len(routes))
	var duplicates []string
	for _, route := range routes {
		operation := route.method + " " + route.pattern
		if registered[operation] {
			duplicates = append(duplicates, operation)
		}
		registered[operation] = true
	}
	missing, extra := setDifferences(documented, registered)
	var problems []string
	if len(missing) > 0 {
		problems = append(problems, "OpenAPI 声明但路由表未注册: "+strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		problems = append(problems, "路由表注册但 OpenAPI 未声明: "+strings.Join(extra, ", "))
	}
	if len(duplicates) > 0 {
		problems = append(problems, "路由表重复注册: "+strings.Join(duplicates, ", "))
	}
	return joinContractProblems("routes", problems)
}

func compareThirdPartyOperations(doc openAPIDocument) error {
	documented := documentedThirdPartyOperations(doc)
	missing, extra := setDifferences(thirdPartyLocalAPIOperations, documented)
	var problems []string
	if len(missing) > 0 {
		problems = append(problems, "显式列出的第三方路径已不在契约中: "+strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		problems = append(problems, "契约中出现未分类的非内部路径: "+strings.Join(extra, ", "))
	}
	return joinContractProblems("third-party-local-api", problems)
}

// schemaGuard 是 OpenAPI schema 到 Go 响应类型的显式映射。
type schemaGuard struct {
	schema string
	goType reflect.Type
	// optionalAlwaysEmitted 是 Go 始终序列化（无 omitempty）、但 OpenAPI
	// 为兼容旧 Core 或只读派生而保持可选的字段。required 集合必须等于
	// “Go 始终序列化字段 - 本列表”；新增例外必须在此说明原因。
	optionalAlwaysEmitted []string
}

// errorCodeSchema 映射到 ErrorInfo 而不是 domain.Error：后者是内部可比较
// 错误对象，没有公开 JSON 形状。
var schemaGuards = []schemaGuard{
	{schema: "SystemInfo", goType: reflect.TypeOf(domain.SystemInfo{})},
	// 运行状态为 2026-11-12 新增派生字段；旧 Core 无此字段，契约保持可选。
	{schema: "Capability", goType: reflect.TypeOf(domain.Capability{}),
		optionalAlwaysEmitted: []string{"runtime_state", "running_tasks"}},
	{schema: "ProjectSummary", goType: reflect.TypeOf(domain.ProjectSummary{})},
	{schema: "Project", goType: reflect.TypeOf(domain.Project{})},
	// input_asset_ids 为创建时冻结的只读派生字段，始终输出；旧 Core 无此字段。
	{schema: "WorkflowRun", goType: reflect.TypeOf(domain.WorkflowRun{}),
		optionalAlwaysEmitted: []string{"input_asset_ids"}},
	{schema: "PipelineNodeState", goType: reflect.TypeOf(domain.PipelineNodeState{})},
	{schema: "Task", goType: reflect.TypeOf(domain.Task{})},
	{schema: "TaskStep", goType: reflect.TypeOf(domain.TaskStep{})},
	{schema: "TaskList", goType: reflect.TypeOf(domain.TaskList{})},
	{schema: "SegmentCandidateJob", goType: reflect.TypeOf(service.SegmentCandidateJob{})},
	{schema: "PreflightIssue", goType: reflect.TypeOf(domain.PreflightIssue{})},
	{schema: "PreflightResult", goType: reflect.TypeOf(domain.PreflightResult{})},
	// 安装记录始终输出三个槽位数组与时间戳；旧 Core 可能省略，故契约保持可选。
	{schema: "Installation", goType: reflect.TypeOf(domain.Installation{}),
		optionalAlwaysEmitted: []string{
			"covered_slots", "bound_slots", "current_slots", "created_at", "updated_at",
		}},
	{schema: "DomainEvent", goType: reflect.TypeOf(domain.DomainEvent{})},
	{schema: "ReviewGate", goType: reflect.TypeOf(domain.ReviewGate{})},
	{schema: "ReviewCurrentBinding", goType: reflect.TypeOf(domain.ReviewCurrentBinding{})},
	{schema: "Snapshot", goType: reflect.TypeOf(domain.Snapshot{})},
	{schema: "ExecutionLog", goType: reflect.TypeOf(domain.ExecutionLog{})},
	{schema: "ExecutionLogLine", goType: reflect.TypeOf(domain.ExecutionLogLine{})},
	{schema: "Asset", goType: reflect.TypeOf(domain.Asset{})},
	{schema: "MediaMeta", goType: reflect.TypeOf(domain.MediaMeta{})},
	{schema: "SubtitleTrack", goType: reflect.TypeOf(domain.SubtitleTrack{})},
	{schema: "SubtitleRevision", goType: reflect.TypeOf(domain.SubtitleRevision{})},
	{schema: "Segment", goType: reflect.TypeOf(domain.Segment{})},
	{schema: "AudioCandidate", goType: reflect.TypeOf(domain.AudioCandidate{})},
	{schema: "CandidateSelection", goType: reflect.TypeOf(domain.CandidateSelection{})},
	{schema: "ExportRecord", goType: reflect.TypeOf(domain.ExportRecord{})},
	// covered_slots 是新计划必返的只读覆盖范围；旧 Core 无此字段。
	{schema: "InstallationPlan", goType: reflect.TypeOf(domain.InstallationPlan{}),
		optionalAlwaysEmitted: []string{"covered_slots"}},
	{schema: "DeviceInfo", goType: reflect.TypeOf(domain.DeviceInfo{})},
	{schema: "EnvironmentInfo", goType: reflect.TypeOf(domain.EnvironmentInfo{})},
	{schema: "EnvironmentCheck", goType: reflect.TypeOf(domain.EnvironmentCheck{})},
	{schema: "EventCursor", goType: reflect.TypeOf(domain.EventCursor{})},
	{schema: "ReleaseRef", goType: reflect.TypeOf(domain.ReleaseRef{})},
	{schema: "Error", goType: reflect.TypeOf(domain.ErrorInfo{})},
	{schema: "StorageUsage", goType: reflect.TypeOf(domain.StorageUsage{})},
	{schema: "StorageCleanupResult", goType: reflect.TypeOf(domain.StorageCleanupResult{})},
	{schema: "LocalApiSettings", goType: reflect.TypeOf(domain.LocalApiSettings{})},
	{schema: "LocalApiToken", goType: reflect.TypeOf(domain.LocalApiToken{})},
	{schema: "LocalApiStats", goType: reflect.TypeOf(domain.LocalApiStats{})},
	{schema: "LocalApiEndpointStats", goType: reflect.TypeOf(domain.LocalApiEndpointStats{})},
	{schema: "EngineConfig", goType: reflect.TypeOf(domain.EngineConfig{})},
	{schema: "LicenseSummary", goType: reflect.TypeOf(domain.LicenseSummary{})},
	// ErrorResponse 由本包的 errorEnvelope 写出；与成功响应分开映射。
	{schema: "ErrorResponse", goType: reflect.TypeOf(errorEnvelope{})},
}

// internalPublicNames 是绝不能出现在选定公开响应 schema 的属性/required 中
// 的内部执行字段。_core 只存在于 Task.Params（json:"-"）；ExecutionConfig
// 与 model_lock 是 Core 持久化的冻结元数据，不随公开响应下发。
var internalPublicNames = map[string]bool{
	"_core":                  true,
	"execution":              true,
	"execution_config":       true,
	"execution_dependencies": true,
	"model_lock":             true,
}

type goJSONField struct {
	name      string
	typ       reflect.Type
	omitempty bool
}

func goJSONFields(t reflect.Type) (map[string]goJSONField, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%s 不是 struct", t)
	}
	fields := make(map[string]goJSONField, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue // 非导出字段不参与 JSON。
		}
		name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue // json:"-" 是内部字段，不映射公开属性。
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = goJSONField{
			name:      name,
			typ:       field.Type,
			omitempty: hasJSONOption(options, "omitempty"),
		}
	}
	return fields, nil
}

func hasJSONOption(options, want string) bool {
	for _, option := range strings.Split(options, ",") {
		if option == want {
			return true
		}
	}
	return false
}

var timeType = reflect.TypeOf(time.Time{})

// goJSONCategory 把 Go 字段类型映射为 OpenAPI 的 JSON 类型类别。
// time.Time 在线上是 ISO 8601 字符串；命名标量按底层 kind 归类。
func goJSONCategory(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == timeType {
		return "string"
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Map, reflect.Struct:
		return "object"
	default:
		// interface{} 等动态类型不在此处断言。
		return ""
	}
}

func goJSONElementCategory(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
		return ""
	}
	return goJSONCategory(t.Elem())
}

// resolveOpenAPICategory 解析属性自身或 $ref 指向 schema 的 JSON 类型。
// 复合 schema（allOf/oneOf/anyOf）返回空类别，交由“未覆盖范围”说明。
func resolveOpenAPICategory(prop openAPIProperty, doc openAPIDocument, depth int) (string, error) {
	if depth > 8 {
		return "", fmt.Errorf("$ref 嵌套超过 8 层")
	}
	if prop.Ref != "" {
		name, err := schemaNameFromRef(prop.Ref)
		if err != nil {
			return "", err
		}
		schema, ok := doc.Components.Schemas[name]
		if !ok {
			return "", fmt.Errorf("$ref 指向不存在的 schema %q", name)
		}
		if schema.Ref != "" {
			return resolveOpenAPICategory(openAPIProperty{Ref: schema.Ref}, doc, depth+1)
		}
		if schema.Type != "" {
			return schema.Type, nil
		}
		if len(schema.Enum) > 0 {
			return "string", nil
		}
		return "", nil
	}
	if prop.Type != "" {
		return prop.Type, nil
	}
	if len(prop.Enum) > 0 {
		return "string", nil
	}
	if len(prop.AllOf) > 0 || len(prop.OneOf) > 0 || len(prop.AnyOf) > 0 {
		return "", nil
	}
	return "", nil
}

func schemaNameFromRef(ref string) (string, error) {
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(ref, prefix) {
		return "", fmt.Errorf("不支持的 $ref %q（只解析 components/schemas）", ref)
	}
	name := strings.TrimPrefix(ref, prefix)
	if name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("非法 $ref %q", ref)
	}
	return name, nil
}

func checkSchemaGuards(doc openAPIDocument, guards []schemaGuard) error {
	var problems []string
	for _, guard := range guards {
		schema, ok := doc.Components.Schemas[guard.schema]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: OpenAPI 缺少 schema", guard.schema))
			continue
		}
		problems = append(problems, checkOneSchema(guard, schema, doc)...)
	}
	for name, schema := range doc.Components.Schemas {
		for _, required := range schema.Required {
			if internalPublicNames[required] {
				problems = append(problems,
					fmt.Sprintf("%s.required: 内部字段 %q 不得进入公开 required", name, required))
			}
		}
	}
	if _, ok := doc.Components.Schemas["ExecutionConfig"]; ok {
		problems = append(problems, "components.schemas: 内部 ExecutionConfig 不得成为公开 schema")
	}
	return joinContractProblems("schemas", problems)
}

func checkOneSchema(guard schemaGuard, schema openAPISchema, doc openAPIDocument) []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	if schema.Type != "object" {
		add("%s: OpenAPI type = %q, want object", guard.schema, schema.Type)
	}
	fields, err := goJSONFields(guard.goType)
	if err != nil {
		add("%s: %v", guard.schema, err)
		return problems
	}

	optionalAlways := make(map[string]bool, len(guard.optionalAlwaysEmitted))
	for _, name := range guard.optionalAlwaysEmitted {
		optionalAlways[name] = true
		field, ok := fields[name]
		if !ok {
			add("%s: optionalAlwaysEmitted %q 在 Go 类型 %s 中不存在", guard.schema, name, guard.goType)
		} else if field.omitempty {
			add("%s: optionalAlwaysEmitted %q 在 Go 类型中带 omitempty，不是始终序列化", guard.schema, name)
		}
	}

	var alwaysEmitted []string
	for name, field := range fields {
		if !field.omitempty {
			alwaysEmitted = append(alwaysEmitted, name)
		}
	}
	sort.Strings(alwaysEmitted)

	propertyNames := sortedMapKeys(schema.Properties)
	for _, name := range propertyNames {
		if internalPublicNames[name] {
			add("%s.%s: 内部字段不得出现在公开 properties", guard.schema, name)
		}
		field, ok := fields[name]
		if !ok {
			add("%s.%s: OpenAPI 有属性但 Go 类型 %s 无对应 json tag", guard.schema, name, guard.goType)
			continue
		}
		prop := schema.Properties[name]
		want, err := resolveOpenAPICategory(prop, doc, 0)
		if err != nil {
			add("%s.%s: %v", guard.schema, name, err)
			continue
		}
		if want == "" {
			continue // 无类型或复合 schema：不在首批断言范围。
		}
		got := goJSONCategory(field.typ)
		if got == "" {
			continue // interface{} 等动态类型：不在首批断言范围。
		}
		if got != want {
			add("%s.%s: OpenAPI 类型 %q 与 Go 类型 %s（类别 %q）不一致",
				guard.schema, name, want, field.typ, got)
			continue
		}
		if want == "array" && prop.Items != nil {
			itemWant, err := resolveOpenAPICategory(*prop.Items, doc, 0)
			if err != nil {
				add("%s.%s[]: %v", guard.schema, name, err)
				continue
			}
			if itemWant == "" {
				continue
			}
			itemGot := goJSONElementCategory(field.typ)
			if itemGot == "" {
				continue
			}
			if itemGot != itemWant {
				add("%s.%s[]: OpenAPI items 类型 %q 与 Go 元素 %s（类别 %q）不一致",
					guard.schema, name, itemWant, field.typ.Elem(), itemGot)
			}
		}
	}

	for _, name := range schema.Required {
		if internalPublicNames[name] {
			// 上面全局检查已报告，这里不重复。
			continue
		}
		field, ok := fields[name]
		if !ok {
			add("%s: required %q 在 Go 类型 %s 中无对应 json tag", guard.schema, name, guard.goType)
			continue
		}
		if field.omitempty {
			add("%s: required %q 在 Go 类型中带 omitempty，零值可能被省略", guard.schema, name)
		}
		if _, ok := schema.Properties[name]; !ok {
			add("%s: required %q 未在 properties 中定义", guard.schema, name)
		}
	}

	for _, name := range alwaysEmitted {
		if _, ok := schema.Properties[name]; !ok {
			add("%s: Go 始终序列化字段 %q 未在 OpenAPI properties 中声明（内部字段应使用 json:\"-\"）",
				guard.schema, name)
		}
	}

	wantRequired := differenceSorted(alwaysEmitted, guard.optionalAlwaysEmitted)
	gotRequired := uniqueSorted(schema.Required)
	if !equalStrings(wantRequired, gotRequired) {
		add("%s: required 集合与 Go 始终序列化字段不一致: OpenAPI=%v Go(扣除兼容例外)=%v；"+
			"若为旧 Core 兼容/只读派生，请加入 optionalAlwaysEmitted",
			guard.schema, gotRequired, wantRequired)
	}
	return problems
}

// enumGuard 是 OpenAPI 枚举到 Go 常量集合的显式映射。property 为空表示
// schema 自身是枚举；否则取 schema.properties[property].enum。
type enumGuard struct {
	domainName string
	schema     string
	property   string
	values     []string
}

var enumGuards = []enumGuard{
	// 核心状态机。
	{"RunStatus", "RunStatus", "", enumStrings(
		domain.RunStatusDraft, domain.RunStatusQueued, domain.RunStatusRunning,
		domain.RunStatusPauseRequested, domain.RunStatusPaused, domain.RunStatusWaitingReview,
		domain.RunStatusPartial, domain.RunStatusSucceeded, domain.RunStatusFailed,
		domain.RunStatusCanceled,
	)},
	{"TaskStatus", "TaskStatus", "", enumStrings(
		domain.TaskStatusQueued, domain.TaskStatusWaitingResource, domain.TaskStatusRunning,
		domain.TaskStatusPauseRequested, domain.TaskStatusPaused, domain.TaskStatusSucceeded,
		domain.TaskStatusFailed, domain.TaskStatusCancelRequested, domain.TaskStatusCanceled,
		domain.TaskStatusInterrupted,
	)},
	{"TaskKind", "TaskKind", "", enumStrings(
		domain.TaskKindNode, domain.TaskKindSegment, domain.TaskKindExport,
	)},
	{"StepStatus", "StepStatus", "", enumStrings(
		domain.StepStatusPending, domain.StepStatusRunning, domain.StepStatusCommitting,
		domain.StepStatusSucceeded, domain.StepStatusFailed, domain.StepStatusCanceled,
		domain.StepStatusInterrupted,
	)},
	{"NodeStatus", "NodeStatus", "", enumStrings(
		domain.NodeStatusPending, domain.NodeStatusRunning, domain.NodeStatusWaitingReview,
		domain.NodeStatusDone, domain.NodeStatusSkipped, domain.NodeStatusReused,
		domain.NodeStatusFailed,
	)},
	{"PipelineNodeKey", "PipelineNodeKey", "", enumStrings(
		domain.NodeKeyImport, domain.NodeKeyAnalyze, domain.NodeKeySeparate, domain.NodeKeyExtract,
		domain.NodeKeyErase, domain.NodeKeyTranslate, domain.NodeKeyClone, domain.NodeKeyMix,
	)},
	{"InstallationPhase", "InstallationPhase", "", enumStrings(
		domain.InstallationPhasePlanning, domain.InstallationPhaseDownloading,
		domain.InstallationPhaseVerifying, domain.InstallationPhaseInstalling,
		domain.InstallationPhaseDone, domain.InstallationPhaseFailed,
		domain.InstallationPhaseCanceled,
	)},

	// 能力 / 设备 / 存储 / 导出。
	{"CapabilitySlotKey", "CapabilitySlotKey", "", enumStrings(
		domain.CapabilitySlotASR, domain.CapabilitySlotTranslate, domain.CapabilitySlotTTS,
		domain.CapabilitySlotVoiceClone, domain.CapabilitySlotSubtitleErase,
		domain.CapabilitySlotSeparation, domain.CapabilitySlotOCR, domain.CapabilitySlotFFmpeg,
	)},
	{"ModelSlotKey", "ModelSlotKey", "", enumStrings(
		domain.ModelSlotASR, domain.ModelSlotTranslate, domain.ModelSlotTTS,
		domain.ModelSlotVoiceClone, domain.ModelSlotSubtitleErase, domain.ModelSlotSeparation,
	)},
	{"SlotState", "SlotState", "", enumStrings(
		domain.SlotStateUnknown, domain.SlotStateUnsupported, domain.SlotStateUnverified,
		domain.SlotStateNeedsSetup, domain.SlotStateInstalling, domain.SlotStateVerifying,
		domain.SlotStateReady, domain.SlotStateNeedsAction, domain.SlotStateRestartRequired,
		domain.SlotStateFailed, domain.SlotStateDegraded,
	)},
	{"CapabilityRuntimeState", "Capability", "runtime_state", enumStrings(
		domain.CapabilityRuntimeIdle, domain.CapabilityRuntimeRunning,
	)},
	{"DeviceKind", "DeviceKind", "", enumStrings(
		domain.DeviceKindCPU, domain.DeviceKindCUDA, domain.DeviceKindMPS,
	)},
	{"StorageCategory", "StorageCategory", "", enumStrings(
		domain.StorageCategoryModels, domain.StorageCategoryMedia,
		domain.StorageCategoryCache, domain.StorageCategoryExports,
	)},
	{"ExportKind", "ExportKind", "", enumStrings(
		domain.ExportKindVideo, domain.ExportKindAudio, domain.ExportKindSubtitle,
	)},
	{"ExportFormat", "ExportFormat", "", enumStrings(
		domain.ExportFormatMP4H264, domain.ExportFormatSRT, domain.ExportFormatASS,
		domain.ExportFormatWAV, domain.ExportFormatMP3,
	)},

	// 素材 / 字幕 / 审核。
	{"AssetType", "AssetType", "", enumStrings(
		domain.AssetTypeVideo, domain.AssetTypeAudio, domain.AssetTypeSubtitle,
		domain.AssetTypeAudioTrack, domain.AssetTypeImage, domain.AssetTypeArchive,
		domain.AssetTypeOther,
	)},
	{"AssetRole", "AssetRole", "", enumStrings(
		domain.AssetRoleOriginal, domain.AssetRoleVoice, domain.AssetRoleAmbience,
		domain.AssetRoleCleanVideo, domain.AssetRoleSourceSubtitle,
		domain.AssetRoleTranslatedSubtitle, domain.AssetRoleDubAudio,
		domain.AssetRoleTranslatedVideo, domain.AssetRoleFinalVideo,
	)},
	{"AssetStatus", "AssetStatus", "", enumStrings(
		domain.AssetStatusImporting, domain.AssetStatusReady,
		domain.AssetStatusMissing, domain.AssetStatusCorrupt,
	)},
	{"TrackType", "TrackType", "", enumStrings(
		domain.TrackTypeTranscription, domain.TrackTypeTranslation, domain.TrackTypeCorrection,
	)},
	{"ReviewType", "ReviewType", "", enumStrings(
		domain.ReviewTypeTranslation, domain.ReviewTypeDelivery,
	)},
	{"ReviewStatus", "ReviewStatus", "", enumStrings(
		domain.ReviewStatusPending, domain.ReviewStatusApproved,
	)},
	{"ReviewBindingKind", "ReviewCurrentBinding", "source_kind", enumStrings(
		domain.ReviewBindingTranslationRevision, domain.ReviewBindingDeliveryRevision,
		domain.ReviewBindingDeliveryMix,
	)},

	// 参数 / 提取 / 样式。
	{"ParamFieldType", "ParamField", "type", enumStrings(
		domain.ParamFieldTypeString, domain.ParamFieldTypeNumber,
		domain.ParamFieldTypeBoolean, domain.ParamFieldTypeEnum,
	)},
	{"ExtractMode", "ExtractOptions", "mode", enumStrings(
		domain.ExtractModeASR, domain.ExtractModeOCR, domain.ExtractModeASROCR,
	)},
	{"ExtractMode", "RunParams", "extract_mode", enumStrings(
		domain.ExtractModeASR, domain.ExtractModeOCR, domain.ExtractModeASROCR,
	)},
	{"OCRLanguage", "ExtractOptions", "ocr_language", enumStrings(
		domain.OCRLanguageChinese, domain.OCRLanguageChineseTraditional,
		domain.OCRLanguageEnglish, domain.OCRLanguageJapanese,
	)},
	{"OCRLanguage", "RunParams", "ocr_language", enumStrings(
		domain.OCRLanguageChinese, domain.OCRLanguageChineseTraditional,
		domain.OCRLanguageEnglish, domain.OCRLanguageJapanese,
	)},
	{"EraseMode", "RunParams", "erase_mode", enumStrings(
		domain.EraseModeDialogOnly, domain.EraseModeDialogAndArt,
	)},
	{"SubtitleAlign", "SubtitleStyle", "align", enumStrings(
		domain.SubtitleAlignLeft, domain.SubtitleAlignCenter, domain.SubtitleAlignRight,
	)},
	{"EraseBlockSource", "EraseBlock", "source", enumStrings(
		domain.EraseBlockSourceClean, domain.EraseBlockSourceSource,
	)},

	// 其余公开枚举。
	{"ErrorCode", "ErrorCode", "", enumStrings(
		domain.ErrorCodeInvalidRequest, domain.ErrorCodeLocalAuthRequired,
		domain.ErrorCodeLocalAccessDenied, domain.ErrorCodeRevisionConflict,
		domain.ErrorCodePreflightStale, domain.ErrorCodeIdempotencyConflict,
		domain.ErrorCodeModelNotInstalled, domain.ErrorCodeRuntimeIncompatible,
		domain.ErrorCodeCapabilityUnverified, domain.ErrorCodeInsufficientDisk,
		domain.ErrorCodeCoreNotReady, domain.ErrorCodePersistenceUnavailable,
		domain.ErrorCodeWorkerExited, domain.ErrorCodeResourceExhausted,
		domain.ErrorCodeEventCursorExpired, domain.ErrorCodeNotFound,
		domain.ErrorCodeReviewRequired,
	)},
	{"PreflightSeverity", "PreflightSeverity", "", enumStrings(
		domain.PreflightPass, domain.PreflightWarning, domain.PreflightBlocker,
	)},
	{"PreflightSeverity", "EnvironmentCheck", "severity", enumStrings(
		domain.PreflightPass, domain.PreflightWarning, domain.PreflightBlocker,
	)},
	{"AuthorType", "SubtitleRevision", "author_type", enumStrings(
		domain.AuthorTypeSystem, domain.AuthorTypeUser,
	)},
}

func enumStrings[T ~string](values ...T) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = string(value)
	}
	return result
}

func checkEnumGuards(doc openAPIDocument, guards []enumGuard) error {
	var problems []string
	for _, guard := range guards {
		schema, ok := doc.Components.Schemas[guard.schema]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: OpenAPI 缺少 schema", guard.schema))
			continue
		}
		path := guard.schema
		raw := schema.Enum
		if guard.property != "" {
			prop, ok := schema.Properties[guard.property]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s.%s: OpenAPI 缺少属性", guard.schema, guard.property))
				continue
			}
			path = guard.schema + "." + guard.property
			raw = prop.Enum
		}
		if len(raw) == 0 {
			problems = append(problems, fmt.Sprintf("%s: OpenAPI 未声明 enum", path))
			continue
		}
		documented := make([]string, len(raw))
		invalid := false
		for i, value := range raw {
			text, ok := value.(string)
			if !ok {
				problems = append(problems,
					fmt.Sprintf("%s: enum 值 %v 不是字符串；本守护只比较字符串枚举", path, value))
				invalid = true
				continue
			}
			documented[i] = text
		}
		if invalid {
			continue
		}
		missing, extra := setDifferences(stringSet(guard.values), stringSet(documented))
		if len(missing) > 0 {
			problems = append(problems,
				fmt.Sprintf("%s: Go 常量有但 OpenAPI 缺少（%s）: %s",
					path, guard.domainName, strings.Join(missing, ", ")))
		}
		if len(extra) > 0 {
			problems = append(problems,
				fmt.Sprintf("%s: OpenAPI 有但 Go 常量缺少（%s）: %s",
					path, guard.domainName, strings.Join(extra, ", ")))
		}
	}
	return joinContractProblems("enums", problems)
}

// ---- 测试 ----

func TestContractDriftGuardRoutes(t *testing.T) {
	server := New(Config{})
	routes := server.routeTable()

	doc := loadOpenAPI(t)
	if err := compareRoutes(routes, documentedInternalOperations(doc)); err != nil {
		t.Fatalf("原样契约与路由表不一致（真实漂移，禁止放宽测试）: %v", err)
	}
	if err := compareThirdPartyOperations(doc); err != nil {
		t.Fatalf("第三方本机 API 分类不一致: %v", err)
	}

	t.Run("删除契约路径必须失败", func(t *testing.T) {
		mutated := loadOpenAPI(t)
		delete(mutated.Paths, "/api/v1/devices")
		expectContractFailure(t, compareRoutes(routes, documentedInternalOperations(mutated)),
			"删除契约路径")
	})

	t.Run("修改契约方法必须失败", func(t *testing.T) {
		mutated := loadOpenAPI(t)
		mutated.Paths["/api/v1/system"] = map[string]any{"post": map[string]any{}}
		expectContractFailure(t, compareRoutes(routes, documentedInternalOperations(mutated)),
			"修改契约方法")
	})

	t.Run("新增未注册契约路径必须失败", func(t *testing.T) {
		mutated := loadOpenAPI(t)
		mutated.Paths["/api/v1/unregistered"] = map[string]any{"get": map[string]any{}}
		expectContractFailure(t, compareRoutes(routes, documentedInternalOperations(mutated)),
			"新增未注册契约路径")
	})

	t.Run("新增未分类非内部路径必须失败", func(t *testing.T) {
		mutated := loadOpenAPI(t)
		mutated.Paths["/v1/audio/unclassified"] = map[string]any{"post": map[string]any{}}
		expectContractFailure(t, compareThirdPartyOperations(mutated), "新增未分类非内部路径")
	})
}

func TestContractDriftGuardSchemas(t *testing.T) {
	doc := loadOpenAPI(t)
	if err := checkSchemaGuards(doc, schemaGuards); err != nil {
		t.Fatalf("原样契约与 Go 类型不一致（真实漂移，禁止放宽测试）: %v", err)
	}

	t.Run("删除 required 条目必须失败", func(t *testing.T) {
		mutated := loadOpenAPI(t)
		schema := mutated.Components.Schemas["SystemInfo"]
		schema.Required = withoutString(schema.Required, "core_ready")
		mutated.Components.Schemas["SystemInfo"] = schema
		expectContractFailure(t, checkSchemaGuards(mutated, schemaGuards), "删除 required 条目")
	})

	t.Run("删除 required 字段定义必须失败", func(t *testing.T) {
		mutated := loadOpenAPI(t)
		schema := mutated.Components.Schemas["SystemInfo"]
		schema.Properties = cloneProperties(schema.Properties)
		delete(schema.Properties, "core_ready")
		mutated.Components.Schemas["SystemInfo"] = schema
		expectContractFailure(t, checkSchemaGuards(mutated, schemaGuards), "删除 required 字段定义")
	})

	t.Run("内部字段进入公开 required 必须失败", func(t *testing.T) {
		mutated := loadOpenAPI(t)
		schema := mutated.Components.Schemas["Task"]
		schema.Required = append(append([]string(nil), schema.Required...), "_core")
		mutated.Components.Schemas["Task"] = schema
		expectContractFailure(t, checkSchemaGuards(mutated, schemaGuards), "内部字段进入公开 required")
	})

	t.Run("ExecutionConfig 成为公开 schema 必须失败", func(t *testing.T) {
		mutated := loadOpenAPI(t)
		mutated.Components.Schemas["ExecutionConfig"] = openAPISchema{
			Type:       "object",
			Required:   []string{"model_lock"},
			Properties: map[string]openAPIProperty{"model_lock": {Ref: "#/components/schemas/ReleaseRef"}},
		}
		expectContractFailure(t, checkSchemaGuards(mutated, schemaGuards), "ExecutionConfig 成为公开 schema")
	})

	t.Run("新增未映射公开属性必须失败", func(t *testing.T) {
		mutated := loadOpenAPI(t)
		schema := mutated.Components.Schemas["SystemInfo"]
		schema.Properties = cloneProperties(schema.Properties)
		schema.Properties["unexpected_public"] = openAPIProperty{Type: "string"}
		mutated.Components.Schemas["SystemInfo"] = schema
		expectContractFailure(t, checkSchemaGuards(mutated, schemaGuards), "新增未映射公开属性")
	})
}

func TestContractDriftGuardEnums(t *testing.T) {
	doc := loadOpenAPI(t)
	if err := checkEnumGuards(doc, enumGuards); err != nil {
		t.Fatalf("原样契约枚举与 Go 常量不一致（真实漂移，禁止放宽测试）: %v", err)
	}

	t.Run("修改顶层枚举值必须失败", func(t *testing.T) {
		mutated := loadOpenAPI(t)
		schema := mutated.Components.Schemas["RunStatus"]
		schema.Enum = replaceEnumValue(schema.Enum, "RUNNING", "RUNNINGX")
		mutated.Components.Schemas["RunStatus"] = schema
		expectContractFailure(t, checkEnumGuards(mutated, enumGuards), "修改顶层枚举值")
	})

	t.Run("删除枚举值必须失败", func(t *testing.T) {
		mutated := loadOpenAPI(t)
		schema := mutated.Components.Schemas["TaskStatus"]
		schema.Enum = removeEnumValue(schema.Enum, "CANCELED")
		mutated.Components.Schemas["TaskStatus"] = schema
		expectContractFailure(t, checkEnumGuards(mutated, enumGuards), "删除枚举值")
	})

	t.Run("修改内联枚举值必须失败", func(t *testing.T) {
		mutated := loadOpenAPI(t)
		schema := mutated.Components.Schemas["ReviewCurrentBinding"]
		prop := schema.Properties["source_kind"]
		prop.Enum = replaceEnumValue(prop.Enum, "delivery_mix", "delivery_mix_v2")
		schema.Properties["source_kind"] = prop
		mutated.Components.Schemas["ReviewCurrentBinding"] = schema
		expectContractFailure(t, checkEnumGuards(mutated, enumGuards), "修改内联枚举值")
	})
}

func expectContractFailure(t *testing.T, err error, mutation string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s 后守护仍然通过", mutation)
	}
	t.Logf("%s 被拒绝（预期）: %v", mutation, err)
}

// ---- 小型集合/结构辅助 ----

func joinContractProblems(scope string, problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("%s 契约漂移：\n  - %s", scope, strings.Join(problems, "\n  - "))
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func setDifferences(want, got map[string]bool) (missing, extra []string) {
	for value := range want {
		if !got[value] {
			missing = append(missing, value)
		}
	}
	for value := range got {
		if !want[value] {
			extra = append(extra, value)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

func sortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func uniqueSorted(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	deduped := result[:0]
	for _, value := range result {
		if len(deduped) == 0 || deduped[len(deduped)-1] != value {
			deduped = append(deduped, value)
		}
	}
	return deduped
}

func differenceSorted(values []string, remove []string) []string {
	drop := stringSet(remove)
	var result []string
	for _, value := range uniqueSorted(values) {
		if !drop[value] {
			result = append(result, value)
		}
	}
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func withoutString(values []string, drop string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != drop {
			result = append(result, value)
		}
	}
	return result
}

func cloneProperties(values map[string]openAPIProperty) map[string]openAPIProperty {
	cloned := make(map[string]openAPIProperty, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func replaceEnumValue(values []any, old, replacement string) []any {
	result := append([]any(nil), values...)
	for i, value := range result {
		if value == old {
			result[i] = replacement
		}
	}
	return result
}

func removeEnumValue(values []any, drop string) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		if value != drop {
			result = append(result, value)
		}
	}
	return result
}

// ---- 首批未覆盖清单（后续批次按风险增补，不宣称全 OpenAPI 覆盖） ----
//
// 未进入 schemaGuards 的 schema：
//   - 请求体：UpdatePreparationRequest/Result、InstallationPlanRequest、
//     CreateInstallationRequest、CreateProjectRequest、ProjectUpdate、
//     AssetImportRequest/Result、AssetRelocateRequest、CreateWorkflowRunInput、
//     CreateTaskRequest、ApproveReviewRequest、CreateCandidatesRequest、
//     SelectCandidateRequest、SaveSubtitleRevisionRequest、ImportSubtitleTrackRequest、
//     CustomPipelineTemplate/List、ExportRequest、StorageCleanupRequest、
//     LocalApiSettingsUpdate。
//   - 响应：HealthStatus（handler 直接写 map，无 Go 类型）、WSLStatus（仅作为
//     EnvironmentInfo.wsl 的 object 类别校验）、TaskProgressData/Event（allOf
//     复合 schema）、EventCursor 已覆盖。
//   - 嵌套结构只校验顶层属性的类型类别与数组元素类别，不递归断言每个子字段的
//     required（子 schema 若进入 schemaGuards 才逐字段校验）。
//
// 未进入 enumGuards 的枚举：
//   - MediaMeta.role、ExecutionLogLine.stream：Go 侧是普通 string，没有常量来源；
//   - UpdatePreparationRequest.mode、UpdatePreparationResult.state/reason：
//     由 updateguard 内部字符串表达，未导出为 domain 枚举；
//   - CustomPipelineFeature：Go 侧没有对应枚举常量。
//
// 非确定性/动态面：SSE 帧（task.progress/task.execution_log 等）、错误状态码
// 矩阵、业务状态转换、值域/格式校验和 UI 桥接映射不在本文件覆盖范围内。
