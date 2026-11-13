package db

// schema10 共享安装迁移：真实 schema9 行 → 10 的回填、备份、安全 Down/re-Up、
// 新布局 Down 拒绝与备份恢复证据。旧目录不移动/不去重。

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func insertLegacyInstallation(t *testing.T, db *sql.DB, id, slot, phase, createdAt string, verified int) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO installations
			(id, slot, phase, progress, verified_files, retryable, plan_id, plan_hash,
			 release_json, probe_passed, path_owner, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, 0, 'app', ?, ?)`,
		id, slot, phase, 100, verified, "plan-"+id, "hash-"+id,
		`{"release_id":"`+id+`","version":"1.0.0","digest":"sha256:`+id+`"}`,
		createdAt, createdAt)
	if err != nil {
		t.Fatalf("insert legacy installation %s: %v", id, err)
	}
}

func schemaVersion(t *testing.T, db *sql.DB) uint {
	t.Helper()
	var version uint
	if err := db.QueryRow(`SELECT version FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	return version
}

func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name       string
			typ        string
			notNull    int
			defaultV   sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultV, &primaryKey); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info: %v", err)
	}
	return false
}

type bindingRow struct {
	InstallationID string
	Slot           string
	Current        bool
}

// assertDirtyRefusal 断言失败 Down 数据/结构未变，并留下 fail-closed dirty 标记。
// golang-migrate 在运行步骤前写入目标版本 + dirty=1；这使新旧 Core 的 Migrate
// 都拒绝自动继续，恢复只能走已验证备份。
func assertDirtyRefusal(t *testing.T, db *sql.DB, wantTarget uint) {
	t.Helper()
	if got := schemaVersion(t, db); got != wantTarget {
		t.Fatalf("failed down schema version = %d, want marked target %d", got, wantTarget)
	}
	var dirty int
	if err := db.QueryRow(`SELECT dirty FROM schema_migrations`).Scan(&dirty); err != nil {
		t.Fatalf("read dirty flag: %v", err)
	}
	if dirty != 1 {
		t.Fatalf("failed down left dirty = %d, want fail-closed 1", dirty)
	}
}

// clearDirty 模拟运维确认拒绝后把标记恢复为真实 schema 版本（测试内恢复）。
func clearDirty(t *testing.T, db *sql.DB, version uint) {
	t.Helper()
	if _, err := db.Exec(`UPDATE schema_migrations SET version = ?, dirty = 0`, version); err != nil {
		t.Fatalf("clear dirty: %v", err)
	}
}

func installationBindings(t *testing.T, db *sql.DB) []bindingRow {
	t.Helper()
	rows, err := db.Query(`SELECT installation_id, slot, is_current FROM installation_slot_bindings ORDER BY slot, installation_id`)
	if err != nil {
		t.Fatalf("query bindings: %v", err)
	}
	defer rows.Close()
	var out []bindingRow
	for rows.Next() {
		var (
			binding bindingRow
			current int
		)
		if err := rows.Scan(&binding.InstallationID, &binding.Slot, &current); err != nil {
			t.Fatalf("scan binding: %v", err)
		}
		binding.Current = current != 0
		out = append(out, binding)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate bindings: %v", err)
	}
	return out
}

