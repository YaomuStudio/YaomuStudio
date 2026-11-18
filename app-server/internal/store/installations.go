package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

const installationSelect = `
	SELECT id, slot, phase, progress, verified_files, error_json, retryable, plan_id,
		plan_hash, release_json, probe_passed, path_owner, storage_key, covered_slots_json,
		manifest_json, created_at, updated_at
	FROM installations`

// InstallationBinding 是槽位对安装实体的一条显式引用（当前或保留历史）。
type InstallationBinding struct {
	InstallationID domain.ID
	Slot           domain.CapabilitySlotKey
	Current        bool
	ActivatedAt    time.Time
}

// CreateInstallation 登记安装记录及其槽位引用（同一事务）。
//
// 覆盖槽位来自冻结计划；旧单 slot 测试/兼容写入没有覆盖时只建立原槽位引用。
// 直接创建 Done 记录（旧测试/修复路径）沿用旧语义：该槽位当前选择切到本记录。
func (q *querier) CreateInstallation(ctx context.Context, inst domain.Installation) error {
	if inst.ID == "" {
		return fmt.Errorf("%w: installation id required", domain.ErrInvalidRequest)
	}
	if !inst.Slot.Valid() {
		return fmt.Errorf("%w: invalid installation slot %q", domain.ErrInvalidRequest, inst.Slot)
	}
	if !inst.Phase.Valid() {
		return fmt.Errorf("%w: invalid installation phase %q", domain.ErrInvalidRequest, inst.Phase)
	}
	if inst.Progress < 0 || inst.Progress > 100 {
		return fmt.Errorf("%w: installation progress %d out of range", domain.ErrInvalidRequest, inst.Progress)
	}
	if inst.CreatedAt.IsZero() || inst.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: installation timestamps required", domain.ErrInvalidRequest)
	}
	covered, err := normalizeInstallationSlots(inst)
	if err != nil {
		return err
	}
	if inst.StorageKey != "" {
		if inst.Release == nil {
			return fmt.Errorf("%w: new installation requires a release", domain.ErrInvalidRequest)
		}
		if _, err := domain.ReleaseStorageKey(*inst.Release); err != nil {
			return fmt.Errorf("%w: new installation requires a complete release", domain.ErrInvalidRequest)
		}
	}
	if err := validateInstallationManifest(inst.Manifest); err != nil {
		return err
	}
	errorInfo, err := errorInfoJSON(inst.Error)
	if err != nil {
		return err
	}
	release, err := releaseRefJSON(inst.Release)
	if err != nil {
		return err
	}
	var coveredJSON any
	if len(inst.CoveredSlots) > 0 {
		coveredJSON, err = marshalJSON(covered)
		if err != nil {
			return err
		}
	}
	var manifestJSON any
	if len(inst.Manifest) > 0 {
		manifestJSON, err = marshalJSON(inst.Manifest)
		if err != nil {
			return err
		}
	}
	_, err = q.db.ExecContext(ctx, `
		INSERT INTO installations
			(id, slot, phase, progress, verified_files, error_json, retryable, plan_id,
			 plan_hash, release_json, probe_passed, path_owner, storage_key,
			 covered_slots_json, manifest_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		inst.ID, string(inst.Slot), string(inst.Phase), inst.Progress, inst.VerifiedFiles,
		errorInfo, boolInt(inst.Retryable), nullString(inst.PlanID), nullString(inst.PlanHash),
		release, boolInt(inst.ProbePassed), nullString(inst.PathOwner),
		nullString(inst.StorageKey), coveredJSON, manifestJSON,
		formatTime(inst.CreatedAt), formatTime(inst.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert installation: %w", err)
	}
	for _, slot := range covered {
		current := inst.Phase == domain.InstallationPhaseDone
		if current {
			if err := clearCurrentBinding(ctx, q.db, slot, inst.ID); err != nil {
				return err
			}
		}
		if _, err := q.db.ExecContext(ctx, `
			INSERT INTO installation_slot_bindings (installation_id, slot, is_current, activated_at)
			VALUES (?, ?, ?, ?)`,
			inst.ID, string(slot), boolInt(current), formatTime(inst.CreatedAt)); err != nil {
			return fmt.Errorf("insert installation binding: %w", err)
		}
	}
	return nil
}

func normalizeInstallationSlots(inst domain.Installation) ([]domain.CapabilitySlotKey, error) {
	slots := inst.CoveredSlots
	if len(slots) == 0 {
		slots = []domain.CapabilitySlotKey{inst.Slot}
	}
	normalized, err := domain.NormalizeCapabilitySlots(slots)
	if err != nil {
		return nil, err
	}
	if !domain.ContainsCapabilitySlot(normalized, inst.Slot) {
		return nil, fmt.Errorf("%w: covered slots must include the initiating slot %q",
			domain.ErrInvalidRequest, inst.Slot)
	}
	return normalized, nil
}

func validateInstallationManifest(files []domain.InstallationFile) error {
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		if file.RelPath == "" || file.SHA256 == "" || file.Size < 0 {
			return fmt.Errorf("%w: invalid installation manifest entry", domain.ErrInvalidRequest)
		}
		if _, ok := seen[file.RelPath]; ok {
			return fmt.Errorf("%w: duplicate manifest rel_path %q", domain.ErrInvalidRequest, file.RelPath)
		}
		seen[file.RelPath] = struct{}{}
	}
	return nil
}

// GetInstallation 读取安装记录及其槽位引用投影。
func (q *querier) GetInstallation(ctx context.Context, id domain.ID) (domain.Installation, error) {
	row := q.db.QueryRowContext(ctx, installationSelect+` WHERE id = ?`, id)
	inst, err := scanInstallation(row)
	if err != nil {
		return domain.Installation{}, wrapNotFound(err)
	}
	bindings, err := q.listInstallationBindings(ctx)
	if err != nil {
		return domain.Installation{}, err
	}
	return projectInstallation(inst, bindings), nil
}

// ListInstallations 列出工作区安装记录（最新在前）及其槽位引用投影。
func (q *querier) ListInstallations(ctx context.Context) ([]domain.Installation, error) {
	rows, err := q.db.QueryContext(ctx, installationSelect+` ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("query installations: %w", err)
	}
	defer rows.Close()

	installs := make([]domain.Installation, 0)
	for rows.Next() {
		inst, err := scanInstallation(rows)
		if err != nil {
			return nil, err
		}
		installs = append(installs, inst)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate installations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close installations: %w", err)
	}
	bindings, err := q.listInstallationBindings(ctx)
	if err != nil {
		return nil, err
	}
	return projectInstallations(installs, bindings), nil
}

