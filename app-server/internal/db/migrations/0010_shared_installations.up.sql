-- 共享发布安装（schema 10）。
--
-- 一个新布局的安装实体可服务多个显式槽位引用；旧记录只回填原槽位引用，
-- 不移动/去重旧目录、不扩覆盖、不伪造快照或 probe 证据。
-- storage_key 为完整 ReleaseRef（含 channel）的 SHA-256；旧记录为 NULL。
-- covered_slots_json/manifest_json 是内部字段，不经 API 暴露。

ALTER TABLE installations ADD COLUMN storage_key TEXT;
ALTER TABLE installations ADD COLUMN covered_slots_json TEXT;
ALTER TABLE installations ADD COLUMN manifest_json TEXT;

-- 同一 storage_key 最多一个进行中或已验证的物理拥有者；
-- 失败/取消历史允许保留，但不再同时授权新 writer。
CREATE UNIQUE INDEX idx_installations_storage_key
    ON installations (storage_key)
    WHERE storage_key IS NOT NULL
      AND phase IN ('planning', 'downloading', 'verifying', 'installing', 'done');

CREATE TABLE installation_slot_bindings (
    installation_id TEXT NOT NULL REFERENCES installations (id) ON DELETE CASCADE,
    slot            TEXT NOT NULL,
    is_current      INTEGER NOT NULL DEFAULT 0,
    activated_at    TEXT NOT NULL,
    PRIMARY KEY (installation_id, slot)
);

-- 每个槽位最多一个当前引用；其他引用是保留历史/回滚候选。
CREATE UNIQUE INDEX idx_installation_slot_current
    ON installation_slot_bindings (slot)
    WHERE is_current = 1;

CREATE INDEX idx_installation_slot_retained
    ON installation_slot_bindings (slot, activated_at);

-- 只回填每行原槽位；当前选择保持旧排序（created_at DESC, id ASC 的首个 done）。
INSERT INTO installation_slot_bindings (installation_id, slot, is_current, activated_at)
SELECT i.id, i.slot,
       CASE WHEN i.phase = 'done' AND NOT EXISTS (
           SELECT 1 FROM installations j
           WHERE j.slot = i.slot AND j.phase = 'done'
             AND (j.created_at > i.created_at OR (j.created_at = i.created_at AND j.id < i.id))
       ) THEN 1 ELSE 0 END,
       i.created_at
FROM installations i;
