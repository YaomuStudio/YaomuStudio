// 曜幕业务 Core（App Server）。
//
// 单一业务状态权威：UI / FlashCLI / Runner 不各自维护业务队列。
// 启动顺序（docs/initialization-and-task-lifecycle.md §1.1）：
// 数据目录 → SQLite 迁移与就绪 → 事件总线 → 服务/恢复准备 → 调度器（任务恢复）
// → 第三方端口（如启用）→ 内部 API 监听 → 异步 Runner 初始化。
//
// 私密通道约定（desktop/src/bootstrap.ts）：
//   - YAOMU_DATA_DIR：数据目录；
//   - YAOMU_CORE_PORT：Shell 分配的回环端口（0/缺省表示随机）；
//   - YAOMU_INSTANCE_TOKEN：本次实例令牌（只经环境变量传递，不写日志）。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/api"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/downloader"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/events"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/localapi"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/releasecatalog"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/scheduler"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/system"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/updateguard"
)

func main() {
	if err := run(); err != nil {
		log.Printf("yaomu-core: %v", err)
		os.Exit(1)
	}
}

func run() error {
	dataDir := os.Getenv("YAOMU_DATA_DIR")
	if dataDir == "" {
		dataDir = "."
	}
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("resolve data dir: %w", err)
	}
	if err := os.MkdirAll(absDataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// 诊断日志：文件不可写时降级到 stderr，不阻断启动（日志失败不等于业务失败）。
	logger, logErr := logging.New(logging.Options{
		Path:      filepath.Join(absDataDir, "logs", "core.log"),
		Component: "core",
	})
	if logger == nil {
		logger, _ = logging.New(logging.Options{})
	}
	defer func() { _ = logger.Close() }()
	if logErr != nil {
		log.Printf("yaomu-core: diagnostic log degraded: %v", logErr)
	}

	st, err := store.Open(context.Background(), filepath.Join(absDataDir, "yaomu.db"))
	if err != nil {
		return fmt.Errorf("database migration failed: %w", err)
	}
	defer st.Close()

	instanceID := store.NewID("instance")
	workspaceID, epoch, err := loadIdentity(context.Background(), st, instanceID)
	if err != nil {
		return err
	}

	token := os.Getenv("YAOMU_INSTANCE_TOKEN")
	if token == "" {
		token = store.NewID("devtoken")
		// 只提示缺失，不打印令牌值（令牌不能进日志）。
		log.Printf("yaomu-core: YAOMU_INSTANCE_TOKEN not provided; generated an ephemeral token for this process")
	}

	port, err := corePort()
	if err != nil {
		return err
	}
	heavySlots, err := envInt("YAOMU_HEAVY_SLOTS", 1)
	if err != nil {
		return err
	}

	bus := events.NewBus(st, logger)
	wslRuntimeReady := envBoolPtr("YAOMU_WSL_RUNTIME_READY")
	environmentDevices := service.NewEnvironmentDevices(func(ctx context.Context) (system.EnvironmentSnapshot, error) {
		return system.ProbeEnvironment(ctx, system.ProbeOptions{
			WSLDistro:       os.Getenv("YAOMU_WSL_DISTRO"),
			WSLRuntimeReady: wslRuntimeReady,
		}), nil
	})

	// 产品 interim 发布目录（内嵌 SHA-256 锁）。YAOMU_RELEASE_CATALOG 可指向外部 JSON 覆盖；
	// 设为 "none"/"off" 则退回未配置（安装计划返回 release_not_configured）。
	// 这不是签名 ReleaseManifest——仅接通可校验下载。
	var releases service.ReleaseCatalog = service.UnconfiguredReleases{}
	var engines service.EngineCatalog
	releasePrefixes := splitList(os.Getenv("YAOMU_RELEASE_BASE_URLS"))
	switch strings.ToLower(strings.TrimSpace(os.Getenv("YAOMU_RELEASE_CATALOG"))) {
	case "none", "off", "disable", "disabled":
		logger.Info("core", "core.release_catalog_disabled", nil)
	default:
		cat, err := releasecatalog.LoadFile(os.Getenv("YAOMU_RELEASE_CATALOG"))
		if err != nil {
			return fmt.Errorf("load release catalog: %w", err)
		}
		releases = cat
		engines = cat
		if len(releasePrefixes) == 0 {
			releasePrefixes = append([]string(nil), cat.Prefixes...)
		}
		logger.Info("core", "core.release_catalog_loaded", map[string]any{
			"prefixes": len(releasePrefixes),
			"note":     cat.Note,
		})
	}

	svc := service.New(service.Options{
		Store:       st,
		Bus:         bus,
		Log:         logger,
		WorkspaceID: workspaceID,
		InstanceID:  instanceID,
		ServerEpoch: epoch,
		AppVersion:  os.Getenv("YAOMU_APP_VERSION"),
		DataDir:     absDataDir,
		// 可选 YAOMU_MOCK_MODEL_SLOTS 让未安装槽走 Runner mock READY。
		Capabilities: service.HostMediaCapabilities{
			Inner: service.MockModelCapabilities{
				Inner: service.NewInstallationCapabilitiesWithMediaTools(
					service.MediaToolsReady(os.Getenv("FFMPEG_BIN"), os.Getenv("FFPROBE_BIN")),
				).WithEngines(engines),
				Slots: service.ParseMockModelSlots(os.Getenv("YAOMU_MOCK_MODEL_SLOTS")),
			},
			// 媒体工具由 yaomu WSL 镜像提供；镜像自检通过即视为 ffmpeg 槽位就绪，
			// 客户端不再检查宿主 FFmpeg。
			ImageRuntime: wslRuntimeReady != nil && *wslRuntimeReady,
			// 翻译是工具能力：WSL 镜像探测到 mlstools 时注入绝对路径；Linux 宿主
			// 由 Main 注入 MLSTOOLS_BIN，二者都只读环境，不要求模型安装记录。
			MLSToolsPath: func() string { return resolveMLSToolsPath(os.Getenv) },
		},
		Devices:     environmentDevices,
		Environment: environmentDevices,
		Releases:    releases,
		// 导入/重定位用同一固定 ffprobe 读取媒体元数据；未配置或缺失时
		// 导入仍成功但 Meta 为空（记录警告，不伪造）。
		MediaProber: newMediaProber(os.Getenv),
		// 分发源前缀：优先 YAOMU_RELEASE_BASE_URLS；否则取目录自带 distribution_prefixes。
		// 空前缀仍拒绝全部 URL。生产必须是 https，本地回环允许 http。
		Downloader: downloader.New(downloader.Options{
			Prefixes: releasePrefixes,
		}),
		HeavySlots: heavySlots,
		// Core 自动创建/推进八节点任务；关闭时保留手动 POST /tasks 路径。
		PipelineOrchestration: true,
	})
	if err := svc.EnsureStorageDirs(); err != nil {
		logger.Warn("core", "core.storage_dirs_failed", map[string]any{"error": err.Error()})
	}
	// 上次实例遗留的未完成安装不会自动续跑：收敛为可重试失败并清理临时文件。
	if err := svc.RecoverInstallations(context.Background()); err != nil {
		logger.Warn("core", "core.installation_recovery_failed", map[string]any{"error": err.Error()})
	}

	// 执行器：只按显式环境变量拉起本应用拥有的 Runner；不构造 FlashCLI 参数。
	// 宿主 Runner（ffmpeg/flashcli）走工作区租约；可选 WSL Runner（模型 adapter）
	// 独立启动。初始化是异步的：先创建稳定持有者，调度器只读该句柄；Runner
	// 至少一侧就绪后一次性发布完整不可变 RoutingExecutor，不在并发读取时更换指针。
	holder := execadapter.NewRoutingHolder()

	sched := scheduler.New(scheduler.Options{
		Store:       st,
		Bus:         bus,
		Log:         logger,
		Executor:    holder,
		Slots:       heavySlots,
		WorkspaceID: workspaceID,
		Epoch:       epoch,
		WorkDir:     filepath.Join(absDataDir, "work"),
		DataDir:     absDataDir,
		Hooks: scheduler.Hooks{
			PrepareExecution: svc.PrepareTaskExecution,
			PrepareResult:    svc.PrepareNodeResult,
			OnTaskSucceeded:  svc.OnTaskSucceeded,
			OnTaskFailed:     svc.OnTaskFailed,
			OnTaskCanceled:   svc.OnTaskCanceled,
		},
		PreDispatch: func(ctx context.Context, tx *store.Tx, task domain.Task) error {
			return svc.CheckTaskDispatchable(ctx, tx, task)
		},
	})
	// 通知器/取消器必须在 HTTP 对外前一次装配；不在异步回写 Service。
	svc.SetNotifier(sched)
	svc.SetCanceler(sched)

	// 应用更新准备：只在显式 prepare 时原子封闸普通请求/业务写，空闲复核
	// 通过后才停止空调度器并关闭空闲 Runner 资源。普通退出不触发安装。
	bootstrapDone := make(chan struct{})
	updateOwnership := &updateguard.CoreOwnership{
		Scheduler:     sched,
		Holder:        holder,
		Service:       svc,
		BootstrapDone: bootstrapDone,
	}
	updateGuard := updateguard.New(updateguard.Options{
		Store:       st,
		Ownership:   updateOwnership,
		WorkspaceID: workspaceID,
		InstanceID:  instanceID,
		ServerEpoch: epoch,
	})

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Windows 上 Shell 的 child.kill(SIGTERM) 等于 TerminateProcess，Core 收不到
	// 信号，defer 里的租约释放不会执行。Shell 在持有 stdin 管道时设置
	// YAOMU_SHUTDOWN_ON_STDIN=1，关闭管道即请求与信号相同的退出。
	watchStdinShutdown(stop)
	// Runner 进程树生命周期上下文只随 Core 退出结束；bootstrap 的握手使用
	// rootCtx 派生的可取消上下文，避免握手超时或关闭误杀已就绪的 Runner。
	runnerCtx, runnerCancel := context.WithCancel(context.Background())
	defer runnerCancel()

	// 预热设备/环境缓存；探测失败不阻断启动，读路径会再次尝试。
	go environmentDevices.Refresh(rootCtx)
	if err := svc.RecoverArtifactFiles(rootCtx); err != nil {
		return fmt.Errorf("artifact file recovery failed: %w", err)
	}
	// 任务恢复是可靠 Core 准备：失败时不监听业务 HTTP、不启动 Runner、不派发。
	if err := sched.Start(rootCtx); err != nil {
		return fmt.Errorf("scheduler start: %w", err)
	}
	if err := svc.BackfillMissingCloneAudio(rootCtx); err != nil {
		logger.Warn("core", "core.clone_audio_backfill_failed", map[string]any{"error": err.Error()})
	}
	go maintainEventRetention(rootCtx, st, logger)

	localManager, err := localapi.NewManager(st, logger, localapi.Options{})
	if err != nil {
		sched.Close(context.Background())
		return fmt.Errorf("local api manager: %w", err)
	}
	if err := localManager.Apply(rootCtx); err != nil {
		logger.Warn("core", "core.localapi_apply_failed", map[string]any{"error": err.Error()})
	}
	defer func() { _ = localManager.Close(context.Background()) }()
	if localManager.Enabled() {
		logger.Info("core", "core.localapi_enabled", map[string]any{"port": localManager.Port()})
	}

	handler := api.New(api.Config{
		Service:  svc,
		LocalAPI: localManager,
		Token:    token,
		Log:      logger,
		Updates:  updateGuard,
	}).Handler()

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		sched.Close(context.Background())
		return fmt.Errorf("listen core api: %w", err)
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return rootCtx },
	}
	serverErr := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	actualPort := listener.Addr().(*net.TCPAddr).Port
	// 就绪行只含地址，不含令牌（Shell 已持有令牌经私有通道注入）。
	fmt.Fprintf(os.Stderr, "yaomu-core: ready on 127.0.0.1:%d (pid %d)\n", actualPort, os.Getpid())
	logger.Info("core", "core.ready", map[string]any{"port": actualPort, "epoch": epoch})

	// 控制面已可用后才启动异步 Runner 初始化；慢 hello 不阻塞 health/live，
	// 也不阻塞编辑与诊断。模型未就绪由任务等待原因表达，不放宽 health 语义。
	bootstrapCtx, cancelBootstrap := context.WithCancel(rootCtx)
	defer cancelBootstrap()
	modelRequired := false
	modelCommand := ""
	var modelArgs []string
	if wslCmd := strings.TrimSpace(os.Getenv("YAOMU_WSL_RUNNER_COMMAND")); wslCmd != "" {
		modelRequired = true
		// WSL 冷启动后 hello 可能较慢；60s 预算由 BootstrapRunners 持有。
		parsedArgs, argsErr := parseRunnerArgs(os.Getenv("YAOMU_WSL_RUNNER_ARGS"))
		if argsErr != nil {
			logger.Warn("core", "core.wsl_runner_args_invalid", map[string]any{"error": argsErr.Error()})
		} else {
			modelCommand = wslCmd
			modelArgs = parsedArgs
		}
	}
	go func() {
		defer close(bootstrapDone)
		execadapter.BootstrapRunners(bootstrapCtx, runnerCtx, execadapter.RunnerBootstrapOptions{
			DataDir:       absDataDir,
			LeaseOwner:    execadapter.LeaseOwner{WorkspaceID: workspaceID, InstanceID: instanceID, Epoch: epoch},
			HostCommand:   strings.TrimSpace(os.Getenv("YAOMU_RUNNER_BIN")),
			ModelCommand:  modelCommand,
			ModelArgs:     modelArgs,
			ModelRequired: modelRequired,
			Logger:        logger,
			Holder:        holder,
			Notify:        sched.Notify,
		})
	}()

	var serveErr error
	select {
	case <-rootCtx.Done():
	case err := <-serverErr:
		if err != nil {
			serveErr = fmt.Errorf("core api serve: %w", err)
		}
	}

	// 明确退出：先取消 bootstrap 并停止新派发，再停止服务/调度，最后关闭本
	// 应用拥有的执行树与有所有权证明的租约。关闭可能耗时，不由持有者锁保护。
	cancelBootstrap()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// server.Shutdown 与 sched.Close 共用同一有界预算：若 HTTP shutdown 或
	// 慢取消耗尽预算，sched.Close 可能在活动 Execute 尚未收敛时返回。关闭
	// 顺序由 shutdownRunnerResources 守护，不在这里分散执行。
	_ = server.Shutdown(shutdownCtx)
	sched.Close(shutdownCtx)
	shutdownRunnerResources(shutdownCtx, holder, bootstrapDone, runnerCancel, logger)
	_ = localManager.Close(shutdownCtx)
	logger.Info("core", "core.stopped", map[string]any{})
	return serveErr
}

