package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/updateguard"
)

type updatePreparationResultBody struct {
	SchemaVersion int     `json:"schema_version"`
	OperationID   string  `json:"operation_id"`
	WorkspaceID   string  `json:"workspace_id"`
	InstanceID    string  `json:"instance_id"`
	ServerEpoch   int64   `json:"server_epoch"`
	State         string  `json:"state"`
	Reason        *string `json:"reason"`
}

func preparationRequest(mode, operation string) map[string]any {
	return map[string]any{
		"mode":         mode,
		"operation_id": operation,
		"workspace_id": "ws-test",
		"instance_id":  "instance-test",
		"server_epoch": 7,
	}
}

func (e *testEnv) prepareUpdate(mode, operation string) httpResult {
	e.t.Helper()
	return e.request(http.MethodPost, "/api/v1/system/update-preparation",
		preparationRequest(mode, operation))
}

func decodePreparation(t *testing.T, result httpResult) updatePreparationResultBody {
	t.Helper()
	if result.Status != http.StatusOK {
		t.Fatalf("preparation status = %d, want 200 (body=%s)", result.Status, result.Body)
	}
	return decodeInto[updatePreparationResultBody](t, result)
}

// TestUpdatePreparationEndpointValidation 覆盖鉴权、request_id、坏形状与身份。
func TestUpdatePreparationEndpointValidation(t *testing.T) {
	e := newTestEnv(t)

	unauthorized := e.request(http.MethodPost, "/api/v1/system/update-preparation",
		preparationRequest("check", "op-auth"), withoutToken())
	if unauthorized.Status != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Status)
	}

	missingRequestID := e.request(http.MethodPost, "/api/v1/system/update-preparation",
		preparationRequest("check", "op-request-id"),
		func(r *http.Request) { r.Header.Del("request_id") })
	if missingRequestID.Status != http.StatusBadRequest {
		t.Fatalf("missing request_id status = %d, want 400", missingRequestID.Status)
	}
	if code := missingRequestID.code(t); code != string(domain.ErrorCodeInvalidRequest) {
		t.Fatalf("missing request_id code = %s", code)
	}

	unknownField := e.request(http.MethodPost, "/api/v1/system/update-preparation",
		map[string]any{
			"mode": "check", "operation_id": "op", "workspace_id": "ws-test",
			"instance_id": "instance-test", "server_epoch": 7, "extra": true,
		})
	if unknownField.Status != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, want 400", unknownField.Status)
	}

	badMode := e.request(http.MethodPost, "/api/v1/system/update-preparation",
		map[string]any{
			"mode": "install", "operation_id": "op", "workspace_id": "ws-test",
			"instance_id": "instance-test", "server_epoch": 7,
		})
	if badMode.Status != http.StatusBadRequest {
		t.Fatalf("bad mode status = %d, want 400", badMode.Status)
	}

	wrongIdentity := e.request(http.MethodPost, "/api/v1/system/update-preparation",
		map[string]any{
			"mode": "prepare", "operation_id": "op", "workspace_id": "ws-test",
			"instance_id": "instance-other", "server_epoch": 7,
		})
	if wrongIdentity.Status != http.StatusConflict {
		t.Fatalf("identity mismatch status = %d, want 409", wrongIdentity.Status)
	}
	if code := wrongIdentity.code(t); code != string(domain.ErrorCodeRevisionConflict) {
		t.Fatalf("identity mismatch code = %s", code)
	}

	idle := decodePreparation(t, e.prepareUpdate("check", "op-valid"))
	if idle.State != updateguard.StateIdle || idle.Reason != nil {
		t.Fatalf("check = %+v, want idle/null", idle)
	}
	if idle.OperationID != "op-valid" || idle.WorkspaceID != "ws-test" ||
		idle.InstanceID != "instance-test" || idle.ServerEpoch != 7 || idle.SchemaVersion != 1 {
		t.Fatalf("identity echo = %+v", idle)
	}
}

