package service

import (
	"context"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// TestMixTaskMatchesRevision 覆盖 08 任务冻结 revision 的匹配规则：
// 无 `_core` 绑定的旧任务视为匹配；ID/摘要任一不匹配即失效。
func TestMixTaskMatchesRevision(t *testing.T) {
	revision := &domain.SubtitleRevision{
		ID: "srev-1", TrackID: "track-1", AuthorType: domain.AuthorTypeUser,
		Segments: []domain.Segment{{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "你好"}},
	}
	digest := mixSubtitleDigest(*revision)
	core := func(values map[string]any) map[string]any {
		return map[string]any{"_core": values}
	}
	cases := []struct {
		name   string
		params map[string]any
		want   bool
	}{
		{name: "no core", params: nil, want: true},
		{name: "empty core", params: core(map[string]any{}), want: true},
		{name: "id and digest match", params: core(map[string]any{
			"subtitle_revision_id": "srev-1", "subtitle_digest": digest,
		}), want: true},
		{name: "revision changed", params: core(map[string]any{
			"subtitle_revision_id": "srev-2", "subtitle_digest": digest,
		}), want: false},
		{name: "content changed", params: core(map[string]any{
			"subtitle_revision_id": "srev-1", "subtitle_digest": "stale-digest",
		}), want: false},
		{name: "id only matches", params: core(map[string]any{
			"subtitle_revision_id": "srev-1",
		}), want: true},
		{name: "digest only matches", params: core(map[string]any{
			"subtitle_digest": digest,
		}), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := domain.Task{Params: tc.params}
			if got := mixTaskMatchesRevision(task, revision); got != tc.want {
				t.Fatalf("mixTaskMatchesRevision(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
	if !mixTaskMatchesRevision(domain.Task{}, nil) {
		t.Fatal("nil revision must keep legacy match semantics")
	}
}

// seedMixTaskWithCore 登记一个带冻结 revision 绑定的成功 08 任务。
func seedMixTaskWithCore(
	t *testing.T,
	env *exportTestEnv,
	projectID, runID domain.ID,
	language, artifact string,
	revision domain.SubtitleRevision,
	at time.Time,
) domain.Task {
	t.Helper()
	params := map[string]any{"language": language}
	core := coreTaskParams(params)
	core["subtitle_revision_id"] = revision.ID
	core["subtitle_digest"] = mixSubtitleDigest(revision)
	task := domain.Task{
		ID: store.NewID("task"), ProjectID: projectID, RunID: runID,
		Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyMix, Language: language,
		Status: domain.TaskStatusSucceeded, ArtifactIDs: []domain.ID{artifact},
		InputDigest: "mix-input-" + artifact, Params: params,
		CreatedAt: at, UpdatedAt: at,
	}
	if err := env.store.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

// TestCurrentDeliveryDigestsRejectsStaleMixRevision 覆盖导出摘要口径：
// 08 任务冻结在旧 revision 时不进入当前交付摘要；编辑后的新 revision
// 仍保留 revision 级交付绑定通道（main 的字幕/音频导出兼容）。
func TestCurrentDeliveryDigestsRejectsStaleMixRevision(t *testing.T) {
	env, project, run, tracks := newReviewBindingFixture(t, "zh")
	ctx := context.Background()
	track := tracks["zh"]
	revision := currentRevision(track)
	if revision == nil {
		t.Fatal("fixture track has no current revision")
	}

	stale := seedMixTaskWithCore(t, env, project.ID, run.ID, "zh", "asset-mix-old", *revision, time.Now().UTC())
	staleDigest := mixDigest(t, stale)
	firstRevisionBind := domain.DeliveryBind(revisionDigest(t, *revision))

	digests := func(kind domain.ExportKind) []string {
		t.Helper()
		var got []string
		if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
			var err error
			got, err = env.svc.currentDeliveryDigests(ctx, tx, project, run,
				kind, "zh", nil)
			return err
		}); err != nil {
			t.Fatalf("currentDeliveryDigests: %v", err)
		}
		return got
	}
	contains := func(values []string, want string) bool {
		for _, value := range values {
			if value == want {
				return true
			}
		}
		return false
	}

	got := digests(domain.ExportKindSubtitle)
	if !contains(got, staleDigest) || !contains(got, firstRevisionBind) {
		t.Fatalf("initial subtitle digests = %v, want stale mix %q and revision bind %q",
			got, staleDigest, firstRevisionBind)
	}
	got = digests(domain.ExportKindVideo)
	if !contains(got, staleDigest) || contains(got, firstRevisionBind) {
		t.Fatalf("initial video digests = %v, want only mix %q (no revision bind)", got, staleDigest)
	}

	resp, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, track.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: track.CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "seg-zh", StartMs: 0, EndMs: 950, Text: "改过的字幕"},
		},
	})
	if err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	edited := resp.Body.(domain.SubtitleRevision)
	editedBind := domain.DeliveryBind(revisionDigest(t, edited))

	got = digests(domain.ExportKindSubtitle)
	if contains(got, staleDigest) {
		t.Fatalf("stale 08 digest %q must not bind the edited revision: %v", staleDigest, got)
	}
	if !contains(got, editedBind) {
		t.Fatalf("edited revision delivery bind %q missing from %v", editedBind, got)
	}
	if got = digests(domain.ExportKindVideo); contains(got, staleDigest) || contains(got, editedBind) {
		t.Fatalf("media export must not accept revision-level bindings: %v", got)
	}

	fresh := seedMixTaskWithCore(t, env, project.ID, run.ID, "zh", "asset-mix-new", edited,
		time.Now().UTC().Add(time.Second))
	freshDigest := mixDigest(t, fresh)
	got = digests(domain.ExportKindVideo)
	if !contains(got, freshDigest) {
		t.Fatalf("current 08 digest %q missing from %v", freshDigest, got)
	}
}

