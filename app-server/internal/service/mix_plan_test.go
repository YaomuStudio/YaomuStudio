package service

// 08 混音计划（buildMixPlan）单元测试：白名单形状、绝对输入、相对输出、
// 时间轴/音量、字幕 staging、05 优先/等待、无 07 烧录、缺选择/过期、Meta 缺失、
// 纯音频与多语言命名。真实 FFmpeg 执行见 scheduler/mix_integration_test.go。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// mixPlanFixture 准备一个可构造 08 计划的最小环境。
type mixPlanFixture struct {
	env      *exportTestEnv
	ctx      context.Context
	project  domain.Project
	video    domain.Asset
	voiceA   domain.Asset
	voiceB   domain.Asset
	track    domain.SubtitleTrack
	run      domain.WorkflowRun
	revision *domain.SubtitleRevision
}

func newMixPlanFixture(t *testing.T) *mixPlanFixture {
	t.Helper()
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = StaticCapabilities{ExecutionFixtures: testExecutionFixtures()}
	ctx := context.Background()
	project := env.seedProject("混音计划")
	video := env.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		env.tempFile("source.mp4", "video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 1, DurationMs: 2000})
	voiceA := env.seedMediaAsset(project.ID, "voice-a.wav", domain.AssetTypeAudio,
		env.tempFile("voice-a.wav", "voice-a"), nil)
	voiceB := env.seedMediaAsset(project.ID, "voice-b.wav", domain.AssetTypeAudio,
		env.tempFile("voice-b.wav", "voice-b"), nil)
	track := env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "你好"},
		{ID: "seg-2", StartMs: 1000, EndMs: 1900, Text: "世界"},
	})
	env.seedSelection(project.ID, "seg-1", "zh", voiceA.ID)
	env.seedSelection(project.ID, "seg-2", "zh", voiceB.ID)
	run := env.seedRun(project.ID, video.ID)
	run.Config.Execution = testExecutionFixtures()
	return &mixPlanFixture{
		env: env, ctx: ctx, project: project, video: video,
		voiceA: voiceA, voiceB: voiceB, track: track, run: run,
		revision: currentRevision(track),
	}
}

// mixTemplateWithClone 是“含 07、不含 05”的模板：08 直接用 Run 输入视频，
// 与 custom 链路 [clone, mix] 的节点闭包一致（不引入擦除产物依赖）。
func mixTemplateWithClone() pipelineTemplate {
	return customPipelineTemplateFromFeatures([]string{featureClone, featureMix})
}

// seedCloneDependencyWork mirrors the successful 07 segment tasks that a real
// Run would have before 08 freezes its selection. Missing/stale selections are
// intentionally skipped so the planner still produces its readable failure.
func seedCloneDependencyWork(
	t *testing.T,
	ctx context.Context,
	env *exportTestEnv,
	tx *store.Tx,
	snap *pipelineSnapshot,
	revision domain.SubtitleRevision,
	language string,
) {
	t.Helper()
	work, err := env.svc.segmentWorkForRevision(ctx, tx, snap, revision, language)
	if err != nil {
		// Erase/translation prerequisites may intentionally be absent in the
		// test; the planner reports that failure on its own.
		return
	}
	for _, segment := range revision.Segments {
		selection, err := tx.GetCandidateSelection(ctx, segment.ID, language)
		if err != nil {
			continue
		}
		if selection.Revision != segment.ContentRevision {
			continue
		}
		candidates, err := tx.ListAudioCandidates(ctx, segment.ID, language, &selection.Revision)
		if err != nil {
			t.Fatalf("ListAudioCandidates: %v", err)
		}
		assetID := ""
		for _, candidate := range candidates {
			if candidate.ID == selection.CandidateID {
				assetID = candidate.AudioAssetID
				break
			}
		}
		if assetID == "" {
			continue
		}
		snap.tasks = append(snap.tasks, domain.Task{
			ID: "clone-" + segment.ID, Kind: domain.TaskKindSegment, NodeKey: domain.NodeKeyClone,
			SegmentID: segment.ID, Language: language, Status: domain.TaskStatusSucceeded,
			ArtifactIDs: []domain.ID{assetID},
			Params:      map[string]any{"_core": map[string]any{"segment_cache_key": work[segment.ID].CacheKey}},
		})
	}
}