// TestUpdatePreparationSealsRequestsAndSharesPreparedProof 覆盖空闲封闸、
// 普通请求拒绝、排除项保持可用与同一操作复用证明。
func TestUpdatePreparationSealsRequestsAndSharesPreparedProof(t *testing.T) {
	ownership := &fakeUpdateOwnership{}
	e := newTestEnv(t, func(cfg *envConfig) { cfg.ownership = ownership })
	if _, err := e.svc.CreateProject(context.Background(), service.Idem{}, service.CreateProjectRequest{Name: "p"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	prepared := decodePreparation(t, e.prepareUpdate("prepare", "op-1"))
	if prepared.State != updateguard.StatePrepared || prepared.Reason != nil {
		t.Fatalf("prepare = %+v, want prepared/null", prepared)
	}
	if !e.updates.Sealed() || !e.store.Sealed() {
		t.Fatal("prepare did not seal")
	}
	if ownership.SettleCalls() != 1 {
		t.Fatalf("settle calls = %d, want 1", ownership.SettleCalls())
	}

	blocked := e.request(http.MethodGet, "/api/v1/projects", nil)
	if blocked.Status != http.StatusServiceUnavailable {
		t.Fatalf("ordinary GET after seal = %d, want 503", blocked.Status)
	}
	if code := blocked.code(t); code != string(domain.ErrorCodeCoreNotReady) {
		t.Fatalf("ordinary GET code = %s", code)
	}
	var envelope struct {
		Error domain.ErrorInfo `json:"error"`
	}
	if err := json.Unmarshal(blocked.Body, &envelope); err != nil {
		t.Fatalf("decode blocked body: %v", err)
	}
	if envelope.Error.MessageKey != "error.update_preparing" {
		t.Fatalf("blocked message key = %q", envelope.Error.MessageKey)
	}
	if write := e.request(http.MethodPost, "/api/v1/projects", map[string]any{"name": "x"}); write.Status != http.StatusServiceUnavailable {
		t.Fatalf("ordinary POST after seal = %d, want 503", write.Status)
	}

	// health 与纯 system 握手仍然可用（契约排除项）。
	if health := e.request(http.MethodGet, "/health/live", nil); health.Status != http.StatusOK {
		t.Fatalf("health after seal = %d", health.Status)
	}
	if system := e.request(http.MethodGet, "/api/v1/system", nil); system.Status != http.StatusOK {
		t.Fatalf("system after seal = %d", system.Status)
	}

	same := decodePreparation(t, e.prepareUpdate("prepare", "op-1"))
	if same.State != updateguard.StatePrepared {
		t.Fatalf("same op prepare = %+v, want prepared", same)
	}
	if ownership.SettleCalls() != 1 {
		t.Fatalf("same op re-ran cleanup: %d", ownership.SettleCalls())
	}
	other := decodePreparation(t, e.prepareUpdate("prepare", "op-2"))
	if other.State != updateguard.StateBlocked || other.Reason == nil || *other.Reason != updateguard.ReasonRestartRequired {
		t.Fatalf("other op prepare = %+v, want blocked/restart_required", other)
	}
	check := decodePreparation(t, e.prepareUpdate("check", "op-2"))
	if check.State != updateguard.StateBlocked || check.Reason == nil || *check.Reason != updateguard.ReasonRestartRequired {
		t.Fatalf("check after seal = %+v, want blocked/restart_required", check)
	}
}

// TestAdmissionMiddlewareHoldsThroughHandler 证明普通 handler 从进入到处
// 理结束都持有准入（含事务外文件 IO/GET 副作用），prepare 不会越过在飞
// handler 封闸。
func TestAdmissionMiddlewareHoldsThroughHandler(t *testing.T) {
	ownership := &fakeUpdateOwnership{}
	e := newTestEnv(t, func(cfg *envConfig) { cfg.ownership = ownership })
	entered := make(chan struct{})
	release := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	})
	wrapped := (&Server{updates: e.updates}).admissionMiddleware(inner)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/projects/project-1", nil))
	}()
	<-entered

	blocked := decodePreparation(t, e.prepareUpdate("prepare", "op-handler"))
	if blocked.State != updateguard.StateBlocked || blocked.Reason == nil || *blocked.Reason != updateguard.ReasonRequestInFlight {
		t.Fatalf("prepare during ordinary handler = %+v, want blocked/request_in_flight", blocked)
	}
	if e.store.Sealed() || ownership.SettleCalls() != 0 {
		t.Fatal("in-flight handler allowed sealing or cleanup")
	}
	close(release)
	<-finished

	prepared := decodePreparation(t, e.prepareUpdate("prepare", "op-handler"))
	if prepared.State != updateguard.StatePrepared {
		t.Fatalf("prepare after handler finished = %+v, want prepared", prepared)
	}
}

