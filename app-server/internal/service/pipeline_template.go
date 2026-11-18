package service

import (
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

// pipelineTemplate 描述一个 WorkflowRun 的节点子集与前驱。
//
// 模板只决定 Core 创建/推进哪些节点；Task/Attempt/ReviewGate/ArtifactCommit
// 仍是同一套业务对象。未知模板必须拒绝，不能回退到 full_pipeline。
type pipelineTemplate struct {
	ID                      string
	Features                []string
	Nodes                   []domain.PipelineNodeKey
	Predecessors            map[domain.PipelineNodeKey][]domain.PipelineNodeKey
	RequiresTargetLanguages bool
	// SkipReview 覆盖全局 NodeRequiresReview；只对模板显式列出的节点生效。
	SkipReview map[domain.PipelineNodeKey]bool
}

func (t pipelineTemplate) Has(key domain.PipelineNodeKey) bool {
	for _, node := range t.Nodes {
		if node == key {
			return true
		}
	}
	return false
}

// RequiresReview 报告节点在该模板下是否需要人工审核。
func (t pipelineTemplate) RequiresReview(key domain.PipelineNodeKey) bool {
	return domain.NodeRequiresReview(key) && !t.SkipReview[key]
}

// HasReviewNodes 报告模板是否包含任何需要人工审核的节点。
func (t pipelineTemplate) HasReviewNodes() bool {
	for _, node := range t.Nodes {
		if t.RequiresReview(node) {
			return true
		}
	}
	return false
}

// pipelineTemplateFor 返回已注册模板；空 ID 视为 full_pipeline（契约默认）。
func pipelineTemplateFor(id string) (pipelineTemplate, error) {
	switch canonicalTemplateID(id) {
	case templateFullPipeline:
		return fullPipelineTemplate(), nil
	case templateToolExtract:
		return toolExtractTemplate(), nil
	case templateToolSeparate:
		return toolSeparateTemplate(), nil
	case templateToolErase:
		return toolEraseTemplate(), nil
	case templateToolTranslate:
		return toolTranslateTemplate(), nil
	case templateVoiceClone:
		return voiceCloneTemplate(), nil
	default:
		return pipelineTemplate{}, invalidRequestf("unknown workflow template %q", id)
	}
}

const (
	templateFullPipeline   = "full_pipeline"
	templateToolExtract    = "tool_extract"
	templateToolSeparate   = "tool_separate"
	templateToolErase      = "tool_erase"
	templateToolTranslate  = "tool_translate"
	templateVoiceClone     = "tool_voice_clone"
	templateCustomPipeline = "custom_pipeline"
)

// 专属链路功能枚举（contracts/openapi.yaml CustomPipelineFeature）。
const (
	featureSeparation = "separation"
	featureErase      = "erase"
	featureTranslate  = "translate"
	featureClone      = "clone"
	featureMix        = "mix"
)

func canonicalTemplateID(id string) string {
	if id == "" {
		return templateFullPipeline
	}
	return id
}

func fullPipelineTemplate() pipelineTemplate {
	nodes := []domain.PipelineNodeKey{
		domain.NodeKeyImport, domain.NodeKeyAnalyze, domain.NodeKeySeparate,
		domain.NodeKeyExtract, domain.NodeKeyErase, domain.NodeKeyTranslate,
		domain.NodeKeyClone, domain.NodeKeyMix,
	}
	return pipelineTemplate{
		ID:    templateFullPipeline,
		Nodes: nodes,
		Predecessors: map[domain.PipelineNodeKey][]domain.PipelineNodeKey{
			domain.NodeKeyAnalyze:   {domain.NodeKeyImport},
			domain.NodeKeySeparate:  {domain.NodeKeyAnalyze},
			domain.NodeKeyExtract:   {domain.NodeKeySeparate},
			domain.NodeKeyErase:     {domain.NodeKeyExtract},
			domain.NodeKeyTranslate: {domain.NodeKeyErase},
			domain.NodeKeyClone:     {domain.NodeKeyTranslate},
			domain.NodeKeyMix:       {domain.NodeKeyClone},
		},
		RequiresTargetLanguages: true,
	}
}

// toolExtractTemplate 是字幕提取工具的最小 Run：导入 → 分析 → 提取。
// 不启用分离/擦除/翻译/克隆/混音，因此 04 的前驱是 02，不是 03。
func toolExtractTemplate() pipelineTemplate {
	return pipelineTemplate{
		ID:    templateToolExtract,
		Nodes: []domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeyAnalyze, domain.NodeKeyExtract},
		Predecessors: map[domain.PipelineNodeKey][]domain.PipelineNodeKey{
			domain.NodeKeyAnalyze: {domain.NodeKeyImport},
			domain.NodeKeyExtract: {domain.NodeKeyAnalyze},
		},
		RequiresTargetLanguages: false,
	}
}

