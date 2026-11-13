package scheduler

import (
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
)

// 冻结输入导致的时间轴失败无法通过原地重试修复，错误信息不应推荐 retry_task。
func TestCloneTimelineErrorsDoNotOfferRetryAction(t *testing.T) {
	cases := []struct {
		code    string
		wantKey string
	}{
		{code: "CLONE_TIMELINE_OUT_OF_RANGE", wantKey: "task.clone_timeline_out_of_range"},
		{code: "CLONE_TIMELINE_UNKNOWN_DURATION", wantKey: "task.clone_timeline_unknown_duration"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			info := errorInfoFor(execadapter.Result{
				Error: &execadapter.ExecError{Code: tc.code, Message: "clone timeline rejected", Retryable: false},
			})
			if info.Code != domain.ErrorCodeInvalidRequest || info.MessageKey != tc.wantKey {
				t.Fatalf("errorInfoFor = %+v", info)
			}
			if len(info.ActionIDs) != 0 {
				t.Fatalf("retry action offered for frozen clone failure: %v", info.ActionIDs)
			}
		})
	}
}
