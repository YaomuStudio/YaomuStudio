package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// pipelineTaskID 生成流水线任务的确定性身份。
//
// 流水线推进会在 Run 创建、节点成功、审核放行、恢复等多个事务入口重复触发；
// 用 (Run, 节点, 语言, 句段, 输入摘要) 的稳定哈希作为 Task ID，重复触发时
// 命中同一行并跳过创建，不依赖“先查后写”的竞态窗口，也不需要新 schema。
//
// 重试仍是同一 Task 的新 Attempt；重新生成候选等显式用户动作继续使用随机 ID
// 的独立 Task，不复用该身份。
func pipelineTaskID(parts ...string) domain.ID {
	doc, _ := json.Marshal(struct {
		Parts []string `json:"parts"`
	}{Parts: parts})
	sum := sha256.Sum256(doc)
	return domain.ID("task-" + hex.EncodeToString(sum[:16]))
}
