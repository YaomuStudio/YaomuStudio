package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func TestCustomPipelineWithoutClone(t *testing.T) {
	exec := &pipelineExecutor{t: t}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())

	project := e.seedProject()
	asset := e.seedMediaAsset(project)
	run := createCustomRun(t, e, service.CreateWorkflowRunInput{
		ProjectID:        string(project),
		Name:             "专属链路·无配音",
		Template:         "custom_pipeline",
		Features:         []string{"separation", "translate", "mix"},
		SourceLanguage:   "en",
		TargetLanguages:  []string{"zh"},
		InputRevisionIDs: []string{string(asset.ID)},
	})
	if len(run.Nodes) != 5 {
		t.Fatalf("created nodes = %+v, want 01/03/04/06/08", run.Nodes)
	}

	// 06 平台译文需要人工放行。
	e.waitRunStatus(run.ID, domain.RunStatusWaitingReview, 20*time.Second)
	approveGate(t, e, waitForGate(t, e, run.ID, domain.ReviewTypeTranslation))
	// 08 成片仍有交付审核。
	approveGate(t, e, waitForGate(t, e, run.ID, domain.ReviewTypeDelivery))
	final := e.waitRunStatus(run.ID, domain.RunStatusSucceeded, 20*time.Second)

	seen := map[domain.PipelineNodeKey]domain.NodeStatus{}
	for _, node := range final.Nodes {
		seen[node.Key] = node.Status
	}
	for _, key := range []domain.PipelineNodeKey{
		domain.NodeKeyImport, domain.NodeKeySeparate, domain.NodeKeyExtract,
		domain.NodeKeyTranslate, domain.NodeKeyMix,
	} {
		if seen[key] != domain.NodeStatusDone {
			t.Fatalf("node %s status = %s, want done", key, seen[key])
		}
	}
	if _, ok := seen[domain.NodeKeyClone]; ok {
		t.Fatalf("custom without clone created 07: %+v", final.Nodes)
	}

	tasks, err := e.st.ListRunTasks(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	mixTasks := 0
	for _, task := range tasks {
		if task.NodeKey == domain.NodeKeyClone {
			t.Fatalf("custom without clone created 07 task: %+v", task)
		}
		if task.NodeKey == domain.NodeKeyMix && task.Language == "zh" {
			mixTasks++
			if task.Status != domain.TaskStatusSucceeded {
				t.Fatalf("08 task status = %s", task.Status)
			}
		}
	}
	if mixTasks != 1 {
		t.Fatalf("08 tasks = %d, want 1", mixTasks)
	}
	gates, err := e.st.ListProjectReviews(context.Background(), project, string(run.ID))
	if err != nil {
		t.Fatalf("ListProjectReviews: %v", err)
	}
	if len(gates) != 2 {
		t.Fatalf("gates = %+v, want translation+delivery", gates)
	}
}

