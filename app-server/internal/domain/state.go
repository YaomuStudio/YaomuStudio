package domain

// 状态机与业务规则（纯函数，无 I/O）。
//
// 依据：
//   - docs/initialization-and-task-lifecycle.md §4（Task 状态转换表）
//   - docs/workflow-and-review.md §3/§6/§8（审核、暂停、派发闸门）
//   - docs/local-api-contract.md §8（Run 统一派发闸门）
//   - contracts/openapi.yaml（枚举口径）

import (
	"fmt"
	"time"
)

// runTransitions 是 Run 的合法状态迁移表。
//
// PAUSED 只能由 PAUSE_REQUESTED 在安全单元边界确认得到；恢复重新预检后回 QUEUED。
// 非终止状态都可以进入终止状态（取消/失败/部分成功/成功），终止状态不可再迁移。
var runTransitions = map[RunStatus]map[RunStatus]bool{
	RunStatusDraft: {
		RunStatusQueued:   true,
		RunStatusCanceled: true,
	},
	RunStatusQueued: {
		RunStatusRunning:        true,
		RunStatusPauseRequested: true,
		RunStatusFailed:         true,
		RunStatusCanceled:       true,
	},
	RunStatusRunning: {
		RunStatusPauseRequested: true,
		RunStatusWaitingReview:  true,
		RunStatusSucceeded:      true,
		RunStatusPartial:        true,
		RunStatusFailed:         true,
		RunStatusCanceled:       true,
	},
	RunStatusPauseRequested: {
		RunStatusPaused:    true,
		RunStatusSucceeded: true,
		RunStatusPartial:   true,
		RunStatusFailed:    true,
		RunStatusCanceled:  true,
	},
	RunStatusPaused: {
		RunStatusQueued:   true,
		RunStatusFailed:   true,
		RunStatusCanceled: true,
	},
	RunStatusWaitingReview: {
		RunStatusRunning:        true,
		RunStatusPauseRequested: true,
		RunStatusSucceeded:      true,
		RunStatusPartial:        true,
		RunStatusFailed:         true,
		RunStatusCanceled:       true,
	},
	RunStatusPartial:   {},
	RunStatusSucceeded: {},
	RunStatusFailed:    {},
	RunStatusCanceled:  {},
}

// CanTransitionRun 报告 Run 是否允许从 from 迁移到 to。
func CanTransitionRun(from, to RunStatus) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	return runTransitions[from][to]
}

// TransitionRun 校验并返回目标状态；非法迁移返回 ErrInvalidTransition。
// 相同状态不是迁移，由调用方按幂等/进度更新处理。
func TransitionRun(from, to RunStatus) (RunStatus, error) {
	if from == to {
		return "", fmt.Errorf("%w: run already %s", ErrInvalidTransition, from)
	}
	if !CanTransitionRun(from, to) {
		return "", fmt.Errorf("%w: run %s -> %s", ErrInvalidTransition, from, to)
	}
	return to, nil
}

// RunIsPaused 报告 Run 是否处于暂停受理或已暂停（只登记待执行工作，不派发）。
func RunIsPaused(s RunStatus) bool {
	return s == RunStatusPauseRequested || s == RunStatusPaused
}

// RunAllowsDispatch 报告该 Run 状态是否允许派发新模型任务。
//
// PAUSE_REQUESTED/PAUSED 只登记不派发；终止 Run 拒绝新执行；
// DRAFT 尚未正式创建，也不派发。
func RunAllowsDispatch(s RunStatus) bool {
	switch s {
	case RunStatusQueued, RunStatusRunning, RunStatusWaitingReview:
		return true
	default:
		return false
	}
}

// RunAllowsReviewApproval 报告该 Run 状态是否允许人工放行。
//
// 终止 Run 拒绝放行；PAUSED 允许保存审核记录，但后续任务仅登记不派发。
func RunAllowsReviewApproval(s RunStatus) bool {
	return s.Valid() && !s.Terminal()
}

