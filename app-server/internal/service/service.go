// Package service 是 Core 的业务编排层：预检、Run/Task 生命周期、审核放行、
// 候选登记、字幕修订、导出、安装计划与第三方本机 API 设置。
//
// 边界（docs/architecture.md、docs/local-api-contract.md §8）：
//   - Core 是唯一业务 writer；每个状态变更通过 store.DoTx 在同一事务内写入
//     业务行、关键事件与幂等回执，提交成功后才通知调度器；
//   - 派发闸门在事务内比较 Run 状态与版本：PAUSE_REQUESTED/PAUSED 只登记，
//     终止 Run 拒绝新增执行；
//   - 对外只返回安全错误码，原始输出不进入 API。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/events"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/system"
)

// Notifier 接收“有新工作可派发/暂停需收敛”的内存唤醒。
type Notifier interface {
	Notify()
}

// TaskCanceler 请求调度层取消正在执行的尝试；未实现时取消只落库。
type TaskCanceler interface {
	CancelTask(id domain.ID)
}

// Options 是 Service 的构造参数。
type Options struct {
	Store       *store.Store
	Bus         *events.Bus
	Log         *logging.Logger
	Clock       func() time.Time
	WorkspaceID string
	InstanceID  string
	ServerEpoch int64

	AppID           string
	AppVersion      string
	ProtocolVersion string

	// DataDir 是应用数据根目录；子目录由本包按 FR-SET-42 的类别划分。
	DataDir string

	Capabilities CapabilityProvider
	Devices      DeviceProvider
	Environment  EnvironmentProvider
	Releases     ReleaseCatalog
	Downloader   Downloader

	// HeavySlots 是重型推理资源槽上限（默认 1，可配置）。
	HeavySlots int

	// PipelineOrchestration 控制是否由 Core 自动创建/推进八节点任务。
	// 默认关闭以保持既有测试与手动 POST /tasks 的基线；生产入口开启。
	PipelineOrchestration bool

	// Statfs 探测路径所在文件系统的容量；nil 时使用 system.Statfs。
	// 测试可注入假实现，构造空间不足/健康线以下的场景。
	Statfs func(path string) (system.DiskSpace, error)

	// MediaProber 在导入/重定位时读取媒体元数据；nil 时文件仍可登记，
	// 但 Meta 为空并记录警告（不伪造容器/时长/采样率）。
	MediaProber MediaProber

	// DiskHealthBytes 是应用数据分区的健康线（FR-SET-22，默认 30GiB）；
	// <=0 时使用默认值。测试可注入小阈值。
	DiskHealthBytes int64
}

// Service 承载业务编排。
type Service struct {
	store *store.Store
	bus   *events.Bus
	log   *logging.Logger
	now   func() time.Time
	opts  Options

	notifier Notifier
	canceler TaskCanceler

	installMu sync.Mutex
	installs  map[domain.ID]context.CancelFunc
	// installRoots 记录仍在运行的安装 worker 拥有的物理根；取消/失败尾部
	// 在退出前继续阻止同一目录的新 writer/删除（短锁，不在持锁时等 DB）。
	installRoots map[domain.ID]string

	// verifyFiles 校验已落位文件（复用/回滚前，事务与全局写锁之外）。
	// 生产实现为本地 hash；测试可注入屏障实现观察事务边界。
	verifyFiles installationFileVerifier

	// installationLifecycleMu 串行化安装生命周期决策（create/join/delete/
	// rollback/model cleanup）。它只保护决策与目录所有权，不是第二套业务队列。
	installationLifecycleMu sync.Mutex

	// writeMu 串行化业务写事务：SQLite 单写者，且避免同一幂等键的并发请求
	// 在回执写入前各自执行副作用。
	writeMu         sync.Mutex
	importMu        sync.Mutex
	assetIdentityMu sync.Mutex
	assetIdentities map[domain.ID]verifiedAssetIdentity

	// storageUsage 缓存四类目录用量（TTL 内复用），避免每次读取都全盘扫描；
	// 清理等改变磁盘内容的操作通过 InvalidateStorageUsage 失效缓存。
	usageMu     sync.Mutex
	usageCached bool
	usageValue  domain.StorageUsage
	usageAt     time.Time
}

// New 构造 Service；缺失的可选依赖使用安全默认实现。
func New(opts Options) *Service {
	if opts.Clock == nil {
		opts.Clock = func() time.Time { return time.Now().UTC() }
	}
	if opts.Capabilities == nil {
		opts.Capabilities = NewInstallationCapabilities()
	}
	if opts.Devices == nil {
		opts.Devices = SystemDevices{}
	}
	if opts.Environment == nil {
		opts.Environment = SystemEnvironment{Devices: opts.Devices}
	}
	if opts.Releases == nil {
		opts.Releases = UnconfiguredReleases{}
	}
	if opts.Downloader == nil {
		opts.Downloader = UnavailableDownloader{}
	}
	if opts.Statfs == nil {
		opts.Statfs = system.Statfs
	}
	if opts.AppID == "" {
		opts.AppID = "yaomu"
	}
	if opts.ProtocolVersion == "" {
		opts.ProtocolVersion = "0.2"
	}
	s := &Service{
		store:        opts.Store,
		bus:          opts.Bus,
		log:          opts.Log,
		now:          opts.Clock,
		opts:         opts,
		installs:     make(map[domain.ID]context.CancelFunc),
		installRoots: make(map[domain.ID]string),
		verifyFiles:  verifyInstallationFiles,
	}
	return s
}

