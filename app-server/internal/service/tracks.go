package service

import (
	"context"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// SaveSubtitleRevisionRequest 是保存人工修改输入；content_revision/digest 由 Core 计算。
type SaveSubtitleRevisionRequest struct {
	BaseRevisionID string                `json:"base_revision_id"`
	Segments       []domain.SegmentInput `json:"segments"`
	Style          *domain.SubtitleStyle `json:"style,omitempty"`
	EraseBlocks    []domain.EraseBlock   `json:"erase_blocks,omitempty"`
}

// SaveSubtitleRevision 保存新的字幕 revision（201）：base revision 不一致返回冲突。
//
// 保存成功后使相关审核失效：为受影响的非终止 Run 新建 pending ReviewGate，
// 旧 approved 绑定旧摘要，不能放行新内容。暂停中的 Run 同样允许保存编辑。
func (s *Service) SaveSubtitleRevision(ctx context.Context, idem Idem, trackID string, req SaveSubtitleRevisionRequest) (Response, error) {
	if trackID == "" {
		return Response{}, invalidRequestf("track id required")
	}
	if err := domain.ValidateSegmentInputs(req.Segments); err != nil {
		return Response{}, err
	}
	if err := domain.ValidateSubtitleStyle(req.Style); err != nil {
		return Response{}, err
	}
	segmentIDs := make(map[domain.ID]struct{}, len(req.Segments))
	for _, segment := range req.Segments {
		if segment.ID != "" {
			segmentIDs[segment.ID] = struct{}{}
		}
	}
	eraseBlocks, err := normalizeEraseBlocks(req.EraseBlocks, segmentIDs)
	if err != nil {
		return Response{}, err
	}
	var events []domain.DomainEvent
	resp, err := s.write(ctx, "subtitle-tracks.revisions", idem, 201, func(ctx context.Context, tx *store.Tx) (any, error) {
		track, err := tx.GetSubtitleTrack(ctx, trackID)
		if err != nil {
			return nil, err
		}
		inputRevisionID := domain.ID("")
		if track.Type == domain.TrackTypeTranslation {
			inputRevisionID, err = s.currentSourceRevisionID(ctx, tx, track.ProjectID)
			if err != nil {
				return nil, err
			}
		}
		revision, err := tx.SaveSubtitleRevision(ctx, trackID, req.BaseRevisionID, store.RevisionSave{
			AuthorType:      domain.AuthorTypeUser,
			Segments:        req.Segments,
			InputRevisionID: inputRevisionID,
			Style:           req.Style,
			EraseBlocks:     eraseBlocks,
		}, s.Now())
		if err != nil {
			return nil, err
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type:      "subtitle.revision_saved",
			ProjectID: track.ProjectID,
			Data: map[string]any{
				"track_id":    trackID,
				"revision_id": revision.ID,
				"segments":    len(revision.Segments),
			},
		}); err != nil {
			return nil, err
		}
		if err := s.invalidateReviews(ctx, tx, track, revision, &events); err != nil {
			return nil, err
		}
		return revision, nil
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	return resp, nil
}

// currentSourceRevisionID 返回译文 revision 应记录的源 transcription revision。
//
// 有 Run 时只认最近 Run 的源语言对应轨；找不到同语言源轨时返回空（未知），
// 不回退到其他语言。无 Run 时仅在唯一 transcription 轨时使用，避免多语歧义。
func (s *Service) currentSourceRevisionID(
	ctx context.Context,
	tx *store.Tx,
	projectID domain.ID,
) (domain.ID, error) {
	tracks, err := tx.ListProjectTracks(ctx, projectID)
	if err != nil {
		return "", err
	}
	runs, err := tx.ListProjectRuns(ctx, projectID)
	if err != nil {
		return "", err
	}
	if len(runs) > 0 {
		sourceLanguage := runs[len(runs)-1].SourceLanguage
		if sourceLanguage == "" {
			return "", nil
		}
		track := pickPipelineTrack(tracks, sourceLanguage, domain.TrackTypeTranscription)
		if track == nil {
			return "", nil
		}
		return track.CurrentRevisionID, nil
	}
	var only *domain.SubtitleTrack
	for i := range tracks {
		track := &tracks[i]
		if track.Type != domain.TrackTypeTranscription || track.CurrentRevisionID == "" {
			continue
		}
		if only != nil {
			return "", nil
		}
		only = track
	}
	if only == nil {
		return "", nil
	}
	return only.CurrentRevisionID, nil
}

// invalidateReviews 为受影响的非终止 Run 新建 pending 审核门槛。
func (s *Service) invalidateReviews(ctx context.Context, tx *store.Tx, track domain.SubtitleTrack, revision domain.SubtitleRevision, events *[]domain.DomainEvent) error {
	digest, err := domain.RevisionBindingDigest(revision)
	if err != nil {
		return err
	}
	runs, err := tx.ListProjectRuns(ctx, track.ProjectID)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.Status.Terminal() {
			continue
		}
		tpl, err := pipelineTemplateForRun(run)
		if err != nil {
			return err
		}
		// 工具模板（如 tool_translate）没有审核节点，人工编辑不应给 Run 造审核门槛。
		if !tpl.HasReviewNodes() {
			continue
		}
		existing, err := tx.ListRunReviews(ctx, run.ID)
		if err != nil {
			return err
		}
		hasPending := func(typ domain.ReviewType, language, bindDigest string) bool {
			for _, gate := range existing {
				if gate.Type == typ && gate.Language == language &&
					gate.Status == domain.ReviewStatusPending && gate.RevisionDigest == bindDigest {
					return true
				}
			}
			return false
		}
		languages := run.Languages
		if track.Type == domain.TrackTypeTranslation {
			languages = []string{track.Language}
		}
		for _, language := range languages {
			// 06 审核门槛与模型生成门槛同口径：绑定原始 RevisionBindingDigest，
			// language 是独立字段。transcription 轨影响多语言的既有绑定保持不变。
			bind := digest
			if track.Type != domain.TrackTypeTranslation {
				bind = digest + ":" + language
			}
			if hasPending(domain.ReviewTypeTranslation, language, bind) {
				continue
			}
			gate, err := s.createReviewGate(ctx, tx, run.ID, domain.ReviewTypeTranslation, language, bind)
			if err != nil {
				return err
			}
			if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
				Type:      "review.created",
				ProjectID: run.ProjectID,
				Data: map[string]any{
					"run_id":          run.ID,
					"review_id":       gate.ID,
					"review_type":     gate.Type,
					"language":        language,
					"revision_digest": bind,
				},
			}); err != nil {
				return err
			}
		}
		deliveryBind := domain.DeliveryBind(digest)
		if !hasPending(domain.ReviewTypeDelivery, "", deliveryBind) {
			gate, err := s.createReviewGate(ctx, tx, run.ID, domain.ReviewTypeDelivery, "", deliveryBind)
			if err != nil {
				return err
			}
			if err := s.stageEvent(ctx, tx, events, domain.DomainEvent{
				Type:      "review.created",
				ProjectID: run.ProjectID,
				Data: map[string]any{
					"run_id":          run.ID,
					"review_id":       gate.ID,
					"review_type":     gate.Type,
					"revision_digest": deliveryBind,
				},
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// normalizeEraseBlocks 为省略 ID 的块分配稳定 ID，并校验契约可判定的几何/来源规则。
// 这是 revision 级占位数据，不创建擦除任务。
func normalizeEraseBlocks(blocks []domain.EraseBlock, segmentIDs map[domain.ID]struct{}) ([]domain.EraseBlock, error) {
	if len(blocks) == 0 {
		return nil, nil
	}
	out := make([]domain.EraseBlock, len(blocks))
	copy(out, blocks)
	for i := range out {
		if out[i].ID == "" {
			out[i].ID = store.NewID("eblock")
		}
		if out[i].Source == "" {
			out[i].Source = domain.EraseBlockSourceClean
		}
	}
	if err := domain.ValidateEraseBlocks(out, segmentIDs); err != nil {
		return nil, err
	}
	return out, nil
}
