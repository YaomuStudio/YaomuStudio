package store

// API 面需要的读取/查询与少量内部状态持久化。
//
// 本文件补充 internal/api 与 internal/service 需要、但不属于单实体仓储的方法：
// 组合查询（任务过滤/调度候选/句段上下文/项目审核）、engine_configs、
// exports 与 app_meta 键值状态。仍遵守包边界：不 import net/http，不做调度。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// ReadTx 在单个读事务内执行 fn：快照语义（例如 GET /snapshot 的事件高水位
// 与项目视图必须来自同一一致读）。fn 只应做读取。
func (s *Store) ReadTx(ctx context.Context, fn func(*Tx) error) error {
	tx, err := s.sqlDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin read tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return fn(&Tx{querier: &querier{db: tx}, tx: tx})
}

// ---- app_meta 键值状态（非业务领域表） ----

// GetMeta 读取应用级键值；不存在返回 found=false。
func (q *querier) GetMeta(ctx context.Context, key string) (string, bool, error) {
	var value string
	row := q.db.QueryRowContext(ctx, `SELECT value FROM app_meta WHERE key = ?`, key)
	if err := row.Scan(&value); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, fmt.Errorf("query app_meta %q: %w", key, err)
	}
	return value, true, nil
}

// SetMeta 写入应用级键值（upsert）。
func (q *querier) SetMeta(ctx context.Context, key, value string) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO app_meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("set app_meta %q: %w", key, err)
	}
	return nil
}

// ---- 任务查询 ----

// TaskFilter 是任务列表/调度的过滤条件。
type TaskFilter struct {
	ProjectID string
	RunID     string
	Status    domain.TaskStatus
	// Cursor 是上一页返回的不透明游标（created_at + id）。
	Cursor string
	Limit  int
}

// ListTasks 按过滤条件分页返回任务（不含完整步骤历史）。
func (q *querier) ListTasks(ctx context.Context, f TaskFilter) ([]domain.Task, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	query := taskSelect + ` WHERE 1 = 1`
	args := make([]any, 0, 6)
	if f.ProjectID != "" {
		query += ` AND project_id = ?`
		args = append(args, f.ProjectID)
	}
	if f.RunID != "" {
		query += ` AND run_id = ?`
		args = append(args, f.RunID)
	}
	if f.Status != "" {
		query += ` AND status = ?`
		args = append(args, string(f.Status))
	}
	if f.Cursor != "" {
		created, id, err := DecodeTaskCursor(f.Cursor)
		if err != nil {
			return nil, err
		}
		query += ` AND (created_at > ? OR (created_at = ? AND id > ?))`
		args = append(args, created, created, id)
	}
	query += ` ORDER BY created_at, id LIMIT ?`
	args = append(args, limit+1)

	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query tasks: %w", err)
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0, limit)
	for rows.Next() {
		task, err := scanTask(rows, false)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tasks: %w", err)
	}
	return tasks, nil
}

// EncodeTaskCursor 编码任务分页游标（created_at 固定宽度文本 + 不透明 ID）。
func EncodeTaskCursor(createdAt time.Time, id domain.ID) string {
	return formatTime(createdAt) + "|" + id
}

// DecodeTaskCursor 解析任务分页游标。
func DecodeTaskCursor(cursor string) (createdAt, id string, err error) {
	idx := strings.LastIndexByte(cursor, '|')
	if idx <= 0 || idx == len(cursor)-1 {
		return "", "", fmt.Errorf("%w: invalid task cursor", domain.ErrInvalidRequest)
	}
	createdAt, id = cursor[:idx], cursor[idx+1:]
	if _, err := parseTime(createdAt); err != nil {
		return "", "", fmt.Errorf("%w: invalid task cursor", domain.ErrInvalidRequest)
	}
	return createdAt, id, nil
}

// NextDispatchableTask 返回下一条可派发任务（QUEUED 且所属 Run 允许派发）。
//
// 没有 run_id 的任务（例如独立导出）不受 Run 闸门约束；导出是交付动作，
// 即使来源 Run 已暂停/终止也允许执行（不占用模型槽）。已成功的克隆/专属链路
// 只额外放行补齐完整音轨的 07 assemble 任务，不重开 Run。其余终止 Run 的
// 任务不会被选中（docs/local-api-contract.md §8）。
func (q *querier) NextDispatchableTask(ctx context.Context) (domain.Task, bool, error) {
	return q.nextDispatchableTask(ctx, true)
}

