package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

const taskSelect = `
	SELECT id, project_id, run_id, kind, node_key, segment_id, language, label, status,
		progress, state_version, steps_json, input_revision_ids_json, input_digest,
		params_json, model_lock_json, artifact_ids_json, error_json, created_at, updated_at
	FROM tasks`

// CreateTask 登记可调度任务（steps 作为任务聚合的一部分保存）。
func (q *querier) CreateTask(ctx context.Context, task domain.Task) error {
	if task.ID == "" || task.ProjectID == "" {
		return fmt.Errorf("%w: task id/project_id required", domain.ErrInvalidRequest)
	}
	if !task.Kind.Valid() {
		return fmt.Errorf("%w: invalid task kind %q", domain.ErrInvalidRequest, task.Kind)
	}
	if !task.Status.Valid() {
		return fmt.Errorf("%w: invalid task status %q", domain.ErrInvalidRequest, task.Status)
	}
	if task.NodeKey != "" && !task.NodeKey.Valid() {
		return fmt.Errorf("%w: invalid node key %q", domain.ErrInvalidRequest, task.NodeKey)
	}
	if task.Progress < 0 || task.Progress > 100 {
		return fmt.Errorf("%w: task progress %d out of range", domain.ErrInvalidRequest, task.Progress)
	}
	if task.CreatedAt.IsZero() || task.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: task timestamps required", domain.ErrInvalidRequest)
	}
	for _, step := range task.Steps {
		if step.StepID == "" || !step.Status.Valid() {
			return fmt.Errorf("%w: invalid task step %q/%q", domain.ErrInvalidRequest, step.StepID, step.Status)
		}
	}
	stateVersion := task.StateVersion
	if stateVersion <= 0 {
		stateVersion = 1
	}
	steps, err := marshalJSONSteps(task.Steps)
	if err != nil {
		return err
	}
	inputRevisions, err := marshalJSONList(task.InputRevisionIDs)
	if err != nil {
		return err
	}
	artifactIDs, err := marshalJSONList(task.ArtifactIDs)
	if err != nil {
		return err
	}
	var params any
	if task.Params != nil {
		params, err = marshalJSON(task.Params)
		if err != nil {
			return err
		}
	}
	modelLock, err := releaseRefJSON(task.ModelLock)
	if err != nil {
		return err
	}
	errorInfo, err := errorInfoJSON(task.Error)
	if err != nil {
		return err
	}

	_, err = q.db.ExecContext(ctx, `
		INSERT INTO tasks
			(id, project_id, run_id, kind, node_key, segment_id, language, label, status,
			 progress, state_version, steps_json, input_revision_ids_json, input_digest,
			 params_json, model_lock_json, artifact_ids_json, error_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.ProjectID, nullString(task.RunID), string(task.Kind),
		nullString(string(task.NodeKey)), nullString(task.SegmentID), nullString(task.Language),
		nullString(task.Label), string(task.Status), task.Progress, stateVersion,
		steps, inputRevisions, nullString(task.InputDigest), params, modelLock,
		artifactIDs, errorInfo, formatTime(task.CreatedAt), formatTime(task.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	return nil
}

// GetTask 读取任务详情（含步骤）。
func (q *querier) GetTask(ctx context.Context, id domain.ID) (domain.Task, error) {
	row := q.db.QueryRowContext(ctx, taskSelect+` WHERE id = ?`, id)
	task, err := scanTask(row, true)
	if err != nil {
		return domain.Task{}, wrapNotFound(err)
	}
	return task, nil
}

// LatestSucceededNodeTask 返回 Run 中指定节点最近一次成功的任务。
//
// 交付导出门禁用它重算 08 交付摘要：只有已提交成功的任务构成可导出产物，
// 失败/运行中的尝试不被当作最新修订。language 非空时优先取该语种的产物，
// 并在没有该语种任务时回退到未标注语言的单语节点任务（宁可回退，不能把
// 其他语种的混音当作本语种的 08 审核依据）。
func (q *querier) LatestSucceededNodeTask(ctx context.Context, runID domain.ID, nodeKey domain.PipelineNodeKey, language string) (domain.Task, bool, error) {
	row := q.db.QueryRowContext(ctx, taskSelect+`
		WHERE run_id = ? AND node_key = ? AND kind = ? AND status = ?
		  AND (? = '' OR language IS NULL OR language = '' OR language = ?)
		ORDER BY CASE WHEN language = ? THEN 0 ELSE 1 END,
			updated_at DESC, created_at DESC, id DESC
		LIMIT 1`,
		runID, string(nodeKey), string(domain.TaskKindNode), string(domain.TaskStatusSucceeded),
		language, language, language)
	task, err := scanTask(row, false)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.Task{}, false, nil
		}
		return domain.Task{}, false, err
	}
	return task, true, nil
}

// ListProjectTasks 列出项目任务；列表不带完整步骤历史（契约 TaskList 语义）。
func (q *querier) ListProjectTasks(ctx context.Context, projectID domain.ID) ([]domain.Task, error) {
	rows, err := q.db.QueryContext(ctx, taskSelect+` WHERE project_id = ? ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query project tasks: %w", err)
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

func scanTask(row rowScanner, withSteps bool) (domain.Task, error) {
	var (
		task        domain.Task
		runID       sql.NullString
		kind        string
		nodeKey     sql.NullString
		segmentID   sql.NullString
		language    sql.NullString
		label       sql.NullString
		status      string
		steps       sql.NullString
		inputRevs   sql.NullString
		inputHash   sql.NullString
		params      sql.NullString
		modelLock   sql.NullString
		artifactIDs sql.NullString
		errorInfo   sql.NullString
		created     string
		updated     string
	)
	if err := row.Scan(&task.ID, &task.ProjectID, &runID, &kind, &nodeKey, &segmentID,
		&language, &label, &status, &task.Progress, &task.StateVersion, &steps, &inputRevs,
		&inputHash, &params, &modelLock, &artifactIDs, &errorInfo, &created, &updated); err != nil {
		return domain.Task{}, err
	}
	task.Kind = domain.TaskKind(kind)
	if !task.Kind.Valid() {
		return domain.Task{}, fmt.Errorf("task %s has invalid kind %q", task.ID, kind)
	}
	task.Status = domain.TaskStatus(status)
	if !task.Status.Valid() {
		return domain.Task{}, fmt.Errorf("task %s has invalid status %q", task.ID, status)
	}
	if runID.Valid {
		task.RunID = runID.String
	}
	if nodeKey.Valid {
		task.NodeKey = domain.PipelineNodeKey(nodeKey.String)
		if !task.NodeKey.Valid() {
			return domain.Task{}, fmt.Errorf("task %s has invalid node key %q", task.ID, nodeKey.String)
		}
	}
	if segmentID.Valid {
		task.SegmentID = segmentID.String
	}
	if language.Valid {
		task.Language = language.String
	}
	if label.Valid {
		task.Label = label.String
	}
	if inputHash.Valid {
		task.InputDigest = inputHash.String
	}
	var err error
	if task.CreatedAt, err = parseTime(created); err != nil {
		return domain.Task{}, err
	}
	if task.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.Task{}, err
	}
	if withSteps && steps.Valid {
		if err := unmarshalNull(steps, &task.Steps); err != nil {
			return domain.Task{}, err
		}
	}
	if err := unmarshalNull(inputRevs, &task.InputRevisionIDs); err != nil {
		return domain.Task{}, err
	}
	if err := unmarshalNull(params, &task.Params); err != nil {
		return domain.Task{}, err
	}
	if err := unmarshalNull(artifactIDs, &task.ArtifactIDs); err != nil {
		return domain.Task{}, err
	}
	if modelLock.Valid {
		var ref domain.ReleaseRef
		if err := unmarshalNull(modelLock, &ref); err != nil {
			return domain.Task{}, err
		}
		task.ModelLock = &ref
	}
	if errorInfo.Valid {
		var info domain.ErrorInfo
		if err := unmarshalNull(errorInfo, &info); err != nil {
			return domain.Task{}, err
		}
		task.Error = &info
	}
	if !withSteps {
		task.Steps = nil
	}
	return task, nil
}

