package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// RevisionSave 是保存一次新字幕 revision 的输入。
//
// BaseRevisionID 走乐观并发检查；Segments 中已有 ID 视为稳定句段，
// 省略 ID 表示拆分/合并产生的新句段（由 Core 分配新 ID 并保留 Lineage）。
type RevisionSave struct {
	RevisionID       domain.ID
	ParentRevisionID domain.ID
	InputRevisionID  domain.ID
	AuthorType       domain.AuthorType
	Segments         []domain.SegmentInput
	Style            *domain.SubtitleStyle
	EraseBlocks      []domain.EraseBlock
}

// CreateSubtitleTrack 创建字幕轨及其初始 revision。
func (q *querier) CreateSubtitleTrack(ctx context.Context, track domain.SubtitleTrack) error {
	return q.createSubtitleTrack(ctx, track)
}

func (q *querier) createSubtitleTrack(ctx context.Context, track domain.SubtitleTrack) error {
	if track.ID == "" || track.ProjectID == "" || track.Language == "" {
		return fmt.Errorf("%w: track id/project_id/language required", domain.ErrInvalidRequest)
	}
	if !track.Type.Valid() {
		return fmt.Errorf("%w: invalid track type %q", domain.ErrInvalidRequest, track.Type)
	}
	if track.CreatedAt.IsZero() {
		return fmt.Errorf("%w: track created_at required", domain.ErrInvalidRequest)
	}
	updated := track.UpdatedAt
	if updated.IsZero() {
		updated = track.CreatedAt
	}

	currentRevisionID := track.CurrentRevisionID
	for i := range track.Revisions {
		rev := &track.Revisions[i]
		if rev.ID == "" {
			return fmt.Errorf("%w: revision id required", domain.ErrInvalidRequest)
		}
		if rev.TrackID == "" {
			rev.TrackID = track.ID
		}
		if rev.TrackID != track.ID {
			return fmt.Errorf("%w: revision %s belongs to another track", domain.ErrInvalidRequest, rev.ID)
		}
		if !rev.AuthorType.Valid() {
			return fmt.Errorf("%w: invalid author type %q", domain.ErrInvalidRequest, rev.AuthorType)
		}
		if rev.CreatedAt.IsZero() {
			return fmt.Errorf("%w: revision %s created_at required", domain.ErrInvalidRequest, rev.ID)
		}
		if err := normalizeSegments(rev.Segments); err != nil {
			return err
		}
		currentRevisionID = rev.ID
	}
	if track.CurrentRevisionID != "" {
		found := false
		for _, rev := range track.Revisions {
			if rev.ID == track.CurrentRevisionID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: current revision %s not in track revisions", domain.ErrInvalidRequest, track.CurrentRevisionID)
		}
		currentRevisionID = track.CurrentRevisionID
	}

	_, err := q.db.ExecContext(ctx, `
		INSERT INTO subtitle_tracks (id, project_id, language, type, current_revision_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		track.ID, track.ProjectID, track.Language, string(track.Type),
		nullString(currentRevisionID), formatTime(track.CreatedAt), formatTime(updated),
	)
	if err != nil {
		return fmt.Errorf("insert subtitle track: %w", err)
	}
	for _, rev := range track.Revisions {
		if err := insertSubtitleRevision(ctx, q, rev); err != nil {
			return err
		}
	}
	return nil
}

// Store 包装：在独立事务中创建轨道。
func (s *Store) CreateSubtitleTrack(ctx context.Context, track domain.SubtitleTrack) error {
	return s.DoTx(ctx, func(tx *Tx) error {
		return tx.createSubtitleTrack(ctx, track)
	})
}

// GetSubtitleTrack 读取轨道及全部 revision。
func (q *querier) GetSubtitleTrack(ctx context.Context, id domain.ID) (domain.SubtitleTrack, error) {
	var (
		track     domain.SubtitleTrack
		trackType string
		current   sql.NullString
		created   string
		updated   string
	)
	row := q.db.QueryRowContext(ctx, `
		SELECT id, project_id, language, type, current_revision_id, created_at, updated_at
		FROM subtitle_tracks WHERE id = ?`, id)
	if err := row.Scan(&track.ID, &track.ProjectID, &track.Language, &trackType,
		&current, &created, &updated); err != nil {
		return domain.SubtitleTrack{}, wrapNotFound(err)
	}
	track.Type = domain.TrackType(trackType)
	if !track.Type.Valid() {
		return domain.SubtitleTrack{}, fmt.Errorf("track %s has invalid type %q", track.ID, trackType)
	}
	if current.Valid {
		track.CurrentRevisionID = current.String
	}
	var err error
	if track.CreatedAt, err = parseTime(created); err != nil {
		return domain.SubtitleTrack{}, err
	}
	if track.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.SubtitleTrack{}, err
	}

	rows, err := q.db.QueryContext(ctx, `
		SELECT id FROM subtitle_revisions WHERE track_id = ? ORDER BY created_at, rowid`, id)
	if err != nil {
		return domain.SubtitleTrack{}, fmt.Errorf("query track revisions: %w", err)
	}
	defer rows.Close()
	var revisionIDs []domain.ID
	for rows.Next() {
		var revID domain.ID
		if err := rows.Scan(&revID); err != nil {
			return domain.SubtitleTrack{}, fmt.Errorf("scan revision id: %w", err)
		}
		revisionIDs = append(revisionIDs, revID)
	}
	if err := rows.Err(); err != nil {
		return domain.SubtitleTrack{}, fmt.Errorf("iterate revision ids: %w", err)
	}
	// 连接池上限为 1：先释放 rows 再逐条读取，避免嵌套查询等待同一连接。
	if err := rows.Close(); err != nil {
		return domain.SubtitleTrack{}, fmt.Errorf("close revision ids: %w", err)
	}

	track.Revisions = make([]domain.SubtitleRevision, 0, len(revisionIDs))
	for _, revID := range revisionIDs {
		rev, err := q.GetSubtitleRevision(ctx, revID)
		if err != nil {
			return domain.SubtitleTrack{}, err
		}
		track.Revisions = append(track.Revisions, rev)
	}
	return track, nil
}

// GetSubtitleRevision 读取一个 revision 及其句段。
func (q *querier) GetSubtitleRevision(ctx context.Context, id domain.ID) (domain.SubtitleRevision, error) {
	var (
		rev        domain.SubtitleRevision
		parent     sql.NullString
		author     string
		inputRev   sql.NullString
		styleJSON  sql.NullString
		blocksJSON sql.NullString
		createdAt  string
	)
	row := q.db.QueryRowContext(ctx, `
		SELECT id, track_id, parent_revision_id, author_type, input_revision_id,
		       style_json, erase_blocks_json, created_at
		FROM subtitle_revisions WHERE id = ?`, id)
	if err := row.Scan(&rev.ID, &rev.TrackID, &parent, &author, &inputRev,
		&styleJSON, &blocksJSON, &createdAt); err != nil {
		return domain.SubtitleRevision{}, wrapNotFound(err)
	}
	rev.AuthorType = domain.AuthorType(author)
	if !rev.AuthorType.Valid() {
		return domain.SubtitleRevision{}, fmt.Errorf("revision %s has invalid author type %q", rev.ID, author)
	}
	if parent.Valid {
		rev.ParentRevisionID = parent.String
	}
	if inputRev.Valid {
		rev.InputRevisionID = inputRev.String
	}
	var err error
	if rev.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.SubtitleRevision{}, err
	}
	if rev.Segments, err = q.loadSegments(ctx, rev.ID); err != nil {
		return domain.SubtitleRevision{}, err
	}
	if err := unmarshalNull(styleJSON, &rev.Style); err != nil {
		return domain.SubtitleRevision{}, err
	}
	if err := unmarshalNull(blocksJSON, &rev.EraseBlocks); err != nil {
		return domain.SubtitleRevision{}, err
	}
	if rev.RevisionDigest, err = domain.RevisionBindingDigest(rev); err != nil {
		return domain.SubtitleRevision{}, err
	}
	return rev, nil
}

// CurrentSegments 返回轨道当前 revision 的句段。
func (q *querier) CurrentSegments(ctx context.Context, trackID domain.ID) ([]domain.Segment, error) {
	var current sql.NullString
	row := q.db.QueryRowContext(ctx, `SELECT current_revision_id FROM subtitle_tracks WHERE id = ?`, trackID)
	if err := row.Scan(&current); err != nil {
		return nil, wrapNotFound(err)
	}
	if !current.Valid {
		return []domain.Segment{}, nil
	}
	return q.loadSegments(ctx, current.String)
}

// loadSegments 读取某个 revision 的有序句段。
func (q *querier) loadSegments(ctx context.Context, revisionID domain.ID) ([]domain.Segment, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT id, start_ms, end_ms, text, speaker, content_revision, content_digest, lineage_json
		FROM segments WHERE revision_id = ? ORDER BY ordinal`, revisionID)
	if err != nil {
		return nil, fmt.Errorf("query segments: %w", err)
	}
	defer rows.Close()

	segments := make([]domain.Segment, 0)
	for rows.Next() {
		var (
			seg     domain.Segment
			speaker sql.NullString
			lineage sql.NullString
		)
		if err := rows.Scan(&seg.ID, &seg.StartMs, &seg.EndMs, &seg.Text, &speaker,
			&seg.ContentRevision, &seg.ContentDigest, &lineage); err != nil {
			return nil, fmt.Errorf("scan segment: %w", err)
		}
		if speaker.Valid {
			seg.Speaker = speaker.String
		}
		if err := unmarshalNull(lineage, &seg.Lineage); err != nil {
			return nil, err
		}
		segments = append(segments, seg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate segments: %w", err)
	}
	return segments, nil
}

// ListProjectTracks 列出项目字幕轨（含 revision）。
func (q *querier) ListProjectTracks(ctx context.Context, projectID domain.ID) ([]domain.SubtitleTrack, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT id FROM subtitle_tracks WHERE project_id = ? ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query project tracks: %w", err)
	}
	defer rows.Close()

	var ids []domain.ID
	for rows.Next() {
		var id domain.ID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan track id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate track ids: %w", err)
	}
	// 连接池上限为 1：先释放 rows 再逐条读取。
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close track ids: %w", err)
	}

	tracks := make([]domain.SubtitleTrack, 0, len(ids))
	for _, id := range ids {
		track, err := q.GetSubtitleTrack(ctx, id)
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, track)
	}
	return tracks, nil
}

