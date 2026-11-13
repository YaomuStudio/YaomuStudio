package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// 本文件覆盖审核 current_bindings 只读投影与可选批准守卫：
// 不启动真实模型，不改变生产准入；投影在同一 ReadTx 派生，批准守卫在写事务内
// 于任何状态变更前复核。历史记录/旧请求语义保持不变。

// newReviewBindingFixture 构造真实 service/store 的多语 Run 与译文轨。
func newReviewBindingFixture(t *testing.T, languages ...string) (*exportTestEnv, domain.Project, domain.WorkflowRun, map[string]domain.SubtitleTrack) {
	t.Helper()
	env := newExportTestEnv(t)
	project := env.seedProject("审核当前绑定")
	video := env.seedMediaAsset(project.ID, "source.mp4", domain.AssetTypeVideo,
		env.tempFile("source.mp4", "video-bytes"),
		&domain.MediaMeta{Container: "mp4", AudioTracks: 1, DurationMs: 2000})
	run := env.seedRunForLanguages(project.ID, languages, video.ID)
	tracks := make(map[string]domain.SubtitleTrack, len(languages))
	for _, language := range languages {
		tracks[language] = env.seedTrack(project.ID, language, []domain.Segment{
			{ID: "seg-" + language, StartMs: 0, EndMs: 900, Text: language + " 字幕"},
		})
	}
	return env, project, run, tracks
}

func seedReviewGate(t *testing.T, env *exportTestEnv, runID domain.ID, typ domain.ReviewType, language, digest string) domain.ReviewGate {
	t.Helper()
	gate := domain.ReviewGate{
		ID: store.NewID("review"), RunID: runID, Type: typ, Language: language,
		RevisionDigest: digest, Status: domain.ReviewStatusPending,
	}
	if err := env.store.CreateReviewGate(context.Background(), gate); err != nil {
		t.Fatalf("CreateReviewGate: %v", err)
	}
	return gate
}

