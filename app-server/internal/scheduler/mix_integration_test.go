package scheduler

// 真实 Runner + 真实 FFmpeg 的 08 混音执行集成测试。
//
// 覆盖：自动流水线 08 任务携带顶层 FFmpeg 计划 → 调度 → Runner → FFmpeg，
// 产出可解码的 H.264/AAC 预览；交付审核 pending；导出回归；候选音频被删除
// 时 08 可读失败且不登记空产物。
//
// 模型节点由 test-only executor 写真实但最小的媒体（SRT/WAV/源视频拷贝），
// 不构造任何 FlashCLI 参数；08 委托真实 Runner。真实 07 TTS、05 擦除画面与
// 字幕视觉质量不在本测试验证范围。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

// mixPipelineExecutor 对模型节点写固定真实媒体；对 ffmpeg 适配器（08/导出）
// 委托真实 Runner。
type mixPipelineExecutor struct {
	real      Executor
	videoPath string
	voicePath string

	beforeMix func(req execadapter.Request)
	mixOnce   sync.Once
}

func (m *mixPipelineExecutor) Execute(
	ctx context.Context,
	req execadapter.Request,
	progress execadapter.ProgressFunc,
	logs execadapter.LogFunc,
) execadapter.Result {
	if req.Adapter.ID == "ffmpeg" && req.Params != nil {
		if op, _ := req.Params["op"].(string); op != "" {
			if m.beforeMix != nil {
				m.mixOnce.Do(func() { m.beforeMix(req) })
			}
			return m.real.Execute(ctx, req, progress, logs)
		}
	}
	if req.OutputDir == "" {
		return pipelineFailure("MIX_TEST_OUTPUT_DIR_MISSING", "调度层未分配输出目录")
	}
	if err := os.MkdirAll(req.OutputDir, 0o755); err != nil {
		return pipelineFailure("MIX_TEST_OUTPUT_DIR_UNAVAILABLE", err.Error())
	}
	var outputs []execadapter.Output
	write := func(name string, data []byte) bool {
		path := filepath.Join(req.OutputDir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return false
		}
		outputs = append(outputs, execadapter.Output{RelPath: name, SizeBytes: int64(len(data))})
		return true
	}
	copyFile := func(name, source, role string) bool {
		data, err := os.ReadFile(source)
		if err != nil {
			return false
		}
		path := filepath.Join(req.OutputDir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return false
		}
		outputs = append(outputs, execadapter.Output{
			RelPath: name, SizeBytes: int64(len(data)),
			Media: map[string]any{"role": role},
		})
		return true
	}
	ok := true
	switch req.StepID {
	case "02_analyze":
		ok = write("analysis.json", []byte(`{"language":"en"}`))
	case "03_separate":
		ok = copyFile("voice.wav", m.voicePath, "vocals") && copyFile("ambience.wav", m.voicePath, "background")
	case "04_extract":
		ok = write("source.srt", []byte(pipelineSRT("Hello", "World")))
	case "05_erase":
		ok = copyFile("clean.mp4", m.videoPath, "")
	case "06_translate":
		ok = write("translated.srt", []byte(pipelineSRT("你好", "世界")))
	case "segment":
		ok = copyFile("candidate.wav", m.voicePath, "")
	default:
		ok = write("artifact.txt", []byte("artifact"))
	}
	if !ok {
		return pipelineFailure("MIX_TEST_WRITE_FAILED", req.StepID)
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

// buildMixVoiceClip 生成覆盖整段视频的 WAV：08 用 -shortest 截到视频长度，
// 只有句音频覆盖时间轴时产物时长才与源视频一致。
func buildMixVoiceClip(t *testing.T, ffmpeg, dir, name string, frequency int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	runMediaCommand(t, "build_mix_voice_clip", ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:sample_rate=48000:duration=2", frequency),
		"-c:a", "pcm_s16le", path)
	return path
}

func startMixIntegration(t *testing.T) (ffmpeg, ffprobe, python, runnerRoot, videoPath, voicePath string) {
	t.Helper()
	if testing.Short() {
		t.Skip("SKIP: -short 模式不运行真实 FFmpeg 集成测试")
	}
	ffmpeg = findExecutable("FFMPEG_BIN", "ffmpeg")
	if ffmpeg == "" {
		t.Skip("FFMPEG_NOT_AVAILABLE: 未找到 ffmpeg；真实 FFmpeg 集成测试跳过（不得视为通过）")
	}
	ffprobe = findExecutable("FFPROBE_BIN", "ffprobe")
	if ffprobe == "" {
		if os.Getenv("FFPROBE_REQUIRED") == "1" {
			t.Fatalf("FFPROBE_REQUIRED: 未找到 ffprobe；无法验证 08 预览的编码/时长")
		}
		t.Logf("FFPROBE_UNAVAILABLE: 08 预览改用容器头 + ffmpeg 全量解码校验（不伪造 ffprobe 输出）")
	}
	python = findExecutable("PYTHON_BIN", "python3")
	if python == "" {
		t.Skip("PYTHON_NOT_AVAILABLE: 未找到 python3；真实 Runner 集成测试跳过（不得视为通过）")
	}
	runnerRoot = filepath.Join(repoRoot(t), "runner")
	if _, err := os.Stat(filepath.Join(runnerRoot, "src", "yaomustudio_runner", "main.py")); err != nil {
		t.Fatalf("runner source missing under %s: %v", runnerRoot, err)
	}
	assetsDir := t.TempDir()
	videoPath = buildSampleVideo(t, ffmpeg, assetsDir)
	voicePath = buildMixVoiceClip(t, ffmpeg, assetsDir, "voice.wav", 660)
	return ffmpeg, ffprobe, python, runnerRoot, videoPath, voicePath
}

func mixTaskOf(t *testing.T, e *env, runID domain.ID) domain.Task {
	t.Helper()
	tasks, err := e.st.ListRunTasks(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	var found domain.Task
	count := 0
	for _, task := range tasks {
		if task.NodeKey != domain.NodeKeyMix {
			continue
		}
		found = task
		count++
	}
	if count != 1 {
		t.Fatalf("08 tasks = %d, want 1: %+v", count, tasks)
	}
	return found
}

// TestMixExecutionIntegration 覆盖 08 自动任务的完整执行闭环。
func TestMixExecutionIntegration(t *testing.T) {
	ffmpeg, ffprobe, python, runnerRoot, videoPath, voicePath := startMixIntegration(t)
	client := startRunnerClient(t, python, runnerRoot, ffmpeg, ffprobe)
	exec := &mixPipelineExecutor{
		real:      &execadapter.Executor{Client: client},
		videoPath: videoPath, voicePath: voicePath,
	}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())

	project := e.seedProject()
	source := e.seedExportFileAsset(project, "source.mp4", domain.AssetTypeVideo, videoPath,
		&domain.MediaMeta{Container: "mp4", Codec: "h264", DurationMs: 2000, AudioTracks: 1})
	run := e.seedRunWithInputs(project, source.ID)

	// 06 译文审核放行后自动跑 07 句级任务，再自动创建并执行 08。
	approveGate(t, e, waitForGate(t, e, run.ID, domain.ReviewTypeTranslation))
	delivery := waitForGate(t, e, run.ID, domain.ReviewTypeDelivery)
	if delivery.Status != domain.ReviewStatusPending {
		t.Fatalf("delivery gate = %+v, want pending", delivery)
	}

	mixTask := mixTaskOf(t, e, run.ID)
	if mixTask.Status != domain.TaskStatusSucceeded {
		t.Fatalf("08 task = %+v", mixTask)
	}
	plan, _ := mixTask.Params["plan"].(map[string]any)
	if _, ok := plan["language"]; ok {
		t.Fatalf("08 plan must not carry language: %+v", plan)
	}
	if plan["op"] != "video_merge" {
		t.Fatalf("08 op = %v, want video_merge", plan["op"])
	}
	if plan["out"] != "preview.mp4" {
		t.Fatalf("08 out = %v", plan["out"])
	}
	inputs, _ := plan["audio_inputs"].([]any)
	if len(inputs) != 3 {
		// full_pipeline 含 03 分离：08 使用声明的 background，而不是源视频原声。
		t.Fatalf("08 audio_inputs = %d, want 2 dubbed + 1 background: %+v", len(inputs), inputs)
	}
	if background, _ := inputs[2].(map[string]any); background["volume"] != 0.3 {
		t.Fatalf("08 background input = %+v", background)
	}
	subtitlePath, _ := plan["subtitle"].(string)
	if subtitlePath != "core://frozen-subtitle" {
		t.Fatalf("08 subtitle = %q, want frozen placeholder", subtitlePath)
	}

	var preview *domain.Asset
	for _, assetID := range mixTask.ArtifactIDs {
		asset, err := e.st.GetAsset(context.Background(), assetID)
		if err != nil {
			t.Fatalf("08 artifact %s: %v", assetID, err)
		}
		if asset.Type == domain.AssetTypeVideo || asset.Type == domain.AssetTypeAudio {
			preview = &asset
			break
		}
	}
	if preview == nil || preview.Type != domain.AssetTypeVideo {
		t.Fatalf("08 artifact_ids = %v, want a video preview", mixTask.ArtifactIDs)
	}
	assertExportFile(t, preview.Path, domain.ExportFormatMP4H264, ffmpeg, ffprobe)

	// 交付导出回归：放行 08 门槛后仍能创建并执行 MP4 导出。
	approveGate(t, e, delivery)
	record := e.runExport(t, service.ExportRequest{
		ProjectID: project, RunID: run.ID,
		Kind: domain.ExportKindVideo, Format: domain.ExportFormatMP4H264,
		OutputDirGrant: t.TempDir(), NamingTemplate: "{project}-{language}",
	}, 120*time.Second)
	if record.Status != domain.TaskStatusSucceeded || record.OutputPath == "" {
		t.Fatalf("video export after 08 = %+v", record)
	}
	assertExportFile(t, record.OutputPath, domain.ExportFormatMP4H264, ffmpeg, ffprobe)
}

// TestMixExecutionWithoutCloneBurnsSubtitles 覆盖无 07 的 custom 链路：
// 08 用 burn_subtitles 保留源视频原声并烧录导入译文，仍产生交付审核。
func TestMixExecutionWithoutCloneBurnsSubtitles(t *testing.T) {
	ffmpeg, ffprobe, python, runnerRoot, videoPath, _ := startMixIntegration(t)
	client := startRunnerClient(t, python, runnerRoot, ffmpeg, ffprobe)
	e := newEnv(t, envOptions{executor: &execadapter.Executor{Client: client}, pipeline: true})
	startScheduler(t, e.sched, context.Background())

	project := e.seedProject()
	source := e.seedExportFileAsset(project, "source.mp4", domain.AssetTypeVideo, videoPath,
		&domain.MediaMeta{Container: "mp4", Codec: "h264", DurationMs: 2000, AudioTracks: 1})
	// 导入译稿冻结进 Run；04/06 都不需要执行，08 直接烧录。
	e.seedExportTrack(project, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "第一句"},
		{ID: "seg-2", StartMs: 1000, EndMs: 1900, Text: "第二句"},
	})
	resp, err := e.svc.CreateWorkflowRun(context.Background(), service.Idem{}, service.CreateWorkflowRunInput{
		ProjectID:        string(project),
		Name:             "无配音混音",
		Template:         "custom_pipeline",
		Features:         []string{"translate", "mix"},
		SourceLanguage:   "en",
		TargetLanguages:  []string{"zh"},
		InputRevisionIDs: []string{string(source.ID)},
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := resp.Body.(domain.WorkflowRun)
	delivery := waitForGate(t, e, run.ID, domain.ReviewTypeDelivery)
	if delivery.Status != domain.ReviewStatusPending {
		t.Fatalf("delivery gate = %+v, want pending", delivery)
	}

	mixTask := mixTaskOf(t, e, run.ID)
	if mixTask.Status != domain.TaskStatusSucceeded {
		t.Fatalf("08 task = %+v", mixTask)
	}
	plan, _ := mixTask.Params["plan"].(map[string]any)
	if plan["op"] != "burn_subtitles" {
		t.Fatalf("08 op = %v, want burn_subtitles", plan["op"])
	}
	if plan["input"] != source.Path || plan["out"] != "preview.mp4" {
		t.Fatalf("08 plan = %+v", plan)
	}
	if _, ok := plan["audio_inputs"]; ok {
		t.Fatalf("burn_subtitles must not carry audio_inputs: %+v", plan)
	}
	if _, ok := plan["language"]; ok {
		t.Fatalf("08 plan must not carry language: %+v", plan)
	}
	var preview *domain.Asset
	for _, assetID := range mixTask.ArtifactIDs {
		asset, err := e.st.GetAsset(context.Background(), assetID)
		if err != nil {
			t.Fatalf("08 artifact %s: %v", assetID, err)
		}
		if asset.Type == domain.AssetTypeVideo {
			preview = &asset
			break
		}
	}
	if preview == nil {
		t.Fatalf("08 artifact_ids = %v, want a video preview", mixTask.ArtifactIDs)
	}
	// burn_subtitles 不 -an：输出必须仍有音频流（源视频原声）。
	assertExportFile(t, preview.Path, domain.ExportFormatMP4H264, ffmpeg, ffprobe)
}

// TestMixExecutionUsesInputVideoAndAmbient 覆盖无 05 的 clone+mix 链路：
// 08 使用 Run 输入视频，并按 Asset.Meta 把原声以 0.3 混入。
func TestMixExecutionUsesInputVideoAndAmbient(t *testing.T) {
	ffmpeg, ffprobe, python, runnerRoot, videoPath, voicePath := startMixIntegration(t)
	client := startRunnerClient(t, python, runnerRoot, ffmpeg, ffprobe)
	exec := &mixPipelineExecutor{
		real:      &execadapter.Executor{Client: client},
		videoPath: videoPath, voicePath: voicePath,
	}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())

	project := e.seedProject()
	source := e.seedExportFileAsset(project, "source.mp4", domain.AssetTypeVideo, videoPath,
		&domain.MediaMeta{Container: "mp4", Codec: "h264", DurationMs: 2000, AudioTracks: 1})
	e.seedExportTrack(project, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "第一句"},
		{ID: "seg-2", StartMs: 1000, EndMs: 1900, Text: "第二句"},
	})
	resp, err := e.svc.CreateWorkflowRun(context.Background(), service.Idem{}, service.CreateWorkflowRunInput{
		ProjectID:        string(project),
		Name:             "输入视频混音",
		Template:         "custom_pipeline",
		Features:         []string{"clone", "mix"},
		SourceLanguage:   "en",
		TargetLanguages:  []string{"zh"},
		InputRevisionIDs: []string{string(source.ID)},
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := resp.Body.(domain.WorkflowRun)
	delivery := waitForGate(t, e, run.ID, domain.ReviewTypeDelivery)
	if delivery.Status != domain.ReviewStatusPending {
		t.Fatalf("delivery gate = %+v, want pending", delivery)
	}
	mixTask := mixTaskOf(t, e, run.ID)
	plan, _ := mixTask.Params["plan"].(map[string]any)
	if plan["op"] != "video_merge" || plan["video_src"] != source.Path {
		t.Fatalf("08 plan = %+v", plan)
	}
	inputs, _ := plan["audio_inputs"].([]any)
	if len(inputs) != 3 {
		t.Fatalf("audio_inputs = %d, want 2 dubbed + 1 ambient: %+v", len(inputs), inputs)
	}
	ambient, _ := inputs[2].(map[string]any)
	// 计划从 SQLite 读回后数字是 JSON float64；按值比较而不是 Go 类型。
	if ambient["path"] != source.Path || ambient["volume"] != 0.3 {
		t.Fatalf("ambient input = %+v", ambient)
	}
	if start, ok := ambient["start_ms"].(float64); !ok || start != 0 {
		t.Fatalf("ambient start_ms = %v", ambient["start_ms"])
	}
	var preview *domain.Asset
	for _, assetID := range mixTask.ArtifactIDs {
		asset, err := e.st.GetAsset(context.Background(), assetID)
		if err != nil {
			t.Fatalf("08 artifact %s: %v", assetID, err)
		}
		if asset.Type == domain.AssetTypeVideo {
			preview = &asset
			break
		}
	}
	if preview == nil {
		t.Fatalf("08 artifact_ids = %v, want a video preview", mixTask.ArtifactIDs)
	}
	assertExportFile(t, preview.Path, domain.ExportFormatMP4H264, ffmpeg, ffprobe)
}

// TestMixExecutionDeletesCandidateFailsReadably 覆盖计划创建后候选音频丢失：
// 08 失败且不登记空产物，Run 不为 SUCCEEDED。
func TestMixExecutionDeletesCandidateFailsReadably(t *testing.T) {
	ffmpeg, ffprobe, python, runnerRoot, videoPath, voicePath := startMixIntegration(t)
	client := startRunnerClient(t, python, runnerRoot, ffmpeg, ffprobe)
	exec := &mixPipelineExecutor{
		real:      &execadapter.Executor{Client: client},
		videoPath: videoPath, voicePath: voicePath,
	}
	// 用带缓冲通道把“删除了哪个候选路径”从调度 goroutine 同步给测试，
	// 避免 -race 下的普通变量读写竞态。
	deletedCh := make(chan string, 1)
	exec.beforeMix = func(req execadapter.Request) {
		inputs, _ := req.Params["audio_inputs"].([]any)
		if len(inputs) == 0 {
			return
		}
		entry, _ := inputs[0].(map[string]any)
		path, _ := entry["path"].(string)
		if path != "" {
			_ = os.Remove(path)
			deletedCh <- path
		}
	}
	e := newEnv(t, envOptions{executor: exec, pipeline: true})
	startScheduler(t, e.sched, context.Background())

	project := e.seedProject()
	source := e.seedExportFileAsset(project, "source.mp4", domain.AssetTypeVideo, videoPath,
		&domain.MediaMeta{Container: "mp4", Codec: "h264", DurationMs: 2000, AudioTracks: 1})
	run := e.seedRunWithInputs(project, source.ID)
	approveGate(t, e, waitForGate(t, e, run.ID, domain.ReviewTypeTranslation))

	failed := waitForNodeTask(t, e, run.ID, domain.NodeKeyMix, domain.TaskStatusFailed, 60*time.Second)
	select {
	case deleted := <-deletedCh:
		if deleted == "" {
			t.Fatal("test executor removed an empty candidate path")
		}
	default:
		t.Fatal("test executor did not observe the 08 audio plan")
	}
	if failed.Error == nil || failed.Error.Code == "" {
		t.Fatalf("failed 08 task has no readable error: %+v", failed)
	}
	if len(failed.ArtifactIDs) != 0 {
		t.Fatalf("failed 08 task must not register artifacts: %v", failed.ArtifactIDs)
	}
	gates, err := e.st.ListRunReviews(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ListRunReviews: %v", err)
	}
	for _, gate := range gates {
		if gate.Type == domain.ReviewTypeDelivery {
			t.Fatalf("failed 08 must not create a delivery gate: %+v", gate)
		}
	}
	final := e.waitRunStatus(run.ID, domain.RunStatusPartial, 20*time.Second)
	if final.Status != domain.RunStatusPartial {
		t.Fatalf("run status = %s, want PARTIAL", final.Status)
	}
}
