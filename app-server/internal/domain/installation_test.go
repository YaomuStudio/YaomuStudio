package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReleaseStorageKeyCanonicalIdentity(t *testing.T) {
	base := ReleaseRef{ReleaseID: "rel-1", Version: "1.0.0", Digest: "sha256:abc"}
	key, err := ReleaseStorageKey(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 64 || strings.ToLower(key) != key {
		t.Fatalf("storage key = %q, want lowercase sha256 hex", key)
	}
	same, err := ReleaseStorageKey(base)
	if err != nil || same != key {
		t.Fatalf("stable key mismatch: %q vs %q (%v)", key, same, err)
	}
	for name, ref := range map[string]ReleaseRef{
		"digest":  {ReleaseID: "rel-1", Version: "1.0.0", Digest: "sha256:def"},
		"version": {ReleaseID: "rel-1", Version: "2.0.0", Digest: "sha256:abc"},
		"channel": {ReleaseID: "rel-1", Version: "1.0.0", Digest: "sha256:abc", Channel: "beta"},
	} {
		other, err := ReleaseStorageKey(ref)
		if err != nil {
			t.Fatal(err)
		}
		if other == key {
			t.Fatalf("%s change did not isolate storage key", name)
		}
	}
	if _, err := ReleaseStorageKey(ReleaseRef{ReleaseID: "rel-1", Digest: "sha256:abc"}); err == nil {
		t.Fatal("incomplete release accepted for storage key")
	}
}

func TestNormalizeCapabilitySlots(t *testing.T) {
	slots, err := NormalizeCapabilitySlots([]CapabilitySlotKey{
		CapabilitySlotVoiceClone, CapabilitySlotFFmpeg, CapabilitySlotTTS, CapabilitySlotTTS,
	})
	if err == nil {
		t.Fatalf("duplicate slots accepted: %v", slots)
	}
	slots, err = NormalizeCapabilitySlots([]CapabilitySlotKey{
		CapabilitySlotVoiceClone, CapabilitySlotFFmpeg, CapabilitySlotTTS,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []CapabilitySlotKey{CapabilitySlotTTS, CapabilitySlotVoiceClone, CapabilitySlotFFmpeg}
	if len(slots) != len(want) {
		t.Fatalf("normalized slots = %v", slots)
	}
	for i := range want {
		if slots[i] != want[i] {
			t.Fatalf("canonical order = %v, want %v", slots, want)
		}
	}
	if _, err := NormalizeCapabilitySlots([]CapabilitySlotKey{"bogus"}); err == nil {
		t.Fatal("unknown slot accepted")
	}
}

func TestInstallationJSONAlwaysProjectsArrays(t *testing.T) {
	inst := Installation{ID: "inst-1", Slot: CapabilitySlotTTS, Phase: InstallationPhaseDone}
	raw, err := json.Marshal(inst)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"covered_slots", "bound_slots", "current_slots"} {
		value, ok := decoded[field]
		if !ok {
			t.Fatalf("installation JSON missing %s: %s", field, raw)
		}
		if _, ok := value.([]any); !ok {
			t.Fatalf("%s = %v, want array (not null)", field, value)
		}
	}

	shared := Installation{
		ID: "inst-2", Slot: CapabilitySlotTTS,
		CoveredSlots: []CapabilitySlotKey{CapabilitySlotTTS, CapabilitySlotVoiceClone},
		BoundSlots:   []CapabilitySlotKey{CapabilitySlotVoiceClone},
		CurrentSlots: []CapabilitySlotKey{},
	}
	raw, err = json.Marshal(shared)
	if err != nil {
		t.Fatal(err)
	}
	var projected struct {
		Covered []CapabilitySlotKey `json:"covered_slots"`
		Bound   []CapabilitySlotKey `json:"bound_slots"`
		Current []CapabilitySlotKey `json:"current_slots"`
	}
	if err := json.Unmarshal(raw, &projected); err != nil {
		t.Fatal(err)
	}
	if len(projected.Covered) != 2 || len(projected.Bound) != 1 || len(projected.Current) != 0 {
		t.Fatalf("projections = %+v", projected)
	}
}

func TestInstallationSlotProjectionFallback(t *testing.T) {
	legacy := Installation{Slot: CapabilitySlotTTS, Phase: InstallationPhaseDone}
	if !InstallationBoundSlot(legacy, CapabilitySlotTTS) || !InstallationCurrentSlot(legacy, CapabilitySlotTTS) {
		t.Fatal("unprojected done fixture must keep singleton semantics")
	}
	// 真实投影的空数组不回退发起槽位。
	detached := Installation{
		Slot: CapabilitySlotTTS, Phase: InstallationPhaseDone,
		CoveredSlots: []CapabilitySlotKey{CapabilitySlotTTS, CapabilitySlotVoiceClone},
		BoundSlots:   []CapabilitySlotKey{}, CurrentSlots: []CapabilitySlotKey{},
	}
	if InstallationBoundSlot(detached, CapabilitySlotTTS) || InstallationCurrentSlot(detached, CapabilitySlotTTS) {
		t.Fatal("empty binding arrays must not revive the initiating slot")
	}
	if got := EffectiveCoveredSlots(detached); len(got) != 2 {
		t.Fatalf("effective coverage = %v", got)
	}
}
