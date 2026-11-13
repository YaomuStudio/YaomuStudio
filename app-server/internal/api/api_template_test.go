package api

import (
	"net/http"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func TestToolExtractRunTemplate(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("提取工具")
	asset := env.seedVideoAsset(projectID, 1024)

	run := decodeInto[domain.WorkflowRun](t, env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":         projectID,
		"name":               "提取工具计划",
		"template":           "tool_extract",
		"source_language":    "en",
		"target_languages":   []string{},
		"input_revision_ids": []string{asset.ID},
		"extract":            map[string]any{"mode": "asr"},
	}), http.StatusCreated))
	if run.Template != "tool_extract" {
		t.Fatalf("template = %s", run.Template)
	}
	if len(run.Nodes) != 3 {
		t.Fatalf("nodes = %+v, want 01/02/04", run.Nodes)
	}
	want := map[domain.PipelineNodeKey]bool{
		domain.NodeKeyImport: false, domain.NodeKeyAnalyze: false, domain.NodeKeyExtract: false,
	}
	for _, node := range run.Nodes {
		if _, ok := want[node.Key]; !ok {
			t.Fatalf("unexpected node %s", node.Key)
		}
		want[node.Key] = true
	}
	for key, seen := range want {
		if !seen {
			t.Fatalf("missing node %s", key)
		}
	}
	if len(run.Languages) != 0 {
		t.Fatalf("languages = %v, want empty", run.Languages)
	}

	// 完整流水线仍要求至少一个目标语言。
	env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":         projectID,
		"name":               "空语言完整流水线",
		"source_language":    "en",
		"target_languages":   []string{},
		"input_revision_ids": []string{asset.ID},
		"extract":            map[string]any{"mode": "asr"},
	}), http.StatusBadRequest)

	// 未知模板必须拒绝，不回退 full_pipeline。
	env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":         projectID,
		"name":               "未知模板",
		"template":           "unknown_tool",
		"source_language":    "en",
		"target_languages":   []string{"zh"},
		"input_revision_ids": []string{asset.ID},
		"extract":            map[string]any{"mode": "asr"},
	}), http.StatusBadRequest)
}

func TestToolSeparateRunTemplate(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("分离工具")
	asset := env.seedVideoAsset(projectID, 1024)

	run := decodeInto[domain.WorkflowRun](t, env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":         projectID,
		"name":               "分离工具计划",
		"template":           "tool_separate",
		"source_language":    "und",
		"target_languages":   []string{},
		"input_revision_ids": []string{asset.ID},
	}), http.StatusCreated))
	if run.Template != "tool_separate" {
		t.Fatalf("template = %s", run.Template)
	}
	if run.SourceLanguage != "und" {
		t.Fatalf("source_language = %s, want und", run.SourceLanguage)
	}
	if len(run.Nodes) != 2 {
		t.Fatalf("nodes = %+v, want 01/03", run.Nodes)
	}
	want := map[domain.PipelineNodeKey]bool{
		domain.NodeKeyImport: false, domain.NodeKeySeparate: false,
	}
	for _, node := range run.Nodes {
		if _, ok := want[node.Key]; !ok {
			t.Fatalf("unexpected node %s", node.Key)
		}
		want[node.Key] = true
	}
	for key, seen := range want {
		if !seen {
			t.Fatalf("missing node %s", key)
		}
	}
	if len(run.Languages) != 0 {
		t.Fatalf("languages = %v, want empty", run.Languages)
	}

	// 分离工具至少需要一个输入资产。
	env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":       projectID,
		"name":             "分离空输入",
		"template":         "tool_separate",
		"source_language":  "und",
		"target_languages": []string{},
	}), http.StatusBadRequest)
}