func applyMixRunTemplate(run *domain.WorkflowRun, tpl pipelineTemplate) {
	run.Template = tpl.ID
	run.Nodes = nil
	for _, key := range tpl.Nodes {
		run.Nodes = append(run.Nodes, domain.PipelineNodeState{Key: key})
	}
}

func (f *mixPlanFixture) plan(
	t *testing.T, tpl pipelineTemplate, language string, revision *domain.SubtitleRevision,
	tasks []domain.Task,
) (frozenMixPlan, error) {
	t.Helper()
	run := f.run
	applyMixRunTemplate(&run, tpl)
	snap := &pipelineSnapshot{run: &run, tasks: tasks, taskByID: map[domain.ID]domain.Task{}}
	var plan frozenMixPlan
	err := f.env.store.DoTx(f.ctx, func(tx *store.Tx) error {
		if tpl.Has(domain.NodeKeyClone) {
			seedCloneDependencyWork(t, f.ctx, f.env, tx, snap, *revision, language)
		}
		built, err := f.env.svc.freezeMixPlan(f.ctx, tx, snap, tpl, *revision, language)
		plan = built
		return err
	})
	return plan, err
}

func audioInputEntries(t *testing.T, plan map[string]any, key string) []map[string]any {
	t.Helper()
	raw, ok := plan[key].([]any)
	if !ok {
		t.Fatalf("plan[%s] = %T, want array: %+v", key, plan[key], plan)
	}
	entries := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("plan[%s] item = %T, want object", key, item)
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestBuildMixPlanVideoMerge(t *testing.T) {
	f := newMixPlanFixture(t)
	built, err := f.plan(t, mixTemplateWithClone(), "zh", f.revision, nil)
	if err != nil {
		t.Fatalf("freezeMixPlan: %v", err)
	}
	plan := built.Params
	assertWhitelistedPlan(t, plan)
	if plan["op"] != "video_merge" {
		t.Fatalf("op = %v", plan["op"])
	}
	if plan["out"] != "preview.mp4" {
		t.Fatalf("out = %v", plan["out"])
	}
	if plan["video_src"] != f.video.Path {
		t.Fatalf("video_src = %v, want %s", plan["video_src"], f.video.Path)
	}
	if _, ok := plan["language"]; ok {
		t.Fatalf("08 plan must not carry a language key: %+v", plan)
	}
	if plan["video_codec"] != "h264" || plan["audio_codec"] != "aac" ||
		plan["crf"] != 20 || plan["preset"] != "medium" ||
		plan["audio_bitrate_kbps"] != exportAudioBitrate || plan["sample_rate"] != exportSampleRate {
		t.Fatalf("encoding params = %+v", plan)
	}
	inputs := audioInputEntries(t, plan, "audio_inputs")
	if len(inputs) != 3 {
		t.Fatalf("audio_inputs = %d, want 2 voices + 1 ambient: %+v", len(inputs), inputs)
	}
	for i, want := range []struct {
		path    string
		volume  float64
		startMs int64
	}{
		{f.voiceA.Path, 1.0, 0},
		{f.voiceB.Path, 1.0, 1000},
		{f.video.Path, 0.3, 0},
	} {
		entry := inputs[i]
		if entry["path"] != want.path || entry["volume"] != want.volume ||
			entry["start_ms"] != want.startMs {
			t.Fatalf("audio_inputs[%d] = %+v, want %+v", i, entry, want)
		}
	}
	subtitlePath, _ := plan["subtitle"].(string)
	if subtitlePath != stagedSubtitlePlaceholder {
		t.Fatalf("subtitle path = %q, want frozen placeholder %q", subtitlePath, stagedSubtitlePlaceholder)
	}
	if _, ok := plan["style"]; !ok {
		t.Fatalf("video_merge plan with subtitles must include style: %+v", plan)
	}
}

func TestBuildMixPlanWithoutCloneBurnsSubtitles(t *testing.T) {
	f := newMixPlanFixture(t)
	// 无 07 的专属链路：只合成译文与视频，保留原声。
	tpl := customPipelineTemplateFromFeatures([]string{featureTranslate, featureMix})
	built, err := f.plan(t, tpl, "zh", f.revision, nil)
	if err != nil {
		t.Fatalf("freezeMixPlan: %v", err)
	}
	plan := built.Params
	assertWhitelistedPlan(t, plan)
	if plan["op"] != "burn_subtitles" {
		t.Fatalf("op = %v", plan["op"])
	}
	if plan["input"] != f.video.Path || plan["out"] != "preview.mp4" {
		t.Fatalf("plan = %+v", plan)
	}
	if _, ok := plan["audio_inputs"]; ok {
		t.Fatalf("burn_subtitles must not carry audio_inputs: %+v", plan)
	}
	if _, ok := plan["sample_rate"]; ok {
		t.Fatalf("burn_subtitles whitelist has no sample_rate: %+v", plan)
	}
	subtitlePath, _ := plan["subtitle"].(string)
	if subtitlePath != stagedSubtitlePlaceholder {
		t.Fatalf("burn_subtitles plan subtitle = %q, want placeholder", subtitlePath)
	}
	if _, ok := plan["style"]; !ok {
		t.Fatalf("burn_subtitles plan has no style: %+v", plan)
	}
}

func TestBuildMixPlanWithoutCloneRequiresSubtitles(t *testing.T) {
	f := newMixPlanFixture(t)
	emptyTrack := f.env.seedTrack(f.project.ID, "ja", []domain.Segment{
		{ID: "seg-blank", StartMs: 0, EndMs: 900, Text: "   "},
	})
	tpl := customPipelineTemplateFromFeatures([]string{featureTranslate, featureMix})
	_, err := f.plan(t, tpl, "ja", currentRevision(emptyTrack), nil)
	if err == nil {
		t.Fatal("empty translated subtitles without clone must fail 08")
	}
	if !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("error = %v, want INVALID_REQUEST", err)
	}
}

