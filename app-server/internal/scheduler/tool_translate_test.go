package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func TestToolTranslateRun(t *testing.T) {
	exec := &pipelineExecutor{t: t}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())

	project := e.seedProject()
	sourcePath := filepath.Join(t.TempDir(), "source.srt")
	payload := []byte(pipelineSRT("Hello", "World"))
	if err := os.WriteFile(sourcePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	asset := domain.Asset{
		ID:         store.NewID("asset"),
		ProjectID:  project,
		Type:       domain.AssetTypeSubtitle,
		Name:       "source.srt",
		SizeBytes:  int64(len(payload)),
		SHA256:     hex.EncodeToString(sum[:]),
		Status:     domain.AssetStatusReady,
		ImportedAt: time.Now().UTC(),
	}
	if err := e.st.CreateAsset(context.Background(), asset); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	if err := e.st.AddAssetLocation(context.Background(), domain.AssetLocation{
		ID: store.NewID("loc"), AssetID: asset.ID, Path: sourcePath, Available: true,
		Source: domain.LocationSourceExternalReference, Owner: domain.LocationOwnerUser, CreatedAt: asset.ImportedAt,
	}); err != nil {
		t.Fatal(err)
	}
	source := seedTranscriptionTrack(t, e, project, "en", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "Hello"},
		{ID: "seg-2", StartMs: 1000, EndMs: 1900, Text: "World"},
	})

	resp, err := e.svc.CreateWorkflowRun(context.Background(), service.Idem{}, service.CreateWorkflowRunInput{
		ProjectID:        string(project),
		Name:             "字幕翻译工具",
		Template:         "tool_translate",
		SourceLanguage:   "en",
		TargetLanguages:  []string{"zh"},
		InputRevisionIDs: []string{string(asset.ID)},
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := resp.Body.(domain.WorkflowRun)
	if len(run.Nodes) != 2 {
		t.Fatalf("created nodes = %+v, want 01/06", run.Nodes)
	}

	final := e.waitRunStatus(run.ID, domain.RunStatusSucceeded, 20*time.Second)
	seen := map[domain.PipelineNodeKey]domain.NodeStatus{}
	for _, node := range final.Nodes {
		seen[node.Key] = node.Status
	}
	for _, key := range []domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeyTranslate} {
		if seen[key] != domain.NodeStatusDone {
			t.Fatalf("node %s status = %s, want done", key, seen[key])
		}
	}
	if len(seen) != 2 {
		t.Fatalf("final nodes = %+v, want only 01/06", final.Nodes)
	}

	tasks, err := e.st.ListRunTasks(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].NodeKey != domain.NodeKeyTranslate {
		t.Fatalf("tasks = %+v, want one 06 task", tasks)
	}
	task := tasks[0]
	if task.Status != domain.TaskStatusSucceeded || task.Language != "zh" {
		t.Fatalf("task = %s/%s, want succeeded/zh", task.Status, task.Language)
	}
	if len(task.InputRevisionIDs) != 1 || task.InputRevisionIDs[0] != source.CurrentRevisionID {
		t.Fatalf("task inputs = %v, want source revision %s", task.InputRevisionIDs, source.CurrentRevisionID)
	}
	if task.Params["language"] != "zh" {
		t.Fatalf("task params = %+v, want language zh", task.Params)
	}

	if len(exec.requests) != 1 {
		t.Fatalf("executor requests = %d, want 1: %+v", len(exec.requests), exec.requests)
	}
	translateReq := exec.requests[0]
	if translateReq.Adapter.ID != translateAdapterID || translateReq.Adapter.Digest != translateAdapterDigest {
		t.Fatalf("adapter = %s/%s, want %s/%s", translateReq.Adapter.ID,
			translateReq.Adapter.Digest, translateAdapterID, translateAdapterDigest)
	}
	for _, leaked := range []string{"_core", "device_id", "voice_source_asset_id"} {
		if _, exists := translateReq.Params[leaked]; exists {
			t.Fatalf("runner params leaked core field %q: %+v", leaked, translateReq.Params)
		}
	}
	if _, exists := translateReq.Params["plan"]; exists {
		t.Fatalf("06 runner params unexpectedly use an 08 plan: %+v", translateReq.Params)
	}
	subtitleURL, _ := translateReq.Params["subtitle_url"].(string)
	if subtitleURL == "" || !filepath.IsAbs(subtitleURL) {
		t.Fatalf("subtitle_url = %q, want absolute frozen SRT", subtitleURL)
	}
	frozen, err := os.ReadFile(subtitleURL)
	if err != nil {
		t.Fatalf("read frozen subtitle: %v", err)
	}
	for _, text := range []string{"Hello", "World"} {
		if !strings.Contains(string(frozen), text) {
			t.Fatalf("frozen subtitle missing source cue %q: %q", text, frozen)
		}
	}

	gates, err := e.st.ListProjectReviews(context.Background(), project, string(run.ID))
	if err != nil {
		t.Fatalf("ListProjectReviews: %v", err)
	}
	if len(gates) != 0 {
		t.Fatalf("tool_translate created review gates: %+v", gates)
	}

	tracks, err := e.st.ListProjectTracks(context.Background(), project)
	if err != nil {
		t.Fatalf("ListProjectTracks: %v", err)
	}
	translated := findTrack(tracks, "zh", domain.TrackTypeTranslation)
	if translated == nil {
		t.Fatalf("translation track missing: %+v", tracks)
	}
	if len(translated.Revisions) == 0 || len(translated.Revisions[0].Segments) != 3 {
		t.Fatalf("translation track = %+v", translated)
	}
}