func TestToolSeparatePreflightInputAssets(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("分离输入校验")
	video := env.seedVideoAsset(projectID, 1024)
	track := env.seedAudioTrackAsset(projectID)

	preflight := func(input map[string]any) domain.PreflightResult {
		t.Helper()
		return decodeInto[domain.PreflightResult](t, env.expect(
			env.request(http.MethodPost, "/api/v1/tasks/preflight", input), http.StatusOK))
	}
	hasInputAssetIssue := func(result domain.PreflightResult) bool {
		for _, issue := range result.Issues {
			if issue.Code == "INPUT_ASSET_INVALID" && issue.Severity == domain.PreflightBlocker {
				return true
			}
		}
		return false
	}
	base := func(assetID string) map[string]any {
		return map[string]any{
			"project_id":         projectID,
			"name":               "分离预检",
			"template":           "tool_separate",
			"source_language":    "und",
			"target_languages":   []string{},
			"input_revision_ids": []string{assetID},
		}
	}

	// 视频/音频是分离工具的合法输入。
	if hasInputAssetIssue(preflight(base(string(video.ID)))) {
		t.Fatalf("video input rejected for tool_separate")
	}
	// 已分离的 audio_track 不再作为分离输入，避免与输出语义混淆。
	if !hasInputAssetIssue(preflight(base(string(track.ID)))) {
		t.Fatalf("audio_track input not rejected for tool_separate")
	}
	// tool_extract 的既有语义保持不变：audio_track 仍可作为提取输入。
	extract := base(string(track.ID))
	extract["template"] = "tool_extract"
	extract["source_language"] = "en"
	extract["extract"] = map[string]any{"mode": "asr"}
	if hasInputAssetIssue(preflight(extract)) {
		t.Fatalf("audio_track input rejected for tool_extract")
	}
}

func TestToolEraseRunTemplate(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("擦除工具")
	asset := env.seedVideoAsset(projectID, 1024)

	run := decodeInto[domain.WorkflowRun](t, env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":         projectID,
		"name":               "擦除工具计划",
		"template":           "tool_erase",
		"source_language":    "chinese",
		"target_languages":   []string{},
		"input_revision_ids": []string{asset.ID},
		"extract":            map[string]any{"mode": "ocr", "ocr_language": "chinese"},
		"erase_mode":         "dialog_only",
	}), http.StatusCreated))
	if run.Template != "tool_erase" {
		t.Fatalf("template = %s", run.Template)
	}
	if run.SourceLanguage != "chinese" {
		t.Fatalf("source_language = %s, want chinese", run.SourceLanguage)
	}
	if len(run.Nodes) != 3 {
		t.Fatalf("nodes = %+v, want 01/04/05", run.Nodes)
	}
	want := map[domain.PipelineNodeKey]bool{
		domain.NodeKeyImport: false, domain.NodeKeyExtract: false, domain.NodeKeyErase: false,
	}
	for _, node := range run.Nodes {
		if _, ok := want[node.Key]; !ok {
			t.Fatalf("unexpected node %s", node.Key)
		}
		want[node.Key] = true
	}
	for key, seen := range want {
		if !seen {
			t.Fatalf("missing node %s", key)
		}
	}
	if len(run.Languages) != 0 {
		t.Fatalf("languages = %v, want empty", run.Languages)
	}

	// 擦除工具必须带 erase_mode 与 OCR 提取。
	base := map[string]any{
		"project_id":         projectID,
		"name":               "擦除校验",
		"template":           "tool_erase",
		"source_language":    "chinese",
		"target_languages":   []string{},
		"input_revision_ids": []string{asset.ID},
		"extract":            map[string]any{"mode": "ocr", "ocr_language": "chinese"},
	}
	env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", base), http.StatusBadRequest)
	base["erase_mode"] = "dialog_only"
	base["extract"] = map[string]any{"mode": "asr"}
	env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", base), http.StatusBadRequest)
}

