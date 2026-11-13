package domain

import "testing"

// legacyRevisionBindingDigest 复刻阶段一（无 style/erase_blocks）的摘要 payload，
// 用于证明新增字段为 nil 时旧 revision 摘要不变。
func legacyRevisionBindingDigest(rev SubtitleRevision) (string, error) {
	return Digest(struct {
		RevisionID ID
		TrackID    ID
		Author     AuthorType
		Segments   []Segment
	}{
		RevisionID: rev.ID, TrackID: rev.TrackID,
		Author: rev.AuthorType, Segments: rev.Segments,
	})
}

func TestRevisionBindingDigestKeepsLegacyValueWhenStyleAndEraseAreNil(t *testing.T) {
	rev := SubtitleRevision{
		ID: "srev-1", TrackID: "track-1", AuthorType: AuthorTypeUser,
		Segments: []Segment{{
			ID: "seg-1", StartMs: 0, EndMs: 900, Text: "你好",
			ContentRevision: 1, ContentDigest: "digest-1",
		}},
	}
	want, err := legacyRevisionBindingDigest(rev)
	if err != nil {
		t.Fatalf("legacy digest: %v", err)
	}
	got, err := RevisionBindingDigest(rev)
	if err != nil {
		t.Fatalf("RevisionBindingDigest: %v", err)
	}
	if got != want {
		t.Fatalf("nil style/erase digest = %q, want legacy %q", got, want)
	}
}

func TestRevisionBindingDigestTracksStyleAndEraseBlocks(t *testing.T) {
	rev := SubtitleRevision{
		ID: "srev-1", TrackID: "track-1", AuthorType: AuthorTypeUser,
		Segments: []Segment{{
			ID: "seg-1", StartMs: 0, EndMs: 900, Text: "你好",
			ContentRevision: 1, ContentDigest: "digest-1",
		}},
	}
	base, err := RevisionBindingDigest(rev)
	if err != nil {
		t.Fatalf("base digest: %v", err)
	}
	rev.Style = &SubtitleStyle{X: 0.5, Y: 0.9, FontSize: 48}
	styled, err := RevisionBindingDigest(rev)
	if err != nil {
		t.Fatalf("styled digest: %v", err)
	}
	if styled == base {
		t.Fatal("style change did not change revision digest")
	}
	rev.EraseBlocks = []EraseBlock{{
		ID: "eblock-1", StartMs: 0, EndMs: 900,
		BBox: [4]int{10, 20, 110, 120}, Source: EraseBlockSourceClean,
	}}
	blocked, err := RevisionBindingDigest(rev)
	if err != nil {
		t.Fatalf("erase digest: %v", err)
	}
	if blocked == styled || blocked == base {
		t.Fatal("erase block change did not change revision digest")
	}
}

func TestValidateEraseBlocks(t *testing.T) {
	segments := map[ID]struct{}{"seg-1": {}}
	valid := EraseBlock{
		ID: "eblock-1", SegmentID: "seg-1", StartMs: 0, EndMs: 900,
		BBox: [4]int{10, 20, 110, 120}, Source: EraseBlockSourceClean,
	}
	if err := ValidateEraseBlocks([]EraseBlock{valid}, segments); err != nil {
		t.Fatalf("valid block rejected: %v", err)
	}

	cases := []struct {
		name   string
		block  EraseBlock
		segIDs map[ID]struct{}
	}{
		{name: "duplicate id", block: EraseBlock{
			ID: "eblock-1", StartMs: 0, EndMs: 10, BBox: [4]int{0, 0, 1, 1},
		}, segIDs: segments},
		{name: "end before start", block: EraseBlock{
			ID: "eblock-2", StartMs: 10, EndMs: 10, BBox: [4]int{0, 0, 1, 1},
		}, segIDs: segments},
		{name: "inverted bbox", block: EraseBlock{
			ID: "eblock-3", StartMs: 0, EndMs: 10, BBox: [4]int{10, 0, 5, 1},
		}, segIDs: segments},
		{name: "invalid source", block: EraseBlock{
			ID: "eblock-4", StartMs: 0, EndMs: 10, BBox: [4]int{0, 0, 1, 1},
			Source: "other",
		}, segIDs: segments},
		{name: "unknown segment", block: EraseBlock{
			ID: "eblock-5", SegmentID: "seg-x", StartMs: 0, EndMs: 10,
			BBox: [4]int{0, 0, 1, 1},
		}, segIDs: segments},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blocks := []EraseBlock{tc.block}
			if tc.name == "duplicate id" {
				blocks = append(blocks, tc.block)
			}
			if err := ValidateEraseBlocks(blocks, tc.segIDs); err == nil {
				t.Fatal("invalid erase blocks accepted")
			}
		})
	}
}

func TestValidateEraseBlocksRejectsOverLimit(t *testing.T) {
	blocks := make([]EraseBlock, MaxEraseBlocks+1)
	for i := range blocks {
		blocks[i] = EraseBlock{
			ID: "eblock", StartMs: 0, EndMs: 10, BBox: [4]int{0, 0, 1, 1},
		}
	}
	if err := ValidateEraseBlocks(blocks, nil); err == nil {
		t.Fatal("erase blocks over the contract maxItems accepted")
	}
}
