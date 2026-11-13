package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func seedTrackWithRevision(t *testing.T, s *Store, projectID string) (domain.SubtitleTrack, domain.SubtitleRevision) {
	t.Helper()
	initial := domain.SubtitleRevision{
		ID:         "rev-1",
		TrackID:    "track-1",
		AuthorType: domain.AuthorTypeSystem,
		Segments: []domain.Segment{
			{ID: "seg-1", StartMs: 0, EndMs: 1000, Text: "hello"},
			{ID: "seg-2", StartMs: 1000, EndMs: 2000, Text: "world"},
		},
		CreatedAt: testTime,
	}
	track := domain.SubtitleTrack{
		ID:                "track-1",
		ProjectID:         projectID,
		Language:          "en",
		Type:              domain.TrackTypeTranscription,
		CurrentRevisionID: initial.ID,
		Revisions:         []domain.SubtitleRevision{initial},
		CreatedAt:         testTime,
		UpdatedAt:         testTime,
	}
	if err := s.CreateSubtitleTrack(context.Background(), track); err != nil {
		t.Fatalf("CreateSubtitleTrack() error = %v", err)
	}
	return track, initial
}

func TestCreateTrackNormalizesSegments(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedTrackWithRevision(t, s, project.ID)

	got, err := s.GetSubtitleTrack(ctx, "track-1")
	if err != nil {
		t.Fatalf("GetSubtitleTrack() error = %v", err)
	}
	if got.CurrentRevisionID != "rev-1" || len(got.Revisions) != 1 {
		t.Fatalf("track = %+v", got)
	}
	segments := got.Revisions[0].Segments
	if len(segments) != 2 {
		t.Fatalf("segments count = %d, want 2", len(segments))
	}
	for _, seg := range segments {
		if seg.ContentRevision != 1 {
			t.Errorf("segment %s content_revision = %d, want 1", seg.ID, seg.ContentRevision)
		}
		if seg.ContentDigest != domain.SegmentContentDigest(seg.Text, seg.Speaker) {
			t.Errorf("segment %s digest not computed", seg.ID)
		}
	}
	if segments[0].ContentDigest == segments[1].ContentDigest {
		t.Error("different segment text produced identical digest")
	}
}

func TestSaveSubtitleRevisionOptimisticConcurrency(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedTrackWithRevision(t, s, project.ID)

	save := RevisionSave{
		AuthorType: domain.AuthorTypeUser,
		Segments: []domain.SegmentInput{
			{ID: "seg-1", StartMs: 0, EndMs: 1000, Text: "hello there"},
			{ID: "seg-2", StartMs: 1000, EndMs: 2000, Text: "world"},
			{StartMs: 2000, EndMs: 3000, Text: "new line", Lineage: []domain.ID{"seg-2"}},
		},
	}
	rev, err := s.SaveSubtitleRevision(ctx, "track-1", "rev-1", save, testTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("SaveSubtitleRevision() error = %v", err)
	}
	if rev.ID == "" || rev.TrackID != "track-1" {
		t.Fatalf("saved revision = %+v", rev)
	}
	if rev.ParentRevisionID != "rev-1" {
		t.Errorf("parent revision = %q, want rev-1", rev.ParentRevisionID)
	}
	if len(rev.Segments) != 3 {
		t.Fatalf("segments count = %d, want 3", len(rev.Segments))
	}
	if rev.Segments[0].ContentRevision != 2 {
		t.Errorf("changed segment content_revision = %d, want 2", rev.Segments[0].ContentRevision)
	}
	if rev.Segments[1].ContentRevision != 1 {
		t.Errorf("unchanged segment content_revision = %d, want 1", rev.Segments[1].ContentRevision)
	}
	newSeg := rev.Segments[2]
	if newSeg.ID == "" || len(newSeg.Lineage) != 1 || newSeg.Lineage[0] != "seg-2" {
		t.Errorf("new segment id/lineage = %q/%v", newSeg.ID, newSeg.Lineage)
	}

	track, err := s.GetSubtitleTrack(ctx, "track-1")
	if err != nil {
		t.Fatalf("GetSubtitleTrack() error = %v", err)
	}
	if track.CurrentRevisionID != rev.ID || len(track.Revisions) != 2 {
		t.Fatalf("track revisions = %+v", track)
	}
	oldRevision := track.Revisions[0]
	if oldRevision.ID != "rev-1" || oldRevision.Segments[0].Text != "hello" {
		t.Error("old revision was mutated; revisions must be immutable")
	}

	// 过期 base revision 必须返回 REVISION_CONFLICT，不能覆盖新版本。
	if _, err := s.SaveSubtitleRevision(ctx, "track-1", "rev-1", save, testTime); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Errorf("stale base error = %v, want ErrRevisionConflict", err)
	}

	// 重叠时间窗在写入前被拒绝。
	overlap := RevisionSave{
		AuthorType: domain.AuthorTypeUser,
		Segments: []domain.SegmentInput{
			{ID: "seg-1", StartMs: 0, EndMs: 1500, Text: "hello there"},
			{ID: "seg-2", StartMs: 1000, EndMs: 2000, Text: "world"},
		},
	}
	if _, err := s.SaveSubtitleRevision(ctx, "track-1", rev.ID, overlap, testTime); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Errorf("overlap error = %v, want ErrInvalidRequest", err)
	}
}

