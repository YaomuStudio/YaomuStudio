package scheduler

// 外部仿真全流程测试（test-only）。
//
// 本文件不改变生产 Runner/FlashCLI 门控：模型节点由 simulationExecutor 返回
// 固定产物；导出任务仍交给真实 Python Runner → FFmpeg（有工具时）。
// 输入来自 testdata/simulation/ 的 JSON 与 SRT 文件，不在测试代码里硬编码
// 字幕正文。测试覆盖：八节点顺序、06/08 审核 gate、候选/选用、SRT/ASS/WAV/
// MP3/MP4 导出、事件链与 Run 终态。
//
// 运行：
//   cd app-server
//   go test ./internal/scheduler -run TestSimulationFullFlow -v
//
// FFmpeg/Python 缺失时仍会跑节点与字幕导出，媒体导出显式 skip，不伪装通过。

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

type simulationMedia struct {
	DurationMs       domain.Millis `json:"duration_ms"`
	Width            int           `json:"width"`
	Height           int           `json:"height"`
	SampleRate       int           `json:"sample_rate"`
	VideoFrequencyHz int           `json:"video_frequency_hz"`
	VoiceFrequencyHz int           `json:"voice_frequency_hz"`
}

type simulationFixture struct {
	Name            string                   `json:"name"`
	SourceLanguage  string                   `json:"source_language"`
	TargetLanguages []string                 `json:"target_languages"`
	ExtractMode     domain.ExtractMode       `json:"extract_mode"`
	EraseMode       domain.EraseMode         `json:"erase_mode"`
	SourceSRT       string                   `json:"source_srt"`
	TranslatedSRT   string                   `json:"translated_srt"`
	Media           simulationMedia          `json:"media"`
	Nodes           []domain.PipelineNodeKey `json:"nodes"`
}

type simulationCue struct {
	StartMs domain.Millis
	EndMs   domain.Millis
	Text    string
}

func loadSimulationFixture(t *testing.T) (simulationFixture, []simulationCue, []simulationCue) {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "app-server", "testdata", "simulation")
	data, err := os.ReadFile(filepath.Join(dir, "full-flow.json"))
	if err != nil {
		t.Fatalf("read simulation fixture: %v", err)
	}
	var fixture simulationFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode simulation fixture: %v", err)
	}
	if fixture.Name == "" || fixture.SourceLanguage == "" || len(fixture.TargetLanguages) == 0 {
		t.Fatalf("simulation fixture missing project/language fields: %+v", fixture)
	}
	for _, language := range fixture.TargetLanguages {
		if strings.TrimSpace(language) == "" {
			t.Fatalf("simulation fixture has empty target language: %+v", fixture.TargetLanguages)
		}
	}
	if len(fixture.Nodes) != 8 {
		t.Fatalf("simulation fixture nodes = %d, want 8", len(fixture.Nodes))
	}
	expectedNodes := []domain.PipelineNodeKey{
		domain.NodeKeyImport,
		domain.NodeKeyAnalyze,
		domain.NodeKeySeparate,
		domain.NodeKeyExtract,
		domain.NodeKeyErase,
		domain.NodeKeyTranslate,
		domain.NodeKeyClone,
		domain.NodeKeyMix,
	}
	seen := map[domain.PipelineNodeKey]bool{}
	for i, key := range fixture.Nodes {
		if !key.Valid() || seen[key] {
			t.Fatalf("simulation fixture has invalid/duplicate node %q", key)
		}
		if key != expectedNodes[i] {
			t.Fatalf("simulation fixture node %d = %q, want %q", i, key, expectedNodes[i])
		}
		seen[key] = true
	}
	if fixture.Media.DurationMs <= 0 || fixture.Media.SampleRate <= 0 ||
		fixture.Media.Width <= 0 || fixture.Media.Height <= 0 ||
		fixture.Media.VideoFrequencyHz <= 0 || fixture.Media.VoiceFrequencyHz <= 0 {
		t.Fatalf("simulation media spec invalid: %+v", fixture.Media)
	}

	sourceCues := parseSimulationSRTFile(t, dir, fixture.SourceSRT)
	translatedCues := parseSimulationSRTFile(t, dir, fixture.TranslatedSRT)
	if len(sourceCues) != len(translatedCues) {
		t.Fatalf("source/translated cue counts differ: %d vs %d", len(sourceCues), len(translatedCues))
	}
	for i := range sourceCues {
		if sourceCues[i].StartMs != translatedCues[i].StartMs || sourceCues[i].EndMs != translatedCues[i].EndMs {
			t.Fatalf("cue %d timing differs: source=%+v translated=%+v", i, sourceCues[i], translatedCues[i])
		}
	}
	return fixture, sourceCues, translatedCues
}

