package db

import (
	"context"
	"path/filepath"
	"testing"
)

// TestMigrationRoundTrip 验证业务表迁移可回滚再重放。
//
// down 必须按外键依赖逆序删表；up 重放后表结构完整。回滚步数按内嵌迁移
// 的最大版本推导，新增迁移时无需再改本测试的硬编码版本号。
func TestMigrationRoundTrip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "yaomu.db")

	database, err := Migrate(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	defer database.Close()

	versionedTables := map[uint][]string{
		2: {
			"projects", "assets", "asset_locations", "subtitle_tracks", "subtitle_revisions",
			"segments", "workflow_runs", "run_nodes", "tasks", "attempts", "artifact_commits",
			"review_gates", "audio_candidates", "candidate_selections", "installations",
			"events", "receipts",
		},
		3:  {"engine_configs", "exports"},
		10: {"installation_slot_bindings"},
		11: {"voice_authorization_archive"},
	}
	for _, tables := range versionedTables {
		for _, name := range tables {
			if !tableExists(t, database, name) {
				t.Fatalf("table %s missing after up migration", name)
			}
		}
	}

	maxVersion, err := maxMigrationVersion(migrationsFS, migrationsDir)
	if err != nil {
		t.Fatalf("maxMigrationVersion() error = %v", err)
	}
	stepsDown := -int(maxVersion - 1) // 回滚到 0001 baseline
	m, err := newMigrator(database)
	if err != nil {
		t.Fatalf("newMigrator() error = %v", err)
	}
	if err := m.Steps(stepsDown); err != nil {
		t.Fatalf("apply down migration: %v", err)
	}
	if got := currentVersion(t, database); got != 1 {
		t.Errorf("schema version after down = %d, want 1", got)
	}
	for _, tables := range versionedTables {
		for _, name := range tables {
			if tableExists(t, database, name) {
				t.Errorf("table %s still exists after down migration", name)
			}
		}
	}
	if !tableExists(t, database, "app_meta") {
		t.Error("0001 baseline table app_meta was dropped by down migrations")
	}

	if err := m.Steps(int(maxVersion - 1)); err != nil {
		t.Fatalf("re-apply up migration: %v", err)
	}
	if got := currentVersion(t, database); got != maxVersion {
		t.Errorf("schema version after re-up = %d, want %d", got, maxVersion)
	}
	for _, tables := range versionedTables {
		for _, name := range tables {
			if !tableExists(t, database, name) {
				t.Errorf("table %s missing after re-applying up migration", name)
			}
		}
	}
}
