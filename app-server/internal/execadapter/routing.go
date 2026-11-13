package execadapter

import (
	"context"
	"errors"
	"strings"
)

// 已知宿主媒体适配器；宿主 Runner 存在时优先走宿主，否则可回退模型侧（WSL 镜像）。
const hostMediaAdapterID = "ffmpeg"

// RoutingExecutor 按 adapter.id 把 Attempt 分到宿主或模型侧 Runner。
//
// 方案 2：ffmpeg/flashcli/separate/voice_clone 统一走 WSL 镜像 Runner；
// 若显式配置了宿主 Runner，ffmpeg 仍优先走宿主（开发/回归兼容）。
// Model 为 nil 且 ModelRequired=false 时回退 Host（测试/无 WSL 开发兼容）。
// ModelRequired=true 且 Model=nil 时模型任务返回可读不可用错误。
type RoutingExecutor struct {
	Host          *Executor
	Model         *Executor
	ModelRequired bool
}

// Execute 实现 scheduler.Executor。
func (r *RoutingExecutor) Execute(ctx context.Context, req Request, progress ProgressFunc, logf LogFunc) Result {
	if r == nil {
		return Result{TransportErr: context.Canceled}
	}
	hostMedia := isHostMediaAdapter(req.Adapter.ID)
	if hostMedia && r.Host != nil {
		return r.Host.Execute(ctx, req, progress, logf)
	}
	if r.Model != nil {
		translated := translateRequestForWSL(req)
		result := r.Model.Execute(ctx, translated, progress, logf)
		return translateResultFromWSL(result)
	}
	if hostMedia {
		return Result{Error: &ExecError{
			Code:      "MEDIA_TOOL_UNAVAILABLE",
			Message:   "media runner is not configured",
			Retryable: true,
			ActionIDs: []string{"recheck_environment"},
		}}
	}
	if r.ModelRequired {
		return Result{Error: &ExecError{
			Code:      "MODEL_RUNTIME_UNAVAILABLE",
			Message:   "WSL model runner is not available",
			Retryable: true,
			ActionIDs: []string{"install_wsl", "recheck_environment"},
		}}
	}
	if r.Host == nil {
		return Result{Error: &ExecError{
			Code:      "MODEL_RUNTIME_UNAVAILABLE",
			Message:   "runner is not configured",
			Retryable: true,
		}}
	}
	return r.Host.Execute(ctx, req, progress, logf)
}

// Capacity 恒为 1：Host 与 Model 共用一个调度槽池，派发前没有按目标 Runner
// 分别计数，不能用两者容量之和；提高重型槽配置也不能向同一 Runner 重叠派发。
func (*RoutingExecutor) Capacity() int { return 1 }

// IsolationErr 合并 Host/Model 两侧的永久隔离状态：任一侧隔离即 fail-closed，
// 调度器停止新派发；两侧健康时返回 nil，不误隔离。
func (r *RoutingExecutor) IsolationErr() error {
	if r == nil {
		return nil
	}
	var errs []error
	if r.Host != nil {
		if err := r.Host.IsolationErr(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.Model != nil {
		if err := r.Model.IsolationErr(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Identity 组合 Host/Model 的诊断身份，省略空值与 nil Client；身份只用于诊断，
// 不作为终止进程依据。
func (r *RoutingExecutor) Identity() string {
	if r == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if r.Host != nil && r.Host.Client != nil {
		if id := r.Host.Identity(); id != "" {
			parts = append(parts, "host:"+id)
		}
	}
	if r.Model != nil && r.Model.Client != nil {
		if id := r.Model.Identity(); id != "" {
			parts = append(parts, "model:"+id)
		}
	}
	return strings.Join(parts, "|")
}

func isHostMediaAdapter(id string) bool {
	return strings.EqualFold(strings.TrimSpace(id), hostMediaAdapterID)
}
