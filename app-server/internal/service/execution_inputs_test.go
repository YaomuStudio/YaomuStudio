package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func TestTranslateExecutionInputUsesSubtitleURL(t *testing.T) {
	e := newExportTestEnv(t)
	ctx := context.Background()
	project := e.seedProject("translate input")
	track := e.seedTrackWithType(project.ID, "en", domain.TrackTypeTranscription,
		[]domain.Segment{
			{ID: "seg-1", StartMs: 0, EndMs: 1000, Text: "Hello"},
			{ID: "seg-2", StartMs: 1000, EndMs: 2000, Text: "World"},
		}, "")
	revision := currentRevision(track)
	if revision == nil {
		t.Fatal("seeded track has no current revision")
	}
	digest := sha256.Sum256([]byte(renderSRT(revision.Segments)))
	task := domain.Task{
		ID:        store.NewID("task"),
		ProjectID: project.ID,
		Kind:      domain.TaskKindNode,
		NodeKey:   domain.NodeKeyTranslate,
		Params: map[string]any{
			"language": "zh",
			"_core": map[string]any{
				"subtitle_revision_id": revision.ID,
				"subtitle_digest":      hex.EncodeToString(digest[:]),
			},
		},
		InputRevisionIDs: []domain.ID{revision.ID},
	}

	params, err := e.svc.PrepareTaskExecution(ctx, task)
	if err != nil {
		t.Fatalf("PrepareTaskExecution: %v", err)
	}
	subtitleURL, _ := params["subtitle_url"].(string)
	if subtitleURL == "" || !filepath.IsAbs(subtitleURL) {
		t.Fatalf("subtitle_url = %q, want absolute path", subtitleURL)
	}
	if !pathWithin(e.svc.workDir(), subtitleURL) {
		t.Fatalf("subtitle_url %q is outside task workspace", subtitleURL)
	}
	if _, exists := params["plan"]; exists {
		t.Fatalf("06 params unexpectedly contain an 08 plan: %+v", params)
	}
	frozen, err := os.ReadFile(subtitleURL)
	if err != nil {
		t.Fatalf("read frozen subtitle: %v", err)
	}
	for _, text := range []string{"Hello", "World"} {
		if !strings.Contains(string(frozen), text) {
			t.Fatalf("frozen subtitle missing %q: %q", text, frozen)
		}
	}

	// 重试复用同一份冻结输入，不重复写盘。
	again, err := e.svc.PrepareTaskExecution(ctx, task)
	if err != nil {
		t.Fatalf("PrepareTaskExecution retry: %v", err)
	}
	if again["subtitle_url"] != subtitleURL {
		t.Fatalf("retry subtitle_url = %q, want %q", again["subtitle_url"], subtitleURL)
	}
}

func TestTranslateExecutionInputRejectsMismatchedFrozenInput(t *testing.T) {
	e := newExportTestEnv(t)
	ctx := context.Background()
	project := e.seedProject("translate mismatch")
	track := e.seedTrackWithType(project.ID, "en", domain.TrackTypeTranscription,
		[]domain.Segment{{ID: "seg-1", StartMs: 0, EndMs: 1000, Text: "Hello"}}, "")
	revision := currentRevision(track)
	other := e.seedProject("translate mismatch other")

	baseTask := func(projectID domain.ID, digestValue string) domain.Task {
		return domain.Task{
			ID:        store.NewID("task"),
			ProjectID: projectID,
			Kind:      domain.TaskKindNode,
			NodeKey:   domain.NodeKeyTranslate,
			Params: map[string]any{
				"language": "zh",
				"_core": map[string]any{
					"subtitle_revision_id": revision.ID,
					"subtitle_digest":      digestValue,
				},
			},
			InputRevisionIDs: []domain.ID{revision.ID},
		}
	}

	if _, err := e.svc.PrepareTaskExecution(ctx, baseTask(project.ID, strings.Repeat("0", 64))); err == nil {
		t.Fatal("accepted a subtitle digest that does not match the frozen revision")
	}
	if _, err := e.svc.PrepareTaskExecution(ctx, baseTask(other.ID,
		hex.EncodeToString(digestBytes(renderSRT(revision.Segments))))); err == nil {
		t.Fatal("accepted a subtitle revision owned by another project")
	}
}

func digestBytes(text string) []byte {
	sum := sha256.Sum256([]byte(text))
	return sum[:]
}
