package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

const (
	flashCLIPrepareMarkerName = ".yaomu-flashcli-prepared"
	// flashCLIPrepareTimeout 是一次 WSL `flashcli pull` 的等待上限。
	flashCLIPrepareTimeout = time.Hour
	// flashCLIQueryTimeout 是 `flashcli models list` 的等待上限。
	flashCLIQueryTimeout = 30 * time.Second
	// flashCLIRemoveTimeout 是 `flashcli bundle clean` 的等待上限。
	flashCLIRemoveTimeout = 2 * time.Minute
	flashCLIListCacheTTL  = 15 * time.Second
)

var flashCLIPrepareEnvKeys = []string{
	"FLASHCLI_BIN",
	"FLASHCLI_CUDA_TAG",
	"FLASHCLI_CHECKPOINT",
	"CUDA_VISIBLE_DEVICES",
}

// prepareFlashCLIModel 用桌面注入的 WSL Runner 参数调用已准入的
// `flashcli pull <ref>`。不新增 FlashCLI 开关，也不记录命令输出。
func prepareFlashCLIModel(ctx context.Context, ref string) error {
	if !domain.AdmittedFlashCLIRef(ref) {
		return fmt.Errorf("flashcli ref is not admitted")
	}
	command, runnerArgs, err := flashCLIRunnerLaunch()
	if err != nil {
		return err
	}
	args, err := buildFlashCLIPullArgs(runnerArgs, ref)
	if err != nil {
		return err
	}
	return runFlashCLI(ctx, command, args, flashCLIPrepareTimeout, nil)
}

// ListFlashCLICachedModels 执行 `flashcli models list`，返回已准入 ref 的权重是否为 cached。
func ListFlashCLICachedModels(ctx context.Context) (map[string]bool, error) {
	if cached, ok := cachedFlashCLIModels(); ok {
		return cached, nil
	}
	command, runnerArgs, err := flashCLIRunnerLaunch()
	if err != nil {
		return nil, err
	}
	args, err := buildFlashCLIModelsListArgs(runnerArgs)
	if err != nil {
		return nil, err
	}
	var stdout bytes.Buffer
	if err := runFlashCLI(ctx, command, args, flashCLIQueryTimeout, &stdout); err != nil {
		return nil, err
	}
	parsed, err := parseFlashCLIModelsList(stdout.String())
	if err != nil {
		return nil, err
	}
	storeFlashCLIModels(parsed)
	return parsed, nil
}

func removeFlashCLIModel(ctx context.Context, ref string) error {
	if !domain.AdmittedFlashCLIRef(ref) {
		return fmt.Errorf("flashcli ref is not admitted")
	}
	command, runnerArgs, err := flashCLIRunnerLaunch()
	if err != nil {
		return err
	}
	args, err := buildFlashCLIRemoveArgs(runnerArgs, ref)
	if err != nil {
		return err
	}
	return runFlashCLI(ctx, command, args, flashCLIRemoveTimeout, nil)
}

func runFlashCLI(ctx context.Context, command string, args []string, timeout time.Duration, stdout io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	pullCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(pullCtx, command, args...)
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if pullCtx.Err() != nil {
			return pullCtx.Err()
		}
		return fmt.Errorf("flashcli exited: %w", err)
	}
	return nil
}

func flashCLIRunnerLaunch() (string, []string, error) {
	command := strings.TrimSpace(os.Getenv("YAOMU_WSL_RUNNER_COMMAND"))
	if command == "" {
		command = "wsl.exe"
	}
	if strings.ContainsAny(command, " \t\r\n") {
		return "", nil, fmt.Errorf("wsl launcher path is not a single token")
	}
	rawArgs := strings.TrimSpace(os.Getenv("YAOMU_WSL_RUNNER_ARGS"))
	if rawArgs == "" {
		return "", nil, fmt.Errorf("wsl runner args are not configured")
	}
	var runnerArgs []string
	if err := json.Unmarshal([]byte(rawArgs), &runnerArgs); err != nil {
		return "", nil, fmt.Errorf("wsl runner args are not a string array")
	}
	return command, runnerArgs, nil
}

