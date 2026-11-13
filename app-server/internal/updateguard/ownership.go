package updateguard

// CoreOwnership 把调度器/执行持有者/安装 worker 的拥有者事实接到更新准备
// 守卫。它只读检查、只在封闸成功后清理，不持有业务队列。

import (
	"context"
	"errors"
	"fmt"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/scheduler"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

// CoreOwnership 是生产组装；字段可部分为 nil（测试夹具）。
type CoreOwnership struct {
	Scheduler *scheduler.Scheduler
	Holder    *execadapter.RoutingHolder
	Service   *service.Service
	// BootstrapDone 在异步 Runner 初始化收敛后关闭。
	BootstrapDone <-chan struct{}
}

// InMemoryActive 报告调度器执行/预留与安装 worker 尾部。
func (o *CoreOwnership) InMemoryActive() bool {
	if o == nil {
		return false
	}
	if o.Scheduler != nil && !o.Scheduler.Idle() {
		return true
	}
	if o.Service != nil && o.Service.ActiveInstallationWork() {
		return true
	}
	return false
}

// ExecutorBlock 报告保留的执行拥有者不确定：bootstrap 未收敛、bootstrap/
// 租约清理错误、永久隔离或已关闭的持有者都不能当作干净关闭。
func (o *CoreOwnership) ExecutorBlock() string {
	if o == nil {
		return ""
	}
	if o.Holder != nil {
		if o.Holder.BootstrapErr() != nil {
			return ReasonExecutorUnconfirmed
		}
		if o.Holder.IsolationErr() != nil {
			return ReasonExecutorUnconfirmed
		}
		switch o.Holder.Readiness() {
		case execadapter.ExecutorReadinessStarting:
			// 慢 hello 等异步初始化尚未收敛：等待，不关闭半个执行器。
			return ReasonExecutorStarting
		case execadapter.ExecutorReadinessClosed:
			return ReasonExecutorUnconfirmed
		}
	}
	if o.Scheduler != nil && o.Scheduler.Isolated() {
		return ReasonExecutorUnconfirmed
	}
	return ""
}

// Settle 停止空调度器、关闭空闲执行资源并等待 bootstrap 收敛。
//
// 预算由调用方控制；任何未确认项都返回错误（不伪成功）。holder.Close 与
// scheduler.Stop 都是共享一次完成，主进程既有 shutdown 复用同一所有者，
// 不重做清理。
func (o *CoreOwnership) Settle(ctx context.Context) error {
	if o == nil {
		return nil
	}
	var errs []error
	if o.Scheduler != nil {
		if err := o.Scheduler.Stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if o.Holder != nil {
		// 先 join 一次性关闭；错误在等待 bootstrap 后与保留错误一并读取。
		_ = o.Holder.Close()
	}
	if o.BootstrapDone != nil {
		select {
		case <-o.BootstrapDone:
		default:
			select {
			case <-o.BootstrapDone:
			case <-ctx.Done():
				errs = append(errs, fmt.Errorf("runner bootstrap did not settle: %w", ctx.Err()))
			}
		}
	}
	if o.Holder != nil {
		if err := o.Holder.Err(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