func TestSharedInstallationMigrationPreservesLegacyAndSafeDown(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "yaomu.db")
	dataDirPath := filepath.Join(dataDir, "models", "tts", "rel-old")
	if err := os.MkdirAll(dataDirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyFile := filepath.Join(dataDirPath, "model.bin")
	if err := os.WriteFile(legacyFile, []byte("legacy-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := Migrate(ctx, dbPath)
	if err != nil {
		t.Fatalf("initial Migrate: %v", err)
	}
	migrator, err := newMigrator(db)
	if err != nil {
		t.Fatalf("newMigrator: %v", err)
	}
	maxVersion, err := maxMigrationVersion(migrationsFS, migrationsDir)
	if err != nil {
		t.Fatalf("maxMigrationVersion: %v", err)
	}
	if err := migrator.Steps(-int(maxVersion - 9)); err != nil {
		t.Fatalf("down to schema 9: %v", err)
	}
	if got := schemaVersion(t, db); got != 9 {
		t.Fatalf("schema version after down = %d, want 9", got)
	}

	insertLegacyInstallation(t, db, "inst-tts-old", "tts", "done", "2026-11-01T00:00:00.000000000Z", 2)
	insertLegacyInstallation(t, db, "inst-tts-new", "tts", "done", "2026-11-02T00:00:00.000000000Z", 3)
	insertLegacyInstallation(t, db, "inst-tts-failed", "tts", "failed", "2026-11-03T00:00:00.000000000Z", 1)
	insertLegacyInstallation(t, db, "inst-clone", "voice-clone", "done", "2026-11-04T00:00:00.000000000Z", 4)
	// 迁移前一致备份应产生在这个版本；关闭连接后由 Migrate 生成。
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Migrate(ctx, dbPath)
	if err != nil {
		t.Fatalf("upgrade Migrate: %v", err)
	}
	defer upgraded.Close()
	if got := schemaVersion(t, upgraded); got != maxVersion {
		t.Fatalf("schema version after upgrade = %d, want %d", got, maxVersion)
	}
	for _, column := range []string{"storage_key", "covered_slots_json", "manifest_json"} {
		if !columnExists(t, upgraded, "installations", column) {
			t.Fatalf("installations.%s missing after upgrade", column)
		}
	}
	if !tableExists(t, upgraded, "installation_slot_bindings") {
		t.Fatal("installation_slot_bindings missing after upgrade")
	}
	// 旧行不带新布局/覆盖/probe 证据；目录原样保留。
	var storageKeys int
	if err := upgraded.QueryRow(`SELECT COUNT(*) FROM installations WHERE storage_key IS NOT NULL`).Scan(&storageKeys); err != nil {
		t.Fatal(err)
	}
	if storageKeys != 0 {
		t.Fatalf("legacy rows fabricated storage_key: %d", storageKeys)
	}
	var coveredJSON int
	if err := upgraded.QueryRow(`SELECT COUNT(*) FROM installations WHERE covered_slots_json IS NOT NULL OR manifest_json IS NOT NULL`).Scan(&coveredJSON); err != nil {
		t.Fatal(err)
	}
	if coveredJSON != 0 {
		t.Fatalf("legacy rows fabricated coverage/manifest: %d", coveredJSON)
	}
	var probe int
	if err := upgraded.QueryRow(`SELECT COUNT(*) FROM installations WHERE probe_passed != 0`).Scan(&probe); err != nil {
		t.Fatal(err)
	}
	if probe != 0 {
		t.Fatalf("legacy migration forged probe_passed: %d", probe)
	}
	if content, err := os.ReadFile(legacyFile); err != nil || string(content) != "legacy-bytes" {
		t.Fatalf("legacy directory moved/removed: %v", err)
	}

	bindings := installationBindings(t, upgraded)
	if len(bindings) != 4 {
		t.Fatalf("bindings = %d, want one per legacy row", len(bindings))
	}
	wantCurrent := map[string]bool{
		"inst-tts-new":    true, // created_at DESC 的首个 done
		"inst-tts-old":    false,
		"inst-tts-failed": false,
		"inst-clone":      true,
	}
	for _, binding := range bindings {
		if want, ok := wantCurrent[binding.InstallationID]; !ok || want != binding.Current {
			t.Fatalf("binding %+v current = %v, want %v", binding, binding.Current, want)
		}
	}

	// 迁移前一致备份可用：包含 schema9 行，不含 schema10 列，可被旧恢复路径读取。
	backupPath := dbPath + ".bak"
	if err := verifyBackup(backupPath); err != nil {
		t.Fatalf("verifyBackup: %v", err)
	}
	backup, err := sql.Open("sqlite", backupPath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backup.Close()
	if got := schemaVersion(t, backup); got != 9 {
		t.Fatalf("backup schema version = %d, want 9", got)
	}
	if columnExists(t, backup, "installations", "storage_key") {
		t.Fatal("backup unexpectedly contains schema10 columns")
	}
	var rows int
	if err := backup.QueryRow(`SELECT COUNT(*) FROM installations`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 4 {
		t.Fatalf("backup legacy rows = %d, want 4", rows)
	}

	// 迁移-only 数据的安全 Down / re-Up：保留行与原始当前选择。
	downMigrator, err := newMigrator(upgraded)
	if err != nil {
		t.Fatal(err)
	}
	// 0011（移除 voice_authorization）与 0012（校正样式/擦除列）与本测试无关：
	// 先回滚到 schema 10，后续继续验证 0010 的 Down/Re-Up。
	if err := downMigrator.Steps(-int(maxVersion - 10)); err != nil {
		t.Fatalf("down to schema 10: %v", err)
	}
	if got := schemaVersion(t, upgraded); got != 10 {
		t.Fatalf("schema version after 0011/0012 down = %d, want 10", got)
	}
	if err := downMigrator.Steps(-1); err != nil {
		t.Fatalf("safe down: %v", err)
	}
	if got := schemaVersion(t, upgraded); got != 9 {
		t.Fatalf("schema version after safe down = %d, want 9", got)
	}
	if tableExists(t, upgraded, "installation_slot_bindings") || columnExists(t, upgraded, "installations", "storage_key") {
		t.Fatal("safe down did not remove schema10 structures")
	}
	var preserved int
	if err := upgraded.QueryRow(`SELECT COUNT(*) FROM installations`).Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	if preserved != 4 {
		t.Fatalf("safe down lost legacy rows: %d", preserved)
	}
	if err := downMigrator.Steps(1); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	if got := schemaVersion(t, upgraded); got != 10 {
		t.Fatalf("schema version after re-up = %d, want 10", got)
	}
	rebuilt := installationBindings(t, upgraded)
	if len(rebuilt) != 4 {
		t.Fatalf("re-up bindings = %d, want 4", len(rebuilt))
	}
	for _, binding := range rebuilt {
		if wantCurrent[binding.InstallationID] != binding.Current {
			t.Fatalf("re-up changed selection for %s: %v", binding.InstallationID, binding.Current)
		}
	}

	// 新布局记录存在时 Down 拒绝，且不丢数据/不降级。
	if _, err := upgraded.Exec(`UPDATE installations SET storage_key = 'new-layout-key' WHERE id = 'inst-clone'`); err != nil {
		t.Fatal(err)
	}
	if err := downMigrator.Steps(-1); err == nil {
		t.Fatal("unsafe down accepted new-layout rows")
	}
	// golang-migrate 在失败步骤上留下 dirty 标记（fail-closed）；数据/结构未变。
	assertDirtyRefusal(t, upgraded, 9)
	if !tableExists(t, upgraded, "installation_slot_bindings") {
		t.Fatal("failed down removed bindings before refusing")
	}
	var stillHasKey int
	if err := upgraded.QueryRow(`SELECT COUNT(*) FROM installations WHERE storage_key = 'new-layout-key'`).Scan(&stillHasKey); err != nil || stillHasKey != 1 {
		t.Fatalf("failed down lost new-layout row marker: n=%d err=%v", stillHasKey, err)
	}
	clearDirty(t, upgraded, 10)

	// 恢复旧布局后模拟引用变化（回滚到旧 done）：Down 仍拒绝。
	if _, err := upgraded.Exec(`UPDATE installations SET storage_key = NULL WHERE id = 'inst-clone'`); err != nil {
		t.Fatal(err)
	}
	if _, err := upgraded.Exec(`UPDATE installation_slot_bindings SET is_current = 0 WHERE slot = 'tts' AND is_current = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := upgraded.Exec(`UPDATE installation_slot_bindings SET is_current = 1 WHERE installation_id = 'inst-tts-old' AND slot = 'tts'`); err != nil {
		t.Fatal(err)
	}
	if err := downMigrator.Steps(-1); err == nil {
		t.Fatal("unsafe down accepted nonrepresentable current selection")
	}
	assertDirtyRefusal(t, upgraded, 9)
	clearDirty(t, upgraded, 10)

	// 恢复原选择后可安全 Down（显式修复路径），回行数不变。
	if _, err := upgraded.Exec(`UPDATE installation_slot_bindings SET is_current = 0 WHERE slot = 'tts'`); err != nil {
		t.Fatal(err)
	}
	if _, err := upgraded.Exec(`UPDATE installation_slot_bindings SET is_current = 1 WHERE installation_id = 'inst-tts-new' AND slot = 'tts'`); err != nil {
		t.Fatal(err)
	}
	if err := downMigrator.Steps(-1); err != nil {
		t.Fatalf("down after restoring selection: %v", err)
	}
	if err := upgraded.QueryRow(`SELECT COUNT(*) FROM installations`).Scan(&preserved); err != nil || preserved != 4 {
		t.Fatalf("restored down rows = %d, err = %v", preserved, err)
	}
}
