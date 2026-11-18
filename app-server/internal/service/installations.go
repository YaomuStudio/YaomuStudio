package service

// 模型/运行时安装：计划解析、下载执行、校验落位、取消、删除与回滚。
//
// 边界（docs/flashcli-integration.md §3、FR-MODEL-02、contracts/shared-installations.md）：
//   - 安装计划绑定精确发布、完整覆盖范围、文件清单与用户同意范围；执行时不能扩大；
//   - 一条已验证的物理安装可服务多个显式槽位引用；不按槽位复制 Done 记录；
//   - 真实传输使用 downloader：Range 续传、节流进度、SHA-256/大小校验；
//   - 半成品（.part 或未完成记录）永远不会被能力目录判为 READY；
//   - 只有全部文件校验/落位成功后才在同一事务激活全部覆盖槽位；失败/取消保留旧当前；
//   - 删除只解绑请求槽位；最后引用移除后才回收物理目录；回滚只切该槽位指针。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/downloader"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// ReleasePlan 是发布目录解析出的精确版本、完整覆盖、文件清单与体积/许可/权限。
type ReleasePlan struct {
	Release domain.ReleaseRef
	// CoveredSlots 是已准入发布明确声明的能力覆盖（排序去重）；空值仅表示
	// 旧静态测试提供方没有范围声明，由服务层按请求槽位单 slot 解读。
	CoveredSlots []domain.CapabilitySlotKey
	// Files 是安装锁中的文件清单（最小形状 {rel_path,url,sha256,size}）。
	Files            []downloader.File
	DownloadBytes    int64
	CacheReuseBytes  int64
	InstallBytes     int64
	PeakStagingBytes int64
	Permissions      []string
	License          domain.LicenseSummary
	RequiresRestart  bool
	// FlashCLIRef 是安装完成后要预拉的已准入模型引用。空值表示该发布不走 FlashCLI。
	FlashCLIRef string
}

// ReleaseCatalog 解析槽位的准入发布；发布流水线未接入时返回未就绪。
type ReleaseCatalog interface {
	Resolve(ctx context.Context, slot domain.CapabilitySlotKey) (ReleasePlan, error)
}

// UnconfiguredReleases 是默认发布目录：没有经准入的发布可解析。
type UnconfiguredReleases struct{}

// Resolve 实现 ReleaseCatalog。
func (UnconfiguredReleases) Resolve(_ context.Context, slot domain.CapabilitySlotKey) (ReleasePlan, error) {
	return ReleasePlan{}, &domain.Error{
		Code:       domain.ErrorCodeModelNotInstalled,
		MessageKey: "installation.release_not_configured",
		Retryable:  false,
		ActionIDs:  []domain.ID{"open_model_setup"},
	}
}

// StaticReleases 是测试/离线开发用的发布目录。
type StaticReleases struct {
	Releases map[domain.CapabilitySlotKey]ReleasePlan
}

// FlashCLIRefFor 返回与已安装发布匹配的已准入引用。测试目录用它，不额外消耗 Resolve。
func (s StaticReleases) FlashCLIRefFor(slot domain.CapabilitySlotKey, release domain.ReleaseRef) string {
	plan, ok := s.Releases[slot]
	if !ok || plan.Release.ReleaseID != release.ReleaseID || plan.Release.Version != release.Version {
		return ""
	}
	return plan.FlashCLIRef
}

// Resolve 实现 ReleaseCatalog。
func (s StaticReleases) Resolve(_ context.Context, slot domain.CapabilitySlotKey) (ReleasePlan, error) {
	plan, ok := s.Releases[slot]
	if !ok {
		return ReleasePlan{}, &domain.Error{
			Code:       domain.ErrorCodeModelNotInstalled,
			MessageKey: "installation.release_not_configured",
			Retryable:  false,
			ActionIDs:  []domain.ID{"open_model_setup"},
		}
	}
	return plan, nil
}

// Downloader 是安装传输适配接口。
//
// 每个文件先 Fetch 到 `<root>/<rel_path>.part`，Verify 校验大小与 SHA-256，
// 全部通过后 Commit 原子落位。Cleanup 删除安装目录下的 `.part` 临时文件。
type Downloader interface {
	Fetch(ctx context.Context, root string, file downloader.File, onProgress func(written, total int64)) error
	Verify(ctx context.Context, root string, file downloader.File) error
	Commit(root string, file downloader.File) error
	Cleanup(root string) error
}

// UnavailableDownloader 是默认实现：传输未接入时立即失败并标记可重试。
type UnavailableDownloader struct{}

func unavailableDownloadError() error {
	return &domain.Error{
		Code:       domain.ErrorCodeCapabilityUnverified,
		MessageKey: "installation.download_unavailable",
		Retryable:  true,
		ActionIDs:  []domain.ID{"retry_installation"},
	}
}

// Fetch 实现 Downloader。
func (UnavailableDownloader) Fetch(_ context.Context, _ string, _ downloader.File, _ func(int64, int64)) error {
	return unavailableDownloadError()
}

// Verify 实现 Downloader。
func (UnavailableDownloader) Verify(_ context.Context, _ string, _ downloader.File) error {
	return unavailableDownloadError()
}

// Commit 实现 Downloader。
func (UnavailableDownloader) Commit(_ string, _ downloader.File) error {
	return unavailableDownloadError()
}

// Cleanup 实现 Downloader。
func (UnavailableDownloader) Cleanup(_ string) error { return nil }

// planTTL 是安装计划的同意有效期；过期需要重新解析（PREFLIGHT_STALE）。
const planTTL = 15 * time.Minute

// errInstallationRaced 表示事务外验证的复用/回滚目标在最终事务中已变化；
// 调用方重新预检并验证，绝不在未验证实体上激活或切换。
var errInstallationRaced = errors.New("installation state raced during file verification")

// installationFileVerifier 校验安装根内最终文件的路径/大小/SHA-256。
// 生产实现是本地 hash；测试可注入屏障实现观察事务边界。
type installationFileVerifier func(ctx context.Context, root string, manifest []domain.InstallationFile) error

// verifyInstallationFiles 调用可注入的验证器（默认本地 hash）。
func (s *Service) verifyInstallationFiles(ctx context.Context, root string, manifest []domain.InstallationFile) error {
	verifier := s.verifyFiles
	if verifier == nil {
		verifier = verifyInstallationFiles
	}
	return verifier(ctx, root, manifest)
}

const installationPlanMetaPrefix = "installation_plan."

type storedPlan struct {
	Slot      domain.CapabilitySlotKey `json:"slot"`
	PlanHash  string                   `json:"plan_hash"`
	CreatedAt time.Time                `json:"created_at"`
	Plan      domain.InstallationPlan  `json:"plan"`
	Files     []downloader.File        `json:"files"`
	// FlashCLIRef 只留在安装计划元数据里，不进入对外 InstallationPlan。
	FlashCLIRef string `json:"flashcli_ref,omitempty"`
}

// PlanInstallation 解析安装计划（200）；不持久化执行意图。
func (s *Service) PlanInstallation(ctx context.Context, slot domain.CapabilitySlotKey) (domain.InstallationPlan, error) {
	if !slot.Valid() {
		return domain.InstallationPlan{}, notFoundf("unknown capability slot %q", slot)
	}
	resolved, err := s.opts.Releases.Resolve(ctx, slot)
	if err != nil {
		return domain.InstallationPlan{}, err
	}
	if !validRelease(resolved.Release) {
		return domain.InstallationPlan{}, releaseIncomplete()
	}
	if err := validateManifest(resolved.Files); err != nil {
		return domain.InstallationPlan{}, releaseIncomplete()
	}
	covered, err := planCoveredSlots(slot, resolved.CoveredSlots)
	if err != nil {
		return domain.InstallationPlan{}, err
	}
	key, err := domain.ReleaseStorageKey(resolved.Release)
	if err != nil {
		return domain.InstallationPlan{}, releaseIncomplete()
	}
	plan := domain.InstallationPlan{
		Slot:             slot,
		CoveredSlots:     covered,
		Release:          resolved.Release,
		DownloadBytes:    resolved.DownloadBytes,
		CacheReuseBytes:  resolved.CacheReuseBytes,
		InstallBytes:     resolved.InstallBytes,
		PeakStagingBytes: resolved.PeakStagingBytes,
		Permissions:      resolved.Permissions,
		License:          resolved.License,
		RequiresRestart:  resolved.RequiresRestart,
	}
	if plan.Permissions == nil {
		plan.Permissions = []string{}
	}
	manifest := installationManifest(resolved.Files)
	reuse, reuseIsDone, err := s.planReuseTarget(ctx, key, covered, manifest)
	if err != nil {
		return domain.InstallationPlan{}, err
	}
	if reuse != "" {
		plan.ReuseInstallationID = reuse
		if reuseIsDone {
			// 已验证实体复用：不启动额外传输/暂存，只报告可复用字节。
			plan.DownloadBytes = 0
			plan.PeakStagingBytes = 0
			plan.CacheReuseBytes = resolved.InstallBytes
		}
	}
	hash, err := digestOf(struct {
		Slot     domain.CapabilitySlotKey
		Covered  []domain.CapabilitySlotKey
		Reuse    string
		Release  domain.ReleaseRef
		Files    []downloader.File
		Download int64
		Cache    int64
		Install  int64
		Peak     int64
		Permits  []string
		License  domain.LicenseSummary
		Restart  bool
	}{
		Slot: slot, Covered: covered, Reuse: plan.ReuseInstallationID,
		Release: resolved.Release, Files: resolved.Files,
		Download: plan.DownloadBytes, Cache: plan.CacheReuseBytes,
		Install: resolved.InstallBytes, Peak: plan.PeakStagingBytes,
		Permits: plan.Permissions, License: plan.License, Restart: plan.RequiresRestart,
	})
	if err != nil {
		return domain.InstallationPlan{}, err
	}
	plan.PlanHash = hash
	plan.PlanID = "plan_" + hash[:16]
	flashRef := admittedStoredRef(resolved.FlashCLIRef)
	if flashRef != "" && s.flashCLIPrepareRunning(flashRef) {
		return domain.InstallationPlan{}, errResourceBusy("flashcli model prepare is already running")
	}
	s.rememberFlashCLIRef(plan.PlanHash, flashRef)

	stored, err := json.Marshal(storedPlan{
		Slot: slot, PlanHash: hash, CreatedAt: s.Now(), Plan: plan, Files: resolved.Files,
		FlashCLIRef: flashRef,
	})
	if err != nil {
		return domain.InstallationPlan{}, fmt.Errorf("encode plan: %w", err)
	}
	if err := s.store.DoTx(ctx, func(tx *store.Tx) error {
		return tx.SetMeta(ctx, installationPlanMetaPrefix+plan.PlanID, string(stored))
	}); err != nil {
		return domain.InstallationPlan{}, err
	}
	return plan, nil
}

