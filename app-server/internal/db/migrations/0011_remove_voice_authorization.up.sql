CREATE TABLE voice_authorization_archive (
  run_id TEXT PRIMARY KEY,
  voice_authorization INTEGER NOT NULL,
  archived_at TEXT NOT NULL
);

INSERT INTO voice_authorization_archive (run_id, voice_authorization, archived_at)
SELECT id, voice_authorization, CURRENT_TIMESTAMP FROM workflow_runs;

ALTER TABLE workflow_runs DROP COLUMN voice_authorization;