func TestBuildMixPlanPrefersEraseArtifact(t *testing.T) {
	f := newMixPlanFixture(t)
	clean := f.env.seedMediaAsset(f.project.ID, "clean.mp4", domain.AssetTypeVideo,
		f.env.tempFile("clean.mp4", "clean-video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 0, DurationMs: 2000})
	stamp := time.Now().UTC()
	tasks := []domain.Task{{
		ID: "task-05", ProjectID: f.project.ID, RunID: f.run.ID,
		Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyErase,
		Status: domain.TaskStatusSucceeded, ArtifactIDs: []domain.ID{clean.ID},
		CreatedAt: stamp, UpdatedAt: stamp,
	}}
	f.run.Config.ReuseAudioTrackID = f.voiceA.ID
	built, err := f.plan(t, fullPipelineTemplate(), "zh", f.revision, tasks)
	if err != nil {
		t.Fatalf("freezeMixPlan: %v", err)
	}
	plan := built.Params
	if plan["op"] != "video_merge" || plan["video_src"] != clean.Path {
		t.Fatalf("plan must use the erase artifact, got %+v", plan)
	}
	// Meta.AudioTracks=0：不混入环境音，避免把无音轨视频当音频输入。
	inputs := audioInputEntries(t, plan, "audio_inputs")
	if len(inputs) != 2 {
		t.Fatalf("audio_inputs = %d, want only the two dubbed clips: %+v", len(inputs), inputs)
	}
}

func TestBuildMixPlanWaitsForEraseArtifact(t *testing.T) {
	f := newMixPlanFixture(t)
	cases := []struct {
		name  string
		tasks []domain.Task
	}{
		{"erase not run", nil},
		{"erase running", []domain.Task{{
			ID: "task-05", ProjectID: f.project.ID, RunID: f.run.ID,
			Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyErase,
			Status: domain.TaskStatusRunning, CreatedAt: time.Now().UTC(),
		}}},
		{"erase succeeded without video", []domain.Task{{
			ID: "task-05", ProjectID: f.project.ID, RunID: f.run.ID,
			Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyErase,
			Status: domain.TaskStatusSucceeded, ArtifactIDs: []domain.ID{"asset-missing"},
			CreatedAt: time.Now().UTC(),
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.plan(t, fullPipelineTemplate(), "zh", f.revision, tc.tasks)
			if !errors.Is(err, errMixPlanNotReady) {
				t.Fatalf("error = %v, want errMixPlanNotReady (must not fall back to the original video)", err)
			}
		})
	}
}

func TestBuildMixPlanMissingSelectionFails(t *testing.T) {
	f := newMixPlanFixture(t)
	track := f.env.seedTrack(f.project.ID, "ja", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "一"},
		{ID: "seg-2", StartMs: 1000, EndMs: 1900, Text: "二"},
	})
	// 只为第一句建立选择；第二句缺失必须失败，不能跳过或插静音。
	f.env.seedSelection(f.project.ID, "seg-1", "ja", f.voiceA.ID)
	_, err := f.plan(t, mixTemplateWithClone(), "ja", currentRevision(track), nil)
	if err == nil {
		t.Fatal("missing sentence selection must fail 08")
	}
	if !errors.Is(err, domain.ErrInvalidRequest) || errors.Is(err, errMixPlanNotReady) {
		t.Fatalf("error = %v, want readable INVALID_REQUEST", err)
	}
	if !strings.Contains(err.Error(), "seg-2") {
		t.Fatalf("error must name the missing segment: %v", err)
	}
}

func TestBuildMixPlanStaleSelectionFails(t *testing.T) {
	f := newMixPlanFixture(t)
	track := f.env.seedTrack(f.project.ID, "ja", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "一", ContentRevision: 2},
	})
	// 选择仍绑定 revision 1；句段已到 revision 2。
	f.env.seedSelection(f.project.ID, "seg-1", "ja", f.voiceA.ID)
	_, err := f.plan(t, mixTemplateWithClone(), "ja", currentRevision(track), nil)
	if err == nil {
		t.Fatal("stale selection must fail 08")
	}
	if !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("error = %v, want INVALID_REQUEST", err)
	}
}