// buildFlashCLIPullArgs 从已有的 WSL Runner argv 取出发行版和已注入的环境，
// 组装 `wsl -d <distro> -e env <已有键> <FLASHCLI_BIN> pull <ref>`。
func buildFlashCLIPullArgs(runnerArgs []string, ref string) ([]string, error) {
	if !domain.AdmittedFlashCLIRef(ref) {
		return nil, fmt.Errorf("flashcli ref is not admitted")
	}
	return buildFlashCLICommand(runnerArgs, "pull", ref)
}

func buildFlashCLIModelsListArgs(runnerArgs []string) ([]string, error) {
	return buildFlashCLICommand(runnerArgs, "models", "list")
}

// buildFlashCLIRemoveArgs 组装官方 `flashcli bundle clean <ref> --all --yes`。
// `--all` 连同该 ref 的 bundle 与权重一起删除，`--yes` 跳过交互确认。
func buildFlashCLIRemoveArgs(runnerArgs []string, ref string) ([]string, error) {
	if !domain.AdmittedFlashCLIRef(ref) {
		return nil, fmt.Errorf("flashcli ref is not admitted")
	}
	return buildFlashCLICommand(runnerArgs, "bundle", "clean", ref, "--all", "--yes")
}

func buildFlashCLICommand(runnerArgs []string, command ...string) ([]string, error) {
	distro, env, err := runnerLaunchEnv(runnerArgs)
	if err != nil {
		return nil, err
	}
	bin := env["FLASHCLI_BIN"]
	if !safePOSIXExecutable(bin) {
		return nil, fmt.Errorf("FLASHCLI_BIN is not an absolute executable path")
	}
	if strings.TrimSpace(env["FLASHCLI_CUDA_TAG"]) == "" {
		return nil, fmt.Errorf("FLASHCLI_CUDA_TAG is not configured")
	}
	args := []string{"-d", distro, "-e", "env"}
	for _, key := range flashCLIPrepareEnvKeys {
		value := env[key]
		if value == "" {
			continue
		}
		if !safeEnvValue(value) {
			return nil, fmt.Errorf("%s contains unsupported characters", key)
		}
		args = append(args, key+"="+value)
	}
	args = append(args, bin)
	args = append(args, command...)
	return args, nil
}

// parseFlashCLIModelsList 解析 `flashcli models list`。
// 只有 WEIGHTS 列为 cached 才算该 ref 已下载。镜像里的 FlashCLI 用空格对齐列，
// 不是制表符；两种表都认。空缓存是合法结果。
func parseFlashCLIModelsList(output string) (map[string]bool, error) {
	text := strings.TrimSpace(output)
	if text == "" || strings.HasPrefix(text, "No cached presets") {
		return map[string]bool{}, nil
	}
	lines := strings.Split(text, "\n")
	header := strings.TrimRight(lines[0], "\r")
	refOf, weightsOf, err := modelsListFields(header)
	if err != nil {
		return nil, err
	}
	cached := map[string]bool{}
	for _, line := range lines[1:] {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		ref := refOf(line)
		if !domain.AdmittedFlashCLIRef(ref) {
			continue
		}
		cached[ref] = weightsOf(line) == "cached"
	}
	return cached, nil
}