func TestCustomPipelineImportedTracksAndClone(t *testing.T) {
	exec := &pipelineExecutor{t: t}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())

	project := e.seedProject()
	asset := seedCustomVoiceAsset(t, e, project)
	source := seedCloneTrack(t, e, project, "en", domain.TrackTypeTranscription, []domain.Segment{
		{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"},
	})
	seedCloneTrack(t, e, project, "zh", domain.TrackTypeTranslation, []domain.Segment{
		{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"},
	})

	run := createCustomRun(t, e, service.CreateWorkflowRunInput{
		ProjectID:            string(project),
		Name:                 "专属链路·导入译稿",
		Template:             "custom_pipeline",
		Features:             []string{"clone"},
		SourceLanguage:       "en",
		TargetLanguages:      []string{"zh"},
		InputRevisionIDs:     []string{string(asset.ID)},
		ReuseSubtitleTrackID: string(source.ID),
		AdjustDuration:       boolPtr(true),
		ValidateTimestamps:   boolPtr(true),
	})
	final := e.waitRunStatus(run.ID, domain.RunStatusSucceeded, 20*time.Second)
	seen := map[domain.PipelineNodeKey]domain.NodeStatus{}
	for _, node := range final.Nodes {
		seen[node.Key] = node.Status
	}
	if seen[domain.NodeKeyExtract] != domain.NodeStatusReused {
		t.Fatalf("04 status = %s, want reused", seen[domain.NodeKeyExtract])
	}
	if seen[domain.NodeKeyTranslate] != domain.NodeStatusDone {
		t.Fatalf("06 status = %s, want done from imported track", seen[domain.NodeKeyTranslate])
	}
	if seen[domain.NodeKeyClone] != domain.NodeStatusDone {
		t.Fatalf("07 status = %s, want done", seen[domain.NodeKeyClone])
	}

	tasks, err := e.st.ListRunTasks(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	cloneTasks := 0
	for _, task := range tasks {
		if task.NodeKey == domain.NodeKeyExtract || task.NodeKey == domain.NodeKeyTranslate {
			t.Fatalf("imported tracks should skip 04/06 tasks: %+v", task)
		}
		if task.Kind == domain.TaskKindSegment && task.NodeKey == domain.NodeKeyClone {
			cloneTasks++
			if task.Params["adjust_duration"] != true || task.Params["validate_timestamps"] != true {
				t.Fatalf("07 params = %+v", task.Params)
			}
			if task.Params["voice_source_asset_id"] != asset.ID {
				t.Fatalf("custom 07 did not freeze the run reference source: %+v", task.Params)
			}
		}
	}
	if cloneTasks != 1 {
		t.Fatalf("07 segment tasks = %d, want 1", cloneTasks)
	}
	gates, err := e.st.ListProjectReviews(context.Background(), project, string(run.ID))
	if err != nil {
		t.Fatalf("ListProjectReviews: %v", err)
	}
	if len(gates) != 0 {
		t.Fatalf("imported-only custom run created gates: %+v", gates)
	}
	candidates, err := e.svc.ListCandidates(context.Background(), "tr-1", "zh")
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(candidates) == 0 || candidates[0].AudioAssetID == "" {
		t.Fatalf("candidates = %+v", candidates)
	}
}

func TestCustomPipelineEraseKeepsOCRBoundToImportedSource(t *testing.T) {
	exec := &pipelineExecutor{t: t}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())

	project := e.seedProject()
	asset := e.seedMediaAsset(project)
	source := seedCloneTrack(t, e, project, "en", domain.TrackTypeTranscription, []domain.Segment{
		{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"},
	})
	run := createCustomRun(t, e, service.CreateWorkflowRunInput{
		ProjectID:            string(project),
		Name:                 "专属链路·擦除翻译",
		Template:             "custom_pipeline",
		Features:             []string{"erase", "translate"},
		SourceLanguage:       "en",
		TargetLanguages:      []string{"zh"},
		InputRevisionIDs:     []string{string(asset.ID)},
		ReuseSubtitleTrackID: string(source.ID),
		EraseMode:            domain.EraseModeDialogOnly,
		Extract: &domain.ExtractOptions{
			Mode:        domain.ExtractModeOCR,
			OCRLanguage: domain.OCRLanguageChinese,
		},
	})
	e.waitRunStatus(run.ID, domain.RunStatusWaitingReview, 20*time.Second)
	approveGate(t, e, waitForGate(t, e, run.ID, domain.ReviewTypeTranslation))
	final := e.waitRunStatus(run.ID, domain.RunStatusSucceeded, 20*time.Second)

	seen := map[domain.PipelineNodeKey]domain.NodeStatus{}
	for _, node := range final.Nodes {
		seen[node.Key] = node.Status
	}
	for _, key := range []domain.PipelineNodeKey{
		domain.NodeKeyImport, domain.NodeKeyExtract, domain.NodeKeyErase, domain.NodeKeyTranslate,
	} {
		if seen[key] != domain.NodeStatusDone {
			t.Fatalf("node %s status = %s, want done", key, seen[key])
		}
	}

	tasks, err := e.st.ListRunTasks(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	var extractTask, translateTask domain.Task
	for _, task := range tasks {
		switch task.NodeKey {
		case domain.NodeKeyExtract:
			extractTask = task
		case domain.NodeKeyTranslate:
			translateTask = task
		}
	}
	if extractTask.ID == "" {
		t.Fatalf("erase must still run 04 OCR despite reused source track")
	}
	if translateTask.ID == "" || len(translateTask.InputRevisionIDs) != 1 ||
		translateTask.InputRevisionIDs[0] != source.CurrentRevisionID {
		t.Fatalf("06 task = %+v, want imported source revision %s", translateTask, source.CurrentRevisionID)
	}
}

func TestCustomPipelineRejectsAudioForErase(t *testing.T) {
	exec := &pipelineExecutor{t: t}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	project := e.seedProject()
	audio := domain.Asset{
		ID:         store.NewID("asset"),
		ProjectID:  project,
		Type:       domain.AssetTypeAudio,
		Name:       "voice.wav",
		SizeBytes:  16,
		Status:     domain.AssetStatusReady,
		ImportedAt: time.Now().UTC(),
	}
	if err := e.st.CreateAsset(context.Background(), audio); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	result, err := e.svc.Preflight(context.Background(), service.CreateWorkflowRunInput{
		ProjectID:        string(project),
		Name:             "专属链路·类型错误",
		Template:         "custom_pipeline",
		Features:         []string{"erase"},
		SourceLanguage:   "chinese",
		InputRevisionIDs: []string{string(audio.ID)},
		EraseMode:        domain.EraseModeDialogOnly,
		Extract: &domain.ExtractOptions{
			Mode:        domain.ExtractModeOCR,
			OCRLanguage: domain.OCRLanguageChinese,
		},
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	found := false
	for _, issue := range result.Issues {
		if issue.Code == "INPUT_ASSET_INVALID" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audio + erase issues = %+v", result.Issues)
	}
}

func createCustomRun(t *testing.T, e *env, in service.CreateWorkflowRunInput) domain.WorkflowRun {
	t.Helper()
	resp, err := e.svc.CreateWorkflowRun(context.Background(), service.Idem{}, in)
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	return resp.Body.(domain.WorkflowRun)
}

func TestCustomPipelineSkipsExtractWhenAllTranslationsImported(t *testing.T) {
	exec := &pipelineExecutor{t: t}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())

	project := e.seedProject()
	asset := seedCustomVoiceAsset(t, e, project)
	seedCloneTrack(t, e, project, "zh", domain.TrackTypeTranslation, []domain.Segment{
		{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"},
	})
	run := createCustomRun(t, e, service.CreateWorkflowRunInput{
		ProjectID:        string(project),
		Name:             "专属链路·全导入",
		Template:         "custom_pipeline",
		Features:         []string{"clone"},
		SourceLanguage:   "en",
		TargetLanguages:  []string{"zh"},
		InputRevisionIDs: []string{string(asset.ID)},
	})
	final := e.waitRunStatus(run.ID, domain.RunStatusSucceeded, 20*time.Second)
	seen := map[domain.PipelineNodeKey]domain.NodeStatus{}
	for _, node := range final.Nodes {
		seen[node.Key] = node.Status
	}
	if seen[domain.NodeKeyExtract] != domain.NodeStatusSkipped {
		t.Fatalf("04 status = %s, want skipped; nodes=%+v", seen[domain.NodeKeyExtract], final.Nodes)
	}
	if seen[domain.NodeKeyTranslate] != domain.NodeStatusDone {
		t.Fatalf("06 status = %s, want done", seen[domain.NodeKeyTranslate])
	}
	tasks, err := e.st.ListRunTasks(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	for _, task := range tasks {
		if task.NodeKey == domain.NodeKeyExtract || task.NodeKey == domain.NodeKeyTranslate {
			t.Fatalf("skipped nodes created tasks: %+v", task)
		}
	}
}

func TestCustomPipelineFreezesImportedTranslationPerRun(t *testing.T) {
	exec := &pipelineExecutor{t: t}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})

	project := e.seedProject()
	asset := seedCustomVoiceAsset(t, e, project)
	source := seedCloneTrack(t, e, project, "en", domain.TrackTypeTranscription, []domain.Segment{
		{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"},
	})
	translation := seedCloneTrack(t, e, project, "zh", domain.TrackTypeTranslation, []domain.Segment{
		{ID: "first-1", StartMs: 0, EndMs: 900, Text: "第一版"},
	})
	runA := createCustomRun(t, e, service.CreateWorkflowRunInput{
		ProjectID:            string(project),
		Name:                 "冻结 A",
		Template:             "custom_pipeline",
		Features:             []string{"clone"},
		SourceLanguage:       "en",
		TargetLanguages:      []string{"zh"},
		InputRevisionIDs:     []string{string(asset.ID)},
		ReuseSubtitleTrackID: string(source.ID),
	})
	// run A 创建后在同一译文轨保存新 revision，模拟用户更新译文后新建 run B。
	// 不在两个时间戳完全相同的同语言轨之间依赖“最新轨”排序。
	secondRev, err := e.st.SaveSubtitleRevision(context.Background(), translation.ID, translation.CurrentRevisionID, store.RevisionSave{
		AuthorType: domain.AuthorTypeUser,
		Segments: []domain.SegmentInput{
			{ID: "first-1", StartMs: 0, EndMs: 900, Text: "第二版"},
		},
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	runB := createCustomRun(t, e, service.CreateWorkflowRunInput{
		ProjectID:            string(project),
		Name:                 "冻结 B",
		Template:             "custom_pipeline",
		Features:             []string{"clone"},
		SourceLanguage:       "en",
		TargetLanguages:      []string{"zh"},
		InputRevisionIDs:     []string{string(asset.ID)},
		ReuseSubtitleTrackID: string(source.ID),
	})

	startScheduler(t, e.sched, context.Background())
	e.waitRunStatus(runA.ID, domain.RunStatusSucceeded, 20*time.Second)
	e.waitRunStatus(runB.ID, domain.RunStatusSucceeded, 20*time.Second)

	revisionFor := func(runID domain.ID) domain.ID {
		t.Helper()
		tasks, err := e.st.ListRunTasks(context.Background(), runID)
		if err != nil {
			t.Fatalf("ListRunTasks: %v", err)
		}
		for _, task := range tasks {
			if task.Kind == domain.TaskKindSegment && task.NodeKey == domain.NodeKeyClone {
				if len(task.InputRevisionIDs) != 2 || task.InputRevisionIDs[1] != asset.ID {
					t.Fatalf("07 task inputs = %v", task.InputRevisionIDs)
				}
				return task.InputRevisionIDs[0]
			}
		}
		t.Fatalf("run %s has no 07 segment task", runID)
		return ""
	}
	if got := revisionFor(runA.ID); got != translation.CurrentRevisionID {
		t.Fatalf("run A 07 revision = %s, want frozen %s", got, translation.CurrentRevisionID)
	}
	if got := revisionFor(runB.ID); got != secondRev.ID {
		t.Fatalf("run B 07 revision = %s, want frozen %s", got, secondRev.ID)
	}

	for _, runID := range []domain.ID{runA.ID, runB.ID} {
		tasks, err := e.st.ListRunTasks(context.Background(), runID)
		if err != nil {
			t.Fatalf("ListRunTasks: %v", err)
		}
		for _, task := range tasks {
			if task.NodeKey == domain.NodeKeyTranslate {
				t.Fatalf("imported translation should skip 06: %+v", task)
			}
		}
	}
}

func seedCustomVoiceAsset(t *testing.T, e *env, projectID domain.ID) domain.Asset {
	t.Helper()
	payload := []byte("fixture-reference-audio")
	path := filepath.Join(t.TempDir(), "voice.wav")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	asset := domain.Asset{
		ID: store.NewID("asset"), ProjectID: projectID, Type: domain.AssetTypeAudio,
		Name: "voice.wav", SizeBytes: int64(len(payload)), SHA256: hex.EncodeToString(sum[:]),
		Meta:   &domain.MediaMeta{Container: "wav", DurationMs: 2000, AudioTracks: 1},
		Status: domain.AssetStatusReady, ImportedAt: time.Now().UTC(),
	}
	if err := e.st.CreateAsset(context.Background(), asset); err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddAssetLocation(context.Background(), domain.AssetLocation{
		ID: store.NewID("loc"), AssetID: asset.ID, Path: path, Available: true,
		Source: domain.LocationSourceExternalReference, Owner: domain.LocationOwnerUser, CreatedAt: asset.ImportedAt,
	}); err != nil {
		t.Fatal(err)
	}
	return asset
}
