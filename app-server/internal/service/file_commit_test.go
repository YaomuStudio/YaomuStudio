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

func TestFileCommitRecovery(t *testing.T) {
	cases := []struct {
		name       string
		managed    bool
		registered bool
		missing    bool
	}{
		{name: "remove owned orphan", managed: true},
		{name: "preserve user output orphan"},
		{name: "retain committed file", managed: true, registered: true},
		{name: "mark missing committed file", managed: true, registered: true, missing: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			e := newExportTestEnv(t)
			project := e.seedProject("recovery")
			assetID := store.NewID("asset")
			dir := t.TempDir()
			if test.managed {
				dir = filepath.Join(e.svc.mediaDir(), project.ID)
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, assetID+".wav")
			intent, err := e.svc.beginFileIntent(assetID, path, test.managed)
			if err != nil {
				t.Fatal(err)
			}
			if !test.missing {
				if err := os.WriteFile(path, []byte("owned-output"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.registered {
				if err := e.store.DoTx(context.Background(), func(tx *store.Tx) error {
					return e.svc.createAssetRow(context.Background(), tx, project.ID, assetID,
						"output.wav", path, domain.LocationSourceGenerated, domain.LocationOwnerApp,
						12, "", importProbe{}, e.svc.Now())
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.svc.RecoverArtifactFiles(context.Background()); err != nil {
				t.Fatal(err)
			}
			_, fileErr := os.Stat(path)
			shouldExist := !test.missing && (!test.managed || test.registered)
			if shouldExist != (fileErr == nil) {
				t.Fatalf("file exists=%v, want %v", fileErr == nil, shouldExist)
			}
			if !test.managed {
				if _, err := os.Stat(intent.journal + ".orphan"); err != nil {
					t.Fatal(err)
				}
			} else if _, err := os.Stat(intent.journal); !os.IsNotExist(err) {
				t.Fatalf("journal not resolved: %v", err)
			}
			if test.registered && test.missing {
				asset, err := e.store.GetAsset(context.Background(), assetID)
				if err != nil {
					t.Fatal(err)
				}
				if asset.Status != domain.AssetStatusCorrupt {
					t.Fatalf("status=%s", asset.Status)
				}
			}
		})
	}
}

func TestFileCommitRecoveryRejectsUnownedPath(t *testing.T) {
	e := newExportTestEnv(t)
	path := e.tempFile("original.wav", "user-original")
	if _, err := e.svc.beginFileIntent(store.NewID("asset"), path, true); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RecoverArtifactFiles(context.Background()); err == nil {
		t.Fatal("accepted unowned managed path")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "user-original" {
		t.Fatalf("user file changed: %v", err)
	}
}

func TestExecutionInputIsFrozenOutsideDatabase(t *testing.T) {
	e := newExportTestEnv(t)
	ctx := context.Background()
	project := e.seedProject("freeze")
	source := e.tempFile("source.wav", "AAAA")
	response, err := e.svc.ImportAssets(ctx, Idem{}, AssetImportRequest{ProjectID: project.ID, FileGrants: []string{source}})
	if err != nil {
		t.Fatal(err)
	}
	id := response.Body.(AssetImportResult).AssetIDs[0]
	task := domain.Task{
		ID: store.NewID("task"), ProjectID: project.ID, InputRevisionIDs: []domain.ID{id},
		Params: map[string]any{"plan": map[string]any{"op": "mix", "inputs": []any{map[string]any{"path": source}}}},
	}
	params, err := e.svc.PrepareTaskExecution(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	plan := params["plan"].(map[string]any)
	input := plan["inputs"].([]any)[0].(map[string]any)["path"].(string)
	if input == source || !pathWithin(e.svc.workDir(), input) {
		t.Fatal("execution did not receive an owned snapshot")
	}
	if err := os.WriteFile(source, []byte("BBBB"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(input)
	if err != nil || string(data) != "AAAA" {
		t.Fatalf("frozen input changed: %q %v", data, err)
	}
	// A retry retains the same immutable content even when the original is changed.
	if _, err := e.svc.PrepareTaskExecution(ctx, task); err != nil {
		t.Fatal(err)
	}
	original := task.Params["plan"].(map[string]any)["inputs"].([]any)[0].(map[string]any)["path"]
	if original != source {
		t.Fatal("persisted task parameters were mutated")
	}
}

func TestStagedExecutionInputRequiresFrozenContent(t *testing.T) {
	e := newExportTestEnv(t)
	ctx := context.Background()
	project := e.seedProject("staged subtitle")
	track := e.seedTrack(project.ID, "zh", []domain.Segment{{
		ID: "segment", StartMs: 0, EndMs: 1000, Text: "frozen",
	}})
	revision := currentRevision(track)
	digest := sha256.Sum256([]byte(renderSRT(revision.Segments)))
	task := domain.Task{
		ID: store.NewID("task"), ProjectID: project.ID,
		Params: map[string]any{
			"plan": map[string]any{
				"op": "video_merge", "subtitle": stagedSubtitlePlaceholder, "out": "preview.mp4",
			},
			"_core": map[string]any{
				"subtitle_revision_id": revision.ID,
				"subtitle_digest":      hex.EncodeToString(digest[:]),
			},
		},
	}
	params, err := e.svc.PrepareTaskExecution(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	frozen := params["plan"].(map[string]any)["subtitle"].(string)
	if !pathWithin(e.svc.workDir(), frozen) {
		t.Fatal("subtitle was not materialized into task ownership")
	}
	data, err := os.ReadFile(frozen)
	if err != nil || !strings.Contains(string(data), "frozen") {
		t.Fatalf("frozen subtitle = %q err=%v", data, err)
	}
	task.Params["_core"].(map[string]any)["subtitle_digest"] = strings.Repeat("0", 64)
	task.ID = store.NewID("task")
	if _, err := e.svc.PrepareTaskExecution(ctx, task); err == nil {
		t.Fatal("subtitle with a mismatched frozen digest was accepted")
	}
}
