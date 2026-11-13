package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/downloader"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/system"
)

// TestHealthAuthHostOrigin 覆盖 /health/live、实例令牌与 Host/Origin 校验。
func TestHealthAuthHostOrigin(t *testing.T) {
	env := newTestEnv(t)

	health := env.expect(env.request(http.MethodGet, "/health/live", nil, withoutToken()), http.StatusOK)
	if !strings.Contains(string(health.Body), `"ok"`) {
		t.Fatalf("health body = %s", health.Body)
	}

	unauthorized := env.expect(env.request(http.MethodGet, "/api/v1/system", nil, withoutToken()), http.StatusUnauthorized)
	if code := unauthorized.code(t); code != "LOCAL_AUTH_REQUIRED" {
		t.Fatalf("code = %s", code)
	}
	if unauthorized.requestID(t) == "" {
		t.Fatal("error response missing request_id")
	}
	env.expect(env.request(http.MethodGet, "/api/v1/system", nil, withToken("wrong")), http.StatusUnauthorized)

	// Host 校验：非回环 Host 拒绝（DNS rebinding 防护）。
	req, err := http.NewRequest(http.MethodGet, env.server.URL+"/api/v1/system", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(tokenHeader, testToken)
	req.Host = "evil.example:1234"
	resp, err := env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad host status = %d, want 403", resp.StatusCode)
	}

	env.expect(env.request(http.MethodGet, "/api/v1/system", nil, withHeader("Origin", "http://evil.example")), http.StatusForbidden)
	system := env.expect(env.request(http.MethodGet, "/api/v1/system", nil,
		withHeader("Origin", "http://127.0.0.1:9999")), http.StatusOK)
	info := decodeInto[domain.SystemInfo](t, system)
	if info.AppID != "yaomu" || info.WorkspaceID != "ws-test" || info.ServerEpoch != 7 || !info.CoreReady {
		t.Fatalf("system info = %+v", info)
	}
	if info.InstanceID == "" || info.ProtocolVersion == "" {
		t.Fatalf("system info missing fields: %+v", info)
	}
}

// TestCapabilitiesAndDevices 覆盖能力目录与设备清单。
func TestCapabilitiesAndDevices(t *testing.T) {
	env := newTestEnv(t, func(cfg *envConfig) {
		cfg.capabilities = service.StaticCapabilities{States: map[domain.CapabilitySlotKey]domain.SlotState{
			domain.CapabilitySlotASR: domain.SlotStateNeedsSetup,
		}}
	})

	caps := decodeInto[[]domain.Capability](t,
		env.expect(env.request(http.MethodGet, "/api/v1/capabilities", nil), http.StatusOK))
	if len(caps) != 8 {
		t.Fatalf("capability count = %d, want 8", len(caps))
	}
	states := map[domain.CapabilitySlotKey]domain.SlotState{}
	for _, capability := range caps {
		states[capability.Slot] = capability.State
		if capability.RuntimeState != domain.CapabilityRuntimeIdle || capability.RunningTasks != 0 {
			t.Fatalf("capability %s runtime = %s/%d, want idle/0",
				capability.Slot, capability.RuntimeState, capability.RunningTasks)
		}
	}
	if states[domain.CapabilitySlotASR] != domain.SlotStateNeedsSetup {
		t.Fatalf("asr state = %s", states[domain.CapabilitySlotASR])
	}
	if states[domain.CapabilitySlotTranslate] != domain.SlotStateReady {
		t.Fatalf("translate state = %s", states[domain.CapabilitySlotTranslate])
	}

	devices := decodeInto[[]domain.DeviceInfo](t,
		env.expect(env.request(http.MethodGet, "/api/v1/devices", nil), http.StatusOK))
	if len(devices) == 0 {
		t.Fatal("no devices")
	}
	selected := 0
	for _, device := range devices {
		if device.Selected {
			selected++
		}
	}
	if selected != 1 {
		t.Fatalf("selected devices = %d, want 1", selected)
	}
}

// TestEnvironmentSnapshot 覆盖环境快照端点、CUDA 设备与认证。
func TestEnvironmentSnapshot(t *testing.T) {
	total := int64(12 * 1024 * 1024 * 1024)
	cuda := true
	env := newTestEnv(t, func(cfg *envConfig) {
		cfg.environment = fakeEnvironmentProvider{snapshot: service.EnvironmentSnapshot{
			Platform: "windows",
			WSL: &domain.WSLStatus{
				Available:     true,
				Version:       2,
				CUDAAvailable: &cuda,
			},
			Devices: []domain.DeviceInfo{
				{ID: "cpu", Name: "CPU", Kind: domain.DeviceKindCPU},
				{
					ID:               "cuda:0",
					Name:             "NVIDIA GeForce RTX 3060",
					Kind:             domain.DeviceKindCUDA,
					Recommended:      true,
					MemoryTotalBytes: &total,
					DriverVersion:    "555.99",
				},
			},
			Checks: []domain.EnvironmentCheck{
				{
					Code:       "CUDA_AVAILABLE",
					Severity:   domain.PreflightPass,
					MessageKey: "environment.cuda_ready",
					ActionIDs:  []domain.ID{},
				},
			},
		}}
	})

	info := decodeInto[domain.EnvironmentInfo](t,
		env.expect(env.request(http.MethodGet, "/api/v1/environment", nil), http.StatusOK))
	if info.Platform != "windows" {
		t.Fatalf("platform = %q", info.Platform)
	}
	if info.WSL == nil || !info.WSL.Available || info.WSL.Version != 2 || info.WSL.CUDAAvailable == nil || !*info.WSL.CUDAAvailable {
		t.Fatalf("wsl = %+v", info.WSL)
	}
	if len(info.Devices) != 2 {
		t.Fatalf("devices = %+v", info.Devices)
	}
	if !info.Devices[1].Selected || info.Devices[1].Kind != domain.DeviceKindCUDA {
		t.Fatalf("cuda device = %+v", info.Devices[1])
	}
	if info.Devices[1].DriverVersion != "555.99" || info.Devices[1].MemoryTotalBytes == nil {
		t.Fatalf("cuda fields = %+v", info.Devices[1])
	}
	if len(info.Checks) != 1 || info.Checks[0].Code != "CUDA_AVAILABLE" {
		t.Fatalf("checks = %+v", info.Checks)
	}

	unauthorized := env.expect(env.request(http.MethodGet, "/api/v1/environment", nil, withoutToken()), http.StatusUnauthorized)
	if code := unauthorized.code(t); code != "LOCAL_AUTH_REQUIRED" {
		t.Fatalf("code = %s", code)
	}
}

type fakeEnvironmentProvider struct {
	snapshot service.EnvironmentSnapshot
	err      error
}

func (f fakeEnvironmentProvider) Snapshot(context.Context) (service.EnvironmentSnapshot, error) {
	return f.snapshot, f.err
}

type fakeForceEnvironment struct {
	fakeEnvironmentProvider
	forceCalls int
}

func (f *fakeForceEnvironment) ForceRefresh(context.Context) {
	f.forceCalls++
}

func TestEnvironmentRefreshQueryForcesProbe(t *testing.T) {
	force := &fakeForceEnvironment{fakeEnvironmentProvider: fakeEnvironmentProvider{
		snapshot: service.EnvironmentSnapshot{
			Platform: "windows",
			Devices: []domain.DeviceInfo{{
				ID:          "cpu",
				Name:        "CPU",
				Kind:        domain.DeviceKindCPU,
				Recommended: true,
			}},
			Checks: []domain.EnvironmentCheck{},
		},
	}}
	env := newTestEnv(t, func(cfg *envConfig) {
		cfg.environment = force
	})

	env.expect(env.request(http.MethodGet, "/api/v1/environment", nil), http.StatusOK)
	if force.forceCalls != 0 {
		t.Fatalf("force calls = %d, want 0 for normal read", force.forceCalls)
	}
	env.expect(env.request(http.MethodGet, "/api/v1/environment?refresh=true", nil), http.StatusOK)
	if force.forceCalls != 1 {
		t.Fatalf("force calls = %d, want 1 for refresh=true", force.forceCalls)
	}
}

// TestProjectsLifecycleAndIdempotency 覆盖项目 CRUD 与幂等回执语义。
func TestProjectsLifecycleAndIdempotency(t *testing.T) {
	env := newTestEnv(t)

	missingKey := env.expect(env.request(http.MethodPost, "/api/v1/projects",
		map[string]any{"name": "无幂等键"}, withoutIdem()), http.StatusBadRequest)
	if code := missingKey.code(t); code != "INVALID_REQUEST" {
		t.Fatalf("code = %s", code)
	}

	key := "idem-project-1"
	body := map[string]any{"name": "项目一"}
	created := env.expect(env.request(http.MethodPost, "/api/v1/projects", body, withIdemKey(key)), http.StatusCreated)
	project := decodeInto[domain.Project](t, created)
	if project.ID == "" || project.Name != "项目一" || project.SchemaVersion != 1 {
		t.Fatalf("project = %+v", project)
	}
	if project.EpisodeID == "" {
		t.Fatal("new project must have episode_id")
	}
	if project.Assets == nil || project.Tracks == nil || project.Runs == nil {
		t.Fatalf("project arrays must be []: %+v", project)
	}
	if project.Directory == "" {
		t.Fatalf("project directory missing: %+v", project)
	}
	if info, statErr := os.Stat(project.Directory); statErr != nil || !info.IsDir() {
		t.Fatalf("project directory not created: %q err=%v", project.Directory, statErr)
	}

	// 同 key 同摘要：返回原回执。
	replayed := env.expect(env.request(http.MethodPost, "/api/v1/projects", body, withIdemKey(key)), http.StatusCreated)
	replayedProject := decodeInto[domain.Project](t, replayed)
	if replayedProject.ID != project.ID || replayedProject.Directory != project.Directory {
		t.Fatalf("replay project = %+v, want id/directory of %+v", replayedProject, project)
	}
	// 同 key 不同摘要：IDEMPOTENCY_CONFLICT。
	conflict := env.expect(env.request(http.MethodPost, "/api/v1/projects",
		map[string]any{"name": "另一个名字"}, withIdemKey(key)), http.StatusConflict)
	if code := conflict.code(t); code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("code = %s", code)
	}

	list := decodeInto[[]domain.ProjectSummary](t,
		env.expect(env.request(http.MethodGet, "/api/v1/projects", nil), http.StatusOK))
	if len(list) != 1 || list[0].ID != project.ID {
		t.Fatalf("project list = %+v", list)
	}
	if list[0].EpisodeID != project.EpisodeID {
		t.Fatalf("summary episode_id = %q, want %q", list[0].EpisodeID, project.EpisodeID)
	}
	got := decodeInto[domain.Project](t,
		env.expect(env.request(http.MethodGet, "/api/v1/projects/"+project.ID, nil), http.StatusOK))
	if got.ID != project.ID {
		t.Fatalf("project = %+v", got)
	}
	if got.Directory != project.Directory {
		t.Fatalf("project directory = %q, want %q", got.Directory, project.Directory)
	}

	// PATCH 重命名：成功后版本递增，旧版本号返回 409，非法名称返回 400。
	renamed := env.expect(env.request(http.MethodPatch, "/api/v1/projects/"+project.ID,
		map[string]any{"name": "项目一改名", "edit_version": project.EditVersion},
		withIdemKey("idem-project-update")), http.StatusOK)
	renamedProject := decodeInto[domain.Project](t, renamed)
	if renamedProject.Name != "项目一改名" || renamedProject.EditVersion != project.EditVersion+1 {
		t.Fatalf("renamed project = %+v", renamedProject)
	}
	stale := env.expect(env.request(http.MethodPatch, "/api/v1/projects/"+project.ID,
		map[string]any{"name": "过期更新", "edit_version": project.EditVersion},
		withIdemKey("idem-project-update-stale")), http.StatusConflict)
	if code := stale.code(t); code != "REVISION_CONFLICT" {
		t.Fatalf("stale code = %s", code)
	}
	env.expect(env.request(http.MethodPatch, "/api/v1/projects/"+project.ID,
		map[string]any{"name": "   "}, withIdemKey("idem-project-update-invalid")), http.StatusBadRequest)
	env.expect(env.request(http.MethodPatch, "/api/v1/projects/missing",
		map[string]any{"name": "不存在"}, withIdemKey("idem-project-update-missing")), http.StatusNotFound)

	env.expect(env.request(http.MethodGet, "/api/v1/projects/missing", nil), http.StatusNotFound)

	env.expect(env.request(http.MethodDelete, "/api/v1/projects/"+project.ID, nil), http.StatusNoContent)
	env.expect(env.request(http.MethodGet, "/api/v1/projects/"+project.ID, nil), http.StatusNotFound)

	// 存在活跃任务时删除项目 → 409。
	busyProject := env.seedProject("活跃项目")
	env.expect(env.request(http.MethodPost, "/api/v1/tasks", map[string]any{
		"project_id": busyProject,
		"kind":       "node",
		"node_key":   "02_analyze",
	}), http.StatusAccepted)
	busy := env.expect(env.request(http.MethodDelete, "/api/v1/projects/"+busyProject, nil), http.StatusConflict)
	if busy.code(t) == "" {
		t.Fatal("expected conflict code while tasks are active")
	}
}

