package service

import (
	"context"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// TestToolExtractAnalyzeSkippedWithoutTask 守护 02 的空模板语义：
// 没有历史 02 Task 时返回 SKIPPED；修复前已创建的 02 Task 仍按真实状态收敛，
// 不能被改写成 skipped（failed/running → skipped 不在状态机允许的路径内）。
func TestToolExtractAnalyzeSkippedWithoutTask(t *testing.T) {
	svc := &Service{}
	tpl := toolExtractTemplate()
	run := domain.WorkflowRun{ID: "run_test", Template: templateToolExtract}
	snap := &pipelineSnapshot{run: &run}

	status, err := svc.computeNodeStatus(context.Background(), nil, snap, tpl, domain.NodeKeyAnalyze)
	if err != nil {
		t.Fatalf("computeNodeStatus: %v", err)
	}
	if status != domain.NodeStatusSkipped {
		t.Fatalf("analyze status = %s, want skipped without task", status)
	}

	snap.tasks = []domain.Task{{
		ID:      "task_test",
		Kind:    domain.TaskKindNode,
		NodeKey: domain.NodeKeyAnalyze,
		Status:  domain.TaskStatusFailed,
	}}
	status, err = svc.computeNodeStatus(context.Background(), nil, snap, tpl, domain.NodeKeyAnalyze)
	if err != nil {
		t.Fatalf("computeNodeStatus: %v", err)
	}
	if status != domain.NodeStatusFailed {
		t.Fatalf("analyze status with legacy failed task = %s, want failed", status)
	}
}
