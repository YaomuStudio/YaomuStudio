package store

// 应用更新准备的写入封闸与显式活跃集。
//
// 契约（contracts/app-update-installation.md §Atomicity and failure behavior）：
//   - SealIfIdle 先取得独占准入，再在同一读事务内复核显式活跃集与内存拥有者，
//     空闲时提交检查并把封闸标记在释放独占前生效；
//   - 已进入的创建/认领要么先提交并被观察，要么在封闸后失去准入；
//   - 检查失败/忙只重新开放准入，不产生业务行、事件或回执副作用；
//   - 封闸不持久化、不自动 TTL 解封；恢复方式是显式重启新实例。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// ActiveWork 是显式活跃业务集的数量快照（不对外暴露计数，只用于空闲判断）。
//
// FAILED/INTERRUPTED Task 是可重试历史，不在此集合；不能使用 Task.Terminal()
// 推断活跃性（见 research/core-drain-boundary-supplement.md §5）。
type ActiveWork struct {
	Runs          int
	Tasks         int
	Attempts      int
	Installations int
}

// Empty 报告四个显式集合是否都为空。
func (w ActiveWork) Empty() bool {
	return w.Runs == 0 && w.Tasks == 0 && w.Attempts == 0 && w.Installations == 0
}

// ActiveWork 读取显式活跃集：非终态 Run、明确活跃 Task、未收敛 Attempt 与
// 未终态 Installation。查询只读，可与封闸检查在同一事务内执行。
func (q *querier) ActiveWork(ctx context.Context) (ActiveWork, error) {
	var out ActiveWork
	counts := []struct {
		table  string
		column string
		values []string
		target *int
	}{
		{"workflow_runs", "status", activeRunStatuses(), &out.Runs},
		{"tasks", "status", activeTaskStatuses(), &out.Tasks},
		{"attempts", "status", activeAttemptStatuses(), &out.Attempts},
		{"installations", "phase", activeInstallationPhases(), &out.Installations},
	}
	for _, item := range counts {
		n, err := q.countValues(ctx, item.table, item.column, item.values)
		if err != nil {
			return ActiveWork{}, err
		}
		*item.target = n
	}
	return out, nil
}

func activeRunStatuses() []string {
	return []string{
		string(domain.RunStatusDraft), string(domain.RunStatusQueued),
		string(domain.RunStatusRunning), string(domain.RunStatusPauseRequested),
		string(domain.RunStatusPaused), string(domain.RunStatusWaitingReview),
	}
}

func activeTaskStatuses() []string {
	return []string{
		string(domain.TaskStatusQueued), string(domain.TaskStatusWaitingResource),
		string(domain.TaskStatusRunning), string(domain.TaskStatusPauseRequested),
		string(domain.TaskStatusPaused), string(domain.TaskStatusCancelRequested),
	}
}

func activeAttemptStatuses() []string {
	return []string{
		string(domain.StepStatusPending), string(domain.StepStatusRunning),
		string(domain.StepStatusCommitting),
	}
}

func activeInstallationPhases() []string {
	return []string{
		string(domain.InstallationPhasePlanning), string(domain.InstallationPhaseDownloading),
		string(domain.InstallationPhaseVerifying), string(domain.InstallationPhaseInstalling),
	}
}

// countValues 统计 table.column 属于 values 的行数；表名/列名只来自本文件常量。
func (q *querier) countValues(ctx context.Context, table, column string, values []string) (int, error) {
	if len(values) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	var n int
	query := `SELECT COUNT(*) FROM ` + table + ` WHERE ` + column + ` IN (` + placeholders + `)`
	if err := q.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count active %s: %w", table, err)
	}
	return n, nil
}

// SealIfIdle 以独占准入原子封闸。
//
// check 在单个只读事务内执行：返回 idle=false 表示仍有活跃工作，事务回滚且
// 不封闸；返回错误同等回滚。只有 idle=true 时提交检查并把封闸标记在释放
// 独占准入之前生效。已有普通请求/事务在飞时返回 ErrAdmissionBusy（不等待、
// 不打断），已封闸时返回 ErrSealed。
func (s *Store) SealIfIdle(ctx context.Context, check func(*Tx) (bool, error)) (bool, error) {
	if err := s.gate.TryExclusive(); err != nil {
		return false, err
	}
	release := true
	defer func() {
		if release {
			s.gate.ReleaseExclusive()
		}
	}()
	tx, err := s.sqlDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, fmt.Errorf("begin seal check: %w", err)
	}
	idle, err := check(&Tx{querier: &querier{db: tx}, tx: tx})
	if err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit seal check: %w", err)
	}
	if !idle {
		return false, nil
	}
	s.gate.Seal()
	s.gate.ReleaseExclusive()
	release = false
	return true, nil
}

// PruneEventsBeforeTime 在准入保护下裁剪历史事件（后台保留策略唯一入口）。
//
// Store 级方法覆盖 querier 的自动提交版本：封闸后返回 ErrSealed，事件保留
// 不能绕过更新准备闸门；事务内版本仍可通过 Tx 使用。
func (s *Store) PruneEventsBeforeTime(ctx context.Context, before time.Time) (int64, error) {
	var removed int64
	err := s.DoTx(ctx, func(tx *Tx) error {
		n, err := tx.PruneEventsBeforeTime(ctx, before)
		if err != nil {
			return err
		}
		removed = n
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// SetAssetStatus 在准入保护下更新资产可用状态。
//
// GET 项目刷新等读路径的副作用也必须经封闸准入；封闸后拒绝写入。
func (s *Store) SetAssetStatus(ctx context.Context, id domain.ID, status domain.AssetStatus) error {
	return s.DoTx(ctx, func(tx *Tx) error {
		return tx.SetAssetStatus(ctx, id, status)
	})
}

// SetAssetLocationAvailable 在准入保护下标记资产位置可用性。
func (s *Store) SetAssetLocationAvailable(ctx context.Context, locationID domain.ID, available bool) error {
	return s.DoTx(ctx, func(tx *Tx) error {
		return tx.SetAssetLocationAvailable(ctx, locationID, available)
	})
}