// TestReviewBindingsRejectStaleMixRevision 覆盖 current_bindings 投影：
// 08 任务冻结在旧 revision 时，其 delivery gate 不再是当前绑定。
func TestReviewBindingsRejectStaleMixRevision(t *testing.T) {
	env, project, run, tracks := newReviewBindingFixture(t, "zh")
	ctx := context.Background()
	revision := currentRevision(tracks["zh"])
	if revision == nil {
		t.Fatal("fixture track has no current revision")
	}
	stale := seedMixTaskWithCore(t, env, project.ID, run.ID, "zh", "asset-mix-old", *revision, time.Now().UTC())
	gate := seedReviewGate(t, env, run.ID, domain.ReviewTypeDelivery, "zh", mixDigest(t, stale))

	loadGate := func() domain.ReviewGate {
		t.Helper()
		var loaded domain.ReviewGate
		if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
			gates, err := tx.ListRunReviews(ctx, run.ID)
			if err != nil {
				return err
			}
			if err := attachReviewCurrentBindings(ctx, tx, gates); err != nil {
				return err
			}
			for i := range gates {
				if gates[i].ID == gate.ID {
					loaded = gates[i]
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("attachReviewCurrentBindings: %v", err)
		}
		return loaded
	}

	expectBindings(t, loadGate(), domain.ReviewCurrentBinding{
		Language: "zh", SourceKind: domain.ReviewBindingDeliveryMix, SourceID: stale.ID,
	})

	if _, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, tracks["zh"].ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: tracks["zh"].CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "seg-zh", StartMs: 0, EndMs: 950, Text: "改过的字幕"},
		},
	}); err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	// 编辑后旧 08 不再冻结当前 revision，旧 delivery gate 无当前绑定。
	expectBindings(t, loadGate())
}

