package api

// HTTP 路由与处理器装配。
//
// 路由与 contracts/openapi.yaml（v0.2）逐端点对应；业务语义在 internal/service。
// 实例令牌与 Host/Origin 校验在中间件完成，处理器只处理已授权请求。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/events"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/localapi"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/updateguard"
)

const (
	tokenHeader = "X-YaoMu-Token"
	// maxJSONBody 是 JSON 请求体上限，避免一次读入无限输入。
	maxJSONBody = 1 << 20
	// maxIDLength 是不透明路径 ID 的长度上限。
	maxIDLength = 200
)

type serviceResponse = service.Response

type serviceIdem = service.Idem

// Config 是 API 装配参数。
type Config struct {
	Service  *service.Service
	LocalAPI *localapi.Manager
	Token    string
	Log      *logging.Logger
	// Updates 是应用更新准备守卫（Main-only 私有端点）；nil 时端点 fail-closed。
	Updates *updateguard.Guard
}

// Server 持有路由与依赖。
type Server struct {
	svc     *service.Service
	bus     *events.Bus
	local   *localapi.Manager
	token   string
	log     *logging.Logger
	updates *updateguard.Guard
	mux     *http.ServeMux
}

// New 构造 API 服务器。
func New(cfg Config) *Server {
	s := &Server{
		svc:     cfg.Service,
		local:   cfg.LocalAPI,
		token:   cfg.Token,
		log:     cfg.Log,
		updates: cfg.Updates,
		mux:     http.NewServeMux(),
	}
	if cfg.Service != nil {
		s.bus = cfg.Service.Bus()
	}
	s.routes()
	return s
}

// Handler 返回带中间件的 http.Handler。
func (s *Server) Handler() http.Handler {
	var handler http.Handler = s.mux
	// 普通 handler 全程准入在鉴权之后：未授权请求不占更新准备闸门。
	handler = s.admissionMiddleware(handler)
	handler = s.authMiddleware(handler)
	handler = s.hostOriginMiddleware(handler)
	handler = requestIDMiddleware(handler)
	handler = s.recoverMiddleware(handler)
	handler = s.accessLog(handler)
	return handler
}

// ---- 通用帮助 ----

// decodeJSON 读取并严格解码 JSON 请求体；返回原始字节用于幂等摘要。
func decodeJSON(r *http.Request, dst any) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBody+1))
	if err != nil {
		return nil, errRequestBody
	}
	if len(body) > maxJSONBody {
		return nil, fmt.Errorf("%w: request body too large", domain.ErrInvalidRequest)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("%w: request body required", domain.ErrInvalidRequest)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON body", domain.ErrInvalidRequest)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: unexpected trailing JSON", domain.ErrInvalidRequest)
	}
	return body, nil
}

var errRequestBody = fmt.Errorf("%w: invalid request body", domain.ErrInvalidRequest)

// idemFrom 读取幂等键并计算请求摘要；需要时强制键存在。
func idemFrom(r *http.Request, body []byte, required bool) (service.Idem, error) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key != "" && len(key) > 200 {
		return service.Idem{}, fmt.Errorf("%w: Idempotency-Key too long", domain.ErrInvalidRequest)
	}
	if key == "" && required {
		return service.Idem{}, fmt.Errorf("%w: Idempotency-Key required", domain.ErrInvalidRequest)
	}
	return service.Idem{
		Key:    key,
		Digest: service.RequestDigest(r.Method, r.URL.Path, r.URL.RawQuery, body),
	}, nil
}

// writeCall 执行写操作：校验/计算幂等键后调用 service 并写响应。
func (s *Server) writeCall(
	w http.ResponseWriter,
	r *http.Request,
	body []byte,
	required bool,
	call func(idem service.Idem) (service.Response, error),
) {
	idem, err := idemFrom(r, body, required)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	resp, err := call(idem)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeServiceResponse(w, resp)
}

func pathID(r *http.Request, name string) (string, error) {
	value := r.PathValue(name)
	if value == "" || len(value) > maxIDLength {
		return "", fmt.Errorf("%w: invalid path parameter %s", domain.ErrInvalidRequest, name)
	}
	if value == "." || value == ".." || strings.ContainsAny(value, "\x00/\\") {
		return "", fmt.Errorf("%w: invalid path parameter %s", domain.ErrInvalidRequest, name)
	}
	return value, nil
}

func queryValue(r *http.Request, name string) string {
	return strings.TrimSpace(r.URL.Query().Get(name))
}

func queryInt(r *http.Request, name string, fallback int) (int, error) {
	raw := queryValue(r, name)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid integer query parameter %s", domain.ErrInvalidRequest, name)
	}
	return n, nil
}

func queryInt64(r *http.Request, name string) (int64, bool, error) {
	raw := queryValue(r, name)
	if raw == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("%w: invalid integer query parameter %s", domain.ErrInvalidRequest, name)
	}
	return n, true, nil
}

// routeEntry 是一条内部 API 路由。
//
// pattern 与 contracts/openapi.yaml 的 path 使用同一模板语法；method 使用
// 大写 HTTP 方法。contract_drift_test.go 直接枚举本表并与契约比对——新增或
// 修改端点时必须先改契约，再登记到这里。
type routeEntry struct {
	method  string
	pattern string
	handler http.HandlerFunc
}