// shutdownRunnerResources 关闭本实例拥有的执行资源与租约。
//
// 这里防御性恢复既有清理顺序：先让已发布客户端走正常 Cancel 确认（进程仍
// 存活），再等待未发布 bootstrap 自行回收，最后取消进程树生命周期上下文。
// 正常预算内 sched.Close 会等待 Executor.Execute 完成取消确认，这不是普通
// 任务路径。只有在 HTTP shutdown 与 sched.Close 共用预算已耗尽、活动执行
// 尚未收敛的边界，若先取消进程 ctx，活动 Execute 才会断流，Client.Close
// 只能得到 ErrStopUnconfirmed 并 fail-closed 保留租约；不扩大总预算。
func shutdownRunnerResources(
	shutdownCtx context.Context,
	holder *execadapter.RoutingHolder,
	bootstrapDone <-chan struct{},
	cancelProcess context.CancelFunc,
	logger *logging.Logger,
) {
	// 先关闭已发布客户端：Client.Cancel 仍能向存活 Runner 请求退出确认。
	// 预算耗尽时这一步可能得到 ErrStopUnconfirmed，此时保留租约是正确行为。
	if err := holder.Close(); err != nil {
		if logger != nil {
			logger.Error("core", "core.runner_shutdown_failed", err, nil)
		}
	}
	// 未发布 bootstrap 仍拥有其客户端与租约，先等它自行回收（有界）。
	select {
	case <-bootstrapDone:
	case <-shutdownCtx.Done():
		if logger != nil {
			logger.Warn("core", "core.runner_bootstrap_shutdown_timeout", map[string]any{
				"reason": "bounded_wait",
			})
		}
	}
	// 最后取消进程树生命周期上下文，清理仍未退出的自有进程。
	cancelProcess()
}