func TestSaveSubtitleRevisionTimeChangeKeepsContentRevision(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedTrackWithRevision(t, s, project.ID)

	// 只移动时间窗、不改文本：句级内容版本保持不变，缓存键由时间窗间接失效。
	save := RevisionSave{
		AuthorType: domain.AuthorTypeUser,
		Segments: []domain.SegmentInput{
			{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "hello"},
			{ID: "seg-2", StartMs: 1000, EndMs: 2000, Text: "world"},
		},
	}
	rev, err := s.SaveSubtitleRevision(ctx, "track-1", "rev-1", save, testTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("SaveSubtitleRevision() error = %v", err)
	}
	if rev.Segments[0].ContentRevision != 1 {
		t.Errorf("content_revision = %d, want 1 after timing-only change", rev.Segments[0].ContentRevision)
	}
	if rev.Segments[0].EndMs != 900 {
		t.Errorf("end_ms = %d, want 900", rev.Segments[0].EndMs)
	}
}

func TestSaveSubtitleRevisionSplitKeepsLineage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedTrackWithRevision(t, s, project.ID)

	// 拆分 seg-1：一行变两行，两个新 ID 都指向原句段；seg-2 保持不变。
	rev, err := s.SaveSubtitleRevision(ctx, "track-1", "rev-1", RevisionSave{
		AuthorType: domain.AuthorTypeUser,
		Segments: []domain.SegmentInput{
			{StartMs: 0, EndMs: 500, Text: "hello", Lineage: []domain.ID{"seg-1"}},
			{StartMs: 500, EndMs: 1000, Text: "again", Lineage: []domain.ID{"seg-1"}},
			{ID: "seg-2", StartMs: 1000, EndMs: 2000, Text: "world"},
		},
	}, testTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("SaveSubtitleRevision() error = %v", err)
	}
	if rev.ParentRevisionID != "rev-1" {
		t.Errorf("parent revision = %q, want rev-1", rev.ParentRevisionID)
	}
	if len(rev.Segments) != 3 {
		t.Fatalf("segments count = %d, want 3", len(rev.Segments))
	}
	left, right := rev.Segments[0], rev.Segments[1]
	if left.ID == "" || right.ID == "" || left.ID == right.ID {
		t.Fatalf("split ids = %q/%q, want two distinct new ids", left.ID, right.ID)
	}
	for _, seg := range []domain.Segment{left, right} {
		if len(seg.Lineage) != 1 || seg.Lineage[0] != "seg-1" {
			t.Errorf("segment %s lineage = %v, want [seg-1]", seg.ID, seg.Lineage)
		}
		if seg.ContentRevision != 1 {
			t.Errorf("new segment %s content_revision = %d, want 1", seg.ID, seg.ContentRevision)
		}
	}
	if rev.Segments[2].ID != "seg-2" || rev.Segments[2].ContentRevision != 1 {
		t.Errorf("untouched segment = %+v", rev.Segments[2])
	}

	// 读回新 revision 的 lineage；旧 revision 保持不可变。
	track, err := s.GetSubtitleTrack(ctx, "track-1")
	if err != nil {
		t.Fatalf("GetSubtitleTrack() error = %v", err)
	}
	if track.CurrentRevisionID != rev.ID {
		t.Errorf("current revision = %q, want %q", track.CurrentRevisionID, rev.ID)
	}
	old := track.Revisions[0]
	if old.ID != "rev-1" || len(old.Segments) != 2 || old.Segments[0].ID != "seg-1" {
		t.Errorf("old revision mutated: %+v", old)
	}
	built := track.Revisions[len(track.Revisions)-1]
	if len(built.Segments[0].Lineage) != 1 || built.Segments[0].Lineage[0] != "seg-1" {
		t.Errorf("lineage did not round-trip: %v", built.Segments[0].Lineage)
	}
}