func modelsListFields(header string) (func(string) string, func(string) string, error) {
	if strings.Contains(header, "\t") {
		cols := strings.Split(header, "\t")
		refCol, weightsCol := -1, -1
		for i, name := range cols {
			switch strings.TrimSpace(name) {
			case "REF":
				refCol = i
			case "WEIGHTS":
				weightsCol = i
			}
		}
		if refCol < 0 || weightsCol < 0 {
			return nil, nil, fmt.Errorf("flashcli models list header is not REF/WEIGHTS")
		}
		field := func(col int) func(string) string {
			return func(line string) string {
				parts := strings.Split(line, "\t")
				if col >= len(parts) {
					return ""
				}
				return strings.TrimSpace(parts[col])
			}
		}
		return field(refCol), field(weightsCol), nil
	}
	refAt := strings.Index(header, "REF")
	bundleAt := strings.Index(header, "BUNDLE")
	weightsAt := strings.Index(header, "WEIGHTS")
	weightsSizeAt := strings.Index(header, "WEIGHTS SIZE")
	if weightsSizeAt >= 0 && weightsAt == weightsSizeAt {
		weightsAt = -1
	}
	if refAt < 0 || bundleAt <= refAt || weightsAt < 0 {
		return nil, nil, fmt.Errorf("flashcli models list header is not REF/WEIGHTS")
	}
	weightsEnd := len(header)
	if weightsSizeAt > weightsAt {
		weightsEnd = weightsSizeAt
	}
	sliceCol := func(start, end int) func(string) string {
		return func(line string) string {
			if start >= len(line) {
				return ""
			}
			stop := end
			if stop > len(line) || stop < start {
				stop = len(line)
			}
			return strings.TrimSpace(line[start:stop])
		}
	}
	return sliceCol(refAt, bundleAt), sliceCol(weightsAt, weightsEnd), nil
}

func runnerLaunchEnv(args []string) (string, map[string]string, error) {
	if len(args) < 4 || args[0] != "-d" || args[2] != "-e" || args[3] != "env" {
		return "", nil, fmt.Errorf("wsl runner args do not start with a distro env launch")
	}
	distro := args[1]
	if !safeDistroName(distro) {
		return "", nil, fmt.Errorf("wsl distro name is not allowed")
	}
	env := map[string]string{}
	for _, token := range args[4:] {
		key, value, ok := strings.Cut(token, "=")
		if !ok {
			break
		}
		if key == "" || strings.Contains(key, "=") {
			return "", nil, fmt.Errorf("wsl runner env token is invalid")
		}
		if _, exists := env[key]; exists {
			return "", nil, fmt.Errorf("wsl runner env key %s is duplicated", key)
		}
		env[key] = value
	}
	return distro, env, nil
}

func safeDistroName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func safePOSIXExecutable(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return false
	}
	return safeEnvValue(path)
}

func safeEnvValue(value string) bool {
	if value == "" || len(value) > 4096 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || r == ' ' || r == '\'' || r == '"' || r == '`' || r == '$' {
			return false
		}
	}
	return true
}

func flashCLIPrepareMarkerPath(root string) string {
	return filepath.Join(root, flashCLIPrepareMarkerName)
}

func writeFlashCLIPrepareMarker(root, ref string) error {
	if root == "" || !domain.AdmittedFlashCLIRef(ref) {
		return fmt.Errorf("cannot record flashcli prepare marker")
	}
	return os.WriteFile(flashCLIPrepareMarkerPath(root), []byte(ref+"\n"), 0o644)
}

func flashCLIPrepareMarkerMatches(root, ref string) bool {
	if root == "" || !domain.AdmittedFlashCLIRef(ref) {
		return false
	}
	raw, err := os.ReadFile(flashCLIPrepareMarkerPath(root))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(raw)) == ref
}

func (s *Service) rememberFlashCLIRef(planHash, ref string) {
	ref = strings.TrimSpace(ref)
	if s == nil || planHash == "" || !domain.AdmittedFlashCLIRef(ref) {
		return
	}
	s.flashRefMu.Lock()
	defer s.flashRefMu.Unlock()
	if s.flashRefs == nil {
		s.flashRefs = map[string]string{}
	}
	s.flashRefs[planHash] = ref
}