// TestComputeMixStatusRequiresCurrentMixRevision 覆盖 08 节点聚合：
// 旧 08 冻结在旧 revision 时即使 delivery 门槛已放行也回到 pending；
// 匹配当前 revision 的新 08 再次进入 waiting_review，放行后恢复 done。
func TestComputeMixStatusRequiresCurrentMixRevision(t *testing.T) {
	env, project, run, tracks := newReviewBindingFixture(t, "zh")
	ctx := context.Background()
	// 只启用 08 的模板：翻译视为就绪，不触发 07 句级聚合，隔离混音状态判定。
	tpl := pipelineTemplate{ID: templateCustomPipeline, Nodes: []domain.PipelineNodeKey{domain.NodeKeyMix}}
	revision := currentRevision(tracks["zh"])
	if revision == nil {
		t.Fatal("fixture track has no current revision")
	}

	mixStatus := func() domain.NodeStatus {
		t.Helper()
		var got domain.NodeStatus
		if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
			tasks, err := tx.ListRunTasks(ctx, run.ID)
			if err != nil {
				return err
			}
			gates, err := tx.ListRunReviews(ctx, run.ID)
			if err != nil {
				return err
			}
			trackList, err := tx.ListProjectTracks(ctx, run.ProjectID)
			if err != nil {
				return err
			}
			snap := &pipelineSnapshot{run: &run, tasks: tasks, gates: gates, tracks: trackList}
			got, err = env.svc.computeMixStatus(ctx, tx, snap, tpl)
			return err
		}); err != nil {
			t.Fatalf("computeMixStatus: %v", err)
		}
		return got
	}
	approveDelivery := func(digest string) {
		t.Helper()
		gate := seedReviewGate(t, env, run.ID, domain.ReviewTypeDelivery, "zh", digest)
		if _, err := env.store.ApproveReview(ctx, gate.ID, digest, domain.ReviewTypeDelivery, time.Now().UTC()); err != nil {
			t.Fatalf("ApproveReview: %v", err)
		}
	}

	stale := seedMixTaskWithCore(t, env, project.ID, run.ID, "zh", "asset-mix-old", *revision, time.Now().UTC())
	approveDelivery(mixDigest(t, stale))
	if got := mixStatus(); got != domain.NodeStatusDone {
		t.Fatalf("current 08 status = %s, want done", got)
	}

	resp, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, tracks["zh"].ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: tracks["zh"].CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "seg-zh", StartMs: 0, EndMs: 950, Text: "改过的字幕"},
		},
	})
	if err != nil {
		t.Fatalf("SaveSubtitleRevision: %v", err)
	}
	edited := resp.Body.(domain.SubtitleRevision)
	if got := mixStatus(); got != domain.NodeStatusPending {
		t.Fatalf("stale 08 status = %s, want pending after the revision edit", got)
	}

	fresh := seedMixTaskWithCore(t, env, project.ID, run.ID, "zh", "asset-mix-new", edited,
		time.Now().UTC().Add(time.Second))
	if got := mixStatus(); got != domain.NodeStatusWaitingReview {
		t.Fatalf("new 08 status = %s, want waiting_review before delivery approval", got)
	}
	approveDelivery(mixDigest(t, fresh))
	if got := mixStatus(); got != domain.NodeStatusDone {
		t.Fatalf("approved new 08 status = %s, want done", got)
	}
}

// TestReviewBindingsMixBindingUsesExportRevision 覆盖多轨项目口径一致：
// 导出按 pickExportSubtitleTrack 选择先创建的同语种译文轨；08 冻结在另一条
// 轨道时 delivery 门槛同样不投影为当前绑定，与导出门禁得出同一结论。
func TestReviewBindingsMixBindingUsesExportRevision(t *testing.T) {
	env, project, run, tracks := newReviewBindingFixture(t, "zh")
	ctx := context.Background()
	other := env.seedTrack(project.ID, "zh", []domain.Segment{
		{ID: "seg-zh-other", StartMs: 0, EndMs: 900, Text: "另一条译文轨"},
	})
	exportTrack := pickExportSubtitleTrack([]domain.SubtitleTrack{tracks["zh"], other}, "zh")
	if exportTrack == nil || exportTrack.ID != tracks["zh"].ID {
		t.Fatalf("export track selection = %+v, want the first-created track %s", exportTrack, tracks["zh"].ID)
	}
	exported := currentRevision(*exportTrack)
	if exported == nil {
		t.Fatal("fixture track has no current revision")
	}
	otherRevision := currentRevision(other)
	if otherRevision == nil || otherRevision.ID == exported.ID {
		t.Fatalf("second track revision = %+v, want a distinct revision", otherRevision)
	}
	mix := seedMixTaskWithCore(t, env, project.ID, run.ID, "zh", "asset-mix-other", *otherRevision, time.Now().UTC())
	gate := seedReviewGate(t, env, run.ID, domain.ReviewTypeDelivery, "zh", mixDigest(t, mix))

	var loaded domain.ReviewGate
	if err := env.store.DoTx(ctx, func(tx *store.Tx) error {
		gates, err := tx.ListRunReviews(ctx, run.ID)
		if err != nil {
			return err
		}
		if err := attachReviewCurrentBindings(ctx, tx, gates); err != nil {
			return err
		}
		for i := range gates {
			if gates[i].ID == gate.ID {
				loaded = gates[i]
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("attachReviewCurrentBindings: %v", err)
	}
	// 导出会使用先创建的译文轨 revision；08 冻结在另一条轨，不能投影为当前交付。
	expectBindings(t, loaded)
}