func TestSaveSubtitleRevisionMergeKeepsLineage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedTrackWithRevision(t, s, project.ID)

	// 合并 seg-1 与 seg-2：两行变一行，新句段记录两个直接来源。
	rev, err := s.SaveSubtitleRevision(ctx, "track-1", "rev-1", RevisionSave{
		AuthorType: domain.AuthorTypeUser,
		Segments: []domain.SegmentInput{
			{StartMs: 0, EndMs: 2000, Text: "hello world", Lineage: []domain.ID{"seg-1", "seg-2"}},
		},
	}, testTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("SaveSubtitleRevision() error = %v", err)
	}
	if len(rev.Segments) != 1 {
		t.Fatalf("segments count = %d, want 1", len(rev.Segments))
	}
	merged := rev.Segments[0]
	if merged.ID == "" || merged.ID == "seg-1" || merged.ID == "seg-2" {
		t.Fatalf("merged segment id = %q, want a new core-assigned id", merged.ID)
	}
	if len(merged.Lineage) != 2 || merged.Lineage[0] != "seg-1" || merged.Lineage[1] != "seg-2" {
		t.Errorf("merged lineage = %v, want [seg-1 seg-2]", merged.Lineage)
	}
	if merged.ContentRevision != 1 {
		t.Errorf("merged content_revision = %d, want 1", merged.ContentRevision)
	}

	track, err := s.GetSubtitleTrack(ctx, "track-1")
	if err != nil {
		t.Fatalf("GetSubtitleTrack() error = %v", err)
	}
	if len(track.Revisions[0].Segments) != 2 {
		t.Error("old revision was mutated by merge")
	}
}

func TestSaveSubtitleRevisionRejectsInvalidLineage(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedTrackWithRevision(t, s, project.ID)

	cases := []struct {
		name    string
		lineage []domain.ID
	}{
		{name: "unknown reference", lineage: []domain.ID{"missing"}},
		{name: "duplicate reference", lineage: []domain.ID{"seg-1", "seg-1"}},
		{name: "empty reference", lineage: []domain.ID{"seg-1", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.SaveSubtitleRevision(ctx, "track-1", "rev-1", RevisionSave{
				AuthorType: domain.AuthorTypeUser,
				Segments: []domain.SegmentInput{
					{StartMs: 0, EndMs: 1000, Text: "new line", Lineage: tc.lineage},
				},
			}, testTime.Add(time.Minute))
			if !errors.Is(err, domain.ErrInvalidRequest) {
				t.Errorf("error = %v, want ErrInvalidRequest", err)
			}
		})
	}

	// 校验失败不得写入新 revision。
	track, err := s.GetSubtitleTrack(ctx, "track-1")
	if err != nil {
		t.Fatalf("GetSubtitleTrack() error = %v", err)
	}
	if track.CurrentRevisionID != "rev-1" || len(track.Revisions) != 1 {
		t.Errorf("invalid lineage wrote a revision: %+v", track)
	}

	// 新句段不带 lineage 仍允许保存（兼容其他保存路径）。
	if _, err := s.SaveSubtitleRevision(ctx, "track-1", "rev-1", RevisionSave{
		AuthorType: domain.AuthorTypeUser,
		Segments: []domain.SegmentInput{
			{StartMs: 0, EndMs: 1000, Text: "new without lineage"},
		},
	}, testTime.Add(time.Minute)); err != nil {
		t.Fatalf("save without lineage error = %v", err)
	}
}

func TestEmptyTrackFirstRevision(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	track := domain.SubtitleTrack{
		ID: "track-empty", ProjectID: project.ID, Language: "ja", Type: domain.TrackTypeTranscription,
		CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := s.CreateSubtitleTrack(ctx, track); err != nil {
		t.Fatalf("CreateSubtitleTrack() error = %v", err)
	}
	got, err := s.GetSubtitleTrack(ctx, "track-empty")
	if err != nil {
		t.Fatalf("GetSubtitleTrack() error = %v", err)
	}
	if got.CurrentRevisionID != "" || len(got.Revisions) != 0 {
		t.Fatalf("empty track = %+v", got)
	}
	rev, err := s.SaveSubtitleRevision(ctx, "track-empty", "", RevisionSave{
		AuthorType: domain.AuthorTypeSystem,
		Segments:   []domain.SegmentInput{{StartMs: 0, EndMs: 500, Text: "first"}},
	}, testTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("SaveSubtitleRevision(initial) error = %v", err)
	}
	if rev.ParentRevisionID != "" || len(rev.Segments) != 1 {
		t.Errorf("initial revision = %+v", rev)
	}
	// 再次以空 base 保存必须冲突。
	if _, err := s.SaveSubtitleRevision(ctx, "track-empty", "", RevisionSave{
		AuthorType: domain.AuthorTypeUser,
		Segments:   []domain.SegmentInput{{StartMs: 0, EndMs: 500, Text: "second"}},
	}, testTime); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Errorf("second empty-base save error = %v, want ErrRevisionConflict", err)
	}
}