func (s *Service) rememberedFlashCLIRef(planHash string) string {
	if s == nil || planHash == "" {
		return ""
	}
	s.flashRefMu.Lock()
	defer s.flashRefMu.Unlock()
	ref := s.flashRefs[planHash]
	if !domain.AdmittedFlashCLIRef(ref) {
		return ""
	}
	return ref
}

type flashCLIRefSource interface {
	FlashCLIRefFor(slot domain.CapabilitySlotKey, release domain.ReleaseRef) string
}

func admittedStoredRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if !domain.AdmittedFlashCLIRef(ref) {
		return ""
	}
	return ref
}

func (s *Service) flashCLIRefForRelease(slot domain.CapabilitySlotKey, release *domain.ReleaseRef) string {
	if s == nil || s.opts.Releases == nil || release == nil || release.ReleaseID == "" {
		return ""
	}
	src, ok := s.opts.Releases.(flashCLIRefSource)
	if !ok {
		return ""
	}
	return admittedStoredRef(src.FlashCLIRefFor(slot, *release))
}

func (s *Service) prepareAdmittedModel(ctx context.Context, ref string) error {
	prepare := s.opts.PrepareModel
	if prepare == nil {
		prepare = prepareFlashCLIModel
	}
	return prepare(ctx, ref)
}

func (s *Service) removeAdmittedModel(ctx context.Context, ref string) error {
	remove := s.opts.RemoveModel
	if remove == nil {
		remove = removeFlashCLIModel
	}
	return remove(ctx, ref)
}

// flashCLIPrepareNeeded 判断这次复用还要不要执行 pull。
// 没有标记时直接预拉。标记还在时，models list 的 WEIGHTS 不是 cached 也要重拉；
// 列表查询失败同样重拉，避免一张过期标记把缺失权重挡在下载之外。
func (s *Service) flashCLIPrepareNeeded(ctx context.Context, inst domain.Installation, ref string) bool {
	if !flashCLIPrepareMarkerMatches(s.installationRoot(inst), ref) {
		return true
	}
	if s == nil || s.opts.ListCachedModels == nil {
		return false
	}
	cached, err := s.opts.ListCachedModels(ctx)
	if err != nil {
		return true
	}
	return !cached[ref]
}

func (s *Service) flashCLIPrepareRunning(ref string) bool {
	if s == nil || !domain.AdmittedFlashCLIRef(ref) {
		return false
	}
	s.prepareMu.Lock()
	defer s.prepareMu.Unlock()
	_, running := s.preparing[ref]
	return running
}

func (s *Service) startFlashCLIPrepare(inst domain.Installation, ref string) {
	if s == nil || !domain.AdmittedFlashCLIRef(ref) {
		return
	}
	root := s.installationRoot(inst)
	s.prepareMu.Lock()
	if s.preparing == nil {
		s.preparing = map[string]struct{}{}
	}
	if _, running := s.preparing[ref]; running {
		s.prepareMu.Unlock()
		return
	}
	s.preparing[ref] = struct{}{}
	s.prepareMu.Unlock()
	go func() {
		failed := true
		defer func() {
			s.prepareMu.Lock()
			delete(s.preparing, ref)
			s.prepareMu.Unlock()
			eventType := "installation.completed"
			if failed {
				eventType = "installation.failed"
			}
			s.emitInstallationEvent(context.Background(), domain.DomainEvent{
				Type: eventType,
				Data: map[string]any{
					"installation_id": inst.ID,
					"slot":            inst.Slot,
					"slots":           domain.EffectiveCoveredSlots(inst),
				},
			})
		}()
		ctx, cancel := context.WithTimeout(context.Background(), flashCLIPrepareTimeout)
		defer cancel()
		// 这次要重拉，先去掉可能过期的成功标记。失败时不再留下“已准备”的假凭据。
		_ = os.Remove(flashCLIPrepareMarkerPath(root))
		s.logInfo("installation", "installation.model_prepare_started", map[string]any{
			"installation_id": string(inst.ID),
			"slot":            string(inst.Slot),
		})
		if err := s.prepareAdmittedModel(ctx, ref); err != nil {
			s.logWarn("installation", "installation.model_prepare_failed", map[string]any{
				"installation_id": string(inst.ID),
				"error":           err.Error(),
			})
			return
		}
		if err := writeFlashCLIPrepareMarker(root, ref); err != nil {
			s.logWarn("installation", "installation.model_prepare_failed", map[string]any{
				"installation_id": string(inst.ID),
				"error":           err.Error(),
			})
			return
		}
		invalidateFlashCLIListCache()
		failed = false
	}()
}

