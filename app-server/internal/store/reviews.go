package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// CreateReviewGate 登记待审核门槛（首次创建为 pending）。
func (q *querier) CreateReviewGate(ctx context.Context, gate domain.ReviewGate) error {
	if gate.ID == "" || gate.RunID == "" {
		return fmt.Errorf("%w: review id/run_id required", domain.ErrInvalidRequest)
	}
	if !gate.Type.Valid() {
		return fmt.Errorf("%w: invalid review type %q", domain.ErrInvalidRequest, gate.Type)
	}
	if !gate.Status.Valid() {
		return fmt.Errorf("%w: invalid review status %q", domain.ErrInvalidRequest, gate.Status)
	}
	if gate.Status != domain.ReviewStatusPending {
		return fmt.Errorf("%w: review must start pending", domain.ErrInvalidRequest)
	}
	if gate.RevisionDigest == "" {
		return fmt.Errorf("%w: review revision_digest required", domain.ErrInvalidRequest)
	}
	if gate.ConfirmedAt != nil {
		return fmt.Errorf("%w: pending review cannot have confirmed_at", domain.ErrInvalidRequest)
	}
	var createdAt any
	if gate.CreatedAt != nil {
		createdAt = formatTime(*gate.CreatedAt)
	}
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO review_gates (id, run_id, type, language, revision_digest, status, confirmed_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, NULL, ?)`,
		gate.ID, gate.RunID, string(gate.Type), nullString(gate.Language),
		gate.RevisionDigest, string(gate.Status), createdAt,
	)
	if err != nil {
		return fmt.Errorf("insert review gate: %w", err)
	}
	return nil
}

// GetReviewGate 读取审核门槛。
func (q *querier) GetReviewGate(ctx context.Context, id domain.ID) (domain.ReviewGate, error) {
	return q.getReviewGate(ctx, id)
}

func (q *querier) getReviewGate(ctx context.Context, id domain.ID) (domain.ReviewGate, error) {
	var (
		gate        domain.ReviewGate
		reviewType  string
		language    sql.NullString
		status      string
		confirmedAt sql.NullString
		createdAt   sql.NullString
	)
	row := q.db.QueryRowContext(ctx, `
		SELECT id, run_id, type, language, revision_digest, status, confirmed_at, created_at
		FROM review_gates WHERE id = ?`, id)
	if err := row.Scan(&gate.ID, &gate.RunID, &reviewType, &language,
		&gate.RevisionDigest, &status, &confirmedAt, &createdAt); err != nil {
		return domain.ReviewGate{}, wrapNotFound(err)
	}
	gate.Type = domain.ReviewType(reviewType)
	gate.Status = domain.ReviewStatus(status)
	if !gate.Type.Valid() {
		return domain.ReviewGate{}, fmt.Errorf("review %s has invalid type %q", gate.ID, reviewType)
	}
	if !gate.Status.Valid() {
		return domain.ReviewGate{}, fmt.Errorf("review %s has invalid status %q", gate.ID, status)
	}
	if language.Valid {
		gate.Language = language.String
	}
	var err error
	if gate.ConfirmedAt, err = parseNullTime(confirmedAt); err != nil {
		return domain.ReviewGate{}, err
	}
	if gate.CreatedAt, err = parseNullTime(createdAt); err != nil {
		return domain.ReviewGate{}, err
	}
	return gate, nil
}

// ListRunReviews 列出 Run 的审核门槛。
func (q *querier) ListRunReviews(ctx context.Context, runID domain.ID) ([]domain.ReviewGate, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT id FROM review_gates WHERE run_id = ? ORDER BY type, language, id`, runID)
	if err != nil {
		return nil, fmt.Errorf("query run reviews: %w", err)
	}
	defer rows.Close()

	var ids []domain.ID
	for rows.Next() {
		var id domain.ID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan review id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate review ids: %w", err)
	}
	// 连接池上限为 1：先释放 rows 再逐条读取。
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close review ids: %w", err)
	}

	gates := make([]domain.ReviewGate, 0, len(ids))
	for _, id := range ids {
		gate, err := q.getReviewGate(ctx, id)
		if err != nil {
			return nil, err
		}
		gates = append(gates, gate)
	}
	return gates, nil
}

