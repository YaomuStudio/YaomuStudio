package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// Receipt 是持久化的幂等回执。
//
// 幂等范围为 workspace + operation + key：相同请求摘要返回原回执，
// 不同摘要返回 IDEMPOTENCY_CONFLICT。key 与回执至少保存到任务/回执保留期。
type Receipt struct {
	WorkspaceID   string
	Operation     string
	Key           string
	RequestDigest string
	ResponseJSON  []byte
	CreatedAt     time.Time
	ExpiresAt     *time.Time
}

// GetReceipt 读取幂等回执；不存在返回 NOT_FOUND。
func (q *querier) GetReceipt(ctx context.Context, workspaceID, operation, key string) (Receipt, error) {
	var (
		r        Receipt
		response string
		created  string
		expires  sql.NullString
	)
	row := q.db.QueryRowContext(ctx, `
		SELECT workspace_id, operation, idempotency_key, request_digest, response_json,
			created_at, expires_at
		FROM receipts
		WHERE workspace_id = ? AND operation = ? AND idempotency_key = ?`,
		workspaceID, operation, key)
	if err := row.Scan(&r.WorkspaceID, &r.Operation, &r.Key, &r.RequestDigest,
		&response, &created, &expires); err != nil {
		return Receipt{}, wrapNotFound(err)
	}
	r.ResponseJSON = []byte(response)
	var err error
	if r.CreatedAt, err = parseTime(created); err != nil {
		return Receipt{}, err
	}
	if r.ExpiresAt, err = parseNullTime(expires); err != nil {
		return Receipt{}, err
	}
	return r, nil
}

// PutReceipt 写入幂等回执并返回是否本次新建。
//
// 已存在且请求摘要相同：返回原回执（created=false），调用方应原样返回原响应；
// 已存在但摘要不同：返回 ErrIdempotencyConflict。
func (q *querier) PutReceipt(ctx context.Context, r Receipt) (Receipt, bool, error) {
	if r.WorkspaceID == "" || r.Operation == "" || r.Key == "" {
		return Receipt{}, false, fmt.Errorf("%w: receipt workspace_id/operation/key required", domain.ErrInvalidRequest)
	}
	if r.RequestDigest == "" {
		return Receipt{}, false, fmt.Errorf("%w: receipt request_digest required", domain.ErrInvalidRequest)
	}
	if r.CreatedAt.IsZero() {
		return Receipt{}, false, fmt.Errorf("%w: receipt created_at required", domain.ErrInvalidRequest)
	}
	res, err := q.db.ExecContext(ctx, `
		INSERT INTO receipts
			(workspace_id, operation, idempotency_key, request_digest, response_json, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, operation, idempotency_key) DO NOTHING`,
		r.WorkspaceID, r.Operation, r.Key, r.RequestDigest, string(r.ResponseJSON),
		formatTime(r.CreatedAt), nullTimeValue(r.ExpiresAt),
	)
	if err != nil {
		return Receipt{}, false, fmt.Errorf("insert receipt: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Receipt{}, false, fmt.Errorf("rows affected: %w", err)
	}
	if n == 1 {
		return r, true, nil
	}
	existing, err := q.GetReceipt(ctx, r.WorkspaceID, r.Operation, r.Key)
	if err != nil {
		return Receipt{}, false, err
	}
	if existing.RequestDigest != r.RequestDigest {
		return Receipt{}, false, fmt.Errorf("%w: key %q reused with a different request", domain.ErrIdempotencyConflict, r.Key)
	}
	return existing, false, nil
}

// DeleteReceiptsBefore 清理保留期外的回执，返回删除行数。
func (q *querier) DeleteReceiptsBefore(ctx context.Context, before time.Time) (int64, error) {
	res, err := q.db.ExecContext(ctx, `DELETE FROM receipts WHERE created_at < ?`, formatTime(before))
	if err != nil {
		return 0, fmt.Errorf("delete receipts: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}
