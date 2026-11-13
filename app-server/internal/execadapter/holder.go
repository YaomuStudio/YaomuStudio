package execadapter

import (
	"context"
	"errors"
	"sync"
)

// ExecutorReadiness 是执行资源的内部就绪状态。
//
// 它只描述本 Core 实例的执行器是否可派发，不是业务状态、不持久化、不进入
// 公开 API；持有者不可用时由任务既有 WAITING_RESOURCE 等待原因表达。
type ExecutorReadiness int

const (
	// ExecutorReadinessStarting 表示 Runner 初始化尚未结束。调度器应把任务
	// 标记为可恢复的启动中等待，而不是永久隔离或失败。
	ExecutorReadinessStarting ExecutorReadiness = iota
	// ExecutorReadinessReady 表示完整不可变执行器已发布，可正常派发。
	ExecutorReadinessReady
	// ExecutorReadinessUnavailable 表示本次启动没有得到可用执行器，且不会在
	// 同一执行流上自动重试成健康。
	ExecutorReadinessUnavailable
	// ExecutorReadinessClosed 表示 Core 正在退出：拒绝新派发与迟到发布。
	ExecutorReadinessClosed
)

func (r ExecutorReadiness) String() string {
	switch r {
	case ExecutorReadinessStarting:
		return "starting"
	case ExecutorReadinessReady:
		return "ready"
	case ExecutorReadinessUnavailable:
		return "unavailable"
	case ExecutorReadinessClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// RoutingHolder 是调度器持有的稳定执行器句柄。
//
// 构造时处于 starting；异步初始化完成后一次性发布完整不可变
// RoutingExecutor，此后不再替换其中任何 Runner 指针。持有者不排队、不保存
// 业务状态，只提供执行资源就绪门禁与单一清理所有者。
type RoutingHolder struct {
	mu        sync.Mutex
	executor  *RoutingExecutor
	cleanup   func() error
	readiness ExecutorReadiness
	// bootstrapErr 保留初始化/租约获取与回收的不确定项；它一旦出现即锁存，
	// 不能被后续 unavailable/closed 读数洗成“干净关闭”。
	bootstrapErr error
	// closeDone 在首次 Close 清理完成时关闭；并发 Close 共享同一完成与错误。
	closeDone chan struct{}
	closeErr  error
}

// NewRoutingHolder 返回处于 starting 状态的新持有者。
func NewRoutingHolder() *RoutingHolder {
	return &RoutingHolder{readiness: ExecutorReadinessStarting}
}

// Readiness 实现调度器的可选就绪接口。
func (h *RoutingHolder) Readiness() ExecutorReadiness {
	if h == nil {
		return ExecutorReadinessUnavailable
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.readiness
}

// Publish 一次性发布完整执行器并接管其清理函数。
//
// 只有 starting 状态接受发布；已关闭或已发布时返回 false，调用方（初始化
// 拥有者）必须自行回收传入的客户端与租约，避免迟到发布泄漏资源。发布成功
// 后不再原地修改 Host/Model 指针。
func (h *RoutingHolder) Publish(executor *RoutingExecutor, cleanup func() error) bool {
	if h == nil || executor == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.readiness != ExecutorReadinessStarting {
		return false
	}
	h.executor = executor
	h.cleanup = cleanup
	h.readiness = ExecutorReadinessReady
	return true
}

// MarkUnavailable 在本次初始化没有可用执行器时收敛为 unavailable。
// 已就绪或已关闭时不改变状态，避免失败初始化或迟到结果覆盖健康/关闭事实。
func (h *RoutingHolder) MarkUnavailable() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.readiness == ExecutorReadinessStarting {
		h.readiness = ExecutorReadinessUnavailable
	}
}

// RecordBootstrapError 锁存一次初始化/租约清理的未确认错误。
//
// 记录只增不减：bootstrap 失败、未知租约拥有者或回收错误都让“干净关闭”
// 不可声称，直到显式重启。
func (h *RoutingHolder) RecordBootstrapError(err error) {
	if h == nil || err == nil {
		return
	}
	h.mu.Lock()
	h.bootstrapErr = errors.Join(h.bootstrapErr, err)
	h.mu.Unlock()
}

// BootstrapErr 返回锁存的初始化/租约清理错误；nil 表示没有已知不确定项。
func (h *RoutingHolder) BootstrapErr() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.bootstrapErr
}

// Err 返回本持有者的最终清理错误与锁存初始化错误的合并结果。
func (h *RoutingHolder) Err() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return errors.Join(h.bootstrapErr, h.closeErr)
}

// Close 进入 closed 并拒绝迟到发布；已发布执行器的清理只执行一次。
//
// 清理在锁外执行，避免关闭/IO 阻塞 Readiness 与派发门禁读取。并发或重复
// Close 等待同一完成并返回同一结果：首次清理未完成/失败时，后续调用不得
// 伪报成功。
func (h *RoutingHolder) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	if h.closeDone != nil {
		done := h.closeDone
		h.mu.Unlock()
		<-done
		return h.Err()
	}
	h.readiness = ExecutorReadinessClosed
	cleanup := h.cleanup
	h.executor = nil
	h.cleanup = nil
	done := make(chan struct{})
	h.closeDone = done
	h.mu.Unlock()

	var closeErr error
	if cleanup != nil {
		closeErr = cleanup()
	}
	h.mu.Lock()
	h.closeErr = closeErr
	h.mu.Unlock()
	close(done)
	return h.Err()
}

// Execute 实现调度器的执行接口；未就绪时 fail-closed，不把执行转交给
// 尚未发布的 Runner。正常派发路径由调度器先检查 Readiness，这里是防御性检查。
func (h *RoutingHolder) Execute(ctx context.Context, req Request, progress ProgressFunc, logf LogFunc) Result {
	if h == nil {
		return Result{TransportErr: context.Canceled}
	}
	h.mu.Lock()
	executor := h.executor
	readiness := h.readiness
	h.mu.Unlock()
	if executor == nil || readiness != ExecutorReadinessReady {
		return Result{Error: &ExecError{
			Code:      "CAPABILITY_UNVERIFIED",
			Message:   "execution runner is not ready",
			Retryable: true,
		}}
	}
	return executor.Execute(ctx, req, progress, logf)
}

// Capacity 恒为 1：持有者不排队，也不掩盖底层 RoutingExecutor 的单槽约束。
func (*RoutingHolder) Capacity() int { return 1 }

// IsolationErr 透传已发布执行器的永久隔离；启动中/不可用不是永久隔离。
func (h *RoutingHolder) IsolationErr() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	executor := h.executor
	h.mu.Unlock()
	if executor == nil {
		return nil
	}
	return executor.IsolationErr()
}

// Identity 仅用于诊断；未发布时为空。
func (h *RoutingHolder) Identity() string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	executor := h.executor
	h.mu.Unlock()
	if executor == nil {
		return ""
	}
	return executor.Identity()
}
