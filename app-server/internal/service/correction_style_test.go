package service

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// TestSaveSubtitleRevisionStyleEraseAndDigest 覆盖阶段二契约字段：
// style/erase_blocks 保存、Core 补块 ID/默认 source、revision_digest 派生，
// 以及新摘要参与当前 translation gate（译文轨使用裸 RevisionBindingDigest）。
func TestSaveSubtitleRevisionStyleEraseAndDigest(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("校正样式")
	video := env.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		env.tempFile("source.mp4", "video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 1, DurationMs: 2000})
	track := env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "你好"},
	})
	run := env.seedRun(project.ID, video.ID)

	resp, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, track.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: track.CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "你好，世界"},
		},
		Style: &domain.SubtitleStyle{X: 0.5, Y: 0.85, FontSize: 52},
		EraseBlocks: []domain.EraseBlock{{
			SegmentID: "seg-1", StartMs: 0, EndMs: 900,
			BBox: [4]int{10, 20, 110, 120},
		}},
	})
	if err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	saved := resp.Body.(domain.SubtitleRevision)
	if saved.Style == nil || saved.Style.FontSize != 52 {
		t.Fatalf("saved style = %+v", saved.Style)
	}
	if len(saved.EraseBlocks) != 1 || saved.EraseBlocks[0].ID == "" {
		t.Fatalf("saved erase blocks = %+v", saved.EraseBlocks)
	}
	if saved.EraseBlocks[0].Source != domain.EraseBlockSourceClean {
		t.Fatalf("default erase source = %q, want clean", saved.EraseBlocks[0].Source)
	}
	wantDigest, err := domain.RevisionBindingDigest(saved)
	if err != nil {
		t.Fatalf("RevisionBindingDigest: %v", err)
	}
	if saved.RevisionDigest != wantDigest {
		t.Fatalf("saved revision digest = %q, want %q", saved.RevisionDigest, wantDigest)
	}

	reviews, err := env.store.ListRunReviews(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunReviews: %v", err)
	}
	var translation *domain.ReviewGate
	for i := range reviews {
		if reviews[i].Type == domain.ReviewTypeTranslation &&
			reviews[i].Language == "zh" &&
			reviews[i].Status == domain.ReviewStatusPending {
			translation = &reviews[i]
		}
	}
	if translation == nil {
		t.Fatalf("no pending zh translation gate: %+v", reviews)
	}
	if translation.RevisionDigest != wantDigest {
		t.Fatalf("translation digest = %q, want bare revision digest %q",
			translation.RevisionDigest, wantDigest)
	}
	if translation.CreatedAt == nil {
		t.Fatal("new review gate has no created_at")
	}
}

// TestSaveSubtitleRevisionRejectsInvalidStyleAndErase 覆盖保存前校验：
// 非法样式枚举/擦除块几何不得写入新 revision。
func TestSaveSubtitleRevisionRejectsInvalidStyleAndErase(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("校正校验")
	track := env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "你好"},
	})
	base := track.CurrentRevisionID
	segments := []domain.SegmentInput{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "你好"},
	}

	if _, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, track.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: base,
		Segments:       segments,
		Style:          &domain.SubtitleStyle{Align: "justify"},
	}); err == nil {
		t.Fatal("invalid subtitle align accepted")
	}
	if _, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, track.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: base,
		Segments:       segments,
		EraseBlocks: []domain.EraseBlock{{
			StartMs: 0, EndMs: 900, BBox: [4]int{100, 20, 10, 120},
		}},
	}); err == nil {
		t.Fatal("invalid erase bbox accepted")
	}

	stored, err := env.store.GetSubtitleTrack(ctx, track.ID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}
	if stored.CurrentRevisionID != base || len(stored.Revisions) != 1 {
		t.Fatalf("rejected save wrote a revision: %+v", stored)
	}
}

// TestMixPlanPrefersRevisionStyle 覆盖 08 样式优先级：revision.style 优先于 RunConfig。
func TestMixPlanPrefersRevisionStyle(t *testing.T) {
	f := newMixPlanFixture(t)
	f.run.Config.SubtitleStyle = &domain.SubtitleStyle{FontSize: 40, Align: domain.SubtitleAlignLeft}

	styled := *f.revision
	styled.Style = &domain.SubtitleStyle{FontSize: 64, Align: domain.SubtitleAlignRight}
	built, err := f.plan(t, mixTemplateWithClone(), "zh", &styled, nil)
	if err != nil {
		t.Fatalf("freezeMixPlan(styled): %v", err)
	}
	style, ok := built.Params["style"].(map[string]any)
	if !ok {
		t.Fatalf("plan style = %T", built.Params["style"])
	}
	if style["font_size"] != 64 || style["alignment"] != 3 {
		t.Fatalf("revision style not applied: %+v", style)
	}

	built, err = f.plan(t, mixTemplateWithClone(), "zh", f.revision, nil)
	if err != nil {
		t.Fatalf("freezeMixPlan(run style): %v", err)
	}
	style, _ = built.Params["style"].(map[string]any)
	if style["font_size"] != 40 || style["alignment"] != 1 {
		t.Fatalf("run style fallback not applied: %+v", style)
	}
}

