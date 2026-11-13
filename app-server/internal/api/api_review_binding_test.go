package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// reviewWireGate 是审核列表/详情的原始 JSON 视图：测试必须断言 wire 字段
// 存在与形状，不能用本地类型“猜”服务端是否返回了 current_bindings。
type reviewWireGate map[string]any

func (g reviewWireGate) str(key string) string {
	value, _ := g[key].(string)
	return value
}

// TestReviewCurrentBindingsWire 覆盖审核列表/详情的只读当前绑定投影：
// 当前 revision 的门槛带 tuple，历史门槛返回空数组（不是缺字段/错误语种）。
func TestReviewCurrentBindingsWire(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("当前绑定投影")
	env.seedRun(projectID, "zh")
	track := env.seedTrack(projectID, "zh", []domain.Segment{{
		ID: "seg-1", StartMs: 0, EndMs: 1000, Text: "hello",
	}})

	first := env.expect(env.request(http.MethodPut, "/api/v1/subtitle-tracks/"+track.ID+"/revisions", map[string]any{
		"base_revision_id": track.CurrentRevisionID,
		"segments": []map[string]any{
			{"id": "seg-1", "start_ms": 0, "end_ms": 1000, "text": "hello v2"},
		},
	}), http.StatusCreated)
	second := decodeInto[domain.SubtitleRevision](t, first)

	raw := env.expect(env.request(http.MethodGet, "/api/v1/projects/"+projectID+"/reviews", nil), http.StatusOK)
	gates := decodeWireGates(t, raw.Body)
	translation := findWireGate(t, gates, domain.ReviewTypeTranslation, domain.ReviewStatusPending, "zh")
	expectCurrentBindings(t, translation, "zh", "translation_revision", second.ID)

	detail := env.expect(env.request(http.MethodGet, "/api/v1/reviews/"+translation.str("id"), nil), http.StatusOK)
	expectCurrentBindings(t, decodeWireGate(t, detail), "zh", "translation_revision", second.ID)

	// 再次保存把上一门槛变成历史：旧门槛必须显式返回空数组，新门槛绑定新 revision。
	third := env.expect(env.request(http.MethodPut, "/api/v1/subtitle-tracks/"+track.ID+"/revisions", map[string]any{
		"base_revision_id": second.ID,
		"segments": []map[string]any{
			{"id": "seg-1", "start_ms": 0, "end_ms": 1200, "text": "hello v3"},
		},
	}), http.StatusCreated)
	latest := decodeInto[domain.SubtitleRevision](t, third)

	gates = decodeWireGates(t, env.expect(env.request(http.MethodGet,
		"/api/v1/projects/"+projectID+"/reviews?run_id="+translation.str("run_id"), nil), http.StatusOK).Body)
	oldGate := findWireGateByID(t, gates, translation.str("id"))
	expectEmptyCurrentBindings(t, oldGate)
	newGate := findWireGateExcept(t, gates, domain.ReviewTypeTranslation, domain.ReviewStatusPending, "zh", oldGate.str("id"))
	if newGate == nil {
		t.Fatalf("second save did not create a new pending gate: %+v", gates)
	}
	expectCurrentBindings(t, newGate, "zh", "translation_revision", latest.ID)

	// 旧绑定 tuple 已不是当前绑定：409 且不落任何批准状态。
	stale := env.expect(env.request(http.MethodPost, "/api/v1/reviews/"+oldGate.str("id")+"/approve", map[string]any{
		"revision_digest": oldGate.str("revision_digest"),
		"type":            string(domain.ReviewTypeTranslation),
		"current_binding": map[string]any{
			"language": "zh", "source_kind": "translation_revision", "source_id": second.ID,
		},
	}), http.StatusConflict)
	if code := stale.code(t); code != "REVISION_CONFLICT" {
		t.Fatalf("stale binding code = %s, want REVISION_CONFLICT", code)
	}
	afterStale := decodeWireGate(t, env.expect(env.request(http.MethodGet,
		"/api/v1/reviews/"+oldGate.str("id"), nil), http.StatusOK))
	if afterStale.str("status") != string(domain.ReviewStatusPending) {
		t.Fatalf("stale guarded approve changed gate status: %+v", afterStale)
	}

	// 形状合法但 source_kind 非法属于 malformed 请求：400，不是静默忽略。
	env.expect(env.request(http.MethodPost, "/api/v1/reviews/"+newGate.str("id")+"/approve", map[string]any{
		"revision_digest": newGate.str("revision_digest"),
		"type":            string(domain.ReviewTypeTranslation),
		"current_binding": map[string]any{
			"language": "zh", "source_kind": "not_a_binding", "source_id": latest.ID,
		},
	}), http.StatusBadRequest)

	// 当前 tuple 可以放行；批准响应不保证携带投影。
	env.expect(env.request(http.MethodPost, "/api/v1/reviews/"+newGate.str("id")+"/approve", map[string]any{
		"revision_digest": newGate.str("revision_digest"),
		"type":            string(domain.ReviewTypeTranslation),
		"current_binding": map[string]any{
			"language": "zh", "source_kind": "translation_revision", "source_id": latest.ID,
		},
	}), http.StatusOK)
	approved := decodeWireGate(t, env.expect(env.request(http.MethodGet,
		"/api/v1/reviews/"+newGate.str("id"), nil), http.StatusOK))
	if approved.str("status") != string(domain.ReviewStatusApproved) {
		t.Fatalf("current guarded approve did not approve: %+v", approved)
	}
	// 已批准的当前门槛仍保留同一投影；客户端据此显示“当前已批准”。
	expectCurrentBindings(t, approved, "zh", "translation_revision", latest.ID)
}

