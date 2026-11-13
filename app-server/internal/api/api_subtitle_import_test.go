package api

import (
	"net/http"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

const testSRT = `1
00:00:00,000 --> 00:00:01,000
Hello

2
00:00:01,200 --> 00:00:02,000
World
`

func TestImportSubtitleTrack(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("字幕导入")
	asset := env.seedSubtitleAsset(string(projectID), testSRT)

	body := map[string]any{
		"project_id": string(projectID),
		"asset_id":   string(asset.ID),
		"language":   "en",
	}
	track := decodeInto[domain.SubtitleTrack](t, env.expect(
		env.request(http.MethodPost, "/api/v1/subtitle-tracks/imports", body), http.StatusCreated))
	if track.Type != domain.TrackTypeTranscription || track.Language != "en" {
		t.Fatalf("track = %s/%s, want transcription/en", track.Type, track.Language)
	}
	if track.ProjectID != projectID {
		t.Fatalf("track project = %s, want %s", track.ProjectID, projectID)
	}
	if len(track.Revisions) != 1 || len(track.Revisions[0].Segments) != 2 {
		t.Fatalf("track revisions = %+v, want 2 segments", track.Revisions)
	}
	if track.Revisions[0].Segments[0].Text != "Hello" {
		t.Fatalf("first segment = %q", track.Revisions[0].Segments[0].Text)
	}

	// 同一 Idempotency-Key 不重复建轨。
	idemKey := store.NewID("idem")
	withKey := func(r *http.Request) { r.Header.Set("Idempotency-Key", idemKey) }
	first := decodeInto[domain.SubtitleTrack](t, env.expect(
		env.request(http.MethodPost, "/api/v1/subtitle-tracks/imports", body, withKey), http.StatusCreated))
	second := decodeInto[domain.SubtitleTrack](t, env.expect(
		env.request(http.MethodPost, "/api/v1/subtitle-tracks/imports", body, withKey), http.StatusCreated))
	if first.ID != second.ID {
		t.Fatalf("idempotent import created different tracks: %s vs %s", first.ID, second.ID)
	}
	project := decodeInto[domain.Project](t, env.expect(
		env.request(http.MethodGet, "/api/v1/projects/"+string(projectID), nil), http.StatusOK))
	transcriptionTracks := 0
	for _, item := range project.Tracks {
		if item.Type == domain.TrackTypeTranscription && item.Language == "en" {
			transcriptionTracks++
		}
	}
	if transcriptionTracks != 2 {
		// 第一次非幂等调用 + 一次幂等调用 = 2 条独立导入轨。
		t.Fatalf("transcription tracks = %d, want 2", transcriptionTracks)
	}
}

func TestImportSubtitleTrackRejectsOversizedSRT(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("字幕超限")
	// service.maxSubtitleBytes 当前为 32 MiB；超限时按大小拒绝，不解析内容。
	asset := env.seedSubtitleAssetSize(string(projectID), 32<<20+1)
	env.expect(env.request(http.MethodPost, "/api/v1/subtitle-tracks/imports", map[string]any{
		"project_id": string(projectID),
		"asset_id":   string(asset.ID),
		"language":   "en",
	}), http.StatusBadRequest)
}

func TestImportSubtitleTrackRejectsInvalidInput(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("字幕导入校验")
	other := env.seedProject("其他项目")
	video := env.seedVideoAsset(string(projectID), 16)
	audio := env.seedAudioAsset(string(projectID))
	otherSubtitle := env.seedSubtitleAsset(string(other), testSRT)
	malformed := env.seedSubtitleAsset(string(projectID), "not an srt")

	base := func(assetID, language string) map[string]any {
		return map[string]any{
			"project_id": string(projectID),
			"asset_id":   assetID,
			"language":   language,
		}
	}
	env.expect(env.request(http.MethodPost, "/api/v1/subtitle-tracks/imports", base(string(video.ID), "en")), http.StatusBadRequest)
	env.expect(env.request(http.MethodPost, "/api/v1/subtitle-tracks/imports", base(string(audio.ID), "en")), http.StatusBadRequest)
	env.expect(env.request(http.MethodPost, "/api/v1/subtitle-tracks/imports", base(string(otherSubtitle.ID), "en")), http.StatusBadRequest)
	env.expect(env.request(http.MethodPost, "/api/v1/subtitle-tracks/imports", base(string(malformed.ID), "en")), http.StatusBadRequest)
	env.expect(env.request(http.MethodPost, "/api/v1/subtitle-tracks/imports", map[string]any{
		"project_id": string(projectID),
		"asset_id":   string(otherSubtitle.ID),
		"language":   "",
	}), http.StatusBadRequest)
	env.expect(env.request(http.MethodPost, "/api/v1/subtitle-tracks/imports", map[string]any{
		"project_id": string(projectID),
		"asset_id":   "missing",
		"language":   "en",
	}), http.StatusBadRequest)
}

func TestImportSubtitleTrackType(t *testing.T) {
	env := newTestEnv(t)
	projectID := env.seedProject("字幕导入类型")
	asset := env.seedSubtitleAsset(string(projectID), testSRT)

	track := decodeInto[domain.SubtitleTrack](t, env.expect(env.request(http.MethodPost, "/api/v1/subtitle-tracks/imports", map[string]any{
		"project_id": string(projectID),
		"asset_id":   string(asset.ID),
		"language":   "zh",
		"type":       "translation",
	}), http.StatusCreated))
	if track.Type != domain.TrackTypeTranslation || track.Language != "zh" {
		t.Fatalf("track = %s/%s, want translation/zh", track.Type, track.Language)
	}

	env.expect(env.request(http.MethodPost, "/api/v1/subtitle-tracks/imports", map[string]any{
		"project_id": string(projectID),
		"asset_id":   string(asset.ID),
		"language":   "zh",
		"type":       "unknown",
	}), http.StatusBadRequest)
}