// taskTransitions 是 Task 的合法状态迁移表。
//
// FAILED / INTERRUPTED 只能在显式重试时回到 QUEUED；SUCCEEDED / CANCELED 无后续。
// CANCEL_REQUESTED 仍可能先等到成功提交（SUCCEEDED）或无法确认停止（INTERRUPTED）。
var taskTransitions = map[TaskStatus]map[TaskStatus]bool{
	TaskStatusQueued: {
		TaskStatusWaitingResource: true,
		TaskStatusRunning:         true,
		TaskStatusPauseRequested:  true,
		TaskStatusCanceled:        true,
		TaskStatusFailed:          true,
	},
	TaskStatusWaitingResource: {
		TaskStatusRunning:        true,
		TaskStatusPauseRequested: true,
		TaskStatusCanceled:       true,
		TaskStatusFailed:         true,
	},
	TaskStatusRunning: {
		TaskStatusSucceeded:       true,
		TaskStatusFailed:          true,
		TaskStatusCancelRequested: true,
		TaskStatusInterrupted:     true,
		TaskStatusPauseRequested:  true,
	},
	TaskStatusCancelRequested: {
		TaskStatusCanceled:    true,
		TaskStatusSucceeded:   true,
		TaskStatusInterrupted: true,
	},
	TaskStatusPauseRequested: {
		TaskStatusPaused:      true,
		TaskStatusSucceeded:   true,
		TaskStatusFailed:      true,
		TaskStatusCanceled:    true,
		TaskStatusInterrupted: true,
	},
	TaskStatusPaused: {
		TaskStatusQueued:   true,
		TaskStatusRunning:  true,
		TaskStatusCanceled: true,
		TaskStatusFailed:   true,
	},
	TaskStatusFailed: {
		TaskStatusQueued: true,
	},
	TaskStatusInterrupted: {
		TaskStatusQueued: true,
	},
	TaskStatusSucceeded: {},
	TaskStatusCanceled:  {},
}

// CanTransitionTask 报告 Task 是否允许从 from 迁移到 to。
func CanTransitionTask(from, to TaskStatus) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	return taskTransitions[from][to]
}

// TransitionTask 校验并返回目标状态；非法迁移返回 ErrInvalidTransition。
func TransitionTask(from, to TaskStatus) (TaskStatus, error) {
	if from == to {
		return "", fmt.Errorf("%w: task already %s", ErrInvalidTransition, from)
	}
	if !CanTransitionTask(from, to) {
		return "", fmt.Errorf("%w: task %s -> %s", ErrInvalidTransition, from, to)
	}
	return to, nil
}

// TaskRetryable 报告 Task 是否允许显式重试（重试产生新 Attempt 并回到 QUEUED）。
//
// CANCELED 是终止状态（生命周期文档 §4：重新生成是新任务），不能原地重试。
func TaskRetryable(s TaskStatus) bool {
	return s == TaskStatusFailed || s == TaskStatusInterrupted
}

// PrepareTaskRetry 校验重试前置条件并返回回到 QUEUED 的任务副本。
//
// 输入摘要发生变化时返回 ErrRevisionConflict（不能把新输入冒充原输入）；
// 状态不可重试时返回 ErrInvalidTransition。
func PrepareTaskRetry(t Task, currentInputDigest string) (Task, error) {
	if !TaskRetryable(t.Status) {
		return t, fmt.Errorf("%w: retry from %s", ErrInvalidTransition, t.Status)
	}
	if t.InputDigest != "" && currentInputDigest != "" && t.InputDigest != currentInputDigest {
		return t, fmt.Errorf("%w: task input changed", ErrRevisionConflict)
	}
	next, err := TransitionTask(t.Status, TaskStatusQueued)
	if err != nil {
		return t, err
	}
	t.Status = next
	t.Progress = 0
	return t, nil
}

// NextAttemptNo 返回下一个单调递增的 attempt 编号（不复用旧编号）。
func NextAttemptNo(attempts []Attempt) int {
	max := 0
	for _, a := range attempts {
		if a.AttemptNo > max {
			max = a.AttemptNo
		}
	}
	return max + 1
}

// NodeRequiresReview 报告节点是否必须经人工审核才能完成（06 翻译、08 交付）。
func NodeRequiresReview(k PipelineNodeKey) bool {
	return k == NodeKeyTranslate || k == NodeKeyMix
}

