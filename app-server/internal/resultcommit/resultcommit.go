// Package resultcommit 定义所有产物任务成功前的提交契约（节点、句段和导出）。
//
// 调度器只依赖该接口：准备阶段在事务外完成文件校验/落盘，提交阶段在任务
// 终态事务内登记 Asset/ArtifactCommit、回写业务产物并推进流水线。接口放在
// 中立包，避免 service 与 scheduler 互相 import 形成测试环。
package resultcommit

import (
	"context"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// Prepared 是一次已通过校验、等待随任务终态一起提交的产物计划。
type Prepared interface {
	// ArtifactIDs 是准备阶段生成、提交后写入 task.artifact_ids 的 Asset ID。
	ArtifactIDs() []domain.ID
	// Commit 在任务终态事务内执行；返回错误时整个终态事务回滚。
	Commit(ctx context.Context, tx *store.Tx, task domain.Task, events *[]domain.DomainEvent) error
}
