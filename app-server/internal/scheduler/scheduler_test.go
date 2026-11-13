package scheduler

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/events"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/resultcommit"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

type env struct {
	t     *testing.T
	st    *store.Store
	bus   *events.Bus
	svc   *service.Service
	log   *logging.Logger
	sched *Scheduler
	data  string
}

type envOptions struct {
	executor     Executor
	slots        int
	capabilities service.CapabilityProvider
	pipeline     bool
}

func prepareResultFor(enabled bool, svc *service.Service) func(context.Context, domain.Task, resultcommit.ExecutionResult) (PreparedResult, error) {
	return func(ctx context.Context, task domain.Task, result resultcommit.ExecutionResult) (PreparedResult, error) {
		if enabled || task.Kind == domain.TaskKindExport {
			return svc.PrepareNodeResult(ctx, task, result)
		}
		return nil, nil
	}
}

func executionFixtures() map[domain.CapabilitySlotKey]domain.ExecutionConfig {
	fixtures := make(map[domain.CapabilitySlotKey]domain.ExecutionConfig)
	for _, slot := range []domain.CapabilitySlotKey{
		domain.CapabilitySlotASR, domain.CapabilitySlotTranslate, domain.CapabilitySlotTTS,
		domain.CapabilitySlotVoiceClone, domain.CapabilitySlotSubtitleErase, domain.CapabilitySlotSeparation,
		domain.CapabilitySlotOCR, domain.CapabilitySlotFFmpeg,
	} {
		fixtures[slot] = domain.ExecutionConfig{
			ModelLock: domain.ReleaseRef{ReleaseID: "fixture-" + string(slot), Version: "1", Digest: "sha256:fixture"},
			DeviceID:  "cpu", Params: map[string]any{},
		}
	}
	return fixtures
}

func executionLockForNode(node domain.PipelineNodeKey) domain.ReleaseRef {
	slot := domain.CapabilitySlotASR
	switch node {
	case domain.NodeKeyImport:
		slot = domain.CapabilitySlotFFmpeg
	case domain.NodeKeySeparate:
		slot = domain.CapabilitySlotSeparation
	case domain.NodeKeyErase:
		slot = domain.CapabilitySlotSubtitleErase
	case domain.NodeKeyTranslate:
		slot = domain.CapabilitySlotTranslate
	case domain.NodeKeyClone:
		slot = domain.CapabilitySlotVoiceClone
	case domain.NodeKeyMix:
		slot = domain.CapabilitySlotFFmpeg
	}
	return executionFixtures()[slot].ModelLock
}

func newEnv(t *testing.T, opts envOptions) *env {
	t.Helper()
	dataDir, err := os.MkdirTemp("", "yaomu-scheduler-test-")
	if err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	var closedCleanly atomic.Bool
	t.Cleanup(func() { removeTestTempDirWith(t, dataDir, &closedCleanly) })
	st, err := store.Open(context.Background(), filepath.Join(dataDir, "yaomu.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		closeErr := st.Close()
		if closeErr != nil {
			t.Logf("store close error: %v", closeErr)
		}
		// Windows 上未被归还的连接会让临时目录删除失败；留下连接计数，
		// 便于把偶发清理失败区分为自身泄漏与外部进程占用。
		stats := st.DB().Stats()
		if stats.InUse != 0 || stats.OpenConnections != 0 {
			t.Logf("leaked store connections after close: %+v", stats)
		}
		if closeErr == nil && stats.InUse == 0 && stats.OpenConnections == 0 {
			closedCleanly.Store(true)
		}
	})
	logger, err := logging.New(logging.Options{Path: filepath.Join(t.TempDir(), "core.log")})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	bus := events.NewBus(st, logger)
	capabilities := opts.capabilities
	if capabilities == nil {
		capabilities = service.StaticCapabilities{ExecutionFixtures: executionFixtures()}
	}
	if static, ok := capabilities.(service.StaticCapabilities); ok && static.ExecutionFixtures == nil {
		static.ExecutionFixtures = executionFixtures()
		capabilities = static
	}
	svc := service.New(service.Options{
		Store:        st,
		Bus:          bus,
		Log:          logger,
		WorkspaceID:  "ws-sched",
		InstanceID:   "instance-sched",
		ServerEpoch:  11,
		DataDir:      dataDir,
		Capabilities: capabilities,
		Devices:      service.SystemDevices{},
		HeavySlots:   1,

		PipelineOrchestration: opts.pipeline,
	})
	slots := opts.slots
	if slots <= 0 {
		slots = 1
	}
	sched := New(Options{
		Store:        st,
		Bus:          bus,
		Log:          logger,
		Executor:     opts.executor,
		Slots:        slots,
		WorkspaceID:  "ws-sched",
		Epoch:        11,
		WorkDir:      filepath.Join(dataDir, "work"),
		TickInterval: 30 * time.Millisecond,
		Hooks: Hooks{
			PrepareExecution: svc.PrepareTaskExecution,
			PrepareResult:    prepareResultFor(opts.pipeline, svc),
			OnTaskSucceeded:  svc.OnTaskSucceeded,
			OnTaskFailed:     svc.OnTaskFailed,
			OnTaskCanceled:   svc.OnTaskCanceled,
		},
		PreDispatch: func(ctx context.Context, tx *store.Tx, task domain.Task) error {
			return svc.CheckTaskDispatchable(ctx, tx, task)
		},
	})
	svc.SetNotifier(sched)
	svc.SetCanceler(sched)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		sched.Close(ctx)
	})
	return &env{t: t, st: st, bus: bus, svc: svc, log: logger, sched: sched, data: dataDir}
}

