package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// pipelineExecutor 为八节点流水线写入真实但最小的产物，用于验证 Core 的
// 编排、提交、回写与审核推进；不启动任何模型，也不构造 FlashCLI 参数。
type pipelineExecutor struct {
	t *testing.T

	mu            sync.Mutex
	calls         []string
	requests      []execadapter.Request
	failFirst     map[string]int
	omit          map[string]bool
	badPath       map[string]bool
	languageCalls map[string]int
	failLanguage  map[string]int
}

func (p *pipelineExecutor) Execute(
	ctx context.Context,
	req execadapter.Request,
	progress execadapter.ProgressFunc,
	_ execadapter.LogFunc,
) (result execadapter.Result) {
	defer func() {
		if result.Binding == (execadapter.Binding{}) {
			result.Binding = execadapter.Binding{
				Workspace: req.Workspace, ServerEpoch: req.ServerEpoch, TaskID: req.TaskID,
				StepID: req.StepID, Attempt: req.Attempt,
			}
		}
	}()
	p.mu.Lock()
	p.calls = append(p.calls, req.StepID)
	p.requests = append(p.requests, req)
	callNo := 0
	for _, value := range p.calls {
		if value == req.StepID {
			callNo++
		}
	}
	fail := p.failFirst[req.StepID] >= callNo
	omit := p.omit[req.StepID]
	badPath := p.badPath[req.StepID]
	if language, ok := req.Params["language"].(string); ok {
		if p.languageCalls == nil {
			p.languageCalls = map[string]int{}
		}
		p.languageCalls[language]++
		if p.failLanguage[language] >= p.languageCalls[language] {
			fail = true
		}
	}
	p.mu.Unlock()
	if fail {
		return pipelineFailure("PIPELINE_SIMULATED_FAILURE", "simulated first attempt failure")
	}
	if omit {
		return execadapter.Result{Binding: execadapter.Binding{
			Workspace: req.Workspace, ServerEpoch: req.ServerEpoch, TaskID: req.TaskID,
			StepID: req.StepID, Attempt: req.Attempt,
		}}
	}

	if req.OutputDir == "" {
		return pipelineFailure("PIPELINE_OUTPUT_DIR_MISSING", "调度层未分配输出目录")
	}
	if err := os.MkdirAll(req.OutputDir, 0o755); err != nil {
		return pipelineFailure("PIPELINE_OUTPUT_DIR_UNAVAILABLE", err.Error())
	}
	outputs := make([]execadapter.Output, 0)
	write := func(name, content string) bool {
		path := filepath.Join(req.OutputDir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return false
		}
		outputs = append(outputs, execadapter.Output{RelPath: name, SizeBytes: int64(len(content))})
		return true
	}

	switch req.StepID {
	case "02_analyze":
		if !write("analysis.json", `{"language":"en"}`) {
			return pipelineFailure("PIPELINE_WRITE_FAILED", "analysis")
		}
	case "03_separate":
		if !write("voice.wav", "voice") || !write("ambience.wav", "ambience") {
			return pipelineFailure("PIPELINE_WRITE_FAILED", "separate")
		}
		outputs[0].Media = map[string]any{"role": "vocals", "audio_tracks": 1}
		outputs[1].Media = map[string]any{"role": "background", "audio_tracks": 1}
	case "04_extract":
		if !write("source.srt", pipelineSRT("Hello", "World", "Again")) {
			return pipelineFailure("PIPELINE_WRITE_FAILED", "extract")
		}
	case "05_erase":
		if !write("clean.mp4", "clean-video") {
			return pipelineFailure("PIPELINE_WRITE_FAILED", "erase")
		}
	case "06_translate":
		if !write("translated.srt", pipelineSRT("你好", "世界", "再来")) {
			return pipelineFailure("PIPELINE_WRITE_FAILED", "translate")
		}
	case "segment":
		if !write("candidate.wav", "candidate-audio") {
			return pipelineFailure("PIPELINE_WRITE_FAILED", "candidate")
		}
	case "07_clone":
		if !write("full.wav", "full-dub") {
			return pipelineFailure("PIPELINE_WRITE_FAILED", "clone-assembly")
		}
	case "08_mix":
		if !write("preview.mp4", "preview-video") {
			return pipelineFailure("PIPELINE_WRITE_FAILED", "mix")
		}
	default:
		if !write("artifact.txt", "artifact") {
			return pipelineFailure("PIPELINE_WRITE_FAILED", "artifact")
		}
	}
	if badPath {
		outputs = append(outputs, execadapter.Output{RelPath: "../escape.txt", SizeBytes: 1})
	}
	progress("simulate", 1, 1)
	return execadapter.Result{
		Outputs: outputs,
		Binding: execadapter.Binding{
			Workspace: req.Workspace, ServerEpoch: req.ServerEpoch, TaskID: req.TaskID,
			StepID: req.StepID, Attempt: req.Attempt,
		},
	}
}