// SetNotifier 注入调度唤醒器（可在构造后设置，避免包循环）。
func (s *Service) SetNotifier(n Notifier) { s.notifier = n }

// SetCanceler 注入正在执行任务的取消入口。
func (s *Service) SetCanceler(c TaskCanceler) { s.canceler = c }

// WorkspaceID 返回当前工作区 ID。
func (s *Service) WorkspaceID() string { return s.opts.WorkspaceID }

// Store 暴露仓储供调度层共用（Core 内部仍只经同一事务写业务状态）。
func (s *Service) Store() *store.Store { return s.store }

// Bus 暴露事件总线供 API 的 SSE 使用。
func (s *Service) Bus() *events.Bus { return s.bus }

// Now 返回服务时钟（UTC）。
func (s *Service) Now() time.Time { return s.now().UTC() }

// heavySlotCount 返回重型推理槽上限；默认一个。
func (s *Service) heavySlotCount() int {
	if s.opts.HeavySlots > 0 {
		return s.opts.HeavySlots
	}
	return 1
}

// HeavySlots 暴露给 main 组装调度器，保证两处上限一致。
func (s *Service) HeavySlots() int { return s.heavySlotCount() }

func (s *Service) notify() {
	if s.notifier != nil {
		s.notifier.Notify()
	}
}

// warn 记录本地诊断；未配置 logger 的测试环境静默跳过。
// 调用方不得在字段中放路径、媒体正文或令牌。
func (s *Service) warn(event string, fields map[string]any) {
	if s.log == nil {
		return
	}
	s.log.Warn("service", event, fields)
}

// ---- 幂等 ----

// Idem 是幂等键与请求摘要。
type Idem struct {
	Key    string
	Digest string
}

// Response 是写操作的响应；Body 为 json.RawMessage 时表示幂等重放的原响应。
type Response struct {
	Status int
	Body   any
}

// 常见错误构造。

func invalidRequestf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", domain.ErrInvalidRequest, fmt.Sprintf(format, args...))
}

func notFoundf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", domain.ErrNotFound, fmt.Sprintf(format, args...))
}

// referenceError 把请求体里的引用 ID 缺失映射为 INVALID_REQUEST：
// 契约中这些端点只声明 400/409，404 保留给路径直接寻址的资源。
func referenceError(what string) error {
	return invalidRequestf("referenced %s not found", what)
}

// mapReference 在引用读取失败时给出 400；其他错误原样返回。
func mapReference(err error, what string) error {
	if errors.Is(err, domain.ErrNotFound) {
		return referenceError(what)
	}
	return err
}

// errResourceBusy 表示资源仍被活跃任务/唯一有效引用占用（409，可稍后重试）。
// 契约未定义专用错误码；使用 REVISION_CONFLICT 的 409 语义并保留可重试标记。
func errResourceBusy(what string) error {
	return &domain.Error{
		Code:       domain.ErrorCodeRevisionConflict,
		MessageKey: "error.resource_busy",
		Retryable:  true,
		ActionIDs:  []domain.ID{"retry_later"},
	}
}

// errConflictf 构造 409 语义的状态冲突。
func errConflictf(code domain.ErrorCode, messageKey string, format string, args ...any) error {
	return fmt.Errorf("%w: %s", &domain.Error{Code: code, MessageKey: messageKey}, fmt.Sprintf(format, args...))
}

// errRunClosed 表示终止/不满足派发条件的 Run 拒绝新增执行或放行（409）。
func errRunClosed(runID string, status domain.RunStatus) error {
	return errConflictf(domain.ErrorCodePreflightStale, "error.run_not_dispatchable",
		"run %s is %s and refuses new execution", runID, status)
}

// receiptEnvelope 是幂等回执的响应信封（状态码 + 原响应体）。
type receiptEnvelope struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

