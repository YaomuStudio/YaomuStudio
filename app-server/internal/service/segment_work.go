package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

type segmentWork struct {
	CacheKey string
	Params   map[string]any
	SourceID domain.ID
}

func (s *Service) segmentWorkForRevision(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	revision domain.SubtitleRevision,
	language string,
) (map[domain.ID]segmentWork, error) {
	cfg, err := frozenExecution(*snap.run, domain.NodeKeyClone)
	if err != nil {
		return nil, err
	}
	source, err := cloneReferenceAsset(ctx, tx, snap)
	if err != nil {
		return nil, err
	}
	if source.SHA256 == "" || source.SizeBytes <= 0 {
		return nil, changedAssetError()
	}
	var mediaDuration domain.Millis
	if snap.run.Config.ValidateTimestamps {
		mediaDuration, err = cloneMediaDuration(source)
		if err != nil {
			return nil, err
		}
	}
	sourceDigest, err := domain.Digest(struct {
		ID     domain.ID
		Digest string
		Size   int64
	}{ID: source.ID, Digest: source.SHA256, Size: source.SizeBytes})
	if err != nil {
		return nil, err
	}
	params := map[string]any{}
	for key, value := range cfg.Params {
		params[key] = value
	}
	params["language"] = language
	params["voice_source_asset_id"] = source.ID
	params["adjust_duration"] = snap.run.Config.AdjustDuration
	params["validate_timestamps"] = snap.run.Config.ValidateTimestamps
	params["device_id"] = cfg.DeviceID
	keys, err := domain.SegmentCacheKeys(revision.Segments, domain.CacheScope{
		SourceAudioDigest: sourceDigest,
		ModelLock:         cfg.ModelLock,
		Params:            params,
		ContextRadius:     cfg.ContextRadius,
	})
	if err != nil {
		return nil, err
	}
	sourceRevision := sourceTranscriptionRevision(snap)
	work := make(map[domain.ID]segmentWork, len(revision.Segments))
	for _, segment := range revision.Segments {
		sourceSegment, hasSource, err := cloneSourceSegment(segment, sourceRevision)
		if err != nil {
			return nil, err
		}
		if snap.run.Config.ValidateTimestamps {
			// 目标译文句与映射出的参考原文时间窗都必须落在冻结声源时长内；
			// end == duration 允许，越界/未知时长在冻结任务前拒绝。
			if err := domain.ValidateMediaTimeline(mediaDuration, []domain.TimelineWindow{
				{StartMs: segment.StartMs, EndMs: segment.EndMs},
				{StartMs: sourceSegment.StartMs, EndMs: sourceSegment.EndMs},
			}); err != nil {
				return nil, err
			}
		}
		frozen := map[string]any{}
		for key, value := range params {
			frozen[key] = value
		}
		frozen["content_revision"] = segment.ContentRevision
		// 逐句克隆：目标文本、参考文本与参考音频时间窗；适配器按句合成，
		// 不再对整段 SRT 重复跑全量。没有原文轨时退回译文时间窗，参考文本留空。
		frozen["text"] = segment.Text
		frozen["original_text"] = ""
		if hasSource {
			frozen["original_text"] = sourceSegment.Text
		}
		frozen["start_ms"] = sourceSegment.StartMs
		frozen["end_ms"] = sourceSegment.EndMs
		frozen["out"] = "candidate.wav"
		coreTaskParams(frozen)["segment_cache_key"] = keys[segment.ID]
		work[segment.ID] = segmentWork{CacheKey: keys[segment.ID], Params: frozen, SourceID: source.ID}
	}
	return work, nil
}

// cloneSourceSegment 把译文句段映射到原文句段，供参考音频切片与 reference_text。
// 优先使用显式 lineage；否则取唯一最大时间重叠，歧义时失败而不是猜。
// 没有原文轨时返回译文句段本身（reference_text 留空），保留导入译稿/自定义链路。
func cloneSourceSegment(translated domain.Segment, source *domain.SubtitleRevision) (domain.Segment, bool, error) {
	if source == nil || len(source.Segments) == 0 {
		return translated, false, nil
	}
	if len(translated.Lineage) > 0 {
		matched := make([]domain.Segment, 0, len(translated.Lineage))
		for _, segment := range source.Segments {
			for _, id := range translated.Lineage {
				if segment.ID == id {
					matched = append(matched, segment)
					break
				}
			}
		}
		switch len(matched) {
		case 1:
			return matched[0], true, nil
		case 0:
			// lineage 指向了别处：继续按时间重叠匹配，交给唯一性检查。
		default:
			return mergeSourceSegments(matched), true, nil
		}
	}
	bestOverlap := domain.Millis(0)
	best := domain.Segment{}
	ties := 0
	for _, segment := range source.Segments {
		start := translated.StartMs
		if segment.StartMs > start {
			start = segment.StartMs
		}
		end := translated.EndMs
		if segment.EndMs < end {
			end = segment.EndMs
		}
		overlap := end - start
		if overlap <= 0 {
			continue
		}
		switch {
		case overlap > bestOverlap:
			bestOverlap = overlap
			best = segment
			ties = 1
		case overlap == bestOverlap:
			ties++
		}
	}
	if bestOverlap <= 0 || ties != 1 {
		return domain.Segment{}, false, invalidRequestf(
			"clone source segment is ambiguous for segment %s", translated.ID)
	}
	return best, true, nil
}