// TestReviewApproveCurrentBindingGuard 覆盖批准请求的可选当前绑定守卫：
// 旧 tuple 在 revision 变化后 409 且不改状态；不带字段的旧请求仍可记录历史批准。
func TestReviewApproveCurrentBindingGuard(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("批准当前绑定")
	env.seedRun(projectID, "zh")
	track := env.seedTrack(projectID, "zh", []domain.Segment{{
		ID: "seg-1", StartMs: 0, EndMs: 1000, Text: "hello",
	}})

	first := env.expect(env.request(http.MethodPut, "/api/v1/subtitle-tracks/"+track.ID+"/revisions", map[string]any{
		"base_revision_id": track.CurrentRevisionID,
		"segments": []map[string]any{
			{"id": "seg-1", "start_ms": 0, "end_ms": 1000, "text": "hello v2"},
		},
	}), http.StatusCreated)
	second := decodeInto[domain.SubtitleRevision](t, first)
	gates := decodeWireGates(t, env.expect(env.request(http.MethodGet,
		"/api/v1/projects/"+projectID+"/reviews", nil), http.StatusOK).Body)
	oldGate := findWireGate(t, gates, domain.ReviewTypeTranslation, domain.ReviewStatusPending, "zh")

	// 当前 revision 前进后，oldGate 的 tuple 不再属于当前绑定。
	env.expect(env.request(http.MethodPut, "/api/v1/subtitle-tracks/"+track.ID+"/revisions", map[string]any{
		"base_revision_id": second.ID,
		"segments": []map[string]any{
			{"id": "seg-1", "start_ms": 0, "end_ms": 1100, "text": "hello v3"},
		},
	}), http.StatusCreated)

	before, err := env.store.LatestEventSeq(context.Background())
	if err != nil {
		t.Fatalf("LatestEventSeq: %v", err)
	}
	stale := env.expect(env.request(http.MethodPost, "/api/v1/reviews/"+oldGate.str("id")+"/approve", map[string]any{
		"revision_digest": oldGate.str("revision_digest"),
		"type":            string(domain.ReviewTypeTranslation),
		"current_binding": map[string]any{
			"language": "zh", "source_kind": "translation_revision", "source_id": second.ID,
		},
	}), http.StatusConflict)
	if code := stale.code(t); code != "REVISION_CONFLICT" {
		t.Fatalf("stale current binding code = %s, want REVISION_CONFLICT", code)
	}
	after, err := env.store.LatestEventSeq(context.Background())
	if err != nil {
		t.Fatalf("LatestEventSeq: %v", err)
	}
	if after != before {
		t.Fatalf("stale guarded approve wrote events: before=%d after=%d", before, after)
	}
	stillPending := decodeWireGate(t, env.expect(env.request(http.MethodGet,
		"/api/v1/reviews/"+oldGate.str("id"), nil), http.StatusOK))
	if stillPending.str("status") != string(domain.ReviewStatusPending) {
		t.Fatalf("stale guarded approve changed status: %+v", stillPending)
	}

	// 旧客户端不带 current_binding：保持历史记录语义，仍按 gate 自身摘要批准。
	env.expect(env.request(http.MethodPost, "/api/v1/reviews/"+oldGate.str("id")+"/approve", map[string]any{
		"revision_digest": oldGate.str("revision_digest"),
		"type":            string(domain.ReviewTypeTranslation),
	}), http.StatusOK)
	recorded := decodeWireGate(t, env.expect(env.request(http.MethodGet,
		"/api/v1/reviews/"+oldGate.str("id"), nil), http.StatusOK))
	if recorded.str("status") != string(domain.ReviewStatusApproved) {
		t.Fatalf("legacy approve did not record historical gate: %+v", recorded)
	}
	expectEmptyCurrentBindings(t, recorded)
}