func seedSucceededMixTask(t *testing.T, env *exportTestEnv, projectID, runID domain.ID, language, artifact string, at time.Time) domain.Task {
	t.Helper()
	task := domain.Task{
		ID: store.NewID("task"), ProjectID: projectID, RunID: runID,
		Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyMix, Language: language,
		Status: domain.TaskStatusSucceeded, ArtifactIDs: []domain.ID{artifact},
		InputDigest: "mix-input-" + artifact, CreatedAt: at, UpdatedAt: at,
	}
	if err := env.store.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func mixDigest(t *testing.T, task domain.Task) string {
	t.Helper()
	digest, err := domain.NodeReviewDigest(task.RunID, task.NodeKey, task.Language,
		task.ID, task.ArtifactIDs, task.InputDigest)
	if err != nil {
		t.Fatalf("NodeReviewDigest: %v", err)
	}
	return digest
}

func revisionDigest(t *testing.T, revision domain.SubtitleRevision) string {
	t.Helper()
	digest, err := domain.RevisionBindingDigest(revision)
	if err != nil {
		t.Fatalf("RevisionBindingDigest: %v", err)
	}
	return digest
}

func gateFromList(t *testing.T, gates []domain.ReviewGate, id domain.ID) domain.ReviewGate {
	t.Helper()
	for _, gate := range gates {
		if gate.ID == id {
			return gate
		}
	}
	t.Fatalf("review %s not found in %+v", id, gates)
	return domain.ReviewGate{}
}

func gateWithDigest(t *testing.T, gates []domain.ReviewGate, typ domain.ReviewType, digest string) domain.ReviewGate {
	t.Helper()
	for _, gate := range gates {
		if gate.Type == typ && gate.RevisionDigest == digest {
			return gate
		}
	}
	t.Fatalf("no %s gate with digest %s: %+v", typ, digest, gates)
	return domain.ReviewGate{}
}

func expectBindings(t *testing.T, gate domain.ReviewGate, want ...domain.ReviewCurrentBinding) {
	t.Helper()
	if gate.CurrentBindings == nil {
		t.Fatalf("review %s current_bindings is nil, want %+v", gate.ID, want)
	}
	if len(gate.CurrentBindings) != len(want) {
		t.Fatalf("review %s current_bindings = %+v, want %+v", gate.ID, gate.CurrentBindings, want)
	}
	for i := range want {
		if gate.CurrentBindings[i] != want[i] {
			t.Fatalf("review %s binding[%d] = %+v, want %+v", gate.ID, i, gate.CurrentBindings[i], want[i])
		}
	}
}

func expectReviewCode(t *testing.T, err error, want domain.ErrorCode) {
	t.Helper()
	code, ok := domain.CodeOf(err)
	if !ok || code != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
}

// TestReviewCurrentBindingsReadProjection 覆盖列表/详情的只读投影：
// 当前 raw 译文摘要、旧后缀历史门槛、空 language 精确摘要解析、交付字幕绑定。
func TestReviewCurrentBindingsReadProjection(t *testing.T) {
	env, project, run, tracks := newReviewBindingFixture(t, "zh", "ja")
	ctx := context.Background()
	zhRev := currentRevision(tracks["zh"])
	jaRev := currentRevision(tracks["ja"])
	if zhRev == nil || jaRev == nil {
		t.Fatal("fixture tracks have no current revision")
	}
	zhRaw := revisionDigest(t, *zhRev)
	jaRaw := revisionDigest(t, *jaRev)

	rawGate := seedReviewGate(t, env, run.ID, domain.ReviewTypeTranslation, "zh", zhRaw)
	suffixGate := seedReviewGate(t, env, run.ID, domain.ReviewTypeTranslation, "zh", zhRaw+":zh")
	emptyLanguageGate := seedReviewGate(t, env, run.ID, domain.ReviewTypeTranslation, "", zhRaw)
	deliveryEmptyLanguage := seedReviewGate(t, env, run.ID, domain.ReviewTypeDelivery, "", domain.DeliveryBind(zhRaw))
	deliveryJa := seedReviewGate(t, env, run.ID, domain.ReviewTypeDelivery, "ja", domain.DeliveryBind(jaRaw))

	beforeSeq, err := env.store.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("LatestEventSeq: %v", err)
	}
	tasksBefore, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}

	gates, err := env.svc.ListProjectReviews(ctx, project.ID, run.ID)
	if err != nil {
		t.Fatalf("ListProjectReviews: %v", err)
	}
	expectBindings(t, gateFromList(t, gates, rawGate.ID), domain.ReviewCurrentBinding{
		Language: "zh", SourceKind: domain.ReviewBindingTranslationRevision, SourceID: zhRev.ID,
	})
	expectBindings(t, gateFromList(t, gates, suffixGate.ID))
	expectBindings(t, gateFromList(t, gates, emptyLanguageGate.ID), domain.ReviewCurrentBinding{
		Language: "zh", SourceKind: domain.ReviewBindingTranslationRevision, SourceID: zhRev.ID,
	})
	expectBindings(t, gateFromList(t, gates, deliveryEmptyLanguage.ID), domain.ReviewCurrentBinding{
		Language: "zh", SourceKind: domain.ReviewBindingDeliveryRevision, SourceID: zhRev.ID,
	})
	expectBindings(t, gateFromList(t, gates, deliveryJa.ID), domain.ReviewCurrentBinding{
		Language: "ja", SourceKind: domain.ReviewBindingDeliveryRevision, SourceID: jaRev.ID,
	})

	// 详情使用与列表相同的投影。
	detail, err := env.svc.GetReview(ctx, rawGate.ID)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	expectBindings(t, detail, domain.ReviewCurrentBinding{
		Language: "zh", SourceKind: domain.ReviewBindingTranslationRevision, SourceID: zhRev.ID,
	})

	// 空数组必须显式序列化为 []（客户端据此区分“无当前绑定”与旧服务缺字段）。
	encoded, err := json.Marshal(gateFromList(t, gates, suffixGate.ID))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"current_bindings":[]`) {
		t.Fatalf("empty binding gate JSON = %s", encoded)
	}
	encodedCurrent, err := json.Marshal(gateFromList(t, gates, rawGate.ID))
	if err != nil {
		t.Fatalf("Marshal current: %v", err)
	}
	if count := strings.Count(string(encodedCurrent), `"current_bindings"`); count != 1 {
		t.Fatalf("current binding gate JSON has %d current_bindings keys: %s", count, encodedCurrent)
	}
	// 未投影的记录（批准回执）省略可选字段，而不是伪造空数组。
	unprojected, err := json.Marshal(domain.ReviewGate{ID: "review-x"})
	if err != nil {
		t.Fatalf("Marshal unprojected: %v", err)
	}
	if strings.Contains(string(unprojected), "current_bindings") {
		t.Fatalf("unprojected gate JSON = %s", unprojected)
	}

	// 读取不得写业务状态或事件。
	afterSeq, err := env.store.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("LatestEventSeq: %v", err)
	}
	if afterSeq != beforeSeq {
		t.Fatalf("read projection wrote events: before=%d after=%d", beforeSeq, afterSeq)
	}
	tasksAfter, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	if len(tasksAfter) != len(tasksBefore) {
		t.Fatalf("read projection created tasks: before=%d after=%d", len(tasksBefore), len(tasksAfter))
	}
	for _, gate := range []domain.ReviewGate{rawGate, suffixGate, emptyLanguageGate, deliveryEmptyLanguage, deliveryJa} {
		current, err := env.store.GetReviewGate(ctx, gate.ID)
		if err != nil {
			t.Fatalf("GetReviewGate: %v", err)
		}
		if current.Status != domain.ReviewStatusPending {
			t.Fatalf("read projection changed gate %s status to %s", gate.ID, current.Status)
		}
	}
}

// TestReviewCurrentBindingsDeliveryMix 覆盖 08 混音绑定：同语种优先、旧空 language
// 单语回退可匹配多个目标语种、新混音任务使旧门槛变为历史。
func TestReviewCurrentBindingsDeliveryMix(t *testing.T) {
	env, project, run, tracks := newReviewBindingFixture(t, "zh", "ja")
	ctx := context.Background()
	now := time.Now().UTC()
	zhMix := seedSucceededMixTask(t, env, project.ID, run.ID, "zh", "mix-zh", now)
	jaMix := seedSucceededMixTask(t, env, project.ID, run.ID, "ja", "mix-ja", now.Add(time.Second))

	zhGate := seedReviewGate(t, env, run.ID, domain.ReviewTypeDelivery, "zh", mixDigest(t, zhMix))
	jaGate := seedReviewGate(t, env, run.ID, domain.ReviewTypeDelivery, "ja", mixDigest(t, jaMix))
	emptyLanguageGate := seedReviewGate(t, env, run.ID, domain.ReviewTypeDelivery, "", mixDigest(t, zhMix))

	gates, err := env.svc.ListProjectReviews(ctx, project.ID, run.ID)
	if err != nil {
		t.Fatalf("ListProjectReviews: %v", err)
	}
	expectBindings(t, gateFromList(t, gates, zhGate.ID), domain.ReviewCurrentBinding{
		Language: "zh", SourceKind: domain.ReviewBindingDeliveryMix, SourceID: zhMix.ID,
	})
	expectBindings(t, gateFromList(t, gates, jaGate.ID), domain.ReviewCurrentBinding{
		Language: "ja", SourceKind: domain.ReviewBindingDeliveryMix, SourceID: jaMix.ID,
	})
	// 空 language 门槛按精确摘要解析：ja 的最新混音已是 jaMix，只有 zh 命中。
	expectBindings(t, gateFromList(t, gates, emptyLanguageGate.ID), domain.ReviewCurrentBinding{
		Language: "zh", SourceKind: domain.ReviewBindingDeliveryMix, SourceID: zhMix.ID,
	})

	// 同语种出现更新的成功混音：旧门槛不再是当前绑定，新门槛绑定新任务。
	newerZh := seedSucceededMixTask(t, env, project.ID, run.ID, "zh", "mix-zh-2", now.Add(2*time.Second))
	gates, err = env.svc.ListProjectReviews(ctx, project.ID, run.ID)
	if err != nil {
		t.Fatalf("ListProjectReviews: %v", err)
	}
	expectBindings(t, gateFromList(t, gates, zhGate.ID))
	newerGate := seedReviewGate(t, env, run.ID, domain.ReviewTypeDelivery, "zh", mixDigest(t, newerZh))
	gates, err = env.svc.ListProjectReviews(ctx, project.ID, run.ID)
	if err != nil {
		t.Fatalf("ListProjectReviews: %v", err)
	}
	expectBindings(t, gateFromList(t, gates, newerGate.ID), domain.ReviewCurrentBinding{
		Language: "zh", SourceKind: domain.ReviewBindingDeliveryMix, SourceID: newerZh.ID,
	})

	// 旧 Run 的未标注语言混音任务：按 Run 目标语种逐个回退，可同时匹配多语。
	legacyRun := env.seedRunForLanguages(project.ID, []string{"fr", "de"})
	legacyTask := seedSucceededMixTask(t, env, project.ID, legacyRun.ID, "", "mix-legacy", now.Add(3*time.Second))
	legacyGate := seedReviewGate(t, env, legacyRun.ID, domain.ReviewTypeDelivery, "", mixDigest(t, legacyTask))
	gates, err = env.svc.ListProjectReviews(ctx, project.ID, legacyRun.ID)
	if err != nil {
		t.Fatalf("ListProjectReviews: %v", err)
	}
	expectBindings(t, gateFromList(t, gates, legacyGate.ID),
		domain.ReviewCurrentBinding{Language: "fr", SourceKind: domain.ReviewBindingDeliveryMix, SourceID: legacyTask.ID},
		domain.ReviewCurrentBinding{Language: "de", SourceKind: domain.ReviewBindingDeliveryMix, SourceID: legacyTask.ID},
	)

	// 同语种多条译文轨：显式字幕导出可指定任意同项目同语种 current revision，
	// 交付投影必须逐轨精确匹配，不能只取 pickPipelineTrack 的最新一条。
	olderZhRevision := currentRevision(tracks["zh"])
	if olderZhRevision == nil {
		t.Fatal("zh track has no current revision")
	}
	secondZhTrack := env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-zh-extra", StartMs: 0, EndMs: 900, Text: "第二条中文轨"},
	})
	secondZhRevision := currentRevision(secondZhTrack)
	allTracks, err := env.store.ListProjectTracks(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListProjectTracks: %v", err)
	}
	if picked := pickPipelineTrack(allTracks, "zh", domain.TrackTypeTranslation); picked == nil || picked.ID != secondZhTrack.ID {
		t.Fatalf("pickPipelineTrack picked %+v, want newer track %s", picked, secondZhTrack.ID)
	}
	olderTrackGate := seedReviewGate(t, env, run.ID, domain.ReviewTypeDelivery, "zh",
		domain.DeliveryBind(revisionDigest(t, *olderZhRevision)))
	secondTrackGate := seedReviewGate(t, env, run.ID, domain.ReviewTypeDelivery, "zh",
		domain.DeliveryBind(revisionDigest(t, *secondZhRevision)))
	gates, err = env.svc.ListProjectReviews(ctx, project.ID, run.ID)
	if err != nil {
		t.Fatalf("ListProjectReviews: %v", err)
	}
	expectBindings(t, gateFromList(t, gates, olderTrackGate.ID), domain.ReviewCurrentBinding{
		Language: "zh", SourceKind: domain.ReviewBindingDeliveryRevision, SourceID: olderZhRevision.ID,
	})
	expectBindings(t, gateFromList(t, gates, secondTrackGate.ID), domain.ReviewCurrentBinding{
		Language: "zh", SourceKind: domain.ReviewBindingDeliveryRevision, SourceID: secondZhRevision.ID,
	})
}

// TestApproveReviewCurrentBindingStaleAndCurrent 覆盖批准守卫：旧 tuple 409 且
// 零副作用；旧请求仍记录历史批准；当前 tuple 放行后下游资格恢复。
func TestApproveReviewCurrentBindingStaleAndCurrent(t *testing.T) {
	env, run, track := newProductionReviewFixture(t)
	ctx := context.Background()

	first := saveEditedRevision(t, env, track)
	firstGate := pendingTranslationGate(t, env, run.ID, "zh")
	if firstGate.RevisionDigest != revisionDigest(t, first) {
		t.Fatalf("gate digest = %q, want raw revision digest", firstGate.RevisionDigest)
	}
	gates, err := env.svc.ListProjectReviews(ctx, run.ProjectID, run.ID)
	if err != nil {
		t.Fatalf("ListProjectReviews: %v", err)
	}
	firstBindingGate := gateFromList(t, gates, firstGate.ID)
	if len(firstBindingGate.CurrentBindings) != 1 {
		t.Fatalf("first gate current_bindings = %+v", firstBindingGate.CurrentBindings)
	}
	staleBinding := firstBindingGate.CurrentBindings[0]
	if staleBinding.SourceID != first.ID {
		t.Fatalf("current binding = %+v, want revision %s", staleBinding, first.ID)
	}

	// 再保存使 firstGate 成为历史：同一 tuple 不再是当前绑定。
	saved, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, track.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: first.ID,
		Segments: []domain.SegmentInput{
			{ID: "seg-1", StartMs: 0, EndMs: 960, Text: "再次修改的第一句"},
		},
	})
	if err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	second := saved.Body.(domain.SubtitleRevision)

	// 交付字幕绑定同样随 revision 前进：旧交付门槛无当前绑定，新门槛绑定新 revision。
	gates, err = env.svc.ListProjectReviews(ctx, run.ProjectID, run.ID)
	if err != nil {
		t.Fatalf("ListProjectReviews: %v", err)
	}
	oldDelivery := gateWithDigest(t, gates, domain.ReviewTypeDelivery, domain.DeliveryBind(revisionDigest(t, first)))
	newDelivery := gateWithDigest(t, gates, domain.ReviewTypeDelivery, domain.DeliveryBind(revisionDigest(t, second)))
	expectBindings(t, oldDelivery)
	expectBindings(t, newDelivery, domain.ReviewCurrentBinding{
		Language: "zh", SourceKind: domain.ReviewBindingDeliveryRevision, SourceID: second.ID,
	})
	beforeSeq, err := env.store.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("LatestEventSeq: %v", err)
	}
	tasksBefore, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	runBefore, err := env.store.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}

	_, err = env.svc.ApproveReview(ctx, Idem{Key: "stale-guard", Digest: "guard-digest"}, firstGate.ID, ApproveReviewRequest{
		RevisionDigest: firstGate.RevisionDigest,
		Type:           domain.ReviewTypeTranslation,
		CurrentBinding: &staleBinding,
	})
	expectReviewCode(t, err, domain.ErrorCodeRevisionConflict)
	if _, err := env.store.GetReceipt(ctx, "ws-export", "reviews.approve", "stale-guard"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stale guarded approve wrote idempotency receipt: %v", err)
	}

	afterSeq, err := env.store.LatestEventSeq(ctx)
	if err != nil {
		t.Fatalf("LatestEventSeq: %v", err)
	}
	if afterSeq != beforeSeq {
		t.Fatalf("stale guarded approve wrote events: before=%d after=%d", beforeSeq, afterSeq)
	}
	tasksAfter, err := env.store.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	if len(tasksAfter) != len(tasksBefore) {
		t.Fatalf("stale guarded approve created tasks: before=%d after=%d", len(tasksBefore), len(tasksAfter))
	}
	runAfter, err := env.store.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if runAfter.StateVersion != runBefore.StateVersion || runAfter.Status != runBefore.Status {
		t.Fatalf("stale guarded approve changed run: before=%+v after=%+v", runBefore, runAfter)
	}
	pending, err := env.store.GetReviewGate(ctx, firstGate.ID)
	if err != nil {
		t.Fatalf("GetReviewGate: %v", err)
	}
	if pending.Status != domain.ReviewStatusPending {
		t.Fatalf("stale guarded approve changed gate status to %s", pending.Status)
	}

	// 旧客户端不带 current_binding：保留历史记录语义（记录但不放行新稿）。
	if _, err := env.svc.ApproveReview(ctx, Idem{}, firstGate.ID, ApproveReviewRequest{
		RevisionDigest: firstGate.RevisionDigest,
		Type:           domain.ReviewTypeTranslation,
	}); err != nil {
		t.Fatalf("legacy approve should record historical gate: %v", err)
	}
	snap := loadRunSnapshot(t, env, run.ID)
	if env.svc.translationApproved(snap, "zh") {
		t.Fatal("legacy historical approve must not release the new revision")
	}

	// 当前 gate + 当前 tuple：放行后 06 完成且 07 资格恢复。
	gates, err = env.svc.ListProjectReviews(ctx, run.ProjectID, run.ID)
	if err != nil {
		t.Fatalf("ListProjectReviews: %v", err)
	}
	var current domain.ReviewGate
	for _, gate := range gates {
		if gate.Type != domain.ReviewTypeTranslation || len(gate.CurrentBindings) == 0 {
			continue
		}
		binding := gate.CurrentBindings[0]
		if binding.SourceKind == domain.ReviewBindingTranslationRevision && binding.SourceID == second.ID {
			current = gate
		}
	}
	if current.ID == "" {
		t.Fatalf("no current translation gate binds revision %s: %+v", second.ID, gates)
	}
	binding := current.CurrentBindings[0]
	if _, err := env.svc.ApproveReview(ctx, Idem{}, current.ID, ApproveReviewRequest{
		RevisionDigest: current.RevisionDigest,
		Type:           domain.ReviewTypeTranslation,
		CurrentBinding: &binding,
	}); err != nil {
		t.Fatalf("guarded current approve: %v", err)
	}
	snap = loadRunSnapshot(t, env, run.ID)
	if !env.svc.translationApproved(snap, "zh") {
		t.Fatal("guarded current approve did not release translation")
	}
	tpl, err := pipelineTemplateForRun(*snap.run)
	if err != nil {
		t.Fatalf("pipelineTemplateForRun: %v", err)
	}
	if !env.svc.translationReady(snap, "zh", tpl) {
		t.Fatal("guarded current approve did not restore 07 eligibility")
	}
	reloaded, err := env.store.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetWorkflowRun: %v", err)
	}
	if status := nodeStatusOf(reloaded, domain.NodeKeyTranslate); status != domain.NodeStatusDone {
		t.Fatalf("06 status = %s, want done", status)
	}
}

// TestApproveReviewCurrentBindingPausedAndTerminal 覆盖 PAUSED 允许登记但不派发，
// 终止 Run 拒绝新批准（含守卫请求），非法形状返回 INVALID_REQUEST。
func TestApproveReviewCurrentBindingPausedAndTerminal(t *testing.T) {
	ctx := context.Background()

	t.Run("paused records without dispatch", func(t *testing.T) {
		env, _, run, tracks := newReviewBindingFixture(t, "zh")
		revision := currentRevision(tracks["zh"])
		gate := seedReviewGate(t, env, run.ID, domain.ReviewTypeTranslation, "zh", revisionDigest(t, *revision))
		if _, err := env.svc.PauseWorkflowRun(ctx, Idem{}, run.ID); err != nil {
			t.Fatalf("PauseWorkflowRun: %v", err)
		}
		tasksBefore, err := env.store.ListRunTasks(ctx, run.ID)
		if err != nil {
			t.Fatalf("ListRunTasks: %v", err)
		}
		// 与 UI 同路径：PAUSED 下仍从列表投影取得当前 tuple 再批准。
		gates, err := env.svc.ListProjectReviews(ctx, run.ProjectID, run.ID)
		if err != nil {
			t.Fatalf("ListProjectReviews: %v", err)
		}
		current := gateFromList(t, gates, gate.ID)
		if len(current.CurrentBindings) != 1 {
			t.Fatalf("paused current_bindings = %+v", current.CurrentBindings)
		}
		binding := current.CurrentBindings[0]
		if _, err := env.svc.ApproveReview(ctx, Idem{}, gate.ID, ApproveReviewRequest{
			RevisionDigest: gate.RevisionDigest,
			Type:           domain.ReviewTypeTranslation,
			CurrentBinding: &binding,
		}); err != nil {
			t.Fatalf("paused approve: %v", err)
		}
		tasksAfter, err := env.store.ListRunTasks(ctx, run.ID)
		if err != nil {
			t.Fatalf("ListRunTasks: %v", err)
		}
		if len(tasksAfter) != len(tasksBefore) {
			t.Fatalf("paused approve dispatched tasks: before=%d after=%d", len(tasksBefore), len(tasksAfter))
		}
		reloaded, err := env.store.GetWorkflowRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetWorkflowRun: %v", err)
		}
		if reloaded.Status != domain.RunStatusPaused {
			t.Fatalf("paused run status = %s", reloaded.Status)
		}
		approved, err := env.store.GetReviewGate(ctx, gate.ID)
		if err != nil {
			t.Fatalf("GetReviewGate: %v", err)
		}
		if approved.Status != domain.ReviewStatusApproved {
			t.Fatalf("paused gate status = %s", approved.Status)
		}
	})

	t.Run("terminal rejects new approval", func(t *testing.T) {
		env, _, run, tracks := newReviewBindingFixture(t, "zh")
		revision := currentRevision(tracks["zh"])
		gate := seedReviewGate(t, env, run.ID, domain.ReviewTypeTranslation, "zh", revisionDigest(t, *revision))
		if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
			current, err := tx.GetWorkflowRun(ctx, run.ID)
			if err != nil {
				return err
			}
			_, err = tx.UpdateRunStatus(ctx, run.ID, domain.RunStatusFailed, current.StateVersion, time.Now().UTC())
			return err
		}); err != nil {
			t.Fatalf("mark run failed: %v", err)
		}
		beforeSeq, err := env.store.LatestEventSeq(ctx)
		if err != nil {
			t.Fatalf("LatestEventSeq: %v", err)
		}
		binding := domain.ReviewCurrentBinding{
			Language: "zh", SourceKind: domain.ReviewBindingTranslationRevision, SourceID: revision.ID,
		}
		_, err = env.svc.ApproveReview(ctx, Idem{}, gate.ID, ApproveReviewRequest{
			RevisionDigest: gate.RevisionDigest,
			Type:           domain.ReviewTypeTranslation,
			CurrentBinding: &binding,
		})
		expectReviewCode(t, err, domain.ErrorCodePreflightStale)
		afterSeq, err := env.store.LatestEventSeq(ctx)
		if err != nil {
			t.Fatalf("LatestEventSeq: %v", err)
		}
		if afterSeq != beforeSeq {
			t.Fatalf("terminal rejection wrote events: before=%d after=%d", beforeSeq, afterSeq)
		}
		pending, err := env.store.GetReviewGate(ctx, gate.ID)
		if err != nil {
			t.Fatalf("GetReviewGate: %v", err)
		}
		if pending.Status != domain.ReviewStatusPending {
			t.Fatalf("terminal approval changed gate status to %s", pending.Status)
		}
	})

	t.Run("malformed binding", func(t *testing.T) {
		env, _, run, tracks := newReviewBindingFixture(t, "zh")
		revision := currentRevision(tracks["zh"])
		gate := seedReviewGate(t, env, run.ID, domain.ReviewTypeTranslation, "zh", revisionDigest(t, *revision))
		beforeSeq, err := env.store.LatestEventSeq(ctx)
		if err != nil {
			t.Fatalf("LatestEventSeq: %v", err)
		}
		cases := []domain.ReviewCurrentBinding{
			{Language: "", SourceKind: domain.ReviewBindingTranslationRevision, SourceID: revision.ID},
			{Language: "zh", SourceKind: "not_a_binding", SourceID: revision.ID},
			{Language: "zh", SourceKind: domain.ReviewBindingTranslationRevision, SourceID: ""},
		}
		for _, binding := range cases {
			_, err := env.svc.ApproveReview(ctx, Idem{}, gate.ID, ApproveReviewRequest{
				RevisionDigest: gate.RevisionDigest,
				Type:           domain.ReviewTypeTranslation,
				CurrentBinding: &binding,
			})
			expectReviewCode(t, err, domain.ErrorCodeInvalidRequest)
		}
		afterSeq, err := env.store.LatestEventSeq(ctx)
		if err != nil {
			t.Fatalf("LatestEventSeq: %v", err)
		}
		if afterSeq != beforeSeq {
			t.Fatalf("malformed approve wrote events: before=%d after=%d", beforeSeq, afterSeq)
		}
		pending, err := env.store.GetReviewGate(ctx, gate.ID)
		if err != nil {
			t.Fatalf("GetReviewGate: %v", err)
		}
		if pending.Status != domain.ReviewStatusPending {
			t.Fatalf("malformed approve changed gate status to %s", pending.Status)
		}
	})
}