// nodeTransitions 是节点状态的合法迁移表。
var nodeTransitions = map[NodeStatus]map[NodeStatus]bool{
	NodeStatusPending: {
		NodeStatusRunning: true,
		NodeStatusSkipped: true,
		NodeStatusReused:  true,
		NodeStatusFailed:  true,
	},
	NodeStatusRunning: {
		NodeStatusWaitingReview: true,
		NodeStatusDone:          true,
		NodeStatusFailed:        true,
	},
	NodeStatusWaitingReview: {
		NodeStatusDone:   true,
		NodeStatusFailed: true,
	},
	NodeStatusDone:    {},
	NodeStatusSkipped: {},
	NodeStatusReused:  {},
	// 显式重试失败任务时允许节点回到 running；终态本身仍不能被普通推进改写。
	NodeStatusFailed: {NodeStatusRunning: true},
}

// CanTransitionNode 报告节点是否允许从 from 迁移到 to（含全局 06/08 审核门槛）。
func CanTransitionNode(from, to NodeStatus, key PipelineNodeKey) bool {
	return CanTransitionNodeWithReview(from, to, key, NodeRequiresReview(key))
}

// CanTransitionNodeWithReview 由调用方显式提供审核要求；模板可据此跳过 06 审核。
func CanTransitionNodeWithReview(from, to NodeStatus, key PipelineNodeKey, requiresReview bool) bool {
	if !from.Valid() || !to.Valid() || !key.Valid() {
		return false
	}
	if !nodeTransitions[from][to] {
		return false
	}
	// 只有需要审核的节点才能停在 waiting_review。
	if to == NodeStatusWaitingReview && !requiresReview {
		return false
	}
	// 需要审核的节点必须经 waiting_review 放行后才能 done，不能直接标记完成。
	if to == NodeStatusDone && requiresReview && from != NodeStatusWaitingReview {
		return false
	}
	return true
}

// TransitionNode 校验并返回目标状态；非法迁移返回 ErrInvalidTransition。
func TransitionNode(from, to NodeStatus, key PipelineNodeKey) (NodeStatus, error) {
	return TransitionNodeWithReview(from, to, key, NodeRequiresReview(key))
}

// TransitionNodeWithReview 是 TransitionNode 的模板感知版本。
func TransitionNodeWithReview(from, to NodeStatus, key PipelineNodeKey, requiresReview bool) (NodeStatus, error) {
	if from == to {
		return "", fmt.Errorf("%w: node already %s", ErrInvalidTransition, from)
	}
	if !CanTransitionNodeWithReview(from, to, key, requiresReview) {
		return "", fmt.Errorf("%w: node %s %s -> %s", ErrInvalidTransition, key, from, to)
	}
	return to, nil
}

// stepTransitions 是步骤/尝试状态的合法迁移表。
var stepTransitions = map[StepStatus]map[StepStatus]bool{
	StepStatusPending: {
		StepStatusRunning:  true,
		StepStatusFailed:   true,
		StepStatusCanceled: true,
	},
	StepStatusRunning: {
		StepStatusCommitting:  true,
		StepStatusSucceeded:   true,
		StepStatusFailed:      true,
		StepStatusCanceled:    true,
		StepStatusInterrupted: true,
	},
	StepStatusCommitting: {
		StepStatusSucceeded:   true,
		StepStatusFailed:      true,
		StepStatusCanceled:    true,
		StepStatusInterrupted: true,
	},
	StepStatusSucceeded:   {},
	StepStatusFailed:      {},
	StepStatusCanceled:    {},
	StepStatusInterrupted: {},
}

// CanTransitionStep 报告步骤是否允许从 from 迁移到 to。
func CanTransitionStep(from, to StepStatus) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	return stepTransitions[from][to]
}

// TransitionStep 校验并返回目标步骤状态。
func TransitionStep(from, to StepStatus) (StepStatus, error) {
	if from == to {
		return "", fmt.Errorf("%w: step already %s", ErrInvalidTransition, from)
	}
	if !CanTransitionStep(from, to) {
		return "", fmt.Errorf("%w: step %s -> %s", ErrInvalidTransition, from, to)
	}
	return to, nil
}

