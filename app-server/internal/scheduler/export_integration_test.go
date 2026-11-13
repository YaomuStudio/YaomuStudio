package scheduler

// 真实 FFmpeg + 真实 Python Runner 的导出执行集成测试。
//
// 本机有 ffmpeg 时必须真实执行：生成 2 秒样本视频，经 Core → Runner →
// FFmpeg 完成 MP4/WAV/MP3 导出，再对产物做容器头与解码校验；ffprobe 可用时
// 追加媒体属性断言。ffprobe 缺失时显式打印 FFPROBE_UNAVAILABLE，不伪装其输出；
// 设置 FFPROBE_REQUIRED=1（CI / 媒体入口）时 ffprobe 缺失直接失败，避免把跳过当成通过。

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func findExecutable(envKey, name string) string {
	if envKey != "" {
		if value := strings.TrimSpace(os.Getenv(envKey)); value != "" {
			if info, err := os.Stat(value); err == nil && !info.IsDir() {
				return value
			}
		}
	}
	found, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return found
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func buildSampleVideo(t *testing.T, ffmpeg, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "sample.mp4")
	runMediaCommand(t, "build_sample_video", ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=25:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2",
		"-shortest", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "96k", path)
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("sample video missing: %v", err)
	}
	return path
}

func buildVoiceClip(t *testing.T, ffmpeg, dir, name string, frequency int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	runMediaCommand(t, "build_voice_clip", ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i",
		fmt.Sprintf("sine=frequency=%d:sample_rate=48000:duration=0.4", frequency),
		"-c:a", "pcm_s16le", path)
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("voice clip missing: %v", err)
	}
	return path
}

