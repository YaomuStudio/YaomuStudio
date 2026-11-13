package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/events"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/resultcommit"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

type exportTestEnv struct {
	t       *testing.T
	store   *store.Store
	bus     *events.Bus
	svc     *Service
	dataDir string
}

func newExportTestEnv(t *testing.T) *exportTestEnv {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dataDir, "yaomu.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logger, err := logging.New(logging.Options{Path: filepath.Join(t.TempDir(), "core.log")})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	bus := events.NewBus(st, logger)
	svc := New(Options{
		Store:        st,
		Bus:          bus,
		Log:          logger,
		WorkspaceID:  "ws-export",
		InstanceID:   "instance-export",
		ServerEpoch:  3,
		DataDir:      dataDir,
		Capabilities: StaticCapabilities{},
		Devices:      SystemDevices{},
	})
	if err := svc.EnsureStorageDirs(); err != nil {
		t.Fatalf("storage dirs: %v", err)
	}
	return &exportTestEnv{t: t, store: st, bus: bus, svc: svc, dataDir: dataDir}
}

func (e *exportTestEnv) seedProject(name string) domain.Project {
	e.t.Helper()
	now := time.Now().UTC()
	project := domain.Project{
		ID:            store.NewID("proj"),
		WorkspaceID:   "ws-export",
		Name:          name,
		SchemaVersion: 1,
		EditVersion:   1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := e.store.CreateProject(context.Background(), project); err != nil {
		e.t.Fatalf("CreateProject: %v", err)
	}
	return project
}

func (e *exportTestEnv) tempFile(name, content string) string {
	e.t.Helper()
	path := filepath.Join(e.t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func (e *exportTestEnv) seedMediaAsset(projectID, name string, assetType domain.AssetType, path string, meta *domain.MediaMeta) domain.Asset {
	e.t.Helper()
	now := time.Now().UTC()
	info, err := os.Stat(path)
	if err != nil {
		e.t.Fatalf("stat asset: %v", err)
	}
	digest, err := hashFile(path)
	if err != nil {
		e.t.Fatalf("hash asset: %v", err)
	}
	asset := domain.Asset{
		ID:         store.NewID("asset"),
		ProjectID:  projectID,
		Type:       assetType,
		Name:       name,
		SizeBytes:  info.Size(),
		SHA256:     digest,
		Meta:       meta,
		Status:     domain.AssetStatusReady,
		ImportedAt: now,
	}
	if err := e.store.CreateAsset(context.Background(), asset); err != nil {
		e.t.Fatalf("CreateAsset: %v", err)
	}
	if err := e.store.AddAssetLocation(context.Background(), domain.AssetLocation{
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
	built, err := e.store.GetAsset(context.Background(), asset.ID)
	if err != nil {
		e.t.Fatalf("GetAsset: %v", err)
	}
	return built
}

func (e *exportTestEnv) seedTrack(projectID, language string, segments []domain.Segment) domain.SubtitleTrack {
	return e.seedTrackWithType(projectID, language, domain.TrackTypeTranslation, segments, "")
}

// seedTrackWithType 创建指定类型/输入 revision 的字幕轨，供双语导出测试使用。
func (e *exportTestEnv) seedTrackWithType(
	projectID, language string,
	trackType domain.TrackType,
	segments []domain.Segment,
	inputRevisionID domain.ID,
) domain.SubtitleTrack {
	e.t.Helper()
	now := time.Now().UTC()
	revisionID := store.NewID("srev")
	track := domain.SubtitleTrack{
		ID:                store.NewID("track"),
		ProjectID:         projectID,
		Language:          language,
		Type:              trackType,
		CurrentRevisionID: revisionID,
		CreatedAt:         now,
		UpdatedAt:         now,
		Revisions: []domain.SubtitleRevision{{
			ID:              revisionID,
			AuthorType:      domain.AuthorTypeSystem,
			Segments:        segments,
			InputRevisionID: inputRevisionID,
			CreatedAt:       now,
		}},
	}
	if err := e.store.CreateSubtitleTrack(context.Background(), track); err != nil {
		e.t.Fatalf("CreateSubtitleTrack: %v", err)
	}
	built, err := e.store.GetSubtitleTrack(context.Background(), track.ID)
	if err != nil {
		e.t.Fatalf("GetSubtitleTrack: %v", err)
	}
	return built
}

func (e *exportTestEnv) seedSelection(projectID, segmentID, language, audioAssetID string) {
	e.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	candidate := domain.AudioCandidate{
		ID:           store.NewID("cand"),
		ProjectID:    projectID,
		SegmentID:    segmentID,
		Language:     language,
		Revision:     1,
		ModelVersion: "tts-test",
		AudioAssetID: audioAssetID,
		CreatedAt:    now,
	}
	if err := e.store.CreateAudioCandidate(ctx, candidate); err != nil {
		e.t.Fatalf("CreateAudioCandidate: %v", err)
	}
	if err := e.store.SaveCandidateSelection(ctx, domain.CandidateSelection{
		SegmentID:   segmentID,
		Language:    language,
		CandidateID: candidate.ID,
		Revision:    1,
		SelectedAt:  now,
	}); err != nil {
		e.t.Fatalf("SaveCandidateSelection: %v", err)
	}
}

func (e *exportTestEnv) seedRun(projectID string, inputAssetIDs ...string) domain.WorkflowRun {
	e.t.Helper()
	return e.seedRunForLanguages(projectID, []string{"zh"}, inputAssetIDs...)
}

func (e *exportTestEnv) seedRunForLanguages(projectID string, languages []string, inputAssetIDs ...string) domain.WorkflowRun {
	e.t.Helper()
	resp, err := e.svc.CreateWorkflowRun(context.Background(), Idem{}, CreateWorkflowRunInput{
		ProjectID:        projectID,
		Name:             "导出计划",
		SourceLanguage:   "en",
		TargetLanguages:  languages,
		InputRevisionIDs: inputAssetIDs,
		Extract:          &domain.ExtractOptions{Mode: domain.ExtractModeASR},
	})
	if err != nil {
		e.t.Fatalf("CreateWorkflowRun: %v", err)
	}
	return resp.Body.(domain.WorkflowRun)
}

// seedApprovedDelivery 登记并放行绑定 track 当前 revision 的 delivery 门槛。
//
// 交付导出门禁要求最新 delivery 门槛 approved 且绑定 Core 重算的当前修订；
// fixture 按与 Core 相同的口径（domain.RevisionBindingDigest）生成摘要。
func (e *exportTestEnv) seedApprovedDelivery(runID domain.ID, track domain.SubtitleTrack) domain.ReviewGate {
	e.t.Helper()
	ctx := context.Background()
	rev := currentRevision(track)
	if rev == nil {
		e.t.Fatalf("track %s has no current revision", track.ID)
	}
	digest, err := domain.RevisionBindingDigest(*rev)
	if err != nil {
		e.t.Fatalf("RevisionBindingDigest: %v", err)
	}
	bind := domain.DeliveryBind(digest)
	gate := domain.ReviewGate{
		ID:             store.NewID("review"),
		RunID:          runID,
		Type:           domain.ReviewTypeDelivery,
		RevisionDigest: bind,
		Status:         domain.ReviewStatusPending,
	}
	if err := e.store.CreateReviewGate(ctx, gate); err != nil {
		e.t.Fatalf("CreateReviewGate: %v", err)
	}
	approved, err := e.store.ApproveReview(ctx, gate.ID, bind, domain.ReviewTypeDelivery, time.Now().UTC())
	if err != nil {
		e.t.Fatalf("ApproveReview: %v", err)
	}
	return approved
}

// seedApprovedMix 创建冻结在 track 当前 revision 的成功 08 任务，并放行其交付门槛。
// 视频/音频导出必须绑定已审核的 08 产物；字幕 revision 级门槛不能替代。
func (e *exportTestEnv) seedApprovedMix(runID domain.ID, track domain.SubtitleTrack, language string) domain.Task {
	e.t.Helper()
	rev := currentRevision(track)
	if rev == nil {
		e.t.Fatalf("track %s has no current revision", track.ID)
	}
	task := seedMixTaskWithCore(e.t, e, track.ProjectID, runID, language,
		"asset-mix-"+string(rev.ID), *rev, time.Now().UTC())
	digest := mixDigest(e.t, task)
	gate := domain.ReviewGate{
		ID:             store.NewID("review"),
		RunID:          runID,
		Type:           domain.ReviewTypeDelivery,
		Language:       language,
		RevisionDigest: digest,
		Status:         domain.ReviewStatusPending,
	}
	if err := e.store.CreateReviewGate(context.Background(), gate); err != nil {
		e.t.Fatalf("CreateReviewGate: %v", err)
	}
	if _, err := e.store.ApproveReview(context.Background(), gate.ID, digest,
		domain.ReviewTypeDelivery, time.Now().UTC()); err != nil {
		e.t.Fatalf("ApproveReview: %v", err)
	}
	return task
}

func (e *exportTestEnv) mediaFixture() (domain.Project, domain.Asset, domain.Asset, domain.WorkflowRun, domain.SubtitleTrack) {
	e.t.Helper()
	project := e.seedProject("导出项目")
	video := e.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		e.tempFile("source.mp4", "video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 1, DurationMs: 2000})
	voice := e.seedMediaAsset(project.ID, "voice.wav", domain.AssetTypeAudio,
		e.tempFile("voice.wav", "voice-bytes"), nil)
	track := e.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 800, Text: "你好"},
		{ID: "seg-2", StartMs: 900, EndMs: 1800, Text: "世界"},
	})
	e.seedSelection(project.ID, "seg-1", "zh", voice.ID)
	run := e.seedRun(project.ID, video.ID)
	e.seedApprovedMix(run.ID, track, "zh")
	return project, video, voice, run, track
}

func planOf(t *testing.T, st *store.Store, taskID domain.ID) map[string]any {
	t.Helper()
	task, err := st.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	plan, ok := task.Params["plan"].(map[string]any)
	if !ok {
		t.Fatalf("task params has no plan: %+v", task.Params)
	}
	return plan
}

// runnerFfmpegParamKeys 是 runner ffmpeg.py execute 白名单字段（见
// runner/src/yaomustudio_runner/ffmpeg.py 的 *_PARAM_KEYS）；Core 计划不得越界。
var runnerFfmpegParamKeys = map[string]map[string]bool{
	"mix": {
		"op": true, "inputs": true, "out": true, "audio_codec": true,
		"bitrate_kbps": true, "sample_rate": true, "overwrite": true,
	},
	"video_merge": {
		"op": true, "video_src": true, "audio_inputs": true, "out": true,
		"subtitle": true, "style": true, "video_codec": true, "crf": true,
		"preset": true, "audio_codec": true, "audio_bitrate_kbps": true,
		"sample_rate": true, "overwrite": true,
	},
	"burn_subtitles": {
		"op": true, "input": true, "subtitle": true, "out": true,
		"style": true, "start_ms": true, "end_ms": true, "video_codec": true,
		"crf": true, "preset": true, "audio_codec": true,
		"audio_bitrate_kbps": true, "overwrite": true,
	},
}

var runnerStyleKeys = map[string]bool{
	"font_name": true, "font_size": true, "primary_colour": true,
	"outline_colour": true, "back_colour": true, "border_style": true,
	"outline": true, "shadow": true, "alignment": true,
	"margin_l": true, "margin_r": true, "margin_v": true,
}

func assertWhitelistedPlan(t *testing.T, plan map[string]any) {
	t.Helper()
	op, _ := plan["op"].(string)
	allowed, ok := runnerFfmpegParamKeys[op]
	if !ok {
		t.Fatalf("unexpected plan op %q", op)
	}
	for key := range plan {
		if !allowed[key] {
			t.Errorf("plan key %q is not in runner whitelist for %s", key, op)
		}
	}
	inputs, _ := plan["inputs"].([]any)
	if op == "video_merge" {
		inputs, _ = plan["audio_inputs"].([]any)
	}
	for _, item := range inputs {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("audio input %+v is not an object", item)
		}
		for key := range entry {
			switch key {
			case "path", "volume", "start_ms":
			default:
				t.Errorf("audio input key %q is not whitelisted", key)
			}
		}
		if path, _ := entry["path"].(string); !filepath.IsAbs(path) {
			t.Errorf("audio input path %q must be absolute", path)
		}
	}
	if styleValue, ok := plan["style"]; ok {
		style, _ := styleValue.(map[string]any)
		for key := range style {
			if !runnerStyleKeys[key] {
				t.Errorf("style key %q is not in runner whitelist", key)
			}
		}
	}
}

func TestExportMediaPlanMatchesRunnerWhitelist(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project, video, _, run, _ := env.mediaFixture()
	outDir := t.TempDir()

	cases := []struct {
		format     domain.ExportFormat
		kind       domain.ExportKind
		op         string
		outputName string
	}{
		{domain.ExportFormatMP4H264, domain.ExportKindVideo, "video_merge", "out.mp4"},
		{domain.ExportFormatWAV, domain.ExportKindAudio, "mix", "out.wav"},
		{domain.ExportFormatMP3, domain.ExportKindAudio, "mix", "out.mp3"},
	}
	for _, tc := range cases {
		t.Run(string(tc.format), func(t *testing.T) {
			resp, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
				ProjectID:      project.ID,
				RunID:          run.ID,
				Kind:           tc.kind,
				Format:         tc.format,
				OutputDirGrant: outDir,
				NamingTemplate: "{project}-{language}",
			})
			if err != nil {
				t.Fatalf("CreateExport: %v", err)
			}
			record := resp.Body.(domain.ExportRecord)
			if record.Status != domain.TaskStatusQueued || record.OutputPath != "" {
				t.Fatalf("queued export = %+v", record)
			}
			task, err := env.store.GetTask(ctx, record.TaskID)
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if task.Kind != domain.TaskKindExport || len(task.InputRevisionIDs) != 1 ||
				task.InputRevisionIDs[0] != video.ID {
				t.Fatalf("task = %+v", task)
			}
			plan := planOf(t, env.store, record.TaskID)
			assertWhitelistedPlan(t, plan)
			if plan["op"] != tc.op || plan["out"] != tc.outputName {
				t.Fatalf("plan = %+v", plan)
			}
			if tc.format == domain.ExportFormatMP4H264 && plan["video_src"] != video.Path {
				t.Errorf("video_src = %v, want %s", plan["video_src"], video.Path)
			}
			if task.Params["language"] != "zh" || task.Params["format"] != string(tc.format) {
				t.Errorf("task metadata = %+v", task.Params)
			}
			if tc.format == domain.ExportFormatMP4H264 {
				preparedParams, err := env.svc.PrepareTaskExecution(ctx, task)
				if err != nil {
					t.Fatalf("PrepareTaskExecution: %v", err)
				}
				preparedPlan := preparedParams["plan"].(map[string]any)
				subtitlePath, _ := preparedPlan["subtitle"].(string)
				if subtitlePath == "" {
					t.Fatalf("video plan has no materialized subtitle: %+v", preparedPlan)
				}
				data, err := os.ReadFile(subtitlePath)
				if err != nil {
					t.Fatalf("read staging subtitle: %v", err)
				}
				if !strings.Contains(string(data), "你好") || !strings.Contains(string(data), "00:00:00,900") {
					t.Fatalf("staging subtitle = %s", data)
				}
			}
		})
	}
}

