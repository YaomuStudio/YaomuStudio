// Package updateguard 是应用更新前的 Core 控制面准备。
//
// 边界（contracts/app-update-installation.md、openspec/changes/app-update-drain-guard）：
//   - check 只读：观察空闲/阻断，不产生许可、不关闭任何资源；
//   - prepare 先封住普通请求与业务写，再原子复核显式活跃集与内存执行拥有者，
//     只有完全空闲才停止空调度器并关闭空闲 Runner 资源；
//   - 不暂停/取消/修改任何业务队列；等待期间新工作自然延长等待；
//   - 清理开始后失败保持封闸，不自动解封、不伪造 prepared；
//   - 状态只在实例内存中，不持久化、不进入业务状态机、不构成第二队列。
package updateguard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// 请求 mode（wire 唯一口径见 contracts/openapi.yaml UpdatePreparationRequest）。
const (
	ModeCheck   = "check"
	ModePrepare = "prepare"
)

// 结果 state（wire 口径见 UpdatePreparationResult）。
const (
	StateIdle     = "idle"
	StateBlocked  = "blocked"
	StatePrepared = "prepared"
)

// 有限阻断原因；不携带路径、凭据或业务正文。
const (
	ReasonActiveWork          = "active_work"
	ReasonRequestInFlight     = "request_in_flight"
	ReasonExecutorStarting    = "executor_starting"
	ReasonExecutorUnconfirmed = "executor_unconfirmed"
	ReasonPreparing           = "preparing"
	ReasonRestartRequired     = "restart_required"
)

const (
	// defaultBudget 是控制面等待预算（契约：Core 15s，Main prepare 20s）。
	defaultBudget = 15 * time.Second
	// maxIDLength 与路径 ID/请求 ID 上限一致。
	maxIDLength = 200
	// maxSafeInteger 是 JSON 安全整数上限（2^53-1）。
	maxSafeInteger = int64(1<<53 - 1)
)

// Request 是 UpdatePreparationRequest 的精确形状。
type Request struct {
	Mode        string `json:"mode"`
	OperationID string `json:"operation_id"`
	WorkspaceID string `json:"workspace_id"`
	InstanceID  string `json:"instance_id"`
	ServerEpoch int64  `json:"server_epoch"`
}

// Result 是 UpdatePreparationResult 的状态部分；Reason 为空表示 wire null。
type Result struct {
	State  string
	Reason string
}

// Ownership 是准备守卫需要的执行/安装拥有者视图与清理入口。
//
// 实现不得持有业务队列，也不得在检查中修改业务状态。
type Ownership interface {
	// InMemoryActive 报告数据库无法表达的进程内拥有者：调度器执行/预留、
	// 安装 worker 尾部。它必须只读。
	InMemoryActive() bool
	// ExecutorBlock 报告保留的执行拥有者不确定原因；空串表示无阻断。
	ExecutorBlock() string
	// Settle 在封闸成功后停止空调度器并关闭空闲执行资源；必须独立于客户端
	// 请求上下文，返回任何未确认的清理错误。
	Settle(ctx context.Context) error
}

// Options 是 Guard 构造参数。
type Options struct {
	Store     *store.Store
	Ownership Ownership
	// WorkspaceID/InstanceID/ServerEpoch 是本次认证实例身份；请求必须精确匹配。
	WorkspaceID string
	InstanceID  string
	ServerEpoch int64
	// Budget 是控制面等待预算（默认 15s）。
	Budget time.Duration
	// CleanupBudget 是后台清理预算（默认等于 Budget）；测试可注入短钟。
	CleanupBudget time.Duration
}

// 校验错误：坏形状 -> 400 INVALID_REQUEST；形状正确但身份不符 -> 409 REVISION_CONFLICT。
var (
	ErrInvalidRequest   = fmt.Errorf("%w: invalid update preparation request", domain.ErrInvalidRequest)
	ErrIdentityMismatch = fmt.Errorf("%w: update preparation identity does not match this Core instance", domain.ErrRevisionConflict)
)

type guardPhase int

const (
	phaseIdle guardPhase = iota
	phasePreparing
	phaseSealed
)

// cleanupRun 是一次已开始的不可逆清理；完成后 error 与 prepared 事实在
// close(done) 之前写入，后续调用共享同一完成与错误。
type cleanupRun struct {
	done chan struct{}
	err  error
}