func parseSimulationSRTFile(t *testing.T, dir, name string) []simulationCue {
	t.Helper()
	if name == "" || filepath.IsAbs(name) {
		t.Fatalf("simulation fixture SRT name must be a relative path: %q", name)
	}
	path := filepath.Join(dir, name)
	rel, err := filepath.Rel(dir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("simulation SRT %q escapes fixture dir", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read simulation SRT %s: %v", name, err)
	}
	cues, err := parseSimulationSRT(data)
	if err != nil {
		t.Fatalf("parse simulation SRT %s: %v", name, err)
	}
	if len(cues) == 0 {
		t.Fatalf("simulation SRT %s has no cues", name)
	}
	return cues
}

func parseSimulationSRT(data []byte) ([]simulationCue, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	blocks := strings.Split(text, "\n\n")
	cues := make([]simulationCue, 0, len(blocks))
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		lines := strings.Split(block, "\n")
		timeIdx := -1
		for i, line := range lines {
			if strings.Contains(line, "-->") {
				timeIdx = i
				break
			}
		}
		if timeIdx < 0 || timeIdx+1 >= len(lines) {
			return nil, fmt.Errorf("malformed cue block %q", block)
		}
		parts := strings.SplitN(lines[timeIdx], "-->", 2)
		start, err := parseSimulationSRTTime(strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, err
		}
		end, err := parseSimulationSRTTime(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, err
		}
		if end <= start {
			return nil, fmt.Errorf("cue ends before it starts: %d..%d", start, end)
		}
		cues = append(cues, simulationCue{
			StartMs: start,
			EndMs:   end,
			Text:    strings.TrimSpace(strings.Join(lines[timeIdx+1:], "\n")),
		})
	}
	return cues, nil
}

func parseSimulationSRTTime(raw string) (domain.Millis, error) {
	parts := strings.Split(raw, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid SRT time %q", raw)
	}
	seconds := strings.SplitN(parts[2], ",", 2)
	if len(seconds) != 2 {
		return 0, fmt.Errorf("invalid SRT time %q", raw)
	}
	values := make([]int, 4)
	for i, value := range []string{parts[0], parts[1], seconds[0], seconds[1]} {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return 0, fmt.Errorf("invalid SRT time %q: %w", raw, err)
		}
		values[i] = parsed
	}
	if values[0] < 0 || values[1] < 0 || values[1] > 59 || values[2] < 0 || values[2] > 59 ||
		values[3] < 0 || values[3] > 999 {
		return 0, fmt.Errorf("invalid SRT time %q", raw)
	}
	return domain.Millis(((values[0]*60+values[1])*60+values[2])*1000 + values[3]), nil
}

// simulationExecutor 是 test-only 模型执行器：
//   - 模型节点：写固定产物，不构造任何 FlashCLI 参数；
//   - 导出任务（adapter=ffmpeg）：委托真实 Runner/FFmpeg，若未提供则返回显式错误。
type simulationExecutor struct {
	t      *testing.T
	export Executor

	mu          sync.Mutex
	modelCalls  int
	exportCalls int
}

func (s *simulationExecutor) Execute(
	ctx context.Context,
	req execadapter.Request,
	progress execadapter.ProgressFunc,
	logs execadapter.LogFunc,
) (result execadapter.Result) {
	defer func() {
		if result.TransportErr == nil && result.Binding == (execadapter.Binding{}) {
			result.Binding = requestBinding(req)
		}
	}()
	if req.Adapter.ID == "ffmpeg" && req.Params != nil {
		s.mu.Lock()
		s.exportCalls++
		s.mu.Unlock()
		if s.export == nil {
			return simulationFailure("SIMULATION_EXPORT_EXECUTOR_MISSING",
				"仿真导出需要真实 Runner/FFmpeg")
		}
		return s.export.Execute(ctx, req, progress, logs)
	}

	s.mu.Lock()
	s.modelCalls++
	s.mu.Unlock()
	if req.OutputDir == "" {
		return simulationFailure("SIMULATION_OUTPUT_DIR_MISSING", "调度层未分配输出目录")
	}
	if err := os.MkdirAll(req.OutputDir, 0o755); err != nil {
		return simulationFailure("SIMULATION_OUTPUT_DIR_UNAVAILABLE", err.Error())
	}
	artifact := filepath.Join(req.OutputDir, "simulated-"+req.TaskID+".txt")
	if err := os.WriteFile(artifact, []byte("simulated model output for "+req.TaskID+"\n"), 0o600); err != nil {
		return simulationFailure("SIMULATION_ARTIFACT_WRITE_FAILED", err.Error())
	}
	progress("simulate", 1, 1)
	return execadapter.Result{
		ArtifactIDs: []string{"sim-artifact-" + req.TaskID},
		Binding: execadapter.Binding{
			Workspace:   req.Workspace,
			ServerEpoch: req.ServerEpoch,
			TaskID:      req.TaskID,
			StepID:      req.StepID,
			Attempt:     req.Attempt,
		},
	}
}