func TestExportRequiresRunAndInputs(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("无输入项目")
	outDir := t.TempDir()
	if _, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID: project.ID, Kind: domain.ExportKindAudio, Format: domain.ExportFormatMP3,
		OutputDirGrant: outDir, NamingTemplate: "{project}",
	}); err == nil {
		t.Fatal("audio export without run must be rejected")
	}
	run := env.seedRun(project.ID)
	if _, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID: project.ID, RunID: run.ID, Kind: domain.ExportKindVideo, Format: domain.ExportFormatMP4H264,
		OutputDirGrant: outDir, NamingTemplate: "{project}",
	}); err == nil {
		t.Fatal("video export without a video input asset must be rejected")
	}
}

// TestDeliveryGateBindsMixNodeArtifacts 覆盖 08 交付门槛的摘要口径：
// Core 从最近一次成功的混音任务重算摘要，旧 approved 不能放行后到的产物。
func TestDeliveryGateBindsMixNodeArtifacts(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("混音门禁项目")
	video := env.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		env.tempFile("source.mp4", "video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 1, DurationMs: 2000})
	run := env.seedRun(project.ID, video.ID)
	outDir := t.TempDir()

	seedMix := func(offsetSeconds int, artifact string) domain.Task {
		t.Helper()
		stamp := time.Now().UTC().Add(time.Duration(offsetSeconds) * time.Second)
		task := domain.Task{
			ID: store.NewID("task"), ProjectID: project.ID, RunID: run.ID,
			Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyMix, Language: "zh",
			Status: domain.TaskStatusSucceeded, ArtifactIDs: []domain.ID{artifact},
			InputDigest: "mix-input-" + artifact, CreatedAt: stamp, UpdatedAt: stamp,
		}
		if err := env.store.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		return task
	}
	approveMix := func(task domain.Task) {
		t.Helper()
		digest, err := domain.NodeReviewDigest(task.RunID, task.NodeKey, task.Language,
			task.ID, task.ArtifactIDs, task.InputDigest)
		if err != nil {
			t.Fatalf("NodeReviewDigest: %v", err)
		}
		gate := domain.ReviewGate{
			ID: store.NewID("review"), RunID: run.ID, Type: domain.ReviewTypeDelivery,
			RevisionDigest: digest, Status: domain.ReviewStatusPending,
		}
		if err := env.store.CreateReviewGate(ctx, gate); err != nil {
			t.Fatalf("CreateReviewGate: %v", err)
		}
		if _, err := env.store.ApproveReview(ctx, gate.ID, digest, domain.ReviewTypeDelivery, time.Now().UTC()); err != nil {
			t.Fatalf("ApproveReview: %v", err)
		}
	}
	export := func() error {
		_, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
			ProjectID: project.ID, RunID: run.ID, Kind: domain.ExportKindAudio,
			Format: domain.ExportFormatMP3, OutputDirGrant: outDir, NamingTemplate: "{project}",
		})
		return err
	}

	first := seedMix(0, "asset-mix-1")
	approveMix(first)
	if err := export(); err != nil {
		t.Fatalf("approved mix export: %v", err)
	}

	// 08 重跑产出新产物：即使最新 delivery 门槛仍是旧的 approved，
	// 摘要不能匹配当前混音任务 → 409。
	second := seedMix(1, "asset-mix-2")
	if err := export(); err == nil {
		t.Fatal("export must be blocked after a newer mix output")
	} else if code, ok := domain.CodeOf(err); !ok || code != domain.ErrorCodeReviewRequired {
		t.Fatalf("stale mix approval error = %v", err)
	}
	approveMix(second)
	if err := export(); err != nil {
		t.Fatalf("approved newer mix export: %v", err)
	}
}