func startRunnerClient(t *testing.T, python, runnerRoot, ffmpeg, ffprobe string) *execadapter.Client {
	t.Helper()
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		switch {
		case strings.HasPrefix(entry, "FFMPEG_BIN="),
			strings.HasPrefix(entry, "FFPROBE_BIN="),
			strings.HasPrefix(entry, "PYTHONPATH="):
			continue
		}
		env = append(env, entry)
	}
	env = append(env,
		"FFMPEG_BIN="+ffmpeg,
		"PYTHONPATH="+filepath.Join(runnerRoot, "src"),
	)
	if ffprobe != "" {
		env = append(env, "FFPROBE_BIN="+ffprobe)
	}
	client, err := execadapter.NewProcessClient(context.Background(), execadapter.ProcessOptions{
		Command: python,
		Args:    []string{"-m", "yaomustudio_runner"},
		Dir:     runnerRoot,
		Env:     env,
	})
	if err != nil {
		t.Fatalf("start runner: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	info, err := client.Start(ctx)
	if err != nil {
		t.Fatalf("runner hello: %v", err)
	}
	t.Logf("runner hello: version=%s adapters=%v media_tools=%v",
		info.RunnerVersion, info.Adapters, info.MediaTools)
	found := false
	for _, adapter := range info.Adapters {
		if adapter == "ffmpeg" {
			found = true
		}
	}
	if !found {
		t.Fatalf("runner did not declare ffmpeg adapter: %v", info.Adapters)
	}
	return client
}

func (e *env) seedExportFileAsset(projectID, name string, assetType domain.AssetType, path string, meta *domain.MediaMeta) domain.Asset {
	e.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	info, err := os.Stat(path)
	if err != nil {
		e.t.Fatalf("stat asset %s: %v", name, err)
	}
	file, err := os.Open(path)
	if err != nil {
		e.t.Fatal(err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		e.t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		e.t.Fatal(err)
	}
	asset := domain.Asset{
		ID:         store.NewID("asset"),
		ProjectID:  projectID,
		Type:       assetType,
		Name:       name,
		SizeBytes:  info.Size(),
		SHA256:     fmt.Sprintf("%x", hash.Sum(nil)),
		Meta:       meta,
		Status:     domain.AssetStatusReady,
		ImportedAt: now,
	}
	if err := e.st.CreateAsset(ctx, asset); err != nil {
		e.t.Fatalf("CreateAsset: %v", err)
	}
	if err := e.st.AddAssetLocation(ctx, domain.AssetLocation{
		ID:        store.NewID("loc"),
		AssetID:   asset.ID,
		Path:      path,
		Source:    domain.LocationSourceExternalReference,
		Owner:     domain.LocationOwnerUser,
		Available: true,
		CreatedAt: now,
	}); err != nil {
		e.t.Fatalf("AddAssetLocation: %v", err)
	}
	built, err := e.st.GetAsset(ctx, asset.ID)
	if err != nil {
		e.t.Fatalf("GetAsset: %v", err)
	}
	return built
}

func (e *env) seedExportTrack(projectID, language string, segments []domain.Segment) domain.SubtitleTrack {
	e.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	revisionID := store.NewID("srev")
	track := domain.SubtitleTrack{
		ID:                store.NewID("track"),
		ProjectID:         projectID,
		Language:          language,
		Type:              domain.TrackTypeTranslation,
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
		e.t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	built, err := e.st.GetSubtitleTrack(ctx, track.ID)
	if err != nil {
		e.t.Fatalf("GetSubtitleTrack: %v", err)
	}
	return built
}

func (e *env) seedExportSelection(projectID, segmentID, language, audioAssetID string) {
	e.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	candidate := domain.AudioCandidate{
		ID:           store.NewID("cand"),
		ProjectID:    projectID,
		SegmentID:    segmentID,
		Language:     language,
		Revision:     1,
		ModelVersion: "tts-integration",
		AudioAssetID: audioAssetID,
		CreatedAt:    now,
	}
	if err := e.st.CreateAudioCandidate(ctx, candidate); err != nil {
		e.t.Fatalf("CreateAudioCandidate: %v", err)
	}
	if err := e.st.SaveCandidateSelection(ctx, domain.CandidateSelection{
		SegmentID:   segmentID,
		Language:    language,
		CandidateID: candidate.ID,
		Revision:    1,
		SelectedAt:  now,
	}); err != nil {
		e.t.Fatalf("SaveCandidateSelection: %v", err)
	}
}

func (e *env) runExport(t *testing.T, req service.ExportRequest, timeout time.Duration) domain.ExportRecord {
	t.Helper()
	ctx := context.Background()
	resp, err := e.svc.CreateExport(ctx, service.Idem{}, req)
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	record := resp.Body.(domain.ExportRecord)
	deadline := time.Now().Add(timeout)
	for {
		current, err := e.st.GetExport(ctx, record.ID)
		if err != nil {
			t.Fatalf("GetExport: %v", err)
		}
		switch current.Status {
		case domain.TaskStatusSucceeded, domain.TaskStatusFailed, domain.TaskStatusCanceled:
			return current
		}
		if time.Now().After(deadline) {
			t.Fatalf("export %s did not reach a terminal state (last=%s)", record.ID, current.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func readHeader(t *testing.T, path string) []byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()
	head := make([]byte, 32)
	n, _ := file.Read(head)
	return head[:n]
}

type ffprobeOutput struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	} `json:"format"`
}

func assertExportFile(t *testing.T, path string, format domain.ExportFormat, ffmpeg, ffprobe string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		t.Fatalf("export output %s missing/empty: %v", path, err)
	}
	head := readHeader(t, path)
	switch format {
	case domain.ExportFormatMP4H264:
		if !strings.Contains(string(head), "ftyp") {
			t.Fatalf("mp4 header = %q", head)
		}
	case domain.ExportFormatWAV:
		if len(head) < 12 || string(head[:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
			t.Fatalf("wav header = %q", head)
		}
	case domain.ExportFormatMP3:
		isID3 := len(head) >= 3 && string(head[:3]) == "ID3"
		isFrame := len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0
		if !isID3 && !isFrame {
			t.Fatalf("mp3 header = %q", head)
		}
	case domain.ExportFormatSRT, domain.ExportFormatASS:
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read subtitle export: %v", err)
		}
		text := string(data)
		if !utf8.Valid(data) || !strings.Contains(text, "第一句") {
			t.Fatalf("subtitle export content invalid: %q", text)
		}
		switch format {
		case domain.ExportFormatSRT:
			if !strings.Contains(text, "00:00:00,000 --> 00:00:00,900") {
				t.Fatalf("srt timings = %s", text)
			}
		case domain.ExportFormatASS:
			if !strings.HasPrefix(text, "[Script Info]") || !strings.Contains(text, "Dialogue: 0,0:00:00.00,0:00:00.90") {
				t.Fatalf("ass content = %s", text)
			}
		}
		return
	}
	// 真实解码校验：能完整解出流才算可解析产物。
	runMediaCommandStrict(t, "decode_export", ffmpeg, "-hide_banner", "-v", "error", "-i", path, "-f", "null", "-")
	if ffprobe == "" {
		t.Logf("INTEGRATION NOTE: FFPROBE_UNAVAILABLE；%s 使用容器头 + ffmpeg 全量解码校验（没有伪造 ffprobe 输出）",
			filepath.Base(path))
		return
	}
	output := runMediaCommandStrict(t, "ffprobe_export", ffprobe, "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", path)
	var parsed ffprobeOutput
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("ffprobe json: %v", err)
	}
	t.Logf("ffprobe %s: format=%s duration=%s streams=%+v",
		filepath.Base(path), parsed.Format.FormatName, parsed.Format.Duration, parsed.Streams)
	hasVideo, hasAudio := false, false
	for _, stream := range parsed.Streams {
		switch stream.CodecType {
		case "video":
			hasVideo = true
			if format == domain.ExportFormatMP4H264 && stream.CodecName != "h264" {
				t.Errorf("video codec = %s, want h264", stream.CodecName)
			}
		case "audio":
			hasAudio = true
			switch format {
			case domain.ExportFormatWAV:
				if stream.CodecName != "pcm_s16le" {
					t.Errorf("wav audio codec = %s", stream.CodecName)
				}
			case domain.ExportFormatMP3:
				if stream.CodecName != "mp3" {
					t.Errorf("mp3 audio codec = %s", stream.CodecName)
				}
			case domain.ExportFormatMP4H264:
				if stream.CodecName != "aac" {
					t.Errorf("mp4 audio codec = %s", stream.CodecName)
				}
			}
		}
	}
	if format == domain.ExportFormatMP4H264 && (!hasVideo || !hasAudio) {
		t.Fatalf("mp4 streams: video=%v audio=%v", hasVideo, hasAudio)
	}
	if (format == domain.ExportFormatWAV || format == domain.ExportFormatMP3) && !hasAudio {
		t.Fatalf("audio export has no audio stream: %+v", parsed.Streams)
	}
	duration, err := strconv.ParseFloat(parsed.Format.Duration, 64)
	if err != nil || duration < 1.5 || duration > 2.5 {
		t.Errorf("output duration = %q, want ~2s", parsed.Format.Duration)
	}
}

// TestExportExecutionIntegration 覆盖 Core → 调度 → Runner → 真实 FFmpeg 的导出闭环。
func TestExportExecutionIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("SKIP: -short 模式不运行真实 FFmpeg 集成测试")
	}
	ffmpeg := findExecutable("FFMPEG_BIN", "ffmpeg")
	if ffmpeg == "" {
		t.Skip("FFMPEG_NOT_AVAILABLE: 未找到 ffmpeg；真实 FFmpeg 集成测试跳过（不得视为通过）")
	}
	ffprobe := findExecutable("FFPROBE_BIN", "ffprobe")
	if ffprobe == "" {
		if os.Getenv("FFPROBE_REQUIRED") == "1" {
			t.Fatalf("FFPROBE_REQUIRED: 未找到 ffprobe（FFPROBE_REQUIRED=1 要求真实执行媒体探测断言）；请安装 ffmpeg（含 ffprobe），或设置 FFPROBE_BIN 指向 ffprobe 可执行文件")
		}
		t.Logf("FFPROBE_UNAVAILABLE: 未找到 ffprobe；仍会真实执行 ffmpeg 导出，媒体断言退回容器头 + 全量解码校验（不是 ffprobe 结果的替代）")
	}
	python := findExecutable("PYTHON_BIN", "python3")
	if python == "" {
		t.Skip("PYTHON_NOT_AVAILABLE: 未找到 python3；真实 Runner 集成测试跳过（不得视为通过）")
	}
	root := repoRoot(t)
	runnerRoot := filepath.Join(root, "runner")
	if _, err := os.Stat(filepath.Join(runnerRoot, "src", "yaomustudio_runner", "main.py")); err != nil {
		t.Fatalf("runner source missing under %s: %v", runnerRoot, err)
	}

	assetsDir := t.TempDir()
	videoPath := buildSampleVideo(t, ffmpeg, assetsDir)
	voiceA := buildVoiceClip(t, ffmpeg, assetsDir, "voice-a.wav", 660)
	voiceB := buildVoiceClip(t, ffmpeg, assetsDir, "voice-b.wav", 880)

	client := startRunnerClient(t, python, runnerRoot, ffmpeg, ffprobe)
	e := newEnv(t, envOptions{executor: &execadapter.Executor{Client: client}})
	startScheduler(t, e.sched, context.Background())

	project := e.seedProject()
	videoAsset := e.seedExportFileAsset(project, "source.mp4", domain.AssetTypeVideo, videoPath,
		&domain.MediaMeta{Container: "mp4", AudioTracks: 1, DurationMs: 2000})
	voiceAAsset := e.seedExportFileAsset(project, "voice-a.wav", domain.AssetTypeAudio, voiceA, nil)
	voiceBAsset := e.seedExportFileAsset(project, "voice-b.wav", domain.AssetTypeAudio, voiceB, nil)
	track := e.seedExportTrack(project, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "第一句"},
		{ID: "seg-2", StartMs: 1000, EndMs: 2000, Text: "第二句"},
	})
	e.seedExportSelection(project, "seg-1", "zh", voiceAAsset.ID)
	e.seedExportSelection(project, "seg-2", "zh", voiceBAsset.ID)
	run := e.seedRunWithInputs(project, videoAsset.ID)
	// 交付导出要求已放行的 delivery 门槛（绑定当前字幕 revision）。
	e.seedApprovedDelivery(run.ID, track)

	cases := []struct {
		name   string
		kind   domain.ExportKind
		format domain.ExportFormat
	}{
		{"video_mp4", domain.ExportKindVideo, domain.ExportFormatMP4H264},
		{"audio_wav", domain.ExportKindAudio, domain.ExportFormatWAV},
		{"audio_mp3", domain.ExportKindAudio, domain.ExportFormatMP3},
		{"subtitle_srt", domain.ExportKindSubtitle, domain.ExportFormatSRT},
		{"subtitle_ass", domain.ExportKindSubtitle, domain.ExportFormatASS},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := service.ExportRequest{
				ProjectID:      project,
				RunID:          run.ID,
				Kind:           tc.kind,
				Format:         tc.format,
				OutputDirGrant: t.TempDir(),
				NamingTemplate: "{project}-{language}",
			}
			if tc.kind == domain.ExportKindSubtitle {
				request.InputRevisionID = track.CurrentRevisionID
			}
			record := e.runExport(t, request, 120*time.Second)
			if record.Status != domain.TaskStatusSucceeded || record.OutputPath == "" {
				task, _ := e.st.GetTask(context.Background(), record.TaskID)
				t.Fatalf("export = %+v task = %+v", record, task)
			}
			assertExportFile(t, record.OutputPath, tc.format, ffmpeg, ffprobe)

			task, err := e.st.GetTask(context.Background(), record.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if task.Status != domain.TaskStatusSucceeded || task.Error != nil {
				t.Fatalf("task = %+v", task)
			}
			attempts, err := e.st.ListAttempts(context.Background(), record.TaskID)
			if err != nil || len(attempts) == 0 || attempts[len(attempts)-1].Status != domain.StepStatusSucceeded {
				t.Fatalf("attempts = %+v err=%v", attempts, err)
			}
			commits, err := e.st.ListArtifactCommits(context.Background(), attempts[len(attempts)-1].ID)
			if err != nil || len(commits) != 1 || len(commits[0].AssetIDs) == 0 {
				t.Fatalf("artifact commits = %+v err=%v", commits, err)
			}
			events, err := e.st.EventsAfter(context.Background(), 0, 1000)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range events {
				if event.Type == "export.succeeded" && event.TaskID == record.TaskID {
					found = true
				}
			}
			if !found {
				t.Fatalf("export.succeeded event missing for task %s", record.TaskID)
			}
		})
	}
}
