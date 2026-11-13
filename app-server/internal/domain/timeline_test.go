package domain

import (
	"errors"
	"testing"
)

func TestValidateMediaTimeline(t *testing.T) {
	cases := []struct {
		name     string
		duration Millis
		windows  []TimelineWindow
		want     error
	}{
		{name: "known duration no windows", duration: 2000, want: nil},
		{name: "boundary end equals duration", duration: 2000,
			windows: []TimelineWindow{{StartMs: 1000, EndMs: 2000}}, want: nil},
		{name: "zero start", duration: 2000,
			windows: []TimelineWindow{{StartMs: 0, EndMs: 500}}, want: nil},
		{name: "past duration", duration: 2000,
			windows: []TimelineWindow{{StartMs: 1500, EndMs: 2001}}, want: ErrCloneTimelineOutOfRange},
		{name: "negative start", duration: 2000,
			windows: []TimelineWindow{{StartMs: -1, EndMs: 500}}, want: ErrCloneTimelineOutOfRange},
		{name: "end before start", duration: 2000,
			windows: []TimelineWindow{{StartMs: 600, EndMs: 500}}, want: ErrCloneTimelineOutOfRange},
		{name: "zero length", duration: 2000,
			windows: []TimelineWindow{{StartMs: 500, EndMs: 500}}, want: ErrCloneTimelineOutOfRange},
		{name: "unknown zero duration", duration: 0,
			windows: []TimelineWindow{{StartMs: 0, EndMs: 500}}, want: ErrCloneTimelineUnknownDuration},
		{name: "unknown negative duration", duration: -1,
			windows: []TimelineWindow{{StartMs: 0, EndMs: 500}}, want: ErrCloneTimelineUnknownDuration},
		{name: "unknown duration cannot pass empty windows", duration: 0,
			windows: nil, want: ErrCloneTimelineUnknownDuration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMediaTimeline(tc.duration, tc.windows)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ValidateMediaTimeline(%d, %+v) = %v, want %v", tc.duration, tc.windows, err, tc.want)
			}
			if tc.want != nil {
				code, ok := CodeOf(err)
				if !ok || code != ErrorCodeInvalidRequest {
					t.Fatalf("error code = %v/%v, want INVALID_REQUEST", code, ok)
				}
			}
		})
	}
}

func TestSegmentTimelineWindowsProjection(t *testing.T) {
	segments := []Segment{
		{ID: "a", StartMs: 0, EndMs: 100},
		{ID: "b", StartMs: 200, EndMs: 300},
	}
	windows := SegmentTimelineWindows(segments)
	if len(windows) != 2 || windows[0] != (TimelineWindow{StartMs: 0, EndMs: 100}) ||
		windows[1] != (TimelineWindow{StartMs: 200, EndMs: 300}) {
		t.Fatalf("SegmentTimelineWindows() = %+v", windows)
	}
}