// TestRunDerivedExportRequiresApprovedDeliveryGate 覆盖交付审核门禁：
// 无门槛 / pending / 编辑后旧 approved 都返回 REVIEW_REQUIRED；
// 放行绑定当前修订的门槛后才能创建导出，且拒绝时不落盘。
func TestRunDerivedExportRequiresApprovedDeliveryGate(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("审核门禁项目")
	video := env.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		env.tempFile("source.mp4", "video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 1, DurationMs: 2000})
	track := env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "第一句"},
	})
	run := env.seedRun(project.ID, video.ID)
	outDir := t.TempDir()

	expectCode := func(err error, want domain.ErrorCode) {
		t.Helper()
		code, ok := domain.CodeOf(err)
		if !ok || code != want {
			t.Fatalf("error = %v, want %s", err, want)
		}
	}
	export := func() error {
		_, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
			ProjectID: project.ID, RunID: run.ID, Kind: domain.ExportKindAudio,
			Format: domain.ExportFormatMP3, OutputDirGrant: outDir, NamingTemplate: "{project}",
		})
		return err
	}

	// 无交付门槛 → 409，且没有导出记录或文件。
	expectCode(export(), domain.ErrorCodeReviewRequired)
	records, err := env.store.ListExports(ctx, project.ID)
	if err != nil || len(records) != 0 {
		t.Fatalf("exports after blocked create = %+v err=%v", records, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(outDir, "*")); len(leftovers) != 0 {
		t.Fatalf("blocked export left files: %v", leftovers)
	}

	// 字幕 revision 级交付绑定不能放行音频导出：音频交付必须绑定 08 产物。
	revision := currentRevision(track)
	if revision == nil {
		t.Fatal("track has no current revision")
	}
	revisionDigest, err := domain.RevisionBindingDigest(*revision)
	if err != nil {
		t.Fatalf("RevisionBindingDigest: %v", err)
	}
	revisionBind := domain.DeliveryBind(revisionDigest)
	revisionGate := domain.ReviewGate{
		ID: store.NewID("review"), RunID: run.ID, Type: domain.ReviewTypeDelivery,
		RevisionDigest: revisionBind, Status: domain.ReviewStatusPending,
	}
	if err := env.store.CreateReviewGate(ctx, revisionGate); err != nil {
		t.Fatalf("CreateReviewGate(revision): %v", err)
	}
	if _, err := env.store.ApproveReview(ctx, revisionGate.ID, revisionBind,
		domain.ReviewTypeDelivery, time.Now().UTC()); err != nil {
		t.Fatalf("ApproveReview(revision): %v", err)
	}
	expectCode(export(), domain.ErrorCodeReviewRequired)

	// pending 08 门槛不放行。
	mixTask := seedMixTaskWithCore(t, env, project.ID, run.ID, "zh", "asset-mix", *revision, time.Now().UTC())
	mixGateDigest := mixDigest(t, mixTask)
	mixGate := domain.ReviewGate{
		ID: store.NewID("review"), RunID: run.ID, Type: domain.ReviewTypeDelivery,
		RevisionDigest: mixGateDigest, Status: domain.ReviewStatusPending,
	}
	if err := env.store.CreateReviewGate(ctx, mixGate); err != nil {
		t.Fatalf("CreateReviewGate(mix): %v", err)
	}
	expectCode(export(), domain.ErrorCodeReviewRequired)

	// 放行当前 08 后可导出。
	if _, err := env.store.ApproveReview(ctx, mixGate.ID, mixGateDigest, domain.ReviewTypeDelivery, time.Now().UTC()); err != nil {
		t.Fatalf("ApproveReview: %v", err)
	}
	if err := export(); err != nil {
		t.Fatalf("approved export: %v", err)
	}

	// 编辑译文后旧 08 冻结在旧 revision，旧 approved 不再放行。
	if _, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, track.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: track.CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "seg-1", StartMs: 0, EndMs: 950, Text: "改过的第一句"},
		},
	}); err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	expectCode(export(), domain.ErrorCodeReviewRequired)
}