// removeTestTempDir 删除测试临时目录。Windows 上 sql.DB.Close() 不会等待仍在
// 使用的连接，SQLite 文件句柄释放也可能滞后，因此做有界重试；最终失败仍会
// 通过 t.Errorf 暴露，不吞掉清理错误。
func removeTestTempDir(t *testing.T, dir string) {
	removeTestTempDirWith(t, dir, nil)
}

// removeTestTempDirWith 在 closedCleanly 为 true 时容忍 Windows 驱动级僵尸句柄：
// modernc.org/sqlite v1.39.0 的 close_v2 可能返回成功但不释放文件句柄，此时
// 存储本身已无错误关闭且无在途连接；其他清理失败仍会失败。
func removeTestTempDirWith(t *testing.T, dir string, closedCleanly *atomic.Bool) {
	t.Helper()
	attempts := 1
	var delay time.Duration
	if runtime.GOOS == "windows" {
		// 整包运行时句柄释放可能明显滞后于单测（杀软/索引与 sqlite close_v2
		// 延迟释放都会拉长窗口）；5s 曾在全套第三轮偶发不足。
		attempts = 100
		delay = 100 * time.Millisecond
	}
	var err error
	for i := 0; i < attempts; i++ {
		if err = os.RemoveAll(dir); err == nil {
			if i > 0 {
				t.Logf("remove temp dir succeeded after %d attempt(s)", i+1)
			}
			return
		}
		if runtime.GOOS == "windows" && i == 0 {
			// close_v2 在仍有未终结语句时会把实际释放推迟到终结器；
			// 主动回收一次，避免整包运行时 GC 滞后拖过清理窗口。
			runtime.GC()
		}
		if delay > 0 && i < attempts-1 {
			time.Sleep(delay)
		}
	}
	if runtime.GOOS == "windows" && closedCleanly != nil && closedCleanly.Load() {
		t.Logf("remove temp dir warning (clean store close, known driver zombie): %s: %v", dir, err)
		return
	}
	t.Errorf("remove temp dir %s after %d attempt(s): %v", dir, attempts, err)
}

func (e *env) seedProject() domain.ID {
	e.t.Helper()
	resp, err := e.svc.CreateProject(context.Background(), service.Idem{}, service.CreateProjectRequest{Name: "调度项目"})
	if err != nil {
		e.t.Fatalf("CreateProject: %v", err)
	}
	return resp.Body.(domain.Project).ID
}

func (e *env) seedRun(projectID string) domain.WorkflowRun {
	e.t.Helper()
	resp, err := e.svc.CreateWorkflowRun(context.Background(), service.Idem{}, service.CreateWorkflowRunInput{
		ProjectID:       projectID,
		Name:            "调度计划",
		SourceLanguage:  "en",
		TargetLanguages: []string{"zh"},
		Extract:         &domain.ExtractOptions{Mode: domain.ExtractModeASR},
	})
	if err != nil {
		e.t.Fatalf("CreateWorkflowRun: %v", err)
	}
	return resp.Body.(domain.WorkflowRun)
}

func (e *env) seedTask(projectID, runID string, node domain.PipelineNodeKey) domain.Task {
	e.t.Helper()
	lock := executionLockForNode(node)
	resp, err := e.svc.CreateTask(context.Background(), service.Idem{}, service.CreateTaskRequest{
		ProjectID: projectID,
		RunID:     runID,
		Kind:      domain.TaskKindNode,
		NodeKey:   node,
		ModelLock: &lock,
	})
	if err != nil {
		e.t.Fatalf("CreateTask: %v", err)
	}
	return resp.Body.(domain.Task)
}

// seedMediaAsset 登记一个带真实文件位置的源媒体（导出计划冻结输入用）。
func (e *env) seedMediaAsset(projectID domain.ID) domain.Asset {
	e.t.Helper()
	ctx := context.Background()
	path := filepath.Join(e.data, "source.mp4")
	if err := os.WriteFile(path, []byte("fake-media"), 0o600); err != nil {
		e.t.Fatalf("write source media: %v", err)
	}
	now := time.Now().UTC()
	asset := domain.Asset{
		ID:         store.NewID("asset"),
		ProjectID:  projectID,
		Type:       domain.AssetTypeVideo,
		Name:       "source.mp4",
		SizeBytes:  10,
		SHA256:     fmt.Sprintf("%x", sha256.Sum256([]byte("fake-media"))),
		Status:     domain.AssetStatusReady,
		ImportedAt: now,
	}
	if err := e.st.CreateAsset(ctx, asset); err != nil {
		e.t.Fatalf("CreateAsset: %v", err)
	}
	if err := e.st.AddAssetLocation(ctx, domain.AssetLocation{
		ID:        store.NewID("loc"),
		AssetID:   asset.ID,
		Path:      path,
		Source:    domain.LocationSourceExternalReference,
		Owner:     domain.LocationOwnerUser,
		Available: true,
		CreatedAt: now,
	}); err != nil {
		e.t.Fatalf("AddAssetLocation: %v", err)
	}
	built, err := e.st.GetAsset(ctx, asset.ID)
	if err != nil {
		e.t.Fatalf("GetAsset: %v", err)
	}
	return built
}

