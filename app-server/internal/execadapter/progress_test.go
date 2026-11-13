package execadapter

import (
	"context"
	"math"
	"strconv"
	"testing"
	"time"
)

// validProgressPayload 返回与 testRequest() 精确绑定的进度帧载荷。
func validProgressPayload() map[string]any {
	return map[string]any{
		"server_epoch": "7",
		"task_id":      "task-1",
		"step_id":      "step-1",
		"attempt":      2,
		"stage":        "decoding",
		"current":      5,
		"total":        10,
	}
}

// runProgressFrames 让假 Runner 在 execute 应答前发送给定进度帧，
// 返回实际送达 Core 回调的 (stage,current,total) 列表。
func runProgressFrames(t *testing.T, frames ...map[string]any) [][3]any {
	t.Helper()
	client, runner := newFakeClient(t)
	runner.progressFrames = frames
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var delivered [][3]any
	res := client.Execute(ctx, testRequest(), func(stage string, current, total int) {
		delivered = append(delivered, [3]any{stage, current, total})
	}, nil)
	if res.TransportErr != nil {
		t.Fatalf("Execute() transport error = %v", res.TransportErr)
	}
	return delivered
}

// TestClientExecuteDeliversOnlyBoundProgress 守护 Runner 进度帧的顶层绑定：
// 只有 task/step/attempt/epoch 与当前请求完全匹配的帧才允许进入回调。
func TestClientExecuteDeliversOnlyBoundProgress(t *testing.T) {
	valid := validProgressPayload()
	wrongTask := validProgressPayload()
	wrongTask["task_id"] = "other-task"
	wrongStep := validProgressPayload()
	wrongStep["step_id"] = "other-step"
	wrongAttempt := validProgressPayload()
	wrongAttempt["attempt"] = 3
	wrongEpoch := validProgressPayload()
	wrongEpoch["server_epoch"] = "8"
	missingTask := validProgressPayload()
	delete(missingTask, "task_id")
	missingStep := validProgressPayload()
	delete(missingStep, "step_id")
	missingAttempt := validProgressPayload()
	delete(missingAttempt, "attempt")
	missingEpoch := validProgressPayload()
	delete(missingEpoch, "server_epoch")

	delivered := runProgressFrames(t, wrongTask, missingTask, wrongStep, missingStep,
		wrongAttempt, missingAttempt, wrongEpoch, missingEpoch, valid)

	if len(delivered) != 1 {
		t.Fatalf("delivered = %v, want only the bound frame", delivered)
	}
	if got := delivered[0]; got[0] != "decoding" || got[1] != 5 || got[2] != 10 {
		t.Fatalf("delivered = %v", delivered[0])
	}
}

// TestClientExecuteRejectsInvalidProgressCounts 守护计数合法性：
// 缺失/非整数/负数/溢出/total<=0/current>total 一律丢弃，不能当作 0。
func TestClientExecuteRejectsInvalidProgressCounts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing current", func(p map[string]any) { delete(p, "current") }},
		{"missing total", func(p map[string]any) { delete(p, "total") }},
		{"fractional current", func(p map[string]any) { p["current"] = 5.5 }},
		{"fractional total", func(p map[string]any) { p["total"] = 10.5 }},
		{"negative current", func(p map[string]any) { p["current"] = -1 }},
		{"negative total", func(p map[string]any) { p["total"] = -10 }},
		{"zero total", func(p map[string]any) { p["current"] = 0; p["total"] = 0 }},
		{"current greater than total", func(p map[string]any) { p["current"] = 11 }},
		{"count overflow", func(p map[string]any) { p["current"] = math.MaxFloat64 }},
		{"int64 overflow float", func(p map[string]any) { p["current"] = math.Ldexp(1, 63) }},
		{"string count", func(p map[string]any) { p["current"] = "5" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := validProgressPayload()
			tc.mutate(payload)
			if delivered := runProgressFrames(t, payload); len(delivered) != 0 {
				t.Fatalf("delivered = %v, want no callback", delivered)
			}
		})
	}
}

// TestClientExecuteAcceptsCompleteProgress 守护 current == total 的合法边界。
func TestClientExecuteAcceptsCompleteProgress(t *testing.T) {
	payload := validProgressPayload()
	payload["current"] = 10
	if delivered := runProgressFrames(t, payload); len(delivered) != 1 {
		t.Fatalf("delivered = %v, want one callback", delivered)
	}
}

// TestClientExecuteRejectsJSONPrecisionLossProgress 守护 JSON float64 精度边界：
// 9007199254740992 与 9007199254740993 解码后相等；不能用丢失精度的值
// 伪造 current==total（真实 floor 应为 99，而不是 100）。
func TestClientExecuteRejectsJSONPrecisionLossProgress(t *testing.T) {
	if strconv.IntSize != 64 {
		t.Skip("JSON safe-integer boundary requires 64-bit int")
	}
	decoded, err := decodeFrame([]byte(`{"v":1,"op":"progress","req_id":"req-progress","payload":{"server_epoch":"7","task_id":"task-1","step_id":"step-1","attempt":2,"stage":"decoding","current":9007199254740992,"total":9007199254740993}}`))
	if err != nil {
		t.Fatalf("decodeFrame: %v", err)
	}
	current, _ := decoded.Payload["current"].(float64)
	total, _ := decoded.Payload["total"].(float64)
	if current != total {
		t.Fatalf("decodeFrame premise = %v/%v, want equal float64 after precision loss", current, total)
	}
	if delivered := runProgressFrames(t, decoded.Payload); len(delivered) != 0 {
		t.Fatalf("precision-loss frame delivered %v; true floor would be 99, not 100", delivered)
	}
}

// TestClientExecuteAcceptsMaxSafeJSONProgress 守护固定支持范围：
// 2^53-1 以内的 JSON 整数在 float64 中精确，可正常交付。
func TestClientExecuteAcceptsMaxSafeJSONProgress(t *testing.T) {
	if strconv.IntSize != 64 {
		t.Skip("JSON safe-integer boundary requires 64-bit int")
	}
	decoded, err := decodeFrame([]byte(`{"v":1,"op":"progress","req_id":"req-progress","payload":{"server_epoch":"7","task_id":"task-1","step_id":"step-1","attempt":2,"stage":"decoding","current":9007199254740991,"total":9007199254740991}}`))
	if err != nil {
		t.Fatalf("decodeFrame: %v", err)
	}
	delivered := runProgressFrames(t, decoded.Payload)
	if len(delivered) != 1 {
		t.Fatalf("delivered = %v, want one callback at the safe boundary", delivered)
	}
	// Compare in int64 so this test still compiles on 32-bit targets before Skip runs.
	if int64(delivered[0][1].(int)) != 9007199254740991 || int64(delivered[0][2].(int)) != 9007199254740991 {
		t.Fatalf("delivered counts = %v, want exact 2^53-1", delivered[0])
	}
}

// TestParseProgressCountKeepsExactIntegerTypes 守护本地 int/int64 不受 JSON
// 安全范围限制：只有 JSON float64 需要 2^53-1 上限。
func TestParseProgressCountKeepsExactIntegerTypes(t *testing.T) {
	for _, value := range []any{math.MaxInt, int64(math.MaxInt)} {
		got, ok := parseProgressCount(value)
		if !ok || got != math.MaxInt {
			t.Fatalf("parseProgressCount(%T) = %d, %v; want exact %d", value, got, ok, math.MaxInt)
		}
	}
	if _, ok := parseProgressCount(float64(maxSafeJSONInteger) + 1); ok {
		t.Fatal("float count beyond 2^53-1 accepted; JSON safe range must reject it")
	}
}