func TestCurrentSegmentsAndProjectTrackList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-1")
	seedTrackWithRevision(t, s, project.ID)

	segments, err := s.CurrentSegments(ctx, "track-1")
	if err != nil {
		t.Fatalf("CurrentSegments() error = %v", err)
	}
	if len(segments) != 2 || segments[1].ID != "seg-2" {
		t.Errorf("current segments = %+v", segments)
	}

	tracks, err := s.ListProjectTracks(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListProjectTracks() error = %v", err)
	}
	if len(tracks) != 1 || tracks[0].ID != "track-1" || len(tracks[0].Revisions) != 1 {
		t.Errorf("project tracks = %+v", tracks)
	}
	if _, err := s.CurrentSegments(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing track error = %v, want ErrNotFound", err)
	}
}

func TestSaveSubtitleRevisionPersistsStyleAndEraseBlocks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-style")
	seedTrackWithRevision(t, s, project.ID)

	style := &domain.SubtitleStyle{
		X: 0.25, Y: 0.8, FontSize: 48, Align: domain.SubtitleAlignCenter,
		Outline: &domain.SubtitleOutlineStyle{Color: "#000000", Width: 0.1},
	}
	blocks := []domain.EraseBlock{{
		ID: "eblock-1", SegmentID: "seg-1", StartMs: 0, EndMs: 900,
		BBox: [4]int{10, 20, 110, 120}, Source: domain.EraseBlockSourceSource,
	}}
	rev, err := s.SaveSubtitleRevision(ctx, "track-1", "rev-1", RevisionSave{
		AuthorType: domain.AuthorTypeUser,
		Segments: []domain.SegmentInput{
			{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "changed"},
			{ID: "seg-2", StartMs: 1000, EndMs: 2000, Text: "world"},
		},
		Style:       style,
		EraseBlocks: blocks,
	}, testTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("SaveSubtitleRevision() error = %v", err)
	}
	if rev.Style == nil || rev.Style.FontSize != 48 {
		t.Fatalf("saved style = %+v", rev.Style)
	}
	if len(rev.EraseBlocks) != 1 || rev.EraseBlocks[0].ID != "eblock-1" {
		t.Fatalf("saved erase blocks = %+v", rev.EraseBlocks)
	}
	wantDigest, err := domain.RevisionBindingDigest(rev)
	if err != nil {
		t.Fatalf("RevisionBindingDigest: %v", err)
	}
	if rev.RevisionDigest != wantDigest {
		t.Fatalf("saved revision digest = %q, want %q", rev.RevisionDigest, wantDigest)
	}

	track, err := s.GetSubtitleTrack(ctx, "track-1")
	if err != nil {
		t.Fatalf("GetSubtitleTrack() error = %v", err)
	}
	var current *domain.SubtitleRevision
	for i := range track.Revisions {
		if track.Revisions[i].ID == track.CurrentRevisionID {
			current = &track.Revisions[i]
			break
		}
	}
	if current == nil {
		t.Fatal("track has no current revision")
	}
	if current.Style == nil || current.Style.X != 0.25 || current.Style.Outline == nil {
		t.Fatalf("roundtrip style = %+v", current.Style)
	}
	if len(current.EraseBlocks) != 1 || current.EraseBlocks[0].BBox != [4]int{10, 20, 110, 120} {
		t.Fatalf("roundtrip erase blocks = %+v", current.EraseBlocks)
	}
	if current.RevisionDigest == "" {
		t.Fatal("current revision digest not derived on read")
	}

	// 旧 revision（无 style/erase）保持 nil，读取不报错。
	old := track.Revisions[0]
	if old.Style != nil || old.EraseBlocks != nil {
		t.Fatalf("legacy revision gained style/erase: %+v", old)
	}
}

func TestSaveSubtitleRevisionNormalizesEmptyEraseBlocks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	project := seedProject(t, s, "project-empty-blocks")
	seedTrackWithRevision(t, s, project.ID)

	rev, err := s.SaveSubtitleRevision(ctx, "track-1", "rev-1", RevisionSave{
		AuthorType: domain.AuthorTypeUser,
		Segments: []domain.SegmentInput{
			{ID: "seg-1", StartMs: 0, EndMs: 900, Text: "changed"},
		},
		EraseBlocks: []domain.EraseBlock{},
	}, testTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("SaveSubtitleRevision() error = %v", err)
	}
	if rev.EraseBlocks != nil {
		t.Fatalf("empty erase blocks = %+v, want nil", rev.EraseBlocks)
	}
}
