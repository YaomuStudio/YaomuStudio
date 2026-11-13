package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/system"
)

func testEnvironmentSnapshot() system.EnvironmentSnapshot {
	return system.EnvironmentSnapshot{
		Platform: "windows",
		WSL:      &system.WSLStatus{Available: true, Version: 2},
		GPUs: []system.GPUInfo{{
			Name:             "NVIDIA GeForce RTX 3060",
			DriverVersion:    "555.99",
			MemoryTotalBytes: 12 * 1024 * 1024 * 1024,
		}},
		Checks: []system.Check{{
			Code:       "CUDA_AVAILABLE",
			Severity:   system.CheckPass,
			MessageKey: "environment.cuda_ready",
			ActionIDs:  []string{},
		}},
	}
}

func TestEnvironmentDevicesListDoesNotProbe(t *testing.T) {
	calls := 0
	devices := NewEnvironmentDevices(func(context.Context) (system.EnvironmentSnapshot, error) {
		calls++
		return testEnvironmentSnapshot(), nil
	})

	list, err := devices.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if calls != 0 {
		t.Fatalf("List triggered %d probes, want 0", calls)
	}
	for _, device := range list {
		if device.Kind == domain.DeviceKindCUDA {
			t.Fatalf("unexpected CUDA before refresh: %+v", list)
		}
	}

	snapshot, err := devices.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if calls != 1 {
		t.Fatalf("probes = %d, want 1", calls)
	}
	if snapshot.Platform != "windows" || snapshot.WSL == nil || !snapshot.WSL.Available {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if len(snapshot.Devices) == 0 || snapshot.Devices[len(snapshot.Devices)-1].Kind != domain.DeviceKindCUDA {
		t.Fatalf("devices = %+v, want CUDA appended", snapshot.Devices)
	}
	if len(snapshot.Checks) != 1 || snapshot.Checks[0].Code != "CUDA_AVAILABLE" {
		t.Fatalf("checks = %+v", snapshot.Checks)
	}
}

func TestEnvironmentDevicesCachesWithinTTL(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	calls := 0
	devices := NewEnvironmentDevices(func(context.Context) (system.EnvironmentSnapshot, error) {
		calls++
		return testEnvironmentSnapshot(), nil
	})
	devices.now = func() time.Time { return now }
	devices.ttl = time.Minute

	devices.Refresh(context.Background())
	devices.Refresh(context.Background())
	if calls != 1 {
		t.Fatalf("probes = %d, want cached 1", calls)
	}

	now = now.Add(2 * time.Minute)
	devices.Refresh(context.Background())
	if calls != 2 {
		t.Fatalf("probes = %d, want refresh after TTL", calls)
	}
}

func TestEnvironmentDevicesSnapshotUsesTTL(t *testing.T) {
	calls := 0
	devices := NewEnvironmentDevices(func(context.Context) (system.EnvironmentSnapshot, error) {
		calls++
		return testEnvironmentSnapshot(), nil
	})
	if _, err := devices.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := devices.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("probes = %d, want cached Snapshot", calls)
	}
}

func TestEnvironmentDevicesForceRefreshBypassesTTL(t *testing.T) {
	calls := 0
	devices := NewEnvironmentDevices(func(context.Context) (system.EnvironmentSnapshot, error) {
		calls++
		return testEnvironmentSnapshot(), nil
	})
	if _, err := devices.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	devices.ForceRefresh(context.Background())
	if calls != 2 {
		t.Fatalf("probes = %d, want forced second probe", calls)
	}
	if _, err := devices.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("probes = %d, want TTL reuse after force", calls)
	}
}

func TestEnvironmentDevicesKeepsSnapshotOnProbeError(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	calls := 0
	devices := NewEnvironmentDevices(func(context.Context) (system.EnvironmentSnapshot, error) {
		calls++
		if calls == 1 {
			return testEnvironmentSnapshot(), nil
		}
		return system.EnvironmentSnapshot{}, errors.New("probe failed")
	})
	devices.now = func() time.Time { return now }
	devices.ttl = time.Minute

	if _, err := devices.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	snapshot, err := devices.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	foundCUDA := false
	for _, device := range snapshot.Devices {
		if device.Kind == domain.DeviceKindCUDA {
			foundCUDA = true
		}
	}
	if !foundCUDA {
		t.Fatalf("devices = %+v, want previous CUDA snapshot retained", snapshot.Devices)
	}
	foundFailed := false
	for _, check := range snapshot.Checks {
		if check.Code == "ENVIRONMENT_PROBE_FAILED" {
			foundFailed = true
		}
	}
	if !foundFailed {
		t.Fatalf("checks = %+v, want ENVIRONMENT_PROBE_FAILED", snapshot.Checks)
	}
}

