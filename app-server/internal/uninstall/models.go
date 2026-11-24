package uninstall

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf16"

	_ "modernc.org/sqlite"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

const flashCLIPrepareMarkerName = ".yaomu-flashcli-prepared"

type modelInventory struct {
	Files         []fileTarget
	Bytes         int64
	DownloadBytes int64
	Known         bool
	Root          string
}

func collectModelInventory(ctx context.Context, opts Options) (modelInventory, error) {
	coreData := filepath.Join(opts.UserDataDir, "core-data")
	if _, err := os.Stat(coreData); err != nil {
		if os.IsNotExist(err) {
			return modelInventory{Known: true}, nil
		}
		return modelInventory{Known: false}, nil
	}
	dbPath := filepath.Join(coreData, "yaomu.db")
	if err := checkNoReparse(coreData, dbPath); err != nil {
		return modelInventory{Known: false}, nil
	}
	refs, known, err := readOwnedRefs(ctx, dbPath, coreData)
	if err != nil || !known {
		return modelInventory{Known: false}, nil
	}
	if len(refs) == 0 {
		return modelInventory{Known: true}, nil
	}
	root, err := resolveFlashCLIRoot(ctx, opts)
	if err != nil {
		return modelInventory{Known: false}, nil
	}
	appRepos := map[string]struct{}{}
	for _, ref := range refs {
		repos, err := bundleRepos(root, ref)
		if err != nil {
			return modelInventory{Known: false}, nil
		}
		for repo := range repos {
			appRepos[repo] = struct{}{}
		}
	}
	if len(appRepos) == 0 {
		return modelInventory{Known: true}, nil
	}
	modelsDir := filepath.Join(root, "models")
	if _, err := os.Stat(modelsDir); err != nil {
		if os.IsNotExist(err) {
			return modelInventory{Known: true}, nil
		}
		return modelInventory{Known: false}, nil
	}
	allRepos := map[string]map[string]struct{}{}
	downloadSizes := map[string]int64{}
	err = filepath.WalkDir(modelsDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if link, err := isReparsePoint(path); err != nil {
			return err
		} else if link {
			return fmt.Errorf("reparse point refused")
		}
		if entry.IsDir() || entry.Name() != ".flashcli_download.json" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var doc struct {
			Repo  string `json:"repo"`
			Files []struct {
				Path string `json:"path"`
				Size int64  `json:"size"`
			} `json:"files"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("decode download manifest: %w", err)
		}
		repo := strings.TrimSpace(doc.Repo)
		if repo == "" {
			return fmt.Errorf("download manifest has no repo")
		}
		dir := filepath.Dir(path)
		for _, file := range doc.Files {
			if file.Path == "" || strings.Contains(file.Path, `\`) || filepath.IsAbs(file.Path) {
				return fmt.Errorf("download manifest path is unsafe")
			}
			clean := filepath.Clean(filepath.FromSlash(file.Path))
			if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return fmt.Errorf("download manifest path escaped")
			}
			target := filepath.Join(dir, clean)
			if _, ok := within(dir, target); !ok {
				return fmt.Errorf("download manifest target escaped")
			}
			if allRepos[target] == nil {
				allRepos[target] = map[string]struct{}{}
			}
			allRepos[target][repo] = struct{}{}
			if _, owned := appRepos[repo]; owned {
				if file.Size > 0 && file.Size > downloadSizes[target] {
					downloadSizes[target] = file.Size
				}
			}
		}
		return nil
	})
	if err != nil {
		return modelInventory{Known: false}, nil
	}
	var files []fileTarget
	var downloadBytes int64
	for target, repos := range allRepos {
		shared := false
		for repo := range repos {
			if _, owned := appRepos[repo]; !owned {
				shared = true
				break
			}
		}
		if shared {
			continue
		}
		downloadBytes += downloadSizes[target]
		if err := checkNoReparse(root, target); err != nil {
			return modelInventory{Known: false}, nil
		}
		info, err := os.Lstat(target)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return modelInventory{Known: false}, nil
		}
		if !info.Mode().IsRegular() {
			return modelInventory{Known: false}, nil
		}
		links, err := fileLinkCount(target)
		if err != nil || links > 1 {
			return modelInventory{Known: false}, nil
		}
		files = append(files, fileTarget{Path: target, Size: info.Size(), ModTime: info.ModTime().UnixNano()})
	}
	files = dedupeFiles(files)
	return modelInventory{
		Files:         files,
		Bytes:         sumFiles(files),
		DownloadBytes: downloadBytes,
		Known:         true,
		Root:          root,
	}, nil
}

func readOwnedRefs(ctx context.Context, dbPath, coreData string) ([]string, bool, error) {
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	dsn := "file:" + filepath.ToSlash(dbPath) + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, false, nil
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, false, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, plan_id, path_owner, verified_files, storage_key
		FROM installations
		WHERE phase = 'done' AND plan_id IS NOT NULL AND plan_id <> ''`)
	if err != nil {
		return nil, false, nil
	}
	defer rows.Close()
	type ownedRow struct {
		id            string
		planID        string
		pathOwner     string
		verifiedFiles int64
		storageKey    string
	}
	var rowsData []ownedRow
	for rows.Next() {
		var (
			row     ownedRow
			owner   sql.NullString
			storage sql.NullString
		)
		if err := rows.Scan(&row.id, &row.planID, &owner, &row.verifiedFiles, &storage); err != nil {
			return nil, false, nil
		}
		row.pathOwner = owner.String
		row.storageKey = storage.String
		rowsData = append(rowsData, row)
	}
	if err := rows.Err(); err != nil {
		return nil, false, nil
	}
	if err := rows.Close(); err != nil {
		return nil, false, nil
	}
	seen := map[string]struct{}{}
	var refs []string
	for _, row := range rowsData {
		var raw string
		err := db.QueryRowContext(ctx,
			`SELECT value FROM app_meta WHERE key = ?`, "installation_plan."+row.planID,
		).Scan(&raw)
		if err != nil {
			if err == sql.ErrNoRows {
				return nil, false, nil
			}
			return nil, false, nil
		}
		var plan struct {
			FlashCLIRef string `json:"flashcli_ref"`
		}
		if err := json.Unmarshal([]byte(raw), &plan); err != nil {
			return nil, false, nil
		}
		ref := strings.TrimSpace(plan.FlashCLIRef)
		if ref == "" {
			continue
		}
		if !domain.AdmittedFlashCLIRef(ref) ||
			row.pathOwner != "app" ||
			row.verifiedFiles <= 0 ||
			!hex64(row.storageKey) {
			return nil, false, nil
		}
		var bindings int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM installation_slot_bindings WHERE installation_id = ?`, row.id,
		).Scan(&bindings); err != nil || bindings == 0 {
			return nil, false, nil
		}
		marker := filepath.Join(coreData, "models", "releases", row.storageKey, flashCLIPrepareMarkerName)
		if err := checkNoReparse(coreData, marker); err != nil {
			return nil, false, nil
		}
		markerRaw, err := os.ReadFile(marker)
		if err != nil || strings.TrimSpace(string(markerRaw)) != ref {
			return nil, false, nil
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs, true, nil
}

func resolveFlashCLIRoot(ctx context.Context, opts Options) (string, error) {
	if strings.TrimSpace(opts.FlashCLIRoot) != "" {
		return absPath(opts.FlashCLIRoot)
	}
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("WSL root is only available on Windows")
	}
	distro := strings.TrimSpace(opts.WSLDistro)
	if distro == "" {
		distro = strings.TrimSpace(os.Getenv("YAOMU_WSL_DISTRO"))
	}
	if distro == "" {
		distro = "yaomu"
	}
	running, err := wslDistroRunning(ctx, opts, distro)
	if err != nil || !running {
		return "", fmt.Errorf("WSL distribution is not confirmed running")
	}
	out, err := opts.runCommand(ctx, "wsl.exe", "-d", distro, "-e", "python3", "-c",
		"import os; print(os.path.expanduser('~'))")
	if err != nil {
		return "", err
	}
	home := strings.TrimSpace(strings.ReplaceAll(string(out), "\r", ""))
	if !strings.HasPrefix(home, "/") || strings.Contains(home, "..") {
		return "", fmt.Errorf("invalid WSL home")
	}
	root := `\\wsl$\` + distro + strings.ReplaceAll(home, "/", `\`) + `\.flashcli`
	return root, nil
}

func wslDistroRunning(ctx context.Context, opts Options, distro string) (bool, error) {
	out, err := opts.runCommand(ctx, "wsl.exe", "-l", "-v")
	if err != nil {
		return false, err
	}
	text := decodeWSLOutput(out)
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		name, state := fields[0], fields[1]
		if name == "*" && len(fields) >= 3 {
			name, state = fields[1], fields[2]
		}
		if name != distro {
			continue
		}
		if strings.EqualFold(state, "Running") {
			return true, nil
		}
		if strings.EqualFold(state, "Stopped") {
			return false, nil
		}
		return false, fmt.Errorf("unknown WSL state")
	}
	return false, fmt.Errorf("WSL distribution not found")
}

func decodeWSLOutput(data []byte) string {
	data = bytes.TrimPrefix(data, []byte{0xff, 0xfe})
	if bytes.IndexByte(data, 0) >= 0 {
		units := make([]uint16, 0, len(data)/2)
		for i := 0; i+1 < len(data); i += 2 {
			units = append(units, binary.LittleEndian.Uint16(data[i:i+2]))
		}
		return string(utf16.Decode(units))
	}
	return string(data)
}

func hex64(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func bundleRepos(root, ref string) (map[string]struct{}, error) {
	name, version, ok := refParts(ref)
	if !ok {
		return nil, fmt.Errorf("invalid ref")
	}
	path := filepath.Join(root, "bundles", name, version, "flashcli-bundle.json")
	if err := checkNoReparse(root, path); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Defaults struct {
			Weights struct {
				Repo string `json:"repo"`
			} `json:"weights"`
			Extra map[string]struct {
				Repo string `json:"repo"`
			} `json:"extra_weights"`
		} `json:"defaults"`
		Weights struct {
			Repo string `json:"repo"`
		} `json:"weights"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	repos := map[string]struct{}{}
	if repo := strings.TrimSpace(doc.Defaults.Weights.Repo); repo != "" {
		repos[repo] = struct{}{}
	}
	if repo := strings.TrimSpace(doc.Weights.Repo); repo != "" {
		repos[repo] = struct{}{}
	}
	for _, extra := range doc.Defaults.Extra {
		if repo := strings.TrimSpace(extra.Repo); repo != "" {
			repos[repo] = struct{}{}
		}
	}
	return repos, nil
}

func refParts(ref string) (string, string, bool) {
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if slash < 0 || colon <= slash+1 || colon >= len(ref)-1 {
		return "", "", false
	}
	name := ref[slash+1 : colon]
	version := ref[colon+1:]
	if !safeSegment(name) || !safeSegment(version) {
		return "", "", false
	}
	return name, version, true
}

func safeSegment(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '.' || char == '_' || char == '-':
		default:
			return false
		}
	}
	return true
}

func deleteModelFiles(root string, files []fileTarget) (bool, error) {
	deleted := false
	for _, file := range files {
		if err := checkNoReparse(root, file.Path); err != nil {
			return deleted, err
		}
		info, err := os.Lstat(file.Path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return deleted, err
		}
		if !info.Mode().IsRegular() {
			return deleted, fmt.Errorf("non-regular weight target refused")
		}
		if info.Size() != file.Size || info.ModTime().UnixNano() != file.ModTime {
			return deleted, fmt.Errorf("weight target changed")
		}
		if err := os.Remove(file.Path); err != nil {
			return deleted, err
		}
		deleted = true
	}
	return deleted, nil
}
