package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func TestVoiceCloneRun(t *testing.T) {
	exec := &pipelineExecutor{t: t}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())

	project, asset, run := seedVoiceCloneRun(t, e)
	if len(run.Nodes) != 2 {
		t.Fatalf("created nodes = %+v, want 01/07", run.Nodes)
	}

	final := e.waitRunStatus(run.ID, domain.RunStatusSucceeded, 20*time.Second)
	seen := map[domain.PipelineNodeKey]domain.NodeStatus{}
	for _, node := range final.Nodes {
		seen[node.Key] = node.Status
	}
	for _, key := range []domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeyClone} {
		if seen[key] != domain.NodeStatusDone {
			t.Fatalf("node %s status = %s, want done", key, seen[key])
		}
	}
	if len(seen) != 2 {
		t.Fatalf("final nodes = %+v, want only 01/07", final.Nodes)
	}

	tasks, err := e.st.ListRunTasks(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	scriptTasks := 0
	for _, task := range tasks {
		if task.NodeKey != domain.NodeKeyClone {
			continue
		}
		if task.Kind != domain.TaskKindNode {
			t.Fatalf("task = %s/%s, want one clone script task", task.Kind, task.NodeKey)
		}
		if task.Params["op"] != "script" {
			t.Fatalf("clone op = %v, want script", task.Params["op"])
		}
		scriptTasks++
		if task.Status != domain.TaskStatusSucceeded {
			t.Fatalf("task %s status = %s", task.ID, task.Status)
		}
		if task.SegmentID != "" {
			t.Fatalf("script task segment = %s, want empty", task.SegmentID)
		}
		if task.Params["voice_source_asset_id"] != string(asset.ID) {
			t.Fatalf("voice source param = %v", task.Params["voice_source_asset_id"])
		}
		if task.Params["out"] != "out.mp3" {
			t.Fatalf("clone out = %v, want out.mp3", task.Params["out"])
		}
		if task.Params["language"] != "zh" {
			t.Fatalf("clone language = %v", task.Params["language"])
		}
		if len(task.ArtifactIDs) == 0 {
			t.Fatalf("script task produced no artifact")
		}
	}
	if scriptTasks != 1 {
		t.Fatalf("script tasks = %d, want 1", scriptTasks)
	}

	gates, err := e.st.ListProjectReviews(context.Background(), project, string(run.ID))
	if err != nil {
		t.Fatalf("ListProjectReviews: %v", err)
	}
	if len(gates) != 0 {
		t.Fatalf("voice_clone created review gates: %+v", gates)
	}
}

// TestVoiceCloneResume 覆盖暂停后恢复重新预检。
func TestVoiceCloneResume(t *testing.T) {
	exec := &pipelineExecutor{t: t}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	ctx := context.Background()
	// 先不启动调度器，确保 Run 能在任务派发前进入 PAUSED。
	_, _, run := seedVoiceCloneRun(t, e)

	if _, err := e.svc.PauseWorkflowRun(ctx, service.Idem{}, run.ID); err != nil {
		t.Fatalf("PauseWorkflowRun: %v", err)
	}
	if _, err := e.svc.ResumeWorkflowRun(ctx, service.Idem{}, run.ID); err != nil {
		t.Fatalf("ResumeWorkflowRun: %v", err)
	}

	startScheduler(t, e.sched, ctx)
	final := e.waitRunStatus(run.ID, domain.RunStatusSucceeded, 20*time.Second)
	if len(final.Nodes) != 2 {
		t.Fatalf("final nodes = %+v, want 01/07", final.Nodes)
	}
	for _, node := range final.Nodes {
		if node.Status != domain.NodeStatusDone {
			t.Fatalf("node %s status = %s after resume, want done", node.Key, node.Status)
		}
	}
}

// seedVoiceCloneRun 准备声源资产、原文/译文轨并创建 tool_voice_clone Run。
func seedVoiceCloneRun(t *testing.T, e *env) (domain.ID, domain.Asset, domain.WorkflowRun) {
	t.Helper()
	project := e.seedProject()
	path := filepath.Join(e.data, "voice.wav")
	if err := os.WriteFile(path, []byte("voice-source"), 0o600); err != nil {
		t.Fatal(err)
	}
	asset := e.seedExportFileAsset(project, "voice.wav", domain.AssetTypeAudio, path,
		&domain.MediaMeta{Container: "wav", DurationMs: 2000, AudioTracks: 1})
	seedCloneTrack(t, e, project, "en", domain.TrackTypeTranscription, []domain.Segment{
		{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"},
		{ID: "src-2", StartMs: 1000, EndMs: 1900, Text: "World"},
	})
	seedCloneTrack(t, e, project, "zh", domain.TrackTypeTranslation, []domain.Segment{
		{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"},
		{ID: "tr-2", StartMs: 1000, EndMs: 1900, Text: "世界"},
	})

	resp, err := e.svc.CreateWorkflowRun(context.Background(), service.Idem{}, service.CreateWorkflowRunInput{
		ProjectID:          string(project),
		Name:               "声音克隆工具",
		Template:           "tool_voice_clone",
		SourceLanguage:     "en",
		TargetLanguages:    []string{"zh"},
		InputRevisionIDs:   []string{string(asset.ID)},
		ValidateTimestamps: boolPtr(true),
		AdjustDuration:     boolPtr(true),
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	return project, asset, resp.Body.(domain.WorkflowRun)
}

func seedCloneTrack(
	t *testing.T,
	e *env,
	projectID domain.ID,
	language string,
	typ domain.TrackType,
	segments []domain.Segment,
) domain.SubtitleTrack {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	revisionID := store.NewID("srev")
	track := domain.SubtitleTrack{
		ID:                store.NewID("track"),
		ProjectID:         projectID,
		Language:          language,
		Type:              typ,
		CurrentRevisionID: revisionID,
		CreatedAt:         now,
		UpdatedAt:         now,
		Revisions: []domain.SubtitleRevision{{
			ID:         revisionID,
			AuthorType: domain.AuthorTypeSystem,
			Segments:   segments,
			CreatedAt:  now,
		}},
	}
	if err := e.st.CreateSubtitleTrack(ctx, track); err != nil {
		t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	built, err := e.st.GetSubtitleTrack(ctx, track.ID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	return built
}

func boolPtr(value bool) *bool {
	return &value
}
