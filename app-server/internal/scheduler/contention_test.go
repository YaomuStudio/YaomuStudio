package scheduler

// SQLite 单写连接争用测量夹具（默认短测；YAOMU_CONTENTION=1 跑 100/1000/10000）。
//
// 测量对象是 scheduler.waiting_resource.go 的 markBlockedTasks：
//   - 候选在事务外读取，事务内逐条 GetTask + Run 闸门复核，首次阻断写入
//     WAITING_RESOURCE 与事件，同原因稳态只读不写；
//   - 单连接池（MaxOpenConns=1）下，tick 持有连接期间其他请求在 database/sql
//     连接队列上等待，这是需要与 CPU/文件 IO 分开观察的争用。
//
// 约束：只写临时 SQLite 文件；固定种子与负载；不记录 SQL 正文、参数、路径或
// 媒体内容；产品写路径不因测量而改变。

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	internaldb "github.com/YaomuStudio/YaomuStudio/app-server/internal/db"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/events"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"

	sqlite "modernc.org/sqlite"
)

const (
	contentionEnv       = "YAOMU_CONTENTION"
	contentionRepsEnv   = "YAOMU_CONTENTION_REPS"
	contentionSteadyEnv = "YAOMU_CONTENTION_STEADY"
)

type contentionFixture struct {
	t   *testing.T
	dir string

	db       *sql.DB
	st       *store.Store
	bus      *events.Bus
	svc      *service.Service
	sched    *Scheduler
	block    dispatchBlock
	dbPath   string
	scale    int
	sqliteV  string
	dbBytes  int64
	walBytes int64

	projectID      domain.ID
	measuredRunID  domain.ID
	measuredTaskID []domain.ID
	pauseRunID     domain.ID
	cancelTaskID   domain.ID
	progressTaskID domain.ID
	progressStepID string
	progressAttID  domain.ID

	resetTotal time.Duration
	resetCount int
}

// ---- 测试入口 ----

// TestSQLiteContentionMeasure 默认只跑一个很小的短测，验证夹具与断言；
// YAOMU_CONTENTION=1 时跑完整规模与重复，并输出 p50/p95/max。
func TestSQLiteContentionMeasure(t *testing.T) {
	full := os.Getenv(contentionEnv) == "1"
	scales := []int{40}
	reps := 1
	if full {
		scales = []int{100, 1000, 10000}
		reps = envPositiveInt(contentionRepsEnv, 5)
	}
	steadyPerRep := 3
	if !full {
		steadyPerRep = 2
	}
	steadyPerRep = envPositiveInt(contentionSteadyEnv, steadyPerRep)

	t.Logf("contention environment: full=%t scales=%v reps=%d steady_per_rep=%d go=%s goos=%s goarch=%s gomaxprocs=%d",
		full, scales, reps, steadyPerRep, runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0))

	for _, scale := range scales {
		scale := scale
		t.Run(fmt.Sprintf("tasks=%d", scale), func(t *testing.T) {
			fx := newContentionFixture(t, scale)
			runContentionScale(t, fx, reps, steadyPerRep, full)
		})
	}
}

func runContentionScale(t *testing.T, fx *contentionFixture, reps, steadyPerRep int, full bool) {
	t.Helper()

	var firstSamples, steadySamples []tickSample
	for rep := 0; rep < reps; rep++ {
		firstSamples = append(firstSamples, fx.measureIsolatedTick(t, "first_block", true))
		for j := 0; j < steadyPerRep; j++ {
			steadySamples = append(steadySamples, fx.measureIsolatedTick(t, "steady", false))
		}
	}
	logTickSamples(t, fmt.Sprintf("scale=%d phase=first_block mode=isolated", fx.scale), firstSamples)
	logTickSamples(t, fmt.Sprintf("scale=%d phase=steady mode=isolated", fx.scale), steadySamples)

	// 并发响应：tick 进入事务后同时发起 cancel / pause / progress，记录排队延迟。
	concurrencyReps := reps
	if fx.scale >= 10000 && concurrencyReps > 3 {
		concurrencyReps = 3
	}
	baselines := fx.measureBaselines(t)
	concurrentFirstOps := make(map[string][]opSample)
	concurrentSteadyOps := make(map[string][]opSample)
	var concurrentFirstTicks, concurrentSteadyTicks []tickSample
	for rep := 0; rep < concurrencyReps; rep++ {
		fx.resetMeasuredTasks()
		firstTick, firstOps := fx.measureConcurrentTick(t, "first_block", 1)
		concurrentFirstTicks = append(concurrentFirstTicks, firstTick)
		appendOpSamples(concurrentFirstOps, firstOps)

		steadyTick, steadyOps := fx.measureConcurrentTick(t, "steady", 2)
		concurrentSteadyTicks = append(concurrentSteadyTicks, steadyTick)
		appendOpSamples(concurrentSteadyOps, steadyOps)
	}
	logTickSamples(t, fmt.Sprintf("scale=%d phase=first_block mode=concurrent", fx.scale), concurrentFirstTicks)
	logTickSamples(t, fmt.Sprintf("scale=%d phase=steady mode=concurrent", fx.scale), concurrentSteadyTicks)
	logOperationSamples(t, fx.scale, "first_block", baselines, concurrentFirstOps)
	logOperationSamples(t, fx.scale, "steady", baselines, concurrentSteadyOps)

	if fx.resetCount > 0 {
		t.Logf("fixture scale=%d maintenance: reset_count=%d reset_total_ms=%s (excluded from tick samples)",
			fx.scale, fx.resetCount, ms(fx.resetTotal))
	}
}

