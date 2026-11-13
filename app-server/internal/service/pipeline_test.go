package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func TestAdvancePipelineIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "yaomu.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logger, err := logging.New(logging.Options{Path: filepath.Join(t.TempDir(), "core.log")})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	svc := New(Options{
		Store: st, Log: logger, WorkspaceID: "ws-pipeline", ServerEpoch: 1,
		DataDir: dataDir, Capabilities: StaticCapabilities{ExecutionFixtures: testExecutionFixtures()}, Devices: SystemDevices{},
		PipelineOrchestration: true,
	})
	if err := svc.EnsureStorageDirs(); err != nil {
		t.Fatalf("storage dirs: %v", err)
	}
	projectResp, err := svc.CreateProject(ctx, Idem{}, CreateProjectRequest{Name: "流水线幂等"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	project := projectResp.Body.(domain.Project)
	source := filepath.Join(t.TempDir(), "source.mp4")
	if err := os.WriteFile(source, []byte("fake-media"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	importResp, err := svc.ImportAssets(ctx, Idem{}, AssetImportRequest{
		ProjectID: string(project.ID), FileGrants: []string{source},
	})
	if err != nil {
		t.Fatalf("ImportAssets: %v", err)
	}
	imported := importResp.Body.(AssetImportResult)
	if len(imported.AssetIDs) != 1 {
		t.Fatalf("imported assets = %d", len(imported.AssetIDs))
	}
	runResp, err := svc.CreateWorkflowRun(ctx, Idem{}, CreateWorkflowRunInput{
		ProjectID:        string(project.ID),
		Name:             "流水线幂等",
		SourceLanguage:   "en",
		TargetLanguages:  []string{"zh"},
		InputRevisionIDs: []string{string(imported.AssetIDs[0])},
		Extract:          &domain.ExtractOptions{Mode: domain.ExtractModeASR},
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := runResp.Body.(domain.WorkflowRun)

	var events []domain.DomainEvent
	if err := st.DoTx(ctx, func(tx *store.Tx) error {
		if err := svc.advancePipeline(ctx, tx, run.ID, &events); err != nil {
			return err
		}
		return svc.advancePipeline(ctx, tx, run.ID, &events)
	}); err != nil {
		t.Fatalf("advancePipeline: %v", err)
	}
	tasks, err := st.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	counts := map[domain.PipelineNodeKey]int{}
	for _, task := range tasks {
		counts[task.NodeKey]++
	}
	if counts[domain.NodeKeyAnalyze] != 1 {
		t.Fatalf("analyze tasks = %d, want 1 after repeated advance", counts[domain.NodeKeyAnalyze])
	}
	for key, count := range counts {
		if key != domain.NodeKeyAnalyze && count > 0 {
			t.Fatalf("unexpected downstream node %s tasks = %d before upstream success", key, count)
		}
	}
}

// TestNodeTransitionPathRecoversFailed 守护“失败节点被新成功事实恢复”的合法路径：
// 重试/重抽成功后，聚合必须能把 failed 拉回 running → done，而不是永久停在终态。
func TestNodeTransitionPathRecoversFailed(t *testing.T) {
	cases := []struct {
		name string
		key  domain.PipelineNodeKey
		to   domain.NodeStatus
		want []domain.NodeStatus
	}{
		{
			name: "non-review done", key: domain.NodeKeyClone, to: domain.NodeStatusDone,
			want: []domain.NodeStatus{domain.NodeStatusRunning, domain.NodeStatusDone},
		},
		{
			name: "review done", key: domain.NodeKeyTranslate, to: domain.NodeStatusDone,
			want: []domain.NodeStatus{domain.NodeStatusRunning, domain.NodeStatusWaitingReview, domain.NodeStatusDone},
		},
		{
			name: "review waiting", key: domain.NodeKeyMix, to: domain.NodeStatusWaitingReview,
			want: []domain.NodeStatus{domain.NodeStatusRunning, domain.NodeStatusWaitingReview},
		},
		{
			name: "running", key: domain.NodeKeyAnalyze, to: domain.NodeStatusRunning,
			want: []domain.NodeStatus{domain.NodeStatusRunning},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := nodeTransitionPath(domain.NodeStatusFailed, tc.to, tc.key)
			if len(path) != len(tc.want) {
				t.Fatalf("path = %v, want %v", path, tc.want)
			}
			from := domain.NodeStatusFailed
			for i, step := range path {
				if step != tc.want[i] {
					t.Fatalf("path = %v, want %v", path, tc.want)
				}
				if !domain.CanTransitionNode(from, step, tc.key) {
					t.Fatalf("illegal step %s -> %s for %s", from, step, tc.key)
				}
				from = step
			}
		})
	}
	// 已完成的节点不能被聚合事实重新打开。
	if path := nodeTransitionPath(domain.NodeStatusDone, domain.NodeStatusRunning, domain.NodeKeyClone); path != nil {
		t.Fatalf("done -> running path = %v, want nil", path)
	}
}

// TestTranslationRevisionForRunFreezesImportedTrack 守护新旧 custom Run 的译稿选择：
// 新 Run 无 06 Task 时只认冻结 revision；旧 Run（nil map）保持最新 current 轨；
// 06 Task 存在时回到 06 生成轨（最新轨）语义。
func TestTranslationRevisionForRunFreezesImportedTrack(t *testing.T) {
	oldTime := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	newTime := oldTime.Add(time.Hour)
	tracks := []domain.SubtitleTrack{
		{
			ID: "track-old", Language: "zh", Type: domain.TrackTypeTranslation,
			CurrentRevisionID: "rev-old", UpdatedAt: oldTime,
			Revisions: []domain.SubtitleRevision{
				{ID: "rev-old", Segments: []domain.Segment{{ID: "seg-1", Text: "old"}}},
			},
		},
		{
			ID: "track-new", Language: "zh", Type: domain.TrackTypeTranslation,
			CurrentRevisionID: "rev-new", UpdatedAt: newTime,
			Revisions: []domain.SubtitleRevision{
				{ID: "rev-new", Segments: []domain.Segment{{ID: "seg-1", Text: "new"}}},
			},
		},
	}
	frozen := map[string]domain.TranslationTrackRef{
		"zh": {TrackID: "track-old", RevisionID: "rev-old"},
	}
	tpl := customPipelineTemplateFromFeatures([]string{featureTranslate})
	translateTask := []domain.Task{{
		ID: "task-06", Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyTranslate,
		Language: "zh", Status: domain.TaskStatusSucceeded, CreatedAt: newTime,
	}}
	cases := []struct {
		name  string
		cfg   domain.RunConfig
		tasks []domain.Task
		want  domain.ID
	}{
		{"new run uses frozen revision", domain.RunConfig{ImportedTranslationTracks: frozen}, nil, "rev-old"},
		{"legacy run keeps latest revision", domain.RunConfig{}, nil, "rev-new"},
		{"06 task keeps generated latest track", domain.RunConfig{ImportedTranslationTracks: frozen}, translateTask, "rev-new"},
		{"missing frozen revision refuses track current", domain.RunConfig{ImportedTranslationTracks: map[string]domain.TranslationTrackRef{
			"zh": {TrackID: "track-old", RevisionID: "rev-gone"},
		}}, nil, ""},
		{"no frozen entry has no revision", domain.RunConfig{ImportedTranslationTracks: map[string]domain.TranslationTrackRef{}}, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := &pipelineSnapshot{
				run: &domain.WorkflowRun{
					Template:  "custom_pipeline",
					Languages: []string{"zh"},
					Config:    tc.cfg,
				},
				tasks:  tc.tasks,
				tracks: tracks,
			}
			got := translationRevisionForRun(snap, "zh", tpl)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("revision = %+v, want nil", got)
				}
				return
			}
			if got == nil || got.ID != tc.want {
				t.Fatalf("revision = %+v, want %s", got, tc.want)
			}
		})
	}
}
