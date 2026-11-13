package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

func TestToolSeparateRun(t *testing.T) {
	exec := &pipelineExecutor{t: t}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())

	project := e.seedProject()
	asset := e.seedMediaAsset(project)
	resp, err := e.svc.CreateWorkflowRun(context.Background(), service.Idem{}, service.CreateWorkflowRunInput{
		ProjectID:        string(project),
		Name:             "音频分离工具",
		Template:         "tool_separate",
		SourceLanguage:   "und",
		TargetLanguages:  []string{},
		InputRevisionIDs: []string{string(asset.ID)},
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := resp.Body.(domain.WorkflowRun)
	if len(run.Nodes) != 2 {
		t.Fatalf("created nodes = %+v, want 01/03", run.Nodes)
	}
	if run.SourceLanguage != "und" {
		t.Fatalf("source_language = %s, want und", run.SourceLanguage)
	}
	if len(run.Languages) != 0 {
		t.Fatalf("languages = %v, want empty", run.Languages)
	}

	final := e.waitRunStatus(run.ID, domain.RunStatusSucceeded, 20*time.Second)
	seen := map[domain.PipelineNodeKey]domain.NodeStatus{}
	for _, node := range final.Nodes {
		seen[node.Key] = node.Status
	}
	for _, key := range []domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeySeparate} {
		if seen[key] != domain.NodeStatusDone {
			t.Fatalf("node %s status = %s, want done", key, seen[key])
		}
	}
	if len(seen) != 2 {
		t.Fatalf("final nodes = %+v, want only 01/03", final.Nodes)
	}

	tasks, err := e.st.ListRunTasks(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want 03 only: %+v", len(tasks), tasks)
	}
	task := tasks[0]
	if task.NodeKey != domain.NodeKeySeparate || task.Status != domain.TaskStatusSucceeded {
		t.Fatalf("task = %s/%s, want 03 succeeded", task.NodeKey, task.Status)
	}
	if len(task.InputRevisionIDs) != 1 || task.InputRevisionIDs[0] != asset.ID {
		t.Fatalf("task inputs = %v, want source asset %s", task.InputRevisionIDs, asset.ID)
	}
	if len(task.ArtifactIDs) != 2 {
		t.Fatalf("artifacts = %v, want two audio tracks", task.ArtifactIDs)
	}
	gates, err := e.st.ListProjectReviews(context.Background(), project, string(run.ID))
	if err != nil {
		t.Fatalf("ListProjectReviews: %v", err)
	}
	if len(gates) != 0 {
		t.Fatalf("tool_separate created review gates: %+v", gates)
	}

	assets, err := e.st.ListProjectAssets(context.Background(), project)
	if err != nil {
		t.Fatalf("ListProjectAssets: %v", err)
	}
	byID := make(map[domain.ID]domain.Asset, len(assets))
	for _, item := range assets {
		byID[item.ID] = item
	}
	names := map[string]bool{}
	for _, id := range task.ArtifactIDs {
		artifact, ok := byID[id]
		if !ok {
			t.Fatalf("artifact %s missing from project assets", id)
		}
		if artifact.Type != domain.AssetTypeAudioTrack {
			t.Fatalf("artifact %s type = %s, want audio_track", id, artifact.Type)
		}
		names[artifact.Name] = true
	}
	for _, name := range []string{"voice.wav", "ambience.wav"} {
		if !names[name] {
			t.Fatalf("missing separation output %q: %v", name, names)
		}
	}

	if len(exec.requests) != 1 {
		t.Fatalf("executor requests = %d, want 1: %+v", len(exec.requests), exec.requests)
	}
	separateReq := exec.requests[0]
	if separateReq.Adapter.ID != separateAdapterID {
		t.Fatalf("adapter = %s, want separate", separateReq.Adapter.ID)
	}
	for _, leaked := range []string{"_core", "device_id", "voice_source_asset_id"} {
		if _, exists := separateReq.Params[leaked]; exists {
			t.Fatalf("runner params leaked core field %q: %+v", leaked, separateReq.Params)
		}
	}
	if _, exists := separateReq.Params["input"]; !exists {
		t.Fatalf("runner params missing input projection: %+v", separateReq.Params)
	}
}
