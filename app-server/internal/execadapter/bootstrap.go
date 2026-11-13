package execadapter

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
)

// runnerLease 是宿主 Runner 工作区租约的最小接口；生产实现是
// *WorkspaceLease，测试可注入替身。
type runnerLease interface {
	RecordRunner(identity string) error
	ReleaseAfterStop(stopErr error) error
}

// 既有握手预算：宿主 Runner 10s，WSL 冷启动 60s。异步启动不改变这两个值，
// 只把它们从启动关键路径移开。
const (
	defaultHostHelloTimeout  = 10 * time.Second
	defaultModelHelloTimeout = 60 * time.Second
)

// RunnerBootstrapOptions 描述一次 Core 实例的 Runner 初始化。
//
// 初始化只写内存执行资源状态；业务 Run/Task 事实仍只由 Core 事务决定。
type RunnerBootstrapOptions struct {
	// DataDir 是 workspace Runner 租约所在的数据目录。
	DataDir string
	// LeaseOwner 是本实例对 workspace Runner 的租约身份。
	LeaseOwner LeaseOwner
	// HostCommand 非空时拉起宿主 Runner（ffmpeg 等宿主媒体）。
	HostCommand string
	// ModelCommand 非空时拉起模型侧 Runner；ModelArgs 必须来自显式配置，
	// 不构造未锁定的 FlashCLI 参数。
	ModelCommand string
	ModelArgs    []string
	// ModelRequired 表示本实例需要模型侧执行。即使 hello 失败也保留既有
	// RoutingExecutor 可见失败语义，不把模型任务伪装为可执行。
	ModelRequired bool
	// Logger 可为 nil（测试）。
	Logger *logging.Logger
	// Holder 是调度器持有的稳定执行器句柄。
	Holder *RoutingHolder
	// Notify 在 bootstrap 收敛后唤醒调度器一次（可为 nil）。持有者不排队。
	Notify func()

	// 以下字段供测试注入；生产使用默认实现。
	startClient       func(ctx context.Context, opts ProcessOptions) (*Client, error)
	acquireLease      func(dataDir string, owner LeaseOwner) (runnerLease, error)
	hostHelloTimeout  time.Duration
	modelHelloTimeout time.Duration
}