// planCoveredSlots 解析发布声明的完整覆盖；旧静态提供方缺范围只认请求槽位。
func planCoveredSlots(requested domain.CapabilitySlotKey, declared []domain.CapabilitySlotKey) ([]domain.CapabilitySlotKey, error) {
	if len(declared) == 0 {
		return []domain.CapabilitySlotKey{requested}, nil
	}
	covered, err := domain.NormalizeCapabilitySlots(declared)
	if err != nil || !domain.ContainsCapabilitySlot(covered, requested) {
		return nil, releaseIncomplete()
	}
	return covered, nil
}

// planReuseTarget 返回同一精确发布的进行中或已验证实体，以及它是否已完成。
// 覆盖/文件清单不同的同身份记录不在这里静默合并。
func (s *Service) planReuseTarget(ctx context.Context, key string, covered []domain.CapabilitySlotKey, manifest []domain.InstallationFile) (string, bool, error) {
	installs, err := s.store.ListInstallations(ctx)
	if err != nil {
		return "", false, err
	}
	var active, done *domain.Installation
	for i := range installs {
		inst := installs[i]
		if inst.StorageKey != key {
			continue
		}
		if !sameInstallationScope(inst, covered, manifest) {
			continue
		}
		if !inst.Phase.Terminal() {
			candidate := inst
			active = &candidate
			continue
		}
		if inst.Phase == domain.InstallationPhaseDone {
			candidate := inst
			done = &candidate
		}
	}
	if active != nil {
		return active.ID, false, nil
	}
	if done != nil {
		return done.ID, true, nil
	}
	return "", false, nil
}

func sameInstallationScope(inst domain.Installation, covered []domain.CapabilitySlotKey, manifest []domain.InstallationFile) bool {
	if !sameCapabilitySlotSet(inst.CoveredSlots, covered) {
		return false
	}
	if len(inst.Manifest) > 0 && !domain.SameInstallationFiles(inst.Manifest, manifest) {
		return false
	}
	if inst.Phase == domain.InstallationPhaseDone && len(inst.Manifest) == 0 {
		return false
	}
	return true
}

