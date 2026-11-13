package service

import (
	"context"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

// reviewRunProjection 缓存同一 Run 的只读事实：每个 Run 只读取一次运行与字幕轨，
// 各语种最新混音任务按需读取并缓存，避免每个 gate 重复加载整项目。
type reviewRunProjection struct {
	run    domain.WorkflowRun
	tracks []domain.SubtitleTrack
	mixes  map[string]reviewMixLookup
}

type reviewMixLookup struct {
	task domain.Task
	ok   bool
}

// attachReviewCurrentBindings 在同一只读事务内为每个 gate 派生 current_bindings。
//
// 投影只读、不持久化：空切片表示当前无匹配来源；存储错误原样传播，不能伪造
// “当前”状态。旧空 language 门槛按精确摘要解析 Run 目标语种，不做前缀兼容。
func attachReviewCurrentBindings(ctx context.Context, tx *store.Tx, gates []domain.ReviewGate) error {
	projections := make(map[domain.ID]*reviewRunProjection)
	for i := range gates {
		projection, ok := projections[gates[i].RunID]
		if !ok {
			run, err := tx.GetWorkflowRun(ctx, gates[i].RunID)
			if err != nil {
				return err
			}
			tracks, err := tx.ListProjectTracks(ctx, run.ProjectID)
			if err != nil {
				return err
			}
			projection = &reviewRunProjection{
				run:    run,
				tracks: tracks,
				mixes:  make(map[string]reviewMixLookup),
			}
			projections[gates[i].RunID] = projection
		}
		bindings, err := projection.bindings(ctx, tx, gates[i])
		if err != nil {
			return err
		}
		gates[i].CurrentBindings = bindings
	}
	return nil
}

// gateCurrentBindings 在写事务内按同一投影规则重算单个 gate 的当前绑定，
// 供可选 current_binding 守卫在批准前复核。
func gateCurrentBindings(ctx context.Context, tx *store.Tx, run domain.WorkflowRun, gate domain.ReviewGate) ([]domain.ReviewCurrentBinding, error) {
	tracks, err := tx.ListProjectTracks(ctx, run.ProjectID)
	if err != nil {
		return nil, err
	}
	projection := &reviewRunProjection{
		run:    run,
		tracks: tracks,
		mixes:  make(map[string]reviewMixLookup),
	}
	return projection.bindings(ctx, tx, gate)
}

// bindings 返回 gate 在当前持久事实下的精确绑定；非空 language 只查该语种，
// 旧空 language 门槛按 Run 目标语种逐个用摘要精确匹配。
func (p *reviewRunProjection) bindings(ctx context.Context, tx *store.Tx, gate domain.ReviewGate) ([]domain.ReviewCurrentBinding, error) {
	languages := p.run.Languages
	if gate.Language != "" {
		languages = []string{gate.Language}
	}
	bindings := make([]domain.ReviewCurrentBinding, 0)
	seen := make(map[domain.ReviewCurrentBinding]struct{})
	add := func(binding domain.ReviewCurrentBinding) {
		if _, ok := seen[binding]; ok {
			return
		}
		seen[binding] = struct{}{}
		bindings = append(bindings, binding)
	}
	switch gate.Type {
	case domain.ReviewTypeTranslation:
		for _, language := range languages {
			binding, ok, err := p.translationRevisionBinding(gate, language)
			if err != nil {
				return nil, err
			}
			if ok {
				add(binding)
			}
		}
	case domain.ReviewTypeDelivery:
		for _, language := range languages {
			binding, ok, err := p.mixBinding(ctx, tx, gate, language)
			if err != nil {
				return nil, err
			}
			if ok {
				add(binding)
			}
		}
		for _, language := range languages {
			deliveryBindings, err := p.deliveryRevisionBindings(gate, language)
			if err != nil {
				return nil, err
			}
			for _, binding := range deliveryBindings {
				add(binding)
			}
		}
	}
	return bindings, nil
}

// translationRevisionBinding 绑定译文轨当前 revision 的原始 RevisionBindingDigest
// （06 门槛口径），不做旧“摘要:语言”前缀兼容。
func (p *reviewRunProjection) translationRevisionBinding(gate domain.ReviewGate, language string) (domain.ReviewCurrentBinding, bool, error) {
	if language == "" {
		return domain.ReviewCurrentBinding{}, false, nil
	}
	track := pickPipelineTrack(p.tracks, language, domain.TrackTypeTranslation)
	if track == nil {
		return domain.ReviewCurrentBinding{}, false, nil
	}
	revision := currentRevision(*track)
	if revision == nil {
		return domain.ReviewCurrentBinding{}, false, nil
	}
	digest, err := domain.RevisionBindingDigest(*revision)
	if err != nil {
		return domain.ReviewCurrentBinding{}, false, err
	}
	if digest != gate.RevisionDigest {
		return domain.ReviewCurrentBinding{}, false, nil
	}
	return domain.ReviewCurrentBinding{
		Language:   language,
		SourceKind: domain.ReviewBindingTranslationRevision,
		SourceID:   revision.ID,
	}, true, nil
}

// mixBinding 绑定按既有导出口径选取的最新成功 08 任务（同语种优先、
// 旧空 language 单语回退）的 NodeReviewDigest；任务冻结在旧 revision 时
// 不再投影为当前交付绑定。轨道选择复用导出门禁的 pickExportSubtitleTrack，
// 保证 current_bindings 与导出放行对“当前交付 revision”同口径。
func (p *reviewRunProjection) mixBinding(ctx context.Context, tx *store.Tx, gate domain.ReviewGate, language string) (domain.ReviewCurrentBinding, bool, error) {
	if language == "" {
		return domain.ReviewCurrentBinding{}, false, nil
	}
	task, ok, err := p.latestMix(ctx, tx, language)
	if err != nil || !ok {
		return domain.ReviewCurrentBinding{}, false, err
	}
	var revision *domain.SubtitleRevision
	if track := pickExportSubtitleTrack(p.tracks, language); track != nil {
		revision = currentRevision(*track)
	}
	if !mixTaskMatchesRevision(task, revision) {
		return domain.ReviewCurrentBinding{}, false, nil
	}
	digest, err := domain.NodeReviewDigest(task.RunID, task.NodeKey, task.Language,
		task.ID, task.ArtifactIDs, task.InputDigest)
	if err != nil {
		return domain.ReviewCurrentBinding{}, false, err
	}
	if digest != gate.RevisionDigest {
		return domain.ReviewCurrentBinding{}, false, nil
	}
	return domain.ReviewCurrentBinding{
		Language:   language,
		SourceKind: domain.ReviewBindingDeliveryMix,
		SourceID:   task.ID,
	}, true, nil
}

func (p *reviewRunProjection) latestMix(ctx context.Context, tx *store.Tx, language string) (domain.Task, bool, error) {
	if lookup, ok := p.mixes[language]; ok {
		return lookup.task, lookup.ok, nil
	}
	task, ok, err := tx.LatestSucceededNodeTask(ctx, p.run.ID, domain.NodeKeyMix, language)
	if err != nil {
		return domain.Task{}, false, err
	}
	p.mixes[language] = reviewMixLookup{task: task, ok: ok}
	return task, ok, nil
}

// deliveryRevisionBindings 遍历 Run 该目标语种下每条译文轨的当前 revision，
// 按 DeliveryBind 精确匹配。显式字幕导出可指定同项目同语种的任意当前 revision，
// 因此不能只取 pickPipelineTrack 的一条最新轨，否则其他当前轨的合法门槛会漏投影。
// 其他轨道的交付门槛（如转录轨后缀）保持历史/未知。
func (p *reviewRunProjection) deliveryRevisionBindings(gate domain.ReviewGate, language string) ([]domain.ReviewCurrentBinding, error) {
	if language == "" {
		return nil, nil
	}
	bindings := make([]domain.ReviewCurrentBinding, 0)
	for i := range p.tracks {
		track := &p.tracks[i]
		if track.Language != language || track.Type != domain.TrackTypeTranslation {
			continue
		}
		revision := currentRevision(*track)
		if revision == nil {
			continue
		}
		digest, err := domain.RevisionBindingDigest(*revision)
		if err != nil {
			return nil, err
		}
		if domain.DeliveryBind(digest) != gate.RevisionDigest {
			continue
		}
		bindings = append(bindings, domain.ReviewCurrentBinding{
			Language:   language,
			SourceKind: domain.ReviewBindingDeliveryRevision,
			SourceID:   revision.ID,
		})
	}
	return bindings, nil
}

// validateReviewCurrentBinding 校验新客户端批准请求的显式绑定形状；
// 合法但已不是当前 tuple 的情况由批准事务返回 REVISION_CONFLICT。
func validateReviewCurrentBinding(binding domain.ReviewCurrentBinding) error {
	if binding.Language == "" {
		return invalidRequestf("current_binding.language required")
	}
	if !binding.SourceKind.Valid() {
		return invalidRequestf("invalid current_binding.source_kind %q", binding.SourceKind)
	}
	if binding.SourceID == "" {
		return invalidRequestf("current_binding.source_id required")
	}
	return nil
}

func currentBindingMatches(bindings []domain.ReviewCurrentBinding, want domain.ReviewCurrentBinding) bool {
	for _, binding := range bindings {
		if binding == want {
			return true
		}
	}
	return false
}