// write 在单事务内执行状态变更并持久化幂等回执。
//
// idem.Key 为空时只是一次普通事务；非空时：
//   - 已存在且请求摘要相同：返回原响应（Body 为 json.RawMessage）；
//   - 已存在但摘要不同：IDEMPOTENCY_CONFLICT；
//   - 不存在：执行 fn、编码响应、写入回执，与业务行同事务提交。
func (s *Service) write(
	ctx context.Context,
	operation string,
	idem Idem,
	status int,
	fn func(context.Context, *store.Tx) (any, error),
) (Response, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out Response
	err := s.store.DoTx(ctx, func(tx *store.Tx) error {
		if idem.Key != "" {
			existing, err := tx.GetReceipt(ctx, s.opts.WorkspaceID, operation, idem.Key)
			switch {
			case err == nil:
				if existing.RequestDigest != idem.Digest {
					return fmt.Errorf("%w: key reused with a different request digest", domain.ErrIdempotencyConflict)
				}
				var envelope receiptEnvelope
				if err := json.Unmarshal(existing.ResponseJSON, &envelope); err != nil {
					return fmt.Errorf("decode receipt: %w", err)
				}
				out = Response{Status: envelope.Status, Body: envelope.Body}
				return nil
			case errors.Is(err, domain.ErrNotFound):
				// 首次执行。
			default:
				return err
			}
		}

		body, err := fn(ctx, tx)
		if err != nil {
			return err
		}
		out = Response{Status: status, Body: body}
		if idem.Key == "" {
			return nil
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode response: %w", err)
		}
		envelope, err := json.Marshal(receiptEnvelope{Status: status, Body: encoded})
		if err != nil {
			return fmt.Errorf("encode receipt: %w", err)
		}
		_, _, err = tx.PutReceipt(ctx, store.Receipt{
			WorkspaceID:   s.opts.WorkspaceID,
			Operation:     operation,
			Key:           idem.Key,
			RequestDigest: idem.Digest,
			ResponseJSON:  envelope,
			CreatedAt:     s.Now(),
		})
		return err
	})
	if err != nil {
		return Response{}, err
	}
	return out, nil
}

// PerformIdempotentEffect 为“副作用不在业务事务内”的管理操作提供幂等回执：
// 执行前查询回执，执行后另开事务持久化响应。适用于本地 API 设置/吊销这类
// 天然幂等、且响应不含敏感信息的管理端点（令牌签发不持久化回执）。
func (s *Service) PerformIdempotentEffect(
	ctx context.Context,
	operation string,
	idem Idem,
	status int,
	effect func(context.Context) (any, error),
) (Response, error) {
	if idem.Key != "" {
		existing, err := s.store.GetReceipt(ctx, s.opts.WorkspaceID, operation, idem.Key)
		switch {
		case err == nil:
			if existing.RequestDigest != idem.Digest {
				return Response{}, fmt.Errorf("%w: key reused with a different request digest", domain.ErrIdempotencyConflict)
			}
			var envelope receiptEnvelope
			if err := json.Unmarshal(existing.ResponseJSON, &envelope); err != nil {
				return Response{}, fmt.Errorf("decode receipt: %w", err)
			}
			return Response{Status: envelope.Status, Body: envelope.Body}, nil
		case !errors.Is(err, domain.ErrNotFound):
			return Response{}, err
		}
	}

	body, err := effect(ctx)
	if err != nil {
		return Response{}, err
	}
	if idem.Key == "" {
		return Response{Status: status, Body: body}, nil
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("encode response: %w", err)
	}
	envelope, err := json.Marshal(receiptEnvelope{Status: status, Body: encoded})
	if err != nil {
		return Response{}, fmt.Errorf("encode receipt: %w", err)
	}
	err = s.store.DoTx(ctx, func(tx *store.Tx) error {
		_, _, err := tx.PutReceipt(ctx, store.Receipt{
			WorkspaceID:   s.opts.WorkspaceID,
			Operation:     operation,
			Key:           idem.Key,
			RequestDigest: idem.Digest,
			ResponseJSON:  envelope,
			CreatedAt:     s.Now(),
		})
		return err
	})
	if err != nil {
		return Response{}, err
	}
	return Response{Status: status, Body: body}, nil
}

// appendEvent 在事务内追加事件并返回分配到的 seq；调用方负责在提交后 Publish。
func (s *Service) appendEvent(ctx context.Context, tx *store.Tx, ev domain.DomainEvent) (domain.DomainEvent, error) {
	if ev.SchemaVersion == 0 {
		ev.SchemaVersion = 1
	}
	if ev.Data == nil {
		ev.Data = map[string]any{}
	}
	seq, err := tx.AppendEvent(ctx, ev, s.Now())
	if err != nil {
		return domain.DomainEvent{}, err
	}
	ev.Seq = seq
	return ev, nil
}

// publish 在事务提交后广播事件（事件带 seq）。
func (s *Service) publish(evs ...domain.DomainEvent) {
	if s.bus == nil {
		return
	}
	for _, ev := range evs {
		s.bus.Publish(ev)
	}
}

// stageEvent 在事务内追加事件并暂存，待提交成功后由调用方发布。
func (s *Service) stageEvent(ctx context.Context, tx *store.Tx, events *[]domain.DomainEvent, ev domain.DomainEvent) error {
	appended, err := s.appendEvent(ctx, tx, ev)
	if err != nil {
		return err
	}
	*events = append(*events, appended)
	return nil
}

// digestOf 计算请求/计划摘要（sha256 hex）。
func digestOf(v any) (string, error) {
	return domain.Digest(v)
}

// requestDigest 是写请求的幂等摘要：方法 + 路径 + 查询 + 请求体。
func requestDigest(method, path, query string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write([]byte(query))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// RequestDigest 暴露给 API 层计算幂等摘要。
func RequestDigest(method, path, query string, body []byte) string {
	return requestDigest(method, path, query, body)
}
