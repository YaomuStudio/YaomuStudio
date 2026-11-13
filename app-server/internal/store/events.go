package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// AppendEvent 追加一条工作区级持久事件并返回分配到的 seq。
//
// seq 由 SQLite AUTOINCREMENT 生成：重启不归零、不复用被清理的序号。
// 状态与对应事件应通过 DoTx 在同一事务提交，提交成功后才对外发布。
func (q *querier) AppendEvent(ctx context.Context, event domain.DomainEvent, now time.Time) (int64, error) {
	if event.Type == "" {
		return 0, fmt.Errorf("%w: event type required", domain.ErrInvalidRequest)
	}
	schemaVersion := event.SchemaVersion
	if schemaVersion == 0 {
		schemaVersion = 1
	}
	if schemaVersion != 1 {
		return 0, fmt.Errorf("%w: unsupported event schema version %d", domain.ErrInvalidRequest, schemaVersion)
	}
	if now.IsZero() {
		now = time.Now()
	}
	data := event.Data
	if data == nil {
		data = map[string]any{}
	}
	encoded, err := marshalJSON(data)
	if err != nil {
		return 0, err
	}
	res, err := q.db.ExecContext(ctx, `
		INSERT INTO events (schema_version, type, project_id, task_id, state_version, data_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		schemaVersion, event.Type, nullString(event.ProjectID), nullString(event.TaskID),
		event.StateVersion, encoded, formatTime(now),
	)
	if err != nil {
		return 0, fmt.Errorf("insert event: %w", err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("event last insert id: %w", err)
	}
	return seq, nil
}

// EventsAfter 返回 seq > afterSeq 的事件（按 seq 升序；limit 有上限）。
func (q *querier) EventsAfter(ctx context.Context, afterSeq int64, limit int) ([]domain.DomainEvent, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT seq, schema_version, type, project_id, task_id, state_version, data_json
		FROM events WHERE seq > ? ORDER BY seq LIMIT ?`, afterSeq, validatePageSize(limit))
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	events := make([]domain.DomainEvent, 0)
	for rows.Next() {
		var (
			ev           domain.DomainEvent
			projectID    sql.NullString
			taskID       sql.NullString
			stateVersion sql.NullInt64
			dataJSON     string
		)
		if err := rows.Scan(&ev.Seq, &ev.SchemaVersion, &ev.Type, &projectID, &taskID,
			&stateVersion, &dataJSON); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if projectID.Valid {
			ev.ProjectID = projectID.String
		}
		if taskID.Valid {
			ev.TaskID = taskID.String
		}
		if stateVersion.Valid {
			ev.StateVersion = stateVersion.Int64
		}
		if err := unmarshalNull(sql.NullString{String: dataJSON, Valid: true}, &ev.Data); err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return events, nil
}

// LatestEventSeq 返回当前最高 seq；无事件时为 0。
func (q *querier) LatestEventSeq(ctx context.Context) (int64, error) {
	var seq int64
	if err := q.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("query latest event seq: %w", err)
	}
	return seq, nil
}

// EarliestEventSeq 返回仍可回放的最早 seq；无事件时为 0。
//
// 客户端若请求 afterSeq 低于该值，必须重新获取快照（EVENT_CURSOR_EXPIRED），
// 不能静默宣称追赶完成。
func (q *querier) EarliestEventSeq(ctx context.Context) (int64, error) {
	var seq int64
	if err := q.db.QueryRowContext(ctx, `SELECT COALESCE(MIN(seq), 0) FROM events`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("query earliest event seq: %w", err)
	}
	return seq, nil
}

// PruneEventsBefore 按保留策略删除 seq < before 的历史事件，返回删除行数。
// 清理后 AUTOINCREMENT 仍不会复用编号。
func (q *querier) PruneEventsBefore(ctx context.Context, seq int64) (int64, error) {
	res, err := q.db.ExecContext(ctx, `DELETE FROM events WHERE seq < ?`, seq)
	if err != nil {
		return 0, fmt.Errorf("prune events: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}