func TestBuildMixPlanMetaNilOmitsAmbient(t *testing.T) {
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = StaticCapabilities{ExecutionFixtures: testExecutionFixtures()}
	ctx := context.Background()
	project := env.seedProject("静音源")
	video := env.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		env.tempFile("source.mp4", "video-bytes"), nil)
	voice := env.seedMediaAsset(project.ID, "voice.wav", domain.AssetTypeAudio,
		env.tempFile("voice.wav", "voice"), nil)
	track := env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "你好"},
	})
	env.seedSelection(project.ID, "seg-1", "zh", voice.ID)
	run := env.seedRun(project.ID, video.ID)
	run.Config.Execution = testExecutionFixtures()
	applyMixRunTemplate(&run, mixTemplateWithClone())
	snap := &pipelineSnapshot{run: &run}
	var plan map[string]any
	if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
		seedCloneDependencyWork(t, ctx, env, tx, snap, *currentRevision(track), "zh")
		built, err := env.svc.freezeMixPlan(ctx, tx, snap, mixTemplateWithClone(), *currentRevision(track), "zh")
		plan = built.Params
		return err
	}); err != nil {
		t.Fatalf("freezeMixPlan: %v", err)
	}
	inputs := audioInputEntries(t, plan, "audio_inputs")
	if len(inputs) != 1 || inputs[0]["path"] != voice.Path {
		t.Fatalf("Meta=nil must omit ambient and keep only the dubbed clip: %+v", inputs)
	}
}