func sameCapabilitySlotSet(a, b []domain.CapabilitySlotKey) bool {
	left := domain.SortCapabilitySlots(a)
	right := domain.SortCapabilitySlots(b)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func releaseIncomplete() error {
	return &domain.Error{
		Code:       domain.ErrorCodeCapabilityUnverified,
		MessageKey: "installation.release_incomplete",
		Retryable:  false,
		ActionIDs:  []domain.ID{"open_model_setup"},
	}
}

// validateManifest 校验文件清单最小形状与唯一性；空清单视为未准入发布。
func validateManifest(files []downloader.File) error {
	if len(files) == 0 {
		return fmt.Errorf("release manifest has no files")
	}
	seen := make(map[string]bool, len(files))
	for _, file := range files {
		if err := downloader.ValidateFile(file); err != nil {
			return err
		}
		if seen[file.RelPath] {
			return fmt.Errorf("duplicate rel_path %q", file.RelPath)
		}
		seen[file.RelPath] = true
	}
	return nil
}

func installationManifest(files []downloader.File) []domain.InstallationFile {
	manifest := make([]domain.InstallationFile, 0, len(files))
	for _, file := range files {
		manifest = append(manifest, domain.InstallationFile{
			RelPath: file.RelPath,
			SHA256:  file.SHA256,
			Size:    file.Size,
		})
	}
	return manifest
}

// CreateInstallationRequest 是执行用户同意计划的输入。
type CreateInstallationRequest struct {
	PlanID   string `json:"plan_id"`
	PlanHash string `json:"plan_hash"`
}

// ListInstallations 返回安装记录（最新在前），含覆盖/绑定/当前投影。
func (s *Service) ListInstallations(ctx context.Context) ([]domain.Installation, error) {
	installs, err := s.store.ListInstallations(ctx)
	if err != nil {
		return nil, err
	}
	if installs == nil {
		installs = []domain.Installation{}
	}
	return installs, nil
}

// ActiveInstallationWork 报告本实例仍拥有安装 worker（含已取消但尚未退出
// 的尾部）。它是更新准备的只读内存断言：不排队、不改安装状态。
func (s *Service) ActiveInstallationWork() bool {
	if s == nil {
		return false
	}
	s.installMu.Lock()
	defer s.installMu.Unlock()
	return len(s.installs) > 0
}

// GetInstallation 返回安装/恢复状态。
func (s *Service) GetInstallation(ctx context.Context, id string) (domain.Installation, error) {
	if id == "" {
		return domain.Installation{}, invalidRequestf("installation id required")
	}
	return s.store.GetInstallation(ctx, id)
}

// CreateInstallation 执行用户同意的计划（202）；计划过期/摘要不符返回 PREFLIGHT_STALE。
//
// 同一精确发布的并发请求加入同一物理安装 ID；已经完成的实体在最终文件校验
// 通过后直接激活同意引用，不生成伪 Done 记录或第二份传输。
func (s *Service) CreateInstallation(ctx context.Context, idem Idem, req CreateInstallationRequest) (Response, error) {
	if req.PlanID == "" {
		return Response{}, invalidRequestf("plan_id required")
	}
	if req.PlanHash == "" {
		return Response{}, invalidRequestf("plan_hash required")
	}
	s.installationLifecycleMu.Lock()
	defer s.installationLifecycleMu.Unlock()

	// 幂等重放直接返回原快照：不重新验证文件、不重做激活/文件副作用。
	replay, err := s.idempotentReplay(ctx, "installations.create", idem)
	if err != nil {
		return Response{}, err
	}
	if replay {
		return s.createInstallationWrite(ctx, idem, req, nil)
	}
	for attempt := 0; attempt < 3; attempt++ {
		prepared, err := s.prepareCreateInstallation(ctx, req)
		if err != nil {
			return Response{}, err
		}
		resp, err := s.createInstallationWrite(ctx, idem, req, prepared)
		if err == nil {
			return resp, nil
		}
		if !errors.Is(err, errInstallationRaced) {
			return Response{}, err
		}
	}
	return Response{}, errConflictf(domain.ErrorCodePreflightStale,
		"installation.reuse_raced", "installation state changed during verification; retry")
}

// preparedCreate 记录事务外完成的最终文件验证目标；事务内必须仍指向同一实体。
type preparedCreate struct {
	verifiedDoneID domain.ID
}

func (s *Service) createInstallationWrite(ctx context.Context, idem Idem, req CreateInstallationRequest, prepared *preparedCreate) (Response, error) {
	var events []domain.DomainEvent
	var created domain.Installation
	var createdRoot string
	var createdPlan domain.InstallationPlan
	var createdFiles []downloader.File
	var createdRef string
	var reused domain.Installation
	var reusedRef string
	resp, err := s.write(ctx, "installations.create", idem, 202, func(ctx context.Context, tx *store.Tx) (any, error) {
		if prepared == nil {
			// 预检说已有回执；事务内若真的执行则状态已变化，安全失败而不是无验证激活。
			return nil, fmt.Errorf("%w: idempotent installation replay state changed", domain.ErrPreflightStale)
		}
		stored, err := s.installationPlanFrom(ctx, tx.GetMeta, req.PlanID, req.PlanHash)
		if err != nil {
			return nil, err
		}
		covered, key, manifest, err := installationPlanSubject(stored)
		if err != nil {
			return nil, err
		}
		installs, err := tx.ListInstallations(ctx)
		if err != nil {
			return nil, err
		}
		now := s.Now()
		target, found, err := selectCreateTarget(installs, key, stored.Plan.ReuseInstallationID, covered, manifest)
		if err != nil {
			return nil, err
		}
		if found {
			if target.join {
				return target.inst, nil
			}
			// Done 复用必须对应事务外已验证的同一实体；active→done 等竞态重试。
			if prepared.verifiedDoneID != target.inst.ID {
				return nil, errInstallationRaced
			}
			body, staged, err := s.activateDoneInstallation(ctx, tx, target.inst, covered, now)
			if err != nil {
				return nil, err
			}
			events = append(events, staged...)
			reused = target.inst
			reusedRef = admittedStoredRef(stored.FlashCLIRef)
			return body, nil
		}

		// 不同发布的覆盖范围不能同时安装：晚完成的旧版本不得覆盖新选择。
		for _, inst := range installs {
			if inst.Phase.Terminal() || inst.StorageKey == key {
				continue
			}
			if capabilitySlotsOverlap(inst.CoveredSlots, covered) {
				return nil, errResourceBusy("another installation is running for an overlapping capability slot")
			}
		}
		// 取消/失败 worker 尾部仍拥有同一物理根：等待它退出，不启动第二 writer。
		if s.installationRootBusy(s.releaseRoot(key)) {
			return nil, errResourceBusy("an installation worker still owns the target directory")
		}

		inst := domain.Installation{
			ID:           store.NewID("inst"),
			Slot:         stored.Slot,
			CoveredSlots: covered,
			Phase:        domain.InstallationPhasePlanning,
			Progress:     0,
			PlanID:       req.PlanID,
			PlanHash:     req.PlanHash,
			Release:      &stored.Plan.Release,
			StorageKey:   key,
			Manifest:     manifest,
			PathOwner:    "app",
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := tx.CreateInstallation(ctx, inst); err != nil {
			return nil, err
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type: "installation.created",
			Data: map[string]any{
				"installation_id": inst.ID,
				"slot":            inst.Slot,
				"slots":           covered,
				"plan_hash":       inst.PlanHash,
				"release_id":      releaseID(inst.Release),
			},
		}); err != nil {
			return nil, err
		}
		created = inst
		createdRoot = s.releaseRoot(key)
		createdPlan = stored.Plan
		createdFiles = stored.Files
		createdRef = admittedStoredRef(stored.FlashCLIRef)
		return tx.GetInstallation(ctx, inst.ID)
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	// 传输在事务提交后异步执行；真实下载由 Downloader 适配器提供。
	// 幂等重放不会执行事务，created 为空时不重复启动安装。
	if created.ID != "" {
		s.startInstallation(created, createdRoot, createdPlan, createdFiles, createdRef)
	} else if reused.ID != "" && reusedRef != "" && s.flashCLIPrepareNeeded(ctx, reused, reusedRef) {
		// 已完成的目录复用不会再进入下载 worker。本地标记只说明某次 pull 退出过 0，
		// 权重是否还在以 models list 为准；标记还在但权重缺失时仍要重新预拉。
		s.startFlashCLIPrepare(reused, reusedRef)
	}
	return resp, nil
}

// prepareCreateInstallation 在全局写锁与事务之外解析计划并完成 Done 复用的
// 最终文件验证；installationLifecycleMu 保证验证期间目录不被删除/重装。
func (s *Service) prepareCreateInstallation(ctx context.Context, req CreateInstallationRequest) (*preparedCreate, error) {
	stored, err := s.installationPlanFrom(ctx, s.store.GetMeta, req.PlanID, req.PlanHash)
	if err != nil {
		return nil, err
	}
	if ref := admittedStoredRef(stored.FlashCLIRef); ref != "" && s.flashCLIPrepareRunning(ref) {
		return nil, errResourceBusy("flashcli model prepare is already running")
	}
	covered, key, manifest, err := installationPlanSubject(stored)
	if err != nil {
		return nil, err
	}
	installs, err := s.store.ListInstallations(ctx)
	if err != nil {
		return nil, err
	}
	target, found, err := selectCreateTarget(installs, key, stored.Plan.ReuseInstallationID, covered, manifest)
	if err != nil {
		return nil, err
	}
	prepared := &preparedCreate{}
	if found && !target.join {
		if err := s.verifyDoneInstallation(ctx, target.inst); err != nil {
			return nil, err
		}
		prepared.verifiedDoneID = target.inst.ID
	}
	return prepared, nil
}

type createTarget struct {
	inst domain.Installation
	join bool
}

// selectCreateTarget 是创建/加入/复用的唯一选择口径（预检与事务内共用）。
func selectCreateTarget(
	installs []domain.Installation,
	key, planTarget string,
	covered []domain.CapabilitySlotKey,
	manifest []domain.InstallationFile,
) (createTarget, bool, error) {
	stale := func() error {
		return fmt.Errorf("%w: installation reuse target is stale", domain.ErrPreflightStale)
	}
	if planTarget != "" {
		candidate, ok := findInstallation(installs, planTarget)
		if !ok || candidate.StorageKey != key || !sameInstallationScope(candidate, covered, manifest) {
			return createTarget{}, false, stale()
		}
		switch candidate.Phase {
		case domain.InstallationPhaseDone:
			return createTarget{inst: candidate}, true, nil
		case domain.InstallationPhasePlanning, domain.InstallationPhaseDownloading,
			domain.InstallationPhaseVerifying, domain.InstallationPhaseInstalling:
			return createTarget{inst: candidate, join: true}, true, nil
		default:
			return createTarget{}, false, stale()
		}
	}
	var active, done *domain.Installation
	for i := range installs {
		inst := installs[i]
		if inst.StorageKey != key {
			continue
		}
		if !inst.Phase.Terminal() {
			candidate := inst
			active = &candidate
			continue
		}
		if inst.Phase == domain.InstallationPhaseDone {
			candidate := inst
			done = &candidate
		}
	}
	if active != nil {
		if !sameInstallationScope(*active, covered, manifest) {
			return createTarget{}, false, stale()
		}
		return createTarget{inst: *active, join: true}, true, nil
	}
	if done != nil {
		if !sameInstallationScope(*done, covered, manifest) {
			return createTarget{}, false, stale()
		}
		return createTarget{inst: *done}, true, nil
	}
	return createTarget{}, false, nil
}

func (s *Service) installationPlanFrom(
	ctx context.Context,
	getMeta func(context.Context, string) (string, bool, error),
	planID, planHash string,
) (storedPlan, error) {
	raw, found, err := getMeta(ctx, installationPlanMetaPrefix+planID)
	if err != nil {
		return storedPlan{}, err
	}
	if !found {
		return storedPlan{}, fmt.Errorf("%w: installation plan %s not found", domain.ErrPreflightStale, planID)
	}
	var stored storedPlan
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return storedPlan{}, fmt.Errorf("decode installation plan: %w", err)
	}
	if stored.PlanHash != planHash {
		return storedPlan{}, fmt.Errorf("%w: plan hash mismatch", domain.ErrPreflightStale)
	}
	if stored.FlashCLIRef != "" && !domain.AdmittedFlashCLIRef(stored.FlashCLIRef) {
		return storedPlan{}, fmt.Errorf("%w: installation plan flashcli ref is not admitted", domain.ErrPreflightStale)
	}
	if s.Now().Sub(stored.CreatedAt) > planTTL {
		return storedPlan{}, fmt.Errorf("%w: installation plan expired", domain.ErrPreflightStale)
	}
	return stored, nil
}

// installationPlanSubject 校验冻结覆盖/发布/文件清单并返回稳定身份。
func installationPlanSubject(stored storedPlan) ([]domain.CapabilitySlotKey, string, []domain.InstallationFile, error) {
	// 旧版计划没有冻结覆盖范围：必须重新 plan，不能按今天的目录悄悄扩范围。
	if len(stored.Plan.CoveredSlots) == 0 {
		return nil, "", nil, fmt.Errorf("%w: installation plan has no frozen coverage", domain.ErrPreflightStale)
	}
	covered, err := domain.NormalizeCapabilitySlots(stored.Plan.CoveredSlots)
	if err != nil || !domain.ContainsCapabilitySlot(covered, stored.Slot) {
		return nil, "", nil, fmt.Errorf("%w: installation plan coverage is not representable", domain.ErrPreflightStale)
	}
	if !validRelease(stored.Plan.Release) {
		return nil, "", nil, fmt.Errorf("%w: installation plan release is incomplete", domain.ErrPreflightStale)
	}
	if err := validateManifest(stored.Files); err != nil {
		return nil, "", nil, fmt.Errorf("%w: installation plan files are incomplete", domain.ErrPreflightStale)
	}
	key, err := domain.ReleaseStorageKey(stored.Plan.Release)
	if err != nil {
		return nil, "", nil, fmt.Errorf("%w: installation plan release is incomplete", domain.ErrPreflightStale)
	}
	return covered, key, installationManifest(stored.Files), nil
}

// verifyDoneInstallation 在事务之外校验已完成实体的最终文件。
func (s *Service) verifyDoneInstallation(ctx context.Context, candidate domain.Installation) error {
	if candidate.Phase != domain.InstallationPhaseDone {
		return errInstallationRaced
	}
	if candidate.VerifiedFiles <= 0 || candidate.Release == nil || !validRelease(*candidate.Release) {
		return errConflictf(domain.ErrorCodeCapabilityUnverified,
			"installation.reuse_unverified", "existing installation has no admissible verified evidence")
	}
	if len(candidate.Manifest) == 0 {
		return errConflictf(domain.ErrorCodeCapabilityUnverified,
			"installation.reuse_unverified", "existing installation has no frozen file manifest")
	}
	root := s.installationRoot(candidate)
	if root == "" {
		return errConflictf(domain.ErrorCodeCapabilityUnverified,
			"installation.reuse_unverified", "existing installation has no verifiable files")
	}
	if err := s.verifyInstallationFiles(ctx, root, candidate.Manifest); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return errConflictf(domain.ErrorCodeCapabilityUnverified,
			"installation.reuse_verification_failed", "existing installation files are missing or corrupt")
	}
	return nil
}

// activateDoneInstallation 在最终短事务内只做引用激活，不重复文件 IO；
// 重新检查证据与覆盖，拒绝未验证/已变化的实体。
func (s *Service) activateDoneInstallation(
	ctx context.Context,
	tx *store.Tx,
	candidate domain.Installation,
	covered []domain.CapabilitySlotKey,
	now time.Time,
) (domain.Installation, []domain.DomainEvent, error) {
	if candidate.Phase != domain.InstallationPhaseDone ||
		candidate.VerifiedFiles <= 0 || candidate.Release == nil || !validRelease(*candidate.Release) ||
		len(candidate.Manifest) == 0 {
		return domain.Installation{}, nil, errInstallationRaced
	}
	if !sameCapabilitySlotSet(candidate.CoveredSlots, covered) {
		return domain.Installation{}, nil, errInstallationRaced
	}
	if err := tx.SetCurrentBindings(ctx, candidate.ID, covered, now); err != nil {
		return domain.Installation{}, nil, err
	}
	var events []domain.DomainEvent
	if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
		Type: "installation.reused",
		Data: map[string]any{
			"installation_id": candidate.ID,
			"slots":           covered,
			"release_id":      releaseID(candidate.Release),
		},
	}); err != nil {
		return domain.Installation{}, nil, err
	}
	updated, err := tx.GetInstallation(ctx, candidate.ID)
	if err != nil {
		return domain.Installation{}, nil, err
	}
	return updated, events, nil
}