func (s *simulationExecutor) calls() (model, export int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modelCalls, s.exportCalls
}

func simulationFailure(code, message string) execadapter.Result {
	return execadapter.Result{Error: &execadapter.ExecError{
		Code:      code,
		Message:   message,
		Retryable: false,
	}}
}

type simulationDriver struct {
	t        *testing.T
	e        *env
	fixture  simulationFixture
	project  domain.ID
	run      domain.WorkflowRun
	audioDir string

	sourceAsset      domain.Asset
	sourceTrack      domain.SubtitleTrack
	translationTrack domain.SubtitleTrack
	sourceCues       []simulationCue
	translatedCues   []simulationCue
}

func (d *simulationDriver) runNodes() {
	d.t.Helper()
	for _, key := range d.fixture.Nodes {
		switch key {
		case domain.NodeKeyExtract:
			task := d.createNode(key, d.fixture.SourceLanguage, []string{string(d.sourceAsset.ID)})
			d.waitTaskNode(task, key, domain.NodeStatusDone)
			d.sourceTrack = d.seedTrack(d.fixture.SourceLanguage, domain.TrackTypeTranscription, "src", d.sourceCues)
		case domain.NodeKeyTranslate:
			task := d.createNode(key, d.fixture.TargetLanguages[0], []string{string(d.sourceTrack.CurrentRevisionID)})
			d.waitTaskNode(task, key, domain.NodeStatusWaitingReview)
			d.translationTrack = d.seedTrack(d.fixture.TargetLanguages[0], domain.TrackTypeTranslation, "tgt", d.translatedCues)
			d.approveGate(key, domain.ReviewTypeTranslation, d.fixture.TargetLanguages[0])
		case domain.NodeKeyClone:
			task := d.createNode(key, d.fixture.TargetLanguages[0], []string{string(d.translationTrack.CurrentRevisionID)})
			d.waitTaskNode(task, key, domain.NodeStatusDone)
			d.seedCandidates()
		case domain.NodeKeyMix:
			task := d.createNode(key, d.fixture.TargetLanguages[0], []string{string(d.translationTrack.CurrentRevisionID)})
			d.waitTaskNode(task, key, domain.NodeStatusWaitingReview)
			d.approveGate(key, domain.ReviewTypeDelivery, "")
			d.e.waitRunStatus(d.run.ID, domain.RunStatusSucceeded, 20*time.Second)
		default:
			task := d.createNode(key, "", nil)
			d.waitTaskNode(task, key, domain.NodeStatusDone)
		}
	}
}

func (d *simulationDriver) createNode(key domain.PipelineNodeKey, language string, inputIDs []string) domain.Task {
	d.t.Helper()
	lock := executionLockForNode(key)
	resp, err := d.e.svc.CreateTask(context.Background(), service.Idem{}, service.CreateTaskRequest{
		ProjectID:        string(d.project),
		RunID:            string(d.run.ID),
		Kind:             domain.TaskKindNode,
		NodeKey:          key,
		Language:         language,
		InputRevisionIDs: inputIDs,
		ModelLock:        &lock,
	})
	if err != nil {
		d.t.Fatalf("CreateTask %s: %v", key, err)
	}
	task := resp.Body.(domain.Task)
	if err := d.e.svc.CheckTaskDispatchable(context.Background(), d.e.st, task); err != nil {
		d.t.Logf("SIMULATION_DISPATCH_PREVIEW node=%s error=%v lock=%+v", key, err, task.ModelLock)
	}
	return task
}

