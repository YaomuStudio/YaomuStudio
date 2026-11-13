package service

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/system"
)

// EnvironmentSnapshot 是 service 层的环境快照；Devices 已包含 CPU/MPS 兜底。
type EnvironmentSnapshot struct {
	Platform string
	WSL      *domain.WSLStatus
	Devices  []domain.DeviceInfo
	Checks   []domain.EnvironmentCheck
}

// EnvironmentProvider 返回本机执行环境；只读，不写业务库。
type EnvironmentProvider interface {
	Snapshot(ctx context.Context) (EnvironmentSnapshot, error)
}

// DeviceRefresher 由需要执行外部探测的设备提供者实现；读路径可显式刷新。
type DeviceRefresher interface {
	Refresh(ctx context.Context)
}

// EnvironmentForcer 由支持显式重检的环境提供者实现；用于用户点击“重新检测”。
type EnvironmentForcer interface {
	ForceRefresh(ctx context.Context)
}

// SystemEnvironment 是默认环境实现：只有设备清单，没有 WSL/检查项。
type SystemEnvironment struct {
	Devices DeviceProvider
}

// Snapshot 实现 EnvironmentProvider。
func (e SystemEnvironment) Snapshot(ctx context.Context) (EnvironmentSnapshot, error) {
	devices := e.Devices
	if devices == nil {
		devices = SystemDevices{}
	}
	if refresher, ok := devices.(DeviceRefresher); ok {
		refresher.Refresh(ctx)
	}
	list, err := devices.List(ctx)
	if err != nil {
		return EnvironmentSnapshot{}, err
	}
	if list == nil {
		list = []domain.DeviceInfo{}
	}
	return EnvironmentSnapshot{
		Platform: runtime.GOOS,
		Devices:  list,
		Checks:   []domain.EnvironmentCheck{},
	}, nil
}

// EnvironmentProbe 执行一次系统探测；生产实现是 system.ProbeEnvironment。
type EnvironmentProbe func(ctx context.Context) (system.EnvironmentSnapshot, error)

// EnvironmentDevices 缓存系统探测结果，并实现 DeviceProvider 与 EnvironmentProvider。
//
// List 只读缓存，绝不在 SQLite 写事务内执行外部命令；Refresh 负责探测。
type EnvironmentDevices struct {
	refreshMu sync.Mutex
	mu        sync.RWMutex

	probe       EnvironmentProbe
	ttl         time.Duration
	now         func() time.Time
	snapshot    system.EnvironmentSnapshot
	hasSnapshot bool
	checkedAt   time.Time
	lastErr     error
}

var (
	_ DeviceProvider      = (*EnvironmentDevices)(nil)
	_ DeviceRefresher     = (*EnvironmentDevices)(nil)
	_ EnvironmentForcer   = (*EnvironmentDevices)(nil)
	_ EnvironmentProvider = (*EnvironmentDevices)(nil)
)

// NewEnvironmentDevices 构造带缓存的设备/环境提供者。
func NewEnvironmentDevices(probe EnvironmentProbe) *EnvironmentDevices {
	if probe == nil {
		probe = func(ctx context.Context) (system.EnvironmentSnapshot, error) {
			return system.ProbeEnvironment(ctx, system.ProbeOptions{}), nil
		}
	}
	return &EnvironmentDevices{
		probe: probe,
		ttl:   15 * time.Second,
		now:   time.Now,
	}
}

// Refresh 在 TTL 外重新探测；探测失败时保留上一份快照。
func (d *EnvironmentDevices) Refresh(ctx context.Context) {
	if d == nil {
		return
	}
	d.refresh(ctx, false)
}

// ForceRefresh 跳过 TTL 立即探测；用于用户显式点击“重新检测”。
func (d *EnvironmentDevices) ForceRefresh(ctx context.Context) {
	if d == nil {
		return
	}
	d.refresh(ctx, true)
}

func (d *EnvironmentDevices) refresh(ctx context.Context, force bool) {
	d.refreshMu.Lock()
	defer d.refreshMu.Unlock()

	d.mu.RLock()
	fresh := d.hasSnapshot && d.now().Sub(d.checkedAt) < d.ttl
	d.mu.RUnlock()
	if !force && fresh {
		return
	}

	snapshot, err := d.probe(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	if err == nil {
		d.snapshot = snapshot
		d.hasSnapshot = true
	}
	// 失败也记录检查时间，避免连续请求反复触发 6s 探测；TTL 到期后自动重试。
	d.checkedAt = d.now()
	d.lastErr = err
}

// List 只读缓存；缓存未建立时返回 CPU/MPS 兜底。
func (d *EnvironmentDevices) List(ctx context.Context) ([]domain.DeviceInfo, error) {
	base, err := SystemDevices{}.List(ctx)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return base, nil
	}
	d.mu.RLock()
	snapshot, ok := d.snapshot, d.hasSnapshot
	d.mu.RUnlock()
	if !ok {
		return base, nil
	}
	return mergeDeviceMetrics(mergeGPUDevices(base, snapshot.GPUs), snapshot.Host), nil
}