func decodeWireGates(t *testing.T, body []byte) []reviewWireGate {
	t.Helper()
	var gates []reviewWireGate
	if err := json.Unmarshal(body, &gates); err != nil {
		t.Fatalf("decode reviews: %v (%s)", err, body)
	}
	return gates
}

func decodeWireGate(t *testing.T, res httpResult) reviewWireGate {
	t.Helper()
	var gate reviewWireGate
	if err := json.Unmarshal(res.Body, &gate); err != nil {
		t.Fatalf("decode review: %v (%s)", err, res.Body)
	}
	return gate
}

func findWireGate(t *testing.T, gates []reviewWireGate, typ domain.ReviewType, status domain.ReviewStatus, language string) reviewWireGate {
	t.Helper()
	var found reviewWireGate
	for _, gate := range gates {
		if gate.str("type") != string(typ) || gate.str("status") != string(status) {
			continue
		}
		if language != "" && gate.str("language") != language {
			continue
		}
		found = gate
	}
	if found == nil {
		t.Fatalf("no %s/%s gate for language %q: %+v", typ, status, language, gates)
	}
	return found
}

// findWireGateExcept 返回第一个匹配且 ID 不等于 exclude 的门槛；没有时返回 nil。
func findWireGateExcept(t *testing.T, gates []reviewWireGate, typ domain.ReviewType, status domain.ReviewStatus, language, exclude string) reviewWireGate {
	t.Helper()
	for _, gate := range gates {
		if gate.str("id") == exclude || gate.str("type") != string(typ) ||
			gate.str("status") != string(status) {
			continue
		}
		if language != "" && gate.str("language") != language {
			continue
		}
		return gate
	}
	return nil
}

func findWireGateByID(t *testing.T, gates []reviewWireGate, id string) reviewWireGate {
	t.Helper()
	for _, gate := range gates {
		if gate.str("id") == id {
			return gate
		}
	}
	t.Fatalf("no review gate %s: %+v", id, gates)
	return nil
}

func expectCurrentBindings(t *testing.T, gate reviewWireGate, language, kind string, sourceID string) {
	t.Helper()
	value, ok := gate["current_bindings"]
	if !ok {
		t.Fatalf("gate %s missing current_bindings: %+v", gate.str("id"), gate)
	}
	bindings, ok := value.([]any)
	if !ok {
		t.Fatalf("current_bindings is not an array: %T %+v", value, value)
	}
	for _, item := range bindings {
		binding, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("binding is not an object: %T", item)
		}
		if binding["language"] == language && binding["source_kind"] == kind && binding["source_id"] == sourceID {
			return
		}
	}
	t.Fatalf("gate %s current_bindings = %+v, want language=%s kind=%s source=%s",
		gate.str("id"), bindings, language, kind, sourceID)
}

func expectEmptyCurrentBindings(t *testing.T, gate reviewWireGate) {
	t.Helper()
	value, ok := gate["current_bindings"]
	if !ok {
		t.Fatalf("gate %s missing current_bindings: %+v", gate.str("id"), gate)
	}
	bindings, ok := value.([]any)
	if !ok {
		t.Fatalf("current_bindings is not an array: %T %+v", value, value)
	}
	if len(bindings) != 0 {
		t.Fatalf("gate %s current_bindings = %+v, want empty array", gate.str("id"), bindings)
	}
}