func TestEnvironmentDevicesProbeErrorWithoutSnapshot(t *testing.T) {
	devices := NewEnvironmentDevices(func(context.Context) (system.EnvironmentSnapshot, error) {
		return system.EnvironmentSnapshot{}, errors.New("probe failed")
	})
	snapshot, err := devices.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot.Devices) == 0 {
		t.Fatal("expected CPU fallback devices")
	}
	foundFailed := false
	for _, check := range snapshot.Checks {
		if check.Code == "ENVIRONMENT_PROBE_FAILED" {
			foundFailed = true
		}
	}
	if !foundFailed {
		t.Fatalf("checks = %+v, want ENVIRONMENT_PROBE_FAILED", snapshot.Checks)
	}
}

func TestMergeDeviceMetrics(t *testing.T) {
	base := []domain.DeviceInfo{
		{ID: "cpu", Name: "CPU", Kind: domain.DeviceKindCPU},
		{ID: "mps", Name: "Apple GPU (MPS)", Kind: domain.DeviceKindMPS, Recommended: true},
		{
			ID:               "cuda:0",
			Name:             "NVIDIA GeForce RTX 3060",
			Kind:             domain.DeviceKindCUDA,
			DriverVersion:    "555.99",
			MemoryTotalBytes: ptrInt64(12 * 1024 * 1024 * 1024),
			MemoryUsedBytes:  ptrInt64(2 * 1024 * 1024 * 1024),
		},
	}
	host := system.HostInfo{
		CPUName: "Apple M1 Max",
		Memory: system.MemoryInfo{
			TotalBytes: 32 * 1024 * 1024 * 1024,
			UsedBytes:  12 * 1024 * 1024 * 1024,
			HasTotal:   true,
			HasUsed:    true,
		},
	}

	devices := mergeDeviceMetrics(base, host)
	if devices[0].Name != "Apple M1 Max" {
		t.Fatalf("cpu name = %q", devices[0].Name)
	}
	if devices[1].Name != "Apple M1 Max GPU" {
		t.Fatalf("mps name = %q", devices[1].Name)
	}
	for _, index := range []int{0, 1} {
		device := devices[index]
		if device.MemoryTotalBytes == nil || *device.MemoryTotalBytes != host.Memory.TotalBytes {
			t.Fatalf("device[%d] total = %v", index, device.MemoryTotalBytes)
		}
		if device.MemoryUsedBytes == nil || *device.MemoryUsedBytes != host.Memory.UsedBytes {
			t.Fatalf("device[%d] used = %v", index, device.MemoryUsedBytes)
		}
	}
	if devices[2].DriverVersion != "555.99" || devices[2].Name != "NVIDIA GeForce RTX 3060" {
		t.Fatalf("cuda device overwritten: %+v", devices[2])
	}
}

func TestMergeDeviceMetricsOmitsUnavailable(t *testing.T) {
	base := []domain.DeviceInfo{
		{ID: "cpu", Name: "CPU", Kind: domain.DeviceKindCPU},
		{ID: "mps", Name: "Apple GPU (MPS)", Kind: domain.DeviceKindMPS},
	}
	devices := mergeDeviceMetrics(base, system.HostInfo{})
	if devices[0].Name != "CPU" || devices[1].Name != "Apple GPU (MPS)" {
		t.Fatalf("names = %q / %q", devices[0].Name, devices[1].Name)
	}
	for _, device := range devices {
		if device.MemoryTotalBytes != nil || device.MemoryUsedBytes != nil {
			t.Fatalf("device = %+v, want omitted memory", device)
		}
	}
}

func TestMergeDeviceMetricsTotalOnly(t *testing.T) {
	base := []domain.DeviceInfo{{ID: "cpu", Name: "CPU", Kind: domain.DeviceKindCPU}}
	devices := mergeDeviceMetrics(base, system.HostInfo{
		Memory: system.MemoryInfo{TotalBytes: 8 * 1024 * 1024 * 1024, HasTotal: true},
	})
	if devices[0].MemoryTotalBytes == nil || *devices[0].MemoryTotalBytes != 8*1024*1024*1024 {
		t.Fatalf("total = %v", devices[0].MemoryTotalBytes)
	}
	if devices[0].MemoryUsedBytes != nil {
		t.Fatalf("used = %v, want omitted", devices[0].MemoryUsedBytes)
	}
}

func ptrInt64(value int64) *int64 { return &value }