// BootstrapRunners 在当前 goroutine 内完成 Runner 初始化并发布执行器。
//
// 调用方必须把它放在受根 context 管理的 goroutine 中。startupCtx 约束握手、
// 取消与迟到发布；processCtx 是本应用拥有的 Runner 进程树生命周期上下文，
// 两者分离，避免握手超时或调度关闭误杀已就绪的 Runner。
//
// 发布是一次性的：完整不可变 RoutingExecutor 同时包含宿主/模型两侧结果，
// 不在调度器并发读取时原地替换指针。初始化期间保持 starting，失败或没有
// 可用路由时收敛为 unavailable；已关闭的持有者拒绝迟到发布，本函数仍是
// 该次初始化所创建资源的唯一回收者。
func BootstrapRunners(startupCtx, processCtx context.Context, opts RunnerBootstrapOptions) {
	holder := opts.Holder
	if holder == nil {
		return
	}
	startClient := opts.startClient
	if startClient == nil {
		startClient = NewProcessClient
	}
	acquireLease := opts.acquireLease
	if acquireLease == nil {
		acquireLease = func(dataDir string, owner LeaseOwner) (runnerLease, error) {
			return AcquireWorkspaceLease(dataDir, owner)
		}
	}
	hostTimeout := opts.hostHelloTimeout
	if hostTimeout <= 0 {
		hostTimeout = defaultHostHelloTimeout
	}
	modelTimeout := opts.modelHelloTimeout
	if modelTimeout <= 0 {
		modelTimeout = defaultModelHelloTimeout
	}

	var (
		hostClient   *Client
		modelClient  *Client
		lease        runnerLease
		failedCloses []error
		published    bool
	)
	cleanup := func() error {
		var stopErrs []error
		if modelClient != nil {
			stopErrs = append(stopErrs, modelClient.Close())
		}
		if hostClient != nil {
			stopErrs = append(stopErrs, hostClient.Close())
		}
		stopErrs = append(stopErrs, failedCloses...)
		stopErr := errors.Join(stopErrs...)
		if lease != nil {
			if err := lease.ReleaseAfterStop(stopErr); err != nil {
				logBootstrapError(opts.Logger, "core.runner_lease_retained", err)
				return errors.Join(stopErr, err)
			}
		}
		return stopErr
	}
	defer func() {
		if !published {
			// 初始化失败、取消或迟到发布：本次 bootstrap 仍是唯一拥有者。
			// 回收错误必须锁存到持有者；不得只写日志后把“不可用”当成干净。
			if err := cleanup(); err != nil {
				holder.RecordBootstrapError(err)
			}
		}
		if opts.Notify != nil {
			// 收敛后唤醒调度器一次；任务本身从不进入持有者队列。
			opts.Notify()
		}
	}()

	if startupCtx.Err() != nil {
		holder.MarkUnavailable()
		return
	}

	hasHost := strings.TrimSpace(opts.HostCommand) != ""
	hasModel := strings.TrimSpace(opts.ModelCommand) != ""
	if hasHost || hasModel {
		acquired, leaseErr := acquireLease(opts.DataDir, opts.LeaseOwner)
		if leaseErr != nil {
			// A previous Core may have died with a detached media process alive.
			// Keep editing available, but never infer stale ownership from a PID;
			// do not start either Runner without a workspace lease.
			holder.RecordBootstrapError(leaseErr)
			if opts.Logger != nil {
				opts.Logger.Error("core", "core.runner_isolated", leaseErr, map[string]any{
					"reason": "workspace_lease_or_process_tree_unverified", "executor_available": false,
				})
			}
			holder.MarkUnavailable()
			return
		}
		lease = acquired
	}

	if hasHost {
		client, err := startClient(processCtx, ProcessOptions{
			Command: opts.HostCommand,
			Logger:  opts.Logger,
		})
		if err != nil {
			logBootstrapWarn(opts.Logger, "core.runner_start_failed", map[string]any{"error": err.Error()})
		} else {
			helloErr := errors.Join(
				lease.RecordRunner(client.Identity()),
				helloOnce(startupCtx, client, hostTimeout),
			)
			if helloErr != nil {
				logBootstrapWarn(opts.Logger, "core.runner_hello_failed", map[string]any{
					"runner": "host", "error": helloErr.Error(),
				})
				if closeErr := client.Close(); closeErr != nil {
					holder.RecordBootstrapError(closeErr)
					failedCloses = append(failedCloses, closeErr)
				}
			} else {
				hostClient = client
			}
		}
	}

	if startupCtx.Err() == nil && hasModel {
		client, err := startClient(processCtx, ProcessOptions{
			Command:                   opts.ModelCommand,
			Args:                      opts.ModelArgs,
			Logger:                    opts.Logger,
			DetachedChildrenUncovered: true,
		})
		if err != nil {
			logBootstrapWarn(opts.Logger, "core.wsl_runner_start_failed", map[string]any{"error": err.Error()})
		} else if helloErr := errors.Join(
			lease.RecordRunner(client.Identity()),
			helloOnce(startupCtx, client, modelTimeout),
		); helloErr != nil {
			logBootstrapWarn(opts.Logger, "core.runner_hello_failed", map[string]any{
				"runner": "model", "error": helloErr.Error(),
			})
			// 模型侧客户端回收失败同样属于未确认的执行资源清理。
			if closeErr := client.Close(); closeErr != nil {
				holder.RecordBootstrapError(closeErr)
				failedCloses = append(failedCloses, closeErr)
			}
		} else {
			modelClient = client
			logBootstrapInfo(opts.Logger, "core.runner_ready", map[string]any{"runner": "model"})
		}
	}

	if startupCtx.Err() != nil {
		// 取消/退出中的初始化不得发布半个执行器；defer 负责回收。
		holder.MarkUnavailable()
		return
	}
	if hostClient == nil && modelClient == nil && !opts.ModelRequired {
		holder.MarkUnavailable()
		return
	}

	var hostExec *Executor
	var modelExec *Executor
	if hostClient != nil {
		hostExec = &Executor{Client: hostClient}
	}
	if modelClient != nil {
		modelExec = &Executor{Client: modelClient}
	}
	routing := &RoutingExecutor{
		Host:          hostExec,
		Model:         modelExec,
		ModelRequired: opts.ModelRequired,
	}
	if holder.Publish(routing, cleanup) {
		published = true
	}
	// Publish=false 表示持有者已关闭：defer 的 cleanup 回收本 goroutine
	// 创建的资源，绝不由已关闭的持有者接管。
}

// helloOnce 在 startupCtx 派生出的有界上下文中执行一次 hello。
func helloOnce(startupCtx context.Context, client *Client, timeout time.Duration) error {
	helloCtx, cancel := context.WithTimeout(startupCtx, timeout)
	defer cancel()
	_, err := client.Start(helloCtx)
	return err
}

func logBootstrapInfo(logger *logging.Logger, event string, fields map[string]any) {
	if logger != nil {
		logger.Info("core", event, fields)
	}
}

func logBootstrapWarn(logger *logging.Logger, event string, fields map[string]any) {
	if logger != nil {
		logger.Warn("core", event, fields)
	}
}

func logBootstrapError(logger *logging.Logger, event string, err error) {
	if logger != nil {
		logger.Error("core", event, err, nil)
	}
}