func TestBuildMixPlanPureAudioInputFails(t *testing.T) {
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = StaticCapabilities{ExecutionFixtures: testExecutionFixtures()}
	ctx := context.Background()
	project := env.seedProject("纯音频")
	audio := env.seedMediaAsset(project.ID, "voice.wav", domain.AssetTypeAudio,
		env.tempFile("voice.wav", "voice"), nil)
	track := env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "你好"},
	})
	env.seedSelection(project.ID, "seg-1", "zh", audio.ID)
	run := env.seedRun(project.ID, audio.ID)
	run.Config.Execution = testExecutionFixtures()
	applyMixRunTemplate(&run, mixTemplateWithClone())
	snap := &pipelineSnapshot{run: &run}
	err := env.store.DoTx(ctx, func(tx *store.Tx) error {
		_, built := env.svc.freezeMixPlan(ctx, tx, snap, mixTemplateWithClone(), *currentRevision(track), "zh")
		return built
	})
	if err == nil || !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("audio-only run must fail 08 with INVALID_REQUEST, got %v", err)
	}
	if !strings.Contains(err.Error(), "video input") {
		t.Fatalf("error must explain the video requirement: %v", err)
	}
}

func TestBuildMixPlanRejectsTooManyVoiceInputs(t *testing.T) {
	f := newMixPlanFixture(t)
	// 视频 Meta 报告有音轨，所以最终 audio_inputs = 32 句配音 + 1 环境音。
	segments := make([]domain.Segment, 0, mixMaxAudioInputs)
	for i := 0; i < mixMaxAudioInputs; i++ {
		id := fmt.Sprintf("seg-many-%d", i)
		segments = append(segments, domain.Segment{
			ID: id, StartMs: int64(i * 100), EndMs: int64(i*100 + 90), Text: "句",
		})
		f.env.seedSelection(f.project.ID, id, "ja", f.voiceA.ID)
	}
	track := f.env.seedTrack(f.project.ID, "ja", segments)
	_, err := f.plan(t, mixTemplateWithClone(), "ja", currentRevision(track), nil)
	if err == nil || !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("plan above the runner audio-input ceiling must fail readably, got %v", err)
	}
	if !strings.Contains(err.Error(), "at most 32") {
		t.Fatalf("error must explain the runner ceiling: %v", err)
	}
}

func TestEnsureMixPlanFailureClassifiesStagingError(t *testing.T) {
	f := newMixPlanFixture(t)
	snap := &pipelineSnapshot{run: &f.run, taskByID: map[domain.ID]domain.Task{}}
	var events []domain.DomainEvent
	if err := f.env.store.DoTx(f.ctx, func(tx *store.Tx) error {
		return f.env.svc.ensureMixPlanFailure(f.ctx, tx, snap, "zh", f.revision,
			fmt.Errorf("%w: disk full", errMixPlanUnavailable), &events)
	}); err != nil {
		t.Fatalf("ensureMixPlanFailure: %v", err)
	}
	tasks, err := f.env.store.ListRunTasks(f.ctx, f.run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	var mixTask domain.Task
	for _, task := range tasks {
		if task.NodeKey == domain.NodeKeyMix {
			mixTask = task
		}
	}
	if mixTask.Error == nil || mixTask.Error.Code != domain.ErrorCodeResourceExhausted ||
		mixTask.Error.MessageKey != "task.mix_plan_unavailable" || mixTask.Error.Retryable {
		t.Fatalf("staging failure error = %+v, want non-retryable RESOURCE_EXHAUSTED", mixTask.Error)
	}
	if len(mixTask.ArtifactIDs) != 0 {
		t.Fatalf("failed 08 must not register artifacts: %v", mixTask.ArtifactIDs)
	}
}

func TestBuildMixPlanMissingSourceFileFails(t *testing.T) {
	f := newMixPlanFixture(t)
	missingPath := f.env.tempFile("missing.mp4", "video-bytes")
	missing := f.env.seedMediaAsset(f.project.ID, "missing.mp4", domain.AssetTypeVideo,
		missingPath,
		&domain.MediaMeta{Container: "mp4", AudioTracks: 0, DurationMs: 2000})
	if err := os.Remove(missingPath); err != nil {
		t.Fatalf("remove source: %v", err)
	}
	run := f.env.seedRun(f.project.ID, missing.ID)
	run.Config.Execution = testExecutionFixtures()
	applyMixRunTemplate(&run, mixTemplateWithClone())
	snap := &pipelineSnapshot{run: &run}
	err := f.env.store.DoTx(f.ctx, func(tx *store.Tx) error {
		_, built := f.env.svc.freezeMixPlan(f.ctx, tx, snap, mixTemplateWithClone(), *f.revision, "zh")
		return built
	})
	if err == nil || !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("missing source file must fail 08 with INVALID_REQUEST, got %v", err)
	}
	if !strings.Contains(err.Error(), "not accessible") {
		t.Fatalf("error must explain the unreadable source: %v", err)
	}
}