// toolSeparateTemplate 是音频分离工具的最小 Run：导入 → 分离。
// 分离与语言无关，Run 仍保留非空 source_language（UI 使用 und）但不要求目标语言。
func toolSeparateTemplate() pipelineTemplate {
	return pipelineTemplate{
		ID:    templateToolSeparate,
		Nodes: []domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeySeparate},
		Predecessors: map[domain.PipelineNodeKey][]domain.PipelineNodeKey{
			domain.NodeKeySeparate: {domain.NodeKeyImport},
		},
		RequiresTargetLanguages: false,
	}
}

// toolEraseTemplate 是字幕擦除工具的最小 Run：导入 → OCR 定位 → 擦除。
// source_language 使用所选 OCR code；erase_mode 由 RunConfig 冻结后写入 05 Task params。
func toolEraseTemplate() pipelineTemplate {
	return pipelineTemplate{
		ID:    templateToolErase,
		Nodes: []domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeyExtract, domain.NodeKeyErase},
		Predecessors: map[domain.PipelineNodeKey][]domain.PipelineNodeKey{
			domain.NodeKeyExtract: {domain.NodeKeyImport},
			domain.NodeKeyErase:   {domain.NodeKeyExtract},
		},
		RequiresTargetLanguages: false,
	}
}

// toolTranslateTemplate 是字幕翻译工具的最小 Run：导入 SRT Asset → 逐目标语言翻译。
// 06 在工具模板中不要求人工审核；full_pipeline 的 06/08 审核语义不变。
func toolTranslateTemplate() pipelineTemplate {
	return pipelineTemplate{
		ID:    templateToolTranslate,
		Nodes: []domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeyTranslate},
		Predecessors: map[domain.PipelineNodeKey][]domain.PipelineNodeKey{
			domain.NodeKeyTranslate: {domain.NodeKeyImport},
		},
		RequiresTargetLanguages: true,
		SkipReview:              map[domain.PipelineNodeKey]bool{domain.NodeKeyTranslate: true},
	}
}

// voiceCloneTemplate 是声音克隆工具的最小 Run：导入声源 → 一次脚本克隆出成品。
// 原文字幕/译文字幕由导入端点预先建轨；不跑 06 翻译或 08 混音，也不再另做音轨合并。
func voiceCloneTemplate() pipelineTemplate {
	return pipelineTemplate{
		ID:    templateVoiceClone,
		Nodes: []domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeyClone},
		Predecessors: map[domain.PipelineNodeKey][]domain.PipelineNodeKey{
			domain.NodeKeyClone: {domain.NodeKeyImport},
		},
		RequiresTargetLanguages: true,
	}
}

