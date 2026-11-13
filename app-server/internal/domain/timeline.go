package domain

// 媒体时间轴约束。
//
// 结构错误（负时间、end<=start）与“按媒体时长越界”是两类事实：前者由
// 字幕保存入口始终校验；后者只在所需媒体时长已知且 Run 开启
// validate_timestamps 时由 Core 校验。未知/非正时长不能被当作通过。

// ErrCloneTimelineOutOfRange 表示时间窗自身不合法或超出已冻结媒体的时长。
var ErrCloneTimelineOutOfRange = &Error{
	Code:       ErrorCodeInvalidRequest,
	MessageKey: "error.clone_timeline_out_of_range",
	Retryable:  false,
	ActionIDs:  []ID{},
}

// ErrCloneTimelineUnknownDuration 表示媒体缺少可靠时长，无法完成时间轴校验。
var ErrCloneTimelineUnknownDuration = &Error{
	Code:       ErrorCodeInvalidRequest,
	MessageKey: "error.clone_timeline_unknown_duration",
	Retryable:  false,
	ActionIDs:  []ID{},
}

// TimelineWindow 是待校验的媒体时间窗（整数毫秒）。
type TimelineWindow struct {
	StartMs Millis
	EndMs   Millis
}

// SegmentTimelineWindows 把字幕句段投影为时间窗，只用于时间轴校验。
func SegmentTimelineWindows(segments []Segment) []TimelineWindow {
	windows := make([]TimelineWindow, 0, len(segments))
	for _, segment := range segments {
		windows = append(windows, TimelineWindow{StartMs: segment.StartMs, EndMs: segment.EndMs})
	}
	return windows
}

// ValidateMediaTimeline 校验时间窗非负、非空且不超出媒体时长。
//
// duration 必须是 > 0 的真实媒体时长；end == duration 允许，end > duration
// 拒绝。未知/非正时长返回 ErrCloneTimelineUnknownDuration，不假定通过。
func ValidateMediaTimeline(duration Millis, windows []TimelineWindow) error {
	if duration <= 0 {
		return ErrCloneTimelineUnknownDuration
	}
	for _, window := range windows {
		if window.StartMs < 0 || window.EndMs <= window.StartMs || window.EndMs > duration {
			return ErrCloneTimelineOutOfRange
		}
	}
	return nil
}
