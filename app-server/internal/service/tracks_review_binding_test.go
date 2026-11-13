package service

import (
	"context"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// 本文件是“保存新译文后审核门槛与 revision 摘要绑定”的永久回归：
// 不启动真实模型，不修改生产准入，按生产编排开关走真实 service 事务。
//
// 已采用口径（2026-11-07）：
//   - 06 translation gate 统一绑定原始 domain.RevisionBindingDigest，language 是独立字段；
//   - 人工保存 translation 轨产生的门槛与模型生成门槛同口径；
//   - 旧版本可能残留 digest+":"+language 的门槛：不迁移、不做前缀兼容，无法推进；
//     恢复方式是用当前 base revision 显式重新保存，产生新 raw gate 后再确认；
//   - transcription 轨影响多语言的既有失效逻辑与 DeliveryBind 不放宽、不改。
//
// 用例覆盖：旧 gate 批准不放行新内容；当前 raw gate 批准恢复 06/07 资格；保存只
// 影响本语种、不跨语种误放行。

// newProductionReviewFixture 构造一个全流程 Run + 已保存的译文轨，作为真实
// service 事务的测试数据。生产编排与冻结执行锁在创建 Run 前生效。
func newProductionReviewFixture(t *testing.T) (*exportTestEnv, domain.WorkflowRun, domain.SubtitleTrack) {
	t.Helper()
	env := newExportTestEnv(t)
	// 生产开关与冻结执行配置必须在建 Run 前生效：Run 创建时冻结执行锁，
	// 之后的 advancePipeline/审核放行才能按生产编排路径推进。仅测试夹具，
	// 不改变生产准入。
	env.svc.opts.Capabilities = StaticCapabilities{ExecutionFixtures: testExecutionFixtures()}
	env.svc.opts.PipelineOrchestration = true
	ctx := context.Background()
	project := env.seedProject("审核绑定回归")
	video := env.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		env.tempFile("source.mp4", "video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 1, DurationMs: 2000})
	track := env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "第一句"},
	})
	run := env.seedRun(project.ID, video.ID)
	if _, err := env.store.GetWorkflowRun(ctx, run.ID); err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	return env, run, track
}

func saveEditedRevision(t *testing.T, env *exportTestEnv, track domain.SubtitleTrack) domain.SubtitleRevision {
	t.Helper()
	resp, err := env.svc.SaveSubtitleRevision(context.Background(), Idem{}, track.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: track.CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "seg-1", StartMs: 0, EndMs: 950, Text: "改过的第一句"},
		},
	})
	if err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	return resp.Body.(domain.SubtitleRevision)
}

func loadRunSnapshot(t *testing.T, env *exportTestEnv, runID domain.ID) *pipelineSnapshot {
	t.Helper()
	ctx := context.Background()
	var snap *pipelineSnapshot
	if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
		var err error
		snap, err = env.svc.loadPipelineSnapshot(ctx, tx, runID)
		return err
	}); err != nil {
		t.Fatalf("loadPipelineSnapshot: %v", err)
	}
	return snap
}

func pendingTranslationGate(t *testing.T, env *exportTestEnv, runID domain.ID, language string) domain.ReviewGate {
	t.Helper()
	gates, err := env.store.ListRunReviews(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListRunReviews: %v", err)
	}
	for _, gate := range gates {
		if gate.Type == domain.ReviewTypeTranslation && gate.Language == language &&
			gate.Status == domain.ReviewStatusPending {
			return gate
		}
	}
	t.Fatalf("run %s 没有 %s 的 pending translation gate: %+v", runID, language, gates)
	return domain.ReviewGate{}
}

