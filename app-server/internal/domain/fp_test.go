package domain

import (
	"errors"
	"testing"
)

func fpSegment(id, text string, start, end Millis) Segment {
	return Segment{
		ID:              id,
		StartMs:         start,
		EndMs:           end,
		Text:            text,
		ContentRevision: 1,
		ContentDigest:   SegmentContentDigest(text, ""),
	}
}

func fpScope() CacheScope {
	return CacheScope{
		SourceAudioDigest: "voice-digest",
		ModelLock: ReleaseRef{
			ReleaseID: "tts-ja",
			Version:   "1.0.0",
			Digest:    "release-digest",
		},
		Params: map[string]any{
			"speed":         1.0,
			"pitch":         0,
			"format":        "wav",
			"auto_duration": true,
		},
		ContextRadius: 1,
	}
}

func TestSegmentContentDigest(t *testing.T) {
	a := SegmentContentDigest("こんにちは", "speaker-1")
	b := SegmentContentDigest("こんにちは", "speaker-1")
	if a != b {
		t.Errorf("digest not stable: %s != %s", a, b)
	}
	if a == SegmentContentDigest("こんにちは", "speaker-2") {
		t.Error("speaker change did not change digest")
	}
	if a == SegmentContentDigest("変更", "speaker-1") {
		t.Error("text change did not change digest")
	}
	if len(a) != 64 {
		t.Errorf("digest length = %d, want 64 (sha256 hex)", len(a))
	}
}

func TestSegmentCacheKeyStableAndExcludedTrackRevision(t *testing.T) {
	seg := fpSegment("seg-1", "hello", 1000, 2000)
	scope := fpScope()

	key1, err := SegmentCacheKey(seg, nil, nil, scope)
	if err != nil {
		t.Fatalf("SegmentCacheKey() error = %v", err)
	}
	key2, err := SegmentCacheKey(seg, nil, nil, scope)
	if err != nil {
		t.Fatalf("SegmentCacheKey() error = %v", err)
	}
	if key1 != key2 {
		t.Errorf("cache key not stable: %s != %s", key1, key2)
	}

	// 参数 map 的插入顺序不影响键（encoding/json 排序 map 键）。
	reordered := fpScope()
	reordered.Params = map[string]any{
		"auto_duration": true,
		"format":        "wav",
		"pitch":         0,
		"speed":         1.0,
	}
	key3, err := SegmentCacheKey(seg, nil, nil, reordered)
	if err != nil {
		t.Fatalf("SegmentCacheKey() error = %v", err)
	}
	if key3 != key1 {
		t.Errorf("param map order changed cache key")
	}

	// nil 与空参数等价。
	nilParams := scope
	nilParams.Params = nil
	emptyParams := scope
	emptyParams.Params = map[string]any{}
	k4, err := SegmentCacheKey(seg, nil, nil, nilParams)
	if err != nil {
		t.Fatalf("SegmentCacheKey() error = %v", err)
	}
	k5, err := SegmentCacheKey(seg, nil, nil, emptyParams)
	if err != nil {
		t.Fatalf("SegmentCacheKey() error = %v", err)
	}
	if k4 != k5 {
		t.Error("nil params and empty params produced different keys")
	}
	if k4 == key1 {
		t.Error("dropping params did not change cache key")
	}
}

func TestSegmentCacheKeySensitivity(t *testing.T) {
	base := fpSegment("seg-1", "hello", 1000, 2000)
	scope := fpScope()
	baseKey, err := SegmentCacheKey(base, nil, nil, scope)
	if err != nil {
		t.Fatalf("SegmentCacheKey() error = %v", err)
	}

	changedText := base
	changedText.Text = "hello again"
	changedText.ContentDigest = SegmentContentDigest("hello again", "")
	changedText.ContentRevision = 2

	changedWindow := base
	changedWindow.StartMs = 1100

	changedSource := scope
	changedSource.SourceAudioDigest = "another-voice"

	changedModel := scope
	changedModel.ModelLock.Version = "1.0.1"

	changedParams := fpScope()
	changedParams.Params["speed"] = 1.2

	cases := []struct {
		name  string
		seg   Segment
		scope CacheScope
	}{
		{"content", changedText, scope},
		{"time window", changedWindow, scope},
		{"source audio", base, changedSource},
		{"model version", base, changedModel},
		{"params", base, changedParams},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, err := SegmentCacheKey(tc.seg, nil, nil, tc.scope)
			if err != nil {
				t.Fatalf("SegmentCacheKey() error = %v", err)
			}
			if key == baseKey {
				t.Errorf("%s change did not change cache key", tc.name)
			}
		})
	}
}

