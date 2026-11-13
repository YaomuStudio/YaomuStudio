package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func TestParseSRT(t *testing.T) {
	t.Run("bom crlf multiline and dot millis", func(t *testing.T) {
		raw := "\xEF\xBB\xBF1\r\n00:00:01,000 --> 00:00:02,500\r\nHello\r\nworld\r\n\r\n" +
			"2\r\n00:00:03.250 --> 00:00:04.000\r\nSecond\r\n"
		segments, err := parseSRT([]byte(raw))
		if err != nil {
			t.Fatalf("parseSRT: %v", err)
		}
		if len(segments) != 2 {
			t.Fatalf("segments = %d, want 2", len(segments))
		}
		if segments[0].StartMs != 1000 || segments[0].EndMs != 2500 {
			t.Errorf("first timing = %d..%d", segments[0].StartMs, segments[0].EndMs)
		}
		if segments[0].Text != "Hello\nworld" {
			t.Errorf("first text = %q", segments[0].Text)
		}
		if segments[1].StartMs != 3250 || segments[1].Text != "Second" {
			t.Errorf("second = %+v", segments[1])
		}
	})

	t.Run("index optional and trailing settings ignored", func(t *testing.T) {
		raw := "00:00:00,000 --> 00:00:01,000 position:50%\nCue\n"
		segments, err := parseSRT([]byte(raw))
		if err != nil {
			t.Fatalf("parseSRT: %v", err)
		}
		if len(segments) != 1 || segments[0].Text != "Cue" {
			t.Fatalf("segments = %+v", segments)
		}
	})

	invalid := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"no cues", "\n\n  \n"},
		{"missing arrow", "1\n00:00:01,000\nText\n"},
		{"end before start", "1\n00:00:02,000 --> 00:00:01,000\nText\n"},
		{"overlap", "1\n00:00:01,000 --> 00:00:03,000\nA\n\n2\n00:00:02,000 --> 00:00:04,000\nB\n"},
		{"bad milliseconds", "1\n00:00:01,1234 --> 00:00:02,000\nText\n"},
		{"bad minutes", "1\n00:60:01,000 --> 00:01:02,000\nText\n"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseSRT([]byte(tc.raw)); !errors.Is(err, domain.ErrInvalidRequest) {
				t.Fatalf("parseSRT error = %v, want ErrInvalidRequest", err)
			}
		})
	}

	t.Run("oversized input rejected", func(t *testing.T) {
		raw := strings.Repeat("x", maxSubtitleBytes+1)
		if _, err := parseSRT([]byte(raw)); !errors.Is(err, domain.ErrInvalidRequest) {
			t.Fatalf("parseSRT error = %v, want ErrInvalidRequest", err)
		}
	})
}
