package domain

import (
	"errors"
	"testing"
)

func TestValidateRunConfigExecutionLocks(t *testing.T) {
	valid := ExecutionConfig{
		ModelLock: ReleaseRef{ReleaseID: "release", Version: "1", Digest: "sha256:release"},
		DeviceID:  "cpu",
	}
	tests := []struct {
		name string
		slot CapabilitySlotKey
		cfg  ExecutionConfig
	}{
		{name: "unknown slot", slot: "unknown", cfg: valid},
		{name: "missing release", slot: CapabilitySlotASR, cfg: ExecutionConfig{DeviceID: "cpu"}},
		{name: "automatic device", slot: CapabilitySlotASR, cfg: ExecutionConfig{ModelLock: valid.ModelLock, DeviceID: "auto"}},
		{name: "negative context", slot: CapabilitySlotASR, cfg: ExecutionConfig{ModelLock: valid.ModelLock, DeviceID: "cpu", ContextRadius: -1}},
		{name: "ffmpeg empty lock automatic device", slot: CapabilitySlotFFmpeg, cfg: ExecutionConfig{DeviceID: "auto"}},
		{name: "ffmpeg partial lock", slot: CapabilitySlotFFmpeg, cfg: ExecutionConfig{ModelLock: ReleaseRef{ReleaseID: "release"}, DeviceID: "cpu"}},
		{name: "ffmpeg missing digest", slot: CapabilitySlotFFmpeg, cfg: ExecutionConfig{ModelLock: ReleaseRef{ReleaseID: "release", Version: "1"}, DeviceID: "cpu"}},
		{name: "ffmpeg missing version", slot: CapabilitySlotFFmpeg, cfg: ExecutionConfig{ModelLock: ReleaseRef{ReleaseID: "release", Digest: "sha256:release"}, DeviceID: "cpu"}},
		{name: "ffmpeg missing release id", slot: CapabilitySlotFFmpeg, cfg: ExecutionConfig{ModelLock: ReleaseRef{Version: "1", Digest: "sha256:release"}, DeviceID: "cpu"}},
		{name: "ffmpeg lock with only channel", slot: CapabilitySlotFFmpeg, cfg: ExecutionConfig{ModelLock: ReleaseRef{Channel: "beta"}, DeviceID: "cpu"}},
		{name: "ffmpeg non-cpu device", slot: CapabilitySlotFFmpeg, cfg: ExecutionConfig{DeviceID: "mps"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRunConfig(RunConfig{
				Execution: map[CapabilitySlotKey]ExecutionConfig{test.slot: test.cfg},
			})
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
	if err := ValidateRunConfig(RunConfig{
		Execution: map[CapabilitySlotKey]ExecutionConfig{CapabilitySlotASR: valid},
	}); err != nil {
		t.Fatalf("valid execution config rejected: %v", err)
	}
	// FFmpeg is a media tool: production Runs carry an empty model lock with cpu.
	if err := ValidateRunConfig(RunConfig{
		Execution: map[CapabilitySlotKey]ExecutionConfig{
			CapabilitySlotFFmpeg: {DeviceID: "cpu", Params: map[string]any{}},
		},
	}); err != nil {
		t.Fatalf("ffmpeg empty lock rejected: %v", err)
	}
	// Legacy valid FFmpeg locks (fixtures/old Runs) remain readable.
	if err := ValidateRunConfig(RunConfig{
		Execution: map[CapabilitySlotKey]ExecutionConfig{CapabilitySlotFFmpeg: valid},
	}); err != nil {
		t.Fatalf("legacy ffmpeg lock rejected: %v", err)
	}
	// A complete lock may carry a channel; channel alone is not an empty lock.
	if err := ValidateRunConfig(RunConfig{
		Execution: map[CapabilitySlotKey]ExecutionConfig{
			CapabilitySlotFFmpeg: {
				ModelLock: ReleaseRef{
					ReleaseID: "release", Version: "1", Digest: "sha256:release", Channel: "beta",
				},
				DeviceID: "cpu",
			},
		},
	}); err != nil {
		t.Fatalf("ffmpeg complete lock with channel rejected: %v", err)
	}
	// Legacy Runs predate the execution column and remain readable.
	if err := ValidateRunConfig(RunConfig{}); err != nil {
		t.Fatalf("legacy nil execution config rejected: %v", err)
	}
}
