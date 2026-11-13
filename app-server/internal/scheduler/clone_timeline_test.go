package scheduler

import (
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
)

func TestCloneTimelinePreparationFailureStaysReadable(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode string
		wantKey  string
	}{
		{name: "out of range", err: domain.ErrCloneTimelineOutOfRange, wantCode: "CLONE_TIMELINE_OUT_OF_RANGE", wantKey: "task.clone_timeline_out_of_range"},
		{name: "unknown duration", err: domain.ErrCloneTimelineUnknownDuration, wantCode: "CLONE_TIMELINE_UNKNOWN_DURATION", wantKey: "task.clone_timeline_unknown_duration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prepared := prepareFailure(tc.err)
			if prepared.Code != tc.wantCode {
				t.Fatalf("prepareFailure code = %s, want %s", prepared.Code, tc.wantCode)
			}
			info := errorInfoFor(execadapter.Result{Error: prepared})
			if info.Code != domain.ErrorCodeInvalidRequest || info.MessageKey != tc.wantKey {
				t.Fatalf("errorInfoFor = %+v", info)
			}
		})
	}
}