// ---- 夹具 ----

func newContentionFixture(t *testing.T, scale int) *contentionFixture {
	t.Helper()
	dir, err := os.MkdirTemp("", "yaomu-contention-")
	if err != nil {
		t.Fatalf("create contention temp dir: %v", err)
	}
	t.Cleanup(func() { removeTestTempDir(t, dir) })
	dbPath := filepath.Join(dir, "contention.db")

	// 用生产迁移链建库，再关闭并以带计时的 driver 重新打开同一文件；
	// 打开参数随后按生产 openDB 重放并断言。
	migrated, err := internaldb.Migrate(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("migrate temp db: %v", err)
	}
	if err := migrated.Close(); err != nil {
		t.Fatalf("close migrated db: %v", err)
	}

	db, err := openTracedSQLite(dbPath)
	if err != nil {
		t.Fatalf("open traced sqlite: %v", err)
	}
	// 后注册先执行：先关库，再按 Windows 兼容的有界重试删除临时目录。
	t.Cleanup(func() { _ = db.Close() })

	fx := &contentionFixture{
		t:      t,
		dir:    dir,
		db:     db,
		dbPath: dbPath,
		scale:  scale,
	}
	fx.verifyProductionSettings()
	fx.st = store.New(db)
	fx.bus = events.NewBus(fx.st, nil)
	fx.svc = service.New(service.Options{
		Store:       fx.st,
		Bus:         fx.bus,
		WorkspaceID: "ws-contention",
		InstanceID:  "instance-contention",
		ServerEpoch: 1,
		DataDir:     dir,
	})
	fx.sched = New(Options{
		Store:        fx.st,
		Bus:          fx.bus,
		Executor:     nil, // 无执行器：currentDispatchBlock 返回 runner_unavailable。
		Slots:        1,
		WorkspaceID:  "ws-contention",
		Epoch:        1,
		TickInterval: time.Hour, // 夹具不启动 loop，只直接调用 markBlockedTasks。
	})
	block, blocked := fx.sched.currentDispatchBlock()
	if !blocked {
		t.Fatal("fixture expected a blocked dispatcher")
	}
	fx.block = block

	seedStart := time.Now()
	fx.seed()
	t.Logf("fixture scale=%d seed: project=%s measured_run=%s tasks=%d seed_ms=%s",
		scale, fx.projectID, fx.measuredRunID, len(fx.measuredTaskID), ms(time.Since(seedStart)))

	// 用直接计数校验固定负载；不依赖候选查询实现，便于同一夹具对比优化前后。
	var seeded int
	if err := fx.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM tasks WHERE run_id = ? AND status = ?`,
		fx.measuredRunID, string(domain.TaskStatusQueued)).Scan(&seeded); err != nil {
		t.Fatalf("count seeded tasks: %v", err)
	}
	if seeded != scale {
		t.Fatalf("seeded candidates = %d, want %d", seeded, scale)
	}
	fx.logDataset()
	return fx
}

func openTracedSQLite(path string) (*sql.DB, error) {
	driver := &sqlite.Driver{}
	db := sql.OpenDB(&traceConnector{dsn: path, drv: driver})
	// 与 internal/db.openDB 一致：SQLite 单一 writer 连接。
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("apply %q: %w", stmt, err)
		}
	}
	return db, nil
}

func (fx *contentionFixture) verifyProductionSettings() {
	fx.t.Helper()
	var (
		journal string
		foreign int
		busy    int
		version string
	)
	ctx := context.Background()
	if err := fx.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		fx.t.Fatalf("read journal_mode: %v", err)
	}
	if err := fx.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreign); err != nil {
		fx.t.Fatalf("read foreign_keys: %v", err)
	}
	if err := fx.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
		fx.t.Fatalf("read busy_timeout: %v", err)
	}
	if err := fx.db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&version); err != nil {
		fx.t.Fatalf("read sqlite_version: %v", err)
	}
	if !strings.EqualFold(journal, "wal") {
		fx.t.Fatalf("journal_mode = %q, want wal (production openDB)", journal)
	}
	if foreign != 1 {
		fx.t.Fatalf("foreign_keys = %d, want 1 (production openDB)", foreign)
	}
	if busy != 5000 {
		fx.t.Fatalf("busy_timeout = %d, want 5000 (production openDB)", busy)
	}
	if got := fx.db.Stats().MaxOpenConnections; got != 1 {
		fx.t.Fatalf("MaxOpenConnections = %d, want 1 (production openDB)", got)
	}
	fx.sqliteV = version
}

func (fx *contentionFixture) logDataset() {
	fx.t.Helper()
	if info, err := os.Stat(fx.dbPath); err == nil {
		fx.dbBytes = info.Size()
	}
	if info, err := os.Stat(fx.dbPath + "-wal"); err == nil {
		fx.walBytes = info.Size()
	}
	fx.t.Logf("fixture scale=%d dataset: sqlite=%s db_bytes=%d wal_bytes=%d max_open_conns=%d",
		fx.scale, fx.sqliteV, fx.dbBytes, fx.walBytes, fx.db.Stats().MaxOpenConnections)
}

func (fx *contentionFixture) seed() {
	fx.t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	project := domain.Project{
		ID:            "project-contention",
		WorkspaceID:   "ws-contention",
		Name:          "sqlite contention fixture",
		SchemaVersion: 1,
		EditVersion:   1,
		CreatedAt:     base,
		UpdatedAt:     base,
	}
	if err := fx.st.CreateProject(ctx, project); err != nil {
		fx.t.Fatalf("seed project: %v", err)
	}
	fx.projectID = project.ID

	fx.measuredRunID = fx.seedRun("run-contention", base, domain.RunStatusRunning)
	fx.pauseRunID = fx.seedRun("run-pause", base, domain.RunStatusRunning)
	// 取消目标放在不可派发的 PAUSED Run：它不进入等待原因候选集，避免污染
	// markBlockedTasks 的固定负载；取消任务本身与 Run 状态无关。
	cancelRunID := fx.seedRun("run-cancel", base, domain.RunStatusPaused)
	progressRunID := fx.seedRun("run-progress", base, domain.RunStatusRunning)

	fx.cancelTaskID = "task-cancel-target"
	fx.progressTaskID = "task-progress-target"
	fx.progressStepID = "step-progress"
	fx.progressAttID = "attempt-progress-1"

	err := fx.st.DoTx(ctx, func(tx *store.Tx) error {
		for i := 0; i < fx.scale; i++ {
			created := base.Add(time.Duration(i) * time.Millisecond)
			task := domain.Task{
				ID:               domain.ID(fmt.Sprintf("task-measured-%06d", i)),
				ProjectID:        fx.projectID,
				RunID:            fx.measuredRunID,
				Kind:             domain.TaskKindNode,
				NodeKey:          domain.NodeKeyAnalyze,
				Language:         "zh",
				Status:           domain.TaskStatusQueued,
				Progress:         0,
				StateVersion:     1,
				InputRevisionIDs: []domain.ID{"revision-fixture"},
				InputDigest:      "sha256:contention-fixture",
				Params:           map[string]any{"language": "zh", "content_revision": 1},
				CreatedAt:        created,
				UpdatedAt:        created,
			}
			if err := tx.CreateTask(ctx, task); err != nil {
				return fmt.Errorf("seed measured task %d: %w", i, err)
			}
			fx.measuredTaskID = append(fx.measuredTaskID, task.ID)
		}

		cancelTask := domain.Task{
			ID:           fx.cancelTaskID,
			ProjectID:    fx.projectID,
			RunID:        cancelRunID,
			Kind:         domain.TaskKindNode,
			NodeKey:      domain.NodeKeyAnalyze,
			Status:       domain.TaskStatusQueued,
			StateVersion: 1,
			CreatedAt:    base,
			UpdatedAt:    base,
		}
		if err := tx.CreateTask(ctx, cancelTask); err != nil {
			return fmt.Errorf("seed cancel target: %w", err)
		}

		progressTask := domain.Task{
			ID:           fx.progressTaskID,
			ProjectID:    fx.projectID,
			RunID:        progressRunID,
			Kind:         domain.TaskKindNode,
			NodeKey:      domain.NodeKeyClone,
			Language:     "zh",
			Status:       domain.TaskStatusRunning,
			StateVersion: 1,
			CreatedAt:    base,
			UpdatedAt:    base,
		}
		if err := tx.CreateTask(ctx, progressTask); err != nil {
			return fmt.Errorf("seed progress target: %w", err)
		}

		started := base
		attempt := domain.Attempt{
			ID:        fx.progressAttID,
			TaskID:    fx.progressTaskID,
			StepID:    fx.progressStepID,
			AttemptNo: 1,
			Epoch:     1,
			Status:    domain.StepStatusRunning,
			StartedAt: &started,
		}
		if err := tx.CreateAttempt(ctx, attempt); err != nil {
			return fmt.Errorf("seed progress attempt: %w", err)
		}
		return nil
	})
	if err != nil {
		fx.t.Fatalf("seed tasks: %v", err)
	}
}

func (fx *contentionFixture) seedRun(id string, now time.Time, status domain.RunStatus) domain.ID {
	fx.t.Helper()
	nodes := []domain.PipelineNodeState{
		{Key: domain.NodeKeyImport, Status: domain.NodeStatusDone},
		{Key: domain.NodeKeyAnalyze, Status: domain.NodeStatusPending},
		{Key: domain.NodeKeySeparate, Status: domain.NodeStatusPending},
		{Key: domain.NodeKeyExtract, Status: domain.NodeStatusPending},
		{Key: domain.NodeKeyErase, Status: domain.NodeStatusPending},
		{Key: domain.NodeKeyTranslate, Status: domain.NodeStatusPending},
		{Key: domain.NodeKeyClone, Status: domain.NodeStatusPending},
		{Key: domain.NodeKeyMix, Status: domain.NodeStatusPending},
	}
	run := domain.WorkflowRun{
		ID:             domain.ID(id),
		ProjectID:      fx.projectID,
		Name:           "contention " + id,
		Template:       "full_pipeline",
		Status:         status,
		SourceLanguage: "en",
		Languages:      []string{"zh"},
		Nodes:          nodes,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := fx.st.CreateWorkflowRun(context.Background(), run); err != nil {
		fx.t.Fatalf("seed run %s: %v", id, err)
	}
	return run.ID
}

// ---- 单 tick 测量 ----

type tickSample struct {
	phase string

	wall           time.Duration
	firstSQLWait   time.Duration
	candidatePhase time.Duration
	txHold         time.Duration
	txSQL          time.Duration
	txGo           time.Duration
	commit         time.Duration
	sqlActive      time.Duration
	sqlBeforeTx    time.Duration
	sqlInTx        time.Duration
	postCommit     time.Duration
	poolWait       time.Duration
	poolWaits      int64
	cpu            time.Duration
	cpuOK          bool

	rows      int64
	rowsInTx  int64
	affected  int64
	stmts     int64
	busy      int
	committed bool

	cats map[string]*sqlCategoryStat
}

func (fx *contentionFixture) measureIsolatedTick(t *testing.T, phase string, reset bool) tickSample {
	t.Helper()
	if reset {
		fx.resetMeasuredTasks()
	}
	ctxBackground := context.Background()
	seqBefore, err := fx.st.LatestEventSeq(ctxBackground)
	if err != nil {
		t.Fatalf("latest event seq before %s: %v", phase, err)
	}
	sigBefore := fx.taskStateSignature()
	if phase == "first_block" {
		// 首次阻断前必须全部为 QUEUED 且无等待原因。
		var queued int
		if err := fx.db.QueryRowContext(ctxBackground,
			`SELECT COUNT(*) FROM tasks WHERE run_id = ? AND status = ? AND error_json IS NULL`,
			fx.measuredRunID, string(domain.TaskStatusQueued)).Scan(&queued); err != nil {
			t.Fatalf("count queued before first block: %v", err)
		}
		if queued != fx.scale {
			t.Fatalf("queued tasks before first block = %d, want %d", queued, fx.scale)
		}
	}

	trace := newSQLTrace()
	ctx := context.WithValue(ctxBackground, sqlTraceKey{}, trace)
	statsBefore := fx.db.Stats()
	cpuBefore, cpuOK := processCPU()
	start := time.Now()
	fx.sched.markBlockedTasks(ctx, fx.block)
	end := time.Now()
	cpuAfter, _ := processCPU()
	statsAfter := fx.db.Stats()

	sample := newTickSample(trace, start, end,
		cpuAfter-cpuBefore, cpuOK,
		statsAfter.WaitDuration-statsBefore.WaitDuration,
		statsAfter.WaitCount-statsBefore.WaitCount)
	sample.phase = phase

	switch phase {
	case "first_block":
		wantAffected := int64(2 * fx.scale) // 每条：UPDATE tasks + INSERT events
		if !sample.committed || sample.affected != wantAffected {
			t.Fatalf("first_block affected=%d committed=%t want=%d (busy=%d stmts=%d)",
				sample.affected, sample.committed, wantAffected, sample.busy, sample.stmts)
		}
		if got := categoryAffected(sample.cats, "task.update_status"); got != int64(fx.scale) {
			t.Fatalf("first_block task.update_status affected=%d, want %d", got, fx.scale)
		}
		if got := categoryAffected(sample.cats, "event.insert"); got != int64(fx.scale) {
			t.Fatalf("first_block event.insert affected=%d, want %d", got, fx.scale)
		}
		fx.assertAllWaiting(t)
	case "steady":
		if !sample.committed {
			t.Fatalf("steady tick did not commit (busy=%d stmts=%d)", sample.busy, sample.stmts)
		}
		if sample.affected != 0 {
			t.Fatalf("steady tick wrote %d rows, want 0", sample.affected)
		}
		if got := categoryCalls(sample.cats, "event.insert"); got != 0 {
			t.Fatalf("steady tick appended %d events, want 0", got)
		}
		if sigAfter := fx.taskStateSignature(); sigAfter != sigBefore {
			t.Fatalf("steady tick changed task state: before=%+v after=%+v", sigBefore, sigAfter)
		}
		seqAfter, err := fx.st.LatestEventSeq(ctxBackground)
		if err != nil {
			t.Fatalf("latest event seq after steady: %v", err)
		}
		if seqAfter != seqBefore {
			t.Fatalf("steady tick appended events: seq %d -> %d", seqBefore, seqAfter)
		}
	}
	return sample
}

func newTickSample(tr *sqlTrace, start, end time.Time, cpu time.Duration, cpuOK bool, poolWait time.Duration, poolWaits int64) tickSample {
	sample := tickSample{
		wall:        end.Sub(start),
		sqlActive:   tr.sqlTotal,
		sqlBeforeTx: tr.sqlBeforeTx,
		sqlInTx:     tr.sqlInTx,
		rows:        tr.rows,
		rowsInTx:    tr.rowsInTx,
		affected:    tr.affected,
		stmts:       tr.calls,
		busy:        tr.busyErrors,
		committed:   tr.sawCommit,
		poolWait:    poolWait,
		poolWaits:   poolWaits,
		cpu:         cpu,
		cpuOK:       cpuOK,
		cats:        tr.cats,
	}
	if !tr.firstSQL.IsZero() {
		sample.firstSQLWait = tr.firstSQL.Sub(start)
	}
	if !tr.beginAt.IsZero() {
		sample.candidatePhase = tr.beginAt.Sub(start)
	}
	if !tr.commitEnd.IsZero() {
		sample.txHold = tr.commitEnd.Sub(tr.beginAt)
		sample.commit = tr.commitDur
		sample.postCommit = end.Sub(tr.commitEnd)
		sample.txSQL = tr.beginDur + tr.sqlInTx + tr.commitDur
		if sample.txSQL > sample.txHold {
			sample.txGo = 0
		} else {
			sample.txGo = sample.txHold - sample.txSQL
		}
	}
	return sample
}

func (fx *contentionFixture) assertAllWaiting(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	var waiting int
	if err := fx.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE run_id = ? AND status = ? AND error_json IS NOT NULL`,
		fx.measuredRunID, string(domain.TaskStatusWaitingResource)).Scan(&waiting); err != nil {
		t.Fatalf("count waiting tasks: %v", err)
	}
	if waiting != fx.scale {
		t.Fatalf("waiting tasks = %d, want %d", waiting, fx.scale)
	}
	task, err := fx.st.GetTask(ctx, fx.measuredTaskID[0])
	if err != nil {
		t.Fatalf("read waiting task sample: %v", err)
	}
	if task.Error == nil || task.Error.MessageKey != "task.runner_unavailable" || !task.Error.Retryable {
		t.Fatalf("waiting task reason = %+v, want task.runner_unavailable retryable", task.Error)
	}
}