// installationTransitions 是安装阶段的合法迁移表。
var installationTransitions = map[InstallationPhase]map[InstallationPhase]bool{
	InstallationPhasePlanning: {
		InstallationPhaseDownloading: true,
		InstallationPhaseFailed:      true,
		InstallationPhaseCanceled:    true,
	},
	InstallationPhaseDownloading: {
		InstallationPhaseVerifying: true,
		InstallationPhaseFailed:    true,
		InstallationPhaseCanceled:  true,
	},
	InstallationPhaseVerifying: {
		InstallationPhaseInstalling: true,
		InstallationPhaseFailed:     true,
		InstallationPhaseCanceled:   true,
	},
	InstallationPhaseInstalling: {
		InstallationPhaseDone:     true,
		InstallationPhaseFailed:   true,
		InstallationPhaseCanceled: true,
	},
	InstallationPhaseDone:     {},
	InstallationPhaseFailed:   {},
	InstallationPhaseCanceled: {},
}

// CanTransitionInstallationPhase 报告安装是否允许从 from 迁移到 to。
func CanTransitionInstallationPhase(from, to InstallationPhase) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	return installationTransitions[from][to]
}

// TransitionInstallationPhase 校验并返回目标安装阶段。
func TransitionInstallationPhase(from, to InstallationPhase) (InstallationPhase, error) {
	if from == to {
		return "", fmt.Errorf("%w: installation already %s", ErrInvalidTransition, from)
	}
	if !CanTransitionInstallationPhase(from, to) {
		return "", fmt.Errorf("%w: installation %s -> %s", ErrInvalidTransition, from, to)
	}
	return to, nil
}

// ApproveReviewGate 校验人工放行并返回 approved 的审核记录。
//
// 只有 pending 可放行；摘要必须与绑定 revision 完全一致（旧审核不能放行新内容）。
func ApproveReviewGate(g ReviewGate, revisionDigest string, typ ReviewType, now time.Time) (ReviewGate, error) {
	if !typ.Valid() {
		return g, fmt.Errorf("%w: invalid review type %q", ErrInvalidRequest, typ)
	}
	if typ != g.Type {
		return g, fmt.Errorf("%w: review type %s != %s", ErrInvalidRequest, typ, g.Type)
	}
	if g.Status != ReviewStatusPending {
		return g, fmt.Errorf("%w: review %s is %s", ErrInvalidTransition, g.ID, g.Status)
	}
	if revisionDigest == "" || revisionDigest != g.RevisionDigest {
		return g, fmt.Errorf("%w: review %s revision changed", ErrRevisionConflict, g.ID)
	}
	confirmed := now.UTC()
	g.Status = ReviewStatusApproved
	g.ConfirmedAt = &confirmed
	return g, nil
}

// NodeReviewDigest 计算节点审核（06 翻译 / 08 交付）的绑定摘要。
//
// 门槛创建与导出交付校验共用同一口径：摘要由持久化任务事实计算，
// 客户端不参与；导出门禁重算同一摘要，避免用旧放行冒充新产物。
func NodeReviewDigest(runID ID, node PipelineNodeKey, language string, taskID ID, artifacts []ID, inputDigest string) (string, error) {
	return Digest(struct {
		RunID     ID
		Node      PipelineNodeKey
		Language  string
		TaskID    ID
		Artifacts []ID
		Input     string
	}{
		RunID: runID, Node: node, Language: language, TaskID: taskID,
		Artifacts: artifacts, Input: inputDigest,
	})
}

// RevisionBindingDigest 计算字幕 revision 的审核绑定摘要。
//
// 句段内容（含 Core 计算的 content_digest）、作者与稳定 ID 一并参与；
// 整轨 revision 只做溯源，不进入句级缓存键（见 fp.go）。
func RevisionBindingDigest(rev SubtitleRevision) (string, error) {
	return Digest(struct {
		RevisionID  ID
		TrackID     ID
		Author      AuthorType
		Segments    []Segment
		Style       *SubtitleStyle `json:"style,omitempty"`
		EraseBlocks []EraseBlock   `json:"erase_blocks,omitempty"`
	}{
		RevisionID: rev.ID, TrackID: rev.TrackID,
		Author: rev.AuthorType, Segments: rev.Segments,
		Style: rev.Style, EraseBlocks: rev.EraseBlocks,
	})
}

// DeliveryBind 返回字幕 revision 对应的交付/导出审核摘要后缀。
// 译文轨的 translation 门槛使用原始 RevisionBindingDigest，不使用该后缀。
func DeliveryBind(digest string) string { return digest + ":delivery" }

