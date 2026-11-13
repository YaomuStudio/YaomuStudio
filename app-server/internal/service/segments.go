package service

import (
	"context"
	"fmt"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// CreateCandidatesRequest 是句级重抽输入。
type CreateCandidatesRequest struct {
	Language string `json:"language"`
}

// SelectCandidateRequest 是选用配音输入。
type SelectCandidateRequest struct {
	Language    string `json:"language"`
	CandidateID string `json:"candidate_id"`
}

// SegmentCandidateJob 是重抽受理结果。
type SegmentCandidateJob struct {
	TaskID    domain.ID `json:"task_id"`
	SegmentID domain.ID `json:"segment_id"`
	Language  string    `json:"language"`
	AttemptNo int       `json:"attempt_no"`
}

// ListCandidates 返回句段某语言已有候选；只返回该句该语言。
func (s *Service) ListCandidates(ctx context.Context, segmentID, language string) ([]domain.AudioCandidate, error) {
	if segmentID == "" {
		return nil, invalidRequestf("segment id required")
	}
	if language == "" {
		return nil, invalidRequestf("language query parameter required")
	}
	if _, err := s.store.GetSegmentContext(ctx, segmentID); err != nil {
		return nil, err
	}
	candidates, err := s.store.ListAudioCandidates(ctx, segmentID, language, nil)
	if err != nil {
		return nil, err
	}
	if candidates == nil {
		candidates = []domain.AudioCandidate{}
	}
	return candidates, nil
}

// GenerateCandidates 受理一次句级重抽（202）：新任务 + 新 attempt，不覆盖旧候选。
//
// 若项目有非终止 Run，重抽绑定该 Run 的派发闸门：PAUSE_REQUESTED/PAUSED 时
// 只登记为 PAUSED，不派发。
func (s *Service) GenerateCandidates(ctx context.Context, idem Idem, segmentID string, req CreateCandidatesRequest) (Response, error) {
	if segmentID == "" {
		return Response{}, invalidRequestf("segment id required")
	}
	if req.Language == "" {
		return Response{}, invalidRequestf("language required")
	}
	var events []domain.DomainEvent
	resp, err := s.write(ctx, "segments.candidates", idem, 202, func(ctx context.Context, tx *store.Tx) (any, error) {
		segment, err := tx.GetSegmentContext(ctx, segmentID)
		if err != nil {
			return nil, err
		}
		if segment.Language != req.Language || segment.TrackType != domain.TrackTypeTranslation {
			return nil, invalidRequestf("candidate language must match the translation segment")
		}
		run, found, err := tx.LatestRunForProject(ctx, segment.ProjectID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, executionUnverified()
		}
		if run.Status.Terminal() {
			return nil, errRunClosed(run.ID, run.Status)
		}
		tpl, err := pipelineTemplateForRun(run)
		if err != nil {
			return nil, err
		}
		if !tpl.Has(domain.NodeKeyClone) {
			return nil, invalidRequestf("run does not contain a clone node")
		}
		snap, err := s.loadPipelineSnapshot(ctx, tx, run.ID)
		if err != nil {
			return nil, err
		}
		revision := translationRevisionForRun(snap, req.Language, tpl)
		if revision == nil || revision.ID != segment.RevisionID {
			return nil, fmt.Errorf("%w: segment is not part of this run's translation input", domain.ErrRevisionConflict)
		}
		work, err := s.segmentWorkForRevision(ctx, tx, snap, *revision, req.Language)
		if err != nil {
			return nil, err
		}
		item, err := requireSegmentWork(work, segmentID)
		if err != nil {
			return nil, err
		}
		params, modelLock, err := freezeTaskParams(run, domain.NodeKeyClone, item.Params)
		if err != nil {
			return nil, err
		}
		status := domain.TaskStatusQueued
		if !domain.RunAllowsDispatch(run.Status) {
			status = domain.TaskStatusPaused
		}
		inputIDs := []domain.ID{segment.RevisionID, item.SourceID}
		digest, err := taskInputDigest(CreateTaskRequest{
			RunID: run.ID, Kind: domain.TaskKindSegment, NodeKey: domain.NodeKeyClone,
			SegmentID: segmentID, Language: req.Language,
			InputRevisionIDs: inputIDs, Params: params, ModelLock: modelLock,
		})
		if err != nil {
			return nil, err
		}
		now := s.Now()
		stepID := stepIDForTask(domain.TaskKindSegment, domain.NodeKeyClone)
		task := domain.Task{
			ID:               store.NewID("task"),
			ProjectID:        segment.ProjectID,
			RunID:            run.ID,
			Kind:             domain.TaskKindSegment,
			NodeKey:          domain.NodeKeyClone,
			SegmentID:        segmentID,
			Language:         req.Language,
			Status:           status,
			ArtifactIDs:      []domain.ID{},
			InputRevisionIDs: inputIDs,
			InputDigest:      digest,
			Params:           params,
			ModelLock:        modelLock,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		task.Steps = []domain.TaskStep{{
			StepID:    stepID,
			Status:    domain.StepStatusPending,
			AttemptNo: 1,
			Epoch:     s.opts.ServerEpoch,
		}}
		if err := tx.CreateTask(ctx, task); err != nil {
			return nil, err
		}
		if err := tx.CreateAttempt(ctx, domain.Attempt{
			ID:        store.NewID("attempt"),
			TaskID:    task.ID,
			StepID:    stepID,
			AttemptNo: 1,
			Epoch:     s.opts.ServerEpoch,
			Status:    domain.StepStatusPending,
		}); err != nil {
			return nil, err
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type:         "task.created",
			ProjectID:    task.ProjectID,
			TaskID:       task.ID,
			StateVersion: task.StateVersion,
			Data: map[string]any{
				"kind":       task.Kind,
				"node_key":   task.NodeKey,
				"segment_id": task.SegmentID,
				"language":   task.Language,
				"status":     task.Status,
			},
		}); err != nil {
			return nil, err
		}
		return SegmentCandidateJob{
			TaskID:    task.ID,
			SegmentID: segmentID,
			Language:  req.Language,
			AttemptNo: 1,
		}, nil
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	s.notify()
	return resp, nil
}

// GetCandidateSelection 返回句段某语言当前选用；尚无选择返回 NOT_FOUND。
func (s *Service) GetCandidateSelection(ctx context.Context, segmentID, language string) (domain.CandidateSelection, error) {
	if segmentID == "" {
		return domain.CandidateSelection{}, invalidRequestf("segment id required")
	}
	if language == "" {
		return domain.CandidateSelection{}, invalidRequestf("language query parameter required")
	}
	if _, err := s.store.GetSegmentContext(ctx, segmentID); err != nil {
		return domain.CandidateSelection{}, err
	}
	return s.store.GetCandidateSelection(ctx, segmentID, language)
}

// SelectCandidate 登记选用（200）：绑定句段当前 content_revision。
func (s *Service) SelectCandidate(ctx context.Context, idem Idem, segmentID string, req SelectCandidateRequest) (Response, error) {
	if segmentID == "" {
		return Response{}, invalidRequestf("segment id required")
	}
	if req.Language == "" {
		return Response{}, invalidRequestf("language required")
	}
	if req.CandidateID == "" {
		return Response{}, invalidRequestf("candidate_id required")
	}
	var events []domain.DomainEvent
	resp, err := s.write(ctx, "segments.selection", idem, 200, func(ctx context.Context, tx *store.Tx) (any, error) {
		segment, err := tx.GetSegmentContext(ctx, segmentID)
		if err != nil {
			return nil, err
		}
		selection := domain.CandidateSelection{
			SegmentID:   segmentID,
			Language:    req.Language,
			CandidateID: req.CandidateID,
			Revision:    segment.Segment.ContentRevision,
			SelectedAt:  s.Now(),
		}
		if err := tx.SaveCandidateSelection(ctx, selection); err != nil {
			return nil, err
		}
		if err := s.stageEvent(ctx, tx, &events, domain.DomainEvent{
			Type:      "segment.candidate_selected",
			ProjectID: segment.ProjectID,
			Data: map[string]any{
				"segment_id":   segmentID,
				"language":     req.Language,
				"candidate_id": req.CandidateID,
				"revision":     selection.Revision,
			},
		}); err != nil {
			return nil, err
		}
		return selection, nil
	})
	if err != nil {
		return Response{}, err
	}
	s.publish(events...)
	return resp, nil
}