// UpdateTaskStatus 以状态版本 CAS 迁移任务状态，相同状态视为幂等 no-op。
func (tx *Tx) UpdateTaskStatus(ctx context.Context, id domain.ID, to domain.TaskStatus, expectedVersion int64, now time.Time) (domain.Task, error) {
	if !to.Valid() {
		return domain.Task{}, fmt.Errorf("%w: invalid task status %q", domain.ErrInvalidRequest, to)
	}
	var (
		current string
		version int64
	)
	row := tx.db.QueryRowContext(ctx, `SELECT status, state_version FROM tasks WHERE id = ?`, id)
	if err := row.Scan(&current, &version); err != nil {
		return domain.Task{}, wrapNotFound(err)
	}
	if version != expectedVersion {
		return domain.Task{}, fmt.Errorf("%w: task %s state_version %d != %d",
			domain.ErrStateConflict, id, version, expectedVersion)
	}
	from := domain.TaskStatus(current)
	if !from.Valid() {
		return domain.Task{}, fmt.Errorf("task %s has invalid status %q", id, current)
	}
	if from == to {
		return tx.GetTask(ctx, id)
	}
	if _, err := domain.TransitionTask(from, to); err != nil {
		return domain.Task{}, err
	}
	res, err := tx.db.ExecContext(ctx, `
		UPDATE tasks SET status = ?, state_version = state_version + 1, updated_at = ?
		WHERE id = ? AND state_version = ?`,
		string(to), formatTime(now), id, expectedVersion)
	if err != nil {
		return domain.Task{}, fmt.Errorf("update task status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.Task{}, fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return domain.Task{}, fmt.Errorf("%w: task %s updated concurrently", domain.ErrStateConflict, id)
	}
	return tx.GetTask(ctx, id)
}

// Store 包装：在独立事务中迁移任务状态。
func (s *Store) UpdateTaskStatus(ctx context.Context, id domain.ID, to domain.TaskStatus, expectedVersion int64, now time.Time) (domain.Task, error) {
	var task domain.Task
	err := s.DoTx(ctx, func(tx *Tx) error {
		updated, err := tx.UpdateTaskStatus(ctx, id, to, expectedVersion, now)
		if err != nil {
			return err
		}
		task = updated
		return nil
	})
	if err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

// UpdateTaskResult 在同一 CAS 更新中写入状态迁移、产物与错误。
//
// 调度层用它提交终态：状态版本与产物清单一起成功或一起失败，
// 已成功提交的产物不会因为后续错误码写入而部分丢失。
// 终态（SUCCEEDED/CANCELED）不可变：同终态重复提交只回放当前快照，
// 不改产物、不递增状态版本；迟到结果不能改写已提交终态。
func (tx *Tx) UpdateTaskResult(ctx context.Context, id domain.ID, to domain.TaskStatus, artifactIDs []domain.ID, errorInfo *domain.ErrorInfo, expectedVersion int64, now time.Time) (domain.Task, error) {
	if !to.Valid() {
		return domain.Task{}, fmt.Errorf("%w: invalid task status %q", domain.ErrInvalidRequest, to)
	}
	var (
		current string
		version int64
	)
	row := tx.db.QueryRowContext(ctx, `SELECT status, state_version FROM tasks WHERE id = ?`, id)
	if err := row.Scan(&current, &version); err != nil {
		return domain.Task{}, wrapNotFound(err)
	}
	if version != expectedVersion {
		return domain.Task{}, fmt.Errorf("%w: task %s state_version %d != %d",
			domain.ErrStateConflict, id, version, expectedVersion)
	}
	from := domain.TaskStatus(current)
	if !from.Valid() {
		return domain.Task{}, fmt.Errorf("task %s has invalid status %q", id, current)
	}
	if from == to {
		if from.Terminal() {
			return tx.GetTask(ctx, id)
		}
	} else if _, err := domain.TransitionTask(from, to); err != nil {
		return domain.Task{}, err
	}
	encodedArtifacts, err := marshalJSONList(artifactIDs)
	if err != nil {
		return domain.Task{}, err
	}
	encodedError, err := errorInfoJSON(errorInfo)
	if err != nil {
		return domain.Task{}, err
	}
	// 进度快照与状态迁移在同一事务内收敛：新受理/认领从 0 开始，
	// 成功终态写 100；失败/取消/中断保留最后一次合法进度。
	// from==to 的重复提交不改进度，避免把幂等回放当成新尝试。
	progress := -1
	if from != to {
		switch to {
		case domain.TaskStatusQueued, domain.TaskStatusRunning:
			progress = 0
		case domain.TaskStatusSucceeded:
			progress = 100
		}
	}
	res, err := tx.db.ExecContext(ctx, `
		UPDATE tasks
		SET status = ?, artifact_ids_json = ?, error_json = ?, updated_at = ?,
			progress = CASE WHEN ? >= 0 THEN ? ELSE progress END,
			state_version = state_version + 1
		WHERE id = ? AND state_version = ?`,
		string(to), encodedArtifacts, encodedError, formatTime(now), progress, progress, id, expectedVersion)
	if err != nil {
		return domain.Task{}, fmt.Errorf("update task result: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.Task{}, fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return domain.Task{}, fmt.Errorf("%w: task %s updated concurrently", domain.ErrStateConflict, id)
	}
	return tx.GetTask(ctx, id)
}

// Store 包装：在独立事务中提交任务结果。
func (s *Store) UpdateTaskResult(ctx context.Context, id domain.ID, to domain.TaskStatus, artifactIDs []domain.ID, errorInfo *domain.ErrorInfo, expectedVersion int64, now time.Time) (domain.Task, error) {
	var task domain.Task
	err := s.DoTx(ctx, func(tx *Tx) error {
		updated, err := tx.UpdateTaskResult(ctx, id, to, artifactIDs, errorInfo, expectedVersion, now)
		if err != nil {
			return err
		}
		task = updated
		return nil
	})
	if err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

// SetTaskProgress 更新可丢弃的进度反馈；不递增状态版本、不产生持久事件。
func (q *querier) SetTaskProgress(ctx context.Context, id domain.ID, progress int, now time.Time) error {
	if progress < 0 || progress > 100 {
		return fmt.Errorf("%w: task progress %d out of range", domain.ErrInvalidRequest, progress)
	}
	res, err := q.db.ExecContext(ctx, `UPDATE tasks SET progress = ?, updated_at = ? WHERE id = ?`,
		progress, formatTime(now), id)
	if err != nil {
		return fmt.Errorf("update task progress: %w", err)
	}
	return affectedOne(res, domain.ErrNotFound)
}

// ProgressUpdate 是一次执行尝试的进度快照身份与目标值。
//
// AttemptID/StepID/AttemptNo/Epoch 必须精确对应仍 RUNNING 的最新尝试；
// 只用 TaskID 会覆写已重试或已终态的任务。
type ProgressUpdate struct {
	TaskID    domain.ID
	AttemptID domain.ID
	StepID    string
	AttemptNo int
	Epoch     int64
	Progress  int
	Now       time.Time
}

// TaskProgressResult 报告条件更新是否生效与命中行的状态版本。
//
// Applied=false 表示任务/尝试不再匹配或进度没有前进（stale/no-op），
// 不是基础设施错误，也不会发布任何反馈。
type TaskProgressResult struct {
	Applied      bool
	StateVersion int64
}

// UpdateTaskProgress 在单个事务内条件更新当前 RUNNING 尝试的进度快照。
//
// 条件：任务 RUNNING、指定尝试是该任务最新且 RUNNING、身份四元组精确匹配、
// 新进度严格大于旧值。更新不递增 state_version，也不追加持久事件；
// Applied=true 时返回同一行的 state_version，供可丢弃事件标注快照版本。
func (s *Store) UpdateTaskProgress(ctx context.Context, update ProgressUpdate) (TaskProgressResult, error) {
	if update.TaskID == "" || update.AttemptID == "" || update.StepID == "" ||
		update.AttemptNo <= 0 || update.Epoch <= 0 {
		return TaskProgressResult{}, fmt.Errorf("%w: incomplete progress binding", domain.ErrInvalidRequest)
	}
	if update.Progress < 0 || update.Progress > 100 {
		return TaskProgressResult{}, fmt.Errorf("%w: task progress %d out of range", domain.ErrInvalidRequest, update.Progress)
	}
	var result TaskProgressResult
	err := s.DoTx(ctx, func(tx *Tx) error {
		res, err := tx.db.ExecContext(ctx, `
			UPDATE tasks
			SET progress = ?, updated_at = ?
			WHERE id = ? AND status = ? AND progress < ?
			  AND EXISTS (
				SELECT 1 FROM attempts
				WHERE attempts.task_id = ? AND attempts.id = ? AND attempts.step_id = ?
				  AND attempts.attempt_no = ? AND attempts.epoch = ? AND attempts.status = ?
				  AND attempts.attempt_no = (
					SELECT MAX(a.attempt_no) FROM attempts AS a WHERE a.task_id = ?
				  )
			  )`,
			update.Progress, formatTime(update.Now),
			update.TaskID, string(domain.TaskStatusRunning), update.Progress,
			update.TaskID, update.AttemptID, update.StepID,
			update.AttemptNo, update.Epoch, string(domain.StepStatusRunning),
			update.TaskID)
		if err != nil {
			return fmt.Errorf("update task progress: %w", err)
		}
		applied, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("rows affected: %w", err)
		}
		if applied == 0 {
			return nil
		}
		if err := tx.db.QueryRowContext(ctx,
			`SELECT state_version FROM tasks WHERE id = ?`, update.TaskID).Scan(&result.StateVersion); err != nil {
			return fmt.Errorf("read progress state version: %w", err)
		}
		result.Applied = true
		return nil
	})
	if err != nil {
		return TaskProgressResult{}, err
	}
	return result, nil
}

// SetTaskSteps 覆盖任务步骤计划（持久状态，调用方负责与状态版本协调）。
func (q *querier) SetTaskSteps(ctx context.Context, id domain.ID, steps []domain.TaskStep, now time.Time) error {
	for _, step := range steps {
		if step.StepID == "" || !step.Status.Valid() {
			return fmt.Errorf("%w: invalid task step %q/%q", domain.ErrInvalidRequest, step.StepID, step.Status)
		}
	}
	encoded, err := marshalJSONSteps(steps)
	if err != nil {
		return err
	}
	res, err := q.db.ExecContext(ctx, `UPDATE tasks SET steps_json = ?, updated_at = ? WHERE id = ?`,
		encoded, formatTime(now), id)
	if err != nil {
		return fmt.Errorf("update task steps: %w", err)
	}
	return affectedOne(res, domain.ErrNotFound)
}

// CreateAttempt 登记一次执行尝试；同一 task/step 的 attempt_no 单调且唯一。
func (q *querier) CreateAttempt(ctx context.Context, attempt domain.Attempt) error {
	if attempt.ID == "" || attempt.TaskID == "" || attempt.StepID == "" {
		return fmt.Errorf("%w: attempt id/task_id/step_id required", domain.ErrInvalidRequest)
	}
	if attempt.AttemptNo <= 0 {
		return fmt.Errorf("%w: attempt_no must be >= 1", domain.ErrInvalidRequest)
	}
	if !attempt.Status.Valid() {
		return fmt.Errorf("%w: invalid attempt status %q", domain.ErrInvalidRequest, attempt.Status)
	}
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO attempts
			(id, task_id, step_id, attempt_no, epoch, executor_id, status, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.ID, attempt.TaskID, attempt.StepID, attempt.AttemptNo, attempt.Epoch,
		nullString(attempt.ExecutorID), string(attempt.Status),
		nullTimeValue(attempt.StartedAt), nullTimeValue(attempt.FinishedAt),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: attempt %s/%d already exists", domain.ErrStateConflict, attempt.StepID, attempt.AttemptNo)
		}
		return fmt.Errorf("insert attempt: %w", err)
	}
	return nil
}

// ListAttempts 返回任务的尝试记录（按 attempt_no）。
func (q *querier) ListAttempts(ctx context.Context, taskID domain.ID) ([]domain.Attempt, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT id, task_id, step_id, attempt_no, epoch, executor_id, status, started_at, finished_at
		FROM attempts WHERE task_id = ? ORDER BY attempt_no, id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("query attempts: %w", err)
	}
	defer rows.Close()

	attempts := make([]domain.Attempt, 0)
	for rows.Next() {
		var (
			a        domain.Attempt
			executor sql.NullString
			status   string
			started  sql.NullString
			finished sql.NullString
		)
		if err := rows.Scan(&a.ID, &a.TaskID, &a.StepID, &a.AttemptNo, &a.Epoch,
			&executor, &status, &started, &finished); err != nil {
			return nil, fmt.Errorf("scan attempt: %w", err)
		}
		a.Status = domain.StepStatus(status)
		if !a.Status.Valid() {
			return nil, fmt.Errorf("attempt %s has invalid status %q", a.ID, status)
		}
		if executor.Valid {
			a.ExecutorID = executor.String
		}
		var err error
		if a.StartedAt, err = parseNullTime(started); err != nil {
			return nil, err
		}
		if a.FinishedAt, err = parseNullTime(finished); err != nil {
			return nil, err
		}
		attempts = append(attempts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate attempts: %w", err)
	}
	return attempts, nil
}

// CreateArtifactCommit 登记产物提交（控制面确认后才可复用）。
func (q *querier) CreateArtifactCommit(ctx context.Context, commit domain.ArtifactCommit) error {
	if commit.ID == "" || commit.AttemptID == "" {
		return fmt.Errorf("%w: artifact commit id/attempt_id required", domain.ErrInvalidRequest)
	}
	if !commit.Status.Valid() {
		return fmt.Errorf("%w: invalid commit status %q", domain.ErrInvalidRequest, commit.Status)
	}
	assetIDs, err := marshalJSONList(commit.AssetIDs)
	if err != nil {
		return err
	}
	_, err = q.db.ExecContext(ctx, `
		INSERT INTO artifact_commits (id, attempt_id, asset_ids_json, status, committed_at)
		VALUES (?, ?, ?, ?, ?)`,
		commit.ID, commit.AttemptID, assetIDs, string(commit.Status),
		nullTimeValue(commit.CommittedAt),
	)
	if err != nil {
		return fmt.Errorf("insert artifact commit: %w", err)
	}
	return nil
}

// ListArtifactCommits 返回某次尝试的产物提交记录。
func (q *querier) ListArtifactCommits(ctx context.Context, attemptID domain.ID) ([]domain.ArtifactCommit, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT id, attempt_id, asset_ids_json, status, committed_at
		FROM artifact_commits WHERE attempt_id = ? ORDER BY id`, attemptID)
	if err != nil {
		return nil, fmt.Errorf("query artifact commits: %w", err)
	}
	defer rows.Close()

	commits := make([]domain.ArtifactCommit, 0)
	for rows.Next() {
		var (
			c           domain.ArtifactCommit
			assetIDs    string
			status      string
			committedAt sql.NullString
		)
		if err := rows.Scan(&c.ID, &c.AttemptID, &assetIDs, &status, &committedAt); err != nil {
			return nil, fmt.Errorf("scan artifact commit: %w", err)
		}
		c.Status = domain.CommitStatus(status)
		if !c.Status.Valid() {
			return nil, fmt.Errorf("artifact commit %s has invalid status %q", c.ID, status)
		}
		if err := unmarshalNull(sql.NullString{String: assetIDs, Valid: true}, &c.AssetIDs); err != nil {
			return nil, err
		}
		var err error
		if c.CommittedAt, err = parseNullTime(committedAt); err != nil {
			return nil, err
		}
		commits = append(commits, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifact commits: %w", err)
	}
	return commits, nil
}

// marshalJSONSteps 编码步骤列表；nil 归一为 []。
func marshalJSONSteps(steps []domain.TaskStep) (string, error) {
	if steps == nil {
		steps = []domain.TaskStep{}
	}
	return marshalJSON(steps)
}