// TestUpdatePreparationBlockedByActiveWorkPreservesBusiness 覆盖活动 Run/Task
// 阻断、不清理、不改业务事实。
func TestUpdatePreparationBlockedByActiveWorkPreservesBusiness(t *testing.T) {
	ownership := &fakeUpdateOwnership{}
	e := newTestEnv(t, func(cfg *envConfig) { cfg.ownership = ownership })
	projectID := e.seedProject("活动项目")
	run := e.seedRun(projectID)
	var beforeVersion int64
	if err := e.store.ReadTx(context.Background(), func(tx *store.Tx) error {
		current, err := tx.GetWorkflowRun(context.Background(), run.ID)
		if err != nil {
			return err
		}
		beforeVersion = current.StateVersion
		return nil
	}); err != nil {
		t.Fatalf("read run: %v", err)
	}

	check := decodePreparation(t, e.prepareUpdate("check", "op-active"))
	if check.State != updateguard.StateBlocked || check.Reason == nil || *check.Reason != updateguard.ReasonActiveWork {
		t.Fatalf("check = %+v, want blocked/active_work", check)
	}
	prepare := decodePreparation(t, e.prepareUpdate("prepare", "op-active"))
	if prepare.State != updateguard.StateBlocked || prepare.Reason == nil || *prepare.Reason != updateguard.ReasonActiveWork {
		t.Fatalf("prepare = %+v, want blocked/active_work", prepare)
	}
	if e.store.Sealed() || ownership.SettleCalls() != 0 {
		t.Fatal("blocked preparation sealed or ran cleanup")
	}
	if err := e.store.ReadTx(context.Background(), func(tx *store.Tx) error {
		current, err := tx.GetWorkflowRun(context.Background(), run.ID)
		if err != nil {
			return err
		}
		if current.Status.Terminal() || current.StateVersion != beforeVersion {
			t.Fatalf("run changed: status=%s version=%d, want nonterminal/%d",
				current.Status, current.StateVersion, beforeVersion)
		}
		return nil
	}); err != nil {
		t.Fatalf("read run after: %v", err)
	}
	// 等待期间普通编辑/读取保持可用。
	if list := e.request(http.MethodGet, "/api/v1/projects", nil); list.Status != http.StatusOK {
		t.Fatalf("projects after blocked prepare = %d", list.Status)
	}
}

// blockingProber 让导入在事务外文件探测阶段停留，证明 handler 全程准入。
type blockingProber struct {
	entered chan struct{}
	release chan struct{}
}

func (p *blockingProber) Probe(ctx context.Context, path string) (*domain.MediaMeta, error) {
	select {
	case p.entered <- struct{}{}:
	default:
	}
	<-p.release
	return &domain.MediaMeta{Container: "mp4", DurationMs: 1000, AudioTracks: 1}, nil
}

// TestUpdatePreparationBlocksOnInFlightFileIO 证明事务外文件 IO/探测在飞时
// prepare 快速返回 request_in_flight，不打断该 handler。
func TestUpdatePreparationBlocksOnInFlightFileIO(t *testing.T) {
	prober := &blockingProber{entered: make(chan struct{}, 1), release: make(chan struct{})}
	var releaseOnce sync.Once
	releaseProber := func() { releaseOnce.Do(func() { close(prober.release) }) }
	t.Cleanup(releaseProber)
	ownership := &fakeUpdateOwnership{}
	e := newTestEnv(t, func(cfg *envConfig) {
		cfg.ownership = ownership
		cfg.mediaProber = prober
	})
	projectID := e.seedProject("导入项目")
	mediaPath := filepath.Join(t.TempDir(), "source.mp4")
	if err := os.WriteFile(mediaPath, []byte("fake-media-content"), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}

	type importResult struct {
		status int
		body   []byte
	}
	importDone := make(chan importResult, 1)
	go func() {
		payload, err := json.Marshal(map[string]any{
			"project_id":  string(projectID),
			"file_grants": []string{mediaPath},
			"copy_mode":   false,
		})
		if err != nil {
			importDone <- importResult{status: -1}
			return
		}
		req, err := http.NewRequest(http.MethodPost, e.server.URL+"/api/v1/assets/imports", strings.NewReader(string(payload)))
		if err != nil {
			importDone <- importResult{status: -1}
			return
		}
		req.Header.Set(tokenHeader, e.token)
		req.Header.Set("request_id", "test-import-in-flight")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", store.NewID("idem"))
		resp, err := e.client.Do(req)
		if err != nil {
			importDone <- importResult{status: -1}
			return
		}
		defer resp.Body.Close()
		buf := make([]byte, 0, 1024)
		chunk := make([]byte, 1024)
		for {
			n, readErr := resp.Body.Read(chunk)
			buf = append(buf, chunk[:n]...)
			if readErr != nil {
				break
			}
		}
		importDone <- importResult{status: resp.StatusCode, body: buf}
	}()

	select {
	case <-prober.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("import did not reach media probing")
	}

	prepare := decodePreparation(t, e.prepareUpdate("prepare", "op-io"))
	if prepare.State != updateguard.StateBlocked || prepare.Reason == nil || *prepare.Reason != updateguard.ReasonRequestInFlight {
		t.Fatalf("prepare during file IO = %+v, want blocked/request_in_flight", prepare)
	}
	if e.store.Sealed() || ownership.SettleCalls() != 0 {
		t.Fatal("request_in_flight sealed the store or ran cleanup")
	}
	releaseProber()
	select {
	case result := <-importDone:
		if result.status != http.StatusAccepted {
			t.Fatalf("import status = %d, want 202 (body=%s)", result.status, result.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("import did not finish after prober release")
	}

	final := decodePreparation(t, e.prepareUpdate("prepare", "op-io"))
	if final.State != updateguard.StatePrepared {
		t.Fatalf("prepare after IO settled = %+v, want prepared", final)
	}
}

// TestUpdatePreparationSseDoesNotHoldAdmission 证明 SSE 长连接不占用普通
// 请求准入，封闸可完成。
func TestUpdatePreparationSseDoesNotHoldAdmission(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.server.URL+"/api/v1/events?after_seq=0", nil)
	if err != nil {
		t.Fatalf("new SSE request: %v", err)
	}
	req.Header.Set(tokenHeader, e.token)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("open SSE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d", resp.StatusCode)
	}

	prepared := decodePreparation(t, e.prepareUpdate("prepare", "op-sse"))
	if prepared.State != updateguard.StatePrepared {
		t.Fatalf("prepare with SSE open = %+v, want prepared", prepared)
	}
	cancel()
	// 等待 SSE handler 退出，避免 httptest.Server 清理等待。
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, err := resp.Body.Read(make([]byte, 256)); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SSE handler did not exit after cancellation")
	}
}