// Guard 是实例内更新准备控制器。
type Guard struct {
	store     *store.Store
	ownership Ownership
	workspace string
	instance  string
	epoch     int64
	budget    time.Duration
	cleanup   time.Duration

	mu              sync.Mutex
	phase           guardPhase
	activeOperation string
	run             *cleanupRun
	prepared        bool
}

// New 构造控制器；没有 Schema、队列或持久化状态。
func New(opts Options) *Guard {
	if opts.Budget <= 0 {
		opts.Budget = defaultBudget
	}
	if opts.CleanupBudget <= 0 {
		opts.CleanupBudget = opts.Budget
	}
	return &Guard{
		store:     opts.Store,
		ownership: opts.Ownership,
		workspace: opts.WorkspaceID,
		instance:  opts.InstanceID,
		epoch:     opts.ServerEpoch,
		budget:    opts.Budget,
		cleanup:   opts.CleanupBudget,
		phase:     phaseIdle,
	}
}

// Handle 校验并执行一次 check/prepare。
func (g *Guard) Handle(ctx context.Context, req Request) (Result, error) {
	if err := g.validate(req); err != nil {
		return Result{}, err
	}
	if req.Mode == ModeCheck {
		return g.check(ctx)
	}
	return g.prepare(ctx, req.OperationID)
}

func (g *Guard) validate(req Request) error {
	if req.Mode != ModeCheck && req.Mode != ModePrepare {
		return fmt.Errorf("%w: mode must be check or prepare", ErrInvalidRequest)
	}
	if !validIdentityString(req.OperationID) || !validIdentityString(req.WorkspaceID) ||
		!validIdentityString(req.InstanceID) {
		return fmt.Errorf("%w: operation_id, workspace_id and instance_id must be nonempty strings of at most %d characters",
			ErrInvalidRequest, maxIDLength)
	}
	if req.ServerEpoch <= 0 || req.ServerEpoch > maxSafeInteger {
		return fmt.Errorf("%w: server_epoch must be a positive safe integer", ErrInvalidRequest)
	}
	if req.WorkspaceID != g.workspace || req.InstanceID != g.instance || req.ServerEpoch != g.epoch {
		return ErrIdentityMismatch
	}
	return nil
}

func validIdentityString(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maxIDLength
}

