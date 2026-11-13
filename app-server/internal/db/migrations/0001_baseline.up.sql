-- 应用级键值元数据（非业务领域表）。
-- 后续可用于记录 last_run_app_version 等升级门控所需事实。
CREATE TABLE app_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
