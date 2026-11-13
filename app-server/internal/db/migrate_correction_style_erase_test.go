package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// TestCorrectionStyleEraseMigrationRoundTrip 验证 0012 可回滚再重放：
// 旧行在升级后新增列保持 NULL（读为 nil），Down 删列不丢业务行。
func TestCorrectionStyleEraseMigrationRoundTrip(t *testing.T) {
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
	// 先回滚更新的 0013，隔离 0012 的可回滚性。
	if err := m.Steps(-1); err != nil {
		t.Fatalf("down 0013: %v", err)
	}
	if err := m.Steps(-1); err != nil {
		t.Fatalf("down 0012: %v", err)
	}
	if got := schemaVersion(t, database); got != 11 {
		t.Fatalf("schema version after 0012 down = %d, want 11", got)
	}
	for _, column := range []string{"style_json", "erase_blocks_json"} {
		if columnExists(t, database, "subtitle_revisions", column) {
			t.Fatalf("subtitle_revisions.%s exists before 0012 up", column)
		}
	}
	if columnExists(t, database, "review_gates", "created_at") {
		t.Fatal("review_gates.created_at exists before 0012 up")
	}

	const now = "2026-11-10T00:00:00Z"
	if _, err := database.Exec(`
		INSERT INTO projects (id, workspace_id, name, schema_version, created_at, updated_at)
		VALUES ('p1', 'w1', 'project', 1, ?, ?)`, now, now); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO subtitle_tracks (id, project_id, language, type, current_revision_id, created_at, updated_at)
		VALUES ('track-1', 'p1', 'zh', 'translation', 'srev-1', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert track: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO subtitle_revisions (id, track_id, author_type, created_at)
		VALUES ('srev-1', 'track-1', 'system', ?)`, now); err != nil {
		t.Fatalf("insert legacy revision: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO workflow_runs
			(id, project_id, name, template, status, source_language, languages_json, created_at, updated_at)
		VALUES ('run-1', 'p1', 'run', 'full_pipeline', 'QUEUED', 'en', '["zh"]', ?, ?)`,
		now, now); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO review_gates (id, run_id, type, language, revision_digest, status)
		VALUES ('review-1', 'run-1', 'translation', 'zh', 'digest-old', 'pending')`); err != nil {
		t.Fatalf("insert legacy review gate: %v", err)
	}

	if err := m.Steps(1); err != nil {
		t.Fatalf("up 0012: %v", err)
	}
	if got := schemaVersion(t, database); got != 12 {
		t.Fatalf("schema version after 0012 up = %d, want 12", got)
	}
	for _, column := range []string{"style_json", "erase_blocks_json"} {
		if !columnExists(t, database, "subtitle_revisions", column) {
			t.Fatalf("subtitle_revisions.%s missing after 0012 up", column)
		}
	}
	if !columnExists(t, database, "review_gates", "created_at") {
		t.Fatal("review_gates.created_at missing after 0012 up")
	}
	var style, blocks sql.NullString
	if err := database.QueryRow(`
		SELECT style_json, erase_blocks_json
		FROM subtitle_revisions WHERE id = 'srev-1'`).Scan(&style, &blocks); err != nil {
		t.Fatalf("read migrated legacy revision: %v", err)
	}
	var createdAt sql.NullString
	if err := database.QueryRow(`
		SELECT created_at FROM review_gates WHERE id = 'review-1'`).Scan(&createdAt); err != nil {
		t.Fatalf("read migrated legacy review gate: %v", err)
	}
	if style.Valid || blocks.Valid || createdAt.Valid {
		t.Fatalf("legacy rows gained style/erase/created_at: %v/%v/%v", style, blocks, createdAt)
	}

	// 新值可写入并读回；旧业务行不因 Down 丢列丢失。
	if _, err := database.Exec(`
		UPDATE subtitle_revisions SET style_json = '{"x":0.5}', erase_blocks_json = '[]'
		WHERE id = 'srev-1'`); err != nil {
		t.Fatalf("write style/erase: %v", err)
	}
	if err := m.Steps(-1); err != nil {
		t.Fatalf("down 0012 again: %v", err)
	}
	if got := schemaVersion(t, database); got != 11 {
		t.Fatalf("schema version after second 0012 down = %d, want 11", got)
	}
	var revisions, runs, gates int
	if err := database.QueryRow(`SELECT COUNT(*) FROM subtitle_revisions`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM workflow_runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM review_gates`).Scan(&gates); err != nil {
		t.Fatal(err)
	}
	if revisions != 1 || runs != 1 || gates != 1 {
		t.Fatalf("down lost rows: revisions=%d runs=%d gates=%d", revisions, runs, gates)
	}

	if err := m.Steps(1); err != nil {
		t.Fatalf("re-up 0012: %v", err)
	}
	if got := schemaVersion(t, database); got != 12 {
		t.Fatalf("schema version after 0012 re-up = %d, want 12", got)
	}
	if !columnExists(t, database, "subtitle_revisions", "style_json") ||
		!columnExists(t, database, "review_gates", "created_at") {
		t.Fatal("0012 columns missing after re-up")
	}
}
