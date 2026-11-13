// Package store 是业务 Core 的 SQLite 仓储层。
//
// 职责边界：
//   - 只做持久化与事务，不 import net/http，不做调度；
//   - 领域规则（状态机、缓存指纹）在 internal/domain，本包调用其纯函数；
//   - Core 是唯一业务 writer；多语句写操作通过 Tx 在同一事务内完成，
//     调用方用 DoTx 组合「业务写入 + 关键事件 + 幂等回执」。
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/db"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// DBTX 是 *sql.DB 与 *sql.Tx 的共同子集。
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// querier 承载同时适用于 Store（自动提交）与 Tx（显式事务）的仓储方法。
type querier struct {
	db DBTX
}

// Store 是 SQLite 仓储入口。
type Store struct {
	*querier
	sqlDB *sql.DB
	gate  *WriteGate
}

// Open 打开（必要时迁移）数据库并返回 Store。
//
// 迁移沿用 internal/db.Migrate：迁移前一致备份、降级保护、dirty 状态拒绝自动恢复。
func Open(ctx context.Context, dbPath string) (*Store, error) {
	sqlDB, err := db.Migrate(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	return New(sqlDB), nil
}

// New 用已有连接构造 Store；连接应已执行过迁移。
func New(sqlDB *sql.DB) *Store {
	return NewWithGate(sqlDB, NewWriteGate())
}

// NewWithGate 用指定准入闸门构造 Store（测试可注入或检查同一闸门）。
func NewWithGate(sqlDB *sql.DB, gate *WriteGate) *Store {
	return &Store{querier: &querier{db: sqlDB}, sqlDB: sqlDB, gate: gate}
}

// Gate 返回业务准入闸门（更新准备与 HTTP 请求准入共用）。
func (s *Store) Gate() *WriteGate { return s.gate }

// Sealed 报告本实例是否已因应用更新准备封闸。
func (s *Store) Sealed() bool { return s.gate.Sealed() }

// Close 关闭底层连接。
func (s *Store) Close() error { return s.sqlDB.Close() }

// DB 返回底层连接，供只读诊断使用；业务写入请走仓储方法或 DoTx。
func (s *Store) DB() *sql.DB { return s.sqlDB }

// Tx 是显式事务上下文；同时提供 querier 的读写方法。
type Tx struct {
	*querier
	tx *sql.Tx
}

// DoTx 在单个事务内执行 fn：fn 返回错误则回滚，否则提交。
//
// 任务创建、步骤计划、关键事件与幂等回执应在同一个 fn 内写入，
// 提交成功后才允许派发执行器（docs/local-api-contract.md §4）。
//
// 每次 DoTx 先取得普通准入：应用更新准备封闸后返回 ErrSealed，
// 已进入的事务要么先提交并被空闲检查观察，要么在封闸后失去准入。
func (s *Store) DoTx(ctx context.Context, fn func(*Tx) error) error {
	if err := s.gate.AcquireShared(); err != nil {
		return err
	}
	defer s.gate.ReleaseShared()
	return s.doTx(ctx, fn)
}

func (s *Store) doTx(ctx context.Context, fn func(*Tx) error) error {
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(&Tx{querier: &querier{db: tx}, tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	committed = true
	return nil
}

// NewID 生成不透明随机 ID；prefix 非空时以 "prefix_" 开头。
// 加密随机源不可用时直接 panic（无法安全生成实体标识）。
func NewID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("store: generate id: " + err.Error())
	}
	id := hex.EncodeToString(b[:])
	if prefix == "" {
		return id
	}
	return prefix + "_" + id
}

// sqliteTimeLayout 是固定宽度的 UTC 时间文本格式（纳秒补零）。
//
// 不用 time.RFC3339Nano：小数位长度可变会让 SQL 文本比较/排序与时间顺序不一致
// （例如 "…00Z" 与 "…00.5Z"）。固定 9 位小数保证字典序 == 时间序。
const sqliteTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// formatTime 统一把时间存成固定宽度 UTC 文本。
func formatTime(t time.Time) string { return t.UTC().Format(sqliteTimeLayout) }

// parseTime 解析库内时间文本并归一到 UTC；兼容旧的可变小数位 RFC3339。
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}
	return t.UTC(), nil
}

// parseNullTime 解析可空时间。
func parseNullTime(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid {
		return nil, nil
	}
	t, err := parseTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// nullTimeValue 把可空时间转成 SQL 参数。
func nullTimeValue(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

// boolInt 把 bool 存为 SQLite INTEGER。
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// marshalJSON 编码必填 JSON 列。
func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal json: %w", err)
	}
	return string(b), nil
}

// marshalJSONList 编码列表列；nil 归一为 []，避免读回 null 与空列表产生两种表示。
func marshalJSONList(ids []domain.ID) (string, error) {
	if ids == nil {
		ids = []domain.ID{}
	}
	return marshalJSON(ids)
}

// marshalStrings 编码字符串列表列；nil 归一为 []。
func marshalStrings(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	return marshalJSON(values)
}

// errorInfoJSON 编码可空错误信息列；action_ids 归一为空数组（契约要求数组）。
func errorInfoJSON(e *domain.ErrorInfo) (any, error) {
	if e == nil {
		return nil, nil
	}
	info := *e
	if info.ActionIDs == nil {
		info.ActionIDs = []domain.ID{}
	}
	return marshalJSON(info)
}

// releaseRefJSON 编码可空发布引用列。
func releaseRefJSON(r *domain.ReleaseRef) (any, error) {
	if r == nil {
		return nil, nil
	}
	return marshalJSON(r)
}

// unmarshalNull 解码可空 JSON 列。
func unmarshalNull(ns sql.NullString, v any) error {
	if !ns.Valid {
		return nil
	}
	if err := json.Unmarshal([]byte(ns.String), v); err != nil {
		return fmt.Errorf("unmarshal json column: %w", err)
	}
	return nil
}

// rowScanner 是 *sql.Row / *sql.Rows 的共同扫描接口。
type rowScanner interface {
	Scan(dest ...any) error
}

// wrapNotFound 把 sql.ErrNoRows 映射为领域 NOT_FOUND。
func wrapNotFound(err error) error {
	if err == sql.ErrNoRows {
		return domain.ErrNotFound
	}
	return err
}

// affectedOne 断言单行写入；0 行时返回 fallback（通常是 NOT_FOUND 或状态冲突）。
func affectedOne(res sql.Result, fallback error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fallback
	}
	return nil
}

// isUniqueViolation 识别 SQLite 唯一约束冲突（跨驱动稳定的是错误文本）。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// validatePageSize 限定列表查询上限，避免一次读取无限事件。
func validatePageSize(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > 1000 {
		return 1000
	}
	return limit
}