func TestBuildMixPlanMultiLanguageNaming(t *testing.T) {
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = StaticCapabilities{ExecutionFixtures: testExecutionFixtures()}
	ctx := context.Background()
	project := env.seedProject("多语混音")
	video := env.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		env.tempFile("source.mp4", "video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 0, DurationMs: 2000})
	voice := env.seedMediaAsset(project.ID, "voice.wav", domain.AssetTypeAudio,
		env.tempFile("voice.wav", "voice"), nil)
	zhTrack := env.seedTrack(project.ID, "zh-CN", []domain.Segment{
		{ID: "zh-1", StartMs: 0, EndMs: 900, Text: "中文"},
	})
	jaTrack := env.seedTrack(project.ID, "ja", []domain.Segment{
		{ID: "ja-1", StartMs: 0, EndMs: 900, Text: "日本語"},
	})
	env.seedSelection(project.ID, "zh-1", "zh-CN", voice.ID)
	env.seedSelection(project.ID, "ja-1", "ja", voice.ID)
	run := env.seedRunForLanguages(project.ID, []string{"zh-CN", "ja"}, video.ID)
	run.Config.Execution = testExecutionFixtures()
	applyMixRunTemplate(&run, mixTemplateWithClone())
	snap := &pipelineSnapshot{run: &run}

	build := func(language string, track domain.SubtitleTrack) frozenMixPlan {
		t.Helper()
		var plan frozenMixPlan
		if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
			seedCloneDependencyWork(t, ctx, env, tx, snap, *currentRevision(track), language)
			built, err := env.svc.freezeMixPlan(ctx, tx, snap, mixTemplateWithClone(), *currentRevision(track), language)
			plan = built
			return err
		}); err != nil {
			t.Fatalf("freezeMixPlan(%s): %v", language, err)
		}
		return plan
	}
	zhPlan := build("zh-CN", zhTrack)
	jaPlan := build("ja", jaTrack)
	if zhPlan.Params["out"] != "preview.mp4" || jaPlan.Params["out"] != "preview.mp4" {
		t.Fatalf("output names = %v / %v", zhPlan.Params["out"], jaPlan.Params["out"])
	}
	if zhPlan.Params["subtitle"] != stagedSubtitlePlaceholder || jaPlan.Params["subtitle"] != stagedSubtitlePlaceholder {
		t.Fatalf("per-language subtitle must use the frozen placeholder: %v / %v",
			zhPlan.Params["subtitle"], jaPlan.Params["subtitle"])
	}
	if zhPlan.Digest == "" || jaPlan.Digest == "" || zhPlan.Digest == jaPlan.Digest {
		t.Fatalf("per-language plan digests must differ: %q / %q", zhPlan.Digest, jaPlan.Digest)
	}
}