// pipelineTemplateForInput 由请求解析模板；custom_pipeline 需要 features 生成固定闭包，
// 其他模板携带 features 视为契约冲突，避免同一输入有两种含义。
func pipelineTemplateForInput(in CreateWorkflowRunInput) (pipelineTemplate, error) {
	if canonicalTemplateID(in.template()) == templateCustomPipeline {
		features, err := normalizeCustomFeatures(in.Features)
		if err != nil {
			return pipelineTemplate{}, err
		}
		return customPipelineTemplateFromFeatures(features), nil
	}
	if len(in.Features) > 0 {
		return pipelineTemplate{}, invalidRequestf("features are only supported by %s", templateCustomPipeline)
	}
	return pipelineTemplateFor(in.template())
}

// pipelineTemplateForRun 由已持久化 Run 重建模板。custom_pipeline 的节点集合
// 在创建时已冻结，恢复/推进只认 run.Nodes，不因后续字幕轨变化而漂移。
func pipelineTemplateForRun(run domain.WorkflowRun) (pipelineTemplate, error) {
	if canonicalTemplateID(run.Template) == templateCustomPipeline {
		return customPipelineTemplateFromNodes(run.Nodes), nil
	}
	return pipelineTemplateFor(run.Template)
}

// normalizeCustomFeatures 校验并去重功能；顺序按契约固定，避免输入顺序影响计划摘要。
func normalizeCustomFeatures(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, invalidRequestf("features required for %s", templateCustomPipeline)
	}
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		switch value {
		case featureSeparation, featureErase, featureTranslate, featureClone, featureMix:
		default:
			return nil, invalidRequestf("unknown custom feature %q", value)
		}
		if _, ok := seen[value]; ok {
			return nil, invalidRequestf("duplicate custom feature %q", value)
		}
		seen[value] = struct{}{}
	}
	features := make([]string, 0, len(raw))
	for _, value := range []string{featureSeparation, featureErase, featureTranslate, featureClone, featureMix} {
		if _, ok := seen[value]; ok {
			features = append(features, value)
		}
	}
	return features, nil
}

func hasCustomFeature(features []string, want string) bool {
	for _, value := range features {
		if value == want {
			return true
		}
	}
	return false
}

func customFeaturesFromNodes(nodes []domain.PipelineNodeState) []string {
	present := make(map[domain.PipelineNodeKey]bool, len(nodes))
	for _, node := range nodes {
		present[node.Key] = true
	}
	features := make([]string, 0, 5)
	if present[domain.NodeKeySeparate] {
		features = append(features, featureSeparation)
	}
	if present[domain.NodeKeyErase] {
		features = append(features, featureErase)
	}
	if present[domain.NodeKeyTranslate] {
		features = append(features, featureTranslate)
	}
	if present[domain.NodeKeyClone] {
		features = append(features, featureClone)
	}
	if present[domain.NodeKeyMix] {
		features = append(features, featureMix)
	}
	return features
}

// customPipelineTemplateFromFeatures 生成固定闭包：功能只决定节点是否加入，
// 不决定节点顺序或审核门槛。04/06 是否真正执行由预检按导入轨决定。
func customPipelineTemplateFromFeatures(features []string) pipelineTemplate {
	want := map[domain.PipelineNodeKey]bool{
		domain.NodeKeyImport: true,
	}
	if hasCustomFeature(features, featureSeparation) {
		want[domain.NodeKeySeparate] = true
	}
	needsText := hasCustomFeature(features, featureTranslate) ||
		hasCustomFeature(features, featureClone) || hasCustomFeature(features, featureMix)
	if hasCustomFeature(features, featureErase) || needsText {
		want[domain.NodeKeyExtract] = true
	}
	if hasCustomFeature(features, featureErase) {
		want[domain.NodeKeyErase] = true
	}
	if needsText {
		want[domain.NodeKeyTranslate] = true
	}
	if hasCustomFeature(features, featureClone) {
		want[domain.NodeKeyClone] = true
	}
	if hasCustomFeature(features, featureMix) {
		want[domain.NodeKeyMix] = true
	}
	return buildCustomTemplate(want)
}

