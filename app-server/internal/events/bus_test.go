package events

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func newBus(t *testing.T) (*Bus, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "yaomu.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logger, err := logging.New(logging.Options{Path: filepath.Join(t.TempDir(), "core.log")})
	if err != nil {
		t.Fatalf("open logger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	return NewBus(st, logger), st
}

func appendEvent(t *testing.T, st *store.Store, typ string) domain.DomainEvent {
	t.Helper()
	ev := domain.DomainEvent{Type: typ, ProjectID: "p1", Data: map[string]any{"k": typ}}
	err := st.DoTx(context.Background(), func(tx *store.Tx) error {
		seq, err := tx.AppendEvent(context.Background(), ev, time.Now().UTC())
		if err != nil {
			return err
		}
		ev.Seq = seq
		return nil
	})
	if err != nil {
		t.Fatalf("append event: %v", err)
	}
	return ev
}

func TestStreamReplaysThenDeliversLive(t *testing.T) {
	bus, st := newBus(t)
	first := appendEvent(t, st, "task.created")
	second := appendEvent(t, st, "task.queued")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan domain.DomainEvent, 8)
	done := make(chan error, 1)
	go func() {
		done <- bus.Stream(ctx, 0, func(ev domain.DomainEvent) error {
			received <- ev
			return nil
		})
	}()

	// 补读两条历史。
	for i := 0; i < 2; i++ {
		select {
		case ev := <-received:
			if ev.Seq != first.Seq && ev.Seq != second.Seq {
				t.Fatalf("unexpected replay seq %d", ev.Seq)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for replay")
		}
	}

	// 实时发布。
	third := appendEvent(t, st, "task.succeeded")
	bus.Publish(third)
	select {
	case ev := <-received:
		if ev.Seq != third.Seq {
			t.Fatalf("live seq = %d, want %d", ev.Seq, third.Seq)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for live event")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Stream() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not stop on cancel")
	}
}

// TestStreamReplaysBacklogLargerThanSubscriberBuffer 覆盖补读历史超过订阅缓冲深度的场景：
// 补读不能因为缓冲放满就提前结束，也不能零帧断开让客户端在同一游标上反复重连。
func TestStreamReplaysBacklogLargerThanSubscriberBuffer(t *testing.T) {
	bus, st := newBus(t)
	total := subscriberBuffer + 100
	for i := 0; i < total; i++ {
		appendEvent(t, st, "task.queued")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan domain.DomainEvent, total)
	done := make(chan error, 1)
	go func() {
		done <- bus.Stream(ctx, 0, func(ev domain.DomainEvent) error {
			received <- ev
			return nil
		})
	}()

	for i := 0; i < total; i++ {
		select {
		case ev := <-received:
			if ev.Seq != int64(i+1) {
				t.Fatalf("replay seq = %d, want %d", ev.Seq, i+1)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timeout after %d/%d events", i, total)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Stream() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not stop on cancel")
	}
}

func TestStreamCursorExpired(t *testing.T) {
	bus, st := newBus(t)
	for i := 0; i < 5; i++ {
		appendEvent(t, st, "task.queued")
	}
	if _, err := st.PruneEventsBefore(context.Background(), 4); err != nil {
		t.Fatalf("prune: %v", err)
	}
	err := bus.Stream(context.Background(), 0, func(domain.DomainEvent) error { return nil })
	if err == nil {
		t.Fatal("Stream() error = nil, want EVENT_CURSOR_EXPIRED")
	}
	code, ok := domain.CodeOf(err)
	if !ok || code != domain.ErrorCodeEventCursorExpired {
		t.Fatalf("error code = %v, want EVENT_CURSOR_EXPIRED", err)
	}
}

func TestPublishConcurrentWithSubscribe(t *testing.T) {
	bus, st := newBus(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var seen int64
		_ = bus.Stream(ctx, 0, func(ev domain.DomainEvent) error {
			if ev.Seq <= seen {
				t.Errorf("duplicate event seq %d", ev.Seq)
			}
			seen = ev.Seq
			return nil
		})
	}()

	for i := 0; i < 50; i++ {
		ev := appendEvent(t, st, "task.progress")
		bus.Publish(ev)
	}
	cancel()
	wg.Wait()
}

func TestPublishDeliversInSeqOrder(t *testing.T) {
	bus, st := newBus(t)
	appendEvent(t, st, "task.created")
	appendEvent(t, st, "task.queued")
	appendEvent(t, st, "task.started")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan int64, 4)
	go func() {
		_ = bus.Stream(ctx, 3, func(ev domain.DomainEvent) error {
			received <- ev.Seq
			return nil
		})
	}()
	// 订阅握手完成后先发布高 seq，再发布缺口；必须按序交付。
	time.Sleep(100 * time.Millisecond)
	bus.Publish(domain.DomainEvent{Seq: 5, Type: "task.progress"})
	bus.Publish(domain.DomainEvent{Seq: 4, Type: "task.progress"})
	for _, want := range []int64{4, 5} {
		select {
		case got := <-received:
			if got != want {
				t.Fatalf("delivery order = %d, want %d", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for seq %d", want)
		}
	}
}

func TestCursorBeyondLatestIsExpired(t *testing.T) {
	bus, st := newBus(t)
	appendEvent(t, st, "task.created")
	err := bus.Stream(context.Background(), 99, func(domain.DomainEvent) error { return nil })
	if err == nil {
		t.Fatal("Stream() error = nil, want EVENT_CURSOR_EXPIRED")
	}
	if code, _ := domain.CodeOf(err); code != domain.ErrorCodeEventCursorExpired {
		t.Fatalf("code = %s", code)
	}
}

// TestSlowLiveSubscriberDoesNotBlockCriticalEvents 守护可丢弃实时反馈与持久事件的
// 隔离：订阅者卡在进度帧上时 PublishLive 只丢本条，不阻塞关键事件、不关闭订阅，
// 也不产生任何持久 seq。
func TestSlowLiveSubscriberDoesNotBlockCriticalEvents(t *testing.T) {
	bus, st := newBus(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gate := make(chan struct{})
	received := make(chan domain.DomainEvent, subscriberBuffer+64)
	go func() {
		_ = bus.Stream(ctx, 0, func(ev domain.DomainEvent) error {
			if ev.Seq == 0 {
				select {
				case <-gate: // 模拟慢客户端：卡在实时反馈上。
				case <-ctx.Done():
					return nil
				}
			}
			select {
			case received <- ev:
				return nil
			case <-ctx.Done():
				return nil
			}
		})
	}()
	// 持久屏障确认订阅已注册/补读完成，替代固定等待。
	barrier := appendEvent(t, st, "test.barrier")
	bus.Publish(barrier)
	barrierDeadline := time.After(3 * time.Second)
	for observed := false; !observed; {
		select {
		case ev := <-received:
			if ev.Seq == barrier.Seq && ev.Type == barrier.Type {
				observed = true
			}
		case <-barrierDeadline:
			t.Fatal("slow-live subscription barrier not observed")
		}
	}

	// 超过 live 缓冲深度：多余的进度按契约丢弃，不关闭订阅。
	for i := 0; i < subscriberBuffer+50; i++ {
		bus.PublishLive(domain.DomainEvent{
			Type:   "task.progress",
			TaskID: "task-1",
			Data:   map[string]any{"progress": i},
		})
	}
	published := make(chan struct{})
	go func() {
		bus.PublishLive(domain.DomainEvent{Type: "task.progress", TaskID: "task-1"})
		close(published)
	}()
	select {
	case <-published:
	case <-time.After(2 * time.Second):
		t.Fatal("PublishLive blocked on a slow subscriber")
	}

	critical := appendEvent(t, st, "task.failed")
	bus.Publish(critical)

	close(gate)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-received:
			if ev.Seq == critical.Seq {
				goto delivered
			}
		case <-deadline:
			t.Fatal("critical event not delivered after slow live consumer")
		}
	}

delivered:
	persisted, err := st.EventsAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	if len(persisted) != 2 ||
		persisted[0].Seq != barrier.Seq || persisted[0].Type != "test.barrier" ||
		persisted[1].Seq != critical.Seq || persisted[1].Type != "task.failed" {
		t.Fatalf("persisted events = %+v, want barrier then critical only", persisted)
	}
	for _, ev := range persisted {
		if ev.Type == "task.progress" || ev.Seq <= 0 {
			t.Fatalf("live progress persisted: %+v", ev)
		}
	}
	latest, err := st.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("LatestEventSeq: %v", err)
	}
	if latest != critical.Seq {
		t.Fatalf("latest seq = %d, want %d (live must not advance the cursor)", latest, critical.Seq)
	}
}

// TestStreamGapAfterSubscribeIsExpired 覆盖“订阅注册后、补读前历史被裁剪”的竞态：
// 缺口无法补齐时必须显式返回 EVENT_CURSOR_EXPIRED，不能静默等待缺口。
func TestStreamGapAfterSubscribeIsExpired(t *testing.T) {
	bus, st := newBus(t)
	for i := 0; i < 5; i++ {
		appendEvent(t, st, "task.queued")
	}
	afterSubscribeHook = func() {
		// 注册订阅后立刻裁剪掉游标之后的前半段历史，制造 seq 缺口。
		if _, err := st.PruneEventsBefore(context.Background(), 5); err != nil {
			t.Errorf("prune: %v", err)
		}
	}
	t.Cleanup(func() { afterSubscribeHook = nil })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := bus.Stream(ctx, 3, func(domain.DomainEvent) error { return nil })
	code, ok := domain.CodeOf(err)
	if !ok || code != domain.ErrorCodeEventCursorExpired {
		t.Fatalf("Stream() error = %v, want EVENT_CURSOR_EXPIRED", err)
	}
}
