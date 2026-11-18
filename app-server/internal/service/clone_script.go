package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// 声音克隆工具一次调用外部 voice-clone.py，脚本自己完成逐句合成并写出成品。
const cloneScriptOut = "out.mp3"

func isCloneScriptTask(task domain.Task) bool {
	if task.Kind != domain.TaskKindNode || task.NodeKey != domain.NodeKeyClone || task.SegmentID != "" {
		return false
	}
	op, _ := task.Params["op"].(string)
	return op == "script"
}

func (s *Service) ensureCloneScriptTask(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	language string,
	revision *domain.SubtitleRevision,
	events *[]domain.DomainEvent,
) error {
	plan, err := s.cloneScriptPlan(ctx, tx, snap, language, revision)
	if err != nil {
		return err
	}
	return s.ensureNodeTask(ctx, tx, snap, domain.NodeKeyClone, language,
		plan.inputIDs, plan.cacheKey, plan.params, events)
}

func (s *Service) cloneScriptOutcomes(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	language string,
	revision *domain.SubtitleRevision,
) ([]domain.NodeStatus, error) {
	plan, err := s.cloneScriptPlan(ctx, tx, snap, language, revision)
	if err != nil {
		if isCloneTimelineFailure(err) {
			outcomes := []domain.NodeStatus{domain.NodeStatusFailed}
			for _, task := range snap.tasks {
				if !isCloneScriptTask(task) || task.Language != language || task.Status.Terminal() {
					continue
				}
				outcomes = append(outcomes, taskNodeStatus(task))
			}
			return outcomes, nil
		}
		return nil, err
	}
	task, ok := currentCloneScriptTask(snap.tasks, language, plan.cacheKey)
	if !ok {
		return []domain.NodeStatus{domain.NodeStatusPending}, nil
	}
	return []domain.NodeStatus{taskNodeStatus(task)}, nil
}

type cloneScriptPlan struct {
	inputIDs []string
	cacheKey string
	params   map[string]any
}

func (s *Service) cloneScriptPlan(
	ctx context.Context,
	tx *store.Tx,
	snap *pipelineSnapshot,
	language string,
	revision *domain.SubtitleRevision,
) (cloneScriptPlan, error) {
	if _, err := s.segmentWorkForRevision(ctx, tx, snap, *revision, language); err != nil {
		return cloneScriptPlan{}, err
	}
	source, err := cloneReferenceAsset(ctx, tx, snap)
	if err != nil {
		return cloneScriptPlan{}, err
	}
	original := sourceTranscriptionRevision(snap)
	if original == nil {
		return cloneScriptPlan{}, invalidRequestf("clone original subtitle is missing")
	}
	revisionDigest := subtitleTextDigest(*revision)
	originalDigest := subtitleTextDigest(*original)
	cacheKey, err := digestOf(struct {
		Language       string
		Revision       string
		RevisionDigest string
		Original       string
		OriginalDigest string
		Source         string
		SourceDigest   string
	}{
		Language:       language,
		Revision:       revision.ID,
		RevisionDigest: revisionDigest,
		Original:       original.ID,
		OriginalDigest: originalDigest,
		Source:         source.ID,
		SourceDigest:   source.SHA256,
	})
	if err != nil {
		return cloneScriptPlan{}, err
	}
	params := map[string]any{
		"op":                    "script",
		"language":              language,
		"out":                   cloneScriptOut,
		"voice_source_asset_id": string(source.ID),
	}
	core := coreTaskParams(params)
	core["subtitle_revision_id"] = revision.ID
	core["subtitle_digest"] = revisionDigest
	core["original_subtitle_revision_id"] = original.ID
	core["original_subtitle_digest"] = originalDigest
	core["script_cache_key"] = cacheKey
	return cloneScriptPlan{
		inputIDs: []string{revision.ID, original.ID, source.ID},
		cacheKey: cacheKey,
		params:   params,
	}, nil
}

func currentCloneScriptTask(tasks []domain.Task, language, cacheKey string) (domain.Task, bool) {
	var found domain.Task
	ok := false
	for _, task := range tasks {
		if !isCloneScriptTask(task) || task.Language != language || taskScriptCacheKey(task) != cacheKey {
			continue
		}
		if !ok || task.UpdatedAt.After(found.UpdatedAt) {
			found = task
			ok = true
		}
	}
	return found, ok
}

func taskScriptCacheKey(task domain.Task) string {
	core, ok := task.Params["_core"].(map[string]any)
	if !ok {
		return ""
	}
	key, _ := core["script_cache_key"].(string)
	return key
}

func subtitleTextDigest(revision domain.SubtitleRevision) string {
	sum := sha256.Sum256([]byte(renderSRT(revision.Segments)))
	return hex.EncodeToString(sum[:])
}