func TestEffectiveStyleFallsBackToRunConfig(t *testing.T) {
	run := &domain.WorkflowRun{Config: domain.RunConfig{
		SubtitleStyle: &domain.SubtitleStyle{FontSize: 40},
	}}
	if got := effectiveStyle(nil, run); got == nil || got.FontSize != 40 {
		t.Fatalf("nil revision style = %+v", got)
	}
	if got := effectiveStyle(&domain.SubtitleRevision{}, run); got == nil || got.FontSize != 40 {
		t.Fatalf("revision without style = %+v", got)
	}
	styled := &domain.SubtitleRevision{Style: &domain.SubtitleStyle{FontSize: 64}}
	if got := effectiveStyle(styled, run); got == nil || got.FontSize != 64 {
		t.Fatalf("revision style = %+v", got)
	}
	if got := effectiveStyle(nil, nil); got != nil {
		t.Fatalf("no revision/run style = %+v", got)
	}
}

// TestSubtitleExportUsesRevisionStyle 覆盖字幕 ASS 导出：revision.style 覆盖 RunConfig。
func TestSubtitleExportUsesRevisionStyle(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("字幕样式导出")
	track := env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 1000, Text: "第一句"},
	})
	resp, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, track.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: track.CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "seg-1", StartMs: 0, EndMs: 1000, Text: "第一句"},
		},
		Style: &domain.SubtitleStyle{FontSize: 64, Align: domain.SubtitleAlignCenter},
	})
	if err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	revision := resp.Body.(domain.SubtitleRevision)

	outDir := t.TempDir()
	export, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID:       project.ID,
		Kind:            domain.ExportKindSubtitle,
		Format:          domain.ExportFormatASS,
		InputRevisionID: revision.ID,
		OutputDirGrant:  outDir,
		NamingTemplate:  "{project}",
	})
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	record := export.Body.(domain.ExportRecord)
	data, err := os.ReadFile(record.OutputPath)
	if err != nil {
		t.Fatalf("read ASS export: %v", err)
	}
	if !strings.Contains(string(data), "Style: Default,Arial,64,") {
		t.Fatalf("revision style not applied to ASS export: %s", data)
	}
}

// TestRunDerivedVideoExportUsesRevisionStyle 覆盖视频导出计划：
// 当前 revision 的 style 覆盖 RunConfig.SubtitleStyle。
func TestRunDerivedVideoExportUsesRevisionStyle(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("视频样式导出")
	video := env.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		env.tempFile("source.mp4", "video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 1, DurationMs: 2000})
	track := env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "第一句"},
	})
	if _, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, track.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: track.CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "第一句"},
		},
		Style: &domain.SubtitleStyle{FontSize: 64},
	}); err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	updated, err := env.store.GetSubtitleTrack(ctx, track.ID)
	if err != nil {
		t.Fatalf("GetSubtitleTrack: %v", err)
	}

	run, errResp := env.svc.CreateWorkflowRun(ctx, Idem{}, CreateWorkflowRunInput{
		ProjectID:        project.ID,
		Name:             "视频样式",
		SourceLanguage:   "en",
		TargetLanguages:  []string{"zh"},
		InputRevisionIDs: []string{video.ID},
		Extract:          &domain.ExtractOptions{Mode: domain.ExtractModeASR},
		SubtitleStyle:    &domain.SubtitleStyle{FontSize: 40},
	})
	if errResp != nil {
		t.Fatalf("CreateWorkflowRun: %v", errResp)
	}
	created := run.Body.(domain.WorkflowRun)
	env.seedApprovedMix(created.ID, updated, "zh")

	export, err := env.svc.CreateExport(ctx, Idem{}, ExportRequest{
		ProjectID: project.ID, RunID: created.ID,
		Kind: domain.ExportKindVideo, Format: domain.ExportFormatMP4H264,
		OutputDirGrant: t.TempDir(), NamingTemplate: "{project}",
	})
	if err != nil {
		t.Fatalf("CreateExport: %v", err)
	}
	record := export.Body.(domain.ExportRecord)
	task, err := env.store.GetTask(ctx, record.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	plan, ok := task.Params["plan"].(map[string]any)
	if !ok {
		t.Fatalf("task params has no plan: %+v", task.Params)
	}
	style, ok := plan["style"].(map[string]any)
	if !ok {
		t.Fatalf("plan style = %T", plan["style"])
	}
	if got, ok := intValue(style["font_size"]); !ok || got != 64 {
		t.Fatalf("revision style not applied to video plan: %+v", style)
	}
}

