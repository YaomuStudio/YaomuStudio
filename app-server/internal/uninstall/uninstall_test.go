package uninstall

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	_ "modernc.org/sqlite"
)

func testOptions(t *testing.T) (Options, string) {
	t.Helper()
	base := t.TempDir()
	userData := filepath.Join(base, "userData")
	installDir := filepath.Join(base, "install")
	documents := filepath.Join(base, "Documents")
	flashRoot := filepath.Join(base, "flashcli")
	modelKey := strings.Repeat("a", 64)
	for _, dir := range []string{
		filepath.Join(userData, "core-data", "media"),
		filepath.Join(userData, "core-data", "models"),
		filepath.Join(userData, "core-data", "runtime"),
		filepath.Join(userData, "wsl"),
		filepath.Join(userData, "core-data", "models", "releases", modelKey),
		filepath.Join(installDir, "outputs"),
		documents,
		filepath.Join(flashRoot, "bundles", "melband-roformer", "1.0.10"),
		filepath.Join(flashRoot, "models", "store", "melband"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(userData, "core-data", "media", "project.bin"), "personal")
	write(filepath.Join(userData, "desktop-preferences.json"), "prefs")
	write(filepath.Join(userData, "core-data", "models", "keep.bin"), "model-package")
	write(filepath.Join(userData, "core-data", "runtime", "keep.bin"), "runtime")
	write(filepath.Join(userData, "wsl", "keep.bin"), "wsl")
	write(filepath.Join(userData, "core-data", "models", "releases", modelKey, ".yaomu-flashcli-prepared"),
		"xym/melband-roformer:1.0.10\n")
	write(filepath.Join(installDir, "outputs", "out.txt"), "export")
	write(filepath.Join(flashRoot, "bundles", "melband-roformer", "1.0.10", "flashcli-bundle.json"),
		`{"format_version":4,"defaults":{"weights":{"repo":"cpadyun/melband-roformer"}}}`)
	write(filepath.Join(flashRoot, "models", "store", "melband", ".flashcli_download.json"),
		`{"repo":"cpadyun/melband-roformer","files":[{"path":"a.bin","size":4},{"path":"b.bin","size":6}]}`)
	write(filepath.Join(flashRoot, "models", "store", "melband", "a.bin"), "aaaa")
	write(filepath.Join(flashRoot, "models", "store", "melband", "b.bin"), "bbbbbb")
	db, err := sql.Open("sqlite", filepath.Join(userData, "core-data", "yaomu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE installations (id TEXT PRIMARY KEY, slot TEXT, phase TEXT, plan_id TEXT, path_owner TEXT, verified_files INTEGER, storage_key TEXT)`,
		`CREATE TABLE installation_slot_bindings (installation_id TEXT, slot TEXT, is_current INTEGER, activated_at TEXT)`,
		`CREATE TABLE app_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO installations (id, slot, phase, plan_id, path_owner, verified_files, storage_key) VALUES ('inst-1', 'separation', 'done', 'plan-1', 'app', 1, '` + modelKey + `')`,
		`INSERT INTO installation_slot_bindings (installation_id, slot, is_current, activated_at) VALUES ('inst-1', 'separation', 1, '2026-11-24T12:00:00Z')`,
		`INSERT INTO app_meta (key, value) VALUES ('installation_plan.plan-1', '{"flashcli_ref":"xym/melband-roformer:1.0.10"}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return Options{
		UserDataDir:  userData,
		InstallDir:   installDir,
		DocumentsDir: documents,
		FlashCLIRoot: flashRoot,
		Now:          func() time.Time { return time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC) },
		ProcessCheck: func() (bool, error) { return false, nil },
	}, base
}

func TestInventoryAndApplySelections(t *testing.T) {
	opts, _ := testOptions(t)
	inventory, err := Inventory(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !inventory.ModelsKnown || inventory.ModelsBytes != 10 || inventory.ModelsDownloadBytes != 10 {
		t.Fatalf("unexpected model inventory: %+v", inventory)
	}
	if inventory.PersonalBytes == 0 || inventory.Snapshot == "" {
		t.Fatalf("unexpected personal inventory: %+v", inventory)
	}
	opts.Personal = true
	opts.Models = true
	opts.Snapshot = inventory.Snapshot
	result, err := Apply(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" || result.BackupStatus != "ok" || result.BackupPath == "" {
		t.Fatalf("unexpected apply result: %+v", result)
	}
	backup := filepath.Join(result.BackupPath, "program-outputs", "out.txt")
	if raw, err := os.ReadFile(backup); err != nil || string(raw) != "export" {
		t.Fatalf("backup missing: %v %q", err, raw)
	}
	for _, gone := range []string{
		filepath.Join(opts.UserDataDir, "core-data", "media", "project.bin"),
		filepath.Join(opts.UserDataDir, "desktop-preferences.json"),
		filepath.Join(opts.FlashCLIRoot, "models", "store", "melband", "a.bin"),
		filepath.Join(opts.FlashCLIRoot, "models", "store", "melband", "b.bin"),
	} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Fatalf("expected removed: %s", gone)
		}
	}
	for _, kept := range []string{
		filepath.Join(opts.UserDataDir, "core-data", "models", "keep.bin"),
		filepath.Join(opts.UserDataDir, "core-data", "runtime", "keep.bin"),
		filepath.Join(opts.UserDataDir, "wsl", "keep.bin"),
		filepath.Join(opts.FlashCLIRoot, "bundles", "melband-roformer", "1.0.10", "flashcli-bundle.json"),
		filepath.Join(opts.FlashCLIRoot, "models", "store", "melband", ".flashcli_download.json"),
	} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("expected kept: %s: %v", kept, err)
		}
	}
}

func TestSnapshotMismatchRefusesCleanup(t *testing.T) {
	opts, _ := testOptions(t)
	inventory, err := Inventory(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Personal = true
	opts.Snapshot = inventory.Snapshot
	if err := os.WriteFile(filepath.Join(opts.UserDataDir, "core-data", "media", "late.bin"), []byte("late"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Apply(context.Background(), opts)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != 3 {
		t.Fatalf("expected stale failure, got %v", err)
	}
	if result.MessageKey != "uninstall-error-snapshot-stale" {
		t.Fatalf("stale failure message = %q", result.MessageKey)
	}
	if _, err := os.Stat(filepath.Join(opts.UserDataDir, "core-data", "media", "project.bin")); err != nil {
		t.Fatal("personal data changed on stale snapshot")
	}
}

func TestPersonalRequiresSnapshot(t *testing.T) {
	opts, _ := testOptions(t)
	opts.Personal = true
	result, err := Apply(context.Background(), opts)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != 3 {
		t.Fatalf("expected missing snapshot failure, got %v", err)
	}
	if result.MessageKey != "uninstall-error-snapshot-stale" {
		t.Fatalf("missing snapshot message = %q", result.MessageKey)
	}
}

func TestUnknownModelRetainsEverything(t *testing.T) {
	opts, _ := testOptions(t)
	if err := os.WriteFile(filepath.Join(opts.FlashCLIRoot, "bundles", "melband-roformer", "1.0.10", "flashcli-bundle.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	inventory, err := Inventory(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.ModelsKnown {
		t.Fatal("broken bundle was reported known")
	}
	opts.Models = true
	opts.Snapshot = inventory.Snapshot
	_, err = Apply(context.Background(), opts)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != 5 {
		t.Fatalf("expected unknown failure, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(opts.FlashCLIRoot, "models", "store", "melband", "a.bin")); err != nil {
		t.Fatal("weight changed on unknown inventory")
	}
}

func TestPathEscapeIsUnknown(t *testing.T) {
	opts, _ := testOptions(t)
	if err := os.WriteFile(filepath.Join(opts.FlashCLIRoot, "models", "store", "melband", ".flashcli_download.json"),
		[]byte(`{"repo":"cpadyun/melband-roformer","files":[{"path":"../escape.bin","size":4}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	inventory, err := Inventory(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.ModelsKnown {
		t.Fatal("path escape was reported known")
	}
}

func TestBackupFailureStopsBeforeDeletion(t *testing.T) {
	opts, _ := testOptions(t)
	if err := os.RemoveAll(opts.DocumentsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opts.DocumentsDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	inventory, err := Inventory(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Personal = true
	opts.Snapshot = inventory.Snapshot
	_, err = Apply(context.Background(), opts)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != 4 {
		t.Fatalf("expected backup failure, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(opts.UserDataDir, "core-data", "media", "project.bin")); err != nil {
		t.Fatal("personal data changed after backup failure")
	}
}

func TestNoSelectionsStillBacksUp(t *testing.T) {
	opts, _ := testOptions(t)
	result, err := Apply(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupStatus != "ok" || result.PersonalDeleted || result.ModelsDeleted {
		t.Fatalf("unexpected backup-only result: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(result.BackupPath, "program-outputs", "out.txt")); err != nil {
		t.Fatal("backup missing")
	}
	if _, err := os.Stat(filepath.Join(opts.FlashCLIRoot, "models", "store", "melband", "a.bin")); err != nil {
		t.Fatal("model changed without selection")
	}
}

func TestRunningApplicationFailsClosed(t *testing.T) {
	opts, _ := testOptions(t)
	opts.ProcessCheck = func() (bool, error) { return true, nil }
	_, err := Apply(context.Background(), opts)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != 6 {
		t.Fatalf("expected app-running failure, got %v", err)
	}
}

func TestPersonalOnlyKeepsModels(t *testing.T) {
	opts, _ := testOptions(t)
	inventory, err := Inventory(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Personal = true
	opts.Snapshot = inventory.Snapshot
	if _, err := Apply(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(opts.FlashCLIRoot, "models", "store", "melband", "a.bin")); err != nil {
		t.Fatal("personal-only apply removed model weights")
	}
}

func TestModelsOnlyKeepsPersonal(t *testing.T) {
	opts, _ := testOptions(t)
	inventory, err := Inventory(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Models = true
	opts.Snapshot = inventory.Snapshot
	if _, err := Apply(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(opts.UserDataDir, "core-data", "media", "project.bin")); err != nil {
		t.Fatal("models-only apply removed personal data")
	}
	if _, err := os.Stat(filepath.Join(opts.UserDataDir, "core-data", "yaomu.db")); err != nil {
		t.Fatal("models-only apply removed the database")
	}
}

func TestPartialWeightCountsExisting(t *testing.T) {
	opts, _ := testOptions(t)
	if err := os.Remove(filepath.Join(opts.FlashCLIRoot, "models", "store", "melband", "a.bin")); err != nil {
		t.Fatal(err)
	}
	inventory, err := Inventory(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !inventory.ModelsKnown || inventory.ModelsBytes != 6 || inventory.ModelsDownloadBytes != 10 {
		t.Fatalf("unexpected partial inventory: %+v", inventory)
	}
}

func TestMissingDatabaseKeepsModelsUnknown(t *testing.T) {
	opts, _ := testOptions(t)
	if err := os.Remove(filepath.Join(opts.UserDataDir, "core-data", "yaomu.db")); err != nil {
		t.Fatal(err)
	}
	inventory, err := Inventory(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.ModelsKnown {
		t.Fatal("missing database was treated as proof of no models")
	}
}

func TestModelEvidenceMissingKeepsUnknown(t *testing.T) {
	opts, _ := testOptions(t)
	key := strings.Repeat("a", 64)
	if err := os.Remove(filepath.Join(opts.UserDataDir, "core-data", "models", "releases", key, ".yaomu-flashcli-prepared")); err != nil {
		t.Fatal(err)
	}
	inventory, err := Inventory(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.ModelsKnown {
		t.Fatal("missing prepare marker was treated as model ownership")
	}
}

func TestModelBindingMissingKeepsUnknown(t *testing.T) {
	opts, _ := testOptions(t)
	db, err := sql.Open("sqlite", filepath.Join(opts.UserDataDir, "core-data", "yaomu.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM installation_slot_bindings`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	inventory, err := Inventory(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.ModelsKnown {
		t.Fatal("missing installation binding was treated as model ownership")
	}
}

func TestStoppedWSLIsNotStarted(t *testing.T) {
	opts, _ := testOptions(t)
	opts.FlashCLIRoot = ""
	opts.WSLDistro = "yaomu"
	units := utf16.Encode([]rune("  NAME STATE VERSION\r\n* yaomu Stopped 2\r\n"))
	data := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(data[i*2:], unit)
	}
	var calls [][]string
	opts.RunCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return data, nil
	}
	running, err := wslDistroRunning(context.Background(), opts, "yaomu")
	if err != nil {
		t.Fatal(err)
	}
	if running {
		t.Fatal("stopped WSL distribution was accepted")
	}
	if len(calls) != 1 || calls[0][1] != "-l" || calls[0][2] != "-v" {
		t.Fatalf("unexpected WSL calls: %#v", calls)
	}
}

func TestHardLinkKeepsModelsUnknown(t *testing.T) {
	opts, _ := testOptions(t)
	source := filepath.Join(opts.FlashCLIRoot, "models", "store", "melband", "a.bin")
	alias := filepath.Join(opts.FlashCLIRoot, "models", "store", "melband", "alias.bin")
	if err := os.Link(source, alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	inventory, err := Inventory(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.ModelsKnown {
		t.Fatal("hard-linked weight was treated as exclusively owned")
	}
}

func TestBackupDoesNotOverwrite(t *testing.T) {
	opts, _ := testOptions(t)
	first, err := Apply(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Apply(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.BackupPath == second.BackupPath {
		t.Fatalf("backup overwrote existing target: %s", first.BackupPath)
	}
	for _, path := range []string{first.BackupPath, second.BackupPath} {
		if _, err := os.Stat(filepath.Join(path, "program-outputs", "out.txt")); err != nil {
			t.Fatalf("backup missing: %s: %v", path, err)
		}
	}
}

func TestUnknownTopLevelPreserved(t *testing.T) {
	opts, _ := testOptions(t)
	unknown := filepath.Join(opts.UserDataDir, "unknown-user-file.txt")
	if err := os.WriteFile(unknown, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	inventory, err := Inventory(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Personal = true
	opts.Snapshot = inventory.Snapshot
	if _, err := Apply(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatal("unknown top-level file was removed")
	}
}

func TestSymlinkRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires extra privileges on Windows")
	}
	opts, _ := testOptions(t)
	link := filepath.Join(opts.UserDataDir, "core-data", "media", "link.bin")
	if err := os.Symlink(filepath.Join(opts.UserDataDir, "unknown-user-file.txt"), link); err != nil {
		t.Fatal(err)
	}
	inventory, err := Inventory(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !inventory.ModelsKnown || inventory.PersonalBytes == 0 {
		t.Fatalf("unexpected inventory: %+v", inventory)
	}
	opts.Personal = true
	opts.Snapshot = inventory.Snapshot
	_, err = Apply(context.Background(), opts)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != 5 {
		t.Fatalf("expected unsafe personal failure, got %v", err)
	}
}
