//go:build windows

package scheduler

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/execadapter"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/service"
)

// TestCloneWSLRunnerRealGPU 是手动 GPU E2E：Core 一次脚本任务 -> WSL Runner ->
// voice-clone.py 写出成品。默认跳过；设置 YAOMU_WSL_E2E=1 运行。
func TestCloneWSLRunnerRealGPU(t *testing.T) {
	if os.Getenv("YAOMU_WSL_E2E") != "1" {
		t.Skip("set YAOMU_WSL_E2E=1 to run the WSL GPU clone E2E")
	}
	distro := envOr("YAOMU_WSL_E2E_DISTRO", "yaomu")
	runnerSrc := envOr("YAOMU_WSL_E2E_RUNNER_SRC", defaultWSLRunnerSrc(t))
	flashcli := envOr("YAOMU_WSL_E2E_FLASHCLI", "/usr/local/bin/flashcli")
	cudaTag := envOr("YAOMU_WSL_E2E_CUDA_TAG", "cu130")

	logger, err := logging.New(logging.Options{Path: filepath.Join(t.TempDir(), "executor.log")})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() {
		_ = logger.Close()
		if !t.Failed() {
			return
		}
		data, readErr := os.ReadFile(logger.Path())
		if readErr != nil {
			return
		}
		if len(data) > 12000 {
			data = data[len(data)-12000:]
		}
		t.Logf("executor log tail:\n%s", data)
	})

	ctx := context.Background()
	client, err := execadapter.NewProcessClient(ctx, execadapter.ProcessOptions{
		Command: "wsl.exe",
		Args: []string{
			"-d", distro, "-e", "env",
			"PYTHONPATH=" + runnerSrc,
			"FLASHCLI_BIN=" + flashcli,
			"FLASHCLI_CUDA_TAG=" + cudaTag,
			"YAOMU_CAPABILITY_EXECUTOR=flashcli",
			"python3", "-m", "yaomustudio_runner",
		},
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("start WSL runner: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	executor := &execadapter.RoutingExecutor{
		Model:         &execadapter.Executor{Client: client},
		ModelRequired: true,
	}
	e := newEnv(t, envOptions{executor: executor, pipeline: true})
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		data, err := os.ReadFile(e.log.Path())
		if err != nil {
			return
		}
		if len(data) > 12000 {
			data = data[len(data)-12000:]
		}
		t.Logf("scheduler/core log tail:\n%s", data)
	})
	startScheduler(t, e.sched, ctx)

	project := e.seedProject()
	wavPath := filepath.Join(e.data, "ref.wav")
	writeTestWAV(t, wavPath, 3000)
	asset := e.seedExportFileAsset(project, "ref.wav", domain.AssetTypeAudio, wavPath,
		&domain.MediaMeta{Container: "wav", DurationMs: 3000, AudioTracks: 1})
	seedCloneTrack(t, e, project, "en", domain.TrackTypeTranscription, []domain.Segment{
		{ID: "src-1", StartMs: 400, EndMs: 2160, Text: "At the Helios Beast Academy,"},
	})
	seedCloneTrack(t, e, project, "zh", domain.TrackTypeTranslation, []domain.Segment{
		{ID: "tr-1", StartMs: 400, EndMs: 2160, Text: "欢迎来到曜幕。"},
	})

	resp, err := e.svc.CreateWorkflowRun(ctx, service.Idem{}, service.CreateWorkflowRunInput{
		ProjectID:          string(project),
		Name:               "WSL 脚本克隆 E2E",
		Template:           "tool_voice_clone",
		SourceLanguage:     "en",
		TargetLanguages:    []string{"zh"},
		InputRevisionIDs:   []string{string(asset.ID)},
		ValidateTimestamps: boolPtr(true),
		AdjustDuration:     boolPtr(true),
	})
	if err != nil {
		t.Fatalf("CreateWorkflowRun: %v", err)
	}
	run := resp.Body.(domain.WorkflowRun)
	final := e.waitRunStatus(run.ID, domain.RunStatusSucceeded, 90*time.Second)
	if final.Status != domain.RunStatusSucceeded {
		t.Fatalf("run status = %s", final.Status)
	}

	tasks, err := e.st.ListRunTasks(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunTasks: %v", err)
	}
	var clone *domain.Task
	for i := range tasks {
		if tasks[i].NodeKey == domain.NodeKeyClone && tasks[i].Status == domain.TaskStatusSucceeded {
			clone = &tasks[i]
		}
	}
	if clone == nil || clone.Params["op"] != "script" || len(clone.ArtifactIDs) == 0 {
		t.Fatalf("no successful clone script task with artifacts: %+v", tasks)
	}
	product, err := e.st.GetAsset(ctx, clone.ArtifactIDs[0])
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	data, err := os.ReadFile(product.Path)
	if err != nil {
		t.Fatalf("read product: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("product is empty: %s", product.Path)
	}
	logPath := filepath.Join(e.data, "cache", "execution-logs", string(clone.ID), "attempt-1.log")
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read execution log: %v", err)
	}
	if !strings.Contains(string(logData), "voice-clone.py") {
		t.Fatalf("execution log missing script lines:\n%s", logData)
	}
	if strings.Contains(string(logData), "/mnt/") || strings.Contains(string(logData), "C:\\") {
		t.Fatalf("execution log leaked an absolute path:\n%s", logData)
	}
	t.Logf("clone product: %s %d bytes type=%s", product.Path, len(data), product.Type)
	t.Logf("execution log lines: %d", strings.Count(string(logData), "\n"))
}

func defaultWSLRunnerSrc(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", "..", ".."))
	return execadapter.WindowsToWSL(filepath.Join(repoRoot, "runner", "src"))
}

func writeTestWAV(t *testing.T, path string, durationMs int) {
	t.Helper()
	rate := 16000
	frames := rate * durationMs / 1000
	data := make([]byte, frames*2)
	for i := 0; i < frames; i++ {
		// 非静音参考：serve 默认会移除静音，纯零样本会被判为空参考。
		sample := int16(12000 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)))
		binary.LittleEndian.PutUint16(data[i*2:], uint16(sample))
	}
	header := make([]byte, 44)
	copy(header[0:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+len(data)))
	copy(header[8:], "WAVE")
	copy(header[12:], "fmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 1)
	binary.LittleEndian.PutUint32(header[24:], uint32(rate))
	binary.LittleEndian.PutUint32(header[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(len(data)))
	if err := os.WriteFile(path, append(header, data...), 0o600); err != nil {
		t.Fatalf("write test wav: %v", err)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