func (q *querier) listInstallationBindings(ctx context.Context) ([]InstallationBinding, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT installation_id, slot, is_current, activated_at
		FROM installation_slot_bindings
		ORDER BY activated_at DESC, installation_id, slot`)
	if err != nil {
		return nil, fmt.Errorf("query installation bindings: %w", err)
	}
	defer rows.Close()

	bindings := make([]InstallationBinding, 0)
	for rows.Next() {
		var (
			binding   InstallationBinding
			slot      string
			current   int
			activated string
		)
		if err := rows.Scan(&binding.InstallationID, &slot, &current, &activated); err != nil {
			return nil, fmt.Errorf("scan installation binding: %w", err)
		}
		binding.Slot = domain.CapabilitySlotKey(slot)
		if !binding.Slot.Valid() {
			return nil, fmt.Errorf("installation %s has invalid binding slot %q", binding.InstallationID, slot)
		}
		binding.Current = current != 0
		t, err := parseTime(activated)
		if err != nil {
			return nil, err
		}
		binding.ActivatedAt = t
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate installation bindings: %w", err)
	}
	return bindings, nil
}

// applyBindings 把引用折入安装记录的 bound/current 投影（未排序）。
func applyBindings(installs []domain.Installation, bindings []InstallationBinding) {
	byID := make(map[domain.ID]int, len(installs))
	for i := range installs {
		byID[installs[i].ID] = i
		installs[i].BoundSlots = []domain.CapabilitySlotKey{}
		installs[i].CurrentSlots = []domain.CapabilitySlotKey{}
	}
	for _, binding := range bindings {
		index, ok := byID[binding.InstallationID]
		if !ok {
			continue
		}
		installs[index].BoundSlots = append(installs[index].BoundSlots, binding.Slot)
		if binding.Current {
			installs[index].CurrentSlots = append(installs[index].CurrentSlots, binding.Slot)
		}
	}
}

func projectInstallation(inst domain.Installation, bindings []InstallationBinding) domain.Installation {
	installs := []domain.Installation{inst}
	applyBindings(installs, bindings)
	return projectInstallations(installs, nil)[0]
}

func projectInstallations(installs []domain.Installation, bindings []InstallationBinding) []domain.Installation {
	if bindings != nil {
		applyBindings(installs, bindings)
	}
	for i := range installs {
		installs[i].BoundSlots = domain.SortCapabilitySlots(installs[i].BoundSlots)
		installs[i].CurrentSlots = domain.SortCapabilitySlots(installs[i].CurrentSlots)
	}
	return installs
}

func scanInstallation(row rowScanner) (domain.Installation, error) {
	var (
		inst        domain.Installation
		slot        string
		phase       string
		errorJSON   sql.NullString
		retryable   int
		planID      sql.NullString
		planHash    sql.NullString
		release     sql.NullString
		probe       int
		pathOwner   sql.NullString
		storageKey  sql.NullString
		coveredJSON sql.NullString
		manifest    sql.NullString
		created     string
		updated     string
	)
	if err := row.Scan(&inst.ID, &slot, &phase, &inst.Progress, &inst.VerifiedFiles,
		&errorJSON, &retryable, &planID, &planHash, &release, &probe, &pathOwner,
		&storageKey, &coveredJSON, &manifest, &created, &updated); err != nil {
		return domain.Installation{}, err
	}
	inst.Slot = domain.CapabilitySlotKey(slot)
	if !inst.Slot.Valid() {
		return domain.Installation{}, fmt.Errorf("installation %s has invalid slot %q", inst.ID, slot)
	}
	inst.Phase = domain.InstallationPhase(phase)
	if !inst.Phase.Valid() {
		return domain.Installation{}, fmt.Errorf("installation %s has invalid phase %q", inst.ID, phase)
	}
	inst.Retryable = retryable != 0
	inst.ProbePassed = probe != 0
	if errorJSON.Valid {
		var info domain.ErrorInfo
		if err := unmarshalNull(errorJSON, &info); err != nil {
			return domain.Installation{}, err
		}
		inst.Error = &info
	}
	if planID.Valid {
		inst.PlanID = planID.String
	}
	if planHash.Valid {
		inst.PlanHash = planHash.String
	}
	if release.Valid {
		var ref domain.ReleaseRef
		if err := unmarshalNull(release, &ref); err != nil {
			return domain.Installation{}, err
		}
		inst.Release = &ref
	}
	if pathOwner.Valid {
		inst.PathOwner = pathOwner.String
	}
	if storageKey.Valid {
		inst.StorageKey = storageKey.String
	}
	if coveredJSON.Valid {
		var covered []domain.CapabilitySlotKey
		if err := unmarshalNull(coveredJSON, &covered); err != nil {
			return domain.Installation{}, err
		}
		normalized, err := domain.NormalizeCapabilitySlots(covered)
		if err != nil {
			return domain.Installation{}, fmt.Errorf("installation %s covered slots: %w", inst.ID, err)
		}
		inst.CoveredSlots = normalized
	} else {
		// 旧记录只暴露原单 slot（历史事实，不扩覆盖）。
		inst.CoveredSlots = []domain.CapabilitySlotKey{inst.Slot}
	}
	if manifest.Valid {
		if err := unmarshalNull(manifest, &inst.Manifest); err != nil {
			return domain.Installation{}, err
		}
	}
	if err := validateInstallationManifest(inst.Manifest); err != nil {
		return domain.Installation{}, fmt.Errorf("installation %s manifest: %w", inst.ID, err)
	}
	var err error
	if inst.CreatedAt, err = parseTime(created); err != nil {
		return domain.Installation{}, err
	}
	if inst.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.Installation{}, err
	}
	return inst, nil
}

// UpdateInstallationPhase 迁移安装阶段并更新真实进度；同阶段视为进度更新。
func (tx *Tx) UpdateInstallationPhase(ctx context.Context, id domain.ID, to domain.InstallationPhase, progress, verifiedFiles int, errorInfo *domain.ErrorInfo, retryable bool, now time.Time) (domain.Installation, error) {
	if !to.Valid() {
		return domain.Installation{}, fmt.Errorf("%w: invalid installation phase %q", domain.ErrInvalidRequest, to)
	}
	if progress < 0 || progress > 100 {
		return domain.Installation{}, fmt.Errorf("%w: installation progress %d out of range", domain.ErrInvalidRequest, progress)
	}
	var current string
	row := tx.db.QueryRowContext(ctx, `SELECT phase FROM installations WHERE id = ?`, id)
	if err := row.Scan(&current); err != nil {
		return domain.Installation{}, wrapNotFound(err)
	}
	from := domain.InstallationPhase(current)
	if !from.Valid() {
		return domain.Installation{}, fmt.Errorf("installation %s has invalid phase %q", id, current)
	}
	if from != to {
		if _, err := domain.TransitionInstallationPhase(from, to); err != nil {
			return domain.Installation{}, err
		}
	}
	encoded, err := errorInfoJSON(errorInfo)
	if err != nil {
		return domain.Installation{}, err
	}
	res, err := tx.db.ExecContext(ctx, `
		UPDATE installations
		SET phase = ?, progress = ?, verified_files = ?, error_json = ?, retryable = ?, updated_at = ?
		WHERE id = ?`,
		string(to), progress, verifiedFiles, encoded, boolInt(retryable), formatTime(now), id)
	if err != nil {
		return domain.Installation{}, fmt.Errorf("update installation: %w", err)
	}
	if err := affectedOne(res, domain.ErrNotFound); err != nil {
		return domain.Installation{}, err
	}
	return tx.GetInstallation(ctx, id)
}

// SetInstallationProgress 只在阶段仍是 phase 时更新进度。
// 阶段已经离开时返回 false，避免把完成记录改回安装中。
func (tx *Tx) SetInstallationProgress(ctx context.Context, id domain.ID, phase domain.InstallationPhase, progress, verifiedFiles int, now time.Time) (domain.Installation, bool, error) {
	if progress < 0 || progress > 100 {
		return domain.Installation{}, false, fmt.Errorf("%w: installation progress %d out of range", domain.ErrInvalidRequest, progress)
	}
	res, err := tx.db.ExecContext(ctx, `
		UPDATE installations
		SET progress = ?, verified_files = ?, updated_at = ?
		WHERE id = ? AND phase = ?`,
		progress, verifiedFiles, formatTime(now), id, string(phase))
	if err != nil {
		return domain.Installation{}, false, fmt.Errorf("update installation progress: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.Installation{}, false, fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return domain.Installation{}, false, nil
	}
	inst, err := tx.GetInstallation(ctx, id)
	if err != nil {
		return domain.Installation{}, false, err
	}
	return inst, true, nil
}

// SetCurrentBindings 在同一事务内把槽位当前引用切到指定安装实体。
//
// 先清空同槽其他当前引用（部分唯一索引要求），再 upsert 本实体引用；
// 只改请求槽位，其他槽位与保留引用/冻结锁不受影响。
func (tx *Tx) SetCurrentBindings(ctx context.Context, id domain.ID, slots []domain.CapabilitySlotKey, now time.Time) error {
	normalized, err := domain.NormalizeCapabilitySlots(slots)
	if err != nil {
		return err
	}
	for _, slot := range normalized {
		if err := clearCurrentBinding(ctx, tx.db, slot, id); err != nil {
			return err
		}
		if _, err := tx.db.ExecContext(ctx, `
			INSERT INTO installation_slot_bindings (installation_id, slot, is_current, activated_at)
			VALUES (?, ?, 1, ?)
			ON CONFLICT (installation_id, slot) DO UPDATE SET
				is_current = 1,
				activated_at = excluded.activated_at`,
			id, string(slot), formatTime(now)); err != nil {
			return fmt.Errorf("activate installation binding: %w", err)
		}
	}
	return nil
}

func clearCurrentBinding(ctx context.Context, db DBTX, slot domain.CapabilitySlotKey, keep domain.ID) error {
	if _, err := db.ExecContext(ctx, `
		UPDATE installation_slot_bindings SET is_current = 0
		WHERE slot = ? AND is_current = 1 AND installation_id <> ?`,
		string(slot), keep); err != nil {
		return fmt.Errorf("clear current installation binding: %w", err)
	}
	return nil
}

// DeleteSlotBindings 解除某槽位的全部当前/保留引用；安装实体行由调用方按
// 剩余引用决定是否删除。
func (tx *Tx) DeleteSlotBindings(ctx context.Context, slot domain.CapabilitySlotKey) (int64, error) {
	res, err := tx.db.ExecContext(ctx, `DELETE FROM installation_slot_bindings WHERE slot = ?`, string(slot))
	if err != nil {
		return 0, fmt.Errorf("delete installation bindings: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

// DeleteInstallation 删除安装实体（引用随外键级联删除）。
func (tx *Tx) DeleteInstallation(ctx context.Context, id domain.ID) error {
	res, err := tx.db.ExecContext(ctx, `DELETE FROM installations WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete installation: %w", err)
	}
	return affectedOne(res, domain.ErrNotFound)
}

