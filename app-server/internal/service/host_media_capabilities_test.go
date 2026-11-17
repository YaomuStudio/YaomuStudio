package service

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

type emptyInstallQuerier struct{}

func (emptyInstallQuerier) ListInstallations(context.Context) ([]domain.Installation, error) {
	return nil, nil
}

func TestHostMediaCapabilitiesFFmpegReady(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffmpeg")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	mode := os.FileMode(0o755)
	if runtime.GOOS == "windows" {
		mode = 0o666
	}
	if err := os.WriteFile(bin, []byte("ffmpeg"), mode); err != nil {
		t.Fatal(err)
	}

	provider := HostMediaCapabilities{
		Inner:      NewInstallationCapabilities(),
		FFmpegPath: func() string { return bin },
		Platform:   runtime.GOOS,
	}
	caps, err := provider.Catalog(context.Background(), emptyInstallQuerier{})
	if err != nil {
		t.Fatal(err)
	}
	var ffmpeg *domain.Capability
	for i := range caps {
		if caps[i].Slot == domain.CapabilitySlotFFmpeg {
			ffmpeg = &caps[i]
			break
		}
	}
	if ffmpeg == nil {
		t.Fatal("ffmpeg slot missing")
	}
	if ffmpeg.State != domain.SlotStateReady {
		t.Fatalf("state = %s, want READY", ffmpeg.State)
	}
	if ffmpeg.EngineName != "bundled-ffmpeg" {
		t.Fatalf("engine = %q", ffmpeg.EngineName)
	}
}

func TestHostMediaCapabilitiesFFmpegIgnoresImageWithoutBinary(t *testing.T) {
	provider := HostMediaCapabilities{
		Inner:      NewInstallationCapabilities(),
		FFmpegPath: func() string { return "" },
	}
	caps, err := provider.Catalog(context.Background(), emptyInstallQuerier{})
	if err != nil {
		t.Fatal(err)
	}
	for _, cap := range caps {
		if cap.Slot != domain.CapabilitySlotFFmpeg {
			continue
		}
		if cap.State != domain.SlotStateNeedsSetup {
			t.Fatalf("state = %s, want NEEDS_SETUP", cap.State)
		}
		return
	}
	t.Fatal("ffmpeg slot missing")
}

func TestHostMediaCapabilitiesFFmpegNeedsSetup(t *testing.T) {
	provider := HostMediaCapabilities{
		Inner:      NewInstallationCapabilities(),
		FFmpegPath: func() string { return "" },
	}
	caps, err := provider.Catalog(context.Background(), emptyInstallQuerier{})
	if err != nil {
		t.Fatal(err)
	}
	for _, cap := range caps {
		if cap.Slot != domain.CapabilitySlotFFmpeg {
			continue
		}
		if cap.State != domain.SlotStateNeedsSetup {
			t.Fatalf("state = %s, want NEEDS_SETUP", cap.State)
		}
		return
	}
	t.Fatal("ffmpeg slot missing")
}
