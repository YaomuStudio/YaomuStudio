package service

import (
	"context"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func TestParseMockModelSlots(t *testing.T) {
	got := ParseMockModelSlots("separation, voice-clone, bogus, separation")
	if len(got) != 2 || got[0] != domain.CapabilitySlotSeparation || got[1] != domain.CapabilitySlotVoiceClone {
		t.Fatalf("got %#v", got)
	}
}

func TestMockModelCapabilitiesOverlaysNeedsSetup(t *testing.T) {
	provider := MockModelCapabilities{
		Inner: NewInstallationCapabilities(),
		Slots: []domain.CapabilitySlotKey{domain.CapabilitySlotSeparation},
	}
	caps, err := provider.Catalog(context.Background(), emptyInstallQuerier{})
	if err != nil {
		t.Fatal(err)
	}
	for _, cap := range caps {
		if cap.Slot != domain.CapabilitySlotSeparation {
			continue
		}
		if cap.State != domain.SlotStateReady || cap.EngineName != "runner-mock" {
			t.Fatalf("cap = %+v", cap)
		}
		return
	}
	t.Fatal("separation missing")
}
