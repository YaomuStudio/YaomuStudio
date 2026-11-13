-- 0003 API 面所需的本地持久化（内部实现，不改变跨进程契约）。
--
-- 对应任务 appserver-api-scheduler（design.md）：
--   engine_configs  六槽位受控配置（GET/PUT /api/v1/engines/{slot}/config）
--   exports         导出任务记录（GET/POST /api/v1/exports）
--
-- 说明：第三方本机 API 的总开关与令牌哈希沿用 0001 的 app_meta（键值表），
-- 不新增第二套设置真相；这里只补两类需要按行查询/过滤的业务记录。

CREATE TABLE engine_configs (
    slot        TEXT PRIMARY KEY,
    device_id   TEXT,
    values_json TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE exports (
    id                  TEXT PRIMARY KEY,
    project_id          TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    run_id              TEXT,
    kind                TEXT NOT NULL,
    format              TEXT NOT NULL,
    status              TEXT NOT NULL,
    input_revision_id   TEXT,
    output_dir_grant    TEXT NOT NULL,
    naming_template     TEXT NOT NULL,
    output_path         TEXT,
    task_id             TEXT,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

CREATE INDEX idx_exports_project ON exports (project_id, created_at DESC);
CREATE INDEX idx_exports_task ON exports (task_id);
