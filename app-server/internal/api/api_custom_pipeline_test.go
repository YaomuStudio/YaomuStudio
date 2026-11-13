package api

import (
	"net/http"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

func TestCustomPipelineRunTemplate(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("专属链路")
	video := env.seedVideoAsset(projectID, 1024)
	audio := env.seedAudioAsset(projectID)

	run := decodeInto[domain.WorkflowRun](t, env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":         projectID,
		"name":               "专属链路·翻译成片",
		"template":           "custom_pipeline",
		"features":           []string{"mix", "translate", "separation"},
		"source_language":    "en",
		"target_languages":   []string{"zh"},
		"input_revision_ids": []string{video.ID},
	}), http.StatusCreated))
	if run.Template != "custom_pipeline" {
		t.Fatalf("template = %s", run.Template)
	}
	want := []domain.PipelineNodeKey{
		domain.NodeKeyImport, domain.NodeKeySeparate, domain.NodeKeyExtract,
		domain.NodeKeyTranslate, domain.NodeKeyMix,
	}
	if len(run.Nodes) != len(want) {
		t.Fatalf("nodes = %+v, want %v", run.Nodes, want)
	}
	for i, key := range want {
		if run.Nodes[i].Key != key {
			t.Fatalf("node[%d] = %s, want %s", i, run.Nodes[i].Key, key)
		}
	}

	// 仅分离允许空目标语言。
	separate := decodeInto[domain.WorkflowRun](t, env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":         projectID,
		"name":               "专属链路·仅分离",
		"template":           "custom_pipeline",
		"features":           []string{"separation"},
		"source_language":    "und",
		"target_languages":   []string{},
		"input_revision_ids": []string{audio.ID},
	}), http.StatusCreated))
	if len(separate.Nodes) != 2 {
		t.Fatalf("separation nodes = %+v", separate.Nodes)
	}

	// 非 custom 携带 features 必须拒绝。
	env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":         projectID,
		"name":               "错误 features",
		"template":           "tool_separate",
		"features":           []string{"separation"},
		"source_language":    "und",
		"target_languages":   []string{},
		"input_revision_ids": []string{audio.ID},
	}), http.StatusBadRequest)

	// clone 不再要求显式授权。
	clone := decodeInto[domain.WorkflowRun](t, env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":         projectID,
		"name":               "无授权克隆",
		"template":           "custom_pipeline",
		"features":           []string{"clone"},
		"source_language":    "en",
		"target_languages":   []string{"zh"},
		"input_revision_ids": []string{audio.ID},
	}), http.StatusCreated))
	if clone.Template != "custom_pipeline" {
		t.Fatalf("clone template = %s, want custom_pipeline", clone.Template)
	}

	// 擦除必须带 OCR 配置。
	env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":         projectID,
		"name":               "擦除缺 OCR",
		"template":           "custom_pipeline",
		"features":           []string{"erase"},
		"source_language":    "chinese",
		"target_languages":   []string{},
		"input_revision_ids": []string{video.ID},
		"erase_mode":         "dialog_only",
	}), http.StatusBadRequest)

	// 音频 + 擦除在预检阶段阻断（不是静态 400）。
	issues := decodeInto[domain.PreflightResult](t, env.expect(env.request(http.MethodPost, "/api/v1/tasks/preflight", map[string]any{
		"project_id":         projectID,
		"name":               "音频擦除",
		"template":           "custom_pipeline",
		"features":           []string{"erase"},
		"source_language":    "chinese",
		"target_languages":   []string{},
		"input_revision_ids": []string{audio.ID},
		"erase_mode":         "dialog_only",
		"extract":            map[string]any{"mode": "ocr", "ocr_language": "chinese"},
	}), http.StatusOK))
	found := false
	for _, issue := range issues.Issues {
		if issue.Code == "INPUT_ASSET_INVALID" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audio+erase issues = %+v", issues.Issues)
	}
}

func TestCustomPipelineTemplatesAPI(t *testing.T) {
	env := newTestEnv(t)
	withKey := func(r *http.Request) { r.Header.Set("Idempotency-Key", "custom-templates") }
	withKeyDup := func(r *http.Request) { r.Header.Set("Idempotency-Key", "custom-templates-dup") }
	empty := decodeInto[service.CustomPipelineTemplateList](t, env.expect(env.request(http.MethodGet,
		"/api/v1/custom-pipeline/templates", nil), http.StatusOK))
	if len(empty.Items) != 0 {
		t.Fatalf("empty templates = %+v", empty.Items)
	}

	body := map[string]any{"items": []map[string]any{{
		"id":                  "tpl-weekly",
		"name":                "周更剧集链路",
		"features":            []string{"translate", "mix"},
		"languages":           []string{"en", "ja"},
		"source_language":     "zh",
		"validate_timestamps": true,
		"adjust_duration":     true,
	}}}
	saved := decodeInto[service.CustomPipelineTemplateList](t, env.expect(env.request(http.MethodPut,
		"/api/v1/custom-pipeline/templates", body, withKey), http.StatusOK))
	if len(saved.Items) != 1 || saved.Items[0].ID != "tpl-weekly" {
		t.Fatalf("saved = %+v", saved.Items)
	}
	read := decodeInto[service.CustomPipelineTemplateList](t, env.expect(env.request(http.MethodGet,
		"/api/v1/custom-pipeline/templates", nil), http.StatusOK))
	if len(read.Items) != 1 || read.Items[0].Name != "周更剧集链路" {
		t.Fatalf("read = %+v", read.Items)
	}

	// 重名（忽略大小写/空白）拒绝。
	duplicate := map[string]any{"items": []any{
		map[string]any{
			"id": "tpl-1", "name": "周更剧集链路", "features": []string{"translate"},
			"languages": []string{"en"}, "source_language": "zh",
			"validate_timestamps": true, "adjust_duration": true,
		},
		map[string]any{
			"id": "tpl-2", "name": " 周更剧集链路 ", "features": []string{"separation"},
			"languages": []string{}, "source_language": "und",
			"validate_timestamps": false, "adjust_duration": false,
		},
	}}
	env.expect(env.request(http.MethodPut, "/api/v1/custom-pipeline/templates", duplicate, withKeyDup), http.StatusBadRequest)

	// 写操作必须带幂等键。
	env.expect(env.request(http.MethodPut, "/api/v1/custom-pipeline/templates", body,
		func(r *http.Request) { r.Header.Del("Idempotency-Key") }), http.StatusBadRequest)
}