// watchStdinShutdown 在 Shell 显式要求时把 stdin EOF 当作退出请求。
// 未设置该变量时不读 stdin，避免控制台运行或测试里的立即 EOF 把 Core 关掉。
func watchStdinShutdown(stop context.CancelFunc) {
	if strings.TrimSpace(os.Getenv("YAOMU_SHUTDOWN_ON_STDIN")) != "1" {
		return
	}
	go func() {
		buf := make([]byte, 512)
		for {
			_, err := os.Stdin.Read(buf)
			if err != nil {
				stop()
				return
			}
		}
	}()
}

// loadIdentity 读取/初始化工作区 ID，并递增 server epoch。
func loadIdentity(ctx context.Context, st *store.Store, instanceID string) (string, int64, error) {
	var workspaceID string
	var epoch int64
	err := st.DoTx(ctx, func(tx *store.Tx) error {
		value, found, err := tx.GetMeta(ctx, "workspace_id")
		if err != nil {
			return err
		}
		if !found || value == "" {
			value = store.NewID("ws")
			if err := tx.SetMeta(ctx, "workspace_id", value); err != nil {
				return err
			}
		}
		workspaceID = value

		rawEpoch, found, err := tx.GetMeta(ctx, "server_epoch")
		if err != nil {
			return err
		}
		if found {
			if parsed, err := strconv.ParseInt(rawEpoch, 10, 64); err == nil {
				epoch = parsed
			}
		}
		epoch++
		if err := tx.SetMeta(ctx, "server_epoch", strconv.FormatInt(epoch, 10)); err != nil {
			return err
		}
		// 记录本次实例 ID 便于诊断（不含令牌）。
		return tx.SetMeta(ctx, "last_instance_id", instanceID)
	})
	if err != nil {
		return "", 0, fmt.Errorf("initialize identity: %w", err)
	}
	return workspaceID, epoch, nil
}