// TestSubtitleExportUsesRevisionDeliveryGate 覆盖字幕导出的独立交付口径：
// 不要求先合成 08；绑定被导出 revision 的 delivery 门槛即可放行，且历史
// revision 的旧放行仍可用于导出该历史版本（不代表当前 revision 已放行）。
func TestSubtitleExportUsesRevisionDeliveryGate(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("字幕交付门禁")
	video := env.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		env.tempFile("source.mp4", "video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 1, DurationMs: 2000})
	track := env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "第一句"},
	})
	run := env.seedRun(project.ID, video.ID)
	revision := currentRevision(track)
	if revision == nil {
		t.Fatal("track has no current revision")
	}
	export := func(inputRevisionID string) error {
		_, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
			ProjectID: project.ID, RunID: run.ID, Kind: domain.ExportKindSubtitle,
			Format: domain.ExportFormatSRT, InputRevisionID: inputRevisionID,
			OutputDirGrant: t.TempDir(), NamingTemplate: "{project}",
		})
		return err
	}
	expectReviewRequired := func(err error) {
		t.Helper()
		code, ok := domain.CodeOf(err)
		if !ok || code != domain.ErrorCodeReviewRequired {
			t.Fatalf("error = %v, want REVIEW_REQUIRED", err)
		}
	}

	expectReviewRequired(export(revision.ID))

	digest, err := domain.RevisionBindingDigest(*revision)
	if err != nil {
		t.Fatalf("RevisionBindingDigest: %v", err)
	}
	bind := domain.DeliveryBind(digest)
	gate := domain.ReviewGate{
		ID: store.NewID("review"), RunID: run.ID, Type: domain.ReviewTypeDelivery,
		RevisionDigest: bind, Status: domain.ReviewStatusPending,
	}
	if err := env.store.CreateReviewGate(ctx, gate); err != nil {
		t.Fatalf("CreateReviewGate: %v", err)
	}
	if _, err := env.store.ApproveReview(ctx, gate.ID, bind, domain.ReviewTypeDelivery, time.Now().UTC()); err != nil {
		t.Fatalf("ApproveReview: %v", err)
	}
	if err := export(revision.ID); err != nil {
		t.Fatalf("approved subtitle export: %v", err)
	}

	resp, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, track.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: track.CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "seg-1", StartMs: 0, EndMs: 950, Text: "改过的第一句"},
		},
	})
	if err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	edited := resp.Body.(domain.SubtitleRevision)
	expectReviewRequired(export(edited.ID))
	// 历史 revision 的放行仍可导出该历史版本。
	if err := export(revision.ID); err != nil {
		t.Fatalf("historical subtitle export: %v", err)
	}
}

// TestMediaExportAcceptsLegacyMixWithoutCoreBinding 覆盖无 `_core` 的旧 08：
// 升级前创建的成功 08 没有冻结 revision 绑定，按兼容语义视为匹配当前
// revision；放行其交付摘要后视频/音频导出仍可通过。
func TestMediaExportAcceptsLegacyMixWithoutCoreBinding(t *testing.T) {
	env, project, run, _ := newReviewBindingFixture(t, "zh")
	ctx := context.Background()
	legacy := seedSucceededMixTask(t, env, project.ID, run.ID, "zh", "asset-mix-legacy", time.Now().UTC())
	digest := mixDigest(t, legacy)
	gate := seedReviewGate(t, env, run.ID, domain.ReviewTypeDelivery, "zh", digest)
	if _, err := env.store.ApproveReview(ctx, gate.ID, digest, domain.ReviewTypeDelivery, time.Now().UTC()); err != nil {
		t.Fatalf("ApproveReview: %v", err)
	}
	if _, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID: project.ID, RunID: run.ID, Kind: domain.ExportKindVideo,
		Format: domain.ExportFormatMP4H264, OutputDirGrant: t.TempDir(), NamingTemplate: "{project}",
	}); err != nil {
		t.Fatalf("legacy 08 video export: %v", err)
	}
}