// routeTable 返回全部内部 API 路由。
//
// 顺序只用于阅读和分组，ServeMux 的匹配不依赖注册顺序。第三方本机 API
// （/v1/audio/*）由独立回环端口实现，不在本表。
func (s *Server) routeTable() []routeEntry {
	return []routeEntry{
		// health
		{http.MethodGet, "/health/live", s.handleHealth},

		// system / capabilities / devices
		{http.MethodGet, "/api/v1/system", s.handleSystem},
		{http.MethodPost, "/api/v1/system/update-preparation", s.handleUpdatePreparation},
		{http.MethodGet, "/api/v1/capabilities", s.handleCapabilities},
		{http.MethodGet, "/api/v1/devices", s.handleDevices},
		{http.MethodGet, "/api/v1/environment", s.handleEnvironment},

		// installations
		{http.MethodPost, "/api/v1/installations/plan", s.handlePlanInstallation},
		{http.MethodGet, "/api/v1/installations", s.handleListInstallations},
		{http.MethodPost, "/api/v1/installations", s.handleCreateInstallation},
		{http.MethodGet, "/api/v1/installations/{id}", s.handleGetInstallation},
		{http.MethodPost, "/api/v1/installations/{id}/cancel", s.handleCancelInstallation},
		{http.MethodDelete, "/api/v1/capabilities/{slot}/installation", s.handleDeleteInstallation},
		{http.MethodPost, "/api/v1/capabilities/{slot}/rollback", s.handleRollbackInstallation},

		// projects
		{http.MethodGet, "/api/v1/projects", s.handleListProjects},
		{http.MethodPost, "/api/v1/projects", s.handleCreateProject},
		{http.MethodGet, "/api/v1/projects/{id}", s.handleGetProject},
		{http.MethodPatch, "/api/v1/projects/{id}", s.handleUpdateProject},
		{http.MethodDelete, "/api/v1/projects/{id}", s.handleDeleteProject},

		// assets
		{http.MethodPost, "/api/v1/assets/imports", s.handleImportAssets},
		{http.MethodPost, "/api/v1/assets/{id}/relocate", s.handleRelocateAsset},
		{http.MethodGet, "/api/v1/assets/{id}/content", s.handleAssetContent},

		// workflow runs / preflight / tasks
		{http.MethodPost, "/api/v1/tasks/preflight", s.handlePreflight},
		{http.MethodPost, "/api/v1/workflow-runs", s.handleCreateWorkflowRun},
		{http.MethodGet, "/api/v1/workflow-runs/{id}", s.handleGetWorkflowRun},
		{http.MethodPost, "/api/v1/workflow-runs/{id}/pause", s.handlePauseWorkflowRun},
		{http.MethodPost, "/api/v1/workflow-runs/{id}/resume", s.handleResumeWorkflowRun},
		{http.MethodGet, "/api/v1/tasks", s.handleListTasks},
		{http.MethodPost, "/api/v1/tasks", s.handleCreateTask},
		{http.MethodGet, "/api/v1/tasks/{id}", s.handleGetTask},
		{http.MethodGet, "/api/v1/tasks/{id}/execution-log", s.handleGetTaskExecutionLog},
		{http.MethodPost, "/api/v1/tasks/{id}/cancel", s.handleCancelTask},
		{http.MethodPost, "/api/v1/tasks/{id}/retry", s.handleRetryTask},

		// reviews
		{http.MethodGet, "/api/v1/projects/{id}/reviews", s.handleListReviews},
		{http.MethodGet, "/api/v1/reviews/{id}", s.handleGetReview},
		{http.MethodPost, "/api/v1/reviews/{id}/approve", s.handleApproveReview},

		// segments
		{http.MethodGet, "/api/v1/segments/{id}/candidates", s.handleListCandidates},
		{http.MethodPost, "/api/v1/segments/{id}/candidates", s.handleGenerateCandidates},
		{http.MethodGet, "/api/v1/segments/{id}/selection", s.handleGetSelection},
		{http.MethodPut, "/api/v1/segments/{id}/selection", s.handleSelectCandidate},

		// engines / tracks / exports / storage
		{http.MethodGet, "/api/v1/engines/{slot}/config", s.handleGetEngineConfig},
		{http.MethodPut, "/api/v1/engines/{slot}/config", s.handleUpdateEngineConfig},
		{http.MethodPost, "/api/v1/subtitle-tracks/imports", s.handleImportSubtitleTrack},
		{http.MethodPut, "/api/v1/subtitle-tracks/{id}/revisions", s.handleSaveSubtitleRevision},
		{http.MethodGet, "/api/v1/custom-pipeline/templates", s.handleListCustomPipelineTemplates},
		{http.MethodPut, "/api/v1/custom-pipeline/templates", s.handleSaveCustomPipelineTemplates},
		{http.MethodGet, "/api/v1/exports", s.handleListExports},
		{http.MethodPost, "/api/v1/exports", s.handleCreateExport},
		{http.MethodGet, "/api/v1/storage/usage", s.handleStorageUsage},
		{http.MethodPost, "/api/v1/storage/cleanup", s.handleCleanupStorage},

		// sync
		{http.MethodGet, "/api/v1/snapshot", s.handleSnapshot},
		{http.MethodGet, "/api/v1/events", s.handleEvents},
		{http.MethodGet, "/api/v1/events/cursor", s.handleEventCursor},

		// local api management (internal)
		{http.MethodGet, "/api/v1/local-api/settings", s.handleGetLocalApiSettings},
		{http.MethodPut, "/api/v1/local-api/settings", s.handleUpdateLocalApiSettings},
		{http.MethodPost, "/api/v1/local-api/token", s.handleIssueLocalApiToken},
		{http.MethodDelete, "/api/v1/local-api/token", s.handleRevokeLocalApiToken},
		{http.MethodGet, "/api/v1/local-api/stats", s.handleLocalApiStats},
	}
}

// routes 注册全部契约端点；注册内容只来自 routeTable。
func (s *Server) routes() {
	for _, route := range s.routeTable() {
		s.mux.HandleFunc(route.method+" "+route.pattern, route.handler)
	}
}
