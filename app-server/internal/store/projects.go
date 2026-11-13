package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// CreateProject 登记本地项目。EditVersion 未设置时归一为 1。
func (q *querier) CreateProject(ctx context.Context, p domain.Project) error {
	if p.ID == "" || p.WorkspaceID == "" || p.Name == "" {
		return fmt.Errorf("%w: project id/workspace_id/name required", domain.ErrInvalidRequest)
	}
	if p.SchemaVersion <= 0 {
		return fmt.Errorf("%w: project schema_version required", domain.ErrInvalidRequest)
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: project timestamps required", domain.ErrInvalidRequest)
	}
	editVersion := p.EditVersion
	if editVersion <= 0 {
		editVersion = 1
	}
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO projects
			(id, workspace_id, episode_id, name, schema_version, edit_version, created_at, updated_at, last_opened_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, (
			SELECT COALESCE(MAX(seq), 0) + 1 FROM projects WHERE workspace_id = ?
		))`,
		p.ID, p.WorkspaceID, nullString(p.EpisodeID), p.Name, p.SchemaVersion, editVersion,
		formatTime(p.CreatedAt), formatTime(p.UpdatedAt), nullTimeValue(p.LastOpenedAt),
		p.WorkspaceID,
	)
	if err != nil {
		return fmt.Errorf("insert project: %w", err)
	}
	return nil
}

// GetProject 读取项目并组合其资产、字幕轨与 Run（对齐 Project 响应结构）。
func (q *querier) GetProject(ctx context.Context, id domain.ID) (domain.Project, error) {
	p, err := q.getProjectRow(ctx, id)
	if err != nil {
		return domain.Project{}, err
	}
	assets, err := q.ListProjectAssets(ctx, id)
	if err != nil {
		return domain.Project{}, err
	}
	tracks, err := q.ListProjectTracks(ctx, id)
	if err != nil {
		return domain.Project{}, err
	}
	runs, err := q.ListProjectRuns(ctx, id)
	if err != nil {
		return domain.Project{}, err
	}
	p.Assets = assets
	p.Tracks = tracks
	p.Runs = runs
	return p, nil
}

// getProjectRow 只读项目行（不级联子对象）。
func (q *querier) getProjectRow(ctx context.Context, id domain.ID) (domain.Project, error) {
	row := q.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, episode_id, name, schema_version, edit_version, created_at, updated_at, last_opened_at
		FROM projects WHERE id = ?`, id)
	p, err := scanProject(row)
	if err != nil {
		return domain.Project{}, wrapNotFound(err)
	}
	return p, nil
}

func scanProject(row rowScanner) (domain.Project, error) {
	var (
		p        domain.Project
		episode  sql.NullString
		created  string
		updated  string
		lastOpen sql.NullString
	)
	if err := row.Scan(&p.ID, &p.WorkspaceID, &episode, &p.Name, &p.SchemaVersion, &p.EditVersion,
		&created, &updated, &lastOpen); err != nil {
		return domain.Project{}, err
	}
	if episode.Valid {
		p.EpisodeID = episode.String
	}
	var err error
	if p.CreatedAt, err = parseTime(created); err != nil {
		return domain.Project{}, err
	}
	if p.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.Project{}, err
	}
	if p.LastOpenedAt, err = parseNullTime(lastOpen); err != nil {
		return domain.Project{}, err
	}
	return p, nil
}