// ListRunDeliveryReviews 返回 Run 的全部交付审核门槛，按创建顺序（最新在末尾）。
//
// 交付导出门禁要同时看 approved 与 pending 门槛：approved 必须绑定当前摘要，
// 而绑定当前摘要的 pending 门槛表示编辑/重新混音后尚未放行，必须继续拒绝。
// review_gates 的 created_at 是新增可选列；旧记录为 NULL，仍按 rowid 排序，
// 保持迁移前的创建顺序语义。
func (q *querier) ListRunDeliveryReviews(ctx context.Context, runID domain.ID) ([]domain.ReviewGate, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT id FROM review_gates
		WHERE run_id = ? AND type = ?
		ORDER BY rowid`, runID, string(domain.ReviewTypeDelivery))
	if err != nil {
		return nil, fmt.Errorf("query run delivery reviews: %w", err)
	}
	defer rows.Close()

	var ids []domain.ID
	for rows.Next() {
		var id domain.ID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan delivery review id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate delivery review ids: %w", err)
	}
	// 连接池上限为 1：先释放 rows 再逐条读取。
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close delivery review ids: %w", err)
	}

	gates := make([]domain.ReviewGate, 0, len(ids))
	for _, id := range ids {
		gate, err := q.getReviewGate(ctx, id)
		if err != nil {
			return nil, err
		}
		gates = append(gates, gate)
	}
	return gates, nil
}

// ApproveReview 人工放行：Run 必须仍允许放行，revision_digest 不一致返回 REVISION_CONFLICT。
//
// 派发闸门（docs/local-api-contract.md §8）：终止 Run（SUCCEEDED/PARTIAL/FAILED/
// CANCELED）拒绝审核放行；PAUSED 允许保存审核记录，但后续任务只登记不派发。
// 更新语句同时以 status=pending 与 revision_digest 为条件，防止审核与并发
// 编辑/暂停交错时旧审核放行新内容。
func (tx *Tx) ApproveReview(ctx context.Context, id domain.ID, revisionDigest string, typ domain.ReviewType, now time.Time) (domain.ReviewGate, error) {
	gate, err := tx.getReviewGate(ctx, id)
	if err != nil {
		return domain.ReviewGate{}, err
	}
	var runStatus string
	row := tx.db.QueryRowContext(ctx, `SELECT status FROM workflow_runs WHERE id = ?`, gate.RunID)
	if err := row.Scan(&runStatus); err != nil {
		return domain.ReviewGate{}, wrapNotFound(err)
	}
	status := domain.RunStatus(runStatus)
	if !status.Valid() {
		return domain.ReviewGate{}, fmt.Errorf("review %s run %s has invalid status %q", id, gate.RunID, runStatus)
	}
	if !domain.RunAllowsReviewApproval(status) {
		return domain.ReviewGate{}, fmt.Errorf("%w: run %s is %s; review approval rejected",
			domain.ErrInvalidTransition, gate.RunID, status)
	}
	approved, err := domain.ApproveReviewGate(gate, revisionDigest, typ, now)
	if err != nil {
		return domain.ReviewGate{}, err
	}
	res, err := tx.db.ExecContext(ctx, `
		UPDATE review_gates
		SET status = ?, confirmed_at = ?
		WHERE id = ? AND status = ? AND revision_digest = ?`,
		string(domain.ReviewStatusApproved), formatTime(*approved.ConfirmedAt),
		id, string(domain.ReviewStatusPending), revisionDigest)
	if err != nil {
		return domain.ReviewGate{}, fmt.Errorf("approve review: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.ReviewGate{}, fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		// 事务内比较后仍失败说明被并发修改：重新读取给出准确语义。
		current, readErr := tx.getReviewGate(ctx, id)
		if readErr != nil {
			return domain.ReviewGate{}, readErr
		}
		if current.RevisionDigest != revisionDigest {
			return domain.ReviewGate{}, fmt.Errorf("%w: review %s revision changed", domain.ErrRevisionConflict, id)
		}
		return domain.ReviewGate{}, fmt.Errorf("%w: review %s is %s", domain.ErrInvalidTransition, id, current.Status)
	}
	return approved, nil
}

// Store 包装：在独立事务中放行审核。
func (s *Store) ApproveReview(ctx context.Context, id domain.ID, revisionDigest string, typ domain.ReviewType, now time.Time) (domain.ReviewGate, error) {
	var gate domain.ReviewGate
	err := s.DoTx(ctx, func(tx *Tx) error {
		approved, err := tx.ApproveReview(ctx, id, revisionDigest, typ, now)
		if err != nil {
			return err
		}
		gate = approved
		return nil
	})
	if err != nil {
		return domain.ReviewGate{}, err
	}
	return gate, nil
}