// TestApproveOldTranslationGateAfterSaveDoesNotApproveNewRevision 回答疑点 1：
// 保存新译文后对旧 pending gate 用其原 digest 批准，记录会成功（摘要匹配该
// gate 自身），但新 revision 不能因此被视为已放行，06 也不得完成。
func TestApproveOldTranslationGateAfterSaveDoesNotApproveNewRevision(t *testing.T) {
	env, run, track := newProductionReviewFixture(t)
	ctx := context.Background()

	oldRevision := currentRevision(track)
	if oldRevision == nil {
		t.Fatal("fixture track has no current revision")
	}
	oldDigest, err := domain.RevisionBindingDigest(*oldRevision)
	if err != nil {
		t.Fatalf("RevisionBindingDigest: %v", err)
	}
	// 与 pipeline_result.commitTranslationRevision 相同的生产 gate 形状（无后缀）。
	oldGate := domain.ReviewGate{
		ID: store.NewID("review"), RunID: run.ID, Type: domain.ReviewTypeTranslation,
		Language: "zh", RevisionDigest: oldDigest, Status: domain.ReviewStatusPending,
	}
	if err := env.store.CreateReviewGate(ctx, oldGate); err != nil {
		t.Fatalf("CreateReviewGate: %v", err)
	}

	saved := saveEditedRevision(t, env, track)
	newDigest, err := domain.RevisionBindingDigest(saved)
	if err != nil {
		t.Fatalf("RevisionBindingDigest(new): %v", err)
	}
	if newDigest == oldDigest {
		t.Fatal("保存后摘要未变化，夹具无效")
	}

	// 旧 pending gate 提交其原 digest：比较的是该 gate 自身，因此应被接受并记录。
	if _, err := env.svc.ApproveReview(ctx, Idem{}, oldGate.ID, ApproveReviewRequest{
		RevisionDigest: oldDigest, Type: domain.ReviewTypeTranslation,
	}); err != nil {
		t.Fatalf("旧 pending gate 按自身 digest 批准应被记录，实际错误: %v", err)
	}

	snap := loadRunSnapshot(t, env, run.ID)
	if env.svc.translationApproved(snap, "zh") {
		t.Fatalf("旧 gate 批准后不得把新 revision 视为已放行（old=%q new=%q）", oldDigest, newDigest)
	}
	// 当前 translation gate 与模型生成门槛同口径：绑定原始摘要，language 独立。
	savedGate := pendingTranslationGate(t, env, run.ID, "zh")
	if savedGate.RevisionDigest != newDigest {
		t.Fatalf("保存生成的 gate digest = %q，期望原始 revision 摘要 %q", savedGate.RevisionDigest, newDigest)
	}
	if savedGate.Language != "zh" {
		t.Fatalf("保存生成的 gate language = %q，期望 zh", savedGate.Language)
	}

	reloaded, err := env.store.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if status := nodeStatusOf(reloaded, domain.NodeKeyTranslate); status == domain.NodeStatusDone {
		t.Fatalf("旧 gate 批准后 06 仍应为未完成，实际 %s", status)
	}
	tasks, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	for _, task := range tasks {
		if task.NodeKey == domain.NodeKeyClone {
			t.Fatalf("旧 gate 批准后不应派发 07 clone 任务: %+v", task)
		}
	}
}