// idempotentReplay 在执行文件验证等副作用前判断持久回执是否存在。
func (s *Service) idempotentReplay(ctx context.Context, operation string, idem Idem) (bool, error) {
	if idem.Key == "" {
		return false, nil
	}
	existing, err := s.store.GetReceipt(ctx, s.opts.WorkspaceID, operation, idem.Key)
	switch {
	case err == nil:
		if existing.RequestDigest != idem.Digest {
			return false, fmt.Errorf("%w: key reused with a different request digest", domain.ErrIdempotencyConflict)
		}
		return true, nil
	case errors.Is(err, domain.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

func findInstallation(installs []domain.Installation, id domain.ID) (domain.Installation, bool) {
	for _, inst := range installs {
		if inst.ID == id {
			return inst, true
		}
	}
	return domain.Installation{}, false
}

func capabilitySlotsOverlap(a, b []domain.CapabilitySlotKey) bool {
	for _, slot := range a {
		if domain.ContainsCapabilitySlot(b, slot) {
			return true
		}
	}
	return false
}

func (s *Service) startInstallation(inst domain.Installation, root string, plan domain.InstallationPlan, files []downloader.File, ref string) {
	ctx, cancel := context.WithCancel(context.Background())
	s.installMu.Lock()
	if s.installs == nil {
		s.installs = make(map[domain.ID]context.CancelFunc)
	}
	if s.installRoots == nil {
		s.installRoots = make(map[domain.ID]string)
	}
	s.installs[inst.ID] = cancel
	s.installRoots[inst.ID] = root
	s.installMu.Unlock()
	go func() {
		defer func() {
			s.installMu.Lock()
			delete(s.installs, inst.ID)
			delete(s.installRoots, inst.ID)
			s.installMu.Unlock()
			cancel()
		}()
		s.runInstallation(ctx, inst, root, plan, files, ref)
	}()
}

// installationRootBusy 报告同一物理根是否仍有未退出的安装 worker 尾部。
// 只读短锁，不在持锁时访问数据库。
func (s *Service) installationRootBusy(root string) bool {
	if s == nil || root == "" {
		return false
	}
	clean := filepath.Clean(root)
	s.installMu.Lock()
	defer s.installMu.Unlock()
	for _, owned := range s.installRoots {
		if filepath.Clean(owned) == clean {
			return true
		}
	}
	return false
}

func (s *Service) installationRootsBusy(roots []string) bool {
	for _, root := range roots {
		if s.installationRootBusy(root) {
			return true
		}
	}
	return false
}

// installationRoot 返回记录对应的物理根：新布局按 storage_key 隔离，
// 旧记录仍可解析到原 slot/release 目录（不迁移、不去重）。
func (s *Service) installationRoot(inst domain.Installation) string {
	if inst.StorageKey != "" {
		return s.releaseRoot(inst.StorageKey)
	}
	if inst.Release != nil {
		return s.legacyInstallationDir(inst.Slot, *inst.Release)
	}
	return ""
}

// pathWithinRoot 报告 target 解析符号链接后仍位于 resolvedRoot 内。
func pathWithinRoot(resolvedRoot, target string) bool {
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false
	}
	return resolved == resolvedRoot ||
		strings.HasPrefix(resolved, resolvedRoot+string(os.PathSeparator))
}

// regularFileWithinRoot 报告 target 是 root 内、非符号链接的普通文件且大小一致。
func regularFileWithinRoot(resolvedRoot, target string, size int64) bool {
	if !pathWithinRoot(resolvedRoot, target) {
		return false
	}
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return false
	}
	return true
}

// canonicalRelPath 统一清单路径的分隔符、`.` 段与大小写，用于检测大小写不敏感
// 文件系统上的同一物理文件别名。保守小写归一会拒绝大小写不同的合法清单，但不会
// 把同一路径误当作两个条目。
func canonicalRelPath(rel string) string {
	return strings.ToLower(filepath.Clean(filepath.FromSlash(rel)))
}

// recoverInstallingFiles 判断 installing 记录是否具备补写 done 的本地证据，并提交
// 尚未落位的已验证 `.part` 文件。
//
// installing 阶段意味着全部文件已通过 SHA-256 校验（verified_files 与阶段在同一
// 事务持久化）。最终文件可能已经原子落位，也可能仍以 `.part` 存在（commit 中途
// 退出）。这里只复核路径、普通文件与大小，并原子提交 `.part`，不重新哈希大模型；
// 清单路径别名、大小写冲突或 `.part` 本身是另一条最终文件时拒绝自动完成。
func (s *Service) recoverInstallingFiles(inst domain.Installation) bool {
	if inst.Phase != domain.InstallationPhaseInstalling {
		return false
	}
	if inst.Release == nil || !validRelease(*inst.Release) {
		return false
	}
	if len(domain.EffectiveCoveredSlots(inst)) == 0 {
		return false
	}
	if len(inst.Manifest) == 0 || inst.VerifiedFiles != len(inst.Manifest) {
		return false
	}
	root := s.installationRoot(inst)
	if root == "" {
		return false
	}
	if inst.StorageKey != "" {
		key, err := domain.ReleaseStorageKey(*inst.Release)
		if err != nil || key != inst.StorageKey {
			return false
		}
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	manifestPaths := make(map[string]bool, len(inst.Manifest))
	for _, file := range inst.Manifest {
		key := canonicalRelPath(file.RelPath)
		if !filepath.IsLocal(key) || manifestPaths[key] {
			return false
		}
		manifestPaths[key] = true
	}
	for _, file := range inst.Manifest {
		key := canonicalRelPath(file.RelPath)
		if !filepath.IsLocal(key) {
			return false
		}
		rel := filepath.FromSlash(file.RelPath)
		dest := filepath.Join(root, rel)
		part := dest + ".part"
		if manifestPaths[key+".part"] {
			// 该 `.part` 同时是另一条清单项的最终文件，无法安全区分临时文件与最终文件。
			return false
		}
		partInfo, partErr := os.Lstat(part)
		switch {
		case partErr == nil:
			if !partInfo.Mode().IsRegular() || partInfo.Size() != file.Size {
				return false
			}
			if !pathWithinRoot(resolvedRoot, filepath.Dir(dest)) {
				return false
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return false
			}
			if err := os.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
				return false
			}
			if err := os.Rename(part, dest); err != nil {
				return false
			}
		case errors.Is(partErr, os.ErrNotExist):
			// 没有待提交的临时文件，直接复核最终文件。
		default:
			// 权限/占用等异常不能当作“没有临时文件”，否则可能误信旧最终文件。
			return false
		}
		if !regularFileWithinRoot(resolvedRoot, dest, file.Size) {
			return false
		}
	}
	return true
}

// RecoverInstallations 收敛上一次 Core 实例遗留的未完成安装。
//
// 安装执行是进程内 goroutine，重启后不会自动续跑（docs/local-api-contract.md §5）：
// planning/downloading/verifying 一律转为 FAILED（可重试，保留真实进度），并清理该
// 版本目录内的 `.part` 半成品；installing 阶段若已验证文件完整落位或仍以已验证
// `.part` 存在，则提交文件并补写 done、激活当前引用，避免把“文件已提交、done 事务
// 未写入”的崩溃窗口误报为未安装。其余 installing 记录仍按失败处理，但保留
// `.part` 供重试复用；已 done 记录、旧版本目录与其他槽位不受影响。成功补齐时不执行
// `.part` 清理，避免误删本身以 `.part` 结尾的合法最终文件；必须在开始服务请求前
// 调用（main 启动路径）。
func (s *Service) RecoverInstallations(ctx context.Context) error {
	installs, err := s.store.ListInstallations(ctx)
	if err != nil {
		return err
	}
	for _, inst := range installs {
		if inst.Phase.Terminal() {
			continue
		}
		if s.recoverInstallingFiles(inst) {
			if ref := s.flashCLIRefForRelease(inst.Slot, inst.Release); ref != "" &&
				!flashCLIPrepareMarkerMatches(s.installationRoot(inst), ref) {
				// 文件已落位但 FlashCLI 预拉未完成。启动阶段不在这里重新下载，
				// 交给用户重试，避免 Core 在对外服务前阻塞一小时。
				s.failInstallation(inst, inst.Progress, inst.VerifiedFiles, "installation.interrupted_on_restart", true)
				continue
			}
			if err := s.completeInstallation(inst, *inst.Release, len(inst.Manifest)); err != nil {
				s.logWarn("installation", "installation.recovery_complete_failed", map[string]any{
					"installation_id": string(inst.ID),
					"error":           err.Error(),
				})
				s.failInstallation(inst, inst.Progress, inst.VerifiedFiles, "installation.install_failed", true)
			}
			continue
		}
		dir := s.installationRoot(inst)
		// installing 记录可能已有合法最终文件与已验证 `.part`；失败时保留它们供重试
		// 复用，避免把清单中本身以 `.part` 结尾的最终文件当临时文件删除。
		if (inst.Phase != domain.InstallationPhaseInstalling || len(inst.Manifest) == 0) && dir != "" {
			if err := s.opts.Downloader.Cleanup(dir); err != nil {
				s.logWarn("installation", "installation.recovery_cleanup_failed", map[string]any{
					"installation_id": string(inst.ID),
					"error":           err.Error(),
				})
			}
		}
		s.failInstallation(inst, inst.Progress, inst.VerifiedFiles, "installation.interrupted_on_restart", true)
	}
	return nil
}

// runInstallation 驱动 planning→downloading→verifying→installing→done 的阶段推进。
func (s *Service) runInstallation(ctx context.Context, inst domain.Installation, dir string, plan domain.InstallationPlan, files []downloader.File, ref string) {
	if !domain.AdmittedFlashCLIRef(ref) {
		ref = s.rememberedFlashCLIRef(plan.PlanHash)
	}
	// 与目录文件下载并行启动预拉，避免下载过程中看不到 FlashCLI。
	prepareDone := make(chan error, 1)
	if ref != "" {
		go func() {
			s.logInfo("installation", "installation.model_prepare_started", map[string]any{
				"installation_id": string(inst.ID),
				"slot":            string(inst.Slot),
			})
			prepareDone <- s.prepareAdmittedModel(ctx, ref)
		}()
	}
	// 未成功落 done 的所有退出路径（失败、取消、阶段竞争）都清理 `.part`；
	// 旧版本目录不在 dir 内，不受影响。
	completed := false
	defer func() {
		if !completed {
			_ = s.opts.Downloader.Cleanup(dir)
		}
	}()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.failInstallation(inst, 0, 0, "installation.download_failed", true)
		return
	}
	if _, err := s.transitionInstallation(ctx, inst.ID, domain.InstallationPhaseDownloading, 0, 0); err != nil {
		return
	}

	total := int64(0)
	for _, file := range files {
		total += file.Size
	}
	progress := newInstallProgress(s, inst.ID, total)
	// 有 FlashCLI 权重时，进度条用权重清单的实际大小和已落盘字节，不用目录小包。
	trackWeights := ref != "" && s.opts.WeightProgress != nil

	var base int64
	var fetchErr error
	for _, file := range files {
		current := file
		baseAt := base
		fetchErr = s.opts.Downloader.Fetch(ctx, dir, current, func(written, _ int64) {
			if trackWeights {
				return
			}
			progress.report(baseAt + written)
		})
		if fetchErr != nil {
			break
		}
		base += current.Size
	}
	if fetchErr != nil {
		_ = s.opts.Downloader.Cleanup(dir)
		if ctx.Err() != nil {
			return
		}
		s.failInstallation(inst, progress.percent(), 0, "installation.download_failed", true)
		return
	}
	s.emitInstallationEvent(ctx, domain.DomainEvent{
		Type: "installation.downloaded",
		Data: map[string]any{
			"installation_id": inst.ID,
			"slot":            inst.Slot,
			"slots":           domain.EffectiveCoveredSlots(inst),
			"release_id":      plan.Release.ReleaseID,
			"files":           len(files),
			"bytes":           total,
		},
	})

	if _, err := s.transitionInstallation(ctx, inst.ID, domain.InstallationPhaseVerifying, progress.percent(), 0); err != nil {
		return
	}
	verified := 0
	for _, file := range files {
		if err := s.opts.Downloader.Verify(ctx, dir, file); err != nil {
			_ = s.opts.Downloader.Cleanup(dir)
			if ctx.Err() != nil {
				return
			}
			s.failInstallation(inst, progress.percent(), verified, "installation.verify_failed", true)
			return
		}
		verified++
		if _, err := s.progressInstallation(ctx, inst.ID, domain.InstallationPhaseVerifying, progress.percent(), verified); err != nil {
			return
		}
	}

	if _, err := s.transitionInstallation(ctx, inst.ID, domain.InstallationPhaseInstalling, progress.percent(), verified); err != nil {
		return
	}
	for _, file := range files {
		if ctx.Err() != nil {
			_ = s.opts.Downloader.Cleanup(dir)
			return
		}
		if err := s.opts.Downloader.Commit(dir, file); err != nil {
			_ = s.opts.Downloader.Cleanup(dir)
			s.failInstallation(inst, progress.percent(), verified, "installation.install_failed", true)
			return
		}
	}
	// 文件落位后等待并行预拉结束。未完成标记前不写 done，避免任务执行时才开始下载。
	if ref != "" {
		var prepareErr error
		pollCtx, stopPoll := context.WithCancel(ctx)
		if trackWeights {
			go s.pollFlashCLIWeightProgress(pollCtx, inst.ID, ref)
		}
		select {
		case prepareErr = <-prepareDone:
			stopPoll()
		case <-ctx.Done():
			stopPoll()
			return
		}
		if prepareErr != nil {
			if ctx.Err() != nil {
				return
			}
			s.logWarn("installation", "installation.model_prepare_failed", map[string]any{
				"installation_id": string(inst.ID),
				"error":           prepareErr.Error(),
			})
			s.failInstallation(inst, progress.percent(), verified, "installation.install_failed", true)
			return
		}
		if err := writeFlashCLIPrepareMarker(dir, ref); err != nil {
			s.failInstallation(inst, progress.percent(), verified, "installation.install_failed", true)
			return
		}
		invalidateFlashCLIListCache()
	}
	// 状态写入失败不得宣称完成：记录落 failed（可重试），已校验文件保留待重试复用。
	if err := s.completeInstallation(inst, plan.Release, verified); err != nil {
		s.failInstallation(inst, progress.percent(), verified, "installation.install_failed", true)
		completed = true
		return
	}
	completed = true
}

