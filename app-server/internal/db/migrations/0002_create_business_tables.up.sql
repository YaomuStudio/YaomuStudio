-- 0002 业务表。
--
-- 对应 docs/data-and-storage.md §1 的对象：
--   projects / assets / asset_locations / subtitle_tracks / subtitle_revisions / segments
--   workflow_runs / run_nodes / tasks / attempts / artifact_commits
--   review_gates / audio_candidates / candidate_selections / installations
--   events（工作区级持久 seq + 保留策略字段）/ receipts（幂等回执）
--
-- 约定：时间存固定宽度 UTC 文本（纳秒补零，可安全按文本排序）；
-- 媒体时间整数毫秒；ID 为不透明文本。
-- 枚举取值以 contracts/openapi.yaml 为准，数据库层不做枚举 CHECK，
-- 由 Core 的 domain 包校验，避免契约演进时出现第二套真相。

CREATE TABLE projects (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT NOT NULL,
    name           TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    edit_version   INTEGER NOT NULL DEFAULT 1,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    last_opened_at TEXT
);

CREATE INDEX idx_projects_workspace ON projects (workspace_id, updated_at DESC);

CREATE TABLE assets (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    type        TEXT NOT NULL,
    name        TEXT NOT NULL,
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    sha256      TEXT NOT NULL DEFAULT '',
    meta_json   TEXT,
    status      TEXT NOT NULL,
    imported_at TEXT NOT NULL
);

CREATE INDEX idx_assets_project ON assets (project_id, imported_at);

CREATE TABLE asset_locations (
    id         TEXT PRIMARY KEY,
    asset_id   TEXT NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    path       TEXT NOT NULL,
    source     TEXT NOT NULL,
    owner      TEXT NOT NULL,
    available  INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL
);

CREATE INDEX idx_asset_locations_asset ON asset_locations (asset_id, created_at);

