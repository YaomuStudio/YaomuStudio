DROP INDEX IF EXISTS idx_assets_project_role;
ALTER TABLE workflow_runs DROP COLUMN mode_type;
ALTER TABLE assets DROP COLUMN run_id;
ALTER TABLE assets DROP COLUMN language;
ALTER TABLE assets DROP COLUMN role;
DROP INDEX IF EXISTS idx_projects_episode;
ALTER TABLE projects DROP COLUMN episode_id;