// transitionInstallation 迁移安装阶段并写入 phase_changed 事件。
func (s *Service) transitionInstallation(ctx context.Context, id domain.ID, to domain.InstallationPhase, progress, verified int) (domain.Installation, error) {
	return s.updateInstallationPhase(ctx, id, to, progress, verified, "installation.phase_changed", nil)
}

// progressInstallation 在当前阶段内推进真实进度（可丢弃的进度消息）。
func (s *Service) progressInstallation(ctx context.Context, id domain.ID, phase domain.InstallationPhase, progress, verified int) (domain.Installation, error) {
	return s.updateInstallationPhase(ctx, id, phase, progress, verified, "installation.progress", nil)
}

// reportInstallingProgress 只在记录仍处于 installing 时写入权重进度。
func (s *Service) reportInstallingProgress(ctx context.Context, id domain.ID, progress, verified int) error {
	if progress < 0 {
		progress = 0
	}
	if progress > 99 {
		progress = 99
	}
	var events []domain.DomainEvent
	updated := false
	err := s.store.DoTx(ctx, func(tx *store.Tx) error {
		inst, ok, err := tx.SetInstallationProgress(ctx, id, domain.InstallationPhaseInstalling, progress, verified, s.Now())
		if err != nil || !ok {
			return err
		}
		updated = true
		return s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type: "installation.progress",
			Data: map[string]any{
				"installation_id": inst.ID,
				"slot":            inst.Slot,
				"slots":           inst.CoveredSlots,
				"phase":           inst.Phase,
				"progress":        inst.Progress,
			},
		})
	})
	if err != nil || !updated {
		return err
	}
	s.publish(events...)
	return nil
}

