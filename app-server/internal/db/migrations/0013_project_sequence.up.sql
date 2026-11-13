-- 0013 项目本地序号：卡片编号使用按创建顺序分配的单调序号，
-- 删除项目后不回收，避免编号在列表里漂移。

ALTER TABLE projects ADD COLUMN seq INTEGER NOT NULL DEFAULT 0;

WITH ordered AS (
  SELECT id, ROW_NUMBER() OVER (
    PARTITION BY workspace_id ORDER BY created_at ASC, id ASC
  ) AS n
  FROM projects
)
UPDATE projects
SET seq = (SELECT n FROM ordered WHERE ordered.id = projects.id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_workspace_seq
  ON projects(workspace_id, seq);
