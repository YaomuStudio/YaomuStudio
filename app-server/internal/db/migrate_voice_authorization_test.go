package db

import (
	"context"
	"path/filepath"
	"testing"
)

// TestRemoveVoiceAuthorizationMigrationArchivesAndRestores 验证 0011 先归档再删列，
// 且 down 能从归档恢复原值，而不是只回填默认 0。
func TestRemoveVoiceAuthorizationMigrationArchivesAndRestores(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "yaomu.db")
	database, err := Migrate(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	defer database.Close()

	m, err := newMigrator(database)
	if err != nil {
		t.Fatalf("newMigrator() error = %v", err)
	}
	maxVersion, err := maxMigrationVersion(migrationsFS, migrationsDir)
	if err != nil {
		t.Fatalf("maxMigrationVersion() error = %v", err)
	}
	// 回到 0010（去掉 0011 及后续迁移），此时 voice_authorization 列存在、归档表不存在。
	if err := m.Steps(-int(maxVersion - 10)); err != nil {
		t.Fatalf("roll back to 0010: %v", err)
	}
	if !columnExists(t, database, "workflow_runs", "voice_authorization") {
		t.Fatal("voice_authorization column missing at version 10")
	}

	const now = "2026-11-09T00:00:00Z"
	if _, err := database.Exec(`
		INSERT INTO projects (id, workspace_id, name, schema_version, created_at, updated_at)
		VALUES ('p1', 'w1', 'project', 1, ?, ?)`, now, now); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO workflow_runs
			(id, project_id, name, template, status, source_language, languages_json,
			 voice_authorization, created_at, updated_at)
		VALUES ('r1', 'p1', 'run', 'tool_voice_clone', 'QUEUED', 'en', '[]', 1, ?, ?)`,
		now, now); err != nil {
		t.Fatalf("insert workflow run: %v", err)
	}

	if err := m.Steps(1); err != nil {
		t.Fatalf("apply 0011: %v", err)
	}
	if columnExists(t, database, "workflow_runs", "voice_authorization") {
		t.Fatal("voice_authorization column still exists after 0011 up")
	}
	var archived int
	if err := database.QueryRow(
		`SELECT voice_authorization FROM voice_authorization_archive WHERE run_id = 'r1'`,
	).Scan(&archived); err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if archived != 1 {
		t.Fatalf("archived voice_authorization = %d, want 1", archived)
	}

	if err := m.Steps(-1); err != nil {
		t.Fatalf("roll back 0011 again: %v", err)
	}
	if !columnExists(t, database, "workflow_runs", "voice_authorization") {
		t.Fatal("voice_authorization column missing after 0011 down")
	}
	var restored int
	if err := database.QueryRow(
		`SELECT voice_authorization FROM workflow_runs WHERE id = 'r1'`,
	).Scan(&restored); err != nil {
		t.Fatalf("read restored value: %v", err)
	}
	if restored != 1 {
		t.Fatalf("restored voice_authorization = %d, want 1", restored)
	}
	if tableExists(t, database, "voice_authorization_archive") {
		t.Fatal("voice_authorization_archive still exists after 0011 down")
	}
}