func TestToolErasePreflightInputAssets(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("擦除输入校验")
	video := env.seedVideoAsset(projectID, 1024)
	audio := env.seedAudioAsset(projectID)
	track := env.seedAudioTrackAsset(projectID)

	preflight := func(assetID string, extract map[string]any) domain.PreflightResult {
		t.Helper()
		return decodeInto[domain.PreflightResult](t, env.expect(
			env.request(http.MethodPost, "/api/v1/tasks/preflight", map[string]any{
				"project_id":         projectID,
				"name":               "擦除预检",
				"template":           "tool_erase",
				"source_language":    "chinese",
				"target_languages":   []string{},
				"input_revision_ids": []string{assetID},
				"extract":            extract,
				"erase_mode":         "dialog_only",
			}), http.StatusOK))
	}
	hasIssue := func(result domain.PreflightResult, code string) bool {
		for _, issue := range result.Issues {
			if issue.Code == code && issue.Severity == domain.PreflightBlocker {
				return true
			}
		}
		return false
	}
	ocr := map[string]any{"mode": "ocr", "ocr_language": "chinese"}

	if hasIssue(preflight(string(video.ID), ocr), "INPUT_ASSET_INVALID") {
		t.Fatalf("video input rejected for tool_erase")
	}
	for name, id := range map[string]domain.ID{"audio": audio.ID, "audio_track": track.ID} {
		if !hasIssue(preflight(string(id), ocr), "INPUT_ASSET_INVALID") {
			t.Fatalf("%s input not rejected for tool_erase", name)
		}
	}
	if !hasIssue(preflight(string(video.ID), map[string]any{"mode": "ocr"}), "OCR_LANGUAGE_REQUIRED") {
		t.Fatalf("missing OCR language not blocked for tool_erase")
	}
}

func TestToolTranslateRunTemplate(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("翻译工具")
	asset := env.seedSubtitleAsset(string(projectID), testSRT)

	run := decodeInto[domain.WorkflowRun](t, env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":         projectID,
		"name":               "翻译工具计划",
		"template":           "tool_translate",
		"source_language":    "en",
		"target_languages":   []string{"zh"},
		"input_revision_ids": []string{asset.ID},
	}), http.StatusCreated))
	if run.Template != "tool_translate" {
		t.Fatalf("template = %s", run.Template)
	}
	if len(run.Nodes) != 2 {
		t.Fatalf("nodes = %+v, want 01/06", run.Nodes)
	}
	want := map[domain.PipelineNodeKey]bool{
		domain.NodeKeyImport: false, domain.NodeKeyTranslate: false,
	}
	for _, node := range run.Nodes {
		if _, ok := want[node.Key]; !ok {
			t.Fatalf("unexpected node %s", node.Key)
		}
		want[node.Key] = true
	}
	for key, seen := range want {
		if !seen {
			t.Fatalf("missing node %s", key)
		}
	}
	if len(run.Languages) != 1 || run.Languages[0] != "zh" {
		t.Fatalf("languages = %v, want [zh]", run.Languages)
	}

	// tool_translate 至少需要一个目标语言。
	env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":         projectID,
		"name":               "翻译空语言",
		"template":           "tool_translate",
		"source_language":    "en",
		"target_languages":   []string{},
		"input_revision_ids": []string{asset.ID},
	}), http.StatusBadRequest)
}

func TestToolTranslatePreflightInputAssets(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("翻译输入校验")
	subtitle := env.seedSubtitleAsset(string(projectID), testSRT)
	video := env.seedVideoAsset(projectID, 16)
	audio := env.seedAudioAsset(string(projectID))

	preflight := func(assetID string) domain.PreflightResult {
		t.Helper()
		return decodeInto[domain.PreflightResult](t, env.expect(
			env.request(http.MethodPost, "/api/v1/tasks/preflight", map[string]any{
				"project_id":         projectID,
				"name":               "翻译预检",
				"template":           "tool_translate",
				"source_language":    "en",
				"target_languages":   []string{"zh"},
				"input_revision_ids": []string{assetID},
			}), http.StatusOK))
	}
	hasInputAssetIssue := func(result domain.PreflightResult) bool {
		for _, issue := range result.Issues {
			if issue.Code == "INPUT_ASSET_INVALID" && issue.Severity == domain.PreflightBlocker {
				return true
			}
		}
		return false
	}
	if hasInputAssetIssue(preflight(string(subtitle.ID))) {
		t.Fatalf("subtitle input rejected for tool_translate")
	}
	for name, id := range map[string]domain.ID{"video": video.ID, "audio": audio.ID} {
		if !hasInputAssetIssue(preflight(string(id))) {
			t.Fatalf("%s input not rejected for tool_translate", name)
		}
	}
}

