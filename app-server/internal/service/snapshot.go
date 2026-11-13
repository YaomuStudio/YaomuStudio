package service

import (
	"context"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// Snapshot 在单个读事务内返回项目摘要、运行/任务元数据与事件高水位 H。
//
// 客户端随后从 /events?after_seq=H 订阅；大内容（媒体、字幕 revision）另取。
func (s *Service) Snapshot(ctx context.Context, projectID string) (domain.Snapshot, error) {
	if projectID == "" {
		return domain.Snapshot{}, invalidRequestf("project_id query parameter required")
	}
	var snapshot domain.Snapshot
	err := s.store.ReadTx(ctx, func(tx *store.Tx) error {
		summary, err := tx.GetProjectSummary(ctx, projectID)
		if err != nil {
			return err
		}
		runs, err := tx.ListProjectRuns(ctx, projectID)
		if err != nil {
			return err
		}
		tasks, err := tx.ListProjectTasks(ctx, projectID)
		if err != nil {
			return err
		}
		seq, err := tx.LatestEventSeq(ctx)
		if err != nil {
			return err
		}
		if runs == nil {
			runs = []domain.WorkflowRun{}
		}
		for i := range runs {
			normalizeRun(&runs[i])
		}
		if tasks == nil {
			tasks = []domain.Task{}
		}
		for i := range tasks {
			normalizeTask(&tasks[i])
		}
		snapshot = domain.Snapshot{
			ProjectID: projectID,
			Project:   summary,
			Runs:      runs,
			Tasks:     tasks,
			EventSeq:  seq,
		}
		return nil
	})
	if err != nil {
		return domain.Snapshot{}, err
	}
	return snapshot, nil
}

// EventCursor 返回仍可回放的最早事件 seq（EVENT_CURSOR_EXPIRED 判定依据）。
func (s *Service) EventCursor(ctx context.Context) (domain.EventCursor, error) {
	earliest, err := s.store.EarliestEventSeq(ctx)
	if err != nil {
		return domain.EventCursor{}, err
	}
	return domain.EventCursor{EarliestSeq: earliest}, nil
}