func (d *simulationDriver) waitTaskNode(task domain.Task, key domain.PipelineNodeKey, want domain.NodeStatus) {
	d.t.Helper()
	d.e.waitTask(task.ID, domain.TaskStatusSucceeded, 20*time.Second)
	d.e.waitNode(d.run.ID, key, want, 20*time.Second)
}

func (d *simulationDriver) approveGate(key domain.PipelineNodeKey, typ domain.ReviewType, language string) {
	d.t.Helper()
	gates, err := d.e.svc.ListProjectReviews(context.Background(), string(d.project), string(d.run.ID))
	if err != nil {
		d.t.Fatalf("ListProjectReviews: %v", err)
	}
	var found *domain.ReviewGate
	for i := range gates {
		gate := &gates[i]
		if gate.Type != typ || gate.Status != domain.ReviewStatusPending {
			continue
		}
		if language != "" && gate.Language != language {
			continue
		}
		found = gate
		break
	}
	if found == nil {
		d.t.Fatalf("no pending %s review gate for node %s language %q", typ, key, language)
	}
	if _, err := d.e.svc.ApproveReview(context.Background(), service.Idem{}, string(found.ID),
		service.ApproveReviewRequest{RevisionDigest: found.RevisionDigest, Type: found.Type}); err != nil {
		d.t.Fatalf("ApproveReview %s: %v", found.ID, err)
	}
	d.e.waitNode(d.run.ID, key, domain.NodeStatusDone, 20*time.Second)
}

func (d *simulationDriver) seedTrack(language string, typ domain.TrackType, prefix string, cues []simulationCue) domain.SubtitleTrack {
	d.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	revisionID := store.NewID("srev")
	segments := make([]domain.Segment, 0, len(cues))
	for i, cue := range cues {
		segments = append(segments, domain.Segment{
			ID:      domain.ID(fmt.Sprintf("%s-%d", prefix, i+1)),
			StartMs: cue.StartMs,
			EndMs:   cue.EndMs,
			Text:    cue.Text,
		})
	}
	track := domain.SubtitleTrack{
		ID:                store.NewID("track"),
		ProjectID:         d.project,
		Language:          language,
		Type:              typ,
		CurrentRevisionID: revisionID,
		CreatedAt:         now,
		UpdatedAt:         now,
		Revisions: []domain.SubtitleRevision{{
			ID:         revisionID,
			TrackID:    "",
			AuthorType: domain.AuthorTypeSystem,
			Segments:   segments,
			CreatedAt:  now,
		}},
	}
	if err := d.e.st.CreateSubtitleTrack(ctx, track); err != nil {
		d.t.Fatalf("CreateSubtitleTrack %s: %v", language, err)
	}
	built, err := d.e.st.GetSubtitleTrack(ctx, track.ID)
	if err != nil {
		d.t.Fatalf("GetSubtitleTrack %s: %v", language, err)
	}
	return built
}

func (d *simulationDriver) seedCandidates() {
	d.t.Helper()
	if len(d.translationTrack.Revisions) == 0 {
		d.t.Fatalf("translation track has no revision")
	}
	segments := d.translationTrack.Revisions[0].Segments
	if err := os.MkdirAll(d.audioDir, 0o755); err != nil {
		d.t.Fatalf("create simulation audio dir: %v", err)
	}
	for i, segment := range segments {
		duration := segment.EndMs - segment.StartMs
		path := filepath.Join(d.audioDir, fmt.Sprintf("voice-%d.wav", i+1))
		writeSimulationToneWAV(d.t, path, duration, d.fixture.Media.SampleRate,
			d.fixture.Media.VoiceFrequencyHz+i*40)
		asset := d.e.seedExportFileAsset(string(d.project),
			filepath.Base(path), domain.AssetTypeAudio, path,
			&domain.MediaMeta{
				Container:   "wav",
				Codec:       "pcm",
				DurationMs:  duration,
				AudioTracks: 1,
				SampleRate:  d.fixture.Media.SampleRate,
			})
		d.e.seedExportSelection(string(d.project), string(segment.ID),
			d.fixture.TargetLanguages[0], string(asset.ID))
	}
}