func readFlashCLIPrepareMarker(root string) string {
	raw, err := os.ReadFile(flashCLIPrepareMarkerPath(root))
	if err != nil {
		return ""
	}
	return admittedStoredRef(strings.TrimSpace(string(raw)))
}

// flashCLIRefsReleasedByDelete 返回这次删除会卸掉的已准入 ref。
// 共享安装还被其他槽位引用时不删除 FlashCLI 缓存。删除条件不满足时返回原错误。
func (s *Service) flashCLIRefsReleasedByDelete(ctx context.Context, slot domain.CapabilitySlotKey) ([]string, error) {
	var refs []string
	err := s.store.ReadTx(ctx, func(tx *store.Tx) error {
		installs, err := tx.ListInstallations(ctx)
		if err != nil {
			return err
		}
		bound := make([]domain.Installation, 0)
		for _, inst := range installs {
			if domain.ContainsCapabilitySlot(inst.BoundSlots, slot) {
				bound = append(bound, inst)
			}
		}
		if len(bound) == 0 {
			return notFoundf("no installed package for slot %s", slot)
		}
		for _, inst := range bound {
			if !inst.Phase.Terminal() {
				return errResourceBusy("installation is still running")
			}
		}
		active, err := tx.CountActiveTasks(ctx, "")
		if err != nil {
			return err
		}
		if active > 0 {
			return errResourceBusy("active tasks still reference installed packages")
		}
		runExecutions, err := tx.ListNonterminalRunExecutions(ctx)
		if err != nil {
			return err
		}
		for _, execution := range runExecutions {
			cfg, frozen := execution.Config.Execution[slot]
			if !frozen || !validRelease(cfg.ModelLock) {
				continue
			}
			for _, inst := range bound {
				if inst.Release != nil && *inst.Release == cfg.ModelLock {
					return errResourceBusy("a nonterminal run still freezes this installed release")
				}
			}
		}
		roots := make([]string, 0, len(bound))
		for _, inst := range bound {
			if root := s.installationRoot(inst); root != "" {
				roots = append(roots, root)
			}
		}
		if s.installationRootsBusy(roots) {
			return errResourceBusy("an installation worker still owns the installed directory")
		}
		removing := map[domain.ID]struct{}{}
		for _, inst := range bound {
			others := 0
			for _, boundSlot := range inst.BoundSlots {
				if boundSlot != slot {
					others++
				}
			}
			if others == 0 {
				removing[inst.ID] = struct{}{}
			}
		}
		seen := map[string]struct{}{}
		for _, inst := range installs {
			if _, drop := removing[inst.ID]; !drop {
				continue
			}
			ref := s.flashCLIRefForRelease(inst.Slot, inst.Release)
			if ref == "" {
				ref = readFlashCLIPrepareMarker(s.installationRoot(inst))
			}
			if ref == "" {
				continue
			}
			if flashCLIRefStillUsed(s, installs, removing, ref) {
				continue
			}
			if _, ok := seen[ref]; ok {
				continue
			}
			seen[ref] = struct{}{}
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		return nil
	})
	return refs, err
}

func flashCLIRefStillUsed(s *Service, installs []domain.Installation, removing map[domain.ID]struct{}, ref string) bool {
	for _, inst := range installs {
		if _, drop := removing[inst.ID]; drop {
			continue
		}
		other := s.flashCLIRefForRelease(inst.Slot, inst.Release)
		if other == "" {
			other = readFlashCLIPrepareMarker(s.installationRoot(inst))
		}
		if other == ref {
			return true
		}
	}
	return false
}

func (s *Service) applyFlashCLIDownloadEvidence(ctx context.Context, caps []domain.Capability) error {
	if s == nil || s.opts.ListCachedModels == nil {
		return nil
	}
	installs, err := s.store.ListInstallations(ctx)
	if err != nil {
		return err
	}
	refs := map[domain.CapabilitySlotKey]string{}
	for _, inst := range installs {
		if inst.Phase != domain.InstallationPhaseDone || inst.Release == nil {
			continue
		}
		ref := s.flashCLIRefForRelease(inst.Slot, inst.Release)
		if ref == "" {
			ref = readFlashCLIPrepareMarker(s.installationRoot(inst))
		}
		if ref == "" {
			continue
		}
		for _, slot := range domain.InstallationCurrentSlots(inst) {
			refs[slot] = ref
		}
	}
	if len(refs) == 0 {
		return nil
	}
	cached, listErr := s.opts.ListCachedModels(ctx)
	for i := range caps {
		ref := refs[caps[i].Slot]
		if ref == "" {
			continue
		}
		if s.flashCLIPrepareRunning(ref) {
			caps[i].State = domain.SlotStateInstalling
			caps[i].ReasonCode = ""
			caps[i].ActionIDs = nil
			continue
		}
		if listErr == nil && cached[ref] {
			continue
		}
		if caps[i].State == domain.SlotStateReady {
			caps[i].State = domain.SlotStateNeedsSetup
			caps[i].ReasonCode = string(domain.ErrorCodeModelNotInstalled)
			caps[i].ActionIDs = []domain.ID{"open_model_setup"}
		}
		for j := range caps[i].Engines {
			if !caps[i].Engines[j].Installed {
				continue
			}
			caps[i].Engines[j].Installed = false
			caps[i].Engines[j].State = domain.SlotStateNeedsSetup
		}
	}
	return nil
}

var (
	flashListMu sync.Mutex
	flashListAt time.Time
	flashList   map[string]bool
)

func cachedFlashCLIModels() (map[string]bool, bool) {
	flashListMu.Lock()
	defer flashListMu.Unlock()
	if flashList == nil || time.Since(flashListAt) > flashCLIListCacheTTL {
		return nil, false
	}
	out := make(map[string]bool, len(flashList))
	for ref, ok := range flashList {
		out[ref] = ok
	}
	return out, true
}

func storeFlashCLIModels(parsed map[string]bool) {
	flashListMu.Lock()
	defer flashListMu.Unlock()
	flashList = parsed
	flashListAt = time.Now()
}

func invalidateFlashCLIListCache() {
	flashListMu.Lock()
	flashList = nil
	flashListAt = time.Time{}
	flashListMu.Unlock()
}

var (
	flashRootMu sync.Mutex
	flashRoot   string
)

// MeasureFlashCLIWeightProgress 读取 WSL 里该 ref 的权重清单和已落盘字节。
// 清单还没写出时总量为 0，调用方不要把目录小包当成模型已下完。
func MeasureFlashCLIWeightProgress(ctx context.Context, ref string) (int64, int64, error) {
	root, err := flashCLIDataRoot(ctx)
	if err != nil {
		return 0, 0, err
	}
	return weightProgressAt(root, ref)
}

func flashCLIDataRoot(ctx context.Context) (string, error) {
	flashRootMu.Lock()
	defer flashRootMu.Unlock()
	if flashRoot != "" {
		return flashRoot, nil
	}
	command, runnerArgs, err := flashCLIRunnerLaunch()
	if err != nil {
		return "", err
	}
	distro, _, err := runnerLaunchEnv(runnerArgs)
	if err != nil {
		return "", err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, command, "-d", distro, "-e", "python3", "-c",
		"import os; print(os.path.expanduser('~'))")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve flashcli home: %w", err)
	}
	home := strings.TrimSpace(string(out))
	if !strings.HasPrefix(home, "/") || strings.Contains(home, "..") {
		return "", fmt.Errorf("flashcli home is not an absolute path")
	}
	flashRoot = `\\wsl$\` + distro + strings.ReplaceAll(home, "/", `\`) + `\.flashcli`
	return flashRoot, nil
}

func weightProgressAt(root, ref string) (int64, int64, error) {
	name, version, err := flashCLIBundleParts(ref)
	if err != nil {
		return 0, 0, err
	}
	bundlePath := filepath.Join(root, "bundles", name, version, "flashcli-bundle.json")
	raw, err := os.ReadFile(bundlePath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	repos, err := weightRepos(raw)
	if err != nil {
		return 0, 0, err
	}
	if len(repos) == 0 {
		return 0, 0, nil
	}
	var done, total int64
	models := filepath.Join(root, "models")
	err = filepath.WalkDir(models, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || entry.Name() != ".flashcli_download.json" {
			return nil
		}
		fileDone, fileTotal, readErr := weightFileProgress(path, repos)
		if readErr != nil {
			return readErr
		}
		done += fileDone
		total += fileTotal
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return 0, 0, err
	}
	return done, total, nil
}

func flashCLIBundleParts(ref string) (string, string, error) {
	if !domain.AdmittedFlashCLIRef(ref) {
		return "", "", fmt.Errorf("flashcli ref is not admitted")
	}
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if slash < 0 || colon <= slash+1 || colon >= len(ref)-1 {
		return "", "", fmt.Errorf("flashcli ref is not admitted")
	}
	name := ref[slash+1 : colon]
	version := ref[colon+1:]
	if !safePathSegment(name) || !safePathSegment(version) {
		return "", "", fmt.Errorf("flashcli ref is not admitted")
	}
	return name, version, nil
}

type flashCLIBundleDoc struct {
	Defaults struct {
		Weights struct {
			Repo string `json:"repo"`
		} `json:"weights"`
		Extra map[string]struct {
			Repo string `json:"repo"`
		} `json:"extra_weights"`
	} `json:"defaults"`
}

func weightRepos(raw []byte) (map[string]struct{}, error) {
	var doc flashCLIBundleDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode flashcli bundle: %w", err)
	}
	repos := map[string]struct{}{}
	if repo := strings.TrimSpace(doc.Defaults.Weights.Repo); repo != "" {
		repos[repo] = struct{}{}
	}
	for _, extra := range doc.Defaults.Extra {
		if repo := strings.TrimSpace(extra.Repo); repo != "" {
			repos[repo] = struct{}{}
		}
	}
	return repos, nil
}

type flashCLIDownloadDoc struct {
	Repo  string `json:"repo"`
	Files []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	} `json:"files"`
}

func weightFileProgress(manifestPath string, repos map[string]struct{}) (int64, int64, error) {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return 0, 0, err
	}
	var doc flashCLIDownloadDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return 0, 0, fmt.Errorf("decode flashcli download manifest: %w", err)
	}
	if _, ok := repos[strings.TrimSpace(doc.Repo)]; !ok {
		return 0, 0, nil
	}
	dir := filepath.Dir(manifestPath)
	var done, total int64
	for _, file := range doc.Files {
		if file.Size <= 0 {
			continue
		}
		target, ok := fileWithin(dir, file.Path)
		if !ok {
			continue
		}
		total += file.Size
		got := fileSize(target)
		if got == 0 {
			got = fileSize(target + ".incomplete")
		}
		if got > file.Size {
			got = file.Size
		}
		done += got
	}
	return done, total, nil
}

func fileWithin(dir, rel string) (string, bool) {
	if rel == "" || strings.Contains(rel, `\`) || filepath.IsAbs(rel) {
		return "", false
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join(dir, clean), true
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	return info.Size()
}