// customPipelineTemplateFromNodes 与 features 版本使用同一前驱规则；
// 节点集合来自 Run 持久化，未知/缺失节点按存在关系推导。
func customPipelineTemplateFromNodes(nodes []domain.PipelineNodeState) pipelineTemplate {
	want := make(map[domain.PipelineNodeKey]bool, len(nodes))
	for _, node := range nodes {
		want[node.Key] = true
	}
	// 01 是 Run 的物理起点；即使旧数据缺失也补上，避免无前驱节点永久 pending。
	want[domain.NodeKeyImport] = true
	return buildCustomTemplate(want)
}

func buildCustomTemplate(want map[domain.PipelineNodeKey]bool) pipelineTemplate {
	order := []domain.PipelineNodeKey{
		domain.NodeKeyImport, domain.NodeKeyAnalyze, domain.NodeKeySeparate,
		domain.NodeKeyExtract, domain.NodeKeyErase, domain.NodeKeyTranslate,
		domain.NodeKeyClone, domain.NodeKeyMix,
	}
	nodes := make([]domain.PipelineNodeKey, 0, len(want))
	for _, key := range order {
		if want[key] {
			nodes = append(nodes, key)
		}
	}
	predecessors := make(map[domain.PipelineNodeKey][]domain.PipelineNodeKey)
	set := func(node domain.PipelineNodeKey, preds ...domain.PipelineNodeKey) {
		if want[node] {
			predecessors[node] = preds
		}
	}
	separate := want[domain.NodeKeySeparate]
	extract := want[domain.NodeKeyExtract]
	erase := want[domain.NodeKeyErase]
	translate := want[domain.NodeKeyTranslate]
	clone := want[domain.NodeKeyClone]
	mix := want[domain.NodeKeyMix]

	if separate {
		set(domain.NodeKeySeparate, domain.NodeKeyImport)
	}
	if extract {
		if separate {
			set(domain.NodeKeyExtract, domain.NodeKeySeparate)
		} else {
			set(domain.NodeKeyExtract, domain.NodeKeyImport)
		}
	}
	if erase {
		if extract {
			set(domain.NodeKeyErase, domain.NodeKeyExtract)
		} else {
			set(domain.NodeKeyErase, domain.NodeKeyImport)
		}
	}
	if translate {
		if extract {
			set(domain.NodeKeyTranslate, domain.NodeKeyExtract)
		} else if separate {
			set(domain.NodeKeyTranslate, domain.NodeKeySeparate)
		} else {
			set(domain.NodeKeyTranslate, domain.NodeKeyImport)
		}
	}
	if clone {
		if translate {
			set(domain.NodeKeyClone, domain.NodeKeyTranslate)
		} else {
			set(domain.NodeKeyClone, domain.NodeKeyImport)
		}
	}
	if mix {
		preds := make([]domain.PipelineNodeKey, 0, 2)
		if clone {
			preds = append(preds, domain.NodeKeyClone)
		} else if translate {
			preds = append(preds, domain.NodeKeyTranslate)
		}
		if erase {
			preds = append(preds, domain.NodeKeyErase)
		}
		if len(preds) == 0 {
			preds = append(preds, domain.NodeKeyImport)
		}
		set(domain.NodeKeyMix, preds...)
	}
	return pipelineTemplate{
		ID:                      templateCustomPipeline,
		Features:                customFeaturesFromNodesFromWant(want),
		Nodes:                   nodes,
		Predecessors:            predecessors,
		RequiresTargetLanguages: false,
	}
}

func customFeaturesFromNodesFromWant(want map[domain.PipelineNodeKey]bool) []string {
	nodes := make([]domain.PipelineNodeState, 0, len(want))
	for key := range want {
		nodes = append(nodes, domain.PipelineNodeState{Key: key})
	}
	return customFeaturesFromNodes(nodes)
}

func predecessorsDone(tpl pipelineTemplate, run *domain.WorkflowRun, node domain.PipelineNodeKey) bool {
	for _, predecessor := range tpl.Predecessors[node] {
		if !nodeCompleted(run, predecessor) {
			return false
		}
	}
	return true
}