// TestAssetsImportAndContent 覆盖媒体导入与受控内容读取（含 Range）。
func TestAssetsImportAndContent(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("资产项目")

	sourceDir := t.TempDir()
	source := filepath.Join(sourceDir, "sample.wav")
	payload := []byte("0123456789abcdef")
	if err := os.WriteFile(source, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	imported := env.expect(env.request(http.MethodPost, "/api/v1/assets/imports", map[string]any{
		"project_id":  projectID,
		"file_grants": []string{source},
		"copy_mode":   false,
	}), http.StatusAccepted)
	result := decodeInto[service.AssetImportResult](t, imported)
	if len(result.AssetIDs) != 1 {
		t.Fatalf("asset ids = %v", result.AssetIDs)
	}
	assetID := result.AssetIDs[0]

	content := env.expect(env.request(http.MethodGet, "/api/v1/assets/"+assetID+"/content", nil), http.StatusOK)
	if !bytes.Equal(content.Body, payload) {
		t.Fatalf("content = %q", content.Body)
	}
	ranged := env.expect(env.request(http.MethodGet, "/api/v1/assets/"+assetID+"/content", nil,
		withHeader("Range", "bytes=0-3")), http.StatusPartialContent)
	if string(ranged.Body) != "0123" {
		t.Fatalf("ranged body = %q", ranged.Body)
	}
	env.expect(env.request(http.MethodGet, "/api/v1/assets/missing/content", nil), http.StatusNotFound)

	relative := env.expect(env.request(http.MethodPost, "/api/v1/assets/imports", map[string]any{
		"project_id":  projectID,
		"file_grants": []string{"relative.wav"},
		"copy_mode":   false,
	}), http.StatusBadRequest)
	if code := relative.code(t); code != "INVALID_REQUEST" {
		t.Fatalf("code = %s", code)
	}

	// copy_mode 把文件复制进应用拥有的媒体目录。
	copied := env.expect(env.request(http.MethodPost, "/api/v1/assets/imports", map[string]any{
		"project_id":  projectID,
		"file_grants": []string{source},
		"copy_mode":   true,
	}), http.StatusAccepted)
	copiedResult := decodeInto[service.AssetImportResult](t, copied)
	asset, err := env.store.GetAsset(context.Background(), copiedResult.AssetIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(asset.Path, filepath.Join(env.dataDir, "media")) {
		t.Fatalf("copied asset path = %s", asset.Path)
	}
	env.expect(env.request(http.MethodGet, "/api/v1/assets/"+string(copiedResult.AssetIDs[0])+"/content", nil), http.StatusOK)
}

// TestPreflightRunPauseResumeTaskGate 覆盖预检、Run 暂停/恢复与派发闸门。
func TestPreflightRunPauseResumeTaskGate(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("流水线项目")

	preflightInput := map[string]any{
		"project_id":       projectID,
		"name":             "译制计划",
		"source_language":  "en",
		"target_languages": []string{"zh"},
		"extract":          map[string]any{"mode": "asr"},
	}
	// 引用未知项目属于请求体错误（400），不是路径寻址的 404。
	env.expect(env.request(http.MethodPost, "/api/v1/tasks/preflight", map[string]any{
		"project_id":       "missing",
		"name":             "x",
		"source_language":  "en",
		"target_languages": []string{"zh"},
	}), http.StatusBadRequest)

	preflight := env.expect(env.request(http.MethodPost, "/api/v1/tasks/preflight", preflightInput), http.StatusOK)
	result := decodeInto[domain.PreflightResult](t, preflight)
	if result.PlanID == "" || result.PlanHash == "" {
		t.Fatalf("preflight result = %+v", result)
	}
	for _, issue := range result.Issues {
		if issue.Severity == domain.PreflightBlocker {
			t.Fatalf("unexpected blocker: %+v", issue)
		}
	}

	run := env.seedRun(projectID)
	env.freezeCandidateFixture(run)
	if run.Status != domain.RunStatusQueued || len(run.Nodes) != 8 {
		t.Fatalf("run = %+v", run)
	}
	gotRun := decodeInto[domain.WorkflowRun](t,
		env.expect(env.request(http.MethodGet, "/api/v1/workflow-runs/"+run.ID, nil), http.StatusOK))
	if gotRun.ID != run.ID || gotRun.Status != domain.RunStatusQueued || len(gotRun.Nodes) != 8 {
		t.Fatalf("workflow run snapshot = %+v", gotRun)
	}
	env.expect(env.request(http.MethodGet, "/api/v1/workflow-runs/missing", nil), http.StatusNotFound)

	createTask := func() domain.Task {
		res := env.expect(env.request(http.MethodPost, "/api/v1/tasks", map[string]any{
			"project_id": projectID,
			"run_id":     run.ID,
			"kind":       "node",
			"node_key":   "02_analyze",
		}), http.StatusAccepted)
		return decodeInto[domain.Task](t, res)
	}
	taskA := createTask()
	if taskA.Status != domain.TaskStatusQueued {
		t.Fatalf("task status = %s", taskA.Status)
	}
	if len(taskA.Steps) != 1 || taskA.Steps[0].AttemptNo != 1 {
		t.Fatalf("task steps = %+v", taskA.Steps)
	}

	list := decodeInto[domain.TaskList](t,
		env.expect(env.request(http.MethodGet, "/api/v1/tasks?project_id="+projectID, nil), http.StatusOK))
	if len(list.Items) != 1 || list.Items[0].ID != taskA.ID {
		t.Fatalf("task list = %+v", list)
	}
	got := decodeInto[domain.Task](t,
		env.expect(env.request(http.MethodGet, "/api/v1/tasks/"+taskA.ID, nil), http.StatusOK))
	if got.ID != taskA.ID {
		t.Fatalf("task = %+v", got)
	}

	// 暂停：没有执行中任务时立即收敛到 PAUSED，排队任务只登记为 PAUSED。
	paused := env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/pause", nil), http.StatusAccepted)
	pausedRun := decodeInto[domain.WorkflowRun](t, paused)
	if pausedRun.Status != domain.RunStatusPaused {
		t.Fatalf("paused run status = %s", pausedRun.Status)
	}
	parked := decodeInto[domain.Task](t,
		env.expect(env.request(http.MethodGet, "/api/v1/tasks/"+taskA.ID, nil), http.StatusOK))
	if parked.Status != domain.TaskStatusPaused {
		t.Fatalf("parked task status = %s", parked.Status)
	}
	taskB := createTask()
	if taskB.Status != domain.TaskStatusPaused {
		t.Fatalf("task while paused status = %s", taskB.Status)
	}

	// 暂停中仍允许保存编辑与登记候选，但生成任务只登记为 PAUSED。
	track := env.seedTrack(projectID, "zh", []domain.Segment{{ID: "seg-1", StartMs: 0, EndMs: 800, Text: "paused edit"}})
	env.expect(env.request(http.MethodPut, "/api/v1/subtitle-tracks/"+track.ID+"/revisions", map[string]any{
		"base_revision_id": track.CurrentRevisionID,
		"segments":         []map[string]any{{"id": "seg-1", "start_ms": 0, "end_ms": 800, "text": "edited while paused"}},
	}), http.StatusCreated)
	job := env.expect(env.request(http.MethodPost, "/api/v1/segments/seg-1/candidates",
		map[string]any{"language": "zh"}), http.StatusAccepted)
	candidateJob := decodeInto[service.SegmentCandidateJob](t, job)
	candidateTask := decodeInto[domain.Task](t,
		env.expect(env.request(http.MethodGet, "/api/v1/tasks/"+candidateJob.TaskID, nil), http.StatusOK))
	if candidateTask.Status != domain.TaskStatusPaused {
		t.Fatalf("candidate task status = %s while run paused", candidateTask.Status)
	}

	// 恢复：重新预检后回到 QUEUED，暂停任务重新排队。
	resumed := env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/resume", nil), http.StatusAccepted)
	resumedRun := decodeInto[domain.WorkflowRun](t, resumed)
	if resumedRun.Status != domain.RunStatusQueued {
		t.Fatalf("resumed run status = %s", resumedRun.Status)
	}
	for _, id := range []string{taskA.ID, taskB.ID} {
		task := decodeInto[domain.Task](t,
			env.expect(env.request(http.MethodGet, "/api/v1/tasks/"+id, nil), http.StatusOK))
		if task.Status != domain.TaskStatusQueued {
			t.Fatalf("task %s status = %s after resume", id, task.Status)
		}
	}

	// 非 PAUSED 的 Run 再次恢复 → 409 状态冲突（契约未为 resume 声明 400）。
	env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/resume", nil), http.StatusConflict)

	// 取消：排队任务直接 CANCELED；终态重试返回冲突。
	canceled := env.expect(env.request(http.MethodPost, "/api/v1/tasks/"+taskB.ID+"/cancel", nil), http.StatusAccepted)
	if task := decodeInto[domain.Task](t, canceled); task.Status != domain.TaskStatusCanceled {
		t.Fatalf("canceled status = %s", task.Status)
	}
	retryCanceled := env.expect(env.request(http.MethodPost, "/api/v1/tasks/"+taskB.ID+"/retry", nil), http.StatusConflict)
	if code := retryCanceled.code(t); code != "REVISION_CONFLICT" {
		t.Fatalf("retry canceled code = %s", code)
	}

	// FAILED 任务重试：新 attempt 编号单调递增。
	if _, err := env.store.DB().Exec(
		`UPDATE tasks SET status = 'FAILED', state_version = state_version + 1 WHERE id = ?`, taskA.ID); err != nil {
		t.Fatal(err)
	}
	retried := env.expect(env.request(http.MethodPost, "/api/v1/tasks/"+taskA.ID+"/retry", nil), http.StatusAccepted)
	retriedTask := decodeInto[domain.Task](t, retried)
	if retriedTask.Status != domain.TaskStatusQueued {
		t.Fatalf("retried status = %s", retriedTask.Status)
	}
	if len(retriedTask.Steps) != 1 || retriedTask.Steps[0].AttemptNo != 2 {
		t.Fatalf("retried steps = %+v", retriedTask.Steps)
	}

	// 终止 Run 拒绝新增执行。
	if _, err := env.store.DB().Exec(
		`UPDATE workflow_runs SET status = 'SUCCEEDED', state_version = state_version + 1 WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	rejected := env.expect(env.request(http.MethodPost, "/api/v1/tasks", map[string]any{
		"project_id": projectID,
		"run_id":     run.ID,
		"kind":       "node",
		"node_key":   "03_separate",
	}), http.StatusConflict)
	if code := rejected.code(t); code == "" {
		t.Fatal("expected error code for terminal run dispatch")
	}
}

// TestPreflightDiskSpaceAdmission 覆盖磁盘健康线警告与永久性不足的 507 路径。
func TestPreflightDiskSpaceAdmission(t *testing.T) {
	const gib = int64(1) << 30
	fakeSpace := func(available int64) func(string) (system.DiskSpace, error) {
		return func(string) (system.DiskSpace, error) {
			return system.DiskSpace{TotalBytes: 100 * gib, AvailableBytes: available}, nil
		}
	}
	runInput := func(projectID string, inputIDs ...string) map[string]any {
		return map[string]any{
			"project_id":         projectID,
			"name":               "空间预检计划",
			"source_language":    "en",
			"target_languages":   []string{"zh"},
			"extract":            map[string]any{"mode": "asr"},
			"input_revision_ids": inputIDs,
		}
	}

	// 可用 20GiB 低于 30GiB 健康线；输入 10MiB 远小于可用空间 → warning，创建放行。
	warnEnv := newTestEnv(t, func(cfg *envConfig) { cfg.statfs = fakeSpace(20 * gib) })
	warnProject := warnEnv.seedProject("空间警告项目")
	warnAsset := warnEnv.seedVideoAsset(warnProject, 10<<20)
	warnResult := decodeInto[domain.PreflightResult](t,
		warnEnv.expect(warnEnv.request(http.MethodPost, "/api/v1/tasks/preflight",
			runInput(warnProject, warnAsset.ID)), http.StatusOK))
	var lowWarning bool
	for _, issue := range warnResult.Issues {
		if issue.Code == string(domain.ErrorCodeInsufficientDisk) {
			if issue.Severity != domain.PreflightWarning {
				t.Fatalf("low disk issue = %+v, want warning", issue)
			}
			lowWarning = true
		}
	}
	if !lowWarning {
		t.Fatalf("missing low disk warning: %+v", warnResult.Issues)
	}
	warnEnv.expect(warnEnv.request(http.MethodPost, "/api/v1/workflow-runs",
		runInput(warnProject, warnAsset.ID)), http.StatusCreated)

	// 可用 2GiB，输入 2GiB（估算需求 6GiB）→ blocker，创建返回 507 INSUFFICIENT_DISK。
	blockEnv := newTestEnv(t, func(cfg *envConfig) { cfg.statfs = fakeSpace(2 * gib) })
	blockProject := blockEnv.seedProject("空间不足项目")
	blockAsset := blockEnv.seedVideoAsset(blockProject, 2*gib)
	blockResult := decodeInto[domain.PreflightResult](t,
		blockEnv.expect(blockEnv.request(http.MethodPost, "/api/v1/tasks/preflight",
			runInput(blockProject, blockAsset.ID)), http.StatusOK))
	var blocker bool
	for _, issue := range blockResult.Issues {
		if issue.Severity == domain.PreflightBlocker && issue.Code == string(domain.ErrorCodeInsufficientDisk) {
			blocker = true
			if len(issue.ActionIDs) == 0 {
				t.Fatalf("blocker missing action ids: %+v", issue)
			}
		}
	}
	if !blocker {
		t.Fatalf("missing INSUFFICIENT_DISK blocker: %+v", blockResult.Issues)
	}
	blocked := blockEnv.expect(blockEnv.request(http.MethodPost, "/api/v1/workflow-runs",
		runInput(blockProject, blockAsset.ID)), http.StatusInsufficientStorage)
	if code := blocked.code(t); code != string(domain.ErrorCodeInsufficientDisk) {
		t.Fatalf("507 code = %s", code)
	}
}

// TestExportInsufficientDiskReturns507 覆盖导出写前准入：数据分区充足但输出分区
// 不足时，媒体导出返回 507 INSUFFICIENT_DISK，而不是误判数据分区或静默放行。
func TestExportInsufficientDiskReturns507(t *testing.T) {
	const gib = int64(1) << 30
	outDir := t.TempDir()
	env := newTestEnv(t, func(cfg *envConfig) {
		cfg.statfs = func(path string) (system.DiskSpace, error) {
			if filepath.Clean(path) == filepath.Clean(outDir) {
				return system.DiskSpace{TotalBytes: 10 * gib, AvailableBytes: 1 * gib}, nil
			}
			return system.DiskSpace{TotalBytes: 200 * gib, AvailableBytes: 100 * gib}, nil
		}
	})
	projectID := env.seedProject("导出空间项目")
	asset := env.seedVideoAsset(projectID, 2*gib)
	run := decodeInto[domain.WorkflowRun](t, env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs",
		map[string]any{
			"project_id":         projectID,
			"name":               "导出空间计划",
			"source_language":    "en",
			"target_languages":   []string{"zh"},
			"extract":            map[string]any{"mode": "asr"},
			"input_revision_ids": []string{asset.ID},
		}), http.StatusCreated))
	track := env.seedTrack(projectID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 1000, Text: "你好"},
	})
	env.seedApprovedMix(run.ID, track, "zh")

	// 冻结输入 2GiB（估算需求 6GiB）超过输出分区可用 1GiB → 507。
	blocked := env.expect(env.request(http.MethodPost, "/api/v1/exports", map[string]any{
		"project_id":       projectID,
		"run_id":           run.ID,
		"kind":             "video",
		"format":           "mp4_h264",
		"output_dir_grant": outDir,
		"naming_template":  "{project}",
	}), http.StatusInsufficientStorage)
	if code := blocked.code(t); code != string(domain.ErrorCodeInsufficientDisk) {
		t.Fatalf("export 507 code = %s", code)
	}
}

// TestReviewsApproveBindingRevision 覆盖审核列表、详情与精确 revision 放行。
func TestReviewsApproveBindingRevision(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("审核项目")
	run := env.seedRun(projectID, "zh")
	track := env.seedTrack(projectID, "zh", []domain.Segment{{
		ID: "seg-1", StartMs: 0, EndMs: 1000, Text: "hello",
	}})

	saved := env.expect(env.request(http.MethodPut, "/api/v1/subtitle-tracks/"+track.ID+"/revisions", map[string]any{
		"base_revision_id": track.CurrentRevisionID,
		"segments": []map[string]any{
			{"id": "seg-1", "start_ms": 0, "end_ms": 1000, "text": "hello world"},
		},
	}), http.StatusCreated)
	revision := decodeInto[domain.SubtitleRevision](t, saved)
	if revision.ID == track.CurrentRevisionID {
		t.Fatal("revision was not advanced")
	}

	gates := decodeInto[[]domain.ReviewGate](t,
		env.expect(env.request(http.MethodGet, "/api/v1/projects/"+projectID+"/reviews", nil), http.StatusOK))
	var translation, delivery *domain.ReviewGate
	for i := range gates {
		switch gates[i].Type {
		case domain.ReviewTypeTranslation:
			translation = &gates[i]
		case domain.ReviewTypeDelivery:
			delivery = &gates[i]
		}
	}
	if translation == nil || delivery == nil {
		t.Fatalf("gates = %+v", gates)
	}
	if translation.Status != domain.ReviewStatusPending || translation.Language != "zh" {
		t.Fatalf("translation gate = %+v", translation)
	}
	if gates[0].RunID != run.ID {
		t.Fatalf("gate run = %s", gates[0].RunID)
	}

	got := decodeInto[domain.ReviewGate](t,
		env.expect(env.request(http.MethodGet, "/api/v1/reviews/"+translation.ID, nil), http.StatusOK))
	if got.RevisionDigest != translation.RevisionDigest {
		t.Fatalf("review digest mismatch")
	}

	wrong := env.expect(env.request(http.MethodPost, "/api/v1/reviews/"+translation.ID+"/approve", map[string]any{
		"revision_digest": "stale-digest",
		"type":            "translation",
	}), http.StatusConflict)
	if code := wrong.code(t); code != "REVISION_CONFLICT" {
		t.Fatalf("wrong digest code = %s", code)
	}

	approved := env.expect(env.request(http.MethodPost, "/api/v1/reviews/"+translation.ID+"/approve", map[string]any{
		"revision_digest": translation.RevisionDigest,
		"type":            "translation",
	}), http.StatusOK)
	granted := decodeInto[domain.ReviewGate](t, approved)
	if granted.Status != domain.ReviewStatusApproved || granted.ConfirmedAt == nil {
		t.Fatalf("approved gate = %+v", granted)
	}
	env.expect(env.request(http.MethodPost, "/api/v1/reviews/"+translation.ID+"/approve", map[string]any{
		"revision_digest": translation.RevisionDigest,
		"type":            "translation",
	}), http.StatusBadRequest)

	// 再次编辑产生新 pending 门槛，旧 approved 摘要不再匹配。
	if _, err := env.store.DB().Exec(`UPDATE workflow_runs SET status = 'WAITING_REVIEW', state_version = state_version + 1 WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	env.expect(env.request(http.MethodPut, "/api/v1/subtitle-tracks/"+track.ID+"/revisions", map[string]any{
		"base_revision_id": revision.ID,
		"segments": []map[string]any{
			{"id": "seg-1", "start_ms": 0, "end_ms": 1200, "text": "hello again"},
		},
	}), http.StatusCreated)
	gatesAfter := decodeInto[[]domain.ReviewGate](t,
		env.expect(env.request(http.MethodGet, "/api/v1/projects/"+projectID+"/reviews?run_id="+run.ID, nil), http.StatusOK))
	pendingAfter := 0
	for _, gate := range gatesAfter {
		if gate.Type == domain.ReviewTypeTranslation && gate.Status == domain.ReviewStatusPending &&
			gate.RevisionDigest != translation.RevisionDigest {
			pendingAfter++
		}
	}
	if pendingAfter == 0 {
		t.Fatalf("no new pending translation gate after edit: %+v", gatesAfter)
	}
	// 旧 approved 不能用于新摘要：新 pending 门槛与旧摘要不一致。
	var newGate *domain.ReviewGate
	for i := range gatesAfter {
		gate := gatesAfter[i]
		if gate.Type == domain.ReviewTypeTranslation && gate.Status == domain.ReviewStatusPending &&
			gate.RevisionDigest != translation.RevisionDigest {
			newGate = &gatesAfter[i]
		}
	}
	if newGate == nil {
		t.Fatalf("no new pending gate: %+v", gatesAfter)
	}
	env.expect(env.request(http.MethodPost, "/api/v1/reviews/"+newGate.ID+"/approve", map[string]any{
		"revision_digest": translation.RevisionDigest,
		"type":            newGate.Type,
	}), http.StatusConflict)
}

// TestSaveSubtitleMergeSplitLineage 覆盖合并/拆分保存、lineage 回读、审核失效与冲突。
func TestSaveSubtitleMergeSplitLineage(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("合并拆分项目")
	run := env.seedRun(projectID, "en")
	track := env.seedTrack(projectID, "en", []domain.Segment{
		{ID: "seg-a", StartMs: 0, EndMs: 1000, Text: "hello"},
		{ID: "seg-b", StartMs: 1000, EndMs: 2000, Text: "world"},
	})

	// 合并：两行变一行；新句段省略 id，Core 分配新 ID 并保留两个直接来源。
	merged := decodeInto[domain.SubtitleRevision](t, env.expect(env.request(
		http.MethodPut, "/api/v1/subtitle-tracks/"+track.ID+"/revisions", map[string]any{
			"base_revision_id": track.CurrentRevisionID,
			"segments": []map[string]any{
				{"start_ms": 0, "end_ms": 2000, "text": "hello world", "lineage": []string{"seg-a", "seg-b"}},
			},
		}), http.StatusCreated))
	if merged.ParentRevisionID != track.CurrentRevisionID {
		t.Errorf("merged parent = %q, want %q", merged.ParentRevisionID, track.CurrentRevisionID)
	}
	if len(merged.Segments) != 1 {
		t.Fatalf("merged segments = %d, want 1", len(merged.Segments))
	}
	mergedSeg := merged.Segments[0]
	if mergedSeg.ID == "" || mergedSeg.ID == "seg-a" || mergedSeg.ID == "seg-b" {
		t.Fatalf("merged segment id = %q, want a new Core-assigned id", mergedSeg.ID)
	}
	if len(mergedSeg.Lineage) != 2 || mergedSeg.Lineage[0] != "seg-a" || mergedSeg.Lineage[1] != "seg-b" {
		t.Errorf("merged lineage = %v, want [seg-a seg-b]", mergedSeg.Lineage)
	}

	// 保存使非终止 Run 产生新的 pending translation/delivery 门槛。
	gates := decodeInto[[]domain.ReviewGate](t, env.expect(env.request(
		http.MethodGet, "/api/v1/projects/"+projectID+"/reviews?run_id="+run.ID, nil), http.StatusOK))
	pendingTranslation, pendingDelivery := 0, 0
	for _, gate := range gates {
		if gate.Status != domain.ReviewStatusPending {
			continue
		}
		switch gate.Type {
		case domain.ReviewTypeTranslation:
			if gate.Language == "en" {
				pendingTranslation++
			}
		case domain.ReviewTypeDelivery:
			pendingDelivery++
		}
	}
	if pendingTranslation == 0 || pendingDelivery == 0 {
		t.Fatalf("gates after merge = %+v, want pending translation+delivery", gates)
	}

	// 拆分：一行变两行，两个新句段都指向合并后的句段。
	split := decodeInto[domain.SubtitleRevision](t, env.expect(env.request(
		http.MethodPut, "/api/v1/subtitle-tracks/"+track.ID+"/revisions", map[string]any{
			"base_revision_id": merged.ID,
			"segments": []map[string]any{
				{"start_ms": 0, "end_ms": 1000, "text": "hello", "lineage": []string{mergedSeg.ID}},
				{"start_ms": 1000, "end_ms": 2000, "text": "world", "lineage": []string{mergedSeg.ID}},
			},
		}), http.StatusCreated))
	if split.ParentRevisionID != merged.ID {
		t.Errorf("split parent = %q, want %q", split.ParentRevisionID, merged.ID)
	}
	if len(split.Segments) != 2 {
		t.Fatalf("split segments = %d, want 2", len(split.Segments))
	}
	left, right := split.Segments[0], split.Segments[1]
	if left.ID == "" || right.ID == "" || left.ID == right.ID {
		t.Fatalf("split ids = %q/%q, want two distinct new ids", left.ID, right.ID)
	}
	for _, seg := range []domain.Segment{left, right} {
		if len(seg.Lineage) != 1 || seg.Lineage[0] != mergedSeg.ID {
			t.Errorf("segment %s lineage = %v, want [%s]", seg.ID, seg.Lineage, mergedSeg.ID)
		}
	}

	// 旧 revision 保持不变；当前指针推进到拆分 revision。
	project := decodeInto[domain.Project](t, env.expect(env.request(
		http.MethodGet, "/api/v1/projects/"+projectID, nil), http.StatusOK))
	var stored domain.SubtitleTrack
	for _, item := range project.Tracks {
		if item.ID == track.ID {
			stored = item
			break
		}
	}
	if stored.ID == "" {
		t.Fatalf("track %s not found in project", track.ID)
	}
	if stored.CurrentRevisionID != split.ID || len(stored.Revisions) != 3 {
		t.Fatalf("track revisions = %+v, want current=%s and 3 revisions", stored.Revisions, split.ID)
	}
	if old := stored.Revisions[0]; old.ID != track.CurrentRevisionID || len(old.Segments) != 2 {
		t.Errorf("old revision mutated: %+v", old)
	}
	if stored.Revisions[2].Segments[0].Lineage[0] != mergedSeg.ID {
		t.Errorf("lineage did not round-trip: %+v", stored.Revisions[2].Segments[0])
	}

	// 非法 lineage：引用不存在或重复的 base 句段 → 400，且不写 revision。
	bad := env.expect(env.request(http.MethodPut, "/api/v1/subtitle-tracks/"+track.ID+"/revisions",
		map[string]any{
			"base_revision_id": split.ID,
			"segments": []map[string]any{
				{"start_ms": 0, "end_ms": 500, "text": "bad", "lineage": []string{"seg-missing"}},
			},
		}), http.StatusBadRequest)
	if code := bad.code(t); code != "INVALID_REQUEST" {
		t.Fatalf("invalid lineage code = %s", code)
	}
	duplicated := env.expect(env.request(http.MethodPut, "/api/v1/subtitle-tracks/"+track.ID+"/revisions",
		map[string]any{
			"base_revision_id": split.ID,
			"segments": []map[string]any{
				{"start_ms": 0, "end_ms": 500, "text": "bad", "lineage": []string{left.ID, left.ID}},
			},
		}), http.StatusBadRequest)
	if code := duplicated.code(t); code != "INVALID_REQUEST" {
		t.Fatalf("duplicate lineage code = %s", code)
	}

	// 过期 base revision：409 冲突，不覆盖当前 revision。
	stale := env.expect(env.request(http.MethodPut, "/api/v1/subtitle-tracks/"+track.ID+"/revisions",
		map[string]any{
			"base_revision_id": merged.ID,
			"segments": []map[string]any{
				{"start_ms": 0, "end_ms": 500, "text": "stale"},
			},
		}), http.StatusConflict)
	if code := stale.code(t); code != "REVISION_CONFLICT" {
		t.Fatalf("stale base code = %s", code)
	}
	after := decodeInto[domain.Project](t, env.expect(env.request(
		http.MethodGet, "/api/v1/projects/"+projectID, nil), http.StatusOK))
	for _, item := range after.Tracks {
		if item.ID == track.ID && item.CurrentRevisionID != split.ID {
			t.Fatalf("rejected saves advanced revision to %s", item.CurrentRevisionID)
		}
	}
}

// TestSegmentsCandidatesAndSelection 覆盖句级候选列表、重抽与选用绑定。
func TestSegmentsCandidatesAndSelection(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("候选项目")
	run := env.seedRun(projectID, "zh")
	env.freezeCandidateFixture(run)
	track := env.seedTrack(projectID, "zh", []domain.Segment{{
		ID: "seg-1", StartMs: 0, EndMs: 1000, Text: "你好",
	}})

	job := env.expect(env.request(http.MethodPost, "/api/v1/segments/seg-1/candidates",
		map[string]any{"language": "zh"}), http.StatusAccepted)
	candidateJob := decodeInto[service.SegmentCandidateJob](t, job)
	if candidateJob.TaskID == "" || candidateJob.SegmentID != "seg-1" || candidateJob.AttemptNo != 1 {
		t.Fatalf("candidate job = %+v", candidateJob)
	}
	empty := decodeInto[[]domain.AudioCandidate](t,
		env.expect(env.request(http.MethodGet, "/api/v1/segments/seg-1/candidates?language=zh", nil), http.StatusOK))
	if len(empty) != 0 {
		t.Fatalf("candidates = %+v", empty)
	}

	missingLang := env.expect(env.request(http.MethodGet, "/api/v1/segments/seg-1/candidates", nil), http.StatusBadRequest)
	if code := missingLang.code(t); code != "INVALID_REQUEST" {
		t.Fatalf("code = %s", code)
	}
	env.expect(env.request(http.MethodGet, "/api/v1/segments/unknown/candidates?language=zh", nil), http.StatusNotFound)
	env.expect(env.request(http.MethodGet, "/api/v1/segments/seg-1/selection?language=zh", nil), http.StatusNotFound)

	segment, err := env.store.GetSegmentContext(context.Background(), "seg-1")
	if err != nil {
		t.Fatal(err)
	}
	if segment.TrackID != track.ID {
		t.Fatalf("segment track = %s", segment.TrackID)
	}
	audio := env.seedAudioAsset(projectID)
	candidate := domain.AudioCandidate{
		ID:            store.NewID("cand"),
		ProjectID:     projectID,
		SegmentID:     "seg-1",
		Language:      "zh",
		Revision:      segment.Segment.ContentRevision,
		ModelVersion:  "tts-test-1",
		AudioAssetID:  audio.ID,
		IsRecommended: true,
		CreatedAt:     time.Now().UTC(),
	}
	if err := env.store.CreateAudioCandidate(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}

	selected := env.expect(env.request(http.MethodPut, "/api/v1/segments/seg-1/selection", map[string]any{
		"language":     "zh",
		"candidate_id": candidate.ID,
	}), http.StatusOK)
	selection := decodeInto[domain.CandidateSelection](t, selected)
	if selection.CandidateID != candidate.ID || selection.Revision != segment.Segment.ContentRevision {
		t.Fatalf("selection = %+v", selection)
	}
	got := decodeInto[domain.CandidateSelection](t,
		env.expect(env.request(http.MethodGet, "/api/v1/segments/seg-1/selection?language=zh", nil), http.StatusOK))
	if got.CandidateID != candidate.ID {
		t.Fatalf("selection = %+v", got)
	}
	env.expect(env.request(http.MethodPut, "/api/v1/segments/seg-1/selection", map[string]any{
		"language":     "zh",
		"candidate_id": "missing",
	}), http.StatusNotFound)

	// 终止 Run 拒绝新增执行：重抽不能绕开同一派发闸门。
	if _, err := env.store.DB().Exec(
		`UPDATE workflow_runs SET status = 'SUCCEEDED', state_version = state_version + 1 WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	closed := env.expect(env.request(http.MethodPost, "/api/v1/segments/seg-1/candidates",
		map[string]any{"language": "zh"}), http.StatusConflict)
	if code := closed.code(t); code == "" {
		t.Fatal("expected conflict code when re-draw is requested for a terminal run")
	}
}

// TestEngineConfigControlledSchema 覆盖槽位配置读写与受控参数校验。
func TestEngineConfigControlledSchema(t *testing.T) {
	env := newTestEnv(t)

	cfg := decodeInto[domain.EngineConfig](t,
		env.expect(env.request(http.MethodGet, "/api/v1/engines/asr/config", nil), http.StatusOK))
	if cfg.Slot != domain.ModelSlotASR || cfg.DeviceID != "auto" {
		t.Fatalf("default config = %+v", cfg)
	}
	updated := env.expect(env.request(http.MethodPut, "/api/v1/engines/asr/config",
		map[string]any{"device_id": "cpu"}), http.StatusOK)
	if got := decodeInto[domain.EngineConfig](t, updated); got.DeviceID != "cpu" {
		t.Fatalf("device = %s", got.DeviceID)
	}
	env.expect(env.request(http.MethodPut, "/api/v1/engines/asr/config",
		map[string]any{"device_id": "nonexistent"}), http.StatusNotFound)
	env.expect(env.request(http.MethodGet, "/api/v1/engines/ocr/config", nil), http.StatusNotFound)
	env.expect(env.request(http.MethodPut, "/api/v1/engines/asr/config",
		map[string]any{"values": map[string]any{"erase_mode": "dialog_only"}}), http.StatusBadRequest)
	env.expect(env.request(http.MethodPut, "/api/v1/engines/subtitle-erase/config",
		map[string]any{"values": map[string]any{"erase_mode": "dialog_only"}}), http.StatusOK)
	env.expect(env.request(http.MethodPut, "/api/v1/engines/subtitle-erase/config",
		map[string]any{"values": map[string]any{"erase_mode": "unknown_mode"}}), http.StatusBadRequest)
}

// TestExports 覆盖导出创建与类型/格式一致性。
func TestExports(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("导出项目")
	outDir := t.TempDir()
	track := env.seedTrack(projectID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 500, Text: "hi"},
		{ID: "seg-2", StartMs: 600, EndMs: 900, Text: "there"},
	})

	created := env.expect(env.request(http.MethodPost, "/api/v1/exports", map[string]any{
		"project_id":        projectID,
		"kind":              "subtitle",
		"format":            "srt",
		"input_revision_id": track.CurrentRevisionID,
		"output_dir_grant":  outDir,
		"naming_template":   "{project}-{language}",
	}), http.StatusAccepted)
	record := decodeInto[domain.ExportRecord](t, created)
	// 字幕导出由 Core 直接写文本，返回时已完成并带输出路径。
	if record.ID == "" || record.Status != domain.TaskStatusSucceeded || record.OutputPath == "" {
		t.Fatalf("export = %+v", record)
	}
	data, err := os.ReadFile(record.OutputPath)
	if err != nil {
		t.Fatalf("read subtitle export: %v", err)
	}
	for _, want := range []string{"1\n00:00:00,000 --> 00:00:00,500\nhi", "2\n00:00:00,600 --> 00:00:00,900\nthere"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("srt output missing %q:\n%s", want, data)
		}
	}
	ass := env.expect(env.request(http.MethodPost, "/api/v1/exports", map[string]any{
		"project_id":        projectID,
		"kind":              "subtitle",
		"format":            "ass",
		"input_revision_id": track.CurrentRevisionID,
		"output_dir_grant":  outDir,
		"naming_template":   "{project}-{language}",
	}), http.StatusAccepted)
	assRecord := decodeInto[domain.ExportRecord](t, ass)
	assData, err := os.ReadFile(assRecord.OutputPath)
	if err != nil {
		t.Fatalf("read ass export: %v", err)
	}
	if !strings.HasPrefix(string(assData), "[Script Info]") || !strings.Contains(string(assData), "Dialogue:") {
		t.Fatalf("ass output = %s", assData)
	}
	if assRecord.OutputPath == record.OutputPath {
		t.Fatalf("exports overwrote the same output path")
	}
	list := decodeInto[[]domain.ExportRecord](t,
		env.expect(env.request(http.MethodGet, "/api/v1/exports?project_id="+projectID, nil), http.StatusOK))
	if len(list) != 2 || list[0].ID != assRecord.ID {
		t.Fatalf("exports = %+v", list)
	}
	// 未知项目的过滤只是空列表，不是 404。
	empty := decodeInto[[]domain.ExportRecord](t,
		env.expect(env.request(http.MethodGet, "/api/v1/exports?project_id=missing", nil), http.StatusOK))
	if len(empty) != 0 {
		t.Fatalf("exports for missing project = %+v", empty)
	}
	// 引用未知项目的导出请求 → 400。
	env.expect(env.request(http.MethodPost, "/api/v1/exports", map[string]any{
		"project_id":       "missing",
		"kind":             "audio",
		"format":           "mp3",
		"output_dir_grant": outDir,
		"naming_template":  "{project}",
	}), http.StatusBadRequest)
	env.expect(env.request(http.MethodPost, "/api/v1/exports", map[string]any{
		"project_id":       projectID,
		"kind":             "video",
		"format":           "wav",
		"output_dir_grant": outDir,
		"naming_template":  "{project}",
	}), http.StatusBadRequest)
	// 音频/视频成品导出必须绑定 Run（由审核冻结的输入决定）。
	env.expect(env.request(http.MethodPost, "/api/v1/exports", map[string]any{
		"project_id":       projectID,
		"kind":             "audio",
		"format":           "mp3",
		"output_dir_grant": outDir,
		"naming_template":  "{project}",
	}), http.StatusBadRequest)
}

// TestExportRequiresDeliveryReview 覆盖交付审核门禁：Run 派生导出必须已放行
// delivery 门槛（绑定 Core 重算的当前修订），否则 409 REVIEW_REQUIRED；
// 无 run_id 的工具导出不受限。
func TestExportRequiresDeliveryReview(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("交付门禁项目")
	track := env.seedTrack(projectID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 500, Text: "你好"},
	})
	run := env.seedRun(projectID, "zh")
	outDir := t.TempDir()
	body := func(language string) map[string]any {
		payload := map[string]any{
			"project_id":        projectID,
			"run_id":            run.ID,
			"kind":              "subtitle",
			"format":            "srt",
			"input_revision_id": track.CurrentRevisionID,
			"output_dir_grant":  outDir,
			"naming_template":   "{project}-{language}",
		}
		if language != "" {
			payload["language"] = language
		}
		return payload
	}

	blocked := env.expect(env.request(http.MethodPost, "/api/v1/exports", body("")), http.StatusConflict)
	if code := blocked.code(t); code != string(domain.ErrorCodeReviewRequired) {
		t.Fatalf("unreviewed export code = %s", code)
	}
	// 门禁先于落盘：被拒的导出不能在输出目录留下文件。
	if leftovers, _ := filepath.Glob(filepath.Join(outDir, "*")); len(leftovers) != 0 {
		t.Fatalf("blocked export left files: %v", leftovers)
	}

	// 放行当前修订后可导出。
	env.seedApprovedDelivery(run.ID, track)
	created := env.expect(env.request(http.MethodPost, "/api/v1/exports", body("")), http.StatusAccepted)
	record := decodeInto[domain.ExportRecord](t, created)
	if record.Status != domain.TaskStatusSucceeded || record.OutputPath == "" {
		t.Fatalf("approved export = %+v", record)
	}
	if !strings.Contains(record.OutputPath, "-zh") {
		t.Fatalf("export path %s does not use the run language", record.OutputPath)
	}

	// 指定与 revision 不符的语种 → 400。
	env.expect(env.request(http.MethodPost, "/api/v1/exports", body("ja")), http.StatusBadRequest)

	// 无 run_id 的工具导出不设交付门禁。
	tool := env.expect(env.request(http.MethodPost, "/api/v1/exports", map[string]any{
		"project_id":        projectID,
		"kind":              "subtitle",
		"format":            "srt",
		"input_revision_id": track.CurrentRevisionID,
		"output_dir_grant":  outDir,
		"naming_template":   "{project}-tool",
	}), http.StatusAccepted)
	toolRecord := decodeInto[domain.ExportRecord](t, tool)
	if toolRecord.Status != domain.TaskStatusSucceeded {
		t.Fatalf("tool export = %+v", toolRecord)
	}
}

// TestStorageUsageAndCleanup 覆盖分区用量与按类清理的引用保护。
func TestStorageUsageAndCleanup(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("存储项目")

	cacheFile := filepath.Join(env.dataDir, "cache", "orphan.bin")
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cacheFile, bytes.Repeat([]byte("x"), 128), 0o600); err != nil {
		t.Fatal(err)
	}

	usage := decodeInto[domain.StorageUsage](t,
		env.expect(env.request(http.MethodGet, "/api/v1/storage/usage", nil), http.StatusOK))
	if usage.CacheBytes != 128 {
		t.Fatalf("cache bytes = %d", usage.CacheBytes)
	}

	if _, err := env.store.DB().Exec(`INSERT INTO tasks (id, project_id, kind, status, created_at, updated_at)
		VALUES ('running-task', ?, 'node', 'RUNNING', '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')`, projectID); err != nil {
		t.Fatal(err)
	}
	busy := env.expect(env.request(http.MethodPost, "/api/v1/storage/cleanup", map[string]any{"category": "cache"}), http.StatusConflict)
	if busy.code(t) == "" {
		t.Fatal("expected conflict code while tasks are active")
	}
	if _, err := env.store.DB().Exec(`DELETE FROM tasks WHERE id = 'running-task'`); err != nil {
		t.Fatal(err)
	}

	cleaned := env.expect(env.request(http.MethodPost, "/api/v1/storage/cleanup", map[string]any{"category": "cache"}), http.StatusOK)
	result := decodeInto[domain.StorageCleanupResult](t, cleaned)
	if result.Category != domain.StorageCategoryCache || result.FreedBytes != 128 {
		t.Fatalf("cleanup = %+v", result)
	}
	if _, err := os.Stat(cacheFile); !os.IsNotExist(err) {
		t.Fatalf("cache file still exists: %v", err)
	}
	// 清理会失效用量缓存：下一次读取必须重新扫描并反映真实占用。
	after := decodeInto[domain.StorageUsage](t,
		env.expect(env.request(http.MethodGet, "/api/v1/storage/usage", nil), http.StatusOK))
	if after.CacheBytes != 0 {
		t.Fatalf("cache bytes after cleanup = %d, want 0", after.CacheBytes)
	}
	env.expect(env.request(http.MethodPost, "/api/v1/storage/cleanup", map[string]any{"category": "bogus"}), http.StatusBadRequest)
}

// TestSnapshotEventsAndCursor 覆盖单读事务快照、SSE 回放与游标过期。
func TestSnapshotEventsAndCursor(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("同步项目")
	run := env.seedRun(projectID)
	env.expect(env.request(http.MethodPost, "/api/v1/tasks", map[string]any{
		"project_id": projectID,
		"run_id":     run.ID,
		"kind":       "node",
		"node_key":   "02_analyze",
	}), http.StatusAccepted)

	snapshot := decodeInto[domain.Snapshot](t,
		env.expect(env.request(http.MethodGet, "/api/v1/snapshot?project_id="+projectID, nil), http.StatusOK))
	if snapshot.ProjectID != projectID || snapshot.EventSeq <= 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if len(snapshot.Runs) != 1 || len(snapshot.Tasks) != 1 {
		t.Fatalf("snapshot runs/tasks = %d/%d", len(snapshot.Runs), len(snapshot.Tasks))
	}
	if snapshot.Project.ID != projectID {
		t.Fatalf("snapshot project = %+v", snapshot.Project)
	}
	env.expect(env.request(http.MethodGet, "/api/v1/snapshot?project_id=missing", nil), http.StatusNotFound)

	cursor := decodeInto[domain.EventCursor](t,
		env.expect(env.request(http.MethodGet, "/api/v1/events/cursor", nil), http.StatusOK))
	if cursor.EarliestSeq < 1 {
		t.Fatalf("earliest seq = %d", cursor.EarliestSeq)
	}

	// SSE：回放一条历史事件。
	req, err := http.NewRequest(http.MethodGet, env.server.URL+"/api/v1/events?after_seq=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(tokenHeader, testToken)
	streamResp, err := env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d", streamResp.StatusCode)
	}
	if ct := streamResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q", ct)
	}
	scanner := bufio.NewScanner(streamResp.Body)
	var event domain.DomainEvent
	deadline := time.After(3 * time.Second)
	for event.Seq == 0 {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for SSE event")
		default:
		}
		if !scanner.Scan() {
			t.Fatalf("SSE stream ended: %v", scanner.Err())
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatalf("decode event: %v", err)
		}
	}
	if event.Seq < 1 || event.SchemaVersion != 1 || event.Type == "" {
		t.Fatalf("event = %+v", event)
	}

	// 实时事件：从快照高水位订阅后，新的业务写入经 SSE 推送。
	liveReq, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/api/v1/events?after_seq=%d", env.server.URL, snapshot.EventSeq), nil)
	if err != nil {
		t.Fatal(err)
	}
	liveReq.Header.Set(tokenHeader, testToken)
	liveResp, err := env.client.Do(liveReq)
	if err != nil {
		t.Fatal(err)
	}
	defer liveResp.Body.Close()
	if liveResp.StatusCode != http.StatusOK {
		t.Fatalf("live events status = %d", liveResp.StatusCode)
	}
	lineCh := make(chan string, 1)
	go func() {
		liveScanner := bufio.NewScanner(liveResp.Body)
		for liveScanner.Scan() {
			line := liveScanner.Text()
			if strings.HasPrefix(line, "data: ") {
				lineCh <- strings.TrimPrefix(line, "data: ")
				return
			}
		}
	}()
	time.Sleep(100 * time.Millisecond)
	env.seedProject("SSE 实时项目")
	select {
	case data := <-lineCh:
		var liveEvent domain.DomainEvent
		if err := json.Unmarshal([]byte(data), &liveEvent); err != nil {
			t.Fatalf("decode live event: %v", err)
		}
		if liveEvent.Seq <= snapshot.EventSeq || liveEvent.Type != "project.created" {
			t.Fatalf("live event = %+v", liveEvent)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for live SSE event")
	}

	// 游标缺口：裁剪历史后 after_seq=0 无法补齐。
	latest, err := env.store.LatestEventSeq(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.PruneEventsBefore(context.Background(), latest); err != nil {
		t.Fatal(err)
	}
	expired := env.expect(env.request(http.MethodGet, "/api/v1/events?after_seq=0", nil), http.StatusConflict)
	if code := expired.code(t); code != "EVENT_CURSOR_EXPIRED" {
		t.Fatalf("code = %s", code)
	}
	cursorAfter := decodeInto[domain.EventCursor](t,
		env.expect(env.request(http.MethodGet, "/api/v1/events/cursor", nil), http.StatusOK))
	if cursorAfter.EarliestSeq != latest {
		t.Fatalf("earliest = %d, want %d", cursorAfter.EarliestSeq, latest)
	}
}

// TestInstallationsLifecycle 覆盖计划、执行、取消、回滚与删除包。
func TestInstallationsLifecycle(t *testing.T) {
	block := make(chan struct{})
	env := newTestEnv(t, func(cfg *envConfig) {
		cfg.capabilities = service.NewInstallationCapabilities()
		cfg.downloader = fakeDownloader{fetch: func(ctx context.Context, _ string, _ downloader.File, _ func(int64, int64)) error {
			select {
			case <-block:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}
	})
	defer close(block)

	planFor := func(slot string) domain.InstallationPlan {
		res := env.expect(env.request(http.MethodPost, "/api/v1/installations/plan", map[string]any{"slot": slot}), http.StatusOK)
		return decodeInto[domain.InstallationPlan](t, res)
	}
	plan := planFor("asr")
	if plan.PlanID == "" || plan.PlanHash == "" || plan.Release.ReleaseID != "rel-asr" {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.License.LicenseID != "Apache-2.0" {
		t.Fatalf("license = %+v", plan.License)
	}

	// 摘要不符 → PREFLIGHT_STALE。
	stale := env.expect(env.request(http.MethodPost, "/api/v1/installations", map[string]any{
		"plan_id":   plan.PlanID,
		"plan_hash": "wrong",
	}), http.StatusConflict)
	if code := stale.code(t); code != "PREFLIGHT_STALE" {
		t.Fatalf("code = %s", code)
	}

	created := env.expect(env.request(http.MethodPost, "/api/v1/installations", map[string]any{
		"plan_id":   plan.PlanID,
		"plan_hash": plan.PlanHash,
	}, withIdemKey("inst-create-1")), http.StatusAccepted)
	inst := decodeInto[domain.Installation](t, created)
	if inst.Phase != domain.InstallationPhasePlanning {
		t.Fatalf("installation = %+v", inst)
	}
	// 幂等重放：返回原安装记录，不重复启动第二份安装。
	replayed := env.expect(env.request(http.MethodPost, "/api/v1/installations", map[string]any{
		"plan_id":   plan.PlanID,
		"plan_hash": plan.PlanHash,
	}, withIdemKey("inst-create-1")), http.StatusAccepted)
	if replayInst := decodeInto[domain.Installation](t, replayed); replayInst.ID != inst.ID {
		t.Fatalf("replayed installation id = %s, want %s", replayInst.ID, inst.ID)
	}
	got := decodeInto[domain.Installation](t,
		env.expect(env.request(http.MethodGet, "/api/v1/installations/"+inst.ID, nil), http.StatusOK))
	if got.ID != inst.ID {
		t.Fatalf("installation = %+v", got)
	}
	env.expect(env.request(http.MethodPost, "/api/v1/installations/"+inst.ID+"/cancel", nil), http.StatusAccepted)
	deadline := time.Now().Add(2 * time.Second)
	for {
		current := decodeInto[domain.Installation](t,
			env.expect(env.request(http.MethodGet, "/api/v1/installations/"+inst.ID, nil), http.StatusOK))
		if current.Phase == domain.InstallationPhaseCanceled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("installation phase = %s, want canceled", current.Phase)
		}
		time.Sleep(20 * time.Millisecond)
	}

	list := decodeInto[[]domain.Installation](t,
		env.expect(env.request(http.MethodGet, "/api/v1/installations", nil), http.StatusOK))
	if len(list) != 1 {
		t.Fatalf("installations = %+v", list)
	}

	// 计划未知槽位 → 404。
	env.expect(env.request(http.MethodPost, "/api/v1/installations/plan", map[string]any{"slot": "bogus"}), http.StatusNotFound)
	// 只有已取消记录时删除清理该槽位记录（204）。
	env.expect(env.request(http.MethodDelete, "/api/v1/capabilities/asr/installation", nil), http.StatusNoContent)
	// 少于两个已完成版本时回滚 → 409。
	rollback := env.expect(env.request(http.MethodPost, "/api/v1/capabilities/asr/rollback", nil), http.StatusConflict)
	if rollback.code(t) == "" {
		t.Fatal("expected conflict code for rollback without previous version")
	}
}

// sequencedReleases 按槽位依次返回预置发布，再回退到默认目录；模拟升级。
type sequencedReleases struct {
	mu       sync.Mutex
	fallback service.ReleaseCatalog
	plans    map[domain.CapabilitySlotKey][]service.ReleasePlan
	next     map[domain.CapabilitySlotKey]int
}

func (c *sequencedReleases) Resolve(ctx context.Context, slot domain.CapabilitySlotKey) (service.ReleasePlan, error) {
	c.mu.Lock()
	if c.next == nil {
		c.next = make(map[domain.CapabilitySlotKey]int)
	}
	index := c.next[slot]
	if plans := c.plans[slot]; index < len(plans) {
		c.next[slot] = index + 1
		c.mu.Unlock()
		return plans[index], nil
	}
	c.mu.Unlock()
	return c.fallback.Resolve(ctx, slot)
}

// apiReleasePlan 构造带真实内容哈希的测试发布（下载器会写入真实文件）。
func apiReleasePlan(releaseID, version string, files map[string][]byte) service.ReleasePlan {
	manifest := make([]downloader.File, 0, len(files))
	var total int64
	contents := make(map[string][]byte, len(files))
	for relPath, content := range files {
		sum := sha256.Sum256(content)
		manifest = append(manifest, downloader.File{
			RelPath: relPath,
			URL:     "https://releases.invalid/" + releaseID + "/" + relPath,
			SHA256:  hex.EncodeToString(sum[:]),
			Size:    int64(len(content)),
		})
		contents[hex.EncodeToString(sum[:])] = content
		total += int64(len(content))
	}
	return service.ReleasePlan{
		Release:          domain.ReleaseRef{ReleaseID: releaseID, Version: version, Digest: "sha256:" + releaseID},
		Files:            manifest,
		DownloadBytes:    total,
		InstallBytes:     total,
		PeakStagingBytes: total,
		Permissions:      []string{"filesystem_write"},
		License:          domain.LicenseSummary{LicenseID: "Apache-2.0", LicenseName: "Apache License 2.0"},
	}
}

// TestInstallationsCompleteAndRollback 覆盖安装完成后的能力状态、回滚与删除包。
func TestInstallationsCompleteAndRollback(t *testing.T) {
	v1 := []byte("asr-release-v1")
	v2 := []byte("asr-release-v2")
	v1Plan := apiReleasePlan("asr-v1", "1.0.0", map[string][]byte{"model.bin": v1})
	v2Plan := apiReleasePlan("asr-v2", "2.0.0", map[string][]byte{"model.bin": v2})
	contents := map[string][]byte{
		v1Plan.Files[0].SHA256: v1,
		v2Plan.Files[0].SHA256: v2,
	}
	env := newTestEnv(t, func(cfg *envConfig) {
		cfg.capabilities = service.NewInstallationCapabilities()
		cfg.releases = &sequencedReleases{
			fallback: defaultReleases(),
			plans: map[domain.CapabilitySlotKey][]service.ReleasePlan{
				domain.CapabilitySlotASR: {v1Plan, v2Plan},
			},
		}
		cfg.downloader = fakeDownloader{fetch: func(_ context.Context, root string, file downloader.File, _ func(int64, int64)) error {
			content, ok := contents[file.SHA256]
			if !ok {
				return fmt.Errorf("unexpected test file %s", file.SHA256)
			}
			path := filepath.Join(root, filepath.FromSlash(file.RelPath))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return os.WriteFile(path, content, 0o644)
		}}
	})

	install := func(slot string) domain.Installation {
		planRes := env.expect(env.request(http.MethodPost, "/api/v1/installations/plan", map[string]any{"slot": slot}), http.StatusOK)
		plan := decodeInto[domain.InstallationPlan](t, planRes)
		created := env.expect(env.request(http.MethodPost, "/api/v1/installations", map[string]any{
			"plan_id":   plan.PlanID,
			"plan_hash": plan.PlanHash,
		}), http.StatusAccepted)
		inst := decodeInto[domain.Installation](t, created)
		deadline := time.Now().Add(3 * time.Second)
		for {
			current := decodeInto[domain.Installation](t,
				env.expect(env.request(http.MethodGet, "/api/v1/installations/"+inst.ID, nil), http.StatusOK))
			if current.Phase == domain.InstallationPhaseDone {
				return current
			}
			if current.Phase.Terminal() {
				t.Fatalf("installation phase = %s", current.Phase)
			}
			if time.Now().After(deadline) {
				t.Fatalf("installation did not finish, phase = %s", current.Phase)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	first := install("asr")
	// 能力目录由安装记录推导 → READY。
	caps := decodeInto[[]domain.Capability](t,
		env.expect(env.request(http.MethodGet, "/api/v1/capabilities", nil), http.StatusOK))
	asrState := domain.SlotState("")
	asrVersion := ""
	for _, capability := range caps {
		if capability.Slot == domain.CapabilitySlotASR {
			asrState = capability.State
			asrVersion = capability.Version
		}
	}
	if asrState != domain.SlotStateReady || asrVersion != "1.0.0" {
		t.Fatalf("asr capability = %s/%s", asrState, asrVersion)
	}

	second := install("asr")
	rollback := env.expect(env.request(http.MethodPost, "/api/v1/capabilities/asr/rollback", nil), http.StatusAccepted)
	rolled := decodeInto[domain.Installation](t, rollback)
	if rolled.Phase != domain.InstallationPhaseDone || rolled.ID != first.ID || rolled.Slot != domain.CapabilitySlotASR {
		t.Fatalf("rollback = %+v, want existing entity %s", rolled, first.ID)
	}
	installs := decodeInto[[]domain.Installation](t,
		env.expect(env.request(http.MethodGet, "/api/v1/installations", nil), http.StatusOK))
	if len(installs) != 2 {
		t.Fatalf("rollback created a physical Done copy: %+v", installs)
	}
	caps = decodeInto[[]domain.Capability](t,
		env.expect(env.request(http.MethodGet, "/api/v1/capabilities", nil), http.StatusOK))
	for _, capability := range caps {
		if capability.Slot == domain.CapabilitySlotASR && (capability.State != domain.SlotStateReady || capability.Version != "1.0.0") {
			t.Fatalf("asr capability after rollback = %s/%s, want READY/1.0.0", capability.State, capability.Version)
		}
	}
	_ = second

	env.expect(env.request(http.MethodDelete, "/api/v1/capabilities/asr/installation", nil), http.StatusNoContent)
	capsAfter := decodeInto[[]domain.Capability](t,
		env.expect(env.request(http.MethodGet, "/api/v1/capabilities", nil), http.StatusOK))
	for _, capability := range capsAfter {
		if capability.Slot == domain.CapabilitySlotASR && capability.State != domain.SlotStateNeedsSetup {
			t.Fatalf("asr state after delete = %s", capability.State)
		}
	}
}

// TestSharedReleasePlanProjectionAndPerSlotDelete 覆盖共享发布的 wire 契约：
// 计划显示完整覆盖与复用目标、一次安装同时准入两槽、删除只解绑请求槽位。
func TestSharedReleasePlanProjectionAndPerSlotDelete(t *testing.T) {
	payload := []byte("shared-api-payload")
	plan := apiReleasePlan("shared-api", "1.0.0", map[string][]byte{"model.bin": payload})
	plan.CoveredSlots = []domain.CapabilitySlotKey{domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone}
	contents := map[string][]byte{plan.Files[0].SHA256: payload}
	env := newTestEnv(t, func(cfg *envConfig) {
		cfg.capabilities = service.NewInstallationCapabilities()
		cfg.releases = service.StaticReleases{Releases: map[domain.CapabilitySlotKey]service.ReleasePlan{
			domain.CapabilitySlotTTS:        plan,
			domain.CapabilitySlotVoiceClone: plan,
		}}
		cfg.downloader = fakeDownloader{fetch: func(_ context.Context, root string, file downloader.File, _ func(int64, int64)) error {
			content, ok := contents[file.SHA256]
			if !ok {
				return fmt.Errorf("unexpected test file %s", file.SHA256)
			}
			path := filepath.Join(root, filepath.FromSlash(file.RelPath))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return os.WriteFile(path, content, 0o644)
		}}
	})

	planRes := env.expect(env.request(http.MethodPost, "/api/v1/installations/plan", map[string]any{"slot": "tts"}), http.StatusOK)
	createdPlan := decodeInto[domain.InstallationPlan](t, planRes)
	if !sameSlots(createdPlan.CoveredSlots, []domain.CapabilitySlotKey{domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone}) {
		t.Fatalf("plan covered_slots = %v", createdPlan.CoveredSlots)
	}
	if createdPlan.DownloadBytes != int64(len(payload)) {
		t.Fatalf("plan download bytes = %d, want one physical copy", createdPlan.DownloadBytes)
	}
	created := env.expect(env.request(http.MethodPost, "/api/v1/installations", map[string]any{
		"plan_id": createdPlan.PlanID, "plan_hash": createdPlan.PlanHash,
	}), http.StatusAccepted)
	inst := decodeInto[domain.Installation](t, created)
	deadline := time.Now().Add(3 * time.Second)
	for {
		inst = decodeInto[domain.Installation](t,
			env.expect(env.request(http.MethodGet, "/api/v1/installations/"+inst.ID, nil), http.StatusOK))
		if inst.Phase == domain.InstallationPhaseDone {
			break
		}
		if inst.Phase.Terminal() {
			t.Fatalf("installation phase = %s", inst.Phase)
		}
		if time.Now().After(deadline) {
			t.Fatalf("installation stuck in %s", inst.Phase)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sameSlots(inst.CoveredSlots, createdPlan.CoveredSlots) ||
		!sameSlots(inst.BoundSlots, createdPlan.CoveredSlots) ||
		!sameSlots(inst.CurrentSlots, createdPlan.CoveredSlots) {
		t.Fatalf("installation projections = covered %v bound %v current %v",
			inst.CoveredSlots, inst.BoundSlots, inst.CurrentSlots)
	}

	// 另一覆盖槽位 plan：复用同一实体/同一进度，不新增下载或记录。
	cloneRes := env.expect(env.request(http.MethodPost, "/api/v1/installations/plan", map[string]any{"slot": "voice-clone"}), http.StatusOK)
	clonePlan := decodeInto[domain.InstallationPlan](t, cloneRes)
	if clonePlan.ReuseInstallationID != inst.ID || clonePlan.DownloadBytes != 0 {
		t.Fatalf("clone plan reuse = %q download = %d", clonePlan.ReuseInstallationID, clonePlan.DownloadBytes)
	}
	// 新请求的入口 slot 与完整覆盖保持本次计划；复用实体保留原操作历史。
	if clonePlan.Slot != domain.CapabilitySlotVoiceClone {
		t.Fatalf("clone plan slot = %s, want requested entry slot", clonePlan.Slot)
	}
	if !sameSlots(clonePlan.CoveredSlots, []domain.CapabilitySlotKey{domain.CapabilitySlotTTS, domain.CapabilitySlotVoiceClone}) {
		t.Fatalf("clone plan covered_slots = %v", clonePlan.CoveredSlots)
	}
	joined := decodeInto[domain.Installation](t, env.expect(env.request(http.MethodPost, "/api/v1/installations", map[string]any{
		"plan_id": clonePlan.PlanID, "plan_hash": clonePlan.PlanHash,
	}), http.StatusAccepted))
	if joined.ID != inst.ID || joined.Slot != domain.CapabilitySlotTTS || joined.PlanHash != inst.PlanHash {
		t.Fatalf("join/reuse response = %+v, want original physical operation", joined)
	}

	// 删除仅解绑请求槽位；另一槽位仍 READY，记录/文件保留。
	env.expect(env.request(http.MethodDelete, "/api/v1/capabilities/tts/installation", nil), http.StatusNoContent)
	caps := decodeInto[[]domain.Capability](t,
		env.expect(env.request(http.MethodGet, "/api/v1/capabilities", nil), http.StatusOK))
	states := map[domain.CapabilitySlotKey]domain.SlotState{}
	for _, capability := range caps {
		states[capability.Slot] = capability.State
	}
	if states[domain.CapabilitySlotTTS] != domain.SlotStateNeedsSetup {
		t.Fatalf("detached tts state = %s", states[domain.CapabilitySlotTTS])
	}
	if states[domain.CapabilitySlotVoiceClone] != domain.SlotStateReady {
		t.Fatalf("voice-clone state after tts detach = %s", states[domain.CapabilitySlotVoiceClone])
	}
	list := decodeInto[[]domain.Installation](t,
		env.expect(env.request(http.MethodGet, "/api/v1/installations", nil), http.StatusOK))
	if len(list) != 1 || !sameSlots(list[0].BoundSlots, []domain.CapabilitySlotKey{domain.CapabilitySlotVoiceClone}) ||
		!sameSlots(list[0].CurrentSlots, []domain.CapabilitySlotKey{domain.CapabilitySlotVoiceClone}) {
		t.Fatalf("shared record after detach = %+v", list)
	}
}

func sameSlots(a, b []domain.CapabilitySlotKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestLocalApiSettingsTokenAndThirdPartyPort 覆盖第三方本机 API 的默认关闭、
// Bearer 鉴权、令牌吊销与统计。
func TestLocalApiSettingsTokenAndThirdPartyPort(t *testing.T) {
	env := newTestEnv(t)

	settings := decodeInto[domain.LocalApiSettings](t,
		env.expect(env.request(http.MethodGet, "/api/v1/local-api/settings", nil), http.StatusOK))
	if settings.Enabled || settings.TokenPresent {
		t.Fatalf("default settings = %+v", settings)
	}
	if env.local.Port() != 0 {
		t.Fatalf("local api listening by default on port %d", env.local.Port())
	}
	stats := decodeInto[domain.LocalApiStats](t,
		env.expect(env.request(http.MethodGet, "/api/v1/local-api/stats", nil), http.StatusOK))
	if stats.Enabled || stats.TotalCalls != 0 {
		t.Fatalf("default stats = %+v", stats)
	}

	tokenRes := env.expect(env.request(http.MethodPost, "/api/v1/local-api/token", nil), http.StatusCreated)
	token := decodeInto[domain.LocalApiToken](t, tokenRes)
	if token.Token == "" || token.IssuedAt.IsZero() {
		t.Fatalf("token = %+v", token)
	}
	settings = decodeInto[domain.LocalApiSettings](t,
		env.expect(env.request(http.MethodGet, "/api/v1/local-api/settings", nil), http.StatusOK))
	if !settings.TokenPresent || settings.TokenMasked == "" {
		t.Fatalf("settings after token = %+v", settings)
	}

	// 管理端点也遵守幂等回执（响应不含敏感信息）。
	settingsKey := "local-settings-idem"
	first := env.expect(env.request(http.MethodPut, "/api/v1/local-api/settings",
		map[string]any{"concurrency_limit": 1}, withIdemKey(settingsKey)), http.StatusOK)
	replay := env.expect(env.request(http.MethodPut, "/api/v1/local-api/settings",
		map[string]any{"concurrency_limit": 1}, withIdemKey(settingsKey)), http.StatusOK)
	if !bytes.Equal(first.Body, replay.Body) {
		t.Fatalf("settings replay body differs: %s vs %s", first.Body, replay.Body)
	}
	env.expect(env.request(http.MethodPut, "/api/v1/local-api/settings",
		map[string]any{"concurrency_limit": 5}, withIdemKey(settingsKey)), http.StatusConflict)
	if strings.Contains(settings.TokenMasked, token.Token) {
		t.Fatal("masked token leaks plaintext")
	}
	// 明文令牌不得持久化到幂等回执（token 端点不使用回执）。
	var persisted int
	if err := env.store.DB().QueryRow(
		`SELECT COUNT(*) FROM receipts WHERE response_json LIKE ?`, "%"+token.Token+"%").Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 0 {
		t.Fatalf("plaintext token persisted in %d receipts", persisted)
	}

	enabled := decodeInto[domain.LocalApiSettings](t,
		env.expect(env.request(http.MethodPut, "/api/v1/local-api/settings", map[string]any{
			"enabled":           true,
			"concurrency_limit": 2,
		}), http.StatusOK))
	if !enabled.Enabled || enabled.ConcurrencyLimit != 2 {
		t.Fatalf("enabled settings = %+v", enabled)
	}
	var port int
	deadline := time.Now().Add(3 * time.Second)
	for port == 0 {
		port = env.local.Port()
		if time.Now().After(deadline) {
			t.Fatal("local api did not start listening")
		}
		time.Sleep(10 * time.Millisecond)
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	post := func(path, bearer string, body []byte, contentType string) (*http.Response, []byte) {
		req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := env.client.Do(req)
		if err != nil {
			t.Fatalf("third-party request: %v", err)
		}
		defer resp.Body.Close()
		payload := new(bytes.Buffer)
		_, _ = payload.ReadFrom(resp.Body)
		return resp, payload.Bytes()
	}

	speechBody := []byte(`{"input":"hello","voice":"default"}`)
	resp, _ := post("/v1/audio/speech", "", speechBody, "application/json")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no bearer status = %d", resp.StatusCode)
	}
	resp, _ = post("/v1/audio/speech", "wrong-token", speechBody, "application/json")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong bearer status = %d", resp.StatusCode)
	}
	resp, payload := post("/v1/audio/speech", token.Token, speechBody, "application/json")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("valid bearer status = %d body=%s", resp.StatusCode, payload)
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != domain.ErrorCodeCapabilityUnverified {
		t.Fatalf("code = %s", envelope.Error.Code)
	}

	// multipart 转写：缺少 file → 400；带 file → 409（兼容子集未声明）。
	emptyMultipart := new(bytes.Buffer)
	writer := multipart.NewWriter(emptyMultipart)
	_ = writer.Close()
	resp, _ = post("/v1/audio/transcriptions", token.Token, emptyMultipart.Bytes(), writer.FormDataContentType())
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty multipart status = %d", resp.StatusCode)
	}
	withFile := new(bytes.Buffer)
	writer = multipart.NewWriter(withFile)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="a.wav"`)
	header.Set("Content-Type", "audio/wav")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("RIFF....WAVE"))
	_ = writer.Close()
	resp, payload = post("/v1/audio/transcriptions", token.Token, withFile.Bytes(), writer.FormDataContentType())
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("transcription status = %d body=%s", resp.StatusCode, payload)
	}

	stats = decodeInto[domain.LocalApiStats](t,
		env.expect(env.request(http.MethodGet, "/api/v1/local-api/stats", nil), http.StatusOK))
	if !stats.Enabled || stats.TotalCalls < 4 || len(stats.ByEndpoint) == 0 {
		t.Fatalf("stats = %+v", stats)
	}

	// 吊销令牌：第三方端口仍在监听，但旧令牌立即 401。
	env.expect(env.request(http.MethodDelete, "/api/v1/local-api/token", nil), http.StatusNoContent)
	resp, _ = post("/v1/audio/speech", token.Token, speechBody, "application/json")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d", resp.StatusCode)
	}

	// 关闭总开关：停止第三方端口监听，不影响内部 API。
	env.expect(env.request(http.MethodPut, "/api/v1/local-api/settings", map[string]any{"enabled": false}), http.StatusOK)
	deadline = time.Now().Add(3 * time.Second)
	for env.local.Port() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("local api still listening on %d", env.local.Port())
		}
		time.Sleep(10 * time.Millisecond)
	}
	env.expect(env.request(http.MethodGet, "/api/v1/system", nil), http.StatusOK)
}

