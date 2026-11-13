package service

import (
	"context"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// TestSaveTranslationRevisionRecordsSourceInput 验证 Phase B 源轨过期契约：
// 人工保存译文轨时记录当前源 transcription revision；源轨后续变化不篡改旧译文，
// 再次保存译文时才更新到新的源 revision。
func TestSaveTranslationRevisionRecordsSourceInput(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("源轨过期契约")

	source := env.seedTrackWithType(
		project.ID,
		"en",
		domain.TrackTypeTranscription,
		[]domain.Segment{{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello"}},
		"",
	)
	translation := env.seedTrackWithType(
		project.ID,
		"zh",
		domain.TrackTypeTranslation,
		[]domain.Segment{{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"}},
		"",
	)
	env.seedRun(project.ID)

	firstResp, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, translation.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: translation.CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好呀"},
		},
	})
	if err != nil {
		t.Fatalf("save translation: %v", err)
	}
	first := firstResp.Body.(domain.SubtitleRevision)
	if first.InputRevisionID != source.CurrentRevisionID {
		t.Fatalf("translation input = %q, want source %q", first.InputRevisionID, source.CurrentRevisionID)
	}

	sourceResp, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, source.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: source.CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "src-1", StartMs: 0, EndMs: 900, Text: "Hello there"},
		},
	})
	if err != nil {
		t.Fatalf("save source: %v", err)
	}
	newSourceRev := sourceResp.Body.(domain.SubtitleRevision)
	if newSourceRev.ID == source.CurrentRevisionID {
		t.Fatal("source revision did not change")
	}

	// 旧译文仍绑定旧源 revision，不因源轨保存而被静默改写。
	storedTranslation, err := env.store.GetSubtitleTrack(ctx, translation.ID)
	if err != nil {
		t.Fatalf("reload translation track: %v", err)
	}
	if storedTranslation.CurrentRevisionID != first.ID {
		t.Fatalf("translation current = %q, want %q", storedTranslation.CurrentRevisionID, first.ID)
	}
	for _, rev := range storedTranslation.Revisions {
		if rev.ID == first.ID && rev.InputRevisionID != source.CurrentRevisionID {
			t.Fatalf("stored translation input = %q, want old source %q",
				rev.InputRevisionID, source.CurrentRevisionID)
		}
	}

	// 再次保存译文：input 更新到新的源 revision。
	secondResp, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, translation.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: storedTranslation.CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好呀，世界"},
		},
	})
	if err != nil {
		t.Fatalf("re-save translation: %v", err)
	}
	second := secondResp.Body.(domain.SubtitleRevision)
	if second.InputRevisionID != newSourceRev.ID {
		t.Fatalf("translation input after source change = %q, want %q",
			second.InputRevisionID, newSourceRev.ID)
	}
}

// TestSaveTranslationWithoutSourceLeavesInputUnknown 覆盖无源轨/多义时的空输入。
func TestSaveTranslationWithoutSourceLeavesInputUnknown(t *testing.T) {
	env := newExportTestEnv(t)
	ctx := context.Background()
	project := env.seedProject("无源轨")
	translation := env.seedTrackWithType(
		project.ID,
		"zh",
		domain.TrackTypeTranslation,
		[]domain.Segment{{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好"}},
		"",
	)

	resp, err := env.svc.SaveSubtitleRevision(ctx, Idem{}, translation.ID, SaveSubtitleRevisionRequest{
		BaseRevisionID: translation.CurrentRevisionID,
		Segments: []domain.SegmentInput{
			{ID: "tr-1", StartMs: 0, EndMs: 900, Text: "你好啊"},
		},
	})
	if err != nil {
		t.Fatalf("save translation: %v", err)
	}
	saved := resp.Body.(domain.SubtitleRevision)
	if saved.InputRevisionID != "" {
		t.Fatalf("translation input = %q, want empty", saved.InputRevisionID)
	}
}
