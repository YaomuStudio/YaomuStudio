package releasecatalog

import (
	"context"
	"strings"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/downloader"
)

func TestLoadEmbedded(t *testing.T) {
	cat, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	if len(cat.Prefixes) == 0 || !strings.HasPrefix(cat.Prefixes[0], "https://flashhub-cdn.aodianyun.com") {
		t.Fatalf("unexpected prefixes: %#v", cat.Prefixes)
	}

	tts, err := cat.Resolve(context.Background(), domain.CapabilitySlotTTS)
	if err != nil {
		t.Fatalf("tts resolve: %v", err)
	}
	clone, err := cat.Resolve(context.Background(), domain.CapabilitySlotVoiceClone)
	if err != nil {
		t.Fatalf("voice-clone resolve: %v", err)
	}
	if tts.Release.ReleaseID != clone.Release.ReleaseID || tts.Release.Digest != clone.Release.Digest {
		t.Fatalf("tts/voice-clone should share release: %#v vs %#v", tts.Release, clone.Release)
	}
	if len(tts.Files) == 0 {
		t.Fatal("tts files empty")
	}
	if tts.FlashCLIRef != "tts/omnivoice_flashrt:1.0.3" || clone.FlashCLIRef != tts.FlashCLIRef {
		t.Fatalf("tts flashcli ref = %q, clone = %q", tts.FlashCLIRef, clone.FlashCLIRef)
	}
	for _, f := range tts.Files {
		if err := downloader.ValidateFile(f); err != nil {
			t.Fatalf("tts file %s: %v", f.RelPath, err)
		}
	}

	sep, err := cat.Resolve(context.Background(), domain.CapabilitySlotSeparation)
	if err != nil {
		t.Fatalf("separation resolve: %v", err)
	}
	if sep.Release.ReleaseID == "" || len(sep.Files) == 0 || sep.FlashCLIRef != "xym/melband-roformer:1.0.10" {
		t.Fatalf("separation plan incomplete: %#v", sep)
	}

	asr, err := cat.Resolve(context.Background(), domain.CapabilitySlotASR)
	if err != nil {
		t.Fatalf("asr resolve: %v", err)
	}
	if asr.Release.ReleaseID != "fh-asr-qwen3_asr-1.0.1" || len(asr.Files) == 0 || asr.FlashCLIRef != "asr/qwen3_asr:1.0.1" {
		t.Fatalf("asr plan incomplete: %#v", asr)
	}
	for _, f := range asr.Files {
		if err := downloader.ValidateFile(f); err != nil {
			t.Fatalf("asr file %s: %v", f.RelPath, err)
		}
	}
}

func TestListReturnsAdmittedEngine(t *testing.T) {
	cat, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	tts, err := cat.List(context.Background(), domain.CapabilitySlotTTS)
	if err != nil {
		t.Fatalf("list tts: %v", err)
	}
	if len(tts) != 1 || tts[0].Release.ReleaseID == "" {
		t.Fatalf("tts engines = %#v", tts)
	}
	translate, err := cat.List(context.Background(), domain.CapabilitySlotTranslate)
	if err != nil {
		t.Fatalf("list translate: %v", err)
	}
	if len(translate) != 0 {
		t.Fatalf("translate engines = %#v, want empty", translate)
	}
}

func TestParseRejectsBadSlot(t *testing.T) {
	raw := []byte(`{
  "schema_version": 1,
  "kind": "yaomu_product_release_catalog",
  "distribution_prefixes": ["https://flashhub-cdn.aodianyun.com/"],
  "releases": [{
    "slots": ["not-a-slot"],
    "release": {"release_id":"x","version":"1","digest":"sha256:abc"},
    "files": [{"rel_path":"a.bin","url":"https://flashhub-cdn.aodianyun.com/a.bin","sha256":"` + strings.Repeat("a", 64) + `","size":1}]
  }]
}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected error for unknown slot")
	}
}

func catalogDoc(releases string) []byte {
	return []byte(`{
  "schema_version": 1,
  "kind": "yaomu_product_release_catalog",
  "distribution_prefixes": ["https://flashhub-cdn.aodianyun.com/"],
  "releases": [` + releases + `]
}`)
}

func releaseDocJSON(slots string, releaseID, version, digest string) string {
	return `{
    "slots": [` + slots + `],
    "release": {"release_id":"` + releaseID + `","version":"` + version + `","digest":"` + digest + `"},
    "download_bytes": 1,
    "files": [{"rel_path":"a.bin","url":"https://flashhub-cdn.aodianyun.com/` + releaseID + `/a.bin","sha256":"` + strings.Repeat("a", 64) + `","size":1}]
  }`
}

// TestEmbeddedCatalogDeclaresFullCoverage 覆盖内嵌目录保留 TTS/voice-clone
// 的完整声明并冻结进解析后的计划。
func TestEmbeddedCatalogDeclaresFullCoverage(t *testing.T) {
	cat, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	tts, err := cat.Resolve(context.Background(), domain.CapabilitySlotTTS)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := cat.Resolve(context.Background(), domain.CapabilitySlotVoiceClone)
	if err != nil {
		t.Fatal(err)
	}
	if len(tts.CoveredSlots) != 2 || tts.CoveredSlots[0] != domain.CapabilitySlotTTS ||
		tts.CoveredSlots[1] != domain.CapabilitySlotVoiceClone {
		t.Fatalf("tts covered slots = %v", tts.CoveredSlots)
	}
	if len(clone.CoveredSlots) != len(tts.CoveredSlots) {
		t.Fatalf("clone covered slots = %v", clone.CoveredSlots)
	}
	sep, err := cat.Resolve(context.Background(), domain.CapabilitySlotSeparation)
	if err != nil {
		t.Fatal(err)
	}
	if len(sep.CoveredSlots) != 1 || sep.CoveredSlots[0] != domain.CapabilitySlotSeparation {
		t.Fatalf("separation covered slots = %v", sep.CoveredSlots)
	}
}

func TestParseRejectsDuplicateCoverageSlot(t *testing.T) {
	raw := catalogDoc(releaseDocJSON(`"tts","tts"`, "rel-dup", "1", "sha256:dup"))
	if _, err := Parse(raw); err == nil {
		t.Fatal("duplicate coverage slot accepted")
	}
}

func TestParseRejectsConflictingSameReleaseDeclaration(t *testing.T) {
	raw := catalogDoc(
		releaseDocJSON(`"tts"`, "rel-same", "1", "sha256:same") + "," +
			releaseDocJSON(`"voice-clone"`, "rel-same", "1", "sha256:same"))
	if _, err := Parse(raw); err == nil {
		t.Fatal("same exact release with conflicting coverage accepted")
	}
}
