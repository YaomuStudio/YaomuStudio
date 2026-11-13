package service

import (
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/resultcommit"
)

func TestExportCanFinalize(t *testing.T) {
	task := domain.Task{ID: "task-1", Status: domain.TaskStatusSucceeded}
	attempt := domain.Attempt{
		TaskID: "task-1", StepID: "export", AttemptNo: 2, Status: domain.StepStatusSucceeded,
	}
	bound := resultcommit.ExecutionBinding{
		TaskID: "task-1", StepID: "export", Attempt: 2,
	}

	tests := []struct {
		name       string
		record     domain.ExportRecord
		task       domain.Task
		attempt    domain.Attempt
		hasAttempt bool
		result     resultcommit.ExecutionBinding
		want       bool
	}{
		{
			name:       "failed record with successful retry",
			record:     domain.ExportRecord{Status: domain.TaskStatusFailed},
			task:       task,
			attempt:    attempt,
			hasAttempt: true,
			result:     bound,
			want:       true,
		},
		{
			name:       "failed record with unbound legacy result",
			record:     domain.ExportRecord{Status: domain.TaskStatusFailed},
			task:       task,
			attempt:    attempt,
			hasAttempt: true,
			result:     resultcommit.ExecutionBinding{},
			want:       true,
		},
		{
			name:       "failed record without attempt",
			record:     domain.ExportRecord{Status: domain.TaskStatusFailed},
			task:       task,
			hasAttempt: false,
			result:     bound,
			want:       false,
		},
		{
			name:       "failed record with failed latest attempt",
			record:     domain.ExportRecord{Status: domain.TaskStatusFailed},
			task:       task,
			attempt:    domain.Attempt{StepID: "export", AttemptNo: 2, Status: domain.StepStatusFailed},
			hasAttempt: true,
			result:     bound,
			want:       false,
		},
		{
			name:       "failed record with non-succeeded task",
			record:     domain.ExportRecord{Status: domain.TaskStatusFailed},
			task:       domain.Task{ID: "task-1", Status: domain.TaskStatusQueued},
			attempt:    attempt,
			hasAttempt: true,
			result:     bound,
			want:       false,
		},
		{
			name:       "failed record with stale attempt binding",
			record:     domain.ExportRecord{Status: domain.TaskStatusFailed},
			task:       task,
			attempt:    attempt,
			hasAttempt: true,
			result: resultcommit.ExecutionBinding{
				TaskID: "task-1", StepID: "export", Attempt: 1,
			},
			want: false,
		},
		{
			name:       "failed record with foreign task binding",
			record:     domain.ExportRecord{Status: domain.TaskStatusFailed},
			task:       task,
			attempt:    attempt,
			hasAttempt: true,
			result: resultcommit.ExecutionBinding{
				TaskID: "task-2", StepID: "export", Attempt: 2,
			},
			want: false,
		},
		{
			name:       "failed record with wrong step binding",
			record:     domain.ExportRecord{Status: domain.TaskStatusFailed},
			task:       task,
			attempt:    attempt,
			hasAttempt: true,
			result: resultcommit.ExecutionBinding{
				TaskID: "task-1", StepID: "other", Attempt: 2,
			},
			want: false,
		},
		{
			name:       "succeeded record stays immutable",
			record:     domain.ExportRecord{Status: domain.TaskStatusSucceeded},
			task:       task,
			attempt:    attempt,
			hasAttempt: true,
			result:     bound,
			want:       false,
		},
		{
			name:       "canceled record stays immutable",
			record:     domain.ExportRecord{Status: domain.TaskStatusCanceled},
			task:       task,
			attempt:    attempt,
			hasAttempt: true,
			result:     bound,
			want:       false,
		},
		{
			name:       "queued record keeps existing behavior",
			record:     domain.ExportRecord{Status: domain.TaskStatusQueued},
			task:       task,
			attempt:    attempt,
			hasAttempt: true,
			result:     bound,
			want:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := exportCanFinalize(tc.record, tc.task, tc.attempt, tc.hasAttempt, tc.result)
			if got != tc.want {
				t.Fatalf("exportCanFinalize() = %v, want %v", got, tc.want)
			}
		})
	}
}