func (p *pipelineExecutor) callsSnapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.calls...)
}

func pipelineFailure(code, message string) execadapter.Result {
	return execadapter.Result{Error: &execadapter.ExecError{Code: code, Message: message}}
}

func pipelineSRT(texts ...string) string {
	var out string
	for i, text := range texts {
		start := i * 1000
		end := start + 900
		out += fmt.Sprintf("%d\n%s --> %s\n%s\n\n", i+1,
			pipelineTimestamp(start), pipelineTimestamp(end), text)
	}
	return out
}

func pipelineTimestamp(ms int) string {
	return fmt.Sprintf("00:00:%02d,%03d", ms/1000, ms%1000)
}

func TestPipelineArtifactCommitFlow(t *testing.T) {
	exec := &pipelineExecutor{t: t}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())

	project := e.seedProject()
	asset := e.seedMediaAsset(project)
	run := e.seedRunWithInputs(project, asset.ID)

	// 02→05 自动推进，04 的 SRT 回写原文轨，06 的 SRT 回写译文轨并进入审核。
	e.waitRunStatus(run.ID, domain.RunStatusWaitingReview, 20*time.Second)
	translation := waitForGate(t, e, run.ID, domain.ReviewTypeTranslation)
	approveGate(t, e, translation)

	// 06 放行后只为 zh 创建 07 句级任务，全部成功后自动创建 08 并进入交付审核。
	delivery := waitForGate(t, e, run.ID, domain.ReviewTypeDelivery)
	approveGate(t, e, delivery)
	final := e.waitRunStatus(run.ID, domain.RunStatusSucceeded, 20*time.Second)
	for _, node := range final.Nodes {
		if node.Status != domain.NodeStatusDone {
			t.Fatalf("node %s status = %s, want done", node.Key, node.Status)
		}
	}

	tasks, err := e.st.ListRunTasks(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	counts := map[domain.PipelineNodeKey]int{}
	for _, task := range tasks {
		if task.Status != domain.TaskStatusSucceeded {
			t.Fatalf("task %s (%s/%s) status = %s", task.ID, task.NodeKey, task.Language, task.Status)
		}
		counts[task.NodeKey]++
	}
	for _, key := range []domain.PipelineNodeKey{
		domain.NodeKeyAnalyze, domain.NodeKeySeparate, domain.NodeKeyExtract,
		domain.NodeKeyErase, domain.NodeKeyTranslate, domain.NodeKeyMix,
	} {
		if counts[key] != 1 {
			t.Errorf("node %s tasks = %d, want 1", key, counts[key])
		}
	}
	if counts[domain.NodeKeyClone] != 3 {
		t.Errorf("clone tasks = %d, want 3 (one per segment)", counts[domain.NodeKeyClone])
	}

	// Task.language 现在对外可见：06/07/08 为目标语言，02/03/05 为空。
	var mixTask *domain.Task
	for i := range tasks {
		task := &tasks[i]
		switch task.NodeKey {
		case domain.NodeKeyTranslate, domain.NodeKeyClone, domain.NodeKeyMix:
			if task.Language != "zh" {
				t.Errorf("task %s (%s) language = %q, want zh", task.ID, task.NodeKey, task.Language)
			}
		case domain.NodeKeyAnalyze, domain.NodeKeySeparate, domain.NodeKeyErase:
			if task.Language != "" {
				t.Errorf("task %s (%s) language = %q, want empty", task.ID, task.NodeKey, task.Language)
			}
		}
		if task.NodeKey == domain.NodeKeyMix {
			mixTask = task
		}
	}
	if mixTask == nil {
		t.Fatal("no 08_mix task created")
	}

	// 08 预览规则：artifact_ids 中第一个 video/audio 资产即预览 Asset，且内容可读。
	var preview *domain.Asset
	for _, assetID := range mixTask.ArtifactIDs {
		asset, err := e.st.GetAsset(context.Background(), assetID)
		if err != nil {
			t.Fatalf("08 artifact %s: %v", assetID, err)
		}
		if asset.Type == domain.AssetTypeVideo || asset.Type == domain.AssetTypeAudio {
			candidate := asset
			preview = &candidate
			break
		}
	}
	if preview == nil {
		t.Fatalf("08 task artifact_ids = %v, want a video/audio preview", mixTask.ArtifactIDs)
	}
	if preview.Type != domain.AssetTypeVideo {
		t.Fatalf("08 preview type = %s, want video", preview.Type)
	}
	if preview.Path == "" {
		t.Fatal("08 preview asset has no registered location")
	}
	if _, err := os.Stat(preview.Path); err != nil {
		t.Fatalf("08 preview content missing: %v", err)
	}

	tracks, err := e.st.ListProjectTracks(context.Background(), project)
	if err != nil {
		t.Fatalf("ListProjectTracks: %v", err)
	}
	transcription := findTrack(tracks, "en", domain.TrackTypeTranscription)
	translationTrack := findTrack(tracks, "zh", domain.TrackTypeTranslation)
	if transcription == nil || len(transcription.Revisions) != 1 || len(transcription.Revisions[0].Segments) != 3 {
		t.Fatalf("transcription track = %+v", transcription)
	}
	if translationTrack == nil || len(translationTrack.Revisions) != 1 || len(translationTrack.Revisions[0].Segments) != 3 {
		t.Fatalf("translation track = %+v", translationTrack)
	}

	for _, segment := range translationTrack.Revisions[0].Segments {
		candidates, err := e.svc.ListCandidates(context.Background(), segment.ID, "zh")
		if err != nil {
			t.Fatalf("ListCandidates %s: %v", segment.ID, err)
		}
		if len(candidates) != 1 {
			t.Fatalf("candidates for %s = %d, want 1", segment.ID, len(candidates))
		}
		if !candidates[0].IsRecommended {
			t.Errorf("candidate for %s is not recommended", segment.ID)
		}
		selection, err := e.svc.GetCandidateSelection(context.Background(), segment.ID, "zh")
		if err != nil {
			t.Fatalf("GetCandidateSelection %s: %v", segment.ID, err)
		}
		if selection.CandidateID != candidates[0].ID {
			t.Errorf("selection for %s = %s, want %s", segment.ID, selection.CandidateID, candidates[0].ID)
		}
	}

	for _, task := range tasks {
		attempts, err := e.st.ListAttempts(context.Background(), task.ID)
		if err != nil {
			t.Fatalf("ListAttempts %s: %v", task.ID, err)
		}
		if len(attempts) == 0 {
			t.Fatalf("task %s has no attempts", task.ID)
		}
		commits, err := e.st.ListArtifactCommits(context.Background(), attempts[len(attempts)-1].ID)
		if err != nil {
			t.Fatalf("ListArtifactCommits %s: %v", task.ID, err)
		}
		if len(commits) != 1 || len(commits[0].AssetIDs) == 0 {
			t.Fatalf("task %s artifact commit = %+v", task.ID, commits)
		}
		for _, assetID := range commits[0].AssetIDs {
			if _, err := e.st.GetAsset(context.Background(), assetID); err != nil {
				t.Fatalf("task %s committed asset %s: %v", task.ID, assetID, err)
			}
		}
	}
}