// TestPreflightBlockerAndUnconfiguredRelease 覆盖 blocker 阻断创建与发布目录未就绪。
func TestPreflightBlockerAndUnconfiguredRelease(t *testing.T) {
	env := newTestEnv(t, func(cfg *envConfig) {
		cfg.capabilities = service.StaticCapabilities{States: map[domain.CapabilitySlotKey]domain.SlotState{
			domain.CapabilitySlotASR: domain.SlotStateNeedsSetup,
		}}
		cfg.releases = service.UnconfiguredReleases{}
	})
	projectID := env.seedProject("阻断项目")
	input := map[string]any{
		"project_id":       projectID,
		"name":             "阻断计划",
		"source_language":  "en",
		"target_languages": []string{"zh"},
		"extract":          map[string]any{"mode": "asr"},
	}
	preflight := decodeInto[domain.PreflightResult](t,
		env.expect(env.request(http.MethodPost, "/api/v1/tasks/preflight", input), http.StatusOK))
	blocked := false
	for _, issue := range preflight.Issues {
		if issue.Severity == domain.PreflightBlocker && issue.Code == string(domain.ErrorCodeModelNotInstalled) {
			blocked = true
			if len(issue.ActionIDs) == 0 {
				t.Fatalf("blocker has no action_ids: %+v", issue)
			}
		}
	}
	if !blocked {
		t.Fatalf("preflight did not report MODEL_NOT_INSTALLED blocker: %+v", preflight.Issues)
	}
	create := env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", input), http.StatusConflict)
	if code := create.code(t); code != "PREFLIGHT_STALE" {
		t.Fatalf("create run code = %s", code)
	}

	plan := env.expect(env.request(http.MethodPost, "/api/v1/installations/plan",
		map[string]any{"slot": "asr"}), http.StatusConflict)
	if code := plan.code(t); code != "MODEL_NOT_INSTALLED" {
		t.Fatalf("plan code = %s", code)
	}
}