// TestRunParamsEchoesSubtitleStyle 覆盖 RunParams.subtitle_style 只读回显。
func TestRunParamsEchoesSubtitleStyle(t *testing.T) {
	env := newExportTestEnv(t)
	project := env.seedProject("Run 样式基线")
	video := env.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		env.tempFile("source.mp4", "video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 1, DurationMs: 2000})
	style := &domain.SubtitleStyle{X: 0.5, Y: 0.9, FontSize: 44, Align: domain.SubtitleAlignCenter}
	resp, err := env.svc.CreateWorkflowRun(context.Background(), Idem{}, CreateWorkflowRunInput{
		ProjectID:        project.ID,
		Name:             "样式运行",
		SourceLanguage:   "en",
		TargetLanguages:  []string{"zh"},
		InputRevisionIDs: []string{video.ID},
		Extract:          &domain.ExtractOptions{Mode: domain.ExtractModeASR},
		SubtitleStyle:    style,
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := resp.Body.(domain.WorkflowRun)
	if run.Params == nil || run.Params.SubtitleStyle == nil {
		t.Fatalf("run params style = %+v", run.Params)
	}
	if run.Params.SubtitleStyle.FontSize != 44 || run.Params.SubtitleStyle.Align != domain.SubtitleAlignCenter {
		t.Fatalf("run params style = %+v", run.Params.SubtitleStyle)
	}
}

// TestMixPlanDigestKeepsLegacyValueWithoutStyleAndErase 证明 08 冻结计划摘要
// 只包含 revision 的内容字段：从存储读出的只读派生 revision_digest 不进入摘要，
// 因此无 style/erase 的旧 revision 与升级前（无这两个字段）的摘要逐字节一致。
// 否则升级后重新推进同一 Run 会得到不同的 Task.InputDigest，重复创建 08 任务。
func TestMixPlanDigestKeepsLegacyValueWithoutStyleAndErase(t *testing.T) {
	snap := &pipelineSnapshot{run: &domain.WorkflowRun{ID: "run-1"}}
	params := map[string]any{"op": "burn_subtitles", "style": runnerSubtitleStyle(nil)}
	inputs := []domain.ID{"srev-1"}
	createdAt := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	revision := domain.SubtitleRevision{
		ID: "srev-1", TrackID: "track-1", AuthorType: domain.AuthorTypeUser,
		Segments: []domain.Segment{{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "你好"}},
		// store.GetSubtitleRevision 会填充该派生字段；它不属于冻结计划输入。
		RevisionDigest: "derived-digest",
		CreatedAt:      createdAt,
	}

	got, err := mixPlanDigest(snap, revision, "zh", inputs, nil, "subtitle-body", params)
	if err != nil {
		t.Fatalf("mixPlanDigest: %v", err)
	}
	// 阶段一（无 style/erase_blocks/revision_digest）的 revision JSON 投影。
	want, err := domain.Digest(struct {
		RunID    domain.ID
		Language string
		Revision struct {
			ID               domain.ID         `json:"id"`
			TrackID          domain.ID         `json:"track_id"`
			ParentRevisionID domain.ID         `json:"parent_revision_id,omitempty"`
			AuthorType       domain.AuthorType `json:"author_type"`
			Segments         []domain.Segment  `json:"segments"`
			InputRevisionID  domain.ID         `json:"input_revision_id,omitempty"`
			CreatedAt        time.Time         `json:"created_at"`
		}
		Inputs       []domain.ID
		SelectionIDs []domain.ID
		Subtitle     string
		Plan         map[string]any
	}{
		RunID: "run-1", Language: "zh",
		Revision: struct {
			ID               domain.ID         `json:"id"`
			TrackID          domain.ID         `json:"track_id"`
			ParentRevisionID domain.ID         `json:"parent_revision_id,omitempty"`
			AuthorType       domain.AuthorType `json:"author_type"`
			Segments         []domain.Segment  `json:"segments"`
			InputRevisionID  domain.ID         `json:"input_revision_id,omitempty"`
			CreatedAt        time.Time         `json:"created_at"`
		}{
			ID: revision.ID, TrackID: revision.TrackID, AuthorType: revision.AuthorType,
			Segments: revision.Segments, CreatedAt: createdAt,
		},
		Inputs: inputs, Subtitle: "subtitle-body", Plan: params,
	})
	if err != nil {
		t.Fatalf("legacy digest: %v", err)
	}
	if got != want {
		t.Fatalf("mix plan digest = %q, want legacy %q（派生 revision_digest 不得进入冻结计划）",
			got, want)
	}
}
