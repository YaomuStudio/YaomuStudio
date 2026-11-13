package scheduler

import (
	"slices"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
)

func TestErrorInfoForCapabilityCodes(t *testing.T) {
	cases := []struct {
		name       string
		result     execadapter.Result
		wantCode   domain.ErrorCode
		wantKey    string
		retryable  bool
		wantAction []domain.ID
	}{
		{
			name:       "translate runtime unavailable asks for model setup",
			result:     capabilityResult("MODEL_RUNTIME_UNAVAILABLE", false, "open_model_setup"),
			wantCode:   domain.ErrorCodeModelNotInstalled,
			wantKey:    "task.worker_error",
			wantAction: []domain.ID{"open_model_setup"},
		},
		{
			name:       "capability unavailable maps to model not installed",
			result:     capabilityResult("CAPABILITY_UNAVAILABLE", false, "open_model_setup"),
			wantCode:   domain.ErrorCodeModelNotInstalled,
			wantKey:    "task.worker_error",
			wantAction: []domain.ID{"open_model_setup"},
		},
		{
			name:       "invalid adapter params map to invalid request",
			result:     capabilityResult("CAPABILITY_INVALID_PARAMS", false),
			wantCode:   domain.ErrorCodeInvalidRequest,
			wantKey:    "task.worker_error",
			wantAction: []domain.ID{"retry_task"},
		},
		{
			name:       "adapter failure stays worker exited",
			result:     capabilityResult("CAPABILITY_FAILED", true),
			wantCode:   domain.ErrorCodeWorkerExited,
			wantKey:    "task.worker_error",
			retryable:  true,
			wantAction: []domain.ID{"retry_task"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := errorInfoFor(tc.result)
			if info == nil {
				t.Fatal("errorInfoFor returned nil")
			}
			if info.Code != tc.wantCode {
				t.Fatalf("code = %s, want %s", info.Code, tc.wantCode)
			}
			if info.MessageKey != tc.wantKey {
				t.Fatalf("message key = %q, want %q", info.MessageKey, tc.wantKey)
			}
			if info.Retryable != tc.retryable {
				t.Fatalf("retryable = %v, want %v", info.Retryable, tc.retryable)
			}
			if !slices.Equal(info.ActionIDs, tc.wantAction) {
				t.Fatalf("actions = %v, want %v", info.ActionIDs, tc.wantAction)
			}
		})
	}
}

func capabilityResult(code string, retryable bool, actions ...string) execadapter.Result {
	return execadapter.Result{Error: &execadapter.ExecError{
		Code:      code,
		Message:   "adapter error",
		Retryable: retryable,
		ActionIDs: actions,
	}}
}