func (e *env) waitNode(runID domain.ID, key domain.PipelineNodeKey, want domain.NodeStatus, timeout time.Duration) domain.WorkflowRun {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	var last domain.WorkflowRun
	for time.Now().Before(deadline) {
		run, err := e.st.GetWorkflowRun(context.Background(), runID)
		if err != nil {
			e.t.Fatalf("GetWorkflowRun: %v", err)
		}
		last = run
		if nodeStatus(run, key) == want {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("run %s node %s status = %s, want %s (run=%s)",
		runID, key, nodeStatus(last, key), want, last.Status)
	return last
}

func (e *env) waitRunStatus(runID domain.ID, want domain.RunStatus, timeout time.Duration) domain.WorkflowRun {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	var last domain.WorkflowRun
	for time.Now().Before(deadline) {
		run, err := e.st.GetWorkflowRun(context.Background(), runID)
		if err != nil {
			e.t.Fatalf("GetWorkflowRun: %v", err)
		}
		last = run
		if run.Status == want {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tasks, err := e.st.ListRunTasks(context.Background(), runID); err == nil {
		for _, task := range tasks {
			attempts, attemptErr := e.st.ListAttempts(context.Background(), task.ID)
			e.t.Logf("task node=%s language=%s status=%s error=%+v attempts=%+v attempt_error=%v", task.NodeKey, task.Language, task.Status, task.Error, attempts, attemptErr)
		}
	}
	e.t.Fatalf("run %s status = %s, want %s", runID, last.Status, want)
	return last
}

func buildSimulationSource(t *testing.T, ffmpeg string, fixture simulationFixture, dir string) (string, bool) {
	t.Helper()
	path := filepath.Join(dir, "source.mp4")
	if ffmpeg == "" {
		writeSimulationDummy(t, path)
		return path, false
	}
	seconds := strconv.FormatFloat(float64(fixture.Media.DurationMs)/1000, 'f', 3, 64)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=%dx%d:rate=25:duration=%s",
			fixture.Media.Width, fixture.Media.Height, seconds),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:sample_rate=%d:duration=%s",
			fixture.Media.VideoFrequencyHz, fixture.Media.SampleRate, seconds),
		"-shortest", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "96k", path,
	}
	_, failure := runMediaCommandBounded(t, context.Background(),
		mediaCommandOptions{Stage: "build_simulation_sample"}, ffmpeg, args...)
	if failure != nil {
		// 普通工具/编码失败沿用既有 dummy+跳过；超时、取消、输出超限等
		// 必须失败，不能降级成媒体 "skip"。
		if failure.hardFailure() {
			t.Fatalf("SIMULATION_MEDIA_FATAL: %v", failure)
		}
		t.Logf("SIMULATION_MEDIA_UNAVAILABLE: ffmpeg 生成样本失败，媒体导出将跳过：%v", failure)
		writeSimulationDummy(t, path)
		return path, false
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Logf("SIMULATION_MEDIA_UNAVAILABLE: ffmpeg 未生成有效样本，媒体导出将跳过：%v", err)
		writeSimulationDummy(t, path)
		return path, false
	}
	return path, true
}

func writeSimulationDummy(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("simulation-media-placeholder"), 0o600); err != nil {
		t.Fatalf("write simulation placeholder: %v", err)
	}
}