// NextDispatchableNonExportTask 返回下一条非导出的可派发任务。
//
// 调度层在同一执行器已有导出在执行时用它跳过排队中的导出，避免导出
// 串行化阻塞同批其他任务；导出完成后由 Notify 唤醒再取。
func (q *querier) NextDispatchableNonExportTask(ctx context.Context) (domain.Task, bool, error) {
	return q.nextDispatchableTask(ctx, false)
}

func (q *querier) nextDispatchableTask(ctx context.Context, includeExport bool) (domain.Task, bool, error) {
	where, args := dispatchableTaskWhere(includeExport)
	row := q.db.QueryRowContext(ctx, taskSelect+`
		WHERE `+where+`
		ORDER BY CASE tasks.status WHEN 'QUEUED' THEN 0 ELSE 1 END, tasks.created_at, tasks.id
		LIMIT 1`, args...)
	task, err := scanTask(row, false)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.Task{}, false, nil
		}
		return domain.Task{}, false, wrapNotFound(err)
	}
	return task, true, nil
}

// dispatchableTaskWhere 是“可派发任务”的唯一 SQL 谓词：QUEUED/WAITING_RESOURCE，
// 且所属 Run 允许派发（无 Run 的独立任务与导出不受 Run 暂停/终止闸门约束）。
// 已成功 Run 上补齐完整配音的 07 assemble 任务是唯一的节点例外。
// 单条派发与批量等待原因扫描共用它，避免两套候选口径漂移。
func dispatchableTaskWhere(includeExport bool) (string, []any) {
	args := []any{
		string(domain.TaskStatusQueued), string(domain.TaskStatusWaitingResource),
		string(domain.TaskKindExport),
		string(domain.RunStatusSucceeded),
		string(domain.TaskKindNode),
		string(domain.NodeKeyClone),
	}
	clause := `tasks.status IN (?, ?)
		  AND (tasks.run_id IS NULL OR tasks.run_id = '' OR tasks.kind = ?
		       OR EXISTS (
			SELECT 1 FROM workflow_runs r
			WHERE r.id = tasks.run_id AND (
				r.status IN ('QUEUED', 'RUNNING', 'WAITING_REVIEW')
				OR (r.status = ? AND tasks.kind = ? AND tasks.node_key = ?
				    AND COALESCE(tasks.segment_id, '') = ''
				    AND json_extract(tasks.params_json, '$.op') = 'assemble'))))`
	if !includeExport {
		clause += ` AND tasks.kind != ?`
		args = append(args, string(domain.TaskKindExport))
	}
	return clause, args
}

