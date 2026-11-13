package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// 注意：本包测试未用 t.Parallel()，因为 takeBackup 是包级注入点，
// 其中一个用例会临时替换它；并行运行会引入数据竞争。

func TestMigrateFreshDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "yaomu.db")

	db, err := Migrate(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	defer db.Close()

	if !tableExists(t, db, "app_meta") {
		t.Fatal("app_meta table missing after migration")
	}

	want, err := maxMigrationVersion(migrationsFS, migrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := currentVersion(t, db); got != want {
		t.Errorf("schema version = %d, want %d", got, want)
	}
}

func TestMigrateRefusesDowngrade(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "yaomu.db")

	db, err := Migrate(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("first Migrate() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// 把 schema 版本抬高到超出本二进制内嵌范围，模拟由更新版应用写入的库。
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.Exec("UPDATE schema_migrations SET version = 99, dirty = 0"); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Migrate(context.Background(), dbPath); err == nil {
		t.Fatal("Migrate() = nil, want downgrade refusal error")
	}
}

func TestMigrateTakesVerifiedBackup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "yaomu.db")

	db, err := Migrate(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	defer db.Close()

	backupPath := dbPath + ".bak"
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup file not found: %v", err)
	}
	if err := verifyBackup(backupPath); err != nil {
		t.Fatalf("verifyBackup() = %v", err)
	}
}

func TestMigrateBackupFailureAbortsMigration(t *testing.T) {
	orig := takeBackup
	takeBackup = func(context.Context, *sql.DB, string) error {
		return errors.New("injected backup failure")
	}
	t.Cleanup(func() { takeBackup = orig })

	dbPath := filepath.Join(t.TempDir(), "yaomu.db")

	if _, err := Migrate(context.Background(), dbPath); err == nil {
		t.Fatal("Migrate() = nil, want backup failure error")
	}

	// 备份失败时不得执行任何迁移：app_meta 不应存在。
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer raw.Close()
	if tableExists(t, raw, "app_meta") {
		t.Fatal("migration applied despite backup failure")
	}
}

func TestMigrateNoopWhenUpToDate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "yaomu.db")

	db, err := Migrate(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("first Migrate() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := Migrate(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	if err := db2.Close(); err != nil {
		t.Fatal(err)
	}
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	return n > 0
}

func currentVersion(t *testing.T, db *sql.DB) uint {
	t.Helper()
	var version uint
	if err := db.QueryRow("SELECT version FROM schema_migrations").Scan(&version); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	return version
}
