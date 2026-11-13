package store

import (
	"context"
	"fmt"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// CreateAudioCandidate 追加句级配音候选；候选文件不可变，不覆盖旧候选。
func (q *querier) CreateAudioCandidate(ctx context.Context, candidate domain.AudioCandidate) error {
	if candidate.ID == "" || candidate.ProjectID == "" || candidate.SegmentID == "" {
		return fmt.Errorf("%w: candidate id/project_id/segment_id required", domain.ErrInvalidRequest)
	}
	if candidate.Language == "" {
		return fmt.Errorf("%w: candidate language required", domain.ErrInvalidRequest)
	}
	if candidate.Revision < 1 {
		return fmt.Errorf("%w: candidate revision must be >= 1", domain.ErrInvalidRequest)
	}
	if candidate.ModelVersion == "" || candidate.AudioAssetID == "" {
		return fmt.Errorf("%w: candidate model_version/audio_asset_id required", domain.ErrInvalidRequest)
	}
	if candidate.CreatedAt.IsZero() {
		return fmt.Errorf("%w: candidate created_at required", domain.ErrInvalidRequest)
	}
	// 候选音频必须属于同一项目，避免跨项目资产引用。
	var assetProject string
	row := q.db.QueryRowContext(ctx, `SELECT project_id FROM assets WHERE id = ?`, candidate.AudioAssetID)
	if err := row.Scan(&assetProject); err != nil {
		return wrapNotFound(err)
	}
	if assetProject != candidate.ProjectID {
		return fmt.Errorf("%w: candidate asset %s belongs to project %s", domain.ErrInvalidRequest,
			candidate.AudioAssetID, assetProject)
	}
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO audio_candidates
			(id, project_id, segment_id, language, revision, model_version, audio_asset_id,
			 is_recommended, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		candidate.ID, candidate.ProjectID, candidate.SegmentID, candidate.Language,
		candidate.Revision, candidate.ModelVersion, candidate.AudioAssetID,
		boolInt(candidate.IsRecommended), formatTime(candidate.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert audio candidate: %w", err)
	}
	return nil
}

// ListAudioCandidates 返回句段某语言的候选；revision 非空时只看该句级版本。
func (q *querier) ListAudioCandidates(ctx context.Context, segmentID domain.ID, language string, revision *int) ([]domain.AudioCandidate, error) {
	query := `
		SELECT id, project_id, segment_id, language, revision, model_version, audio_asset_id,
			is_recommended, created_at
		FROM audio_candidates WHERE segment_id = ? AND language = ?`
	args := []any{segmentID, language}
	if revision != nil {
		query += ` AND revision = ?`
		args = append(args, *revision)
	}
	query += ` ORDER BY created_at, id`

	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query audio candidates: %w", err)
	}
	defer rows.Close()

	candidates := make([]domain.AudioCandidate, 0)
	for rows.Next() {
		var (
			c         domain.AudioCandidate
			recommend int
			created   string
		)
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.SegmentID, &c.Language, &c.Revision,
			&c.ModelVersion, &c.AudioAssetID, &recommend, &created); err != nil {
			return nil, fmt.Errorf("scan audio candidate: %w", err)
		}
		c.IsRecommended = recommend != 0
		if c.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audio candidates: %w", err)
	}
	return candidates, nil
}

// GetCandidateSelection 返回句段某语言当前选用的候选。
func (q *querier) GetCandidateSelection(ctx context.Context, segmentID domain.ID, language string) (domain.CandidateSelection, error) {
	var (
		sel      domain.CandidateSelection
		selected string
	)
	row := q.db.QueryRowContext(ctx, `
		SELECT segment_id, language, candidate_id, revision, selected_at
		FROM candidate_selections WHERE segment_id = ? AND language = ?`, segmentID, language)
	if err := row.Scan(&sel.SegmentID, &sel.Language, &sel.CandidateID, &sel.Revision, &selected); err != nil {
		return domain.CandidateSelection{}, wrapNotFound(err)
	}
	var err error
	if sel.SelectedAt, err = parseTime(selected); err != nil {
		return domain.CandidateSelection{}, err
	}
	return sel, nil
}

// SaveCandidateSelection 登记选用关系。
//
// 候选必须存在且属于当前句段 revision 与语言，否则返回 ErrRevisionConflict；
// 同一句段语言的重复选用是 upsert（保留单一有效选择）。
func (tx *Tx) SaveCandidateSelection(ctx context.Context, sel domain.CandidateSelection) error {
	if sel.SegmentID == "" || sel.Language == "" || sel.CandidateID == "" {
		return fmt.Errorf("%w: selection segment_id/language/candidate_id required", domain.ErrInvalidRequest)
	}
	if sel.Revision < 1 {
		return fmt.Errorf("%w: selection revision must be >= 1", domain.ErrInvalidRequest)
	}
	if sel.SelectedAt.IsZero() {
		return fmt.Errorf("%w: selection selected_at required", domain.ErrInvalidRequest)
	}
	var (
		candidateSegment  string
		candidateLanguage string
		candidateRevision int
	)
	row := tx.db.QueryRowContext(ctx, `
		SELECT segment_id, language, revision FROM audio_candidates WHERE id = ?`, sel.CandidateID)
	if err := row.Scan(&candidateSegment, &candidateLanguage, &candidateRevision); err != nil {
		return wrapNotFound(err)
	}
	if candidateSegment != sel.SegmentID || candidateLanguage != sel.Language || candidateRevision != sel.Revision {
		return fmt.Errorf("%w: candidate %s does not match segment %s/%s revision %d",
			domain.ErrRevisionConflict, sel.CandidateID, sel.SegmentID, sel.Language, sel.Revision)
	}
	_, err := tx.db.ExecContext(ctx, `
		INSERT INTO candidate_selections (segment_id, language, candidate_id, revision, selected_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (segment_id, language) DO UPDATE SET
			candidate_id = excluded.candidate_id,
			revision = excluded.revision,
			selected_at = excluded.selected_at`,
		sel.SegmentID, sel.Language, sel.CandidateID, sel.Revision, formatTime(sel.SelectedAt),
	)
	if err != nil {
		return fmt.Errorf("save candidate selection: %w", err)
	}
	return nil
}

// Store 包装：在独立事务中保存选用关系。
func (s *Store) SaveCandidateSelection(ctx context.Context, sel domain.CandidateSelection) error {
	return s.DoTx(ctx, func(tx *Tx) error {
		return tx.SaveCandidateSelection(ctx, sel)
	})
}
