package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

func TestToolEraseRun(t *testing.T) {
	exec := &pipelineExecutor{t: t}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())

	project := e.seedProject()
	asset := e.seedMediaAsset(project)
	resp, err := e.svc.CreateWorkflowRun(context.Background(), service.Idem{}, service.CreateWorkflowRunInput{
		ProjectID:        string(project),
		Name:             "字幕擦除工具",
		Template:         "tool_erase",
		SourceLanguage:   "chinese",
		TargetLanguages:  []string{},
		InputRevisionIDs: []string{string(asset.ID)},
		Extract:          &domain.ExtractOptions{Mode: domain.ExtractModeOCR, OCRLanguage: domain.OCRLanguageChinese},
		EraseMode:        domain.EraseModeDialogOnly,
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := resp.Body.(domain.WorkflowRun)
	if len(run.Nodes) != 3 {
		t.Fatalf("created nodes = %+v, want 01/04/05", run.Nodes)
	}
	if len(run.Languages) != 0 {
		t.Fatalf("languages = %v, want empty", run.Languages)
	}

	final := e.waitRunStatus(run.ID, domain.RunStatusSucceeded, 20*time.Second)
	seen := map[domain.PipelineNodeKey]domain.NodeStatus{}
	for _, node := range final.Nodes {
		seen[node.Key] = node.Status
	}
	for _, key := range []domain.PipelineNodeKey{
		domain.NodeKeyImport, domain.NodeKeyExtract, domain.NodeKeyErase,
	} {
		if seen[key] != domain.NodeStatusDone {
			t.Fatalf("node %s status = %s, want done", key, seen[key])
		}
	}
	if len(seen) != 3 {
		t.Fatalf("final nodes = %+v, want only 01/04/05", final.Nodes)
	}

	tasks, err := e.st.ListRunTasks(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want 04/05 only: %+v", len(tasks), tasks)
	}
	var extractTask, eraseTask *domain.Task
	for i := range tasks {
		task := &tasks[i]
		if task.Status != domain.TaskStatusSucceeded {
			t.Fatalf("task %s status = %s", task.ID, task.Status)
		}
		switch task.NodeKey {
		case domain.NodeKeyExtract:
			extractTask = task
		case domain.NodeKeyErase:
			eraseTask = task
		default:
			t.Fatalf("unexpected task node %s", task.NodeKey)
		}
	}
	if extractTask == nil || eraseTask == nil {
		t.Fatalf("missing 04/05 tasks: %+v", tasks)
	}
	if extractTask.Params["language"] != "chinese" {
		t.Fatalf("04 params language = %v", extractTask.Params["language"])
	}
	extractParams, ok := extractTask.Params["extract"].(map[string]any)
	if !ok || extractParams["mode"] != "ocr" || extractParams["ocr_language"] != "chinese" {
		t.Fatalf("04 extract params = %#v", extractTask.Params["extract"])
	}
	if eraseTask.Params["erase_mode"] != "dialog_only" {
		t.Fatalf("05 params erase_mode = %v", eraseTask.Params["erase_mode"])
	}
	if len(eraseTask.InputRevisionIDs) != 2 ||
		eraseTask.InputRevisionIDs[0] != asset.ID ||
		eraseTask.InputRevisionIDs[1] != extractTask.ArtifactIDs[0] {
		t.Fatalf("05 inputs = %v, want source + 04 artifact", eraseTask.InputRevisionIDs)
	}
	if len(eraseTask.ArtifactIDs) != 1 {
		t.Fatalf("05 artifacts = %v, want one clean video", eraseTask.ArtifactIDs)
	}

	gates, err := e.st.ListProjectReviews(context.Background(), project, string(run.ID))
	if err != nil {
		t.Fatalf("ListProjectReviews: %v", err)
	}
	if len(gates) != 0 {
		t.Fatalf("tool_erase created review gates: %+v", gates)
	}

	assets, err := e.st.ListProjectAssets(context.Background(), project)
	if err != nil {
		t.Fatalf("ListProjectAssets: %v", err)
	}
	byID := make(map[domain.ID]domain.Asset, len(assets))
	for _, item := range assets {
		byID[item.ID] = item
	}
	clean, ok := byID[eraseTask.ArtifactIDs[0]]
	if !ok {
		t.Fatalf("05 artifact %s missing from project assets", eraseTask.ArtifactIDs[0])
	}
	if clean.Type != domain.AssetTypeVideo || clean.Name != "clean.mp4" {
		t.Fatalf("05 artifact = %s/%s, want video/clean.mp4", clean.Type, clean.Name)
	}
}
