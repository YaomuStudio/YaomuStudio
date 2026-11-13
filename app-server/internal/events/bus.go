// Package events 是工作区级事件总线：持久 seq 回放 + 实时订阅 + 游标缺口判定。
//
// 语义（docs/local-api-contract.md §5）：
//   - 状态与对应事件在同一事务提交，提交之后才发布（由调用方在 DoTx 成功后调用 Publish）；
//   - 订阅握手先注册后补读 [afterSeq, H]，再转实时，客户端按 seq 去重；
//   - 游标低于仍可回放的最早 seq 时返回 EVENT_CURSOR_EXPIRED，要求重新获取快照；
//   - 高频进度是可丢弃反馈：调度层在条件写快照成功后经 PublishLive 发布
//     task.progress（seq=0），不进入持久事件流；本包不补发进度帧，重连以任务快照为准。
package events

import (
	"context"
	"fmt"
	"sync"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// subscriberBuffer 是单个订阅的缓冲深度；溢出时关闭该订阅，
// 客户端按最后处理到的 seq 重连续传（不能静默丢持久事件）。
const subscriberBuffer = 256

// maxPending 约束乱序缓冲上限；超出说明订阅长期无法按序推进，关闭它。
const maxPending = 4096

// backlogBatch 是补读历史时每批读取的数量。
const backlogBatch = 200

// afterSubscribeHook 仅供测试模拟“订阅注册后、补读前发生保留裁剪”的竞态；
// 生产路径为 nil。缺口无法补齐时必须显式要求快照，不能让订阅静默卡住。
var afterSubscribeHook func()

// Bus 是事件广播器。
type Bus struct {
	store *store.Store
	log   *logging.Logger

	mu     sync.Mutex
	nextID int
	subs   map[int]*subscriber
}

// subscriber 按 seq 顺序向通道推送：enqueued 记录已交给消费者的最大 seq，
// pending 暂存先到的高 seq（多事务提交后的发布顺序可能乱序）。
// live 承载可丢弃事件（seq=0），不参与游标推进。
type subscriber struct {
	ch       chan domain.DomainEvent
	live     chan domain.DomainEvent
	enqueued int64
	pending  map[int64]domain.DomainEvent
}

// NewBus 构造事件总线。
func NewBus(st *store.Store, log *logging.Logger) *Bus {
	return &Bus{store: st, log: log, subs: make(map[int]*subscriber)}
}

// Publish 把已提交的事件广播给当前订阅者；seq 必须由持久层分配。
func (b *Bus) Publish(ev domain.DomainEvent) {
	if ev.Seq <= 0 {
		// 未持久化的事件没有可去重的游标，不能进入恢复语义。
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for id := range b.subs {
		if !b.pushLocked(id, ev) {
			b.log.Warn("events", "subscriber.overflow", map[string]any{"subscriber_id": id, "seq": ev.Seq})
		}
	}
}

// PublishLive 广播可丢弃实时事件（不持久化、不推进游标）；缓冲满时丢弃该订阅的本条。
func (b *Bus) PublishLive(ev domain.DomainEvent) {
	if b == nil {
		return
	}
	if ev.SchemaVersion == 0 {
		ev.SchemaVersion = 1
	}
	if ev.Data == nil {
		ev.Data = map[string]any{}
	}
	ev.Seq = 0
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, sub := range b.subs {
		select {
		case sub.live <- ev:
		default:
			// 可丢弃：跟不上的订阅直接丢本条，不关闭订阅。
		}
	}
}

// pushLocked 按 seq 顺序入队；返回 false 表示订阅已被关闭。
//
// 只有 enqueued+1 存在时才推进；先到的高 seq 暂存，直到缺口补齐。
func (b *Bus) pushLocked(id int, ev domain.DomainEvent) bool {
	sub, ok := b.subs[id]
	if !ok {
		return true
	}
	if ev.Seq <= sub.enqueued {
		return true // 重复（补读与实时重叠）。
	}
	if _, exists := sub.pending[ev.Seq]; !exists {
		if len(sub.pending) >= maxPending {
			b.dropLocked(id)
			return false
		}
		sub.pending[ev.Seq] = ev
	}
	for {
		next, ok := sub.pending[sub.enqueued+1]
		if !ok {
			return true
		}
		select {
		case sub.ch <- next:
			sub.enqueued++
			delete(sub.pending, next.Seq)
		default:
			b.dropLocked(id)
			return false
		}
	}
}

func (b *Bus) dropLocked(id int) {
	sub, ok := b.subs[id]
	if !ok {
		return
	}
	delete(b.subs, id)
	close(sub.ch)
	close(sub.live)
}

func (b *Bus) subscribeLocked(afterSeq int64) (int, *subscriber) {
	b.nextID++
	id := b.nextID
	sub := &subscriber{
		ch:       make(chan domain.DomainEvent, subscriberBuffer),
		live:     make(chan domain.DomainEvent, subscriberBuffer),
		enqueued: afterSeq,
		pending:  make(map[int64]domain.DomainEvent),
	}
	b.subs[id] = sub
	return id, sub
}

func (b *Bus) unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dropLocked(id)
}

// enqueueBacklog 把历史事件按序放入订阅缓冲。
func (b *Bus) enqueueBacklog(id int, ev domain.DomainEvent) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pushLocked(id, ev)
}