func mergeSourceSegments(segments []domain.Segment) domain.Segment {
	merged := segments[0]
	texts := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment.StartMs < merged.StartMs {
			merged.StartMs = segment.StartMs
		}
		if segment.EndMs > merged.EndMs {
			merged.EndMs = segment.EndMs
		}
		if text := strings.TrimSpace(segment.Text); text != "" {
			texts = append(texts, text)
		}
	}
	merged.Text = strings.Join(texts, " ")
	return merged
}

func cloneReferenceAsset(ctx context.Context, tx *store.Tx, snap *pipelineSnapshot) (domain.Asset, error) {
	if id := snap.run.Config.ReuseAudioTrackID; id != "" {
		return pipelineAsset(ctx, tx, snap.run.ProjectID, id)
	}
	tpl, err := pipelineTemplateForRun(*snap.run)
	if err != nil {
		return domain.Asset{}, err
	}
	if tpl.Has(domain.NodeKeySeparate) {
		return separationRoleAsset(ctx, tx, snap, "vocals")
	}
	var source *domain.Asset
	for _, id := range snap.run.Config.InputRevisionIDs {
		asset, err := pipelineAsset(ctx, tx, snap.run.ProjectID, id)
		if err != nil {
			return domain.Asset{}, err
		}
		if !isPipelineAudio(asset) && asset.Type != domain.AssetTypeVideo {
			continue
		}
		if source != nil {
			return domain.Asset{}, invalidRequestf("clone reference media is ambiguous")
		}
		source = &asset
	}
	if source == nil {
		return domain.Asset{}, invalidRequestf("clone reference media is missing")
	}
	return *source, nil
}

func pipelineAsset(ctx context.Context, tx *store.Tx, projectID, id domain.ID) (domain.Asset, error) {
	asset, err := tx.GetAsset(ctx, id)
	if err != nil {
		return domain.Asset{}, mapReference(err, "pipeline asset")
	}
	if asset.ProjectID != projectID || asset.Status != domain.AssetStatusReady {
		return domain.Asset{}, invalidRequestf("pipeline asset %s is not available in this project", id)
	}
	return asset, nil
}

func separationRoleAsset(ctx context.Context, tx *store.Tx, snap *pipelineSnapshot, role string) (domain.Asset, error) {
	ids := latestSucceededArtifacts(snap.tasks, domain.NodeKeySeparate, "")
	var found *domain.Asset
	for _, id := range ids {
		asset, err := pipelineAsset(ctx, tx, snap.run.ProjectID, id)
		if err != nil {
			return domain.Asset{}, err
		}
		if !isPipelineAudio(asset) || asset.Meta == nil || asset.Meta.Role != role {
			continue
		}
		if found != nil {
			return domain.Asset{}, invalidRequestf("separation has multiple %s audio assets", role)
		}
		found = &asset
	}
	if found == nil {
		return domain.Asset{}, invalidRequestf("separation has no explicitly declared %s audio", role)
	}
	return *found, nil
}

func isPipelineAudio(asset domain.Asset) bool {
	return asset.Type == domain.AssetTypeAudio || asset.Type == domain.AssetTypeAudioTrack
}

func taskSegmentCacheKey(task domain.Task) string {
	core, ok := task.Params["_core"].(map[string]any)
	if !ok {
		return ""
	}
	key, _ := core["segment_cache_key"].(string)
	return key
}

func currentSegmentTask(tasks []domain.Task, language, segmentID, cacheKey string) (domain.Task, bool) {
	matching := make([]domain.Task, 0)
	for _, task := range tasks {
		if taskSegmentCacheKey(task) == cacheKey {
			matching = append(matching, task)
		}
	}
	return latestSegmentTask(matching, language, segmentID)
}

func freezeSubtitleOutput(snap *pipelineSnapshot, node domain.PipelineNodeKey, language string, params map[string]any) {
	trackType := domain.TrackTypeTranslation
	if node == domain.NodeKeyExtract {
		trackType = domain.TrackTypeTranscription
	}
	if node != domain.NodeKeyExtract && node != domain.NodeKeyTranslate {
		return
	}
	core := coreTaskParams(params)
	core["output_snapshot"] = true
	core["output_track_id"] = store.NewID("track")
	core["output_base_revision_id"] = ""
	if track := pickPipelineTrack(snap.tracks, language, trackType); track != nil {
		core["output_track_id"] = track.ID
		core["output_base_revision_id"] = track.CurrentRevisionID
	}
}

func requireSegmentWork(work map[domain.ID]segmentWork, id domain.ID) (segmentWork, error) {
	item, ok := work[id]
	if !ok {
		return segmentWork{}, fmt.Errorf("%w: segment %s has no frozen work", domain.ErrInvalidRequest, id)
	}
	return item, nil
}