// TestExportSelectsReviewedLanguage 覆盖多语导出：缺省语种为 Run 第一个语种，
// 指定语种必须属于 Run 且该语种当前修订已放行；导出计划按指定语种取字幕。
func TestExportSelectsReviewedLanguage(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("多语项目")
	video := env.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		env.tempFile("source.mp4", "video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 1, DurationMs: 2000})
	zhTrack := env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 800, Text: "中文字幕"},
	})
	jaTrack := env.seedTrack(project.ID, "ja", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 800, Text: "日本語字幕"},
	})
	run := env.seedRunForLanguages(project.ID, []string{"zh", "ja"}, video.ID)
	// 最新交付审核只放行日语当前 revision。
	env.seedApprovedMix(run.ID, jaTrack, "ja")
	outDir := t.TempDir()

	export := func(language string) (domain.ExportRecord, error) {
		resp, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
			ProjectID: project.ID, RunID: run.ID, Language: language,
			Kind: domain.ExportKindVideo, Format: domain.ExportFormatMP4H264,
			OutputDirGrant: outDir, NamingTemplate: "{project}-{language}",
		})
		if err != nil {
			return domain.ExportRecord{}, err
		}
		return resp.Body.(domain.ExportRecord), nil
	}
	expectCode := func(err error, want domain.ErrorCode) {
		t.Helper()
		code, ok := domain.CodeOf(err)
		if !ok || code != want {
			t.Fatalf("error = %v, want %s", err, want)
		}
	}

	// 缺省语种是 Run 的第一个语种（zh）：未放行 → 409。
	if _, err := export(""); err == nil {
		t.Fatal("default language export without approval must be rejected")
	} else {
		expectCode(err, domain.ErrorCodeReviewRequired)
	}
	// 指定未放行的 zh → 409。
	if _, err := export("zh"); err == nil {
		t.Fatal("zh export without approval must be rejected")
	} else {
		expectCode(err, domain.ErrorCodeReviewRequired)
	}
	// 指定 Run 之外的语种 → 400。
	if _, err := export("fr"); err == nil {
		t.Fatal("language outside the run must be rejected")
	} else {
		expectCode(err, domain.ErrorCodeInvalidRequest)
	}

	record, err := export("ja")
	if err != nil {
		t.Fatalf("approved ja export: %v", err)
	}
	task, err := env.store.GetTask(ctx, record.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Params["language"] != "ja" {
		t.Fatalf("task language = %v", task.Params["language"])
	}
	plan, ok := task.Params["plan"].(map[string]any)
	if !ok {
		t.Fatalf("task params has no plan: %+v", task.Params)
	}
	preparedParams, err := env.svc.PrepareTaskExecution(ctx, task)
	if err != nil {
		t.Fatalf("PrepareTaskExecution: %v", err)
	}
	preparedPlan := preparedParams["plan"].(map[string]any)
	subtitlePath, _ := preparedPlan["subtitle"].(string)
	if subtitlePath == "" {
		t.Fatalf("ja video plan has no subtitle staging file: %+v", plan)
	}
	data, err := os.ReadFile(subtitlePath)
	if err != nil {
		t.Fatalf("read staging subtitle: %v", err)
	}
	if !strings.Contains(string(data), "日本語字幕") || strings.Contains(string(data), "中文字幕") {
		t.Fatalf("staging subtitle = %s", data)
	}

	// zh 轨道本身内容未变，但已放行的门槛绑定的是 ja 修订：不能拿它冒充 zh 审核。
	if _, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID: project.ID, RunID: run.ID, Language: "zh",
		Kind: domain.ExportKindSubtitle, Format: domain.ExportFormatSRT,
		InputRevisionID: zhTrack.CurrentRevisionID,
		OutputDirGrant:  outDir, NamingTemplate: "{project}-{language}",
	}); err == nil {
		t.Fatal("zh subtitle export must still require its own delivery approval")
	} else {
		expectCode(err, domain.ErrorCodeReviewRequired)
	}
}

// TestDeliveryApprovalsAreIndependentPerLanguage 覆盖多语放行互不覆盖：
// 先放行 zh 再放行 ja 后，zh 的已放行仍然有效（后放行的语种不能顶掉先前语种）；
// zh 编辑产生的 pending 门槛只阻断 zh，不阻断 ja。
func TestDeliveryApprovalsAreIndependentPerLanguage(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("多语放行项目")
	video := env.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		env.tempFile("source.mp4", "video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 1, DurationMs: 2000})
	zhTrack := env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 800, Text: "中文字幕"},
	})
	jaTrack := env.seedTrack(project.ID, "ja", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 800, Text: "日本語字幕"},
	})
	run := env.seedRunForLanguages(project.ID, []string{"zh", "ja"}, video.ID)
	outDir := t.TempDir()
	export := func(language string) error {
		_, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
			ProjectID: project.ID, RunID: run.ID, Language: language,
			Kind: domain.ExportKindVideo, Format: domain.ExportFormatMP4H264,
			OutputDirGrant: outDir, NamingTemplate: "{project}-{language}",
		})
		return err
	}
	expectCode := func(err error, want domain.ErrorCode) {
		t.Helper()
		code, ok := domain.CodeOf(err)
		if !ok || code != want {
			t.Fatalf("error = %v, want %s", err, want)
		}
	}

	// 先放行 zh，再放行 ja：两个语种都能导出。
	env.seedApprovedMix(run.ID, zhTrack, "zh")
	env.seedApprovedMix(run.ID, jaTrack, "ja")
	if err := export("zh"); err != nil {
		t.Fatalf("zh export after later ja approval: %v", err)
	}
	if err := export("ja"); err != nil {
		t.Fatalf("ja export: %v", err)
	}

	// zh 编辑生成绑定新修订的 pending 门槛：只阻断 zh，ja 仍按自己的放行导出。
	if _, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, zhTrack.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: zhTrack.CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "改过的中文"},
		},
	}); err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	expectCode(export("zh"), domain.ErrorCodeReviewRequired)
	if err := export("ja"); err != nil {
		t.Fatalf("ja export after zh edit: %v", err)
	}
}

// TestDeliveryGateScopesMixByLanguage 覆盖 08 混音摘要按语种取产物：
// 较新语种的混音（pending）既不能放行其他语种，也不能阻断其他语种已放行的导出。
func TestDeliveryGateScopesMixByLanguage(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("多语混音门禁项目")
	video := env.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		env.tempFile("source.mp4", "video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 1, DurationMs: 2000})
	run := env.seedRunForLanguages(project.ID, []string{"zh", "ja"}, video.ID)
	outDir := t.TempDir()

	seedMix := func(language, artifact string, offsetSeconds int) domain.Task {
		t.Helper()
		stamp := time.Now().UTC().Add(time.Duration(offsetSeconds) * time.Second)
		task := domain.Task{
			ID: store.NewID("task"), ProjectID: project.ID, RunID: run.ID,
			Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyMix, Language: language,
			Status: domain.TaskStatusSucceeded, ArtifactIDs: []domain.ID{artifact},
			InputDigest: "mix-input-" + artifact, CreatedAt: stamp, UpdatedAt: stamp,
		}
		if err := env.store.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		return task
	}
	createGate := func(task domain.Task) domain.ReviewGate {
		t.Helper()
		digest, err := domain.NodeReviewDigest(task.RunID, task.NodeKey, task.Language,
			task.ID, task.ArtifactIDs, task.InputDigest)
		if err != nil {
			t.Fatalf("NodeReviewDigest: %v", err)
		}
		gate := domain.ReviewGate{
			ID: store.NewID("review"), RunID: run.ID, Type: domain.ReviewTypeDelivery,
			RevisionDigest: digest, Status: domain.ReviewStatusPending,
		}
		if err := env.store.CreateReviewGate(ctx, gate); err != nil {
			t.Fatalf("CreateReviewGate: %v", err)
		}
		return gate
	}
	export := func(language string) error {
		_, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
			ProjectID: project.ID, RunID: run.ID, Language: language,
			Kind: domain.ExportKindAudio, Format: domain.ExportFormatMP3,
			OutputDirGrant: outDir, NamingTemplate: "{project}-{language}",
		})
		return err
	}

	// zh 混音已放行；ja 混音更晚且仍 pending。
	zhMix := seedMix("zh", "asset-mix-zh", 0)
	zhGate := createGate(zhMix)
	if _, err := env.store.ApproveReview(ctx, zhGate.ID, zhGate.RevisionDigest, domain.ReviewTypeDelivery, time.Now().UTC()); err != nil {
		t.Fatalf("ApproveReview: %v", err)
	}
	jaMix := seedMix("ja", "asset-mix-ja", 1)
	createGate(jaMix)

	if err := export("zh"); err != nil {
		t.Fatalf("zh export must use the approved zh mix, got %v", err)
	}
	if err := export("ja"); err == nil {
		t.Fatal("ja export must wait for its own mix approval")
	} else if code, ok := domain.CodeOf(err); !ok || code != domain.ErrorCodeReviewRequired {
		t.Fatalf("ja export error = %v", err)
	}
}

