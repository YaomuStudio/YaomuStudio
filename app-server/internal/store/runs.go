package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

const runSelect = `
	SELECT id, project_id, name, template, mode_type, status, state_version, source_language,
		languages_json, input_revision_ids_json, extract_json, reuse_audio_track_id,
		reuse_subtitle_track_id, reuse_subtitle_revision_id, erase_mode,
		validate_timestamps, adjust_duration, bilingual,
		subtitle_style_json, imported_translation_tracks_json, execution_config_json, created_at, updated_at
	FROM workflow_runs`

// CreateWorkflowRun 登记业务 Run 及其八节点状态。
func (q *querier) CreateWorkflowRun(ctx context.Context, run domain.WorkflowRun) error {
	return q.createWorkflowRun(ctx, run)
}

func (q *querier) createWorkflowRun(ctx context.Context, run domain.WorkflowRun) error {
	if run.ID == "" || run.ProjectID == "" || run.Name == "" || run.Template == "" {
		return fmt.Errorf("%w: run id/project_id/name/template required", domain.ErrInvalidRequest)
	}
	if !run.Status.Valid() {
		return fmt.Errorf("%w: invalid run status %q", domain.ErrInvalidRequest, run.Status)
	}
	// 目标语言是否必需由 service 按模板校验；工具模板（如 tool_extract）
	// 可以没有目标语言，store 只要求源语言存在。
	if run.SourceLanguage == "" {
		return fmt.Errorf("%w: run source_language required", domain.ErrInvalidRequest)
	}
	if run.CreatedAt.IsZero() || run.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: run timestamps required", domain.ErrInvalidRequest)
	}
	stateVersion := run.StateVersion
	if stateVersion <= 0 {
		stateVersion = 1
	}
	cfg := run.Config
	if err := domain.ValidateRunConfig(cfg); err != nil {
		return err
	}

	languages, err := marshalStrings(run.Languages)
	if err != nil {
		return err
	}
	inputRevisions, err := marshalJSONList(cfg.InputRevisionIDs)
	if err != nil {
		return err
	}
	var extract any
	if cfg.Extract != nil {
		extract, err = marshalJSON(cfg.Extract)
		if err != nil {
			return err
		}
	}
	var style any
	if cfg.SubtitleStyle != nil {
		style, err = marshalJSON(cfg.SubtitleStyle)
		if err != nil {
			return err
		}
	}
	var importedTranslations any
	if cfg.ImportedTranslationTracks != nil {
		importedTranslations, err = marshalJSON(cfg.ImportedTranslationTracks)
		if err != nil {
			return err
		}
	}
	var execution any
	if cfg.Execution != nil {
		execution, err = marshalJSON(cfg.Execution)
		if err != nil {
			return err
		}
	}

	_, err = q.db.ExecContext(ctx, `
		INSERT INTO workflow_runs
			(id, project_id, name, template, mode_type, status, state_version, source_language,
			 languages_json, input_revision_ids_json, extract_json, reuse_audio_track_id,
			 reuse_subtitle_track_id, reuse_subtitle_revision_id, erase_mode,
			 validate_timestamps, adjust_duration, bilingual,
			 subtitle_style_json, imported_translation_tracks_json, execution_config_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.ProjectID, run.Name, run.Template, nullString(run.ModeType), string(run.Status), stateVersion,
		run.SourceLanguage, languages, inputRevisions, extract,
		nullString(cfg.ReuseAudioTrackID), nullString(cfg.ReuseSubtitleTrackID),
		nullString(cfg.ReuseSubtitleRevisionID),
		nullString(string(cfg.EraseMode)), boolInt(cfg.ValidateTimestamps),
		boolInt(cfg.AdjustDuration), boolInt(cfg.Bilingual), style,
		importedTranslations, execution, formatTime(run.CreatedAt), formatTime(run.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert workflow run: %w", err)
	}
	seenNodes := make(map[domain.PipelineNodeKey]struct{}, len(run.Nodes))
	for i, node := range run.Nodes {
		if !node.Key.Valid() {
			return fmt.Errorf("%w: invalid node key %q", domain.ErrInvalidRequest, node.Key)
		}
		if _, ok := seenNodes[node.Key]; ok {
			return fmt.Errorf("%w: duplicate node key %q", domain.ErrInvalidRequest, node.Key)
		}
		seenNodes[node.Key] = struct{}{}
		if !node.Status.Valid() {
			return fmt.Errorf("%w: invalid node status %q", domain.ErrInvalidRequest, node.Status)
		}
		if node.Progress < 0 || node.Progress > 100 {
			return fmt.Errorf("%w: node progress %d out of range", domain.ErrInvalidRequest, node.Progress)
		}
		if _, err := q.db.ExecContext(ctx, `
			INSERT INTO run_nodes (run_id, key, status, progress, detail, position)
			VALUES (?, ?, ?, ?, ?, ?)`,
			run.ID, string(node.Key), string(node.Status), node.Progress, nullString(node.Detail), i,
		); err != nil {
			return fmt.Errorf("insert run node: %w", err)
		}
	}
	return nil
}

// Store 包装：在独立事务中创建 Run 与节点。
func (s *Store) CreateWorkflowRun(ctx context.Context, run domain.WorkflowRun) error {
	return s.DoTx(ctx, func(tx *Tx) error {
		return tx.createWorkflowRun(ctx, run)
	})
}

// GetWorkflowRun 读取 Run 及其节点状态。
func (q *querier) GetWorkflowRun(ctx context.Context, id domain.ID) (domain.WorkflowRun, error) {
	row := q.db.QueryRowContext(ctx, runSelect+` WHERE id = ?`, id)
	run, err := scanRun(row)
	if err != nil {
		return domain.WorkflowRun{}, wrapNotFound(err)
	}
	nodes, err := q.listRunNodes(ctx, id)
	if err != nil {
		return domain.WorkflowRun{}, err
	}
	run.Nodes = nodes
	return run, nil
}

func scanRun(row rowScanner) (domain.WorkflowRun, error) {
	var (
		run                  domain.WorkflowRun
		modeType             sql.NullString
		status               string
		languages            string
		inputRevs            sql.NullString
		extract              sql.NullString
		reuseAudio           sql.NullString
		reuseSub             sql.NullString
		reuseSubRev          sql.NullString
		eraseMode            sql.NullString
		validateTS           int
		adjustDur            int
		bilingual            int
		subtitleStyle        sql.NullString
		importedTranslations sql.NullString
		execution            sql.NullString
		created              string
		updated              string
	)
	if err := row.Scan(&run.ID, &run.ProjectID, &run.Name, &run.Template, &modeType, &status,
		&run.StateVersion, &run.SourceLanguage, &languages, &inputRevs, &extract,
		&reuseAudio, &reuseSub, &reuseSubRev, &eraseMode, &validateTS, &adjustDur, &bilingual,
		&subtitleStyle, &importedTranslations, &execution, &created, &updated); err != nil {
		return domain.WorkflowRun{}, err
	}
	run.ModeType = modeType.String
	run.Status = domain.RunStatus(status)
	if !run.Status.Valid() {
		return domain.WorkflowRun{}, fmt.Errorf("run %s has invalid status %q", run.ID, status)
	}
	var err error
	if err = unmarshalNull(sql.NullString{String: languages, Valid: true}, &run.Languages); err != nil {
		return domain.WorkflowRun{}, err
	}
	if err = unmarshalNull(inputRevs, &run.Config.InputRevisionIDs); err != nil {
		return domain.WorkflowRun{}, err
	}
	if extract.Valid {
		var opts domain.ExtractOptions
		if err = unmarshalNull(extract, &opts); err != nil {
			return domain.WorkflowRun{}, err
		}
		run.Config.Extract = &opts
	}
	if reuseAudio.Valid {
		run.Config.ReuseAudioTrackID = reuseAudio.String
	}
	if reuseSub.Valid {
		run.Config.ReuseSubtitleTrackID = reuseSub.String
	}
	if reuseSubRev.Valid {
		run.Config.ReuseSubtitleRevisionID = reuseSubRev.String
	}
	if eraseMode.Valid {
		run.Config.EraseMode = domain.EraseMode(eraseMode.String)
	}
	run.Config.ValidateTimestamps = validateTS != 0
	run.Config.AdjustDuration = adjustDur != 0
	run.Config.Bilingual = bilingual != 0
	if subtitleStyle.Valid {
		var style domain.SubtitleStyle
		if err = unmarshalNull(subtitleStyle, &style); err != nil {
			return domain.WorkflowRun{}, err
		}
		run.Config.SubtitleStyle = &style
	}
	if importedTranslations.Valid {
		var refs map[string]domain.TranslationTrackRef
		if err = unmarshalNull(importedTranslations, &refs); err != nil {
			return domain.WorkflowRun{}, err
		}
		run.Config.ImportedTranslationTracks = refs
	}
	if execution.Valid {
		if err = unmarshalNull(execution, &run.Config.Execution); err != nil {
			return domain.WorkflowRun{}, err
		}
	}
	if err = domain.ValidateRunConfig(run.Config); err != nil {
		return domain.WorkflowRun{}, fmt.Errorf("run %s config: %w", run.ID, err)
	}
	if run.CreatedAt, err = parseTime(created); err != nil {
		return domain.WorkflowRun{}, err
	}
	if run.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.WorkflowRun{}, err
	}
	return run, nil
}

// listRunNodes 读取 Run 的节点状态（按创建顺序）。
func (q *querier) listRunNodes(ctx context.Context, runID domain.ID) ([]domain.PipelineNodeState, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT key, status, progress, detail FROM run_nodes
		WHERE run_id = ? ORDER BY position`, runID)
	if err != nil {
		return nil, fmt.Errorf("query run nodes: %w", err)
	}
	defer rows.Close()

	nodes := make([]domain.PipelineNodeState, 0)
	for rows.Next() {
		var (
			node   domain.PipelineNodeState
			key    string
			status string
			detail sql.NullString
		)
		if err := rows.Scan(&key, &status, &node.Progress, &detail); err != nil {
			return nil, fmt.Errorf("scan run node: %w", err)
		}
		node.Key = domain.PipelineNodeKey(key)
		node.Status = domain.NodeStatus(status)
		if !node.Key.Valid() || !node.Status.Valid() {
			return nil, fmt.Errorf("run %s has invalid node %q/%q", runID, key, status)
		}
		if detail.Valid {
			node.Detail = detail.String
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run nodes: %w", err)
	}
	return nodes, nil
}

