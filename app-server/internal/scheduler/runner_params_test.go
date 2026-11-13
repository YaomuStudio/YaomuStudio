package scheduler

import "testing"

// TestRunnerParamsDropsCoreControlFields 覆盖 device_id 泄漏导致的分离失败：
// Core 冻结参数只用于预检/决策，不能原样下发 Runner 的 Adapter 参数白名单。
func TestRunnerParamsDropsCoreControlFields(t *testing.T) {
	in := map[string]any{
		"_core":                 map[string]any{"execution_slot": "separation"},
		"device_id":             "cpu",
		"voice_source_asset_id": "asset_voice",
		"input":                 "/mnt/d/demo.mp4",
		"vocals_sample":         float64(16000),
	}
	for _, adapterID := range []string{separateAdapterID, voiceCloneAdapterID, asrAdapterID, flashcliAdapterID, ffmpegAdapterID} {
		out := runnerParamsFor(adapterID, in)
		for _, key := range []string{"_core", "device_id", "voice_source_asset_id"} {
			if _, exists := out[key]; exists {
				t.Fatalf("%s: runner params leaked core field %q: %+v", adapterID, key, out)
			}
		}
		if out["input"] != "/mnt/d/demo.mp4" || out["vocals_sample"] != float64(16000) {
			t.Fatalf("%s: adapter params not preserved: %+v", adapterID, out)
		}
	}
}

// TestRunnerParamsMapsCUDAWithoutLeakingDeviceID 验证 Core 的 cuda:N 设备在
// Runner 契约里落到 params.device.cuda_visible_devices，且不再携带 device_id。
func TestRunnerParamsMapsCUDAWithoutLeakingDeviceID(t *testing.T) {
	for _, adapterID := range []string{separateAdapterID, voiceCloneAdapterID, asrAdapterID} {
		out := runnerParamsFor(adapterID, map[string]any{"device_id": "cuda:0"})
		device, ok := out["device"].(map[string]any)
		if !ok || device["cuda_visible_devices"] != "0" {
			t.Fatalf("%s: device = %+v, want cuda_visible_devices=0", adapterID, out["device"])
		}
		if _, leaked := out["device_id"]; leaked {
			t.Fatalf("%s: device_id leaked: %+v", adapterID, out)
		}
	}
}

// TestRunnerParamsDropsVoiceCloneCoreOnlyFields 防止 07 的句级 revision 与
// Core 运行选项进入 MVP 适配器白名单；适配器尚未实现时长调整/时间戳校验。
func TestRunnerParamsDropsVoiceCloneCoreOnlyFields(t *testing.T) {
	out := runnerParamsFor(voiceCloneAdapterID, map[string]any{
		"language":            "zh-CN",
		"content_revision":    3,
		"adjust_duration":     true,
		"validate_timestamps": true,
		"voice_path":          "/mnt/d/ref.wav",
	})
	for _, key := range []string{"content_revision", "adjust_duration", "validate_timestamps"} {
		if _, exists := out[key]; exists {
			t.Fatalf("voice_clone runner params leaked %q: %+v", key, out)
		}
	}
	if out["language"] != "zh-CN" || out["voice_path"] != "/mnt/d/ref.wav" {
		t.Fatalf("adapter business params not preserved: %+v", out)
	}
}

// TestRunnerParamsLeavesNonCUDAUnbound 防止把 cpu/mps 猜成 CUDA 设备。
func TestRunnerParamsLeavesNonCUDAUnbound(t *testing.T) {
	for _, deviceID := range []string{"cpu", "mps", "auto"} {
		out := runnerParamsFor(separateAdapterID, map[string]any{"device_id": deviceID})
		if _, exists := out["device"]; exists {
			t.Fatalf("device_id %q produced device %+v", deviceID, out["device"])
		}
	}
	// ffmpeg 的 Adapter 参数白名单不接受 device。
	out := runnerParamsFor(ffmpegAdapterID, map[string]any{"device_id": "cuda:0"})
	if _, exists := out["device"]; exists {
		t.Fatalf("ffmpeg runner params must not carry device: %+v", out)
	}
}