func TestSubtitleExportWritesSRTAndASS(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("字幕项目")
	track := env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 1000, Text: "第一句"},
		{ID: "seg-2", StartMs: 1200, EndMs: 2500, Text: "第二句"},
	})
	outDir := t.TempDir()

	for _, format := range []domain.ExportFormat{domain.ExportFormatSRT, domain.ExportFormatASS} {
		resp, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
			ProjectID:       project.ID,
			Kind:            domain.ExportKindSubtitle,
			Format:          format,
			InputRevisionID: track.CurrentRevisionID,
			OutputDirGrant:  outDir,
			NamingTemplate:  "{project}-{language}",
		})
		if err != nil {
			t.Fatalf("CreateExport(%s): %v", format, err)
		}
		record := resp.Body.(domain.ExportRecord)
		if record.Status != domain.TaskStatusSucceeded || record.OutputPath == "" {
			t.Fatalf("subtitle export(%s) = %+v", format, record)
		}
		data, err := os.ReadFile(record.OutputPath)
		if err != nil {
			t.Fatalf("read %s export: %v", format, err)
		}
		text := string(data)
		switch format {
		case domain.ExportFormatSRT:
			if !strings.Contains(text, "00:00:00,000 --> 00:00:01,000\n第一句") ||
				!strings.Contains(text, "00:00:01,200 --> 00:00:02,500\n第二句") {
				t.Fatalf("srt = %s", text)
			}
		case domain.ExportFormatASS:
			if !strings.HasPrefix(text, "[Script Info]") ||
				!strings.Contains(text, "Dialogue: 0,0:00:00.00,0:00:01.00") ||
				!strings.Contains(text, "第一句") {
				t.Fatalf("ass = %s", text)
			}
		}
		task, err := env.store.GetTask(ctx, record.TaskID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if task.Status != domain.TaskStatusSucceeded || len(task.ArtifactIDs) != 1 {
			t.Fatalf("task = %+v", task)
		}
		attempts, err := env.store.ListAttempts(ctx, record.TaskID)
		if err != nil || len(attempts) != 1 {
			t.Fatalf("attempts = %+v err=%v", attempts, err)
		}
		commits, err := env.store.ListArtifactCommits(ctx, attempts[0].ID)
		if err != nil || len(commits) != 1 || len(commits[0].AssetIDs) != 1 {
			t.Fatalf("commits = %+v err=%v", commits, err)
		}
	}
	// 临时文件必须清理：输出目录里只保留最终成品。
	leftovers, err := filepath.Glob(filepath.Join(outDir, ".yaomu-export-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v err=%v", leftovers, err)
	}
}

// TestSubtitleExportRejectsForeignRevision 覆盖项目隔离：不能用其他项目的
// revision ID 导出字幕。
func TestSubtitleExportRejectsForeignRevision(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	own := env.seedProject("自己的项目")
	other := env.seedProject("其他项目")
	foreign := env.seedTrack(other.ID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 500, Text: "机密"},
	})
	if _, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID:       own.ID,
		Kind:            domain.ExportKindSubtitle,
		Format:          domain.ExportFormatSRT,
		InputRevisionID: foreign.CurrentRevisionID,
		OutputDirGrant:  t.TempDir(),
		NamingTemplate:  "{project}",
	}); err == nil {
		t.Fatal("subtitle export with another project's revision must be rejected")
	}
}