// ListInstallationBindings 返回全部槽位引用（当前/保留），供服务层判断
// 激活顺序、删除范围与 Down 等价性。
func (q *querier) ListInstallationBindings(ctx context.Context) ([]InstallationBinding, error) {
	return q.listInstallationBindings(ctx)
}

// GetInstallation 在单个一致读事务内读取安装实体与槽位引用投影。
//
// 实体行与绑定投影必须来自同一快照：自动提交的两次查询之间插入合法完成
// 写入会返回 phase/current 混合结果。Tx 方法保持不变，供已在外层事务内的
// 调用方复用（不能再嵌套 Store 读事务，单连接会死锁）。
func (s *Store) GetInstallation(ctx context.Context, id domain.ID) (domain.Installation, error) {
	var inst domain.Installation
	err := s.ReadTx(ctx, func(tx *Tx) error {
		var err error
		inst, err = tx.GetInstallation(ctx, id)
		return err
	})
	if err != nil {
		return domain.Installation{}, err
	}
	return inst, nil
}

// ListInstallations 在单个一致读事务内读取安装列表及槽位引用投影。
func (s *Store) ListInstallations(ctx context.Context) ([]domain.Installation, error) {
	var installs []domain.Installation
	err := s.ReadTx(ctx, func(tx *Tx) error {
		var err error
		installs, err = tx.ListInstallations(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return installs, nil
}

// Store 包装：在独立事务中更新安装阶段。
func (s *Store) UpdateInstallationPhase(ctx context.Context, id domain.ID, to domain.InstallationPhase, progress, verifiedFiles int, errorInfo *domain.ErrorInfo, retryable bool, now time.Time) (domain.Installation, error) {
	var inst domain.Installation
	err := s.DoTx(ctx, func(tx *Tx) error {
		updated, err := tx.UpdateInstallationPhase(ctx, id, to, progress, verifiedFiles, errorInfo, retryable, now)
		if err != nil {
			return err
		}
		inst = updated
		return nil
	})
	if err != nil {
		return domain.Installation{}, err
	}
	return inst, nil
}

// MarkInstallationProbePassed 持久化安装完成后的就绪探针结果。
//
// MVP 目录尚无独立可执行探针，安装流程把“全文件 SHA-256 校验通过”作为
// 就绪门槛；冻结执行配置仍只接受 probe_passed=1 的 done 记录，避免把
// 单纯 done 的历史/手工记录当成已验证模型。
func (tx *Tx) MarkInstallationProbePassed(ctx context.Context, id domain.ID, passed bool, now time.Time) error {
	res, err := tx.db.ExecContext(ctx, `
		UPDATE installations
		SET probe_passed = ?, updated_at = ?
		WHERE id = ?`,
		boolInt(passed), formatTime(now), id)
	if err != nil {
		return fmt.Errorf("mark installation probe: %w", err)
	}
	return affectedOne(res, domain.ErrNotFound)
}