// TestPauseDraftRunReturnsConflict 覆盖不可暂停状态返回 409（契约 pause 未声明 400）。
func TestPauseDraftRunReturnsConflict(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("草稿项目")
	run := env.seedRun(projectID)
	if _, err := env.store.DB().Exec(
		`UPDATE workflow_runs SET status = 'DRAFT', state_version = state_version + 1 WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	paused := env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/pause", nil), http.StatusConflict)
	if code := paused.code(t); code == "" {
		t.Fatal("expected conflict code for draft run pause")
	}
}

// TestTerminalRunRefusesApprovalPauseResume 覆盖终止 Run 拒绝放行与暂停/恢复。
func TestTerminalRunRefusesApprovalPauseResume(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("终止项目")
	run := env.seedRun(projectID, "zh")
	track := env.seedTrack(projectID, "zh", []domain.Segment{{ID: "seg-1", StartMs: 0, EndMs: 500, Text: "src"}})
	env.expect(env.request(http.MethodPut, "/api/v1/subtitle-tracks/"+track.ID+"/revisions", map[string]any{
		"base_revision_id": track.CurrentRevisionID,
		"segments":         []map[string]any{{"id": "seg-1", "start_ms": 0, "end_ms": 500, "text": "Edited"}},
	}), http.StatusCreated)
	gates := decodeInto[[]domain.ReviewGate](t,
		env.expect(env.request(http.MethodGet, "/api/v1/projects/"+projectID+"/reviews", nil), http.StatusOK))
	if len(gates) == 0 {
		t.Fatal("no review gates")
	}
	if _, err := env.store.DB().Exec(
		`UPDATE workflow_runs SET status = 'SUCCEEDED', state_version = state_version + 1 WHERE id = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	approve := env.expect(env.request(http.MethodPost, "/api/v1/reviews/"+gates[0].ID+"/approve", map[string]any{
		"revision_digest": gates[0].RevisionDigest,
		"type":            gates[0].Type,
	}), http.StatusConflict)
	if approve.code(t) == "" {
		t.Fatal("expected conflict code")
	}
	env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/pause", nil), http.StatusConflict)
	env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/resume", nil), http.StatusConflict)
}

// TestStatusForCoversAllErrorCodes 覆盖错误码表全量映射。
// TestWorkflowRunInputAssetIDsAndTaskLanguage 覆盖播放/预览所需的两个增量契约字段。
// input_asset_ids 是派生只读字段（不迁移）；Task.language 只补 JSON 输出。
func TestWorkflowRunInputAssetIDsAndTaskLanguage(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("试听契约")
	asset := env.seedVideoAsset(projectID, 10)

	created := decodeInto[domain.WorkflowRun](t, env.expect(env.request(
		http.MethodPost, "/api/v1/workflow-runs", map[string]any{
			"project_id":         projectID,
			"name":               "试听计划",
			"source_language":    "en",
			"target_languages":   []string{"zh"},
			"input_revision_ids": []string{asset.ID},
			"extract":            map[string]any{"mode": "asr"},
		}), http.StatusCreated))
	if len(created.InputAssetIDs) != 1 || created.InputAssetIDs[0] != asset.ID {
		t.Fatalf("created input_asset_ids = %v, want [%s]", created.InputAssetIDs, asset.ID)
	}

	got := decodeInto[domain.WorkflowRun](t, env.expect(env.request(
		http.MethodGet, "/api/v1/workflow-runs/"+created.ID, nil), http.StatusOK))
	if len(got.InputAssetIDs) != 1 || got.InputAssetIDs[0] != asset.ID {
		t.Fatalf("run snapshot input_asset_ids = %v", got.InputAssetIDs)
	}

	project := decodeInto[domain.Project](t, env.expect(env.request(
		http.MethodGet, "/api/v1/projects/"+projectID, nil), http.StatusOK))
	foundInProject := false
	for _, run := range project.Runs {
		if run.ID != created.ID {
			continue
		}
		foundInProject = true
		if len(run.InputAssetIDs) != 1 || run.InputAssetIDs[0] != asset.ID {
			t.Fatalf("project run input_asset_ids = %v", run.InputAssetIDs)
		}
	}
	if !foundInProject {
		t.Fatalf("project runs = %+v, want run %s", project.Runs, created.ID)
	}

	snapshot := decodeInto[domain.Snapshot](t, env.expect(env.request(
		http.MethodGet, "/api/v1/snapshot?project_id="+projectID, nil), http.StatusOK))
	foundInSnapshot := false
	for _, run := range snapshot.Runs {
		if run.ID != created.ID {
			continue
		}
		foundInSnapshot = true
		if len(run.InputAssetIDs) != 1 || run.InputAssetIDs[0] != asset.ID {
			t.Fatalf("snapshot run input_asset_ids = %v", run.InputAssetIDs)
		}
	}
	if !foundInSnapshot {
		t.Fatalf("snapshot runs = %+v, want run %s", snapshot.Runs, created.ID)
	}

	// 08 预览按 run_id + language 定位：任务响应必须带 language。
	task := decodeInto[domain.Task](t, env.expect(env.request(
		http.MethodPost, "/api/v1/tasks", map[string]any{
			"project_id": projectID,
			"run_id":     created.ID,
			"kind":       "node",
			"node_key":   "08_mix",
			"language":   "zh",
		}), http.StatusAccepted))
	if task.Language != "zh" {
		t.Fatalf("task language = %q, want zh", task.Language)
	}
	listed := decodeInto[domain.TaskList](t, env.expect(env.request(
		http.MethodGet, "/api/v1/tasks?project_id="+projectID, nil), http.StatusOK))
	if len(listed.Items) != 1 || listed.Items[0].Language != "zh" {
		t.Fatalf("task list language = %+v", listed.Items)
	}

	// UI 的 08 预览解析走 snapshot（全量任务），同样必须看到 language。
	taskSnapshot := decodeInto[domain.Snapshot](t, env.expect(env.request(
		http.MethodGet, "/api/v1/snapshot?project_id="+projectID, nil), http.StatusOK))
	foundTask := false
	for _, item := range taskSnapshot.Tasks {
		if item.ID != task.ID {
			continue
		}
		foundTask = true
		if item.Language != "zh" {
			t.Fatalf("snapshot task language = %q, want zh", item.Language)
		}
	}
	if !foundTask {
		t.Fatalf("snapshot tasks = %+v, want task %s", taskSnapshot.Tasks, task.ID)
	}

	// pause/resume 响应同样必须带派生 input_asset_ids（写操作不是契约缺口）。
	pauseKey := "pause-playback-replay"
	paused := decodeInto[domain.WorkflowRun](t, env.expect(env.request(
		http.MethodPost, "/api/v1/workflow-runs/"+created.ID+"/pause", nil, withIdemKey(pauseKey)), http.StatusAccepted))
	if len(paused.InputAssetIDs) != 1 || paused.InputAssetIDs[0] != asset.ID {
		t.Fatalf("paused run input_asset_ids = %v", paused.InputAssetIDs)
	}
	// 幂等回放走 json.RawMessage 响应体，同样不能丢派生字段。
	pausedReplay := decodeInto[domain.WorkflowRun](t, env.expect(env.request(
		http.MethodPost, "/api/v1/workflow-runs/"+created.ID+"/pause", nil, withIdemKey(pauseKey)), http.StatusAccepted))
	if len(pausedReplay.InputAssetIDs) != 1 || pausedReplay.InputAssetIDs[0] != asset.ID {
		t.Fatalf("paused replay input_asset_ids = %v", pausedReplay.InputAssetIDs)
	}
	resumeKey := "resume-playback-replay"
	resumed := decodeInto[domain.WorkflowRun](t, env.expect(env.request(
		http.MethodPost, "/api/v1/workflow-runs/"+created.ID+"/resume", nil, withIdemKey(resumeKey)), http.StatusAccepted))
	if len(resumed.InputAssetIDs) != 1 || resumed.InputAssetIDs[0] != asset.ID {
		t.Fatalf("resumed run input_asset_ids = %v", resumed.InputAssetIDs)
	}
	resumedReplay := decodeInto[domain.WorkflowRun](t, env.expect(env.request(
		http.MethodPost, "/api/v1/workflow-runs/"+created.ID+"/resume", nil, withIdemKey(resumeKey)), http.StatusAccepted))
	if len(resumedReplay.InputAssetIDs) != 1 || resumedReplay.InputAssetIDs[0] != asset.ID {
		t.Fatalf("resumed replay input_asset_ids = %v", resumedReplay.InputAssetIDs)
	}
}

func TestStatusForCoversAllErrorCodes(t *testing.T) {
	codes := map[domain.ErrorCode]int{
		domain.ErrorCodeInvalidRequest:         400,
		domain.ErrorCodeLocalAuthRequired:      401,
		domain.ErrorCodeLocalAccessDenied:      403,
		domain.ErrorCodeRevisionConflict:       409,
		domain.ErrorCodePreflightStale:         409,
		domain.ErrorCodeIdempotencyConflict:    409,
		domain.ErrorCodeModelNotInstalled:      409,
		domain.ErrorCodeRuntimeIncompatible:    409,
		domain.ErrorCodeCapabilityUnverified:   409,
		domain.ErrorCodeInsufficientDisk:       507,
		domain.ErrorCodeCoreNotReady:           503,
		domain.ErrorCodePersistenceUnavailable: 503,
		domain.ErrorCodeWorkerExited:           503,
		domain.ErrorCodeResourceExhausted:      503,
		domain.ErrorCodeEventCursorExpired:     409,
		domain.ErrorCodeNotFound:               404,
		domain.ErrorCodeReviewRequired:         409,
	}
	for code, want := range codes {
		if got := statusFor(code); got != want {
			t.Errorf("statusFor(%s) = %d, want %d", code, got, want)
		}
	}
	// 每个映射错误都是领域错误且 action_ids 归一为数组。
	for code := range codes {
		info := (&domain.Error{Code: code, MessageKey: "error.test"}).Info()
		if info.ActionIDs == nil {
			t.Errorf("action_ids nil for %s", code)
		}
	}
}

// TestUnknownRoutesAndMethods 覆盖未知路径与方法不在白名单时的行为。
func TestUnknownRoutesAndMethods(t *testing.T) {
	env := newTestEnv(t)
	env.expect(env.request(http.MethodGet, "/api/v1/unknown", nil), http.StatusNotFound)
	env.expect(env.request(http.MethodDelete, "/api/v1/system", nil), http.StatusMethodNotAllowed)
}

// TestSaveSubtitleRevisionStyleAndEraseBlocks 覆盖阶段二契约字段：
// style/erase_blocks 保存、Core 补块 ID、revision_digest 只读回显，以及非法几何被拒。
func TestSaveSubtitleRevisionStyleAndEraseBlocks(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("样式与擦除块")
	track := env.seedTrack(projectID, "zh", []domain.Segment{{
		ID: "seg-1", StartMs: 0, EndMs: 1000, Text: "hello",
	}})

	saved := decodeInto[domain.SubtitleRevision](t, env.expect(env.request(
		http.MethodPut, "/api/v1/subtitle-tracks/"+track.ID+"/revisions", map[string]any{
			"base_revision_id": track.CurrentRevisionID,
			"segments": []map[string]any{
				{"id": "seg-1", "start_ms": 0, "end_ms": 1000, "text": "hello world"},
			},
			"style": map[string]any{
				"x": 0.5, "y": 0.85, "font_size": 52, "align": "center",
			},
			"erase_blocks": []map[string]any{
				{
					"segment_id": "seg-1", "start_ms": 0, "end_ms": 900,
					"bbox": []int{10, 20, 110, 120}, "source": "source",
				},
			},
		}), http.StatusCreated))
	if saved.Style == nil || saved.Style.FontSize != 52 || saved.Style.Align != domain.SubtitleAlignCenter {
		t.Fatalf("saved style = %+v", saved.Style)
	}
	if len(saved.EraseBlocks) != 1 {
		t.Fatalf("saved erase blocks = %+v", saved.EraseBlocks)
	}
	block := saved.EraseBlocks[0]
	if block.ID == "" {
		t.Fatal("Core did not assign an erase block id")
	}
	if block.SegmentID != "seg-1" || block.Source != domain.EraseBlockSourceSource ||
		block.BBox != [4]int{10, 20, 110, 120} {
		t.Fatalf("saved erase block = %+v", block)
	}
	if saved.RevisionDigest == "" {
		t.Fatal("saved revision has no revision_digest")
	}

	stored, err := env.store.GetSubtitleTrack(context.Background(), track.ID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if len(stored.Revisions) < 2 {
		t.Fatalf("track revisions = %d", len(stored.Revisions))
	}
	current := stored.Revisions[len(stored.Revisions)-1]
	if current.Style == nil || len(current.EraseBlocks) != 1 {
		t.Fatalf("stored revision style/erase = %+v / %+v", current.Style, current.EraseBlocks)
	}
	if current.RevisionDigest == "" {
		t.Fatal("stored revision digest not derived on read")
	}

	// 旧请求（无 style/erase_blocks）继续可用，新增字段保持 nil。
	legacy := decodeInto[domain.SubtitleRevision](t, env.expect(env.request(
		http.MethodPut, "/api/v1/subtitle-tracks/"+track.ID+"/revisions", map[string]any{
			"base_revision_id": saved.ID,
			"segments": []map[string]any{
				{"id": "seg-1", "start_ms": 0, "end_ms": 1000, "text": "legacy"},
			},
		}), http.StatusCreated))
	if legacy.Style != nil || legacy.EraseBlocks != nil {
		t.Fatalf("legacy request gained style/erase: %+v", legacy)
	}

	// 非法 bbox 在任何写入前返回 400。
	env.expect(env.request(
		http.MethodPut, "/api/v1/subtitle-tracks/"+track.ID+"/revisions", map[string]any{
			"base_revision_id": legacy.ID,
			"segments": []map[string]any{
				{"id": "seg-1", "start_ms": 0, "end_ms": 1000, "text": "legacy"},
			},
			"erase_blocks": []map[string]any{
				{"start_ms": 0, "end_ms": 900, "bbox": []int{100, 20, 10, 120}},
			},
		}), http.StatusBadRequest)
}
