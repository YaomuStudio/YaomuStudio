ALTER TABLE projects ADD COLUMN episode_id TEXT;
CREATE UNIQUE INDEX idx_projects_episode ON projects (episode_id)
    WHERE episode_id IS NOT NULL AND episode_id <> '';

ALTER TABLE assets ADD COLUMN role TEXT;
ALTER TABLE assets ADD COLUMN language TEXT;
ALTER TABLE assets ADD COLUMN run_id TEXT;
CREATE INDEX idx_assets_project_role ON assets (project_id, role);

ALTER TABLE workflow_runs ADD COLUMN mode_type TEXT;
