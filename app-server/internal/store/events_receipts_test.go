package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func TestEventSequenceAndPruning(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	var seqs []int64
	for i := 1; i <= 3; i++ {
		seq, err := s.AppendEvent(ctx, domain.DomainEvent{
			Type:         "task.created",
			ProjectID:    "project-1",
			TaskID:       "task-1",
			StateVersion: int64(i),
			Data:         map[string]any{"n": i},
		}, testTime.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
		seqs = append(seqs, seq)
	}
	if seqs[0] != 1 || seqs[1] != 2 || seqs[2] != 3 {
		t.Fatalf("event seqs = %v, want 1,2,3", seqs)
	}
	latest, err := s.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("LatestEventSeq() error = %v", err)
	}
	if latest != 3 {
		t.Errorf("latest seq = %d, want 3", latest)
	}
	earliest, err := s.EarliestEventSeq(ctx)
	if err != nil {
		t.Fatalf("EarliestEventSeq() error = %v", err)
	}
	if earliest != 1 {
		t.Errorf("earliest seq = %d, want 1", earliest)
	}

	events, err := s.EventsAfter(ctx, 1, 10)
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	if len(events) != 2 || events[0].Seq != 2 || events[1].Seq != 3 {
		t.Fatalf("events after 1 = %+v", events)
	}
	if events[0].ProjectID != "project-1" || events[0].TaskID != "task-1" ||
		events[0].StateVersion != 2 || events[0].SchemaVersion != 1 {
		t.Errorf("event envelope = %+v", events[0])
	}
	if got := events[0].Data["n"]; got != float64(2) {
		t.Errorf("event data n = %v, want 2", got)
	}

	// 保留策略：删除 seq < 3 的历史，游标缺口由调用方映射 EVENT_CURSOR_EXPIRED。
	deleted, err := s.PruneEventsBefore(ctx, 3)
	if err != nil {
		t.Fatalf("PruneEventsBefore() error = %v", err)
	}
	if deleted != 2 {
		t.Errorf("pruned = %d, want 2", deleted)
	}
	earliest, err = s.EarliestEventSeq(ctx)
	if err != nil {
		t.Fatalf("EarliestEventSeq() error = %v", err)
	}
	if earliest != 3 {
		t.Errorf("earliest seq after prune = %d, want 3", earliest)
	}
	events, err = s.EventsAfter(ctx, 0, 10)
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	if len(events) != 1 || events[0].Seq != 3 {
		t.Fatalf("events after prune = %+v", events)
	}

	// AUTOINCREMENT：被清理的编号不会复用。
	next, err := s.AppendEvent(ctx, domain.DomainEvent{Type: "task.succeeded", Data: map[string]any{}}, testTime)
	if err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if next != 4 {
		t.Errorf("next seq = %d, want 4 (no reuse)", next)
	}

	if _, err := s.AppendEvent(ctx, domain.DomainEvent{Type: "", Data: map[string]any{}}, testTime); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Errorf("empty event type error = %v, want ErrInvalidRequest", err)
	}
	if _, err := s.AppendEvent(ctx, domain.DomainEvent{Type: "x", SchemaVersion: 2}, testTime); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Errorf("unsupported schema version error = %v, want ErrInvalidRequest", err)
	}
}

func TestEventSequencePersistsAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "yaomu.db")
	ctx := context.Background()

	first, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	for i := 1; i <= 2; i++ {
		if _, err := first.AppendEvent(ctx, domain.DomainEvent{Type: "task.created"}, testTime); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer second.Close()
	latest, err := second.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("LatestEventSeq() error = %v", err)
	}
	if latest != 2 {
		t.Errorf("latest seq after reopen = %d, want 2", latest)
	}
	next, err := second.AppendEvent(ctx, domain.DomainEvent{Type: "task.started"}, testTime)
	if err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if next != 3 {
		t.Errorf("seq after reopen = %d, want 3 (restart must not reset)", next)
	}
}

func TestReceiptRetentionChronologicalOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// 同一秒内零纳秒与半秒：时间文本必须固定宽度，否则 "…00Z" 会排到 "…00.5Z" 之后。
	times := []time.Time{testTime, testTime.Add(500 * time.Millisecond)}
	for i, at := range times {
		if _, _, err := s.PutReceipt(ctx, Receipt{
			WorkspaceID:   "ws-1",
			Operation:     "op",
			Key:           "key-" + string(rune('a'+i)),
			RequestDigest: "digest",
			ResponseJSON:  []byte(`{}`),
			CreatedAt:     at,
		}); err != nil {
			t.Fatalf("PutReceipt() error = %v", err)
		}
	}
	deleted, err := s.DeleteReceiptsBefore(ctx, testTime.Add(250*time.Millisecond))
	if err != nil {
		t.Fatalf("DeleteReceiptsBefore() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if _, err := s.GetReceipt(ctx, "ws-1", "op", "key-a"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("earlier receipt error = %v, want ErrNotFound", err)
	}
	if _, err := s.GetReceipt(ctx, "ws-1", "op", "key-b"); err != nil {
		t.Errorf("later receipt was pruned: %v", err)
	}
}

func TestReceiptIdempotency(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	receipt := Receipt{
		WorkspaceID:   "ws-1",
		Operation:     "POST /api/v1/tasks",
		Key:           "idem-key-1",
		RequestDigest: "digest-1",
		ResponseJSON:  []byte(`{"task_id":"task-1"}`),
		CreatedAt:     testTime,
	}
	stored, created, err := s.PutReceipt(ctx, receipt)
	if err != nil {
		t.Fatalf("PutReceipt() error = %v", err)
	}
	if !created || string(stored.ResponseJSON) != `{"task_id":"task-1"}` {
		t.Fatalf("stored receipt = %+v created=%v", stored, created)
	}

	// 相同摘要返回原回执，不覆盖。
	receipt.ResponseJSON = []byte(`{"task_id":"different"}`)
	replayed, created, err := s.PutReceipt(ctx, receipt)
	if err != nil {
		t.Fatalf("PutReceipt(replay) error = %v", err)
	}
	if created {
		t.Error("replay created = true, want false")
	}
	if string(replayed.ResponseJSON) != `{"task_id":"task-1"}` {
		t.Errorf("replay response = %s, want original receipt", replayed.ResponseJSON)
	}

	// 不同摘要返回 IDEMPOTENCY_CONFLICT。
	conflict := receipt
	conflict.RequestDigest = "digest-2"
	if _, _, err := s.PutReceipt(ctx, conflict); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Errorf("digest conflict error = %v, want ErrIdempotencyConflict", err)
	}

	got, err := s.GetReceipt(ctx, "ws-1", "POST /api/v1/tasks", "idem-key-1")
	if err != nil {
		t.Fatalf("GetReceipt() error = %v", err)
	}
	if got.RequestDigest != "digest-1" || !got.CreatedAt.Equal(testTime) {
		t.Errorf("receipt = %+v", got)
	}
	if _, err := s.GetReceipt(ctx, "ws-1", "POST /api/v1/tasks", "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing receipt error = %v, want ErrNotFound", err)
	}
	if _, _, err := s.PutReceipt(ctx, Receipt{WorkspaceID: "ws-1"}); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Errorf("invalid receipt error = %v, want ErrInvalidRequest", err)
	}

	deleted, err := s.DeleteReceiptsBefore(ctx, testTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("DeleteReceiptsBefore() error = %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted receipts = %d, want 1", deleted)
	}
	if _, err := s.GetReceipt(ctx, "ws-1", "POST /api/v1/tasks", "idem-key-1"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("receipt after prune error = %v, want ErrNotFound", err)
	}
}