func (e *env) seedRunWithInputs(projectID domain.ID, assetIDs ...string) domain.WorkflowRun {
	e.t.Helper()
	resp, err := e.svc.CreateWorkflowRun(context.Background(), service.Idem{}, service.CreateWorkflowRunInput{
		ProjectID:        projectID,
		Name:             "导出计划",
		SourceLanguage:   "en",
		TargetLanguages:  []string{"zh"},
		InputRevisionIDs: assetIDs,
		Extract:          &domain.ExtractOptions{Mode: domain.ExtractModeASR},
	})
	if err != nil {
		e.t.Fatalf("CreateWorkflowRun: %v", err)
	}
	return resp.Body.(domain.WorkflowRun)
}

// seedTrack 直接写入一条字幕轨与初始 revision（交付门禁 fixture）。
func (e *env) seedTrack(projectID domain.ID, language string, segments []domain.Segment) domain.SubtitleTrack {
	e.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	revisionID := store.NewID("srev")
	track := domain.SubtitleTrack{
		ID:                store.NewID("track"),
		ProjectID:         projectID,
		Language:          language,
		Type:              domain.TrackTypeTranslation,
		CurrentRevisionID: revisionID,
		CreatedAt:         now,
		UpdatedAt:         now,
		Revisions: []domain.SubtitleRevision{{
			ID:         revisionID,
			AuthorType: domain.AuthorTypeSystem,
			Segments:   segments,
			CreatedAt:  now,
		}},
	}
	if err := e.st.CreateSubtitleTrack(ctx, track); err != nil {
		e.t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	built, err := e.st.GetSubtitleTrack(ctx, track.ID)
	if err != nil {
		e.t.Fatalf("GetSubtitleTrack: %v", err)
	}
	return built
}

// seedApprovedDelivery 放行绑定 track 当前 revision 的 delivery 门槛。
func (e *env) seedApprovedDelivery(runID domain.ID, track domain.SubtitleTrack) domain.ReviewGate {
	e.t.Helper()
	ctx := context.Background()
	var revision *domain.SubtitleRevision
	for i := range track.Revisions {
		if track.Revisions[i].ID == track.CurrentRevisionID {
			revision = &track.Revisions[i]
			break
		}
	}
	if revision == nil {
		e.t.Fatalf("track %s has no current revision", track.ID)
	}
	digest, err := domain.RevisionBindingDigest(*revision)
	if err != nil {
		e.t.Fatalf("RevisionBindingDigest: %v", err)
	}
	bind := domain.DeliveryBind(digest)
	gate := domain.ReviewGate{
		ID:             store.NewID("review"),
		RunID:          runID,
		Type:           domain.ReviewTypeDelivery,
		RevisionDigest: bind,
		Status:         domain.ReviewStatusPending,
	}
	if err := e.st.CreateReviewGate(ctx, gate); err != nil {
		e.t.Fatalf("CreateReviewGate: %v", err)
	}
	approved, err := e.st.ApproveReview(ctx, gate.ID, bind, domain.ReviewTypeDelivery, time.Now().UTC())
	if err != nil {
		e.t.Fatalf("ApproveReview: %v", err)
	}
	// 视频/音频导出必须绑定当前 revision 的成功 08；fixture 同时放行 08 交付门槛。
	now := time.Now().UTC()
	mixTask := domain.Task{
		ID: store.NewID("task"), ProjectID: track.ProjectID, RunID: runID,
		Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyMix, Language: track.Language,
		Status: domain.TaskStatusSucceeded, ArtifactIDs: []domain.ID{"asset-mix"},
		InputDigest: "mix-input-" + string(revision.ID), CreatedAt: now, UpdatedAt: now,
		Params: map[string]any{
			"language": track.Language,
			"_core":    map[string]any{"subtitle_revision_id": string(revision.ID)},
		},
	}
	if err := e.st.CreateTask(ctx, mixTask); err != nil {
		e.t.Fatalf("CreateTask(mix): %v", err)
	}
	mixDigest, err := domain.NodeReviewDigest(mixTask.RunID, mixTask.NodeKey, mixTask.Language,
		mixTask.ID, mixTask.ArtifactIDs, mixTask.InputDigest)
	if err != nil {
		e.t.Fatalf("NodeReviewDigest(mix): %v", err)
	}
	mixGate := domain.ReviewGate{
		ID: store.NewID("review"), RunID: runID, Type: domain.ReviewTypeDelivery,
		Language: track.Language, RevisionDigest: mixDigest, Status: domain.ReviewStatusPending,
	}
	if err := e.st.CreateReviewGate(ctx, mixGate); err != nil {
		e.t.Fatalf("CreateReviewGate(mix): %v", err)
	}
	if _, err := e.st.ApproveReview(ctx, mixGate.ID, mixDigest, domain.ReviewTypeDelivery, now); err != nil {
		e.t.Fatalf("ApproveReview(mix): %v", err)
	}
	return approved
}

// createExport 创建一条音频导出任务（fixture 已放行交付审核）。
func (e *env) createExport(projectID, runID, outDir string) domain.ExportRecord {
	e.t.Helper()
	resp, err := e.svc.CreateExport(context.Background(), service.Idem{}, service.ExportRequest{
		ProjectID: projectID, RunID: runID, Kind: domain.ExportKindAudio,
		Format: domain.ExportFormatMP3, OutputDirGrant: outDir, NamingTemplate: "{project}",
	})
	if err != nil {
		e.t.Fatalf("CreateExport: %v", err)
	}
	return resp.Body.(domain.ExportRecord)
}

// startScheduler 在测试中启动调度器；恢复失败直接终止测试。
func startScheduler(t *testing.T, s *Scheduler, ctx context.Context) {
	t.Helper()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("scheduler start: %v", err)
	}
}

