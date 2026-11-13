-- 共享发布安装回滚（schema 10 → 9）。
--
-- 旧版 Core 只能表达每行原槽位的 done 选择。存在新共享布局记录，
-- 或当前/保留引用无法用旧语义等价表达时拒绝回滚；恢复走迁移前一致备份，
-- 不静默丢数据。仅迁移回填的旧数据可以安全 Down，并保留全部行。

CREATE TEMP TABLE schema10_down_guard (ok INTEGER NOT NULL CHECK (ok = 1));

-- 1) 新布局记录（storage_key 非空）旧版无法表达其物理身份/共享引用。
INSERT INTO schema10_down_guard (ok)
SELECT 0 WHERE EXISTS (SELECT 1 FROM installations WHERE storage_key IS NOT NULL);

-- 2) 跨槽位引用：旧行只能由自身 slot 解释。
INSERT INTO schema10_down_guard (ok)
SELECT 0 WHERE EXISTS (
    SELECT 1 FROM installation_slot_bindings b
    JOIN installations i ON i.id = b.installation_id
    WHERE b.slot <> i.slot
);

-- 3) 当前引用必须仍是旧排序的第一个 done（否则是回滚/切换后的选择变化）。
INSERT INTO schema10_down_guard (ok)
SELECT 0 WHERE EXISTS (
    SELECT 1 FROM installation_slot_bindings b
    WHERE b.is_current = 1 AND NOT EXISTS (
        SELECT 1 FROM installations i
        WHERE i.id = b.installation_id AND i.slot = b.slot AND i.phase = 'done'
          AND NOT EXISTS (
              SELECT 1 FROM installations j
              WHERE j.slot = i.slot AND j.phase = 'done'
                AND (j.created_at > i.created_at OR (j.created_at = i.created_at AND j.id < i.id))
          )
    )
);

-- 4) 每个槽位按旧排序应视为当前的 done 行必须仍指向当前引用；
--    否则旧版会把已解绑/被替换的版本重新视为可用。
INSERT INTO schema10_down_guard (ok)
SELECT 0 WHERE EXISTS (
    SELECT 1 FROM installations i
    WHERE i.phase = 'done'
      AND NOT EXISTS (
          SELECT 1 FROM installations j
          WHERE j.slot = i.slot AND j.phase = 'done'
            AND (j.created_at > i.created_at OR (j.created_at = i.created_at AND j.id < i.id))
      )
      AND NOT EXISTS (
          SELECT 1 FROM installation_slot_bindings b
          WHERE b.installation_id = i.id AND b.slot = i.slot AND b.is_current = 1
      )
);

DROP TABLE schema10_down_guard;

DROP INDEX IF EXISTS idx_installation_slot_current;
DROP INDEX IF EXISTS idx_installation_slot_retained;
DROP TABLE installation_slot_bindings;

DROP INDEX IF EXISTS idx_installations_storage_key;
ALTER TABLE installations DROP COLUMN storage_key;
ALTER TABLE installations DROP COLUMN covered_slots_json;
ALTER TABLE installations DROP COLUMN manifest_json;
