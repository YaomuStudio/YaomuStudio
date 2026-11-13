ALTER TABLE workflow_runs ADD COLUMN voice_authorization INTEGER NOT NULL DEFAULT 0;

UPDATE workflow_runs
SET voice_authorization = COALESCE(
  (
    SELECT a.voice_authorization
    FROM voice_authorization_archive a
    WHERE a.run_id = workflow_runs.id
  ),
  0
);

DROP TABLE voice_authorization_archive;
