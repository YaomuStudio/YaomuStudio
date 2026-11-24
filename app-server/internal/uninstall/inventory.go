package uninstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// Inventory returns the read-only page summary. Unknown model ownership is a
// successful inventory with models_known=0; the page keeps that option off.
func Inventory(ctx context.Context, opts Options) (Result, error) {
	state, err := loadInventory(ctx, opts)
	if err != nil {
		return Result{SchemaVersion: SchemaVersion, Status: "failed", MessageKey: "uninstall-error-failed"}, err
	}
	return state.result(), nil
}

// Apply re-inventories, checks the snapshot, backs up exports and performs only
// the selected deletions. Models are cleaned before personal data so a failure
// retains the database evidence needed for a retry.
func Apply(ctx context.Context, opts Options) (Result, error) {
	running, err := opts.processCheck()
	if err != nil {
		fail := failure(6, "uninstall-error-app-running", err)
		return Result{SchemaVersion: SchemaVersion, Status: "failed", MessageKey: fail.MessageKey}, fail
	}
	if running {
		fail := failure(6, "uninstall-error-app-running", fmt.Errorf("application process is still running"))
		return Result{SchemaVersion: SchemaVersion, Status: "failed", MessageKey: fail.MessageKey}, fail
	}
	state, err := loadInventory(ctx, opts)
	if err != nil {
		fail := failure(5, "uninstall-error-failed", err)
		return Result{SchemaVersion: SchemaVersion, Status: "failed", MessageKey: fail.MessageKey}, fail
	}
	result := state.result()
	result.PersonalRequested = opts.Personal
	result.ModelsRequested = opts.Models
	if (opts.Personal || opts.Models) && (opts.Snapshot == "" || opts.Snapshot != state.snapshot) {
		fail := failure(3, "uninstall-error-snapshot-stale", fmt.Errorf("inventory changed"))
		result.Status = "failed"
		result.MessageKey = fail.MessageKey
		return result, fail
	}
	if opts.Models && !state.modelsKnown {
		fail := failure(5, "uninstall-error-models-unknown", fmt.Errorf("model ownership is not safely known"))
		result.Status = "failed"
		result.MessageKey = fail.MessageKey
		return result, fail
	}
	if opts.Personal && state.personalUnknown {
		fail := failure(5, "uninstall-error-personal-unknown", fmt.Errorf("personal data target is unsafe"))
		result.Status = "failed"
		result.MessageKey = fail.MessageKey
		return result, fail
	}
	backupStatus, backupPath, err := backupExports(opts.InstallDir, opts.UserDataDir, opts.DocumentsDir, opts.now())
	result.BackupStatus = backupStatus
	result.BackupPath = backupPath
	if err != nil {
		fail := failure(4, "uninstall-error-backup-failed", err)
		result.Status = "failed"
		result.MessageKey = fail.MessageKey
		return result, fail
	}
	fresh, err := loadInventory(ctx, opts)
	if err != nil {
		fail := failure(5, "uninstall-error-failed", err)
		result.Status = "failed"
		result.MessageKey = fail.MessageKey
		return result, fail
	}
	if (opts.Personal || opts.Models) && fresh.snapshot != state.snapshot {
		fail := failure(3, "uninstall-error-snapshot-stale", fmt.Errorf("inventory changed during backup"))
		result.Status = "failed"
		result.MessageKey = fail.MessageKey
		return result, fail
	}
	state = fresh
	if opts.Models {
		if state.modelRoot == "" && len(state.modelFiles) > 0 {
			fail := failure(5, "uninstall-error-models-unknown", fmt.Errorf("model root unavailable"))
			result.Status = "failed"
			result.MessageKey = fail.MessageKey
			return result, fail
		}
		deleted, err := deleteModelFiles(state.modelRoot, state.modelFiles)
		result.ModelsDeleted = deleted
		if err != nil {
			fail := failure(5, "uninstall-error-partial", err)
			result.Status = "failed"
			result.MessageKey = fail.MessageKey
			return result, fail
		}
	}
	if opts.Personal {
		deleted, err := deletePersonalFiles(opts.UserDataDir, state.personalFiles)
		result.PersonalDeleted = deleted
		if err != nil {
			fail := failure(5, "uninstall-error-partial", err)
			result.Status = "failed"
			result.MessageKey = fail.MessageKey
			return result, fail
		}
	}
	result.Status = "ok"
	result.MessageKey = ""
	return result, nil
}

func loadInventory(ctx context.Context, opts Options) (*inventoryState, error) {
	personalFiles, personalBytes, personalUnknown := inventoryPersonal(opts.UserDataDir)
	models, _ := collectModelInventory(ctx, opts)
	modelsDisplay := ""
	if models.Known {
		modelsDisplay = formatBytes(models.Bytes)
	}
	state := &inventoryState{
		personalFiles:     personalFiles,
		personalBytes:     personalBytes,
		personalUnknown:   personalUnknown,
		modelFiles:        models.Files,
		modelRoot:         models.Root,
		modelsBytes:       models.Bytes,
		modelsDownload:    models.DownloadBytes,
		modelsKnown:       models.Known,
		modelsSizeDisplay: modelsDisplay,
	}
	state.snapshot = snapshotOf(state)
	return state, nil
}

func snapshotOf(state *inventoryState) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "schema=%d\npersonal_unknown=%t\nmodels_known=%t\n",
		SchemaVersion, state.personalUnknown, state.modelsKnown)
	files := append(append([]fileTarget{}, state.personalFiles...), state.modelFiles...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, file := range files {
		fmt.Fprintf(hash, "%s|%d|%d\n", file.Path, file.Size, file.ModTime)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