func TestAdvancePipelineRecordsReadableMixPlanFailure(t *testing.T) {
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = StaticCapabilities{ExecutionFixtures: testExecutionFixtures()}
	ctx := context.Background()
	env.svc.opts.PipelineOrchestration = true
	project := env.seedProject("08 计划失败")
	// 资产登记为视频但文件已不可读：预检只校验类型，08 计划必须可读失败。
	missingPath := env.tempFile("missing.mp4", "video-bytes")
	video := env.seedMediaAsset(project.ID, "missing.mp4", domain.AssetTypeVideo,
		missingPath,
		&domain.MediaMeta{Container: "mp4", AudioTracks: 0, DurationMs: 2000})
	if err := os.Remove(missingPath); err != nil {
		t.Fatalf("remove source: %v", err)
	}
	env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "你好"},
	})
	resp, err := env.svc.CreateWorkflowRun(ctx, Idem{}, CreateWorkflowRunInput{
		ProjectID:        string(project.ID),
		Name:             "08 计划失败",
		Template:         templateCustomPipeline,
		Features:         []string{featureTranslate, featureMix},
		SourceLanguage:   "en",
		TargetLanguages:  []string{"zh"},
		InputRevisionIDs: []string{string(video.ID)},
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := resp.Body.(domain.WorkflowRun)

	tasks, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	var mixTask domain.Task
	count := 0
	for _, task := range tasks {
		if task.NodeKey == domain.NodeKeyMix {
			mixTask = task
			count++
		}
	}
	if count != 1 {
		t.Fatalf("08 tasks = %d, want 1: %+v", count, tasks)
	}
	if mixTask.Status != domain.TaskStatusFailed {
		t.Fatalf("08 task status = %s, want FAILED", mixTask.Status)
	}
	if mixTask.Error == nil || mixTask.Error.Code != domain.ErrorCodeInvalidRequest ||
		mixTask.Error.MessageKey != "task.mix_plan_invalid" {
		t.Fatalf("08 error = %+v, want readable INVALID_REQUEST", mixTask.Error)
	}
	if len(mixTask.ArtifactIDs) != 0 {
		t.Fatalf("failed 08 must not register artifacts: %v", mixTask.ArtifactIDs)
	}
	if len(mixTask.Params) != 0 {
		t.Fatalf("failed 08 must not carry a fake execution plan: %+v", mixTask.Params)
	}
	gates, err := env.store.ListRunReviews(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunReviews: %v", err)
	}
	if len(gates) != 0 {
		t.Fatalf("failed 08 must not create a delivery gate: %+v", gates)
	}
	stored, err := env.store.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if stored.Status != domain.RunStatusPartial {
		t.Fatalf("run status = %s nodes=%+v, want PARTIAL", stored.Status, stored.Nodes)
	}
	for _, node := range stored.Nodes {
		if node.Key == domain.NodeKeyMix && node.Status != domain.NodeStatusFailed {
			t.Fatalf("08 node status = %s, want failed", node.Status)
		}
	}
}

// TestAdvancePipelineFailsMixWhenEraseTaskFailed 守护 05 终态失败的收敛：
// 最新 05 任务 FAILED 时不能无限等待擦除，08 必须登记可读失败并让 Run 到 PARTIAL。
func TestAdvancePipelineFailsMixWhenEraseTaskFailed(t *testing.T) {
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = StaticCapabilities{ExecutionFixtures: testExecutionFixtures()}
	ctx := context.Background()
	env.svc.opts.PipelineOrchestration = true
	project := env.seedProject("05 失败")
	video := env.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		env.tempFile("source.mp4", "video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 0, DurationMs: 2000})
	resp, err := env.svc.CreateWorkflowRun(ctx, Idem{}, CreateWorkflowRunInput{
		ProjectID:        string(project.ID),
		Name:             "05 失败",
		Template:         templateCustomPipeline,
		Features:         []string{featureErase, featureTranslate, featureMix},
		SourceLanguage:   "en",
		TargetLanguages:  []string{"zh"},
		InputRevisionIDs: []string{string(video.ID)},
		EraseMode:        domain.EraseModeDialogOnly,
		Extract: &domain.ExtractOptions{
			Mode:        domain.ExtractModeOCR,
			OCRLanguage: domain.OCRLanguageChinese,
		},
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := resp.Body.(domain.WorkflowRun)

	// 04 由创建推进自动建立；直接收敛为成功，排除未执行的 04 阻塞 Run 终态。
	tasks, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	var extract domain.Task
	for _, task := range tasks {
		if task.NodeKey == domain.NodeKeyExtract {
			extract = task
		}
	}
	if extract.ID == "" {
		t.Fatalf("04 task missing: %+v", tasks)
	}
	now := time.Now().UTC()
	running, err := env.store.UpdateTaskStatus(ctx, extract.ID, domain.TaskStatusRunning, extract.StateVersion, now)
	if err != nil {
		t.Fatalf("mark 04 running: %v", err)
	}
	if _, err := env.store.UpdateTaskResult(ctx, extract.ID, domain.TaskStatusSucceeded,
		nil, nil, running.StateVersion, now); err != nil {
		t.Fatalf("mark 04 succeeded: %v", err)
	}

	// 先让推进创建真实身份的 05 任务，再把它标记为失败；手工构造的任务身份
	// 不再匹配架构版的冻结执行摘要，会被推进当成新任务重新排队。
	var events []domain.DomainEvent
	if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
		return env.svc.advancePipeline(ctx, tx, run.ID, &events)
	}); err != nil {
		t.Fatalf("advancePipeline (queue 05): %v", err)
	}
	tasks, err = env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	var queuedErase domain.Task
	for _, task := range tasks {
		if task.Kind == domain.TaskKindNode && task.NodeKey == domain.NodeKeyErase &&
			task.Status == domain.TaskStatusQueued {
			queuedErase = task
		}
	}
	if queuedErase.ID == "" {
		t.Fatalf("queued 05 task missing: %+v", tasks)
	}
	now = time.Now().UTC()
	if _, err := env.store.UpdateTaskResult(ctx, queuedErase.ID, domain.TaskStatusFailed,
		nil, nil, queuedErase.StateVersion, now); err != nil {
		t.Fatalf("mark 05 failed: %v", err)
	}

	events = nil
	if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
		return env.svc.advancePipeline(ctx, tx, run.ID, &events)
	}); err != nil {
		t.Fatalf("advancePipeline must not treat a terminal 05 failure as retryable wait: %v", err)
	}

	tasks, err = env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	var mixTask domain.Task
	count := 0
	for _, task := range tasks {
		if task.NodeKey == domain.NodeKeyMix {
			mixTask = task
			count++
		}
	}
	if count != 1 {
		t.Fatalf("08 tasks = %d, want 1: %+v", count, tasks)
	}
	if mixTask.Status != domain.TaskStatusFailed {
		t.Fatalf("08 task status = %s, want FAILED (no infinite wait on failed 05)", mixTask.Status)
	}
	if mixTask.Error == nil || mixTask.Error.Code != domain.ErrorCodeInvalidRequest ||
		mixTask.Error.MessageKey != "task.mix_plan_invalid" {
		t.Fatalf("08 error = %+v, want readable INVALID_REQUEST", mixTask.Error)
	}
	if len(mixTask.ArtifactIDs) != 0 {
		t.Fatalf("failed 08 must not register artifacts: %v", mixTask.ArtifactIDs)
	}

	stored, err := env.store.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if stored.Status != domain.RunStatusPartial {
		t.Fatalf("run status = %s nodes=%+v, want PARTIAL", stored.Status, stored.Nodes)
	}
	for _, node := range stored.Nodes {
		switch node.Key {
		case domain.NodeKeyErase, domain.NodeKeyMix:
			if node.Status != domain.NodeStatusFailed {
				t.Fatalf("node %s status = %s, want failed", node.Key, node.Status)
			}
		}
	}
}