type taskState struct {
	count      int64
	versionSum int64
	updatedMax string
}

func (fx *contentionFixture) taskStateSignature() taskState {
	fx.t.Helper()
	var state taskState
	if err := fx.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*), COALESCE(SUM(state_version), 0), COALESCE(MAX(updated_at), '')
		 FROM tasks WHERE run_id = ?`, fx.measuredRunID).Scan(&state.count, &state.versionSum, &state.updatedMax); err != nil {
		fx.t.Fatalf("task state signature: %v", err)
	}
	return state
}

// ---- 并发响应测量 ----

type opSample struct {
	kind       string
	wall       time.Duration
	preDB      time.Duration
	sqlActive  time.Duration
	statements int64
	err        error
}

func (fx *contentionFixture) measureConcurrentTick(t *testing.T, phase string, progress int) (tickSample, []opSample) {
	t.Helper()
	if phase == "first_block" {
		fx.resetMeasuredTasks()
	}
	fx.resetInteractive()

	trace := newSQLTrace()
	ctx := context.WithValue(context.Background(), sqlTraceKey{}, trace)
	statsBefore := fx.db.Stats()
	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fx.sched.markBlockedTasks(ctx, fx.block)
	}()
	select {
	case <-trace.beginCh:
	case <-time.After(30 * time.Second):
		t.Fatalf("phase=%s tick never began a transaction", phase)
	}

	operations := []struct {
		kind string
		run  func(context.Context) error
	}{
		{"cancel", func(ctx context.Context) error {
			_, err := fx.svc.CancelTask(ctx, service.Idem{}, string(fx.cancelTaskID))
			return err
		}},
		{"pause", func(ctx context.Context) error {
			_, err := fx.svc.PauseWorkflowRun(ctx, service.Idem{}, string(fx.pauseRunID))
			return err
		}},
		{"progress", func(ctx context.Context) error {
			result, err := fx.st.UpdateTaskProgress(ctx, store.ProgressUpdate{
				TaskID:    fx.progressTaskID,
				AttemptID: fx.progressAttID,
				StepID:    fx.progressStepID,
				AttemptNo: 1,
				Epoch:     1,
				Progress:  progress,
				Now:       time.Now().UTC(),
			})
			if err != nil {
				return err
			}
			if !result.Applied {
				return fmt.Errorf("progress update was not applied")
			}
			return nil
		}},
	}

	results := make(chan opSample, len(operations))
	var wg sync.WaitGroup
	for _, operation := range operations {
		operation := operation
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- fx.runOperation(operation.kind, operation.run)
		}()
	}
	<-done
	end := time.Now() // tick 墙钟截止于 tick 自身返回，不含排队请求的收尾。
	wg.Wait()
	close(results)
	statsAfter := fx.db.Stats()

	sample := newTickSample(trace, start, end, 0, false,
		statsAfter.WaitDuration-statsBefore.WaitDuration,
		statsAfter.WaitCount-statsBefore.WaitCount)
	sample.phase = phase

	var ops []opSample
	for result := range results {
		ops = append(ops, result)
	}
	for _, result := range ops {
		if result.err != nil {
			t.Errorf("phase=%s concurrent %s failed: %v", phase, result.kind, result.err)
		}
	}
	switch phase {
	case "first_block":
		if !sample.committed || sample.affected != int64(2*fx.scale) {
			t.Fatalf("concurrent first_block affected=%d committed=%t want=%d",
				sample.affected, sample.committed, 2*fx.scale)
		}
		fx.assertAllWaiting(t)
	case "steady":
		if sample.affected != 0 {
			t.Fatalf("concurrent steady tick wrote %d rows, want 0", sample.affected)
		}
	}
	return sample, ops
}

func (fx *contentionFixture) runOperation(kind string, fn func(context.Context) error) opSample {
	trace := newSQLTrace()
	ctx := context.WithValue(context.Background(), sqlTraceKey{}, trace)
	start := time.Now()
	err := fn(ctx)
	end := time.Now()
	sample := opSample{
		kind:       kind,
		wall:       end.Sub(start),
		sqlActive:  trace.sqlTotal,
		statements: trace.calls,
		err:        err,
	}
	if !trace.firstSQL.IsZero() {
		sample.preDB = trace.firstSQL.Sub(start)
	}
	return sample
}

func (fx *contentionFixture) measureBaselines(t *testing.T) map[string]opSample {
	t.Helper()
	baselines := make(map[string]opSample)
	fx.resetInteractive()
	baselines["cancel"] = fx.runOperation("cancel", func(ctx context.Context) error {
		_, err := fx.svc.CancelTask(ctx, service.Idem{}, string(fx.cancelTaskID))
		return err
	})
	fx.resetInteractive()
	baselines["pause"] = fx.runOperation("pause", func(ctx context.Context) error {
		_, err := fx.svc.PauseWorkflowRun(ctx, service.Idem{}, string(fx.pauseRunID))
		return err
	})
	fx.resetInteractive()
	baselines["progress"] = fx.runOperation("progress", func(ctx context.Context) error {
		result, err := fx.st.UpdateTaskProgress(ctx, store.ProgressUpdate{
			TaskID:    fx.progressTaskID,
			AttemptID: fx.progressAttID,
			StepID:    fx.progressStepID,
			AttemptNo: 1,
			Epoch:     1,
			Progress:  1,
			Now:       time.Now().UTC(),
		})
		if err != nil {
			return err
		}
		if !result.Applied {
			return fmt.Errorf("baseline progress update was not applied")
		}
		return nil
	})
	for kind, sample := range baselines {
		if sample.err != nil {
			t.Fatalf("baseline %s failed: %v", kind, sample.err)
		}
	}
	return baselines
}

func appendOpSamples(dst map[string][]opSample, samples []opSample) {
	for _, sample := range samples {
		dst[sample.kind] = append(dst[sample.kind], sample)
	}
}

// ---- 夹具状态复位（不计入测量） ----

func (fx *contentionFixture) resetMeasuredTasks() {
	fx.t.Helper()
	start := time.Now()
	_, err := fx.db.ExecContext(context.Background(),
		`UPDATE tasks SET status = ?, error_json = NULL WHERE run_id = ?`,
		string(domain.TaskStatusQueued), fx.measuredRunID)
	if err != nil {
		fx.t.Fatalf("reset measured tasks: %v", err)
	}
	fx.resetTotal += time.Since(start)
	fx.resetCount++
}

func (fx *contentionFixture) resetInteractive() {
	fx.t.Helper()
	ctx := context.Background()
	if _, err := fx.db.ExecContext(ctx,
		`UPDATE workflow_runs SET status = ? WHERE id = ?`,
		string(domain.RunStatusRunning), fx.pauseRunID); err != nil {
		fx.t.Fatalf("reset pause run: %v", err)
	}
	if _, err := fx.db.ExecContext(ctx,
		`UPDATE tasks SET status = ?, error_json = NULL WHERE id = ?`,
		string(domain.TaskStatusQueued), fx.cancelTaskID); err != nil {
		fx.t.Fatalf("reset cancel task: %v", err)
	}
	if _, err := fx.db.ExecContext(ctx,
		`UPDATE tasks SET progress = 0 WHERE id = ?`, fx.progressTaskID); err != nil {
		fx.t.Fatalf("reset progress task: %v", err)
	}
}

// ---- 报告 ----

var contentionCategoryOrder = []string{
	"task.waiting_candidates",
	"task.get",
	"run.get",
	"run.nodes",
	"task.select_status_version",
	"task.update_status",
	"event.insert",
	"task.select_progress_version",
	"task.update_progress",
	"attempt.select",
	"attempt.update",
	"receipt",
	"tx.begin",
	"tx.commit",
}

func logTickSamples(t *testing.T, label string, samples []tickSample) {
	t.Helper()
	if len(samples) == 0 {
		return
	}
	collect := func(get func(tickSample) time.Duration) []time.Duration {
		out := make([]time.Duration, 0, len(samples))
		for _, sample := range samples {
			out = append(out, get(sample))
		}
		return out
	}
	logSummary := func(name string, values []time.Duration) {
		summary := summarizeDurations(values)
		t.Logf("%s %s_ms p50=%s p95=%s max=%s", label, name, ms(summary.p50), ms(summary.p95), ms(summary.max))
	}

	t.Logf("%s samples=%d busy_errors_total=%d", label, len(samples), totalBusy(samples))
	logSummary("tick_wall", collect(func(s tickSample) time.Duration { return s.wall }))
	logSummary("before_first_sql", collect(func(s tickSample) time.Duration { return s.firstSQLWait }))
	logSummary("candidate_phase", collect(func(s tickSample) time.Duration { return s.candidatePhase }))
	logSummary("tx_hold", collect(func(s tickSample) time.Duration { return s.txHold }))
	logSummary("tx_sql", collect(func(s tickSample) time.Duration { return s.txSQL }))
	logSummary("tx_go", collect(func(s tickSample) time.Duration { return s.txGo }))
	logSummary("commit", collect(func(s tickSample) time.Duration { return s.commit }))
	logSummary("sql_active", collect(func(s tickSample) time.Duration { return s.sqlActive }))
	logSummary("sql_before_tx", collect(func(s tickSample) time.Duration { return s.sqlBeforeTx }))
	logSummary("sql_in_tx", collect(func(s tickSample) time.Duration { return s.sqlInTx }))
	logSummary("post_commit", collect(func(s tickSample) time.Duration { return s.postCommit }))
	logSummary("pool_wait", collect(func(s tickSample) time.Duration { return s.poolWait }))
	logSummary("cpu", collect(func(s tickSample) time.Duration { return s.cpu }))

	var rows, rowsInTx, writes, stmts []int64
	for _, sample := range samples {
		rows = append(rows, sample.rows)
		rowsInTx = append(rowsInTx, sample.rowsInTx)
		writes = append(writes, sample.affected)
		stmts = append(stmts, sample.stmts)
	}
	rowsP50, rowsMax := summarizeInt64(rows)
	rowsTxP50, _ := summarizeInt64(rowsInTx)
	writesP50, writesMax := summarizeInt64(writes)
	stmtsP50, stmtsMax := summarizeInt64(stmts)
	t.Logf("%s rows_read p50=%d max=%d rows_read_in_tx p50=%d", label, rowsP50, rowsMax, rowsTxP50)
	t.Logf("%s rows_written p50=%d max=%d statements p50=%d max=%d", label, writesP50, writesMax, stmtsP50, stmtsMax)
	if len(samples) > 0 {
		aggregateCategories(t, label, samples)
	}
}

func totalBusy(samples []tickSample) int {
	total := 0
	for _, sample := range samples {
		total += sample.busy
	}
	return total
}

func aggregateCategories(t *testing.T, label string, samples []tickSample) {
	t.Helper()
	aggregate := make(map[string]*sqlCategoryStat)
	for _, sample := range samples {
		for name, stat := range sample.cats {
			dst := aggregate[name]
			if dst == nil {
				dst = &sqlCategoryStat{}
				aggregate[name] = dst
			}
			dst.calls += stat.calls
			dst.rows += stat.rows
			dst.affected += stat.affected
			dst.query += stat.query
			dst.scan += stat.scan
			dst.exec += stat.exec
		}
	}
	seen := make(map[string]bool)
	names := make([]string, 0, len(aggregate))
	for _, name := range contentionCategoryOrder {
		if stat := aggregate[name]; stat != nil && stat.calls+stat.rows > 0 {
			names = append(names, name)
			seen[name] = true
		}
	}
	for name := range aggregate {
		if !seen[name] && (aggregate[name].calls+aggregate[name].rows) > 0 {
			names = append(names, name)
		}
	}
	for _, name := range names {
		stat := aggregate[name]
		t.Logf("%s cat=%s calls=%d rows=%d affected=%d sql_ms=%s scan_ms=%s",
			label, name, stat.calls, stat.rows, stat.affected,
			ms(stat.query+stat.exec), ms(stat.scan))
	}
}

func logOperationSamples(t *testing.T, scale int, phase string, baselines map[string]opSample, samples map[string][]opSample) {
	t.Helper()
	for _, kind := range []string{"cancel", "pause", "progress"} {
		ops := samples[kind]
		if len(ops) == 0 {
			continue
		}
		wall := make([]time.Duration, 0, len(ops))
		preDB := make([]time.Duration, 0, len(ops))
		sqlActive := make([]time.Duration, 0, len(ops))
		for _, op := range ops {
			wall = append(wall, op.wall)
			preDB = append(preDB, op.preDB)
			sqlActive = append(sqlActive, op.sqlActive)
		}
		wallSummary := summarizeDurations(wall)
		preSummary := summarizeDurations(preDB)
		sqlSummary := summarizeDurations(sqlActive)
		t.Logf("scale=%d phase=%s op=%s baseline_wall_ms=%s under_tick samples=%d wall_p50=%s wall_p95=%s wall_max=%s pre_db_p50=%s pre_db_p95=%s pre_db_max=%s sql_p50=%s",
			scale, phase, kind, ms(baselines[kind].wall), len(ops),
			ms(wallSummary.p50), ms(wallSummary.p95), ms(wallSummary.max),
			ms(preSummary.p50), ms(preSummary.p95), ms(preSummary.max), ms(sqlSummary.p50))
	}
}

// ---- 环境变量 ----

func envPositiveInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	var value int
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil || value <= 0 {
		return fallback
	}
	return value
}

func categoryCalls(cats map[string]*sqlCategoryStat, name string) int64 {
	if stat := cats[name]; stat != nil {
		return stat.calls
	}
	return 0
}

func categoryAffected(cats map[string]*sqlCategoryStat, name string) int64 {
	if stat := cats[name]; stat != nil {
		return stat.affected
	}
	return 0
}
