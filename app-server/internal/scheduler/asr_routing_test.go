package scheduler

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// TestAdapterForTaskRoutesASRExtract 覆盖 04 的模式路由：只有 asr（含缺省）
// 走新适配器；OCR/asr_ocr 尚未交付，继续由 flashcli 门控桩失败。
func TestAdapterForTaskRoutesASRExtract(t *testing.T) {
	cases := []struct {
		name       string
		params     map[string]any
		wantID     string
		wantDigest string
	}{
		{
			name:       "asr",
			params:     map[string]any{"extract": map[string]any{"mode": "asr"}},
			wantID:     asrAdapterID,
			wantDigest: asrAdapterDigest,
		},
		{
			name:       "default-asr",
			params:     nil,
			wantID:     asrAdapterID,
			wantDigest: asrAdapterDigest,
		},
		{
			name:       "ocr-gated",
			params:     map[string]any{"extract": map[string]any{"mode": "ocr"}},
			wantID:     flashcliAdapterID,
			wantDigest: flashcliAdapterDigest,
		},
		{
			name:       "asr-ocr-gated",
			params:     map[string]any{"extract": map[string]any{"mode": "asr_ocr"}},
			wantID:     flashcliAdapterID,
			wantDigest: flashcliAdapterDigest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := domain.Task{
				Kind:    domain.TaskKindNode,
				NodeKey: domain.NodeKeyExtract,
				Params:  tc.params,
			}
			adapter, runtime, _ := adapterForTask(task)
			if adapter.ID != tc.wantID || adapter.Digest != tc.wantDigest {
				t.Fatalf("adapter = %s/%s, want %s/%s",
					adapter.ID, adapter.Digest, tc.wantID, tc.wantDigest)
			}
			if runtime != adapter {
				t.Fatalf("runtime = %+v, want adapter %+v", runtime, adapter)
			}
		})
	}
}

// TestProjectFileInputParamProjectsASRInput 验证 04 asr 任务从冻结输入资产
// 投影绝对路径参数，同时保留 Core 原有的 language/extract 参数。
func TestProjectFileInputParamProjectsASRInput(t *testing.T) {
	e := newEnv(t, envOptions{})
	project := e.seedProject()
	asset := e.seedMediaAsset(project)
	task := domain.Task{
		Kind:             domain.TaskKindNode,
		NodeKey:          domain.NodeKeyExtract,
		InputRevisionIDs: []domain.ID{asset.ID},
		Params: map[string]any{
			"language": "en",
			"extract":  map[string]any{"mode": "asr"},
		},
	}
	params := projectFileInputParam(context.Background(), e.st, task, asrAdapterID, task.Params)
	input, _ := params["input"].(string)
	if input == "" {
		t.Fatalf("asr input not projected: %+v", params)
	}
	if filepath.Base(input) != filepath.Base(asset.Path) {
		t.Fatalf("asr input = %q, want basename %q", input, filepath.Base(asset.Path))
	}
	if params["language"] != "en" {
		t.Fatalf("language = %v, want en", params["language"])
	}
	if _, ok := params["extract"]; !ok {
		t.Fatalf("extract params dropped: %+v", params)
	}
}