// CheckCursor 校验 afterSeq 是否仍在可回放范围内。
//
// earliest 是仍可回放的最早 seq；afterSeq+1 < earliest 表示中间已有被裁剪的
// 事件，必须重新获取快照（EVENT_CURSOR_EXPIRED）。
func (b *Bus) CheckCursor(ctx context.Context, afterSeq int64) error {
	earliest, err := b.store.EarliestEventSeq(ctx)
	if err != nil {
		return fmt.Errorf("read earliest event seq: %w", err)
	}
	latest, err := b.store.LatestEventSeq(ctx)
	if err != nil {
		return fmt.Errorf("read latest event seq: %w", err)
	}
	if earliest > 0 && afterSeq+1 < earliest {
		return fmt.Errorf("%w: cursor %d older than earliest %d", domain.ErrEventCursorExpired, afterSeq, earliest)
	}
	if afterSeq > latest {
		// 游标超前于实际历史（客户端自报错误或历史被裁剪）：必须重新获取快照。
		return fmt.Errorf("%w: cursor %d beyond latest %d", domain.ErrEventCursorExpired, afterSeq, latest)
	}
	return nil
}

// replayBacklog 与流消费循环并行补读 [afterSeq, high]，按序放入订阅缓冲。
// 返回 nil 表示补读完成、订阅已被取消或缓冲溢出（由消费循环收尾）；
// 返回错误表示游标缺口，无法静默补齐。
func (b *Bus) replayBacklog(ctx context.Context, id int, afterSeq, high int64) error {
	cursor := afterSeq
	for cursor < high {
		batch, err := b.store.EventsAfter(ctx, cursor, backlogBatch)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read event backlog: %w", err)
		}
		if len(batch) == 0 {
			return nil
		}
		advanced := false
		for _, ev := range batch {
			if ev.Seq > high {
				break
			}
			if ev.Seq != cursor+1 {
				// 订阅注册后历史又被保留策略裁剪：缺口无法补齐，必须要求重新
				// 获取快照（EVENT_CURSOR_EXPIRED），不能静默等待一个不再出现的 seq。
				return fmt.Errorf("%w: event %d missing after cursor %d",
					domain.ErrEventCursorExpired, cursor+1, cursor)
			}
			if !b.enqueueBacklog(id, ev) {
				return nil
			}
			cursor = ev.Seq
			advanced = true
		}
		if !advanced {
			return nil
		}
	}
	return nil
}

// Stream 从 afterSeq 开始推送事件：先注册订阅，再并行补读 [afterSeq, H]
// 与消费实时事件，按 seq 合流后交给 emit；emit 返回错误时结束流（通常是客户端断开）。
//
// 返回 nil 表示流正常结束（客户端断开或订阅溢出后的重连续传）；返回
// ErrEventCursorExpired 表示需要重新获取快照。
func (b *Bus) Stream(ctx context.Context, afterSeq int64, emit func(domain.DomainEvent) error) error {
	if afterSeq < 0 {
		return fmt.Errorf("%w: after_seq < 0", domain.ErrInvalidRequest)
	}
	if err := b.CheckCursor(ctx, afterSeq); err != nil {
		return err
	}

	b.mu.Lock()
	id, sub := b.subscribeLocked(afterSeq)
	b.mu.Unlock()
	defer b.unsubscribe(id)

	if afterSubscribeHook != nil {
		afterSubscribeHook()
	}

	// 注册后读取高水位 H：此后的实时事件一定 > H，之前的都在 [afterSeq, H] 内。
	high, err := b.store.LatestEventSeq(ctx)
	if err != nil {
		return fmt.Errorf("read latest event seq: %w", err)
	}
	if afterSeq > high {
		return fmt.Errorf("%w: cursor %d beyond latest %d", domain.ErrEventCursorExpired, afterSeq, high)
	}
	// 补读与实时消费必须并行：订阅缓冲只有 subscriberBuffer 深，
	// 若先同步补读完整段历史再进入消费循环，超过缓冲深度的存量会直接
	// 撑爆订阅并零帧断开，客户端只能在同一游标上反复重连。
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	backlogDone := make(chan error, 1)
	go func() { backlogDone <- b.replayBacklog(streamCtx, id, afterSeq, high) }()

	lastSent := afterSeq
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-backlogDone:
			backlogDone = nil
			if err != nil {
				return err
			}
		case ev, ok := <-sub.ch:
			if !ok {
				// 缓冲溢出：结束本次流，客户端按游标重连。
				return nil
			}
			if ev.Seq <= lastSent {
				continue
			}
			lastSent = ev.Seq
			if err := emit(ev); err != nil {
				return err
			}
		case ev, ok := <-sub.live:
			if !ok {
				return nil
			}
			if err := emit(ev); err != nil {
				return err
			}
		}
	}
}
