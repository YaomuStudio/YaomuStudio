package service

import "github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"

// cloneTimelineRetryError 返回冻结媒体/时间轴失败对应的安全领域错误。
//
// 这两类失败来自已冻结的输入本身：原地重试只会再次使用同一无效译稿/媒体，
// 必须修正输入后新建 Run。其他可重试失败（如模型/执行器故障）不在此列。
func cloneTimelineRetryError(task domain.Task) error {
	if task.NodeKey != domain.NodeKeyClone || task.Error == nil {
		return nil
	}
	if task.Error.Code != domain.ErrorCodeInvalidRequest {
		return nil
	}
	var messageKey string
	switch task.Error.MessageKey {
	case "task.clone_timeline_out_of_range":
		messageKey = domain.ErrCloneTimelineOutOfRange.MessageKey
	case "task.clone_timeline_unknown_duration":
		messageKey = domain.ErrCloneTimelineUnknownDuration.MessageKey
	default:
		return nil
	}
	// 保留 retry 端点既有 409 输入冲突语义，持久化 Task.Error 不变。
	return &domain.Error{
		Code:       domain.ErrorCodeRevisionConflict,
		MessageKey: messageKey,
		Retryable:  false,
		ActionIDs:  []domain.ID{},
	}
}
