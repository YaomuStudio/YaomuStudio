package resultcommit

// ExecutionResult 是调度层向业务提交层传递的最小执行结果视图。
//
// 只包含成功前提交所需的产物清单、输出目录与 attempt 绑定。协议错误、
// transport 状态、停止确认与产物 ID 覆盖仍由调度层使用完整执行结果处理，
// 不进入业务层。
type ExecutionResult struct {
	// Outputs 是执行器回传的产物描述，路径相对 OutputDir。
	Outputs []ExecutionOutput
	// OutputDir 是本次尝试的专属输出目录（Core 分配）。
	OutputDir string
	// Binding 是结果的执行绑定，用于拒绝迟到/旧 Attempt 的导出回写。
	Binding ExecutionBinding
}

// ExecutionOutput 是一个执行产物描述。
//
// RelPath 只允许相对 attempt 输出目录；业务层必须校验后再落盘。
type ExecutionOutput struct {
	RelPath   string
	SizeBytes int64
	// Media 是可选媒体属性（ffprobe 结果摘要）；工具缺失时为空。
	Media map[string]any
}

// ExecutionBinding 是结果绑定中业务可见的字段。
type ExecutionBinding struct {
	TaskID  string
	StepID  string
	Attempt int
}