// ValidateSegmentInputs 校验字幕修订输入的稳定 ID、时间窗与非重叠约束。
//
// 越界（超出媒体时长）需要媒体元数据，由 Core 在预检阶段校验；这里只做
// 与输入自身可判定的规则，非法时返回 ErrInvalidRequest。
func ValidateSegmentInputs(segments []SegmentInput) error {
	seen := make(map[ID]struct{}, len(segments))
	var prevEnd Millis
	for i, seg := range segments {
		if seg.StartMs < 0 {
			return fmt.Errorf("%w: segment %d start_ms < 0", ErrInvalidRequest, i)
		}
		if seg.EndMs <= seg.StartMs {
			return fmt.Errorf("%w: segment %d end_ms <= start_ms", ErrInvalidRequest, i)
		}
		if i > 0 && seg.StartMs < prevEnd {
			return fmt.Errorf("%w: segment %d overlaps previous window", ErrInvalidRequest, i)
		}
		if seg.ID != "" {
			if _, ok := seen[seg.ID]; ok {
				return fmt.Errorf("%w: duplicate segment id %q", ErrInvalidRequest, seg.ID)
			}
			seen[seg.ID] = struct{}{}
		}
		prevEnd = seg.EndMs
	}
	return nil
}

// MaxEraseBlocks 是单个 revision 允许的擦除块上限（契约 maxItems）。
const MaxEraseBlocks = 500

// ValidateEraseBlocks 校验 revision 级擦除块占位。
//
// segmentIDs 是本次 revision 最终句段 ID 集合；块只能引用其中已存在的句段。
// 只做契约可判定的几何/来源/唯一性校验，不代表擦除能力已就绪。
func ValidateEraseBlocks(blocks []EraseBlock, segmentIDs map[ID]struct{}) error {
	if len(blocks) > MaxEraseBlocks {
		return fmt.Errorf("%w: erase_blocks exceeds %d", ErrInvalidRequest, MaxEraseBlocks)
	}
	seen := make(map[ID]struct{}, len(blocks))
	for i, block := range blocks {
		if block.ID != "" {
			if _, ok := seen[block.ID]; ok {
				return fmt.Errorf("%w: erase block %d duplicates id %q", ErrInvalidRequest, i, block.ID)
			}
			seen[block.ID] = struct{}{}
		}
		if block.StartMs < 0 {
			return fmt.Errorf("%w: erase block %d start_ms < 0", ErrInvalidRequest, i)
		}
		if block.EndMs <= block.StartMs {
			return fmt.Errorf("%w: erase block %d end_ms <= start_ms", ErrInvalidRequest, i)
		}
		x1, y1, x2, y2 := block.BBox[0], block.BBox[1], block.BBox[2], block.BBox[3]
		if x1 < 0 || y1 < 0 || x2 <= x1 || y2 <= y1 {
			return fmt.Errorf("%w: erase block %d has invalid bbox %v", ErrInvalidRequest, i, block.BBox)
		}
		if !block.Source.Valid() {
			return fmt.Errorf("%w: erase block %d has invalid source %q", ErrInvalidRequest, i, block.Source)
		}
		if block.SegmentID != "" {
			if _, ok := segmentIDs[block.SegmentID]; !ok {
				return fmt.Errorf("%w: erase block %d references segment %q outside this revision",
					ErrInvalidRequest, i, block.SegmentID)
			}
		}
	}
	return nil
}

// ValidateSegmentLineage 校验新句段的 lineage 只引用 base revision 的直接来源。
//
// 只在句段省略/清空稳定 ID（合并/拆分产生的新句段）且提供非空 lineage 时校验：
// 每个引用必须非空、存在于 base revision、且在同一 lineage 中不重复。
// 不强制新句段必须带 lineage，避免影响其他保存路径。
func ValidateSegmentLineage(inputs []SegmentInput, base map[ID]Segment) error {
	baseIDs := make(map[ID]struct{}, len(base))
	for id := range base {
		baseIDs[id] = struct{}{}
	}
	for i, seg := range inputs {
		if seg.ID != "" || len(seg.Lineage) == 0 {
			continue
		}
		seen := make(map[ID]struct{}, len(seg.Lineage))
		for _, ref := range seg.Lineage {
			if ref == "" {
				return fmt.Errorf("%w: segment %d lineage contains an empty id", ErrInvalidRequest, i)
			}
			if _, ok := baseIDs[ref]; !ok {
				return fmt.Errorf("%w: segment %d lineage references %q outside the base revision", ErrInvalidRequest, i, ref)
			}
			if _, ok := seen[ref]; ok {
				return fmt.Errorf("%w: segment %d lineage repeats %q", ErrInvalidRequest, i, ref)
			}
			seen[ref] = struct{}{}
		}
	}
	return nil
}