func (e *env) waitTask(id domain.ID, want domain.TaskStatus, timeout time.Duration) domain.Task {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	var last domain.Task
	for time.Now().Before(deadline) {
		task, err := e.st.GetTask(context.Background(), id)
		if err != nil {
			e.t.Fatalf("GetTask: %v", err)
		}
		last = task
		if task.Status == want {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("task %s kind=%s node=%s status=%s error=%+v, want %s",
		id, last.Kind, last.NodeKey, last.Status, last.Error, want)
	return last
}

// fakeExecutor 是可注入的测试执行器。
type fakeExecutor struct {
	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
	started   chan domain.ID
	gate      chan struct{}
	holdCtx   bool
	progress  func(execadapter.ProgressFunc)
	result    func(req execadapter.Request) execadapter.Result
}

func (f *fakeExecutor) Execute(ctx context.Context, req execadapter.Request, progress execadapter.ProgressFunc, _ execadapter.LogFunc) execadapter.Result {
	f.mu.Lock()
	f.calls++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	if f.started != nil {
		select {
		case f.started <- domain.ID(req.TaskID):
		default:
		}
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
		}
	}
	if f.holdCtx {
		<-ctx.Done()
		return execadapter.Result{TransportErr: ctx.Err()}
	}
	if ctx.Err() != nil {
		return execadapter.Result{TransportErr: ctx.Err()}
	}
	if f.progress != nil {
		f.progress(progress)
	}
	if f.result != nil {
		return f.result(req)
	}
	return execadapter.Result{
		ArtifactIDs: []string{"artifact-" + req.TaskID},
		Binding: execadapter.Binding{
			Workspace:   req.Workspace,
			ServerEpoch: req.ServerEpoch,
			TaskID:      req.TaskID,
			StepID:      req.StepID,
			Attempt:     req.Attempt,
		},
	}
}

func (f *fakeExecutor) snapshot() (calls, maxActive int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.maxActive
}

// TestDispatchSingleHeavySlot 覆盖单重型槽串行派发与成功终态提交。
func TestDispatchSingleHeavySlot(t *testing.T) {
	executor := &fakeExecutor{gate: make(chan struct{}), started: make(chan domain.ID, 1)}
	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	taskA := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)
	taskB := e.seedTask(projectID, run.ID, domain.NodeKeySeparate)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()

	// 等待执行器真正收到任务，而不是只靠数据库短轮询；Windows 上
	// SQLite 读轮询会与派发事务争用，3s 轮询偶发超时。
	// created_at 在低精度系统时钟上可能相同，派发排序随后回落到随机 id，
	// 因此先创建的任务不保证先派发；单槽串行语义与两者先后无关。
	var first, second domain.Task
	select {
	case id := <-executor.started:
		switch id {
		case taskA.ID:
			first, second = taskA, taskB
		case taskB.ID:
			first, second = taskB, taskA
		default:
			t.Fatalf("executor started unknown task %s", id)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("executor did not start a task")
	}
	e.waitTask(first.ID, domain.TaskStatusRunning, 5*time.Second)
	// 单槽：另一个任务在第一个完成前不能进入 RUNNING。
	time.Sleep(150 * time.Millisecond)
	pending, err := e.st.GetTask(context.Background(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != domain.TaskStatusQueued {
		t.Fatalf("task %s status = %s while %s running", second.ID, pending.Status, first.ID)
	}
	close(executor.gate)

	doneFirst := e.waitTask(first.ID, domain.TaskStatusSucceeded, 5*time.Second)
	if len(doneFirst.ArtifactIDs) != 1 {
		t.Fatalf("artifacts = %v", doneFirst.ArtifactIDs)
	}
	doneSecond := e.waitTask(second.ID, domain.TaskStatusSucceeded, 5*time.Second)
	if len(doneSecond.ArtifactIDs) != 1 {
		t.Fatalf("artifacts = %v", doneSecond.ArtifactIDs)
	}
	calls, maxActive := executor.snapshot()
	if calls != 2 || maxActive != 1 {
		t.Fatalf("calls = %d, maxActive = %d", calls, maxActive)
	}
	// 尝试记录终态为 SUCCEEDED。
	attempts, err := e.st.ListAttempts(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != domain.StepStatusSucceeded {
		t.Fatalf("attempts = %+v", attempts)
	}
}

// TestPausedRunRegistersWithoutDispatch 覆盖暂停只登记、恢复后才派发。
func TestPausedRunRegistersWithoutDispatch(t *testing.T) {
	executor := &fakeExecutor{}
	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	run := e.seedRun(projectID)

	if _, err := e.svc.PauseWorkflowRun(context.Background(), service.Idem{}, run.ID); err != nil {
		t.Fatalf("PauseWorkflowRun: %v", err)
	}
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)
	if task.Status != domain.TaskStatusPaused {
		t.Fatalf("task status = %s, want PAUSED", task.Status)
	}

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()
	time.Sleep(250 * time.Millisecond)
	calls, _ := executor.snapshot()
	if calls != 0 {
		t.Fatalf("executor called %d times while paused", calls)
	}
	if current, _ := e.st.GetTask(context.Background(), task.ID); current.Status != domain.TaskStatusPaused {
		t.Fatalf("task status = %s while paused", current.Status)
	}

	if _, err := e.svc.ResumeWorkflowRun(context.Background(), service.Idem{}, run.ID); err != nil {
		t.Fatalf("ResumeWorkflowRun: %v", err)
	}
	e.waitTask(task.ID, domain.TaskStatusSucceeded, 5*time.Second)
	if calls, _ := executor.snapshot(); calls != 1 {
		t.Fatalf("executor calls = %d after resume", calls)
	}
}

// TestCancelRunningTask 覆盖取消受理与执行确认退出后才释放槽。
func TestCancelRunningTask(t *testing.T) {
	executor := &fakeExecutor{started: make(chan domain.ID, 1), holdCtx: true}
	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()
	select {
	case <-executor.started:
	case <-time.After(3 * time.Second):
		t.Fatalf("executor did not start")
	}
	e.waitTask(task.ID, domain.TaskStatusRunning, 2*time.Second)
	if e.sched.RunningCount() != 1 {
		t.Fatalf("running count = %d", e.sched.RunningCount())
	}

	if _, err := e.svc.CancelTask(context.Background(), service.Idem{}, task.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	e.waitTask(task.ID, domain.TaskStatusCanceled, 5*time.Second)
	// 取消不是失败：节点不能被标记为 failed。
	runAfter, err := e.st.GetWorkflowRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range runAfter.Nodes {
		if node.Key == domain.NodeKeyAnalyze && node.Status == domain.NodeStatusFailed {
			t.Fatalf("canceled task marked node failed")
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for e.sched.RunningCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("running count = %d after cancel", e.sched.RunningCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestMalformedCurrentResultFailsWithoutCommit rejects a terminal frame that
// cannot authenticate the active attempt and leaves no fabricated success.
func TestMalformedCurrentResultFailsWithoutCommit(t *testing.T) {
	executor := &fakeExecutor{
		gate:    make(chan struct{}),
		started: make(chan domain.ID, 1),
		result: func(req execadapter.Request) execadapter.Result {
			// 绑定旧 attempt 编号：不得完成当前尝试。
			return execadapter.Result{
				ArtifactIDs: []string{"stale-artifact"},
				Binding: execadapter.Binding{
					Workspace:   req.Workspace,
					TaskID:      req.TaskID,
					StepID:      req.StepID,
					Attempt:     req.Attempt + 7,
					ServerEpoch: req.ServerEpoch,
				},
			}
		},
	}
	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()
	select {
	case <-executor.started:
	case <-time.After(20 * time.Second):
		t.Fatal("executor did not start")
	}
	e.waitTask(task.ID, domain.TaskStatusRunning, 5*time.Second)
	close(executor.gate)
	time.Sleep(300 * time.Millisecond)

	current, err := e.st.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != domain.TaskStatusFailed || current.Error == nil ||
		current.Error.Code != domain.ErrorCodeInvalidRequest {
		t.Fatalf("task status = %s error=%+v, want INVALID_REQUEST", current.Status, current.Error)
	}
	if len(current.ArtifactIDs) != 0 {
		t.Fatalf("stale artifacts committed: %v", current.ArtifactIDs)
	}
}

// TestPauseSettlesAtSafeBoundary 覆盖暂停在安全单元边界生效。
func TestPauseSettlesAtSafeBoundary(t *testing.T) {
	executor := &fakeExecutor{started: make(chan domain.ID, 1), gate: make(chan struct{})}
	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()
	select {
	case <-executor.started:
	case <-time.After(3 * time.Second):
		t.Fatalf("executor did not start")
	}
	resp, err := e.svc.PauseWorkflowRun(context.Background(), service.Idem{}, run.ID)
	if err != nil {
		t.Fatalf("PauseWorkflowRun: %v", err)
	}
	if paused := resp.Body.(domain.WorkflowRun); paused.Status != domain.RunStatusPauseRequested {
		t.Fatalf("run status = %s, want PAUSE_REQUESTED", paused.Status)
	}
	close(executor.gate)
	e.waitTask(task.ID, domain.TaskStatusSucceeded, 5*time.Second)

	deadline := time.Now().Add(5 * time.Second)
	for {
		current, err := e.st.GetWorkflowRun(context.Background(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == domain.RunStatusPaused {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run status = %s, want PAUSED", current.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRecoverMarksInterrupted 覆盖重启恢复：不自动重复长时间推理。
func TestRecoverMarksInterrupted(t *testing.T) {
	e := newEnv(t, envOptions{executor: nil})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	// 模拟上次实例遗留的 RUNNING 任务与尝试。
	ctx := context.Background()
	if _, err := e.st.DB().ExecContext(ctx,
		`UPDATE tasks SET status = 'RUNNING', state_version = state_version + 1 WHERE id = ?`, task.ID); err != nil {
		t.Fatal(err)
	}
	attempts, err := e.st.ListAttempts(ctx, task.ID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %+v, err = %v", attempts, err)
	}
	if err := e.st.UpdateAttempt(ctx, attempts[0].ID, domain.StepStatusRunning, nil, nil); err != nil {
		t.Fatal(err)
	}

	startScheduler(t, e.sched, context.Background())
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, err := e.st.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == domain.TaskStatusInterrupted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task status = %s, want INTERRUPTED", current.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	updated, err := e.st.ListAttempts(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated[0].Status != domain.StepStatusInterrupted {
		t.Fatalf("attempt status = %s", updated[0].Status)
	}
	// 事件已持久化：task.interrupted。
	eventsAfter, err := e.st.EventsAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range eventsAfter {
		if ev.Type == "task.interrupted" && ev.TaskID == task.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("task.interrupted event missing: %+v", eventsAfter)
	}
}

// TestFailedTaskHookUpdatesExportRecord 验证失败钩子更新导出状态。
func TestFailedTaskHookUpdatesExportRecord(t *testing.T) {
	executor := &fakeExecutor{result: func(req execadapter.Request) execadapter.Result {
		return execadapter.Result{
			Error:   &execadapter.ExecError{Code: "MODEL_RUNTIME_UNAVAILABLE", Retryable: false},
			Binding: requestBinding(req),
		}
	}}
	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	source := e.seedMediaAsset(projectID)
	run := e.seedRunWithInputs(projectID, source.ID)
	track := e.seedTrack(projectID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 500, Text: "你好"},
	})
	e.seedApprovedDelivery(run.ID, track)
	resp, err := e.svc.CreateExport(context.Background(), service.Idem{}, service.ExportRequest{
		ProjectID:      projectID,
		RunID:          run.ID,
		Kind:           domain.ExportKindAudio,
		Format:         domain.ExportFormatMP3,
		OutputDirGrant: t.TempDir(),
		NamingTemplate: "{project}",
	})
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	record := resp.Body.(domain.ExportRecord)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()

	deadline := time.Now().Add(5 * time.Second)
	for {
		exports, err := e.st.ListExports(context.Background(), projectID)
		if err != nil {
			t.Fatal(err)
		}
		if len(exports) == 1 && exports[0].Status == domain.TaskStatusFailed {
			if exports[0].ID != record.ID {
				t.Fatalf("export id = %s", exports[0].ID)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("export status = %+v", exports)
		}
		time.Sleep(20 * time.Millisecond)
	}

	task, err := e.st.GetTask(context.Background(), record.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != domain.TaskStatusFailed || task.Error == nil ||
		task.Error.Code != domain.ErrorCodeModelNotInstalled {
		t.Fatalf("task = %+v", task)
	}
	if !task.Error.Retryable {
		// MODEL_RUNTIME_UNAVAILABLE 由门控适配器上报，retryable=false 时保留门控语义。
		t.Logf("retryable = %v (runner-provided)", task.Error.Retryable)
	}
	_ = fmt.Sprint()
}

// TestExportDispatchesAfterRunTerminal 覆盖导出不受来源 Run 闸门约束：
// 交付动作在 Run 终止后仍可执行并完成落盘提交。
func TestExportDispatchesAfterRunTerminal(t *testing.T) {
	executor := &fakeExecutor{result: func(req execadapter.Request) execadapter.Result {
		payload := append([]byte("ID3"), make([]byte, 48)...)
		path := filepath.Join(req.OutputDir, "out.mp3")
		if err := os.MkdirAll(req.OutputDir, 0o755); err != nil {
			t.Errorf("mkdir attempt dir: %v", err)
		}
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Errorf("write fake output: %v", err)
		}
		return execadapter.Result{
			OutputDir: req.OutputDir,
			Outputs:   []execadapter.Output{{RelPath: "out.mp3", SizeBytes: int64(len(payload))}},
			Binding: execadapter.Binding{
				Workspace: req.Workspace, TaskID: req.TaskID, StepID: req.StepID, Attempt: req.Attempt, ServerEpoch: req.ServerEpoch,
			},
		}
	}}
	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	source := e.seedMediaAsset(projectID)
	run := e.seedRunWithInputs(projectID, source.ID)
	track := e.seedTrack(projectID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 500, Text: "你好"},
	})
	e.seedApprovedDelivery(run.ID, track)
	// 交付导出发生在审核完成之后：来源 Run 已是终止状态。
	if _, err := e.st.DB().ExecContext(context.Background(),
		`UPDATE workflow_runs SET status = 'SUCCEEDED', state_version = state_version + 1 WHERE id = ?`,
		run.ID); err != nil {
		t.Fatalf("terminate run: %v", err)
	}

	resp, err := e.svc.CreateExport(context.Background(), service.Idem{}, service.ExportRequest{
		ProjectID:      projectID,
		RunID:          run.ID,
		Kind:           domain.ExportKindAudio,
		Format:         domain.ExportFormatMP3,
		OutputDirGrant: t.TempDir(),
		NamingTemplate: "{project}",
	})
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	record := resp.Body.(domain.ExportRecord)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, err := e.st.GetExport(context.Background(), record.ID)
		if err != nil {
			t.Fatalf("GetExport: %v", err)
		}
		if current.Status == domain.TaskStatusSucceeded {
			if current.OutputPath == "" {
				t.Fatalf("succeeded export has no output path")
			}
			if _, err := os.Stat(current.OutputPath); err != nil {
				t.Fatalf("landed output missing: %v", err)
			}
			return
		}
		if current.Status == domain.TaskStatusFailed {
			task, _ := e.st.GetTask(context.Background(), record.TaskID)
			t.Fatalf("export failed: %+v task=%+v", current, task)
		}
		if time.Now().After(deadline) {
			t.Fatalf("export status = %s, want SUCCEEDED", current.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestExportsSerializePerExecutor 覆盖导出串行化：即使重型槽大于 1，
// 同一执行器同时只执行一个导出，第二个保持 QUEUED，完成后再取且不丢任务。
func TestExportsSerializePerExecutor(t *testing.T) {
	executor := &fakeExecutor{gate: make(chan struct{}), started: make(chan domain.ID, 2)}
	executor.result = func(req execadapter.Request) execadapter.Result {
		payload := append([]byte("ID3"), make([]byte, 48)...)
		path := filepath.Join(req.OutputDir, "out.mp3")
		if err := os.MkdirAll(req.OutputDir, 0o755); err != nil {
			t.Errorf("mkdir attempt dir: %v", err)
		}
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Errorf("write fake output: %v", err)
		}
		return execadapter.Result{
			OutputDir: req.OutputDir,
			Outputs:   []execadapter.Output{{RelPath: "out.mp3", SizeBytes: int64(len(payload))}},
			Binding: execadapter.Binding{
				Workspace: req.Workspace, TaskID: req.TaskID, StepID: req.StepID, Attempt: req.Attempt, ServerEpoch: req.ServerEpoch,
			},
		}
	}
	e := newEnv(t, envOptions{executor: executor, slots: 2})
	projectID := e.seedProject()
	source := e.seedMediaAsset(projectID)
	track := e.seedTrack(projectID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 500, Text: "你好"},
	})
	run := e.seedRunWithInputs(projectID, source.ID)
	e.seedApprovedDelivery(run.ID, track)

	first := e.createExport(projectID, run.ID, t.TempDir())
	second := e.createExport(projectID, run.ID, t.TempDir())

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()

	var running domain.ID
	select {
	case running = <-executor.started:
	case <-time.After(3 * time.Second):
		t.Fatal("no export started")
	}
	other := first.TaskID
	if running == first.TaskID {
		other = second.TaskID
	}
	// 第二个导出必须保持 QUEUED：导出按执行器互斥，Runner 单执行。
	time.Sleep(150 * time.Millisecond)
	pending, err := e.st.GetTask(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != domain.TaskStatusQueued {
		t.Fatalf("second export status = %s while first running", pending.Status)
	}
	close(executor.gate)

	e.waitTask(first.TaskID, domain.TaskStatusSucceeded, 5*time.Second)
	e.waitTask(second.TaskID, domain.TaskStatusSucceeded, 5*time.Second)
	calls, maxActive := executor.snapshot()
	if calls != 2 || maxActive != 1 {
		t.Fatalf("calls = %d, maxActive = %d", calls, maxActive)
	}
}

// TestMediaToolUnavailableMapsToReadableFailure 覆盖缺少 FFmpeg 时的错误映射：
// 任务失败码为 MODEL_NOT_INSTALLED（可打开运行时修复），导出记录 FAILED。
func TestMediaToolUnavailableMapsToReadableFailure(t *testing.T) {
	executor := &fakeExecutor{result: func(req execadapter.Request) execadapter.Result {
		return execadapter.Result{
			Error: &execadapter.ExecError{
				Code:      "MEDIA_TOOL_UNAVAILABLE",
				Message:   "未注入 FFMPEG_BIN",
				Retryable: false,
				ActionIDs: []string{"open_engine_setup"},
			},
			Binding: requestBinding(req),
		}
	}}
	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	source := e.seedMediaAsset(projectID)
	run := e.seedRunWithInputs(projectID, source.ID)
	track := e.seedTrack(projectID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 500, Text: "你好"},
	})
	e.seedApprovedDelivery(run.ID, track)
	resp, err := e.svc.CreateExport(context.Background(), service.Idem{}, service.ExportRequest{
		ProjectID:      projectID,
		RunID:          run.ID,
		Kind:           domain.ExportKindVideo,
		Format:         domain.ExportFormatMP4H264,
		OutputDirGrant: t.TempDir(),
		NamingTemplate: "{project}",
	})
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	record := resp.Body.(domain.ExportRecord)

	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()
	deadline := time.Now().Add(5 * time.Second)
	for {
		task, err := e.st.GetTask(context.Background(), record.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status == domain.TaskStatusFailed {
			if task.Error == nil || task.Error.Code != domain.ErrorCodeModelNotInstalled {
				t.Fatalf("task error = %+v, want MODEL_NOT_INSTALLED", task.Error)
			}
			if task.Error.MessageKey != "task.media_tool_unavailable" {
				t.Fatalf("message key = %s", task.Error.MessageKey)
			}
			// 任务终态与 export 记录分属两个事务：观察到 task FAILED 时，
			// export 记录可能仍是 QUEUED，需要轮询到导出记录收敛。
			exportDeadline := time.Now().Add(5 * time.Second)
			for {
				exportRecord, err := e.st.GetExport(context.Background(), record.ID)
				if err != nil {
					t.Fatal(err)
				}
				if exportRecord.Status == domain.TaskStatusFailed {
					if exportRecord.OutputPath != "" {
						t.Fatalf("failed export output path = %q, want empty", exportRecord.OutputPath)
					}
					return
				}
				if time.Now().After(exportDeadline) {
					t.Fatalf("export status = %s, want FAILED", exportRecord.Status)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("task status = %s, want FAILED", task.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type mutableCapabilities struct {
	mu     sync.Mutex
	states map[domain.CapabilitySlotKey]domain.SlotState
}

func (m *mutableCapabilities) set(slot domain.CapabilitySlotKey, state domain.SlotState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.states == nil {
		m.states = map[domain.CapabilitySlotKey]domain.SlotState{}
	}
	m.states[slot] = state
}

func (m *mutableCapabilities) Catalog(ctx context.Context, q service.CatalogQuerier) ([]domain.Capability, error) {
	m.mu.Lock()
	states := make(map[domain.CapabilitySlotKey]domain.SlotState, len(m.states))
	for slot, state := range m.states {
		states[slot] = state
	}
	m.mu.Unlock()
	return service.StaticCapabilities{States: states}.Catalog(ctx, q)
}

func (m *mutableCapabilities) ExecutionFixture(
	slot domain.CapabilitySlotKey,
) (domain.ExecutionConfig, bool) {
	cfg, ok := executionFixtures()[slot]
	return cfg, ok
}

// TestPreDispatchRecheckWaitsForCapability 覆盖派发前重检：能力未就绪时转入
// WAITING_RESOURCE，不调用执行器；就绪后自动继续。
func TestPreDispatchRecheckWaitsForCapability(t *testing.T) {
	capabilities := &mutableCapabilities{}
	executor := &fakeExecutor{}
	e := newEnv(t, envOptions{executor: executor, capabilities: capabilities})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	task := e.seedTask(projectID, run.ID, domain.NodeKeyAnalyze)

	// 任务创建后能力被移除：派发前重检必须拦住。
	capabilities.set(domain.CapabilitySlotASR, domain.SlotStateNeedsSetup)
	startScheduler(t, e.sched, context.Background())
	e.sched.Notify()

	deadline := time.Now().Add(5 * time.Second)
	for {
		current, err := e.st.GetTask(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == domain.TaskStatusWaitingResource {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task status = %s, want WAITING_RESOURCE", current.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if calls, _ := executor.snapshot(); calls != 0 {
		t.Fatalf("executor called %d times while capability missing", calls)
	}

	// 能力恢复（如安装完成）后，下一轮重检自动派发。
	capabilities.set(domain.CapabilitySlotASR, domain.SlotStateReady)
	e.sched.Notify()
	e.waitTask(task.ID, domain.TaskStatusSucceeded, 5*time.Second)
	if calls, _ := executor.snapshot(); calls != 1 {
		t.Fatalf("executor calls = %d after capability ready", calls)
	}
}

// TestPauseCreateRaceKeepsDispatchGate 并发创建任务与请求暂停：
// 暂停收敛后不得再出现 QUEUED/WAITING_RESOURCE 任务，也不得再派发执行器。
func TestPauseCreateRaceKeepsDispatchGate(t *testing.T) {
	executor := &fakeExecutor{}
	e := newEnv(t, envOptions{executor: executor})
	projectID := e.seedProject()
	run := e.seedRun(projectID)
	startScheduler(t, e.sched, context.Background())

	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = e.svc.CreateTask(ctx, service.Idem{}, service.CreateTaskRequest{
				ProjectID: projectID,
				RunID:     run.ID,
				Kind:      domain.TaskKindNode,
				NodeKey:   domain.NodeKeyAnalyze,
			})
		}()
		go func() {
			defer wg.Done()
			_, _ = e.svc.PauseWorkflowRun(ctx, service.Idem{}, run.ID)
		}()
	}
	wg.Wait()
	if _, err := e.svc.PauseWorkflowRun(ctx, service.Idem{}, run.ID); err != nil {
		t.Fatalf("PauseWorkflowRun: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		current, err := e.st.GetWorkflowRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == domain.RunStatusPaused {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run status = %s, want PAUSED", current.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 暂停收敛后在途执行已结束：不再派发，也不能留下可派发任务。
	time.Sleep(100 * time.Millisecond)
	tasks, err := e.st.ListProjectTasks(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		switch task.Status {
		case domain.TaskStatusSucceeded, domain.TaskStatusPaused, domain.TaskStatusCanceled:
		default:
			t.Fatalf("task %s status = %s after run paused", task.ID, task.Status)
		}
	}
}