// maintainEventRetention 按保留天数裁剪历史事件；0 表示不裁剪。
// 恢复所需的关键状态在业务表，裁剪只影响增量回放窗口。
func maintainEventRetention(ctx context.Context, st *store.Store, logger *logging.Logger) {
	days, err := envInt("YAOMU_EVENT_RETENTION_DAYS", 30)
	if err != nil {
		logger.Warn("core", "core.event_retention_invalid", map[string]any{"error": err.Error()})
		return
	}
	if days == 0 {
		return
	}
	prune := func() {
		before := time.Now().UTC().AddDate(0, 0, -days)
		// Store 级 PruneEventsBeforeTime 经受准入保护的写事务执行：更新准备
		// 封闸后事件保留不能绕过闸门。
		removed, err := st.PruneEventsBeforeTime(ctx, before)
		if err != nil {
			if errors.Is(err, store.ErrSealed) {
				logger.Info("core", "core.event_prune_skipped", map[string]any{"reason": "update_preparation_sealed"})
				return
			}
			logger.Error("core", "core.event_prune_failed", err, nil)
			return
		}
		if removed > 0 {
			logger.Info("core", "core.events_pruned", map[string]any{"removed": removed, "retention_days": days})
		}
	}
	prune()
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

// newMediaProber 按顺序选择导入/重定位的媒体探测器：
//
//  1. FFPROBE_BIN 非空 → 宿主显式工具（既有行为完全不变）；
//  2. YAOMU_WSL_FFPROBE_BIN 非空且 distro/工具路径合法 → 镜像内 WSL 探测器；
//  3. 否则现有宿主 PATH 回退（开发机兼容，不作为 Windows 生产依赖）。
//
// WSL 配置非法或构造失败只安全回退，不阻塞 Core 就绪；错误信息不回显路径。
// resolveMLSToolsPath 优先使用 WSL 镜像内的绝对路径；Linux 宿主由 Main
// 注入 MLSTOOLS_BIN。两者都只读环境，不检查宿主 PATH。
func resolveMLSToolsPath(getenv func(string) string) string {
	if p := getenv("YAOMU_WSL_MLSTOOLS_BIN"); p != "" {
		return p
	}
	return getenv("MLSTOOLS_BIN")
}

func newMediaProber(getenv func(string) string) service.MediaProber {
	if explicit := strings.TrimSpace(getenv("FFPROBE_BIN")); explicit != "" {
		return system.NewFFprobeProber(explicit)
	}
	wslFFprobe := strings.TrimSpace(getenv("YAOMU_WSL_FFPROBE_BIN"))
	if wslFFprobe != "" {
		if prober, err := system.NewWSLFFprobeProber(system.WSLMediaProbeOptions{
			Distro:      strings.TrimSpace(getenv("YAOMU_WSL_DISTRO")),
			FFprobePath: wslFFprobe,
			WSLExe:      strings.TrimSpace(getenv("YAOMU_WSL_EXE")),
		}); err == nil {
			return prober
		}
	}
	return system.NewFFprobeProber("")
}

// corePort 解析 YAOMU_CORE_PORT；缺省 0 表示随机端口。
func corePort() (int, error) {
	raw := strings.TrimSpace(os.Getenv("YAOMU_CORE_PORT"))
	if raw == "" {
		return 0, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 0 || port > 65535 {
		return 0, fmt.Errorf("invalid YAOMU_CORE_PORT %q", raw)
	}
	return port, nil
}

func envInt(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid %s %q", name, raw)
	}
	return value, nil
}

// envBoolPtr 解析可选布尔开关；未设置返回 nil，便于区分“未探测”和“探测失败”。
func envBoolPtr(name string) *bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	value := raw == "1" || strings.EqualFold(raw, "true") || strings.EqualFold(raw, "yes")
	return &value
}

// splitList 解析逗号分隔的配置值，忽略空项。
func splitList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// parseRunnerArgs 解析 YAOMU_WSL_RUNNER_ARGS：JSON 字符串数组，或空。
func parseRunnerArgs(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("YAOMU_WSL_RUNNER_ARGS must be a JSON string array: %w", err)
	}
	return args, nil
}