// ListProjectSummaries 返回工作区内的项目摘要（资产数、Run 数与最近 Run 状态）。
func (q *querier) ListProjectSummaries(ctx context.Context, workspaceID domain.ID) ([]domain.ProjectSummary, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT p.seq, p.id, p.name, p.edit_version, p.episode_id, p.created_at, p.updated_at, p.last_opened_at,
			(SELECT COUNT(*) FROM assets a WHERE a.project_id = p.id),
			(SELECT COUNT(*) FROM workflow_runs r WHERE r.project_id = p.id),
			(SELECT r.status FROM workflow_runs r WHERE r.project_id = p.id
				ORDER BY r.created_at DESC, r.id DESC LIMIT 1),
			(SELECT r.mode_type FROM workflow_runs r WHERE r.project_id = p.id
				ORDER BY r.created_at DESC, r.id DESC LIMIT 1),
			(SELECT EXISTS(
				SELECT 1 FROM tasks t WHERE t.status = 'WAITING_RESOURCE' AND t.run_id = (
					SELECT r.id FROM workflow_runs r WHERE r.project_id = p.id
					ORDER BY r.created_at DESC, r.id DESC LIMIT 1
				)
			))
		FROM projects p
		WHERE p.workspace_id = ?
		ORDER BY p.updated_at DESC, p.id`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("query project summaries: %w", err)
	}
	defer rows.Close()

	summaries := make([]domain.ProjectSummary, 0)
	for rows.Next() {
		var (
			s        domain.ProjectSummary
			episode  sql.NullString
			created  string
			updated  string
			lastOpen sql.NullString
			latest   sql.NullString
			modeType sql.NullString
		)
		if err := rows.Scan(&s.Seq, &s.ID, &s.Name, &s.EditVersion, &episode, &created, &updated, &lastOpen,
			&s.AssetCount, &s.RunCount, &latest, &modeType, &s.WaitingResource); err != nil {
			return nil, fmt.Errorf("scan project summary: %w", err)
		}
		if episode.Valid {
			s.EpisodeID = episode.String
		}
		s.ModeType = modeType.String
		if s.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if s.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		if s.LastOpenedAt, err = parseNullTime(lastOpen); err != nil {
			return nil, err
		}
		if latest.Valid {
			s.LatestRunStatus = domain.RunStatus(latest.String)
			if !s.LatestRunStatus.Valid() {
				return nil, fmt.Errorf("project %s has invalid run status %q", s.ID, latest.String)
			}
		}
		summaries = append(summaries, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project summaries: %w", err)
	}
	return summaries, nil
}

// BumpProjectEditVersion 以乐观并发递增编辑版本；expected 不匹配返回 ErrStateConflict。
func (q *querier) BumpProjectEditVersion(ctx context.Context, id domain.ID, expected int64, now time.Time) (int64, error) {
	res, err := q.db.ExecContext(ctx, `
		UPDATE projects SET edit_version = edit_version + 1, updated_at = ?
		WHERE id = ? AND edit_version = ?`, formatTime(now), id, expected)
	if err != nil {
		return 0, fmt.Errorf("bump project edit version: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		if _, err := q.getProjectRow(ctx, id); err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("%w: project %s edit_version != %d", domain.ErrStateConflict, id, expected)
	}
	return expected + 1, nil
}

// UpdateProjectName 更新项目名称并递增编辑版本；expected 必须与当前值一致。
func (q *querier) UpdateProjectName(
	ctx context.Context,
	id domain.ID,
	name string,
	expected int64,
	now time.Time,
) (int64, error) {
	res, err := q.db.ExecContext(ctx, `
		UPDATE projects
		SET name = ?, edit_version = edit_version + 1, updated_at = ?
		WHERE id = ? AND edit_version = ?`,
		name, formatTime(now), id, expected,
	)
	if err != nil {
		return 0, fmt.Errorf("update project name: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		if _, err := q.getProjectRow(ctx, id); err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("%w: project %s edit_version != %d", domain.ErrStateConflict, id, expected)
	}
	return expected + 1, nil
}

// TouchProjectOpened 记录项目最近打开时间（不递增编辑版本）。
func (q *querier) TouchProjectOpened(ctx context.Context, id domain.ID, now time.Time) error {
	res, err := q.db.ExecContext(ctx, `
		UPDATE projects SET last_opened_at = ? WHERE id = ?`, formatTime(now), id)
	if err != nil {
		return fmt.Errorf("touch project: %w", err)
	}
	return affectedOne(res, domain.ErrNotFound)
}

// DeleteProject 删除项目及其级联业务数据；调用方必须先取得用户确认。
func (q *querier) DeleteProject(ctx context.Context, id domain.ID) error {
	res, err := q.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	return affectedOne(res, domain.ErrNotFound)
}
