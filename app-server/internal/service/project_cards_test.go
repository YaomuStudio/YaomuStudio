package service

import (
	"context"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func TestProjectCardsCoreFields(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()

	created, err := env.svc.CreateProject(ctx, Idem{}, CreateProjectRequest{Name: "卡片项目"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	project := created.Body.(domain.Project)
	if project.EpisodeID == "" {
		t.Fatal("new project must have episode_id")
	}

	got, err := env.svc.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.EpisodeID != project.EpisodeID {
		t.Fatalf("episode_id = %q, want %q", got.EpisodeID, project.EpisodeID)
	}

	summaries, err := env.svc.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(summaries) != 1 || summaries[0].EpisodeID != project.EpisodeID {
		t.Fatalf("summary = %+v", summaries)
	}

	runResp, err := env.svc.CreateWorkflowRun(ctx, Idem{}, CreateWorkflowRunInput{
		ProjectID:       project.ID,
		Name:            "卡片 Run",
		SourceLanguage:  "zh",
		TargetLanguages: []string{"en"},
		Extract:         &domain.ExtractOptions{Mode: domain.ExtractModeASR},
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := runResp.Body.(domain.WorkflowRun)
	if run.ModeType != "full_pipeline" {
		t.Fatalf("mode_type = %q, want full_pipeline", run.ModeType)
	}
	if run.Params == nil || run.Params.ExtractMode != domain.ExtractModeASR || run.Params.ASRLanguage != "zh" {
		t.Fatalf("params = %+v", run.Params)
	}

	summaries, err = env.svc.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects after run: %v", err)
	}
	if summaries[0].ModeType != "full_pipeline" {
		t.Fatalf("summary mode_type = %q, want full_pipeline", summaries[0].ModeType)
	}
}

func TestAssetRoleForTask(t *testing.T) {
	cases := []struct {
		name string
		task domain.Task
		meta *domain.MediaMeta
		role domain.AssetRole
	}{
		{name: "segment", task: domain.Task{Kind: domain.TaskKindSegment}, role: domain.AssetRoleDubAudio},
		{name: "erase", task: domain.Task{Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyErase}, role: domain.AssetRoleCleanVideo},
		{name: "extract", task: domain.Task{Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyExtract}, role: domain.AssetRoleSourceSubtitle},
		{name: "translate", task: domain.Task{Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyTranslate}, role: domain.AssetRoleTranslatedSubtitle},
		{name: "mix", task: domain.Task{Kind: domain.TaskKindNode, NodeKey: domain.NodeKeyMix}, role: domain.AssetRoleFinalVideo},
		{
			name: "separate vocals",
			task: domain.Task{Kind: domain.TaskKindNode, NodeKey: domain.NodeKeySeparate},
			meta: &domain.MediaMeta{Role: "vocals"},
			role: domain.AssetRoleVoice,
		},
		{
			name: "separate background",
			task: domain.Task{Kind: domain.TaskKindNode, NodeKey: domain.NodeKeySeparate},
			meta: &domain.MediaMeta{Role: "background"},
			role: domain.AssetRoleAmbience,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if role := assetRoleForTask(tc.task, tc.meta); role != tc.role {
				t.Fatalf("role = %q, want %q", role, tc.role)
			}
		})
	}
}