func TestFinalizeExportLandsOutputAndCommits(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project, _, _, run, _ := env.mediaFixture()
	outDir := t.TempDir()
	resp, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID: project.ID, RunID: run.ID, Kind: domain.ExportKindAudio,
		Format: domain.ExportFormatMP3, OutputDirGrant: outDir, NamingTemplate: "{project}-{language}",
	})
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	record := resp.Body.(domain.ExportRecord)
	attemptDir := filepath.Join(env.dataDir, "work", record.TaskID, "attempt-1")
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := append([]byte("ID3"), make([]byte, 64)...)
	if err := os.WriteFile(filepath.Join(attemptDir, "out.mp3"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	task, err := env.store.GetTask(ctx, record.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	result := resultcommit.ExecutionResult{
		OutputDir: attemptDir,
		Outputs:   []resultcommit.ExecutionOutput{{RelPath: "out.mp3", SizeBytes: int64(len(payload))}},
	}
	if err := env.svc.OnTaskSucceeded(ctx, task, result); err != nil {
		t.Fatalf("OnTaskSucceeded: %v", err)
	}
	updated, err := env.store.GetExport(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.TaskStatusSucceeded || updated.OutputPath == "" {
		t.Fatalf("export after finalize = %+v", updated)
	}
	if filepath.Dir(updated.OutputPath) != outDir {
		t.Fatalf("output path %s is outside grant %s", updated.OutputPath, outDir)
	}
	landed, err := os.ReadFile(updated.OutputPath)
	if err != nil || string(landed) != string(payload) {
		t.Fatalf("landed output = %d bytes err=%v", len(landed), err)
	}
	attempts, err := env.store.ListAttempts(ctx, record.TaskID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %+v err=%v", attempts, err)
	}
	commits, err := env.store.ListArtifactCommits(ctx, attempts[0].ID)
	if err != nil || len(commits) != 1 || len(commits[0].AssetIDs) != 1 {
		t.Fatalf("commits = %+v err=%v", commits, err)
	}
	asset, err := env.store.GetAsset(ctx, commits[0].AssetIDs[0])
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	if asset.Type != domain.AssetTypeAudio || asset.Path != updated.OutputPath {
		t.Fatalf("export asset = %+v", asset)
	}
	// 再导出一次同名：不覆盖已有文件。
	if _, _, _, err := env.svc.landOutput(filepath.Join(attemptDir, "out.mp3"), outDir, "same", ".mp3"); err != nil {
		t.Fatalf("land same name: %v", err)
	}
	if _, _, _, err := env.svc.landOutput(filepath.Join(attemptDir, "out.mp3"), outDir, "same", ".mp3"); err != nil {
		t.Fatalf("land same name second: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(outDir, "same*.mp3"))
	if err != nil || len(matches) != 2 {
		t.Fatalf("same-name files = %v err=%v", matches, err)
	}
}

func TestFinalizeExportRejectsPathEscape(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project, _, _, run, _ := env.mediaFixture()
	outDir := t.TempDir()
	cases := []struct {
		name   string
		output func(attemptDir string, dataDir string, taskID string) resultcommit.ExecutionOutput
	}{
		{"dotdot", func(string, string, string) resultcommit.ExecutionOutput {
			return resultcommit.ExecutionOutput{RelPath: "../secret.mp3"}
		}},
		{"absolute", func(_ string, dataDir, taskID string) resultcommit.ExecutionOutput {
			return resultcommit.ExecutionOutput{RelPath: filepath.Join(dataDir, "work", taskID, "secret.mp3")}
		}},
		{"missing", func(string, string, string) resultcommit.ExecutionOutput {
			return resultcommit.ExecutionOutput{RelPath: "missing.mp3"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
				ProjectID: project.ID, RunID: run.ID, Kind: domain.ExportKindAudio,
				Format: domain.ExportFormatMP3, OutputDirGrant: outDir, NamingTemplate: "{project}",
			})
			if err != nil {
				t.Fatalf("CreateExport: %v", err)
			}
			record := resp.Body.(domain.ExportRecord)
			attemptDir := filepath.Join(env.dataDir, "work", record.TaskID, "attempt-1")
			if err := os.MkdirAll(attemptDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(env.dataDir, "work", record.TaskID, "secret.mp3"), []byte("ID3xxxx"), 0o600); err != nil {
				t.Fatal(err)
			}
			task, err := env.store.GetTask(ctx, record.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			result := resultcommit.ExecutionResult{
				OutputDir: attemptDir,
				Outputs:   []resultcommit.ExecutionOutput{tc.output(attemptDir, env.dataDir, record.TaskID)},
			}
			if err := env.svc.OnTaskSucceeded(ctx, task, result); err == nil {
				t.Fatalf("expected finalize rejection for %+v", result.Outputs)
			}
			current, err := env.store.GetExport(ctx, record.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Status != domain.TaskStatusFailed || current.OutputPath != "" {
				t.Fatalf("export after rejection = %+v", current)
			}
		})
	}
	events, err := env.store.EventsAfter(ctx, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	for _, event := range events {
		if event.Type == "export.failed" {
			failed++
		}
	}
	if failed < len(cases) {
		t.Fatalf("export.failed events = %d, want %d", failed, len(cases))
	}
}

// TestFinalizeExportRejectsSymlinkOutput 覆盖输出逃逸的符号链接变体：
// attempt 目录内的链接不能把用户目录之外的任意文件带回成成果。
func TestFinalizeExportRejectsSymlinkOutput(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project, _, _, run, _ := env.mediaFixture()
	outDir := t.TempDir()
	resp, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID: project.ID, RunID: run.ID, Kind: domain.ExportKindAudio,
		Format: domain.ExportFormatMP3, OutputDirGrant: outDir, NamingTemplate: "{project}",
	})
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	record := resp.Body.(domain.ExportRecord)
	attemptDir := filepath.Join(env.dataDir, "work", record.TaskID, "attempt-1")
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(env.dataDir, "outside.mp3")
	payload := append([]byte("ID3"), make([]byte, 64)...)
	if err := os.WriteFile(outside, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(attemptDir, "out.mp3")); err != nil {
		t.Skipf("symlink unavailable on this platform: %v", err)
	}
	task, err := env.store.GetTask(ctx, record.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	result := resultcommit.ExecutionResult{
		OutputDir: attemptDir,
		Outputs:   []resultcommit.ExecutionOutput{{RelPath: "out.mp3", SizeBytes: int64(len(payload))}},
	}
	if err := env.svc.OnTaskSucceeded(ctx, task, result); err == nil {
		t.Fatal("symlink output must be rejected")
	}
	current, err := env.store.GetExport(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != domain.TaskStatusFailed || current.OutputPath != "" {
		t.Fatalf("export after symlink rejection = %+v", current)
	}
	landed, err := filepath.Glob(filepath.Join(outDir, "*"))
	if err != nil || len(landed) != 0 {
		t.Fatalf("output dir must stay empty: %v err=%v", landed, err)
	}
}

// TestFinalizeExportRejectsOutputDirOutsideTask 覆盖输出目录归属校验：
// 执行结果只能指向本任务工作目录内的 attempt 目录。
func TestFinalizeExportRejectsOutputDirOutsideTask(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project, _, _, run, _ := env.mediaFixture()
	outDir := t.TempDir()
	resp, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID: project.ID, RunID: run.ID, Kind: domain.ExportKindAudio,
		Format: domain.ExportFormatMP3, OutputDirGrant: outDir, NamingTemplate: "{project}",
	})
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	record := resp.Body.(domain.ExportRecord)
	foreignDir := filepath.Join(env.dataDir, "work", "task-other", "attempt-1")
	if err := os.MkdirAll(foreignDir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := append([]byte("ID3"), make([]byte, 32)...)
	if err := os.WriteFile(filepath.Join(foreignDir, "out.mp3"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	task, err := env.store.GetTask(ctx, record.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	result := resultcommit.ExecutionResult{
		OutputDir: foreignDir,
		Outputs:   []resultcommit.ExecutionOutput{{RelPath: "out.mp3", SizeBytes: int64(len(payload))}},
	}
	if err := env.svc.OnTaskSucceeded(ctx, task, result); err == nil {
		t.Fatal("output dir outside the task work dir must be rejected")
	}
	current, err := env.store.GetExport(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != domain.TaskStatusFailed || current.OutputPath != "" {
		t.Fatalf("export after rejection = %+v", current)
	}
	if landed, _ := filepath.Glob(filepath.Join(outDir, "*")); len(landed) != 0 {
		t.Fatalf("output dir must stay empty: %v", landed)
	}
}

func TestRetryExportKeepsFrozenPlanAndIgnoresRunGate(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project, _, _, run, _ := env.mediaFixture()
	resp, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID: project.ID, RunID: run.ID, Kind: domain.ExportKindAudio,
		Format: domain.ExportFormatMP3, OutputDirGrant: t.TempDir(), NamingTemplate: "{project}",
	})
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	record := resp.Body.(domain.ExportRecord)
	task, err := env.store.GetTask(ctx, record.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟一次执行失败（不改变冻结输入）。
	if _, err := env.store.UpdateTaskResult(ctx, task.ID, domain.TaskStatusFailed, nil, nil, task.StateVersion, time.Now().UTC()); err != nil {
		t.Fatalf("UpdateTaskResult: %v", err)
	}
	// 来源 Run 终止不应阻止交付导出的重试。
	if _, err := env.store.DB().ExecContext(ctx,
		`UPDATE workflow_runs SET status = 'SUCCEEDED', state_version = state_version + 1 WHERE id = ?`,
		run.ID); err != nil {
		t.Fatal(err)
	}
	retried, err := env.svc.RetryTask(ctx, Idem{}, record.TaskID)
	if err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	updated := retried.Body.(domain.Task)
	if updated.Status != domain.TaskStatusQueued || updated.Steps[0].AttemptNo != 2 {
		t.Fatalf("retried task = %+v", updated)
	}
	attempts, err := env.store.ListAttempts(ctx, record.TaskID)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts = %+v err=%v", attempts, err)
	}
}

func TestCancelQueuedExportConvergesRecord(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project, _, _, run, _ := env.mediaFixture()
	resp, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID: project.ID, RunID: run.ID, Kind: domain.ExportKindAudio,
		Format: domain.ExportFormatMP3, OutputDirGrant: t.TempDir(), NamingTemplate: "{project}",
	})
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	record := resp.Body.(domain.ExportRecord)
	if _, err := env.svc.CancelTask(ctx, Idem{}, record.TaskID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	current, err := env.store.GetExport(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != domain.TaskStatusCanceled || current.OutputPath != "" {
		t.Fatalf("canceled export = %+v", current)
	}
}

func TestBilingualSubtitleExportWritesSourceAndTranslation(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("双语字幕")
	source := env.seedTrackWithType(project.ID, "zh", domain.TrackTypeTranscription, []domain.Segment{
		{ID: "src-1", StartMs: 0, EndMs: 1000, Text: "你好"},
		{ID: "src-2", StartMs: 1200, EndMs: 2500, Text: "世界"},
	}, "")
	translation := env.seedTrackWithType(project.ID, "en", domain.TrackTypeTranslation, []domain.Segment{
		{ID: "tr-1", StartMs: 0, EndMs: 1000, Text: "Hello", Lineage: []domain.ID{"src-1"}},
		{ID: "tr-2", StartMs: 1200, EndMs: 2500, Text: "World", Lineage: []domain.ID{"src-2"}},
	}, source.CurrentRevisionID)

	resp, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID:       project.ID,
		Kind:            domain.ExportKindSubtitle,
		Format:          domain.ExportFormatSRT,
		InputRevisionID: translation.CurrentRevisionID,
		Bilingual:       true,
		OutputDirGrant:  t.TempDir(),
		NamingTemplate:  "{project}-{language}",
	})
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	record := resp.Body.(domain.ExportRecord)
	if !record.Bilingual || record.SourceRevisionID != source.CurrentRevisionID {
		t.Fatalf("record bilingual/source = %+v", record)
	}
	data, err := os.ReadFile(record.OutputPath)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "你好\nHello") || !strings.Contains(text, "世界\nWorld") {
		t.Fatalf("bilingual srt = %s", text)
	}
}

func TestBilingualSubtitleExportFallsBackToUniqueSourceTrack(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("双语回退")
	env.seedTrackWithType(project.ID, "zh", domain.TrackTypeTranscription, []domain.Segment{
		{ID: "src-1", StartMs: 0, EndMs: 1000, Text: "原文"},
	}, "")
	translation := env.seedTrackWithType(project.ID, "en", domain.TrackTypeTranslation, []domain.Segment{
		{ID: "tr-1", StartMs: 0, EndMs: 1000, Text: "translated"},
	}, "")

	resp, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID:       project.ID,
		Kind:            domain.ExportKindSubtitle,
		Format:          domain.ExportFormatSRT,
		InputRevisionID: translation.CurrentRevisionID,
		Bilingual:       true,
		OutputDirGrant:  t.TempDir(),
		NamingTemplate:  "{project}",
	})
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	record := resp.Body.(domain.ExportRecord)
	data, err := os.ReadFile(record.OutputPath)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	if !strings.Contains(string(data), "原文\ntranslated") {
		t.Fatalf("fallback srt = %s", data)
	}
}

func TestBilingualSubtitleExportRejectsInvalidRequests(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("双语拒绝")
	env.seedTrackWithType(project.ID, "zh", domain.TrackTypeTranscription, []domain.Segment{
		{ID: "src-1", StartMs: 0, EndMs: 1000, Text: "一"},
	}, "")
	env.seedTrackWithType(project.ID, "ja", domain.TrackTypeTranscription, []domain.Segment{
		{ID: "src-2", StartMs: 0, EndMs: 1000, Text: "二"},
	}, "")
	translation := env.seedTrackWithType(project.ID, "en", domain.TrackTypeTranslation, []domain.Segment{
		{ID: "tr-1", StartMs: 0, EndMs: 1000, Text: "one"},
	}, "")

	// 没有 input_revision_id 且存在多条 transcription 轨：无法唯一确定原文。
	_, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID:       project.ID,
		Kind:            domain.ExportKindSubtitle,
		Format:          domain.ExportFormatSRT,
		InputRevisionID: translation.CurrentRevisionID,
		Bilingual:       true,
		OutputDirGrant:  t.TempDir(),
		NamingTemplate:  "{project}",
	})
	if code, ok := domain.CodeOf(err); !ok || code != domain.ErrorCodeInvalidRequest {
		t.Fatalf("ambiguous source error = %v, want INVALID_REQUEST", err)
	}

	// 非字幕导出不接受 bilingual。
	_, err = env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID:      project.ID,
		Kind:           domain.ExportKindVideo,
		Format:         domain.ExportFormatMP4H264,
		Bilingual:      true,
		OutputDirGrant: t.TempDir(),
		NamingTemplate: "{project}",
	})
	if code, ok := domain.CodeOf(err); !ok || code != domain.ErrorCodeInvalidRequest {
		t.Fatalf("video bilingual error = %v, want INVALID_REQUEST", err)
	}

	// 双语首版只支持 SRT。
	_, err = env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID:       project.ID,
		Kind:            domain.ExportKindSubtitle,
		Format:          domain.ExportFormatASS,
		InputRevisionID: translation.CurrentRevisionID,
		Bilingual:       true,
		OutputDirGrant:  t.TempDir(),
		NamingTemplate:  "{project}",
	})
	if code, ok := domain.CodeOf(err); !ok || code != domain.ErrorCodeInvalidRequest {
		t.Fatalf("ass bilingual error = %v, want INVALID_REQUEST", err)
	}
}

func TestRenderBilingualSRTFallsBackToTime(t *testing.T) {
	source := []domain.Segment{
		{ID: "src-1", StartMs: 0, EndMs: 1000, Text: "原文一"},
		{ID: "src-2", StartMs: 1200, EndMs: 2500, Text: "原文二"},
	}
	translation := []domain.Segment{
		{ID: "tr-1", StartMs: 0, EndMs: 1000, Text: "one"},
		{ID: "tr-2", StartMs: 1300, EndMs: 2400, Text: "two"},
	}
	text := renderBilingualSRT(translation, source)
	if !strings.Contains(text, "原文一\none") {
		t.Fatalf("exact-time pairing missing: %s", text)
	}
	if !strings.Contains(text, "原文二\ntwo") {
		t.Fatalf("overlap pairing missing: %s", text)
	}
}