// TestUpdatePreparationCleanupFailureStaysSealed 覆盖清理失败：不伪 prepared、
// 普通请求仍被拒绝。
func TestUpdatePreparationCleanupFailureStaysSealed(t *testing.T) {
	ownership := &fakeUpdateOwnership{settle: func(context.Context) error {
		return errors.New("runner lease retained")
	}}
	e := newTestEnv(t, func(cfg *envConfig) { cfg.ownership = ownership })
	if _, err := e.svc.CreateProject(context.Background(), service.Idem{}, service.CreateProjectRequest{Name: "p"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	prepared := decodePreparation(t, e.prepareUpdate("prepare", "op-fail"))
	if prepared.State != updateguard.StateBlocked || prepared.Reason == nil || *prepared.Reason != updateguard.ReasonRestartRequired {
		t.Fatalf("failed cleanup = %+v, want blocked/restart_required", prepared)
	}
	if !e.store.Sealed() {
		t.Fatal("failed cleanup reopened the store")
	}
	if blocked := e.request(http.MethodGet, "/api/v1/projects", nil); blocked.Status != http.StatusServiceUnavailable {
		t.Fatalf("ordinary request after failed cleanup = %d, want 503", blocked.Status)
	}
	again := decodePreparation(t, e.prepareUpdate("prepare", "op-fail"))
	if again.State != updateguard.StateBlocked || *again.Reason != updateguard.ReasonRestartRequired {
		t.Fatalf("repeat failed cleanup = %+v", again)
	}
	if ownership.SettleCalls() != 1 {
		t.Fatalf("cleanup re-ran: %d", ownership.SettleCalls())
	}
}

// TestUpdateAdmissionExclusionTable 锁定唯一排除表：GET 项目副作用不得被排除。
func TestUpdateAdmissionExclusionTable(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodGet, "/health/live", true},
		{http.MethodGet, "/api/v1/system", true},
		{http.MethodGet, "/api/v1/events", true},
		{http.MethodPost, "/api/v1/system/update-preparation", true},
		{http.MethodGet, "/api/v1/projects", false},
		{http.MethodGet, "/api/v1/projects/project-1", false},
		{http.MethodPost, "/api/v1/projects", false},
		{http.MethodGet, "/api/v1/events/cursor", false},
		{http.MethodGet, "/api/v1/capabilities", false},
		{http.MethodGet, "/api/v1/snapshot", false},
	}
	for _, tc := range cases {
		req, err := http.NewRequest(tc.method, "http://127.0.0.1"+tc.path, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if got := updateAdmissionExcluded(req); got != tc.want {
			t.Fatalf("%s %s excluded = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}
