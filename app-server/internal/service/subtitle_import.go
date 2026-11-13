package service

import (
	"context"
	"os"
	"strings"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// ImportSubtitleTrackInput 是从已导入 SRT Asset 创建原文轨的请求。
type ImportSubtitleTrackInput struct {
	ProjectID string `json:"project_id"`
	AssetID   string `json:"asset_id"`
	Language  string `json:"language"`
	Type      string `json:"type"`
}

// ImportSubtitleTrack 从同项目的 SRT Asset 解析并创建新的 transcription 轨（201）。
//
// 文件读取/解析在事务外完成；事务内只复核项目/资产并写业务状态。
// 不读取任意宿主机路径，也不接受非 subtitle Asset。
func (s *Service) ImportSubtitleTrack(
	ctx context.Context,
	idem Idem,
	in ImportSubtitleTrackInput,
) (Response, error) {
	projectID := strings.TrimSpace(in.ProjectID)
	assetID := strings.TrimSpace(in.AssetID)
	language := strings.TrimSpace(in.Language)
	if projectID == "" {
		return Response{}, invalidRequestf("project_id required")
	}
	if assetID == "" {
		return Response{}, invalidRequestf("asset_id required")
	}
	if language == "" {
		return Response{}, invalidRequestf("language required")
	}
	trackType := domain.TrackType(strings.TrimSpace(in.Type))
	if trackType == "" {
		trackType = domain.TrackTypeTranscription
	}
	if trackType != domain.TrackTypeTranscription && trackType != domain.TrackTypeTranslation {
		return Response{}, invalidRequestf("invalid subtitle track type %q", in.Type)
	}

	project, err := s.store.GetProject(ctx, storeID(projectID))
	if err != nil {
		return Response{}, mapReference(err, "project")
	}
	asset, err := s.store.GetAsset(ctx, storeID(assetID))
	if err != nil {
		return Response{}, mapReference(err, "asset")
	}
	if asset.ProjectID != project.ID {
		return Response{}, invalidRequestf("asset %s does not belong to project %s", assetID, projectID)
	}
	if asset.Type != domain.AssetTypeSubtitle {
		return Response{}, invalidRequestf("asset %s is not a subtitle file", assetID)
	}
	content, err := s.AssetContentFor(ctx, assetID)
	if err != nil {
		return Response{}, err
	}
	info, err := os.Stat(content.Path)
	if err != nil {
		return Response{}, notFoundf("asset %s has no readable file", assetID)
	}
	if info.Size() > maxSubtitleBytes {
		return Response{}, invalidRequestf("subtitle exceeds %d bytes", maxSubtitleBytes)
	}
	data, err := os.ReadFile(content.Path)
	if err != nil {
		return Response{}, notFoundf("asset %s has no readable file", assetID)
	}
	segments, err := parseSRT(data)
	if err != nil {
		return Response{}, err
	}
	if len(segments) == 0 {
		return Response{}, invalidRequestf("subtitle contains no segments")
	}

	var events []domain.DomainEvent
	resp, err := s.write(ctx, "subtitle-tracks.import", idem, 201, func(ctx context.Context, tx *store.Tx) (any, error) {
		// 事务内复核，避免文件读取期间项目/资产被删除或替换。
		project, err := tx.GetProject(ctx, storeID(projectID))
		if err != nil {
			return nil, mapReference(err, "project")
		}
		asset, err := tx.GetAsset(ctx, storeID(assetID))
		if err != nil {
			return nil, mapReference(err, "asset")
		}
		if asset.ProjectID != project.ID || asset.Type != domain.AssetTypeSubtitle {
			return nil, invalidRequestf("asset %s is not a subtitle of project %s", assetID, projectID)
		}

		now := s.Now()
		trackID := store.NewID("track")
		revisionID := store.NewID("rev")
		revision := domain.SubtitleRevision{
			ID:         revisionID,
			TrackID:    trackID,
			AuthorType: domain.AuthorTypeSystem,
			Segments:   make([]domain.Segment, 0, len(segments)),
			CreatedAt:  now,
		}
		for _, segment := range segments {
			revision.Segments = append(revision.Segments, domain.Segment{
				ID:      store.NewID("seg"),
				StartMs: segment.StartMs,
				EndMs:   segment.EndMs,
				Text:    segment.Text,
				Speaker: segment.Speaker,
				Lineage: segment.Lineage,
			})
		}
		track := domain.SubtitleTrack{
			ID:                trackID,
			ProjectID:         project.ID,
			Language:          language,
			Type:              trackType,
			CurrentRevisionID: revisionID,
			Revisions:         []domain.SubtitleRevision{revision},
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		if err := tx.CreateSubtitleTrack(ctx, track); err != nil {
			return nil, err
		}
		stored, err := tx.GetSubtitleTrack(ctx, trackID)
		if err != nil {
			return nil, err
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type:      "subtitle.track_imported",
			ProjectID: project.ID,
			Data: map[string]any{
				"track_id":   trackID,
				"language":   language,
				"track_type": string(trackType),
				"segments":   len(revision.Segments),
			},
		}); err != nil {
			return nil, err
		}
		return stored, nil
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	return resp, nil
}

// storeID 把契约字符串转成领域 ID；当前两者都是 string 的别名，集中转换便于审阅。
func storeID(value string) domain.ID {
	return domain.ID(value)
}
