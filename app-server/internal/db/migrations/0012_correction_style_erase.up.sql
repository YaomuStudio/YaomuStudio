-- 0012 人工校正阶段二：revision 级样式/擦除块占位与审核创建时间。
--
-- 新增列均可空；旧行读为 nil，旧 revision 摘要不变。erase_blocks 只是
-- provisional 契约占位，不据此创建 05 任务，也不含运行期状态。

ALTER TABLE subtitle_revisions ADD COLUMN style_json TEXT;
ALTER TABLE subtitle_revisions ADD COLUMN erase_blocks_json TEXT;
ALTER TABLE review_gates ADD COLUMN created_at TEXT;
