-- 0002 down：按外键依赖逆序删除业务表。
DROP TABLE IF EXISTS receipts;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS installations;
DROP TABLE IF EXISTS candidate_selections;
DROP TABLE IF EXISTS audio_candidates;
DROP TABLE IF EXISTS review_gates;
DROP TABLE IF EXISTS artifact_commits;
DROP TABLE IF EXISTS attempts;
DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS run_nodes;
DROP TABLE IF EXISTS workflow_runs;
DROP TABLE IF EXISTS segments;
DROP TABLE IF EXISTS subtitle_revisions;
DROP TABLE IF EXISTS subtitle_tracks;
DROP TABLE IF EXISTS asset_locations;
DROP TABLE IF EXISTS assets;
DROP TABLE IF EXISTS projects;