// ListWaitingResourceCandidateIDs 返回与 NextDispatchableTask 同一 Run 闸门下的
// 全部 QUEUED/WAITING_RESOURCE 任务 ID。调度器在执行器不可用时用它批量写入
// 等待原因；事务内会按 ID 重新读取当前状态，因此候选只携带身份，避免每个
// tick 重复解码整行 JSON。它不改变单条派发选取顺序，也不是第二套业务队列。
func (q *querier) ListWaitingResourceCandidateIDs(ctx context.Context) ([]domain.ID, error) {
	where, args := dispatchableTaskWhere(true)
	rows, err := q.db.QueryContext(ctx, `SELECT tasks.id FROM tasks
		WHERE `+where+`
		ORDER BY CASE tasks.status WHEN 'QUEUED' THEN 0 ELSE 1 END, tasks.created_at, tasks.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query waiting resource candidates: %w", err)
	}
	defer rows.Close()

	ids := make([]domain.ID, 0)
	for rows.Next() {
		var id domain.ID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan waiting resource candidate: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate waiting resource candidates: %w", err)
	}
	return ids, nil
}

// ListTasksByStatuses 返回某 Run 内指定状态的任务。
func (q *querier) ListTasksByStatuses(ctx context.Context, runID domain.ID, statuses ...domain.TaskStatus) ([]domain.Task, error) {
	if len(statuses) == 0 {
		return []domain.Task{}, nil
	}
	placeholders := make([]string, len(statuses))
	args := make([]any, 0, len(statuses)+1)
	args = append(args, runID)
	for i, s := range statuses {
		placeholders[i] = "?"
		args = append(args, string(s))
	}
	query := taskSelect + ` WHERE run_id = ? AND status IN (` + strings.Join(placeholders, ", ") + `)
		ORDER BY created_at, id`
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query run tasks: %w", err)
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0)
	for rows.Next() {
		task, err := scanTask(rows, false)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run tasks: %w", err)
	}
	return tasks, nil
}

// ListRunTasks 返回某 Run 内全部任务（不含步骤历史），供流水线聚合使用。
//
// 任务列表 API 的分页上限不适合句级流水线：一个 Run 可能有数百个句段任务，
// 聚合必须看到全量事实，不能用截断结果推断节点完成。
func (q *querier) ListRunTasks(ctx context.Context, runID domain.ID) ([]domain.Task, error) {
	rows, err := q.db.QueryContext(ctx, taskSelect+` WHERE run_id = ? ORDER BY created_at, id`, runID)
	if err != nil {
		return nil, fmt.Errorf("query run tasks: %w", err)
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0)
	for rows.Next() {
		task, err := scanTask(rows, false)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run tasks: %w", err)
	}
	return tasks, nil
}

// CountActiveTasks 统计仍可能调度/执行/恢复的任务数（删除/清理前的引用检查）。
// FAILED/INTERRUPTED 是可显式重试的历史状态，不阻塞清理；重试回到 QUEUED 后会
// 重新被计入。
func (q *querier) CountActiveTasks(ctx context.Context, projectID domain.ID) (int, error) {
	query := `SELECT COUNT(*) FROM tasks WHERE status IN ('QUEUED','WAITING_RESOURCE','RUNNING',
		'PAUSE_REQUESTED','PAUSED','CANCEL_REQUESTED')`
	args := []any{}
	if projectID != "" {
		query += ` AND project_id = ?`
		args = append(args, projectID)
	}
	var n int
	if err := q.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count active tasks: %w", err)
	}
	return n, nil
}

// CountRunningTasks 统计仍在执行的模型任务数（资源槽与清理检查）。
func (q *querier) CountRunningTasks(ctx context.Context) (int, error) {
	var n int
	query := `SELECT COUNT(*) FROM tasks WHERE status IN ('RUNNING','CANCEL_REQUESTED')`
	if err := q.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return 0, fmt.Errorf("count running tasks: %w", err)
	}
	return n, nil
}

// ListRunningTasks 返回仍在执行的任务（含冻结参数），用于推导槽位执行占用。
func (q *querier) ListRunningTasks(ctx context.Context) ([]domain.Task, error) {
	rows, err := q.db.QueryContext(ctx,
		taskSelect+` WHERE status IN ('RUNNING','CANCEL_REQUESTED') ORDER BY created_at, id`,
	)
	if err != nil {
		return nil, fmt.Errorf("query running tasks: %w", err)
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0)
	for rows.Next() {
		task, err := scanTask(rows, false)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate running tasks: %w", err)
	}
	return tasks, nil
}

// ---- 句段上下文 ----

// SegmentContext 是句段在其轨道当前 revision 中的上下文。
type SegmentContext struct {
	Segment    domain.Segment
	TrackID    domain.ID
	TrackType  domain.TrackType
	ProjectID  domain.ID
	Language   string
	RevisionID domain.ID
}

// GetSegmentContext 返回句段及其轨道/项目信息；只认轨道当前 revision。
func (q *querier) GetSegmentContext(ctx context.Context, segmentID domain.ID) (SegmentContext, error) {
	var (
		sc      SegmentContext
		track   string
		project string
		lang    string
		typ     string
		speaker sql.NullString
		lineage sql.NullString
	)
	row := q.db.QueryRowContext(ctx, `
		SELECT s.id, s.start_ms, s.end_ms, s.text, s.speaker, s.content_revision,
			s.content_digest, s.lineage_json, r.id, r.track_id, t.project_id, t.language, t.type
		FROM segments s
		JOIN subtitle_revisions r ON r.id = s.revision_id
		JOIN subtitle_tracks t ON t.id = r.track_id
		WHERE s.id = ? AND r.id = t.current_revision_id`, segmentID)
	if err := row.Scan(&sc.Segment.ID, &sc.Segment.StartMs, &sc.Segment.EndMs, &sc.Segment.Text,
		&speaker, &sc.Segment.ContentRevision, &sc.Segment.ContentDigest, &lineage,
		&sc.RevisionID, &track, &project, &lang, &typ); err != nil {
		return SegmentContext{}, wrapNotFound(err)
	}
	sc.TrackID = track
	sc.ProjectID = project
	sc.Language = lang
	sc.TrackType = domain.TrackType(typ)
	if !sc.TrackType.Valid() {
		return SegmentContext{}, fmt.Errorf("track %s has invalid type %q", track, typ)
	}
	if speaker.Valid {
		sc.Segment.Speaker = speaker.String
	}
	if lineage.Valid {
		if err := unmarshalNull(lineage, &sc.Segment.Lineage); err != nil {
			return SegmentContext{}, err
		}
	}
	return sc, nil
}

// ---- 项目审核列表 ----

// ListProjectReviews 返回项目内审核门槛；runID 非空时只返回该 Run。
func (q *querier) ListProjectReviews(ctx context.Context, projectID domain.ID, runID string) ([]domain.ReviewGate, error) {
	query := `
		SELECT g.id FROM review_gates g
		JOIN workflow_runs r ON r.id = g.run_id
		WHERE r.project_id = ?`
	args := []any{projectID}
	if runID != "" {
		query += ` AND g.run_id = ?`
		args = append(args, runID)
	}
	query += ` ORDER BY r.created_at, g.type, g.language, g.id`

	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query project reviews: %w", err)
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

// ---- 导出记录 ----

const exportSelect = `
	SELECT id, project_id, run_id, kind, format, status, input_revision_id,
		bilingual, source_revision_id,
		output_dir_grant, naming_template, output_path, task_id, created_at, updated_at
	FROM exports`

// CreateExport 登记导出任务记录。
func (q *querier) CreateExport(ctx context.Context, rec domain.ExportRecord) error {
	if rec.ID == "" || rec.ProjectID == "" {
		return fmt.Errorf("%w: export id/project_id required", domain.ErrInvalidRequest)
	}
	if !rec.Kind.Valid() || !rec.Format.Valid() {
		return fmt.Errorf("%w: invalid export kind/format", domain.ErrInvalidRequest)
	}
	if !rec.Status.Valid() {
		return fmt.Errorf("%w: invalid export status %q", domain.ErrInvalidRequest, rec.Status)
	}
	if rec.CreatedAt.IsZero() {
		return fmt.Errorf("%w: export created_at required", domain.ErrInvalidRequest)
	}
	updated := rec.UpdatedAt
	if updated.IsZero() {
		updated = rec.CreatedAt
	}
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO exports
			(id, project_id, run_id, kind, format, status, input_revision_id,
			 bilingual, source_revision_id,
			 output_dir_grant, naming_template, output_path, task_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.ProjectID, nullString(rec.RunID), string(rec.Kind), string(rec.Format),
		string(rec.Status), nullString(rec.InputRevisionID), boolInt(rec.Bilingual),
		nullString(rec.SourceRevisionID), rec.OutputDirGrant,
		rec.NamingTemplate, nullString(rec.OutputPath), nullString(rec.TaskID),
		formatTime(rec.CreatedAt), formatTime(updated),
	)
	if err != nil {
		return fmt.Errorf("insert export: %w", err)
	}
	return nil
}

// GetExport 读取导出记录。
func (q *querier) GetExport(ctx context.Context, id domain.ID) (domain.ExportRecord, error) {
	row := q.db.QueryRowContext(ctx, exportSelect+` WHERE id = ?`, id)
	return scanExport(row)
}

// ListExports 返回导出记录；projectID 非空时按项目过滤。
func (q *querier) ListExports(ctx context.Context, projectID string) ([]domain.ExportRecord, error) {
	query := exportSelect
	args := []any{}
	if projectID != "" {
		query += ` WHERE project_id = ?`
		args = append(args, projectID)
	}
	query += ` ORDER BY created_at DESC, id`
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query exports: %w", err)
	}
	defer rows.Close()

	records := make([]domain.ExportRecord, 0)
	for rows.Next() {
		rec, err := scanExport(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate exports: %w", err)
	}
	return records, nil
}

// GetExportByTask 按关联任务读取导出记录。
func (q *querier) GetExportByTask(ctx context.Context, taskID domain.ID) (domain.ExportRecord, error) {
	row := q.db.QueryRowContext(ctx, exportSelect+` WHERE task_id = ?`, taskID)
	return scanExport(row)
}

// UpdateExportStatus 更新导出状态与输出路径。
func (q *querier) UpdateExportStatus(ctx context.Context, id domain.ID, status domain.TaskStatus, outputPath string, now time.Time) error {
	if !status.Valid() {
		return fmt.Errorf("%w: invalid export status %q", domain.ErrInvalidRequest, status)
	}
	res, err := q.db.ExecContext(ctx, `
		UPDATE exports SET status = ?, output_path = COALESCE(NULLIF(?, ''), output_path), updated_at = ?
		WHERE id = ?`, string(status), outputPath, formatTime(now), id)
	if err != nil {
		return fmt.Errorf("update export status: %w", err)
	}
	return affectedOne(res, domain.ErrNotFound)
}

func scanExport(row rowScanner) (domain.ExportRecord, error) {
	var (
		rec       domain.ExportRecord
		runID     sql.NullString
		kind      string
		format    string
		status    string
		inputRev  sql.NullString
		bilingual int
		sourceRev sql.NullString
		output    sql.NullString
		taskID    sql.NullString
		created   string
		updated   string
	)
	if err := row.Scan(&rec.ID, &rec.ProjectID, &runID, &kind, &format, &status,
		&inputRev, &bilingual, &sourceRev, &rec.OutputDirGrant, &rec.NamingTemplate,
		&output, &taskID, &created, &updated); err != nil {
		return domain.ExportRecord{}, wrapNotFound(err)
	}
	rec.Kind = domain.ExportKind(kind)
	rec.Format = domain.ExportFormat(format)
	rec.Status = domain.TaskStatus(status)
	if !rec.Kind.Valid() || !rec.Format.Valid() || !rec.Status.Valid() {
		return domain.ExportRecord{}, fmt.Errorf("export %s has invalid kind/format/status", rec.ID)
	}
	if runID.Valid {
		rec.RunID = runID.String
	}
	if inputRev.Valid {
		rec.InputRevisionID = inputRev.String
	}
	rec.Bilingual = bilingual != 0
	if sourceRev.Valid {
		rec.SourceRevisionID = sourceRev.String
	}
	if output.Valid {
		rec.OutputPath = output.String
	}
	if taskID.Valid {
		rec.TaskID = taskID.String
	}
	var err error
	if rec.CreatedAt, err = parseTime(created); err != nil {
		return domain.ExportRecord{}, err
	}
	if rec.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.ExportRecord{}, err
	}
	return rec, nil
}

// ---- 引擎配置 ----

// GetEngineConfig 读取槽位配置；未配置时 found=false（调用方按默认值处理）。
func (q *querier) GetEngineConfig(ctx context.Context, slot domain.ModelSlotKey) (domain.EngineConfig, bool, error) {
	var (
		cfg     domain.EngineConfig
		device  sql.NullString
		values  string
		updated string
	)
	row := q.db.QueryRowContext(ctx, `
		SELECT slot, device_id, values_json, updated_at FROM engine_configs WHERE slot = ?`, string(slot))
	if err := row.Scan(&slot, &device, &values, &updated); err != nil {
		if err == sql.ErrNoRows {
			return domain.EngineConfig{Slot: slot, DeviceID: "auto"}, false, nil
		}
		return domain.EngineConfig{}, false, fmt.Errorf("query engine config: %w", err)
	}
	cfg.Slot = slot
	if device.Valid {
		cfg.DeviceID = device.String
	}
	if err := unmarshalNull(sql.NullString{String: values, Valid: true}, &cfg.Values); err != nil {
		return domain.EngineConfig{}, false, err
	}
	t, err := parseTime(updated)
	if err != nil {
		return domain.EngineConfig{}, false, err
	}
	cfg.UpdatedAt = &t
	return cfg, true, nil
}

// SaveEngineConfig 写入槽位配置（upsert）。
func (q *querier) SaveEngineConfig(ctx context.Context, cfg domain.EngineConfig, now time.Time) error {
	if !cfg.Slot.Valid() {
		return fmt.Errorf("%w: invalid model slot %q", domain.ErrInvalidRequest, cfg.Slot)
	}
	values := cfg.Values
	if values == nil {
		values = map[string]any{}
	}
	encoded, err := marshalJSON(values)
	if err != nil {
		return err
	}
	_, err = q.db.ExecContext(ctx, `
		INSERT INTO engine_configs (slot, device_id, values_json, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (slot) DO UPDATE SET
			device_id = excluded.device_id,
			values_json = excluded.values_json,
			updated_at = excluded.updated_at`,
		string(cfg.Slot), nullString(cfg.DeviceID), encoded, formatTime(now))
	if err != nil {
		return fmt.Errorf("save engine config: %w", err)
	}
	return nil
}

// ---- 调度/恢复需要的查询 ----

// LatestAttempt 返回任务最近一次尝试；不存在时 found=false。
func (q *querier) LatestAttempt(ctx context.Context, taskID domain.ID) (domain.Attempt, bool, error) {
	var (
		a        domain.Attempt
		executor sql.NullString
		status   string
		started  sql.NullString
		finished sql.NullString
	)
	row := q.db.QueryRowContext(ctx, `
		SELECT id, task_id, step_id, attempt_no, epoch, executor_id, status, started_at, finished_at
		FROM attempts WHERE task_id = ? ORDER BY attempt_no DESC LIMIT 1`, taskID)
	if err := row.Scan(&a.ID, &a.TaskID, &a.StepID, &a.AttemptNo, &a.Epoch, &executor,
		&status, &started, &finished); err != nil {
		if err == sql.ErrNoRows {
			return domain.Attempt{}, false, nil
		}
		return domain.Attempt{}, false, fmt.Errorf("query latest attempt: %w", err)
	}
	a.Status = domain.StepStatus(status)
	a.ExecutorID = executor.String
	var err error
	if a.StartedAt, err = parseNullTime(started); err != nil {
		return domain.Attempt{}, false, err
	}
	if a.FinishedAt, err = parseNullTime(finished); err != nil {
		return domain.Attempt{}, false, err
	}
	return a, true, nil
}

// UpdateAttempt 更新一次尝试的状态与起止时间。
func (q *querier) UpdateAttempt(ctx context.Context, id domain.ID, status domain.StepStatus, startedAt, finishedAt *time.Time) error {
	if !status.Valid() {
		return fmt.Errorf("%w: invalid attempt status %q", domain.ErrInvalidRequest, status)
	}
	res, err := q.db.ExecContext(ctx, `
		UPDATE attempts SET status = ?,
			started_at = COALESCE(?, started_at),
			finished_at = COALESCE(?, finished_at)
		WHERE id = ?`, string(status), nullTimeValue(startedAt), nullTimeValue(finishedAt), id)
	if err != nil {
		return fmt.Errorf("update attempt: %w", err)
	}
	return affectedOne(res, domain.ErrNotFound)
}

// RunRef 是 Run 的轻量引用（调度收敛用，不含节点）。
type RunRef struct {
	ID             domain.ID
	ProjectID      domain.ID
	Status         domain.RunStatus
	StateVersion   int64
	Languages      []string
	SourceLanguage string
}

// ListRunRefsByStatus 返回指定状态的 Run 引用（按创建时间）。
func (q *querier) ListRunRefsByStatus(ctx context.Context, statuses ...domain.RunStatus) ([]RunRef, error) {
	if len(statuses) == 0 {
		return []RunRef{}, nil
	}
	placeholders := make([]string, len(statuses))
	args := make([]any, 0, len(statuses))
	for i, s := range statuses {
		placeholders[i] = "?"
		args = append(args, string(s))
	}
	rows, err := q.db.QueryContext(ctx, `
		SELECT id, project_id, status, state_version, languages_json, source_language
		FROM workflow_runs WHERE status IN (`+strings.Join(placeholders, ", ")+`)
		ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query runs by status: %w", err)
	}
	defer rows.Close()

	refs := make([]RunRef, 0)
	for rows.Next() {
		var (
			ref       RunRef
			status    string
			languages string
		)
		if err := rows.Scan(&ref.ID, &ref.ProjectID, &status, &ref.StateVersion,
			&languages, &ref.SourceLanguage); err != nil {
			return nil, fmt.Errorf("scan run ref: %w", err)
		}
		ref.Status = domain.RunStatus(status)
		if err := unmarshalNull(sql.NullString{String: languages, Valid: true}, &ref.Languages); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate runs: %w", err)
	}
	return refs, nil
}

// ListTasksByStatus 返回全工作区内指定状态的任务。
func (q *querier) ListTasksByStatus(ctx context.Context, status domain.TaskStatus) ([]domain.Task, error) {
	rows, err := q.db.QueryContext(ctx, taskSelect+` WHERE status = ? ORDER BY created_at, id`, string(status))
	if err != nil {
		return nil, fmt.Errorf("query tasks by status: %w", err)
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0)
	for rows.Next() {
		task, err := scanTask(rows, false)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tasks: %w", err)
	}
	return tasks, nil
}

// GetProjectSummary 返回单项目摘要（快照与项目列表共用口径）。
func (q *querier) GetProjectSummary(ctx context.Context, projectID domain.ID) (domain.ProjectSummary, error) {
	var (
		summary  domain.ProjectSummary
		episode  sql.NullString
		created  string
		updated  string
		lastOpen sql.NullString
		latest   sql.NullString
		modeType sql.NullString
	)
	row := q.db.QueryRowContext(ctx, `
		SELECT p.seq, p.id, p.name, p.edit_version, p.episode_id, p.created_at, p.updated_at, p.last_opened_at,
			(SELECT COUNT(*) FROM assets a WHERE a.project_id = p.id),
			(SELECT COUNT(*) FROM workflow_runs r WHERE r.project_id = p.id),
			(SELECT r.status FROM workflow_runs r WHERE r.project_id = p.id
				ORDER BY r.created_at DESC, r.id DESC LIMIT 1),
			(SELECT r.mode_type FROM workflow_runs r WHERE r.project_id = p.id
				ORDER BY r.created_at DESC, r.id DESC LIMIT 1),
			(SELECT COALESCE(SUM(json_extract(a.meta_json, '$.duration_ms')), 0)
				FROM assets a WHERE a.project_id = p.id),
			(SELECT EXISTS(
				SELECT 1 FROM tasks t WHERE t.status = 'WAITING_RESOURCE' AND t.run_id = (
					SELECT r.id FROM workflow_runs r WHERE r.project_id = p.id
					ORDER BY r.created_at DESC, r.id DESC LIMIT 1
				)
			))
		FROM projects p WHERE p.id = ?`, projectID)
	if err := row.Scan(&summary.Seq, &summary.ID, &summary.Name, &summary.EditVersion, &episode,
		&created, &updated, &lastOpen, &summary.AssetCount, &summary.RunCount,
		&latest, &modeType, &summary.TotalDurationMs, &summary.WaitingResource); err != nil {
		return domain.ProjectSummary{}, wrapNotFound(err)
	}
	if episode.Valid {
		summary.EpisodeID = episode.String
	}
	summary.ModeType = modeType.String
	var err error
	if summary.CreatedAt, err = parseTime(created); err != nil {
		return domain.ProjectSummary{}, err
	}
	if summary.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.ProjectSummary{}, err
	}
	if summary.LastOpenedAt, err = parseNullTime(lastOpen); err != nil {
		return domain.ProjectSummary{}, err
	}
	if latest.Valid {
		summary.LatestRunStatus = domain.RunStatus(latest.String)
	}
	return summary, nil
}