// ListProjectRuns 列出项目 Run（含节点状态）。
func (q *querier) ListProjectRuns(ctx context.Context, projectID domain.ID) ([]domain.WorkflowRun, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT id FROM workflow_runs WHERE project_id = ? ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query project runs: %w", err)
	}
	defer rows.Close()

	var ids []domain.ID
	for rows.Next() {
		var id domain.ID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan run id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run ids: %w", err)
	}
	// 连接池上限为 1：先释放 rows 再逐条读取。
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close run ids: %w", err)
	}

	runs := make([]domain.WorkflowRun, 0, len(ids))
	for _, id := range ids {
		run, err := q.GetWorkflowRun(ctx, id)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

// UpdateRunStatus 以状态版本 CAS 迁移 Run 状态。
//
// 相同状态视为幂等 no-op（不递增版本）；非法迁移返回 ErrInvalidTransition；
// 版本不匹配返回 ErrStateConflict。
func (tx *Tx) UpdateRunStatus(ctx context.Context, id domain.ID, to domain.RunStatus, expectedVersion int64, now time.Time) (domain.WorkflowRun, error) {
	if !to.Valid() {
		return domain.WorkflowRun{}, fmt.Errorf("%w: invalid run status %q", domain.ErrInvalidRequest, to)
	}
	var (
		current string
		version int64
	)
	row := tx.db.QueryRowContext(ctx, `SELECT status, state_version FROM workflow_runs WHERE id = ?`, id)
	if err := row.Scan(&current, &version); err != nil {
		return domain.WorkflowRun{}, wrapNotFound(err)
	}
	if version != expectedVersion {
		return domain.WorkflowRun{}, fmt.Errorf("%w: run %s state_version %d != %d",
			domain.ErrStateConflict, id, version, expectedVersion)
	}
	from := domain.RunStatus(current)
	if !from.Valid() {
		return domain.WorkflowRun{}, fmt.Errorf("run %s has invalid status %q", id, current)
	}
	if from == to {
		return tx.GetWorkflowRun(ctx, id)
	}
	if _, err := domain.TransitionRun(from, to); err != nil {
		return domain.WorkflowRun{}, err
	}
	res, err := tx.db.ExecContext(ctx, `
		UPDATE workflow_runs
		SET status = ?, state_version = state_version + 1, updated_at = ?
		WHERE id = ? AND state_version = ?`,
		string(to), formatTime(now), id, expectedVersion)
	if err != nil {
		return domain.WorkflowRun{}, fmt.Errorf("update run status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.WorkflowRun{}, fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return domain.WorkflowRun{}, fmt.Errorf("%w: run %s updated concurrently", domain.ErrStateConflict, id)
	}
	return tx.GetWorkflowRun(ctx, id)
}

// Store 包装：在独立事务中迁移 Run 状态。
func (s *Store) UpdateRunStatus(ctx context.Context, id domain.ID, to domain.RunStatus, expectedVersion int64, now time.Time) (domain.WorkflowRun, error) {
	var run domain.WorkflowRun
	err := s.DoTx(ctx, func(tx *Tx) error {
		updated, err := tx.UpdateRunStatus(ctx, id, to, expectedVersion, now)
		if err != nil {
			return err
		}
		run = updated
		return nil
	})
	if err != nil {
		return domain.WorkflowRun{}, err
	}
	return run, nil
}

// UpdateRunNode 更新节点状态/进度；同状态时只更新进度与说明。
func (tx *Tx) UpdateRunNode(ctx context.Context, runID domain.ID, key domain.PipelineNodeKey, to domain.NodeStatus, progress int, detail string) error {
	return tx.UpdateRunNodeWithReview(ctx, runID, key, to, progress, detail, domain.NodeRequiresReview(key))
}

// UpdateRunNodeWithReview 由调用方显式提供审核要求；模板可跳过 06 审核。
func (tx *Tx) UpdateRunNodeWithReview(
	ctx context.Context,
	runID domain.ID,
	key domain.PipelineNodeKey,
	to domain.NodeStatus,
	progress int,
	detail string,
	requiresReview bool,
) error {
	if !key.Valid() {
		return fmt.Errorf("%w: invalid node key %q", domain.ErrInvalidRequest, key)
	}
	if !to.Valid() {
		return fmt.Errorf("%w: invalid node status %q", domain.ErrInvalidRequest, to)
	}
	if progress < 0 || progress > 100 {
		return fmt.Errorf("%w: node progress %d out of range", domain.ErrInvalidRequest, progress)
	}
	var current string
	row := tx.db.QueryRowContext(ctx, `SELECT status FROM run_nodes WHERE run_id = ? AND key = ?`, runID, string(key))
	if err := row.Scan(&current); err != nil {
		return wrapNotFound(err)
	}
	from := domain.NodeStatus(current)
	if !from.Valid() {
		return fmt.Errorf("run %s node %s has invalid status %q", runID, key, current)
	}
	if from != to {
		if _, err := domain.TransitionNodeWithReview(from, to, key, requiresReview); err != nil {
			return err
		}
	}
	res, err := tx.db.ExecContext(ctx, `
		UPDATE run_nodes SET status = ?, progress = ?, detail = ?
		WHERE run_id = ? AND key = ?`,
		string(to), progress, nullString(detail), runID, string(key))
	if err != nil {
		return fmt.Errorf("update run node: %w", err)
	}
	return affectedOne(res, domain.ErrNotFound)
}

// Store 包装：在独立事务中更新节点。
func (s *Store) UpdateRunNode(ctx context.Context, runID domain.ID, key domain.PipelineNodeKey, to domain.NodeStatus, progress int, detail string) error {
	return s.DoTx(ctx, func(tx *Tx) error {
		return tx.UpdateRunNode(ctx, runID, key, to, progress, detail)
	})
}