// TestApproveSavedRevisionTranslationGateResumesProductionPipeline 守护疑点 2 的
// 已采用语义：批准绑定当前 revision 的 raw translation gate 后，06 恢复完成，
// 07 的资格（translationReady）恢复。fixture 的 02/05 前驱未完成，因此这里
// 只断言下游资格，不假装测试了 07 任务的创建。
func TestApproveSavedRevisionTranslationGateResumesProductionPipeline(t *testing.T) {
	env, run, track := newProductionReviewFixture(t)
	ctx := context.Background()

	saved := saveEditedRevision(t, env, track)
	rawDigest, err := domain.RevisionBindingDigest(saved)
	if err != nil {
		t.Fatalf("RevisionBindingDigest: %v", err)
	}
	gate := pendingTranslationGate(t, env, run.ID, "zh")
	if gate.RevisionDigest != rawDigest {
		t.Fatalf("当前 translation gate digest = %q，期望原始 revision 摘要 %q", gate.RevisionDigest, rawDigest)
	}

	if _, err := env.svc.ApproveReview(ctx, Idem{}, gate.ID, ApproveReviewRequest{
		RevisionDigest: gate.RevisionDigest, Type: domain.ReviewTypeTranslation,
	}); err != nil {
		t.Fatalf("批准保存生成的当前 translation gate 失败: %v", err)
	}

	snap := loadRunSnapshot(t, env, run.ID)
	if !env.svc.translationApproved(snap, "zh") {
		t.Fatalf("批准当前 translation gate 后 translationApproved=false；"+
			"revision digest=%q，gate digest=%q", rawDigest, gate.RevisionDigest)
	}
	tpl, err := pipelineTemplateForRun(*snap.run)
	if err != nil {
		t.Fatalf("pipelineTemplateForRun: %v", err)
	}
	if !env.svc.translationReady(snap, "zh", tpl) {
		t.Fatal("批准当前 translation gate 后 07 的 translationReady 仍为 false")
	}
	reloaded, err := env.store.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if status := nodeStatusOf(reloaded, domain.NodeKeyTranslate); status != domain.NodeStatusDone {
		t.Fatalf("批准当前 translation gate 后 06 状态 = %s，期望 %s", status, domain.NodeStatusDone)
	}
}

// TestTranslationSaveScopesGateToLanguage 守护保存的语种隔离：保存 zh 译文只生成
// 一个 zh pending translation gate（不重复生成、不生成其他语种），批准后只放行
// zh，不跨语种误放行 ja。
func TestTranslationSaveScopesGateToLanguage(t *testing.T) {
	env := newExportTestEnv(t)
	env.svc.opts.Capabilities = StaticCapabilities{ExecutionFixtures: testExecutionFixtures()}
	env.svc.opts.PipelineOrchestration = true
	ctx := context.Background()
	project := env.seedProject("审核语种隔离")
	video := env.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		env.tempFile("source.mp4", "video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 1, DurationMs: 2000})
	segments := []domain.Segment{{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "第一句"}}
	zhTrack := env.seedTrack(project.ID, "zh", segments)
	env.seedTrack(project.ID, "ja", segments)
	run := env.seedRunForLanguages(project.ID, []string{"zh", "ja"}, video.ID)

	saved := saveEditedRevision(t, env, zhTrack)
	rawDigest, err := domain.RevisionBindingDigest(saved)
	if err != nil {
		t.Fatalf("RevisionBindingDigest: %v", err)
	}
	gates, err := env.store.ListRunReviews(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunReviews: %v", err)
	}
	zhPending, jaPending := 0, 0
	for _, gate := range gates {
		if gate.Type != domain.ReviewTypeTranslation || gate.Status != domain.ReviewStatusPending {
			continue
		}
		switch gate.Language {
		case "zh":
			zhPending++
		case "ja":
			jaPending++
		}
	}
	if zhPending != 1 || jaPending != 0 {
		t.Fatalf("保存 zh 译文后的 pending translation gate: zh=%d ja=%d，期望 1/0", zhPending, jaPending)
	}

	gate := pendingTranslationGate(t, env, run.ID, "zh")
	if gate.RevisionDigest != rawDigest {
		t.Fatalf("zh gate digest = %q，期望原始 revision 摘要 %q", gate.RevisionDigest, rawDigest)
	}
	if _, err := env.svc.ApproveReview(ctx, Idem{}, gate.ID, ApproveReviewRequest{
		RevisionDigest: gate.RevisionDigest, Type: domain.ReviewTypeTranslation,
	}); err != nil {
		t.Fatalf("ApproveReview: %v", err)
	}
	snap := loadRunSnapshot(t, env, run.ID)
	if !env.svc.translationApproved(snap, "zh") {
		t.Fatal("批准 zh gate 后 zh 未放行")
	}
	if env.svc.translationApproved(snap, "ja") {
		t.Fatal("批准 zh gate 不得跨语种放行 ja")
	}
}