func (s *Service) pollFlashCLIWeightProgress(ctx context.Context, id domain.ID, ref string) {
	measure := s.opts.WeightProgress
	if measure == nil {
		return
	}
	interval := s.opts.WeightProgressInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	measureOnce := func() {
		if ctx.Err() != nil {
			return
		}
		done, total, err := measure(ctx, ref)
		if err != nil || total <= 0 || done < 0 {
			return
		}
		if done > total {
			done = total
		}
		pct := int(done * 100 / total)
		if pct > 99 {
			pct = 99
		}
		current, err := s.GetInstallation(ctx, string(id))
		if err != nil || current.Phase != domain.InstallationPhaseInstalling || current.Progress == pct {
			return
		}
		_ = s.reportInstallingProgress(ctx, id, pct, current.VerifiedFiles)
	}
	measureOnce()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			measureOnce()
		}
	}
}

func (s *Service) updateInstallationPhase(ctx context.Context, id domain.ID, to domain.InstallationPhase, progress, verified int, eventType string, extra map[string]any) (domain.Installation, error) {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	var events []domain.DomainEvent
	var updated domain.Installation
	err := s.store.DoTx(ctx, func(tx *store.Tx) error {
		inst, err := tx.UpdateInstallationPhase(ctx, id, to, progress, verified, nil, false, s.Now())
		if err != nil {
			return err
		}
		data := map[string]any{
			"installation_id": inst.ID,
			"slot":            inst.Slot,
			"slots":           inst.CoveredSlots,
			"phase":           inst.Phase,
			"progress":        inst.Progress,
		}
		for key, value := range extra {
			data[key] = value
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{Type: eventType, Data: data}); err != nil {
			return err
		}
		updated = inst
		return nil
	})
	if err != nil {
		return domain.Installation{}, err
	}
	s.publish(events...)
	return updated, nil
}

// emitInstallationEvent 追加一条无状态变更的关键事件。
func (s *Service) emitInstallationEvent(ctx context.Context, ev domain.DomainEvent) {
	var appended domain.DomainEvent
	err := s.store.DoTx(ctx, func(tx *store.Tx) error {
		var err error
		appended, err = s.appendEvent(ctx, tx, ev)
		return err
	})
	if err != nil {
		s.logWarn("installation", "installation.event_failed", map[string]any{"type": ev.Type})
		return
	}
	s.publish(appended)
}

// completeInstallation 落 done：同一事务激活全部冻结覆盖槽位；旧版本目录保留供回滚。
func (s *Service) completeInstallation(inst domain.Installation, release domain.ReleaseRef, verified int) error {
	slots := domain.EffectiveCoveredSlots(inst)
	var events []domain.DomainEvent
	err := s.store.DoTx(context.Background(), func(tx *store.Tx) error {
		done, err := tx.UpdateInstallationPhase(context.Background(), inst.ID,
			domain.InstallationPhaseDone, 100, verified, nil, false, s.Now())
		if err != nil {
			return err
		}
		// Interim 目录尚无独立可执行探针；MVP 以“全文件 SHA-256 校验通过”
		// 作为就绪门槛。冻结执行配置仍只接受 probe_passed=1 的 done 记录，
		// 因此在该事务内显式落标志；接入真实 probe 后改为探针结果。
		if err := tx.MarkInstallationProbePassed(context.Background(), done.ID, true, s.Now()); err != nil {
			return err
		}
		if err := tx.SetCurrentBindings(context.Background(), done.ID, slots, s.Now()); err != nil {
			return err
		}
		if err := s.stageEvent(context.Background(), tx, &events, domain.DomainEvent{
			Type: "installation.phase_changed",
			Data: map[string]any{
				"installation_id": done.ID,
				"slot":            done.Slot,
				"slots":           slots,
				"phase":           done.Phase,
				"progress":        done.Progress,
			},
		}); err != nil {
			return err
		}
		return s.stageEvent(context.Background(), tx, &events, domain.DomainEvent{
			Type: "installation.completed",
			Data: map[string]any{
				"installation_id": done.ID,
				"slot":            done.Slot,
				"slots":           slots,
				"release_id":      release.ReleaseID,
				"version":         release.Version,
				"verified_files":  done.VerifiedFiles,
			},
		})
	})
	if err != nil {
		s.logWarn("installation", "installation.complete_failed", map[string]any{"installation_id": string(inst.ID)})
		return err
	}
	s.publish(events...)
	return nil
}

// failInstallation 落 failed 并记录安全错误（不激活、不删除旧版本）。
func (s *Service) failInstallation(inst domain.Installation, progress, verified int, messageKey string, retryable bool) {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	actions := []domain.ID{"retry_installation"}
	if !retryable {
		actions = []domain.ID{"open_model_setup"}
	}
	info := domain.ErrorInfo{
		Code:       domain.ErrorCodeCapabilityUnverified,
		MessageKey: messageKey,
		Retryable:  retryable,
		ActionIDs:  actions,
	}
	var events []domain.DomainEvent
	err := s.store.DoTx(context.Background(), func(tx *store.Tx) error {
		failed, err := tx.UpdateInstallationPhase(context.Background(), inst.ID,
			domain.InstallationPhaseFailed, progress, verified, &info, retryable, s.Now())
		if err != nil {
			return err
		}
		if err := s.stageEvent(context.Background(), tx, &events, domain.DomainEvent{
			Type: "installation.phase_changed",
			Data: map[string]any{
				"installation_id": failed.ID,
				"slot":            failed.Slot,
				"slots":           failed.CoveredSlots,
				"phase":           failed.Phase,
				"progress":        failed.Progress,
			},
		}); err != nil {
			return err
		}
		return s.stageEvent(context.Background(), tx, &events, domain.DomainEvent{
			Type: "installation.failed",
			Data: map[string]any{
				"installation_id": failed.ID,
				"slot":            failed.Slot,
				"slots":           failed.CoveredSlots,
				"code":            info.Code,
				"message_key":     info.MessageKey,
				"retryable":       info.Retryable,
			},
		})
	})
	if err != nil {
		s.logWarn("installation", "installation.fail_persist_failed", map[string]any{"installation_id": string(inst.ID)})
		return
	}
	s.publish(events...)
}

// installProgress 把下载字节进度节流为持久进度与 installation.progress 事件。
// 进度是真实已下载字节，封顶 99，done 时才到 100。
type installProgress struct {
	svc         *Service
	id          domain.ID
	total       int64
	minInterval time.Duration

	mu      sync.Mutex
	started bool
	lastAt  time.Time
	lastPct int
}

func newInstallProgress(svc *Service, id domain.ID, total int64) *installProgress {
	return &installProgress{svc: svc, id: id, total: total, minInterval: 500 * time.Millisecond, lastPct: -1}
}

func (p *installProgress) report(done int64) {
	pct := p.percentFor(done)
	p.mu.Lock()
	now := time.Now()
	if p.started {
		if pct < p.lastPct {
			pct = p.lastPct
		}
		if now.Sub(p.lastAt) < p.minInterval || pct == p.lastPct {
			p.mu.Unlock()
			return
		}
	}
	p.started = true
	p.lastAt = now
	p.lastPct = pct
	p.mu.Unlock()
	_, _ = p.svc.progressInstallation(context.Background(), p.id, domain.InstallationPhaseDownloading, pct, 0)
}

func (p *installProgress) percentFor(done int64) int {
	if p.total <= 0 {
		return 0
	}
	pct := int(done * 100 / p.total)
	if pct < 0 {
		return 0
	}
	if pct > 99 {
		return 99
	}
	return pct
}

func (p *installProgress) percent() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastPct < 0 {
		return 0
	}
	return p.lastPct
}