// ValidateExtractOptions 校验节点 04 提取配置的契约枚举。
//
// ocr_language 缺省表示未指定（是否必需由预检按提取方式决定）；模式永远必需。
func ValidateExtractOptions(o *ExtractOptions) error {
	if o == nil {
		return nil
	}
	if !o.Mode.Valid() {
		return fmt.Errorf("%w: invalid extract mode %q", ErrInvalidRequest, o.Mode)
	}
	if o.OCRLanguage != "" && !o.OCRLanguage.Valid() {
		return fmt.Errorf("%w: invalid ocr language %q", ErrInvalidRequest, o.OCRLanguage)
	}
	if o.OCRQuality != "" && !o.OCRQuality.Valid() {
		return fmt.Errorf("%w: invalid ocr quality %q", ErrInvalidRequest, o.OCRQuality)
	}
	return nil
}

// ValidateSubtitleStyle 校验受控字幕样式的契约枚举与范围（SubtitleStyle）。
//
// align 缺省表示使用默认值；max_translation_lines 为 0 表示未指定（默认 3）。
func ValidateSubtitleStyle(s *SubtitleStyle) error {
	if s == nil {
		return nil
	}
	if s.Align != "" && !s.Align.Valid() {
		return fmt.Errorf("%w: invalid subtitle align %q", ErrInvalidRequest, s.Align)
	}
	if s.MaxTranslationLines != 0 && (s.MaxTranslationLines < 1 || s.MaxTranslationLines > 4) {
		return fmt.Errorf("%w: max_translation_lines %d out of range [1,4]",
			ErrInvalidRequest, s.MaxTranslationLines)
	}
	return nil
}

// ValidateRunConfig 校验创建 Run 时冻结配置的契约枚举与范围。
func ValidateRunConfig(cfg RunConfig) error {
	if err := ValidateExtractOptions(cfg.Extract); err != nil {
		return err
	}
	if cfg.EraseMode != "" && !cfg.EraseMode.Valid() {
		return fmt.Errorf("%w: invalid erase mode %q", ErrInvalidRequest, cfg.EraseMode)
	}
	if err := ValidateSubtitleStyle(cfg.SubtitleStyle); err != nil {
		return err
	}
	for slot, execution := range cfg.Execution {
		if !slot.Valid() {
			return fmt.Errorf("%w: invalid execution slot %q", ErrInvalidRequest, slot)
		}
		// FFmpeg and the mlstools translate tool have no model installation
		// record: production Runs freeze an empty model lock and must use the cpu
		// device. Translate may still carry a full model lock for a future local
		// model. Only the full ReleaseRef zero value is empty; a channel-only lock
		// is incomplete. Partially filled locks are still invalid, and all other
		// model slots require a full lock.
		emptyLock := execution.ModelLock == (ReleaseRef{})
		toolSlot := slot == CapabilitySlotFFmpeg || slot == CapabilitySlotTranslate
		if toolSlot && emptyLock {
			if execution.DeviceID != "cpu" {
				return fmt.Errorf("%w: execution device for %s must be cpu", ErrInvalidRequest, slot)
			}
		} else if execution.ModelLock.ReleaseID == "" || execution.ModelLock.Version == "" ||
			execution.ModelLock.Digest == "" {
			return fmt.Errorf("%w: incomplete execution release for %s", ErrInvalidRequest, slot)
		}
		if execution.DeviceID == "" || execution.DeviceID == "auto" {
			return fmt.Errorf("%w: execution device for %s must be frozen", ErrInvalidRequest, slot)
		}
		if execution.ContextRadius < 0 {
			return fmt.Errorf("%w: execution context radius for %s is negative", ErrInvalidRequest, slot)
		}
	}
	return nil
}