CREATE TABLE subtitle_tracks (
    id                  TEXT PRIMARY KEY,
    project_id          TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    language            TEXT NOT NULL,
    type                TEXT NOT NULL,
    current_revision_id TEXT,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

CREATE INDEX idx_subtitle_tracks_project ON subtitle_tracks (project_id, language);
CREATE INDEX idx_subtitle_tracks_language ON subtitle_tracks (language);

CREATE TABLE subtitle_revisions (
    id                 TEXT PRIMARY KEY,
    track_id           TEXT NOT NULL REFERENCES subtitle_tracks (id) ON DELETE CASCADE,
    parent_revision_id TEXT REFERENCES subtitle_revisions (id) ON DELETE SET NULL,
    author_type        TEXT NOT NULL,
    input_revision_id  TEXT,
    created_at         TEXT NOT NULL
);

CREATE INDEX idx_subtitle_revisions_track ON subtitle_revisions (track_id, created_at);

CREATE TABLE segments (
    revision_id      TEXT NOT NULL REFERENCES subtitle_revisions (id) ON DELETE CASCADE,
    id               TEXT NOT NULL,
    ordinal          INTEGER NOT NULL,
    start_ms         INTEGER NOT NULL,
    end_ms           INTEGER NOT NULL,
    text             TEXT NOT NULL,
    speaker          TEXT,
    content_revision INTEGER NOT NULL,
    content_digest   TEXT NOT NULL,
    lineage_json     TEXT,
    PRIMARY KEY (revision_id, id)
);

CREATE INDEX idx_segments_segment ON segments (id);
CREATE INDEX idx_segments_revision_order ON segments (revision_id, ordinal);

CREATE TABLE workflow_runs (
    id                        TEXT PRIMARY KEY,
    project_id                TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name                      TEXT NOT NULL,
    template                  TEXT NOT NULL,
    status                    TEXT NOT NULL,
    state_version             INTEGER NOT NULL DEFAULT 1,
    source_language           TEXT NOT NULL,
    languages_json            TEXT NOT NULL,
    input_revision_ids_json   TEXT,
    extract_json              TEXT,
    reuse_audio_track_id      TEXT,
    reuse_subtitle_track_id   TEXT,
    erase_mode                TEXT,
    validate_timestamps       INTEGER NOT NULL DEFAULT 0,
    adjust_duration           INTEGER NOT NULL DEFAULT 0,
    bilingual                 INTEGER NOT NULL DEFAULT 0,
    subtitle_style_json       TEXT,
    created_at                TEXT NOT NULL,
    updated_at                TEXT NOT NULL
);

CREATE INDEX idx_workflow_runs_project ON workflow_runs (project_id, created_at);

CREATE TABLE run_nodes (
    run_id   TEXT NOT NULL REFERENCES workflow_runs (id) ON DELETE CASCADE,
    key      TEXT NOT NULL,
    status   TEXT NOT NULL,
    progress INTEGER NOT NULL DEFAULT 0,
    detail   TEXT,
    position INTEGER NOT NULL,
    PRIMARY KEY (run_id, key)
);

CREATE TABLE tasks (
    id                      TEXT PRIMARY KEY,
    project_id              TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    run_id                  TEXT REFERENCES workflow_runs (id) ON DELETE SET NULL,
    kind                    TEXT NOT NULL,
    node_key                TEXT,
    segment_id              TEXT,
    language                TEXT,
    label                   TEXT,
    status                  TEXT NOT NULL,
    progress                INTEGER NOT NULL DEFAULT 0,
    state_version           INTEGER NOT NULL DEFAULT 1,
    steps_json              TEXT,
    input_revision_ids_json TEXT,
    input_digest            TEXT,
    params_json             TEXT,
    model_lock_json         TEXT,
    artifact_ids_json       TEXT,
    error_json              TEXT,
    created_at              TEXT NOT NULL,
    updated_at              TEXT NOT NULL
);

CREATE INDEX idx_tasks_project_status ON tasks (project_id, status, updated_at);
CREATE INDEX idx_tasks_run ON tasks (run_id);
CREATE INDEX idx_tasks_segment ON tasks (segment_id);

CREATE TABLE attempts (
    id          TEXT PRIMARY KEY,
    task_id     TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    step_id     TEXT NOT NULL,
    attempt_no  INTEGER NOT NULL,
    epoch       INTEGER NOT NULL,
    executor_id TEXT,
    status      TEXT NOT NULL,
    started_at  TEXT,
    finished_at TEXT,
    UNIQUE (task_id, step_id, attempt_no)
);

CREATE INDEX idx_attempts_task ON attempts (task_id, attempt_no);

CREATE TABLE artifact_commits (
    id             TEXT PRIMARY KEY,
    attempt_id     TEXT NOT NULL REFERENCES attempts (id) ON DELETE CASCADE,
    asset_ids_json TEXT NOT NULL,
    status         TEXT NOT NULL,
    committed_at   TEXT
);

CREATE INDEX idx_artifact_commits_attempt ON artifact_commits (attempt_id);

CREATE TABLE review_gates (
    id              TEXT PRIMARY KEY,
    run_id          TEXT NOT NULL REFERENCES workflow_runs (id) ON DELETE CASCADE,
    type            TEXT NOT NULL,
    language        TEXT,
    revision_digest TEXT NOT NULL,
    status          TEXT NOT NULL,
    confirmed_at    TEXT
);

CREATE INDEX idx_review_gates_run ON review_gates (run_id, type, language);

CREATE TABLE audio_candidates (
    id              TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    segment_id      TEXT NOT NULL,
    language        TEXT NOT NULL,
    revision        INTEGER NOT NULL,
    model_version   TEXT NOT NULL,
    audio_asset_id  TEXT NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    is_recommended  INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL
);

CREATE INDEX idx_audio_candidates_segment ON audio_candidates (segment_id, language, revision);

CREATE TABLE candidate_selections (
    segment_id   TEXT NOT NULL,
    language     TEXT NOT NULL,
    candidate_id TEXT NOT NULL REFERENCES audio_candidates (id) ON DELETE CASCADE,
    revision     INTEGER NOT NULL,
    selected_at  TEXT NOT NULL,
    PRIMARY KEY (segment_id, language)
);

CREATE TABLE installations (
    id             TEXT PRIMARY KEY,
    slot           TEXT NOT NULL,
    phase          TEXT NOT NULL,
    progress       INTEGER NOT NULL DEFAULT 0,
    verified_files INTEGER NOT NULL DEFAULT 0,
    error_json     TEXT,
    retryable      INTEGER NOT NULL DEFAULT 0,
    plan_id        TEXT,
    plan_hash      TEXT,
    release_json   TEXT,
    probe_passed   INTEGER NOT NULL DEFAULT 0,
    path_owner     TEXT,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);

CREATE INDEX idx_installations_slot ON installations (slot, created_at);

-- 工作区级持久事件。seq 由 SQLite AUTOINCREMENT 保证重启不归零、不复用。
-- prunable=1 表示可丢弃的进度类事件（不推进持久游标），保留策略可先清理。
CREATE TABLE events (
    seq            INTEGER PRIMARY KEY AUTOINCREMENT,
    schema_version INTEGER NOT NULL DEFAULT 1,
    type           TEXT NOT NULL,
    project_id     TEXT,
    task_id        TEXT,
    state_version  INTEGER,
    data_json      TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    prunable       INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_events_created ON events (created_at);
CREATE INDEX idx_events_project ON events (project_id, seq);

-- 幂等回执。幂等范围为 workspace + operation + key；
-- 相同摘要返回原回执，不同摘要返回 IDEMPOTENCY_CONFLICT。
CREATE TABLE receipts (
    workspace_id    TEXT NOT NULL,
    operation       TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_digest  TEXT NOT NULL,
    response_json   TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    expires_at      TEXT,
    PRIMARY KEY (workspace_id, operation, idempotency_key)
);

CREATE INDEX idx_receipts_created ON receipts (created_at);