// ListAllAssetLocations 返回全部资产位置（存储清理的引用检查）。
func (q *querier) ListAllAssetLocations(ctx context.Context) ([]domain.AssetLocation, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT id, asset_id, path, source, owner, available, created_at
		FROM asset_locations ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("query asset locations: %w", err)
	}
	defer rows.Close()

	locations := make([]domain.AssetLocation, 0)
	for rows.Next() {
		var (
			loc       domain.AssetLocation
			available int
			created   string
		)
		if err := rows.Scan(&loc.ID, &loc.AssetID, &loc.Path, &loc.Source, &loc.Owner,
			&available, &created); err != nil {
			return nil, fmt.Errorf("scan asset location: %w", err)
		}
		loc.Available = available != 0
		t, err := parseTime(created)
		if err != nil {
			return nil, err
		}
		loc.CreatedAt = t
		locations = append(locations, loc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate asset locations: %w", err)
	}
	return locations, nil
}

// NonterminalRunExecution 是仍可推进 Run 的冻结执行配置（删除安装前的引用检查）。
type NonterminalRunExecution struct {
	RunID  domain.ID
	Status domain.RunStatus
	Config domain.RunConfig
}

// ListNonterminalRunExecutions 返回状态非终止且带冻结执行配置的 Run。
//
// FAILED/INTERRUPTED 任务历史不在这里；Run 仍可能创建/恢复工作时会冻结
// 精确版本锁，删除对应安装引用必须保留或显式拒绝。
func (q *querier) ListNonterminalRunExecutions(ctx context.Context) ([]NonterminalRunExecution, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT id, status, execution_config_json FROM workflow_runs
		WHERE execution_config_json IS NOT NULL AND execution_config_json <> ''
		ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("query nonterminal run executions: %w", err)
	}
	defer rows.Close()

	out := make([]NonterminalRunExecution, 0)
	for rows.Next() {
		var (
			execution NonterminalRunExecution
			status    string
			raw       string
		)
		if err := rows.Scan(&execution.RunID, &status, &raw); err != nil {
			return nil, fmt.Errorf("scan run execution: %w", err)
		}
		execution.Status = domain.RunStatus(status)
		if execution.Status.Terminal() {
			continue
		}
		// execution_config_json 持久化的是 Execution 映射本身，不是 RunConfig 对象。
		var frozen map[domain.CapabilitySlotKey]domain.ExecutionConfig
		if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
			return nil, fmt.Errorf("decode run %s execution config: %w", execution.RunID, err)
		}
		execution.Config = domain.RunConfig{Execution: frozen}
		out = append(out, execution)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run executions: %w", err)
	}
	return out, nil
}