func TestVoiceCloneRunTemplate(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("声音克隆")
	audio := env.seedAudioAsset(string(projectID))
	env.seedTrackType(string(projectID), "en", domain.TrackTypeTranscription, []domain.Segment{
		{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"},
	})
	env.seedTrack(string(projectID), "zh", []domain.Segment{
		{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"},
	})

	run := decodeInto[domain.WorkflowRun](t, env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":         projectID,
		"name":               "克隆计划",
		"template":           "tool_voice_clone",
		"source_language":    "en",
		"target_languages":   []string{"zh"},
		"input_revision_ids": []string{audio.ID},
	}), http.StatusCreated))
	if run.Template != "tool_voice_clone" || len(run.Nodes) != 2 {
		t.Fatalf("run = template %s nodes %+v, want voice_clone 01/07", run.Template, run.Nodes)
	}
	if len(run.Languages) != 1 || run.Languages[0] != "zh" {
		t.Fatalf("languages = %v, want [zh]", run.Languages)
	}

	// 授权确认已移除：不带 voice_authorization 仍可创建。
	unauthenticated := decodeInto[domain.WorkflowRun](t, env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":         projectID,
		"name":               "克隆无授权字段",
		"template":           "tool_voice_clone",
		"source_language":    "en",
		"target_languages":   []string{"zh"},
		"input_revision_ids": []string{audio.ID},
	}), http.StatusCreated))
	if unauthenticated.Template != "tool_voice_clone" {
		t.Fatalf("unauthenticated clone template = %s, want tool_voice_clone", unauthenticated.Template)
	}

	// 旧客户端携带已移除字段：严格 JSON 解码拒绝。
	env.expect(env.request(http.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"project_id":          projectID,
		"name":                "旧客户端克隆",
		"template":            "tool_voice_clone",
		"source_language":     "en",
		"target_languages":    []string{"zh"},
		"input_revision_ids":  []string{audio.ID},
		"voice_authorization": true,
	}), http.StatusBadRequest)
}

func TestVoiceClonePreflightTracks(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("克隆预检")
	audio := env.seedAudioAsset(string(projectID))
	video := env.seedVideoAsset(projectID, 16)

	preflight := func(assetID string, source, target string, seed bool) domain.PreflightResult {
		t.Helper()
		if seed {
			env.seedTrackType(string(projectID), source, domain.TrackTypeTranscription, []domain.Segment{
				{ID: "src-" + source, StartMs: 0, EndMs: 900, Text: "Hello"},
			})
			env.seedTrack(string(projectID), target, []domain.Segment{
				{ID: "tr-" + target, StartMs: 0, EndMs: 900, Text: "你好"},
			})
		}
		return decodeInto[domain.PreflightResult](t, env.expect(
			env.request(http.MethodPost, "/api/v1/tasks/preflight", map[string]any{
				"project_id":         projectID,
				"name":               "克隆预检",
				"template":           "tool_voice_clone",
				"source_language":    source,
				"target_languages":   []string{target},
				"input_revision_ids": []string{assetID},
			}), http.StatusOK))
	}
	hasIssue := func(result domain.PreflightResult, code string) bool {
		for _, issue := range result.Issues {
			if issue.Code == code && issue.Severity == domain.PreflightBlocker {
				return true
			}
		}
		return false
	}

	if hasIssue(preflight(string(audio.ID), "en", "zh", true), "INPUT_ASSET_INVALID") {
		t.Fatalf("audio input rejected for voice_clone")
	}
	if !hasIssue(preflight(string(video.ID), "en", "zh", false), "INPUT_ASSET_INVALID") {
		t.Fatalf("video input not rejected for voice_clone")
	}
	if !hasIssue(preflight(string(audio.ID), "ja", "zh", false), "SOURCE_TRACK_REQUIRED") {
		t.Fatalf("missing source track not blocked")
	}
	if !hasIssue(preflight(string(audio.ID), "en", "fr", false), "TRANSLATION_TRACK_REQUIRED") {
		t.Fatalf("missing translation track not blocked")
	}
}
