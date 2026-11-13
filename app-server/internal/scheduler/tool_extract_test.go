package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

func TestToolExtractRun(t *testing.T) {
	final, tasks, project, e := runToolExtract(t, "en", domain.ExtractOptions{Mode: domain.ExtractModeASR})
	assertNodes(t, final, []domain.PipelineNodeKey{
		domain.NodeKeyImport, domain.NodeKeyAnalyze, domain.NodeKeyExtract,
	})
	assertToolExtractTasks(t, tasks)
	assertExtractParams(t, taskForNode(t, tasks, domain.NodeKeyExtract), "en", "asr", "")
	assertTranscription(t, e, project, "en", 3)
}

func TestToolExtractRunOCR(t *testing.T) {
	// 源语言与 OCR 语言取不同值，确保 params.language 来自 source_language，
	// 而不是误从 ocr_language 复制。
	_, tasks, project, e := runToolExtract(t, "ja", domain.ExtractOptions{
		Mode:        domain.ExtractModeOCR,
		OCRLanguage: domain.OCRLanguageChinese,
	})
	assertToolExtractTasks(t, tasks)
	assertExtractParams(t, taskForNode(t, tasks, domain.NodeKeyExtract), "ja", "ocr", "chinese")
	assertTranscription(t, e, project, "ja", 3)
}

func runToolExtract(t *testing.T, sourceLanguage string, extract domain.ExtractOptions) (domain.WorkflowRun, []domain.Task, domain.ID, *env) {
	t.Helper()
	exec := &pipelineExecutor{t: t}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())

	project := e.seedProject()
	asset := e.seedMediaAsset(project)
	resp, err := e.svc.CreateWorkflowRun(context.Background(), service.Idem{}, service.CreateWorkflowRunInput{
		ProjectID:        string(project),
		Name:             "字幕提取工具",
		Template:         "tool_extract",
		SourceLanguage:   sourceLanguage,
		TargetLanguages:  []string{},
		InputRevisionIDs: []string{string(asset.ID)},
		Extract:          &extract,
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := resp.Body.(domain.WorkflowRun)
	if len(run.Nodes) != 3 {
		t.Fatalf("created nodes = %+v, want 01/02/04", run.Nodes)
	}
	if len(run.Languages) != 0 {
		t.Fatalf("languages = %v, want empty", run.Languages)
	}
	final := e.waitRunStatus(run.ID, domain.RunStatusSucceeded, 20*time.Second)
	tasks, err := e.st.ListRunTasks(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	return final, tasks, project, e
}

func assertNodes(t *testing.T, run domain.WorkflowRun, want []domain.PipelineNodeKey) {
	t.Helper()
	seen := map[domain.PipelineNodeKey]domain.NodeStatus{}
	for _, node := range run.Nodes {
		seen[node.Key] = node.Status
	}
	for _, key := range want {
		wantStatus := domain.NodeStatusDone
		if key == domain.NodeKeyAnalyze {
			// 真实分析适配器未交付前，tool_extract 的 02 以 skipped 收敛，
			// 不创建 flashcli 门控任务，也不阻塞 04。
			wantStatus = domain.NodeStatusSkipped
		}
		if seen[key] != wantStatus {
			t.Fatalf("node %s status = %s, want %s", key, seen[key], wantStatus)
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("final nodes = %+v, want only %v", run.Nodes, want)
	}
}

func assertToolExtractTasks(t *testing.T, tasks []domain.Task) {
	t.Helper()
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want 04 only (02 skipped): %+v", len(tasks), tasks)
	}
	task := tasks[0]
	if task.Status != domain.TaskStatusSucceeded {
		t.Fatalf("task %s status = %s", task.ID, task.Status)
	}
	if task.NodeKey != domain.NodeKeyExtract {
		t.Fatalf("unexpected task node %s", task.NodeKey)
	}
}

func taskForNode(t *testing.T, tasks []domain.Task, node domain.PipelineNodeKey) domain.Task {
	t.Helper()
	for _, task := range tasks {
		if task.NodeKey == node {
			return task
		}
	}
	t.Fatalf("task for node %s missing: %+v", node, tasks)
	return domain.Task{}
}

func assertExtractParams(t *testing.T, task domain.Task, language, mode, ocrLanguage string) {
	t.Helper()
	if got := task.Params["language"]; got != language {
		t.Fatalf("04 params language = %v, want %s", got, language)
	}
	raw, ok := task.Params["extract"].(map[string]any)
	if !ok {
		t.Fatalf("04 params extract = %#v, want object", task.Params["extract"])
	}
	if raw["mode"] != mode {
		t.Fatalf("04 params extract.mode = %v, want %s", raw["mode"], mode)
	}
	gotOCR, hasOCR := raw["ocr_language"]
	if ocrLanguage == "" {
		if hasOCR {
			t.Fatalf("04 params has unexpected ocr_language = %v", gotOCR)
		}
		return
	}
	if gotOCR != ocrLanguage {
		t.Fatalf("04 params extract.ocr_language = %v, want %s", gotOCR, ocrLanguage)
	}
}

func assertTranscription(t *testing.T, e *env, project domain.ID, language string, segments int) {
	t.Helper()
	tracks, err := e.st.ListProjectTracks(context.Background(), project)
	if err != nil {
		t.Fatalf("ListProjectTracks: %v", err)
	}
	for i := range tracks {
		if tracks[i].Type == domain.TrackTypeTranscription && tracks[i].Language == language {
			if len(tracks[i].Revisions) == 0 || len(tracks[i].Revisions[0].Segments) != segments {
				t.Fatalf("transcription track = %+v", tracks[i])
			}
			return
		}
	}
	t.Fatalf("transcription track %s missing after tool_extract", language)
}