// check 只读观察：不取得许可、不关闭资源；已封印时返回有限阻断原因。
func (g *Guard) check(ctx context.Context) (Result, error) {
	g.mu.Lock()
	phase := g.phase
	g.mu.Unlock()
	switch phase {
	case phasePreparing:
		return blocked(ReasonPreparing), nil
	case phaseSealed:
		return blocked(ReasonRestartRequired), nil
	}
	if g.store.Sealed() {
		// 防御性：任何已封闸实例都不能再报告 idle。
		return blocked(ReasonRestartRequired), nil
	}
	var reason string
	err := g.store.ReadTx(ctx, func(tx *store.Tx) error {
		current, err := g.activeReason(ctx, tx)
		if err != nil {
			return err
		}
		reason = current
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	if reason != "" {
		return blocked(reason), nil
	}
	return Result{State: StateIdle}, nil
}

// activeReason 在同一事务内读取显式活跃集，并复核内存执行/安装拥有者。
func (g *Guard) activeReason(ctx context.Context, tx *store.Tx) (string, error) {
	work, err := tx.ActiveWork(ctx)
	if err != nil {
		return "", err
	}
	if !work.Empty() {
		return ReasonActiveWork, nil
	}
	if g.ownership != nil && g.ownership.InMemoryActive() {
		return ReasonActiveWork, nil
	}
	if g.ownership != nil {
		if reason := g.ownership.ExecutorBlock(); reason != "" {
			return reason, nil
		}
	}
	return "", nil
}

// prepare 原子复核并封闸。
func (g *Guard) prepare(ctx context.Context, operationID string) (Result, error) {
	g.mu.Lock()
	switch g.phase {
	case phasePreparing:
		g.mu.Unlock()
		return blocked(ReasonPreparing), nil
	case phaseSealed:
		g.mu.Unlock()
		return g.joinSealed(ctx, operationID)
	}
	g.phase = phasePreparing
	g.mu.Unlock()

	// 整个 prepare（封闸复核 + 清理确认）共用同一控制面预算。
	deadline := time.Now().Add(g.budget)
	var reason string
	sealCtx, cancelSeal := context.WithDeadline(ctx, deadline)
	sealed, err := g.store.SealIfIdle(sealCtx, func(tx *store.Tx) (bool, error) {
		current, err := g.activeReason(sealCtx, tx)
		if err != nil {
			return false, err
		}
		if current != "" {
			reason = current
			return false, nil
		}
		return true, nil
	})
	cancelSeal()
	if err != nil {
		g.reopen()
		switch {
		case errors.Is(err, store.ErrAdmissionBusy):
			// 普通 handler/事务仍在飞：快速返回，不打断、不等待。
			return blocked(ReasonRequestInFlight), nil
		case errors.Is(err, store.ErrSealed):
			return blocked(ReasonRestartRequired), nil
		default:
			return Result{}, err
		}
	}
	if !sealed {
		g.reopen()
		if reason == "" {
			reason = ReasonActiveWork
		}
		return blocked(reason), nil
	}
	g.mu.Lock()
	g.phase = phaseSealed
	g.activeOperation = operationID
	run := g.startCleanupLocked()
	g.mu.Unlock()
	return g.awaitCleanup(ctx, run, deadline)
}

// reopen 只在封闸尚未成功时恢复 idle；封闸成功后永不自动解封。
func (g *Guard) reopen() {
	g.mu.Lock()
	if g.phase == phasePreparing {
		g.phase = phaseIdle
	}
	g.mu.Unlock()
}

// startCleanupLocked 启动独立于客户端请求的清理；调用方必须持有 g.mu。
func (g *Guard) startCleanupLocked() *cleanupRun {
	run := &cleanupRun{done: make(chan struct{})}
	g.run = run
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), g.cleanup)
		defer cancel()
		err := g.ownership.Settle(ctx)
		g.mu.Lock()
		run.err = err
		if err == nil {
			g.prepared = true
		}
		g.mu.Unlock()
		close(run.done)
	}()
	return run
}

// joinSealed 处理已封闸实例上的重复 prepare：
// 同一操作可读同一内存证明；其他操作一律 restart_required。
func (g *Guard) joinSealed(ctx context.Context, operationID string) (Result, error) {
	g.mu.Lock()
	operation := g.activeOperation
	prepared := g.prepared
	run := g.run
	g.mu.Unlock()
	if operationID != operation {
		return blocked(ReasonRestartRequired), nil
	}
	if prepared {
		return Result{State: StatePrepared}, nil
	}
	return g.awaitCleanup(ctx, run, time.Now().Add(g.budget))
}

// awaitCleanup 有界等待清理完成；超时/断流不否认清理所有权，也不伪造成功。
func (g *Guard) awaitCleanup(ctx context.Context, run *cleanupRun, deadline time.Time) (Result, error) {
	if run == nil {
		return blocked(ReasonRestartRequired), nil
	}
	waitCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	select {
	case <-run.done:
		// 已完成的清理优先于同刻到达的超时，避免把成功误报为未确认。
	default:
		select {
		case <-run.done:
		case <-waitCtx.Done():
			return blocked(ReasonExecutorUnconfirmed), nil
		}
	}
	g.mu.Lock()
	prepared := g.prepared
	g.mu.Unlock()
	if prepared {
		return Result{State: StatePrepared}, nil
	}
	// 清理已确认失败：实例保持封闸，等待显式重启。
	return blocked(ReasonRestartRequired), nil
}

// BeginRequest 为普通（非 health/system/SSE/preparation）HTTP handler 取得
// 全程准入；封闸或独占准备阶段返回 false。
func (g *Guard) BeginRequest() bool {
	if g == nil || g.store == nil {
		return true
	}
	return g.store.Gate().AcquireShared() == nil
}

// EndRequest 释放普通 handler 准入。
func (g *Guard) EndRequest() {
	if g == nil || g.store == nil {
		return
	}
	g.store.Gate().ReleaseShared()
}

// Sealed 报告本实例是否已封闸（Main 侧防御性读取）。
func (g *Guard) Sealed() bool {
	if g == nil || g.store == nil {
		return false
	}
	return g.store.Sealed()
}

func blocked(reason string) Result {
	return Result{State: StateBlocked, Reason: reason}
}