// Snapshot 按 TTL 刷新后返回环境快照；探测错误以检查项表达，不返回 5xx。
func (d *EnvironmentDevices) Snapshot(ctx context.Context) (EnvironmentSnapshot, error) {
	if d == nil {
		return SystemEnvironment{Devices: SystemDevices{}}.Snapshot(ctx)
	}
	d.refresh(ctx, false)

	d.mu.RLock()
	snapshot, ok, lastErr := d.snapshot, d.hasSnapshot, d.lastErr
	d.mu.RUnlock()

	base, err := SystemDevices{}.List(ctx)
	if err != nil {
		return EnvironmentSnapshot{}, err
	}
	if !ok {
		checks := []domain.EnvironmentCheck{}
		if lastErr != nil {
			checks = append(checks, environmentProbeFailedCheck())
		}
		return EnvironmentSnapshot{
			Platform: runtime.GOOS,
			Devices:  base,
			Checks:   checks,
		}, nil
	}

	checks := mapEnvironmentChecks(snapshot.Checks)
	if lastErr != nil {
		checks = append(checks, environmentProbeFailedCheck())
	}
	platform := snapshot.Platform
	if platform == "" {
		platform = runtime.GOOS
	}
	return EnvironmentSnapshot{
		Platform: platform,
		WSL:      mapWSLStatus(snapshot.WSL),
		Devices:  mergeDeviceMetrics(mergeGPUDevices(base, snapshot.GPUs), snapshot.Host),
		Checks:   checks,
	}, nil
}

func mergeGPUDevices(base []domain.DeviceInfo, gpus []system.GPUInfo) []domain.DeviceInfo {
	if len(gpus) == 0 {
		return base
	}
	// GPU 可用时 CPU/MPS 不再是推荐项；显式配置仍可通过设备选择覆盖。
	for i := range base {
		base[i].Recommended = false
	}
	for index, gpu := range gpus {
		total := gpu.MemoryTotalBytes
		used := gpu.MemoryUsedBytes
		base = append(base, domain.DeviceInfo{
			ID:               fmt.Sprintf("cuda:%d", index),
			Name:             gpu.Name,
			Kind:             domain.DeviceKindCUDA,
			Recommended:      index == 0,
			MemoryTotalBytes: &total,
			MemoryUsedBytes:  &used,
			DriverVersion:    gpu.DriverVersion,
		})
	}
	return base
}

// mergeDeviceMetrics 把平台探测到的 CPU/统一内存信息写入 CPU 与 MPS 设备。
// CUDA 的显存/驱动由 mergeGPUDevices 提供，不在这里覆盖。
func mergeDeviceMetrics(devices []domain.DeviceInfo, host system.HostInfo) []domain.DeviceInfo {
	for i := range devices {
		switch devices[i].Kind {
		case domain.DeviceKindCPU:
			if host.CPUName != "" {
				devices[i].Name = host.CPUName
			}
			applyHostMemory(&devices[i], host.Memory)
		case domain.DeviceKindMPS:
			if host.CPUName != "" {
				devices[i].Name = host.CPUName + " GPU"
			}
			applyHostMemory(&devices[i], host.Memory)
		}
	}
	return devices
}

func applyHostMemory(device *domain.DeviceInfo, memory system.MemoryInfo) {
	if memory.HasTotal {
		total := memory.TotalBytes
		device.MemoryTotalBytes = &total
	}
	if memory.HasUsed {
		used := memory.UsedBytes
		device.MemoryUsedBytes = &used
	}
}

func mapWSLStatus(status *system.WSLStatus) *domain.WSLStatus {
	if status == nil {
		return nil
	}
	out := &domain.WSLStatus{
		Available: status.Available,
		Version:   status.Version,
	}
	if status.CUDAAvailable != nil {
		value := *status.CUDAAvailable
		out.CUDAAvailable = &value
	}
	return out
}

func mapEnvironmentChecks(checks []system.Check) []domain.EnvironmentCheck {
	out := make([]domain.EnvironmentCheck, 0, len(checks))
	for _, check := range checks {
		severity := domain.PreflightSeverity(check.Severity)
		if !severity.Valid() {
			severity = domain.PreflightWarning
		}
		actions := make([]domain.ID, 0, len(check.ActionIDs))
		for _, action := range check.ActionIDs {
			actions = append(actions, domain.ID(action))
		}
		out = append(out, domain.EnvironmentCheck{
			Code:       check.Code,
			Severity:   severity,
			MessageKey: check.MessageKey,
			ActionIDs:  actions,
		})
	}
	return out
}

func environmentProbeFailedCheck() domain.EnvironmentCheck {
	return domain.EnvironmentCheck{
		Code:       "ENVIRONMENT_PROBE_FAILED",
		Severity:   domain.PreflightWarning,
		MessageKey: "environment.probe_failed",
		ActionIDs:  []domain.ID{},
	}
}

// Environment 返回本机环境快照，并复用设备选中/占用标注。
// force=true 时跳过探测缓存，供用户显式重新检测。
func (s *Service) Environment(ctx context.Context, force bool) (domain.EnvironmentInfo, error) {
	provider := s.opts.Environment
	if provider == nil {
		if envProvider, ok := s.opts.Devices.(EnvironmentProvider); ok {
			provider = envProvider
		} else {
			provider = SystemEnvironment{Devices: s.opts.Devices}
		}
	}
	if force {
		if forcer, ok := provider.(EnvironmentForcer); ok {
			forcer.ForceRefresh(ctx)
		}
	}
	snapshot, err := provider.Snapshot(ctx)
	if err != nil {
		return domain.EnvironmentInfo{}, err
	}
	devices := snapshot.Devices
	if devices == nil {
		devices = []domain.DeviceInfo{}
	}
	devices, err = s.annotateDevices(ctx, devices)
	if err != nil {
		return domain.EnvironmentInfo{}, err
	}
	platform := snapshot.Platform
	if platform == "" {
		platform = runtime.GOOS
	}
	checks := snapshot.Checks
	if checks == nil {
		checks = []domain.EnvironmentCheck{}
	}
	return domain.EnvironmentInfo{
		Platform: platform,
		WSL:      snapshot.WSL,
		Devices:  devices,
		Checks:   checks,
	}, nil
}