// SaveSubtitleRevision 保存新 revision：base_revision 不匹配返回 REVISION_CONFLICT。
//
// 句级内容版本按句段延续：已有稳定 ID 的文本/说话人变化时 content_revision+1，
// 新句段从 1 开始；内容摘要由 Core 在本方法内计算。整轨 revision 只做溯源，
// 不改变未受影响句段的句级版本。
func (tx *Tx) SaveSubtitleRevision(ctx context.Context, trackID domain.ID, baseRevisionID domain.ID, in RevisionSave, now time.Time) (domain.SubtitleRevision, error) {
	if !in.AuthorType.Valid() {
		return domain.SubtitleRevision{}, fmt.Errorf("%w: invalid author type %q", domain.ErrInvalidRequest, in.AuthorType)
	}
	if now.IsZero() {
		now = time.Now()
	}
	if err := domain.ValidateSegmentInputs(in.Segments); err != nil {
		return domain.SubtitleRevision{}, err
	}

	var current sql.NullString
	row := tx.db.QueryRowContext(ctx, `SELECT current_revision_id FROM subtitle_tracks WHERE id = ?`, trackID)
	if err := row.Scan(&current); err != nil {
		return domain.SubtitleRevision{}, wrapNotFound(err)
	}
	currentID := ""
	if current.Valid {
		currentID = current.String
	}
	if currentID != baseRevisionID {
		return domain.SubtitleRevision{}, fmt.Errorf("%w: track %s base revision %q != current %q",
			domain.ErrRevisionConflict, trackID, baseRevisionID, currentID)
	}

	prevSegments := map[domain.ID]domain.Segment{}
	if currentID != "" {
		prev, err := tx.GetSubtitleRevision(ctx, currentID)
		if err != nil {
			return domain.SubtitleRevision{}, err
		}
		for _, seg := range prev.Segments {
			prevSegments[seg.ID] = seg
		}
	}
	if err := domain.ValidateSegmentLineage(in.Segments, prevSegments); err != nil {
		return domain.SubtitleRevision{}, err
	}

	revisionID := in.RevisionID
	if revisionID == "" {
		revisionID = NewID("srev")
	}
	parentID := in.ParentRevisionID
	if parentID == "" {
		parentID = currentID
	}
	segments, err := buildRevisionSegments(in.Segments, prevSegments)
	if err != nil {
		return domain.SubtitleRevision{}, err
	}
	rev := domain.SubtitleRevision{
		ID:               revisionID,
		TrackID:          trackID,
		ParentRevisionID: parentID,
		AuthorType:       in.AuthorType,
		Segments:         segments,
		InputRevisionID:  in.InputRevisionID,
		Style:            in.Style,
		EraseBlocks:      normalizeEraseBlocks(in.EraseBlocks),
		CreatedAt:        now.UTC(),
	}
	if err := insertSubtitleRevision(ctx, tx.querier, rev); err != nil {
		return domain.SubtitleRevision{}, err
	}
	if rev.RevisionDigest, err = domain.RevisionBindingDigest(rev); err != nil {
		return domain.SubtitleRevision{}, err
	}

	res, err := tx.db.ExecContext(ctx, `
		UPDATE subtitle_tracks
		SET current_revision_id = ?, updated_at = ?
		WHERE id = ? AND COALESCE(current_revision_id, '') = ?`,
		revisionID, formatTime(now), trackID, currentID)
	if err != nil {
		return domain.SubtitleRevision{}, fmt.Errorf("update track current revision: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.SubtitleRevision{}, fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return domain.SubtitleRevision{}, fmt.Errorf("%w: track %s was updated concurrently", domain.ErrRevisionConflict, trackID)
	}
	return rev, nil
}

// Store 包装：在独立事务中保存 revision。
func (s *Store) SaveSubtitleRevision(ctx context.Context, trackID domain.ID, baseRevisionID domain.ID, in RevisionSave, now time.Time) (domain.SubtitleRevision, error) {
	var saved domain.SubtitleRevision
	err := s.DoTx(ctx, func(tx *Tx) error {
		rev, err := tx.SaveSubtitleRevision(ctx, trackID, baseRevisionID, in, now)
		if err != nil {
			return err
		}
		saved = rev
		return nil
	})
	if err != nil {
		return domain.SubtitleRevision{}, err
	}
	return saved, nil
}

// AppendHistoricalSubtitleRevision preserves a late result without moving the
// track's current pointer or its updated_at ordering.
func (tx *Tx) AppendHistoricalSubtitleRevision(
	ctx context.Context,
	trackID domain.ID,
	baseRevisionID domain.ID,
	in RevisionSave,
	now time.Time,
) (domain.SubtitleRevision, error) {
	if !in.AuthorType.Valid() {
		return domain.SubtitleRevision{}, fmt.Errorf("%w: invalid author type", domain.ErrInvalidRequest)
	}
	if err := domain.ValidateSegmentInputs(in.Segments); err != nil {
		return domain.SubtitleRevision{}, err
	}
	if _, err := tx.GetSubtitleTrack(ctx, trackID); err != nil {
		return domain.SubtitleRevision{}, err
	}
	previous := map[domain.ID]domain.Segment{}
	if baseRevisionID != "" {
		base, err := tx.GetSubtitleRevision(ctx, baseRevisionID)
		if err != nil {
			return domain.SubtitleRevision{}, err
		}
		if base.TrackID != trackID {
			return domain.SubtitleRevision{}, fmt.Errorf("%w: base belongs to another track", domain.ErrInvalidRequest)
		}
		for _, segment := range base.Segments {
			previous[segment.ID] = segment
		}
	}
	if err := domain.ValidateSegmentLineage(in.Segments, previous); err != nil {
		return domain.SubtitleRevision{}, err
	}
	segments, err := buildRevisionSegments(in.Segments, previous)
	if err != nil {
		return domain.SubtitleRevision{}, err
	}
	revision := domain.SubtitleRevision{
		ID: NewID("srev"), TrackID: trackID, ParentRevisionID: baseRevisionID,
		AuthorType: in.AuthorType, Segments: segments,
		InputRevisionID: in.InputRevisionID, CreatedAt: now.UTC(),
	}
	if err := insertSubtitleRevision(ctx, tx.querier, revision); err != nil {
		return domain.SubtitleRevision{}, err
	}
	return revision, nil
}

// insertSubtitleRevision 写入 revision 与句段。
func insertSubtitleRevision(ctx context.Context, q *querier, rev domain.SubtitleRevision) error {
	var styleJSON, blocksJSON any
	if rev.Style != nil {
		encoded, err := marshalJSON(rev.Style)
		if err != nil {
			return err
		}
		styleJSON = encoded
	}
	if len(rev.EraseBlocks) > 0 {
		encoded, err := marshalJSON(rev.EraseBlocks)
		if err != nil {
			return err
		}
		blocksJSON = encoded
	}
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO subtitle_revisions
			(id, track_id, parent_revision_id, author_type, input_revision_id,
			 style_json, erase_blocks_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rev.ID, rev.TrackID, nullString(rev.ParentRevisionID), string(rev.AuthorType),
		nullString(rev.InputRevisionID), styleJSON, blocksJSON, formatTime(rev.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert subtitle revision: %w", err)
	}
	for i, seg := range rev.Segments {
		lineage, err := marshalJSONList(seg.Lineage)
		if err != nil {
			return err
		}
		_, err = q.db.ExecContext(ctx, `
			INSERT INTO segments
				(revision_id, id, ordinal, start_ms, end_ms, text, speaker, content_revision, content_digest, lineage_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rev.ID, seg.ID, i, seg.StartMs, seg.EndMs, seg.Text, nullString(seg.Speaker),
			seg.ContentRevision, seg.ContentDigest, lineage,
		)
		if err != nil {
			return fmt.Errorf("insert segment: %w", err)
		}
	}
	return nil
}

// normalizeEraseBlocks 把空切片归一为 nil，避免 JSON 列出现 []/null 两种表示。
func normalizeEraseBlocks(blocks []domain.EraseBlock) []domain.EraseBlock {
	if len(blocks) == 0 {
		return nil
	}
	return blocks
}

// buildRevisionSegments 计算新 revision 的句段（稳定 ID、内容版本与摘要）。
func buildRevisionSegments(inputs []domain.SegmentInput, prev map[domain.ID]domain.Segment) ([]domain.Segment, error) {
	segments := make([]domain.Segment, 0, len(inputs))
	for _, in := range inputs {
		id := in.ID
		if id == "" {
			id = NewID("seg")
		}
		digest := domain.SegmentContentDigest(in.Text, in.Speaker)
		contentRevision := 1
		if old, ok := prev[id]; ok {
			contentRevision = old.ContentRevision
			if contentRevision < 1 {
				contentRevision = 1
			}
			if old.ContentDigest != digest {
				contentRevision++
			}
		}
		segments = append(segments, domain.Segment{
			ID:              id,
			StartMs:         in.StartMs,
			EndMs:           in.EndMs,
			Text:            in.Text,
			Speaker:         in.Speaker,
			ContentRevision: contentRevision,
			ContentDigest:   digest,
			Lineage:         in.Lineage,
		})
	}
	return segments, nil
}

// normalizeSegments 为初始 revision 补齐内容摘要/版本（ASR 结果可能只带文本与时间）。
func normalizeSegments(segments []domain.Segment) error {
	seen := make(map[domain.ID]struct{}, len(segments))
	var prevEnd domain.Millis
	for i := range segments {
		seg := &segments[i]
		if seg.ID == "" {
			return fmt.Errorf("%w: segment id required", domain.ErrInvalidRequest)
		}
		if _, ok := seen[seg.ID]; ok {
			return fmt.Errorf("%w: duplicate segment id %q", domain.ErrInvalidRequest, seg.ID)
		}
		seen[seg.ID] = struct{}{}
		if seg.StartMs < 0 || seg.EndMs <= seg.StartMs {
			return fmt.Errorf("%w: segment %s has invalid window", domain.ErrInvalidRequest, seg.ID)
		}
		if i > 0 && seg.StartMs < prevEnd {
			return fmt.Errorf("%w: segment %s overlaps previous window", domain.ErrInvalidRequest, seg.ID)
		}
		if seg.ContentRevision < 1 {
			seg.ContentRevision = 1
		}
		if seg.ContentDigest == "" {
			seg.ContentDigest = domain.SegmentContentDigest(seg.Text, seg.Speaker)
		}
		prevEnd = seg.EndMs
	}
	return nil
}

// nullString 把空串映射为 NULL。
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