func waitForGate(t *testing.T, e *env, runID domain.ID, typ domain.ReviewType) domain.ReviewGate {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		gates, err := e.st.ListRunReviews(context.Background(), runID)
		if err != nil {
			t.Fatalf("ListRunReviews: %v", err)
		}
		for _, gate := range gates {
			if gate.RunID == runID && gate.Type == typ {
				return gate
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("gate %s for run %s not found", typ, runID)
	return domain.ReviewGate{}
}

func approveGate(t *testing.T, e *env, gate domain.ReviewGate) {
	t.Helper()
	if _, err := e.svc.ApproveReview(context.Background(), service.Idem{}, gate.ID, service.ApproveReviewRequest{
		RevisionDigest: gate.RevisionDigest,
		Type:           gate.Type,
	}); err != nil {
		t.Fatalf("ApproveReview %s: %v", gate.ID, err)
	}
}

func findTrack(tracks []domain.SubtitleTrack, language string, typ domain.TrackType) *domain.SubtitleTrack {
	for i := range tracks {
		if tracks[i].Language == language && tracks[i].Type == typ {
			return &tracks[i]
		}
	}
	return nil
}

func seedRunWithLanguages(
	t *testing.T, e *env, projectID, assetID domain.ID, languages []string,
) domain.WorkflowRun {
	t.Helper()
	resp, err := e.svc.CreateWorkflowRun(context.Background(), service.Idem{}, service.CreateWorkflowRunInput{
		ProjectID:        string(projectID),
		Name:             "流水线多语言",
		SourceLanguage:   "en",
		TargetLanguages:  languages,
		InputRevisionIDs: []string{string(assetID)},
		Extract:          &domain.ExtractOptions{Mode: domain.ExtractModeASR},
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	return resp.Body.(domain.WorkflowRun)
}

func waitForNodeTask(
	t *testing.T, e *env, runID domain.ID, node domain.PipelineNodeKey,
	status domain.TaskStatus, timeout time.Duration,
) domain.Task {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last domain.Task
	for time.Now().Before(deadline) {
		tasks, err := e.st.ListRunTasks(context.Background(), runID)
		if err != nil {
			t.Fatalf("ListRunTasks: %v", err)
		}
		for _, task := range tasks {
			if task.NodeKey != node {
				continue
			}
			last = task
			if task.Status == status {
				return task
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("node %s task status = %s, want %s", node, last.Status, status)
	return last
}

func hasNodeTask(t *testing.T, e *env, runID domain.ID, node domain.PipelineNodeKey, language string) bool {
	t.Helper()
	tasks, err := e.st.ListRunTasks(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	for _, task := range tasks {
		if task.NodeKey == node && (language == "" || task.Language == language) {
			return true
		}
	}
	return false
}

func waitForGateLanguage(
	t *testing.T, e *env, runID domain.ID, typ domain.ReviewType, language string,
) domain.ReviewGate {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		gates, err := e.st.ListRunReviews(context.Background(), runID)
		if err != nil {
			t.Fatalf("ListRunReviews: %v", err)
		}
		for _, gate := range gates {
			if gate.RunID == runID && gate.Type == typ && gate.Language == language {
				return gate
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("gate %s/%s for run %s not found", typ, language, runID)
	return domain.ReviewGate{}
}

// pipelineCapabilities 是可在测试中切换状态的固定能力目录。
type pipelineCapabilities struct {
	mu     sync.Mutex
	states map[domain.CapabilitySlotKey]domain.SlotState
}

func (p *pipelineCapabilities) Catalog(ctx context.Context, q service.CatalogQuerier) ([]domain.Capability, error) {
	p.mu.Lock()
	states := make(map[domain.CapabilitySlotKey]domain.SlotState, len(p.states))
	for key, state := range p.states {
		states[key] = state
	}
	p.mu.Unlock()
	return service.StaticCapabilities{States: states}.Catalog(ctx, q)
}

func (p *pipelineCapabilities) ExecutionFixture(slot domain.CapabilitySlotKey) (domain.ExecutionConfig, bool) {
	cfg, ok := executionFixtures()[slot]
	return cfg, ok
}

func (p *pipelineCapabilities) set(slot domain.CapabilitySlotKey, state domain.SlotState) {
	p.mu.Lock()
	p.states[slot] = state
	p.mu.Unlock()
}

func TestPipelineMissingCapabilityWaits(t *testing.T) {
	exec := &pipelineExecutor{t: t}
	caps := &pipelineCapabilities{states: map[domain.CapabilitySlotKey]domain.SlotState{}}
	e := newEnv(t, envOptions{executor: exec, pipeline: true, capabilities: caps})
	project := e.seedProject()
	asset := e.seedMediaAsset(project)
	run := e.seedRunWithInputs(project, asset.ID)
	// Run 创建时 ASR 仍 READY；派发前切换为未安装，任务应等待而不是成功。
	caps.set(domain.CapabilitySlotASR, domain.SlotStateNeedsSetup)
	startScheduler(t, e.sched, context.Background())

	task := waitForNodeTask(t, e, run.ID, domain.NodeKeyAnalyze, domain.TaskStatusWaitingResource, 5*time.Second)
	if task.Status != domain.TaskStatusWaitingResource {
		t.Fatalf("analyze task = %s", task.Status)
	}
	if hasNodeTask(t, e, run.ID, domain.NodeKeySeparate, "") {
		t.Fatal("downstream task created while capability is not ready")
	}
	current, err := e.st.GetWorkflowRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	for _, node := range current.Nodes {
		if node.Key == domain.NodeKeyAnalyze && node.Status == domain.NodeStatusDone {
			t.Fatal("analyze node marked done without a ready model")
		}
	}
}

func TestPipelineZeroOutputsFails(t *testing.T) {
	exec := &pipelineExecutor{t: t, omit: map[string]bool{"02_analyze": true}}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())
	project := e.seedProject()
	asset := e.seedMediaAsset(project)
	run := e.seedRunWithInputs(project, asset.ID)

	task := waitForNodeTask(t, e, run.ID, domain.NodeKeyAnalyze, domain.TaskStatusFailed, 5*time.Second)
	if task.Error == nil || task.Error.Code != domain.ErrorCodeWorkerExited {
		t.Fatalf("analyze task error = %+v, want WORKER_EXITED", task.Error)
	}
	if hasNodeTask(t, e, run.ID, domain.NodeKeySeparate, "") {
		t.Fatal("downstream task created after zero-output failure")
	}
	current, err := e.st.GetWorkflowRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	for _, node := range current.Nodes {
		if node.Key == domain.NodeKeyAnalyze && node.Status != domain.NodeStatusFailed {
			t.Fatalf("analyze node status = %s, want failed", node.Status)
		}
	}
}

func TestPipelinePathEscapeFails(t *testing.T) {
	exec := &pipelineExecutor{t: t, badPath: map[string]bool{"02_analyze": true}}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())
	project := e.seedProject()
	asset := e.seedMediaAsset(project)
	run := e.seedRunWithInputs(project, asset.ID)

	task := waitForNodeTask(t, e, run.ID, domain.NodeKeyAnalyze, domain.TaskStatusFailed, 5*time.Second)
	if task.Error == nil || task.Error.Code != domain.ErrorCodeInvalidRequest {
		t.Fatalf("analyze task error = %+v, want INVALID_REQUEST", task.Error)
	}
}

func TestPipelineRetryAfterFailure(t *testing.T) {
	exec := &pipelineExecutor{t: t, failFirst: map[string]int{"02_analyze": 1}}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())
	project := e.seedProject()
	asset := e.seedMediaAsset(project)
	run := e.seedRunWithInputs(project, asset.ID)

	task := waitForNodeTask(t, e, run.ID, domain.NodeKeyAnalyze, domain.TaskStatusFailed, 5*time.Second)
	if _, err := e.svc.RetryTask(context.Background(), service.Idem{}, task.ID); err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	e.waitRunStatus(run.ID, domain.RunStatusWaitingReview, 20*time.Second)
	tasks, err := e.st.ListRunTasks(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	analyzeTasks := 0
	for _, item := range tasks {
		if item.NodeKey == domain.NodeKeyAnalyze {
			analyzeTasks++
			if item.Status != domain.TaskStatusSucceeded {
				t.Fatalf("retried analyze task status = %s", item.Status)
			}
		}
	}
	if analyzeTasks != 1 {
		t.Fatalf("analyze tasks = %d, want 1 after retry", analyzeTasks)
	}
}

// TestPipelineExtractRetryKeepsParams 覆盖 04 内部参数的持久化往返：重试用
// Task 自身字段重组输入摘要，params 经 JSON 落库读回后摘要必须仍一致。
func TestPipelineExtractRetryKeepsParams(t *testing.T) {
	exec := &pipelineExecutor{t: t, failFirst: map[string]int{"04_extract": 1}}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())
	project := e.seedProject()
	asset := e.seedMediaAsset(project)
	run := e.seedRunWithInputs(project, asset.ID)

	task := waitForNodeTask(t, e, run.ID, domain.NodeKeyExtract, domain.TaskStatusFailed, 5*time.Second)
	if _, err := e.svc.RetryTask(context.Background(), service.Idem{}, task.ID); err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	e.waitRunStatus(run.ID, domain.RunStatusWaitingReview, 20*time.Second)
	tasks, err := e.st.ListRunTasks(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	extractTasks := 0
	for _, item := range tasks {
		if item.NodeKey == domain.NodeKeyExtract {
			extractTasks++
			if item.Status != domain.TaskStatusSucceeded {
				t.Fatalf("retried extract task status = %s", item.Status)
			}
			if item.InputDigest != task.InputDigest {
				t.Fatalf("extract input digest changed across retry: %s -> %s", task.InputDigest, item.InputDigest)
			}
			assertExtractParams(t, item, "en", "asr", "")
		}
	}
	if extractTasks != 1 {
		t.Fatalf("extract tasks = %d, want 1 after retry", extractTasks)
	}
}

func TestPipelineSegmentFailureMarksRunPartial(t *testing.T) {
	exec := &pipelineExecutor{t: t, failFirst: map[string]int{"segment": 1}}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())
	project := e.seedProject()
	asset := e.seedMediaAsset(project)
	run := e.seedRunWithInputs(project, asset.ID)

	translation := waitForGate(t, e, run.ID, domain.ReviewTypeTranslation)
	approveGate(t, e, translation)
	final := e.waitRunStatus(run.ID, domain.RunStatusPartial, 20*time.Second)
	for _, node := range final.Nodes {
		switch node.Key {
		case domain.NodeKeyClone, domain.NodeKeyMix:
			if node.Status != domain.NodeStatusFailed {
				t.Fatalf("node %s status = %s, want failed after segment failure", node.Key, node.Status)
			}
		}
	}
	if hasNodeTask(t, e, run.ID, domain.NodeKeyMix, "zh") {
		t.Fatal("mix task created although a required segment failed")
	}
}

func TestPipelineMultiLanguageFailureIsolation(t *testing.T) {
	exec := &pipelineExecutor{t: t, failLanguage: map[string]int{"ja": 1}}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())
	project := e.seedProject()
	asset := e.seedMediaAsset(project)
	run := seedRunWithLanguages(t, e, project, asset.ID, []string{"zh", "ja"})

	// zh 翻译审核可正常放行；ja 的 06 任务失败，不应阻塞 zh 的 07/08。
	zhTranslation := waitForGateLanguage(t, e, run.ID, domain.ReviewTypeTranslation, "zh")
	approveGate(t, e, zhTranslation)
	zhDelivery := waitForGateLanguage(t, e, run.ID, domain.ReviewTypeDelivery, "zh")
	approveGate(t, e, zhDelivery)

	final := e.waitRunStatus(run.ID, domain.RunStatusPartial, 20*time.Second)
	for _, node := range final.Nodes {
		switch node.Key {
		case domain.NodeKeyTranslate, domain.NodeKeyClone, domain.NodeKeyMix:
			if node.Status != domain.NodeStatusFailed {
				t.Fatalf("node %s status = %s, want failed (ja failed)", node.Key, node.Status)
			}
		}
	}
	if hasNodeTask(t, e, run.ID, domain.NodeKeyClone, "ja") {
		t.Fatal("ja clone task created after ja translation failed")
	}
	if !hasNodeTask(t, e, run.ID, domain.NodeKeyClone, "zh") {
		t.Fatal("zh clone tasks missing")
	}
}

func TestPipelineMultiLanguageReviewIsolation(t *testing.T) {
	exec := &pipelineExecutor{t: t}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())
	project := e.seedProject()
	asset := e.seedMediaAsset(project)
	run := seedRunWithLanguages(t, e, project, asset.ID, []string{"zh", "ja"})

	e.waitRunStatus(run.ID, domain.RunStatusWaitingReview, 20*time.Second)
	zhTranslation := waitForGateLanguage(t, e, run.ID, domain.ReviewTypeTranslation, "zh")
	jaTranslation := waitForGateLanguage(t, e, run.ID, domain.ReviewTypeTranslation, "ja")
	approveGate(t, e, zhTranslation)

	zhDelivery := waitForGateLanguage(t, e, run.ID, domain.ReviewTypeDelivery, "zh")
	approveGate(t, e, zhDelivery)
	if hasNodeTask(t, e, run.ID, domain.NodeKeyClone, "ja") {
		t.Fatal("ja clone task created before ja translation review")
	}
	current, err := e.st.GetWorkflowRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	for _, node := range current.Nodes {
		if node.Key == domain.NodeKeyTranslate && node.Status == domain.NodeStatusDone {
			t.Fatal("translate node marked done while ja review is still pending")
		}
	}

	approveGate(t, e, jaTranslation)
	jaDelivery := waitForGateLanguage(t, e, run.ID, domain.ReviewTypeDelivery, "ja")
	approveGate(t, e, jaDelivery)
	final := e.waitRunStatus(run.ID, domain.RunStatusSucceeded, 20*time.Second)
	for _, node := range final.Nodes {
		if node.Status != domain.NodeStatusDone {
			t.Fatalf("node %s status = %s, want done", node.Key, node.Status)
		}
	}
	if !hasNodeTask(t, e, run.ID, domain.NodeKeyClone, "ja") {
		t.Fatal("ja clone tasks were not created after approval")
	}
}

// seedTranscriptionTrack 直接写入原文轨 fixture，用于验证 04 复用路径。
func seedTranscriptionTrack(
	t *testing.T, e *env, projectID domain.ID, language string, segments []domain.Segment,
) domain.SubtitleTrack {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	revisionID := store.NewID("srev")
	track := domain.SubtitleTrack{
		ID:                store.NewID("track"),
		ProjectID:         projectID,
		Language:          language,
		Type:              domain.TrackTypeTranscription,
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

// TestPipelineReusesProvidedTracks 覆盖 R10：复用音频轨时 03 跳过、复用原文轨时
// 04 跳过且 06 使用该 revision；译文轨复用必须返回明确未实现错误。
func TestPipelineReusesProvidedTracks(t *testing.T) {
	exec := &pipelineExecutor{t: t}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())
	project := e.seedProject()
	asset := e.seedMediaAsset(project)

	ctx := context.Background()
	audio := domain.Asset{
		ID:         store.NewID("asset"),
		ProjectID:  project,
		Type:       domain.AssetTypeAudioTrack,
		Name:       "voice.wav",
		SizeBytes:  5,
		Status:     domain.AssetStatusReady,
		ImportedAt: time.Now().UTC(),
	}
	if err := e.st.CreateAsset(ctx, audio); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	source := seedTranscriptionTrack(t, e, project, "en", []domain.Segment{
		{ID: "seg-reuse-1", StartMs: 0, EndMs: 900, Text: "Hello"},
		{ID: "seg-reuse-2", StartMs: 1000, EndMs: 1900, Text: "World"},
	})

	resp, err := e.svc.CreateWorkflowRun(ctx, service.Idem{}, service.CreateWorkflowRunInput{
		ProjectID:            string(project),
		Name:                 "复用轨计划",
		SourceLanguage:       "en",
		TargetLanguages:      []string{"zh"},
		InputRevisionIDs:     []string{string(asset.ID)},
		Extract:              &domain.ExtractOptions{Mode: domain.ExtractModeASR},
		ReuseAudioTrackID:    string(audio.ID),
		ReuseSubtitleTrackID: string(source.ID),
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := resp.Body.(domain.WorkflowRun)
	e.waitRunStatus(run.ID, domain.RunStatusWaitingReview, 20*time.Second)

	current, err := e.st.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	for _, node := range current.Nodes {
		if node.Key != domain.NodeKeySeparate && node.Key != domain.NodeKeyExtract {
			continue
		}
		if node.Status != domain.NodeStatusReused {
			t.Fatalf("node %s status = %s, want reused", node.Key, node.Status)
		}
	}
	tasks, err := e.st.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	var translate domain.Task
	for _, task := range tasks {
		switch task.NodeKey {
		case domain.NodeKeySeparate, domain.NodeKeyExtract:
			t.Fatalf("node %s dispatched a task although the track is reused", task.NodeKey)
		case domain.NodeKeyTranslate:
			translate = task
		}
	}
	if translate.ID == "" {
		t.Fatal("translate task was not created from the reused transcription track")
	}
	if len(translate.InputRevisionIDs) != 1 || translate.InputRevisionIDs[0] != source.CurrentRevisionID {
		t.Fatalf("translate inputs = %v, want %s", translate.InputRevisionIDs, source.CurrentRevisionID)
	}

	// 目标语言译文轨复用不在本任务范围：必须显式拒绝，不能静默跳过 06。
	translated := e.seedTrack(project, "zh", []domain.Segment{
		{ID: "seg-translated-1", StartMs: 0, EndMs: 900, Text: "你好"},
	})
	_, err = e.svc.CreateWorkflowRun(ctx, service.Idem{}, service.CreateWorkflowRunInput{
		ProjectID:            string(project),
		Name:                 "译文轨复用计划",
		SourceLanguage:       "en",
		TargetLanguages:      []string{"zh"},
		InputRevisionIDs:     []string{string(asset.ID)},
		Extract:              &domain.ExtractOptions{Mode: domain.ExtractModeASR},
		ReuseAudioTrackID:    string(audio.ID),
		ReuseSubtitleTrackID: string(translated.ID),
	})
	if !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("reuse translation track error = %v, want INVALID_REQUEST", err)
	}
}