// CancelInstallation 取消安装（202）：先停止写入，再登记取消；保留旧模型与有效缓存。
// 取消作用于该物理安装及其全部覆盖槽位，不代表 worker 已退出。
func (s *Service) CancelInstallation(ctx context.Context, idem Idem, id string) (Response, error) {
	var events []domain.DomainEvent
	shouldCancel := false
	resp, err := s.write(ctx, "installations.cancel", idem, 202, func(ctx context.Context, tx *store.Tx) (any, error) {
		inst, err := tx.GetInstallation(ctx, id)
		if err != nil {
			return nil, err
		}
		if inst.Phase.Terminal() {
			return inst, nil
		}
		shouldCancel = true
		updated, err := tx.UpdateInstallationPhase(ctx, id, domain.InstallationPhaseCanceled,
			inst.Progress, inst.VerifiedFiles, nil, false, s.Now())
		if err != nil {
			return nil, err
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type: "installation.phase_changed",
			Data: map[string]any{
				"installation_id": updated.ID,
				"slot":            updated.Slot,
				"slots":           updated.CoveredSlots,
				"phase":           updated.Phase,
			},
		}); err != nil {
			return nil, err
		}
		return updated, nil
	})
	if err != nil {
		return Response{}, err
	}
	if shouldCancel {
		s.installMu.Lock()
		cancel, ok := s.installs[id]
		s.installMu.Unlock()
		if ok {
			// 先停止写入（取消传输上下文），再登记取消；旧模型与缓存保留。
			cancel()
		}
	}
	s.publish(events...)
	return resp, nil
}

