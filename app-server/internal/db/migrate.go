// Package db 负责业务 Core 的 SQLite 打开、迁移前一致备份与降级保护。
//
// 单一业务状态权威在 Core；这里的迁移由 Go Core 启动时执行，
// Electron Shell 不直接写业务库。迁移链采用版本化 SQL（内嵌于二进制），
// 新库与升级走同一条链，不维护第二套自动迁移真相。
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	_ "modernc.org/sqlite" // 注册 database/sql 驱动名 "sqlite"（纯 Go，无 CGo）
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationsDir 是 embed.FS 内迁移文件的目录前缀，与 //go:embed 路径一致。
const migrationsDir = "migrations"

// takeBackup 是迁移前一致备份的注入点，测试用它模拟备份失败。
// 默认走 modernc 的在线备份 API（SQLite backup API）。
var takeBackup = backupSQLite

// Migrate 打开 dbPath 的 SQLite 库，按序应用尚未执行的迁移，返回可用的 *sql.DB。
//
// 硬约束（见 docs/data-and-storage.md §6）：
//   - 降级保护：库 schema 版本高于本二进制内嵌最大版本时拒绝打开；
//   - 迁移前一致备份并校验可打开，备份失败则停止迁移、保留原库；
//   - 处于 dirty 状态（上次迁移未完成）时拒绝自动恢复。
func Migrate(ctx context.Context, dbPath string) (*sql.DB, error) {
	db, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*sql.DB, error) {
		_ = db.Close()
		return nil, err
	}

	maxVersion, err := maxMigrationVersion(migrationsFS, migrationsDir)
	if err != nil {
		return fail(err)
	}
	m, err := newMigrator(db)
	if err != nil {
		return fail(err)
	}

	version, dirty, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return fail(fmt.Errorf("read schema version: %w", err))
	}
	if errors.Is(err, migrate.ErrNilVersion) {
		version = 0 // 全新库，尚无已应用迁移
	}
	if dirty {
		return fail(fmt.Errorf("database is in dirty migration state at version %d (previous migration did not complete)", version))
	}
	if version > maxVersion {
		return fail(fmt.Errorf("database schema version %d is newer than this build supports (%d)", version, maxVersion))
	}

	if version < maxVersion {
		backupPath := dbPath + ".bak"
		if err := takeBackup(ctx, db, backupPath); err != nil {
			return fail(fmt.Errorf("pre-migration backup failed: %w", err))
		}
		if err := verifyBackup(backupPath); err != nil {
			return fail(fmt.Errorf("pre-migration backup verification failed: %w", err))
		}
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fail(fmt.Errorf("apply migrations: %w", err))
	}
	return db, nil
}

func openDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	// SQLite 单一 writer：限制连接数，避免写锁竞争。
	db.SetMaxOpenConns(1)

	pragmas := []struct{ name, stmt string }{
		{"journal_mode", "PRAGMA journal_mode=WAL"},
		{"foreign_keys", "PRAGMA foreign_keys=ON"},
		{"busy_timeout", "PRAGMA busy_timeout=5000"},
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p.stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("set pragma %s: %w", p.name, err)
		}
	}
	return db, nil
}

func newMigrator(db *sql.DB) (*migrate.Migrate, error) {
	src, err := iofs.New(migrationsFS, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	instance, err := migratesqlite.WithInstance(db, &migratesqlite.Config{})
	if err != nil {
		return nil, fmt.Errorf("attach sqlite to migrator: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "sqlite", instance)
	if err != nil {
		return nil, fmt.Errorf("build migrator: %w", err)
	}
	return m, nil
}

// maxMigrationVersion 返回内嵌迁移文件的最高版本号。
func maxMigrationVersion(fsys fs.FS, dir string) (uint, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return 0, fmt.Errorf("read migrations dir: %w", err)
	}
	var max uint
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		v, err := migrationVersion(e.Name())
		if err != nil {
			return 0, err
		}
		if v > max {
			max = v
		}
	}
	if max == 0 {
		return 0, errors.New("no embedded migrations found")
	}
	return max, nil
}

// migrationVersion 从 "0001_baseline.up.sql" 这类文件名解析版本号。
func migrationVersion(name string) (uint, error) {
	idx := strings.IndexByte(name, '_')
	if idx <= 0 {
		return 0, fmt.Errorf("invalid migration filename %q: missing version prefix", name)
	}
	v, err := strconv.ParseUint(name[:idx], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid migration version in %q: %w", name, err)
	}
	return uint(v), nil
}