func writeSimulationToneWAV(t *testing.T, path string, durationMs domain.Millis, sampleRate, frequency int) {
	t.Helper()
	samples := int(int64(sampleRate) * int64(durationMs) / 1000)
	if samples <= 0 {
		t.Fatalf("invalid simulation tone duration %dms", durationMs)
	}
	dataSize := samples * 2
	buffer := make([]byte, 44+dataSize)
	copy(buffer[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buffer[4:8], uint32(36+dataSize))
	copy(buffer[8:12], "WAVE")
	copy(buffer[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buffer[16:20], 16)
	binary.LittleEndian.PutUint16(buffer[20:22], 1)
	binary.LittleEndian.PutUint16(buffer[22:24], 1)
	binary.LittleEndian.PutUint32(buffer[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buffer[28:32], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(buffer[32:34], 2)
	binary.LittleEndian.PutUint16(buffer[34:36], 16)
	copy(buffer[36:40], "data")
	binary.LittleEndian.PutUint32(buffer[40:44], uint32(dataSize))
	for i := 0; i < samples; i++ {
		sample := int16(0.2 * 32767 * math.Sin(2*math.Pi*float64(frequency)*float64(i)/float64(sampleRate)))
		binary.LittleEndian.PutUint16(buffer[44+i*2:], uint16(sample))
	}
	if err := os.WriteFile(path, buffer, 0o600); err != nil {
		t.Fatalf("write simulation tone %s: %v", path, err)
	}
}

func assertTrackCues(t *testing.T, label string, track domain.SubtitleTrack, language string, typ domain.TrackType, cues []simulationCue) {
	t.Helper()
	if track.Language != language || track.Type != typ {
		t.Fatalf("%s track = language %q type %q, want %q/%q", label, track.Language, track.Type, language, typ)
	}
	if len(track.Revisions) == 0 {
		t.Fatalf("%s track has no revision", label)
	}
	revision := track.Revisions[0]
	if track.CurrentRevisionID != revision.ID {
		t.Fatalf("%s track current revision = %s, want %s", label, track.CurrentRevisionID, revision.ID)
	}
	if len(revision.Segments) != len(cues) {
		t.Fatalf("%s segments = %d, want %d", label, len(revision.Segments), len(cues))
	}
	for i, cue := range cues {
		segment := revision.Segments[i]
		if segment.StartMs != cue.StartMs || segment.EndMs != cue.EndMs || segment.Text != cue.Text {
			t.Fatalf("%s segment %d = %+v, want %+v", label, i, segment, cue)
		}
	}
}

func formatSimulationSRTTime(ms domain.Millis) string {
	total := int64(ms)
	return fmt.Sprintf("%02d:%02d:%02d,%03d",
		total/3600000, (total/60000)%60, (total/1000)%60, total%1000)
}

func assertSimulationSubtitle(t *testing.T, path string, format domain.ExportFormat, cues []simulationCue) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read subtitle export %s: %v", path, err)
	}
	if !utf8.Valid(data) {
		t.Fatalf("subtitle export %s is not valid UTF-8", path)
	}
	text := string(data)
	for _, cue := range cues {
		if !strings.Contains(text, cue.Text) {
			t.Fatalf("subtitle export %s missing fixture text %q:\n%s", path, cue.Text, text)
		}
	}
	switch format {
	case domain.ExportFormatSRT:
		if strings.Count(text, "-->") < len(cues) {
			t.Fatalf("SRT has fewer cues than fixture (%d):\n%s", len(cues), text)
		}
		wantTiming := formatSimulationSRTTime(cues[0].StartMs) + " --> " + formatSimulationSRTTime(cues[0].EndMs)
		if !strings.Contains(text, wantTiming) {
			t.Fatalf("SRT timing %q from fixture missing:\n%s", wantTiming, text)
		}
	case domain.ExportFormatASS:
		if !strings.HasPrefix(text, "[Script Info]") || strings.Count(text, "Dialogue:") < len(cues) {
			t.Fatalf("ASS structure/cue count invalid:\n%s", text)
		}
	}
}

func findSimulationPython() string {
	var candidates []string
	if value := strings.TrimSpace(os.Getenv("PYTHON_BIN")); value != "" {
		candidates = append(candidates, value)
	}
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		duplicate := false
		for _, candidate := range candidates {
			if strings.EqualFold(candidate, path) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			candidates = append(candidates, path)
		}
	}
	for _, candidate := range candidates {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		output, err := exec.CommandContext(ctx, candidate, "-c", "import sys; print(sys.version_info[0])").CombinedOutput()
		cancel()
		if err == nil && strings.TrimSpace(string(output)) == "3" {
			return candidate
		}
	}
	return ""
}

// TestSimulationFullFlow 用外部 fixture 驱动八节点流程、审核和导出。
func TestSimulationFullFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("SKIP: -short 模式不运行外部仿真全流程")
	}
	fixture, sourceCues, translatedCues := loadSimulationFixture(t)

	ffmpeg := findExecutable("FFMPEG_BIN", "ffmpeg")
	ffprobe := findExecutable("FFPROBE_BIN", "ffprobe")
	python := findSimulationPython()

	var exportExecutor Executor
	if ffmpeg != "" && python != "" {
		client := startRunnerClient(t, python, filepath.Join(repoRoot(t), "runner"), ffmpeg, ffprobe)
		exportExecutor = &execadapter.Executor{Client: client}
	} else {
		t.Logf("SIMULATION_MEDIA_EXPORT_SKIPPED: ffmpeg=%q python=%q；节点与字幕导出仍会执行",
			ffmpeg, python)
	}

	simExec := &simulationExecutor{t: t, export: exportExecutor}
	e := newEnv(t, envOptions{executor: simExec})
	startScheduler(t, e.sched, context.Background())

	projectResp, err := e.svc.CreateProject(context.Background(), service.Idem{},
		service.CreateProjectRequest{Name: fixture.Name})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	project := projectResp.Body.(domain.Project)

	sourcePath, mediaReady := buildSimulationSource(t, ffmpeg, fixture, t.TempDir())
	sourceAsset := e.seedExportFileAsset(string(project.ID), "source.mp4", domain.AssetTypeVideo, sourcePath,
		&domain.MediaMeta{
			Container:   "mp4",
			Codec:       "h264",
			Width:       fixture.Media.Width,
			Height:      fixture.Media.Height,
			DurationMs:  fixture.Media.DurationMs,
			AudioTracks: 1,
			SampleRate:  fixture.Media.SampleRate,
		})

	runResp, err := e.svc.CreateWorkflowRun(context.Background(), service.Idem{}, service.CreateWorkflowRunInput{
		ProjectID:        string(project.ID),
		Name:             fixture.Name,
		SourceLanguage:   fixture.SourceLanguage,
		TargetLanguages:  fixture.TargetLanguages,
		InputRevisionIDs: []string{string(sourceAsset.ID)},
		Extract:          &domain.ExtractOptions{Mode: fixture.ExtractMode},
		EraseMode:        fixture.EraseMode,
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := runResp.Body.(domain.WorkflowRun)
	if len(run.Nodes) != 8 {
		t.Fatalf("created run nodes = %d, want 8", len(run.Nodes))
	}

	driver := &simulationDriver{
		t:              t,
		e:              e,
		fixture:        fixture,
		project:        project.ID,
		run:            run,
		audioDir:       filepath.Join(e.data, "simulation-voices"),
		sourceAsset:    sourceAsset,
		sourceCues:     sourceCues,
		translatedCues: translatedCues,
	}
	driver.runNodes()
	assertTrackCues(t, "source", driver.sourceTrack, fixture.SourceLanguage, domain.TrackTypeTranscription, sourceCues)
	assertTrackCues(t, "translation", driver.translationTrack, fixture.TargetLanguages[0], domain.TrackTypeTranslation, translatedCues)

	finalRun := e.waitRunStatus(run.ID, domain.RunStatusSucceeded, 20*time.Second)
	for _, node := range finalRun.Nodes {
		if node.Status != domain.NodeStatusDone {
			t.Fatalf("node %s status = %s, want done", node.Key, node.Status)
		}
	}

	gates, err := e.svc.ListProjectReviews(context.Background(), string(project.ID), string(run.ID))
	if err != nil {
		t.Fatalf("ListProjectReviews: %v", err)
	}
	if len(gates) != 2 {
		t.Fatalf("review gates = %d, want 2: %+v", len(gates), gates)
	}
	seenReview := map[domain.ReviewType]bool{}
	for _, gate := range gates {
		if gate.Status != domain.ReviewStatusApproved {
			t.Fatalf("review %s status = %s, want approved", gate.ID, gate.Status)
		}
		seenReview[gate.Type] = true
	}
	if !seenReview[domain.ReviewTypeTranslation] || !seenReview[domain.ReviewTypeDelivery] {
		t.Fatalf("review gate types = %+v, want translation + delivery", seenReview)
	}

	tasks, err := e.svc.ListTasks(context.Background(), service.ListTasksQuery{ProjectID: string(project.ID)})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	nodeTasks := 0
	for _, task := range tasks.Items {
		if task.Kind == domain.TaskKindNode {
			nodeTasks++
			if task.Status != domain.TaskStatusSucceeded {
				t.Fatalf("node task %s status = %s, want succeeded", task.ID, task.Status)
			}
		}
	}
	if nodeTasks != 8 {
		t.Fatalf("node tasks = %d, want 8", nodeTasks)
	}

	for _, segment := range driver.translationTrack.Revisions[0].Segments {
		candidates, err := e.svc.ListCandidates(context.Background(), string(segment.ID), fixture.TargetLanguages[0])
		if err != nil {
			t.Fatalf("ListCandidates %s: %v", segment.ID, err)
		}
		if len(candidates) != 1 {
			t.Fatalf("candidates for %s = %d, want 1", segment.ID, len(candidates))
		}
		selection, err := e.svc.GetCandidateSelection(context.Background(), string(segment.ID), fixture.TargetLanguages[0])
		if err != nil {
			t.Fatalf("GetCandidateSelection %s: %v", segment.ID, err)
		}
		if selection.CandidateID != candidates[0].ID {
			t.Fatalf("selection for %s = %s, want %s", segment.ID, selection.CandidateID, candidates[0].ID)
		}
	}

	expectedExports := 0
	for _, format := range []domain.ExportFormat{domain.ExportFormatSRT, domain.ExportFormatASS} {
		record := e.runExport(t, service.ExportRequest{
			ProjectID:       string(project.ID),
			RunID:           string(run.ID),
			Kind:            domain.ExportKindSubtitle,
			Format:          format,
			InputRevisionID: string(driver.translationTrack.CurrentRevisionID),
			OutputDirGrant:  t.TempDir(),
			NamingTemplate:  "sim-{language}",
		}, 20*time.Second)
		if record.Status != domain.TaskStatusSucceeded || record.OutputPath == "" {
			t.Fatalf("subtitle export %s = %+v", format, record)
		}
		assertSimulationSubtitle(t, record.OutputPath, format, translatedCues)
		expectedExports++
	}

	mediaExports := mediaReady && exportExecutor != nil
	if mediaExports {
		mediaCases := []struct {
			name   string
			kind   domain.ExportKind
			format domain.ExportFormat
		}{
			{"video_mp4", domain.ExportKindVideo, domain.ExportFormatMP4H264},
			{"audio_wav", domain.ExportKindAudio, domain.ExportFormatWAV},
			{"audio_mp3", domain.ExportKindAudio, domain.ExportFormatMP3},
		}
		for _, tc := range mediaCases {
			t.Run(tc.name, func(t *testing.T) {
				record := e.runExport(t, service.ExportRequest{
					ProjectID:      string(project.ID),
					RunID:          string(run.ID),
					Kind:           tc.kind,
					Format:         tc.format,
					OutputDirGrant: t.TempDir(),
					NamingTemplate: "sim-{language}",
				}, 120*time.Second)
				if record.Status != domain.TaskStatusSucceeded || record.OutputPath == "" {
					task, _ := e.st.GetTask(context.Background(), record.TaskID)
					t.Fatalf("media export %s = %+v task = %+v", tc.format, record, task)
				}
				assertExportFile(t, record.OutputPath, tc.format, ffmpeg, ffprobe)
				expectedExports++
			})
		}
	} else {
		t.Logf("SIMULATION_MEDIA_EXPORT_SKIPPED: media_ready=%v export_executor=%v", mediaReady, exportExecutor != nil)
	}

	records, err := e.svc.ListExports(context.Background(), string(project.ID))
	if err != nil {
		t.Fatalf("ListExports: %v", err)
	}
	if len(records) != expectedExports {
		t.Fatalf("export records = %d, want %d: %+v", len(records), expectedExports, records)
	}
	for _, record := range records {
		if record.Status != domain.TaskStatusSucceeded || record.OutputPath == "" {
			t.Fatalf("export record = %+v", record)
		}
	}

	events, err := e.st.EventsAfter(context.Background(), 0, 5000)
	if err != nil {
		t.Fatalf("EventsAfter: %v", err)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Type]++
	}
	for _, required := range []string{"workflow.created", "workflow.waiting_review", "review.approved", "workflow.completed", "export.succeeded"} {
		if counts[required] == 0 {
			t.Fatalf("event %s missing; counts=%v", required, counts)
		}
	}
	if counts["workflow.waiting_review"] != 2 || counts["review.approved"] != 2 {
		t.Fatalf("review event counts = %v, want 2 each", counts)
	}
	if counts["workflow.completed"] != 1 {
		t.Fatalf("workflow.completed count = %d, want 1; counts=%v", counts["workflow.completed"], counts)
	}
	if counts["export.succeeded"] < expectedExports {
		t.Fatalf("export.succeeded count = %d, want >=%d; counts=%v", counts["export.succeeded"], expectedExports, counts)
	}

	modelCalls, exportCalls := simExec.calls()
	if modelCalls != 8 {
		t.Fatalf("simulation model calls = %d, want 8", modelCalls)
	}
	if mediaExports && exportCalls != 3 {
		t.Fatalf("simulation export calls = %d, want 3", exportCalls)
	}

	t.Logf("SIMULATION FULL FLOW PASS: project=%s run=%s nodes=8 reviews=2 exports=%d model_calls=%d export_calls=%d",
		project.ID, run.ID, expectedExports, modelCalls, exportCalls)
}