func TestSegmentCacheKeysNeighborWindow(t *testing.T) {
	segments := []Segment{
		fpSegment("a", "one", 0, 1000),
		fpSegment("b", "two", 1000, 2000),
		fpSegment("c", "three", 2000, 3000),
		fpSegment("d", "four", 3000, 4000),
	}

	t.Run("radius 1 invalidates edited segment and immediate neighbors", func(t *testing.T) {
		scope := fpScope()
		before, err := SegmentCacheKeys(segments, scope)
		if err != nil {
			t.Fatalf("SegmentCacheKeys() error = %v", err)
		}
		edited := append([]Segment(nil), segments...)
		edited[1].Text = "two changed"
		edited[1].ContentDigest = SegmentContentDigest("two changed", "")
		edited[1].ContentRevision = 2
		after, err := SegmentCacheKeys(edited, scope)
		if err != nil {
			t.Fatalf("SegmentCacheKeys() error = %v", err)
		}
		for _, id := range []ID{"a", "b", "c"} {
			if before[id] == after[id] {
				t.Errorf("segment %s key unchanged, want invalidated by adjacent edit", id)
			}
		}
		if before["d"] != after["d"] {
			t.Error("segment d key changed, want unchanged (outside context window)")
		}
	})

	t.Run("radius 2 extends invalidated window", func(t *testing.T) {
		scope := fpScope()
		scope.ContextRadius = 2
		before, err := SegmentCacheKeys(segments, scope)
		if err != nil {
			t.Fatalf("SegmentCacheKeys() error = %v", err)
		}
		edited := append([]Segment(nil), segments...)
		edited[1].ContentDigest = "changed-digest"
		after, err := SegmentCacheKeys(edited, scope)
		if err != nil {
			t.Fatalf("SegmentCacheKeys() error = %v", err)
		}
		if before["d"] == after["d"] {
			t.Error("segment d key unchanged with radius 2, want invalidated")
		}
	})

	t.Run("radius 0 only invalidates edited segment", func(t *testing.T) {
		scope := fpScope()
		scope.ContextRadius = 0
		before, err := SegmentCacheKeys(segments, scope)
		if err != nil {
			t.Fatalf("SegmentCacheKeys() error = %v", err)
		}
		edited := append([]Segment(nil), segments...)
		edited[1].ContentDigest = "changed-digest"
		after, err := SegmentCacheKeys(edited, scope)
		if err != nil {
			t.Fatalf("SegmentCacheKeys() error = %v", err)
		}
		if before["a"] != after["a"] || before["c"] != after["c"] || before["d"] != after["d"] {
			t.Error("radius 0 invalidated neighbors")
		}
		if before["b"] == after["b"] {
			t.Error("radius 0 did not invalidate edited segment")
		}
	})
}

func TestSegmentCacheKeyValidation(t *testing.T) {
	if _, err := SegmentCacheKey(Segment{ID: ""}, nil, nil, CacheScope{}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("empty segment id error = %v, want ErrInvalidRequest", err)
	}
	badScope := CacheScope{ContextRadius: -1}
	if _, err := SegmentCacheKey(fpSegment("a", "x", 0, 1), nil, nil, badScope); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("negative radius error = %v, want ErrInvalidRequest", err)
	}
	nonEncodable := CacheScope{Params: map[string]any{"bad": func() {}}}
	if _, err := SegmentCacheKey(fpSegment("a", "x", 0, 1), nil, nil, nonEncodable); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("non-encodable params error = %v, want ErrInvalidRequest", err)
	}
	dupes := []Segment{fpSegment("a", "x", 0, 1), fpSegment("a", "y", 1, 2)}
	if _, err := SegmentCacheKeys(dupes, CacheScope{}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("duplicate ids error = %v, want ErrInvalidRequest", err)
	}
}

func TestDigest(t *testing.T) {
	a, err := Digest(map[string]any{"b": 2, "a": 1})
	if err != nil {
		t.Fatalf("Digest() error = %v", err)
	}
	b, err := Digest(map[string]any{"a": 1, "b": 2})
	if err != nil {
		t.Fatalf("Digest() error = %v", err)
	}
	if a != b {
		t.Error("Digest() not stable across map key order")
	}
	if _, err := Digest(map[string]any{"bad": make(chan int)}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("Digest(chan) error = %v, want ErrInvalidRequest", err)
	}
	keys := SortedMapKeys(map[string]any{"b": 1, "a": 2})
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Errorf("SortedMapKeys() = %v, want [a b]", keys)
	}
}