// DeleteInstalledPackage 解除槽位的当前/保留安装引用（204）。
//
// 只解绑请求槽位；其他槽位与保留版本继续保护物理文件。只有最后一个引用
// 移除且没有相关 worker 尾部/可推进 Run 冻结锁时，才在事务产出的清单上
// 回收目录；幂等重放不重复文件删除。
func (s *Service) DeleteInstalledPackage(ctx context.Context, idem Idem, slotValue string) (Response, error) {
	slot, ok := capabilitySlotOf(slotValue)
	if !ok {
		return Response{}, notFoundf("unknown capability slot %q", slotValue)
	}
	s.installationLifecycleMu.Lock()
	defer s.installationLifecycleMu.Unlock()

	replay, err := s.idempotentReplay(ctx, "installations.delete", idem)
	if err != nil {
		return Response{}, err
	}
	if !replay {
		refs, err := s.flashCLIRefsReleasedByDelete(ctx, slot)
		if err != nil {
			return Response{}, err
		}
		for _, ref := range refs {
			if err := s.removeAdmittedModel(ctx, ref); err != nil {
				s.logWarn("installation", "installation.model_remove_failed", map[string]any{
					"slot":  string(slot),
					"error": err.Error(),
				})
				return Response{}, &domain.Error{
					Code:       domain.ErrorCodeWorkerExited,
					MessageKey: "installation.install_failed",
					Retryable:  true,
				}
			}
			invalidateFlashCLIListCache()
		}
	}

	var events []domain.DomainEvent
	var removeRoots []string
	executed := false
	resp, err := s.write(ctx, "installations.delete", idem, 204, func(ctx context.Context, tx *store.Tx) (any, error) {
		executed = true
		installs, err := tx.ListInstallations(ctx)
		if err != nil {
			return nil, err
		}
		bound := make([]domain.Installation, 0)
		for _, inst := range installs {
			if domain.ContainsCapabilitySlot(inst.BoundSlots, slot) {
				bound = append(bound, inst)
			}
		}
		if len(bound) == 0 {
			return nil, notFoundf("no installed package for slot %s", slot)
		}
		for _, inst := range bound {
			if !inst.Phase.Terminal() {
				return nil, errResourceBusy("installation is still running")
			}
		}
		active, err := tx.CountActiveTasks(ctx, "")
		if err != nil {
			return nil, err
		}
		if active > 0 {
			return nil, errResourceBusy("active tasks still reference installed packages")
		}
		// 仍可推进 Run 的精确冻结锁（含依赖槽位）不能被静默抽掉版本。
		runExecutions, err := tx.ListNonterminalRunExecutions(ctx)
		if err != nil {
			return nil, err
		}
		for _, execution := range runExecutions {
			cfg, frozen := execution.Config.Execution[slot]
			if !frozen || !validRelease(cfg.ModelLock) {
				continue
			}
			for _, inst := range bound {
				if inst.Release != nil && *inst.Release == cfg.ModelLock {
					return nil, errResourceBusy("a nonterminal run still freezes this installed release")
				}
			}
		}
		// 同一物理根上的 worker 尾部（含已取消）阻止删除。
		roots := make([]string, 0, len(bound))
		for _, inst := range bound {
			if root := s.installationRoot(inst); root != "" {
				roots = append(roots, root)
			}
		}
		if s.installationRootsBusy(roots) {
			return nil, errResourceBusy("an installation worker still owns the installed directory")
		}

		if _, err := tx.DeleteSlotBindings(ctx, slot); err != nil {
			return nil, err
		}
		removed := make([]domain.Installation, 0)
		for _, inst := range bound {
			still, err := tx.GetInstallation(ctx, inst.ID)
			if err != nil {
				return nil, err
			}
			if len(still.BoundSlots) > 0 {
				continue
			}
			if err := tx.DeleteInstallation(ctx, inst.ID); err != nil {
				return nil, err
			}
			removed = append(removed, inst)
		}
		// 只在整行删除后再判断物理目录是否还有任何剩余引用。
		after, err := tx.ListInstallations(ctx)
		if err != nil {
			return nil, err
		}
		remainingKeys := make(map[string]struct{}, len(after))
		remainingLegacySlots := make(map[domain.CapabilitySlotKey]struct{}, len(after))
		for _, inst := range after {
			if inst.StorageKey != "" {
				remainingKeys[inst.StorageKey] = struct{}{}
			} else {
				remainingLegacySlots[inst.Slot] = struct{}{}
			}
		}
		seen := make(map[string]struct{})
		for _, inst := range removed {
			if inst.StorageKey != "" {
				if _, ok := remainingKeys[inst.StorageKey]; ok {
					continue
				}
				root := s.releaseRoot(inst.StorageKey)
				if _, ok := seen[root]; ok {
					continue
				}
				seen[root] = struct{}{}
				removeRoots = append(removeRoots, root)
				continue
			}
			if _, ok := remainingLegacySlots[inst.Slot]; ok {
				continue
			}
			root := filepath.Join(s.modelsDir(), string(inst.Slot))
			if _, ok := seen[root]; ok {
				continue
			}
			seen[root] = struct{}{}
			removeRoots = append(removeRoots, root)
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type: "installation.deleted",
			Data: map[string]any{"slot": slot},
		}); err != nil {
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		return Response{}, err
	}
	if executed {
		for _, root := range removeRoots {
			if err := os.RemoveAll(root); err != nil {
				s.logWarn("installation", "installation.delete_files_failed", map[string]any{"slot": slot})
			}
		}
		s.InvalidateStorageUsage()
	}
	s.publish(events...)
	return resp, nil
}

// RollbackInstalledPackage 回滚请求槽位到上一不同且文件可验证的保留实体（202）。
//
// 不创建新 Done 记录，不改其他槽位或已冻结的 Task/Run 锁；旧记录没有
// 冻结清单时只能使用其原始计划清单，缺证据则返回可读冲突。
func (s *Service) RollbackInstalledPackage(ctx context.Context, idem Idem, slotValue string) (Response, error) {
	slot, ok := capabilitySlotOf(slotValue)
	if !ok {
		return Response{}, notFoundf("unknown capability slot %q", slotValue)
	}
	s.installationLifecycleMu.Lock()
	defer s.installationLifecycleMu.Unlock()

	// 幂等重放直接返回原快照：不重新验证文件、不重做绑定切换。
	replay, err := s.idempotentReplay(ctx, "installations.rollback", idem)
	if err != nil {
		return Response{}, err
	}
	if replay {
		return s.rollbackWrite(ctx, idem, slot, nil)
	}
	for attempt := 0; attempt < 3; attempt++ {
		prepared, err := s.prepareRollback(ctx, slot)
		if err != nil {
			return Response{}, err
		}
		resp, err := s.rollbackWrite(ctx, idem, slot, prepared)
		if err == nil {
			return resp, nil
		}
		if !errors.Is(err, errInstallationRaced) {
			return Response{}, err
		}
	}
	return Response{}, errConflictf(domain.ErrorCodeRevisionConflict,
		"installation.rollback_raced", "installation state changed during verification; retry")
}

// preparedRollback 记录事务外已完成文件验证的保留实体；事务内必须仍相同。
type preparedRollback struct {
	installationID domain.ID
	release        domain.ReleaseRef
}

// prepareRollback 在全局写锁与事务之外选择上一不同保留实体并校验最终文件；
// installationLifecycleMu 保证验证期间目录不被删除/重装。
func (s *Service) prepareRollback(ctx context.Context, slot domain.CapabilitySlotKey) (*preparedRollback, error) {
	installs, err := s.store.ListInstallations(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkRollbackActivity(ctx, s.store, slot, installs); err != nil {
		return nil, err
	}
	bindings, err := s.store.ListInstallationBindings(ctx)
	if err != nil {
		return nil, err
	}
	_, previous, err := selectRollbackCandidate(installs, bindings, slot)
	if err != nil {
		return nil, err
	}
	manifest, err := s.rollbackManifest(ctx, previous)
	if err != nil {
		return nil, err
	}
	root := s.installationRoot(previous)
	if root == "" {
		return nil, errConflictf(domain.ErrorCodeCapabilityUnverified,
			"installation.rollback_unverified", "retained installation has no verifiable files")
	}
	if err := s.verifyInstallationFiles(ctx, root, manifest); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, errConflictf(domain.ErrorCodeCapabilityUnverified,
			"installation.rollback_unverified", "retained installation files are missing or corrupt")
	}
	if previous.Release == nil {
		return nil, errConflictf(domain.ErrorCodeCapabilityUnverified,
			"installation.rollback_unverified", "retained installation has no release evidence")
	}
	return &preparedRollback{installationID: previous.ID, release: *previous.Release}, nil
}

func (s *Service) rollbackWrite(ctx context.Context, idem Idem, slot domain.CapabilitySlotKey, prepared *preparedRollback) (Response, error) {
	var events []domain.DomainEvent
	resp, err := s.write(ctx, "installations.rollback", idem, 202, func(ctx context.Context, tx *store.Tx) (any, error) {
		if prepared == nil {
			// 预检说已有回执；事务内若真的执行则状态已变化，安全失败而不是无验证切换。
			return nil, fmt.Errorf("%w: idempotent rollback replay state changed", domain.ErrPreflightStale)
		}
		installs, err := tx.ListInstallations(ctx)
		if err != nil {
			return nil, err
		}
		// 与删除同一口径：事务内重检活动 Task 与仍可推进 Run 的精确冻结引用；
		// 用户新建工作抢先时不得切换当前选择。
		if err := s.checkRollbackActivity(ctx, tx, slot, installs); err != nil {
			return nil, err
		}
		bindings, err := tx.ListInstallationBindings(ctx)
		if err != nil {
			return nil, err
		}
		_, previous, err := selectRollbackCandidate(installs, bindings, slot)
		if err != nil {
			return nil, err
		}
		if previous.ID != prepared.installationID || previous.Release == nil || *previous.Release != prepared.release {
			return nil, errInstallationRaced
		}
		now := s.Now()
		if err := tx.SetCurrentBindings(ctx, previous.ID, []domain.CapabilitySlotKey{slot}, now); err != nil {
			return nil, err
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type: "installation.rollback",
			Data: map[string]any{
				"installation_id": previous.ID,
				"slot":            slot,
				"release_id":      releaseID(previous.Release),
			},
		}); err != nil {
			return nil, err
		}
		return tx.GetInstallation(ctx, previous.ID)
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	return resp, nil
}

// installationActivityQuerier 是回滚/删除共用的活动工作读取面（Store 与 Tx）。
type installationActivityQuerier interface {
	CountActiveTasks(ctx context.Context, projectID domain.ID) (int, error)
	ListNonterminalRunExecutions(ctx context.Context) ([]store.NonterminalRunExecution, error)
}

// checkRollbackActivity 与删除同口径：活动 Task 或仍可推进 Run 的精确冻结
// 引用存在时拒绝切换当前选择。
func (s *Service) checkRollbackActivity(
	ctx context.Context,
	q installationActivityQuerier,
	slot domain.CapabilitySlotKey,
	installs []domain.Installation,
) error {
	active, err := q.CountActiveTasks(ctx, "")
	if err != nil {
		return err
	}
	if active > 0 {
		return errResourceBusy("active tasks still reference installed packages")
	}
	runExecutions, err := q.ListNonterminalRunExecutions(ctx)
	if err != nil {
		return err
	}
	for _, execution := range runExecutions {
		cfg, frozen := execution.Config.Execution[slot]
		if !frozen || !validRelease(cfg.ModelLock) {
			continue
		}
		for _, inst := range installs {
			if domain.ContainsCapabilitySlot(inst.BoundSlots, slot) &&
				inst.Release != nil && *inst.Release == cfg.ModelLock {
				return errResourceBusy("a nonterminal run still freezes this installed release")
			}
		}
	}
	return nil
}

// selectRollbackCandidate 返回槽位当前实体与上一不同且证据完整的保留实体；
// 活动安装存在时拒绝。bindings 已按 activated_at DESC 排序。
func selectRollbackCandidate(
	installs []domain.Installation,
	bindings []store.InstallationBinding,
	slot domain.CapabilitySlotKey,
) (domain.Installation, domain.Installation, error) {
	byID := make(map[domain.ID]domain.Installation, len(installs))
	var current *domain.Installation
	for i := range installs {
		inst := installs[i]
		byID[inst.ID] = inst
		if !inst.Phase.Terminal() && domain.ContainsCapabilitySlot(inst.BoundSlots, slot) {
			return domain.Installation{}, domain.Installation{},
				errResourceBusy("installation is still running for this slot")
		}
		if domain.ContainsCapabilitySlot(inst.CurrentSlots, slot) {
			candidate := inst
			current = &candidate
		}
	}
	if current == nil {
		return domain.Installation{}, domain.Installation{}, errConflictf(domain.ErrorCodeRevisionConflict,
			"installation.rollback_unavailable", "slot %s has no previous retained version", slot)
	}
	var previous *domain.Installation
	for _, binding := range bindings {
		if binding.Slot != slot || binding.Current || binding.InstallationID == current.ID {
			continue
		}
		inst, ok := byID[binding.InstallationID]
		if !ok || inst.Phase != domain.InstallationPhaseDone || !installationAdmissible(inst) {
			continue
		}
		if current.Release != nil && inst.Release != nil && *inst.Release == *current.Release {
			continue // 只回滚到“上一不同精确发布”
		}
		candidate := inst
		previous = &candidate
		break
	}
	if previous == nil {
		return domain.Installation{}, domain.Installation{}, errConflictf(domain.ErrorCodeRevisionConflict,
			"installation.rollback_unavailable", "slot %s has no previous retained version", slot)
	}
	return *current, *previous, nil
}

// rollbackManifest 返回保留实体的冻结清单；旧记录使用其原始计划清单。
func (s *Service) rollbackManifest(ctx context.Context, inst domain.Installation) ([]domain.InstallationFile, error) {
	if len(inst.Manifest) > 0 {
		return inst.Manifest, nil
	}
	return s.legacyPlanManifest(ctx, inst)
}

// legacyPlanManifest 用旧记录创建时的计划清单做文件校验；证据缺失返回可读冲突。
func (s *Service) legacyPlanManifest(ctx context.Context, inst domain.Installation) ([]domain.InstallationFile, error) {
	if inst.PlanID == "" {
		return nil, errConflictf(domain.ErrorCodeCapabilityUnverified,
			"installation.rollback_unverified", "retained installation has no frozen file manifest")
	}
	raw, found, err := s.store.GetMeta(ctx, installationPlanMetaPrefix+inst.PlanID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errConflictf(domain.ErrorCodeCapabilityUnverified,
			"installation.rollback_unverified", "retained installation plan evidence is unavailable")
	}
	var stored storedPlan
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return nil, errConflictf(domain.ErrorCodeCapabilityUnverified,
			"installation.rollback_unverified", "retained installation plan evidence is unreadable")
	}
	if len(stored.Files) == 0 {
		return nil, errConflictf(domain.ErrorCodeCapabilityUnverified,
			"installation.rollback_unverified", "retained installation plan has no files")
	}
	return installationManifest(stored.Files), nil
}

// verifyInstallationFiles 校验最终落位文件的相对路径、大小与 SHA-256。
//
// 只接受安装根内的相对路径；未提供完整清单视为不可验证（不伪成功）。
func verifyInstallationFiles(ctx context.Context, root string, manifest []domain.InstallationFile) error {
	if root == "" || len(manifest) == 0 {
		return fmt.Errorf("installation files are not verifiable")
	}
	for _, file := range manifest {
		path, err := installationFilePath(root, file.RelPath)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != file.Size {
			return fmt.Errorf("installation file %s size mismatch", file.RelPath)
		}
		sum, err := hashInstallationFile(ctx, path)
		if err != nil {
			return err
		}
		if !strings.EqualFold(hex.EncodeToString(sum), file.SHA256) {
			return fmt.Errorf("installation file %s sha256 mismatch", file.RelPath)
		}
	}
	return nil
}

func installationFilePath(root, relPath string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relPath))
	if clean == "." || clean == "" || filepath.IsAbs(clean) ||
		strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, `\`) ||
		clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("installation file %q escapes the install directory", relPath)
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target := filepath.Join(base, clean)
	if target != base && !strings.HasPrefix(target, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("installation file %q escapes the install directory", relPath)
	}
	return target, nil
}

func hashInstallationFile(ctx context.Context, path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	h := sha256.New()
	buf := make([]byte, 256*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, readErr := file.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return h.Sum(nil), nil
}

func releaseID(ref *domain.ReleaseRef) string {
	if ref == nil {
		return ""
	}
	return ref.ReleaseID
}

func (s *Service) logInfo(component, event string, fields map[string]any) {
	if s.log != nil {
		s.log.Info(component, event, fields)
	}
}

func (s *Service) logWarn(component, event string, fields map[string]any) {
	if s.log != nil {
		s.log.Warn(component, event, fields)
	}
}