// LatestRunForProject 返回项目内最近创建的 Run（含终止状态），调用方按派发
// 闸门判断是否允许新增执行（docs/local-api-contract.md §8）。
func (q *querier) LatestRunForProject(ctx context.Context, projectID domain.ID) (domain.WorkflowRun, bool, error) {
	var id domain.ID
	row := q.db.QueryRowContext(ctx, `
		SELECT id FROM workflow_runs
		WHERE project_id = ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, projectID)
	if err := row.Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			return domain.WorkflowRun{}, false, nil
		}
		return domain.WorkflowRun{}, false, fmt.Errorf("query latest run: %w", err)
	}
	run, err := q.GetWorkflowRun(ctx, id)
	if err != nil {
		return domain.WorkflowRun{}, false, err
	}
	return run, true, nil
}

// PruneEventsBeforeTime 按保留策略删除早于 before 的事件，返回删除行数。
// 客户端游标落入被裁剪区间时通过 EVENT_CURSOR_EXPIRED 要求重新获取快照。
func (q *querier) PruneEventsBeforeTime(ctx context.Context, before time.Time) (int64, error) {
	res, err := q.db.ExecContext(ctx, `DELETE FROM events WHERE created_at < ?`, formatTime(before))
	if err != nil {
		return 0, fmt.Errorf("prune events by time: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}
