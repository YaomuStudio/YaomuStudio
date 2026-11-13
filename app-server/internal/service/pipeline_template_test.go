package service

import (
	"errors"
	"testing"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
)

func TestPipelineTemplateRegistry(t *testing.T) {
	full, err := pipelineTemplateFor("")
	if err != nil {
		t.Fatalf("default template: %v", err)
	}
	if full.ID != templateFullPipeline || len(full.Nodes) != 8 || !full.RequiresTargetLanguages {
		t.Fatalf("full template = %+v", full)
	}
	tool, err := pipelineTemplateFor(templateToolExtract)
	if err != nil {
		t.Fatalf("tool_extract: %v", err)
	}
	if tool.ID != templateToolExtract || len(tool.Nodes) != 3 || tool.RequiresTargetLanguages {
		t.Fatalf("tool template = %+v", tool)
	}
	for _, key := range []domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeyAnalyze, domain.NodeKeyExtract} {
		if !tool.Has(key) {
			t.Errorf("tool_extract missing node %s", key)
		}
	}
	if _, err := pipelineTemplateFor("unknown_tool"); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("unknown template error = %v", err)
	}
	separate, err := pipelineTemplateFor(templateToolSeparate)
	if err != nil {
		t.Fatalf("tool_separate: %v", err)
	}
	if separate.ID != templateToolSeparate || len(separate.Nodes) != 2 || separate.RequiresTargetLanguages {
		t.Fatalf("separate template = %+v", separate)
	}
	for _, key := range []domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeySeparate} {
		if !separate.Has(key) {
			t.Errorf("tool_separate missing node %s", key)
		}
	}
	if separate.Has(domain.NodeKeyAnalyze) || separate.Has(domain.NodeKeyExtract) {
		t.Fatalf("tool_separate unexpectedly enables analyze/extract: %+v", separate.Nodes)
	}
	erase, err := pipelineTemplateFor(templateToolErase)
	if err != nil {
		t.Fatalf("tool_erase: %v", err)
	}
	if erase.ID != templateToolErase || len(erase.Nodes) != 3 || erase.RequiresTargetLanguages {
		t.Fatalf("erase template = %+v", erase)
	}
	for _, key := range []domain.PipelineNodeKey{
		domain.NodeKeyImport, domain.NodeKeyExtract, domain.NodeKeyErase,
	} {
		if !erase.Has(key) {
			t.Errorf("tool_erase missing node %s", key)
		}
	}
	if erase.Has(domain.NodeKeyAnalyze) || erase.Has(domain.NodeKeySeparate) || erase.Has(domain.NodeKeyMix) {
		t.Fatalf("tool_erase unexpectedly enables other nodes: %+v", erase.Nodes)
	}
	translate, err := pipelineTemplateFor(templateToolTranslate)
	if err != nil {
		t.Fatalf("tool_translate: %v", err)
	}
	if translate.ID != templateToolTranslate || len(translate.Nodes) != 2 ||
		!translate.RequiresTargetLanguages {
		t.Fatalf("translate template = %+v", translate)
	}
	for _, key := range []domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeyTranslate} {
		if !translate.Has(key) {
			t.Errorf("tool_translate missing node %s", key)
		}
	}
	if translate.RequiresReview(domain.NodeKeyTranslate) {
		t.Fatalf("tool_translate should skip 06 review")
	}
	if !full.RequiresReview(domain.NodeKeyTranslate) || !full.RequiresReview(domain.NodeKeyMix) {
		t.Fatalf("full_pipeline review semantics changed")
	}
	clone, err := pipelineTemplateFor(templateVoiceClone)
	if err != nil {
		t.Fatalf("voice_clone: %v", err)
	}
	if clone.ID != templateVoiceClone || len(clone.Nodes) != 2 || !clone.RequiresTargetLanguages {
		t.Fatalf("voice_clone template = %+v", clone)
	}
	for _, key := range []domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeyClone} {
		if !clone.Has(key) {
			t.Errorf("voice_clone missing node %s", key)
		}
	}
}

func TestValidateRunInputTargetLanguagesByTemplate(t *testing.T) {
	base := CreateWorkflowRunInput{
		ProjectID:        "project-1",
		Name:             "工具提取",
		SourceLanguage:   "en",
		InputRevisionIDs: []string{"asset-1"},
		Extract:          &domain.ExtractOptions{Mode: domain.ExtractModeASR},
	}
	tool := base
	tool.Template = templateToolExtract
	tool.TargetLanguages = nil
	if err := validateRunInput(tool); err != nil {
		t.Fatalf("tool_extract with empty languages: %v", err)
	}

	full := base
	full.Template = templateFullPipeline
	full.TargetLanguages = nil
	if err := validateRunInput(full); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("full_pipeline empty languages error = %v", err)
	}

	unknown := base
	unknown.Template = "unknown_tool"
	unknown.TargetLanguages = []string{"zh"}
	if err := validateRunInput(unknown); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("unknown template error = %v", err)
	}

	noExtract := tool
	noExtract.Extract = nil
	if err := validateRunInput(noExtract); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("tool_extract without extract error = %v", err)
	}

	noInputs := tool
	noInputs.InputRevisionIDs = nil
	if err := validateRunInput(noInputs); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("tool_extract without inputs error = %v", err)
	}
}

func TestValidateRunInputToolSeparate(t *testing.T) {
	base := CreateWorkflowRunInput{
		ProjectID:        "project-1",
		Name:             "分离",
		Template:         templateToolSeparate,
		SourceLanguage:   "und",
		TargetLanguages:  nil,
		InputRevisionIDs: []string{"asset-1"},
	}
	if err := validateRunInput(base); err != nil {
		t.Fatalf("tool_separate with und/no targets: %v", err)
	}
	noInputs := base
	noInputs.InputRevisionIDs = nil
	if err := validateRunInput(noInputs); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("tool_separate without inputs error = %v", err)
	}
	noLanguage := base
	noLanguage.SourceLanguage = ""
	if err := validateRunInput(noLanguage); err != nil {
		t.Fatalf("tool_separate without source_language should be accepted (auto): %v", err)
	}
	reuse := base
	reuse.ReuseAudioTrackID = "asset-audio"
	if err := validateRunInput(reuse); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("tool_separate with reuse audio error = %v", err)
	}
	reuseSubtitle := base
	reuseSubtitle.ReuseSubtitleTrackID = "track-subtitle"
	if err := validateRunInput(reuseSubtitle); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("tool_separate with reuse subtitle error = %v", err)
	}
}

func TestToolSeparateRequiredSlots(t *testing.T) {
	slots := slotsForTest(t, CreateWorkflowRunInput{Template: templateToolSeparate})
	assertSlots(t, slots, domain.CapabilitySlotFFmpeg, domain.CapabilitySlotSeparation)
}

func TestValidateRunInputToolErase(t *testing.T) {
	base := CreateWorkflowRunInput{
		ProjectID:        "project-1",
		Name:             "擦除",
		Template:         templateToolErase,
		SourceLanguage:   "chinese",
		TargetLanguages:  nil,
		InputRevisionIDs: []string{"asset-1"},
		Extract:          &domain.ExtractOptions{Mode: domain.ExtractModeOCR, OCRLanguage: domain.OCRLanguageChinese},
		EraseMode:        domain.EraseModeDialogOnly,
	}
	if err := validateRunInput(base); err != nil {
		t.Fatalf("tool_erase valid input: %v", err)
	}
	noInputs := base
	noInputs.InputRevisionIDs = nil
	if err := validateRunInput(noInputs); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("tool_erase without inputs error = %v", err)
	}
	noExtract := base
	noExtract.Extract = nil
	if err := validateRunInput(noExtract); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("tool_erase without extract error = %v", err)
	}
	asr := base
	asr.Extract = &domain.ExtractOptions{Mode: domain.ExtractModeASR}
	if err := validateRunInput(asr); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("tool_erase with ASR extract error = %v", err)
	}
	noEraseMode := base
	noEraseMode.EraseMode = ""
	if err := validateRunInput(noEraseMode); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("tool_erase without erase_mode error = %v", err)
	}
	reuseAudio := base
	reuseAudio.ReuseAudioTrackID = "asset-audio"
	if err := validateRunInput(reuseAudio); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("tool_erase with reuse audio error = %v", err)
	}
	reuseSubtitle := base
	reuseSubtitle.ReuseSubtitleTrackID = "track-subtitle"
	if err := validateRunInput(reuseSubtitle); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("tool_erase with reuse subtitle error = %v", err)
	}
}

func TestToolEraseRequiredSlots(t *testing.T) {
	slots := slotsForTest(t, CreateWorkflowRunInput{Template: templateToolErase})
	assertSlots(t, slots,
		domain.CapabilitySlotFFmpeg,
		domain.CapabilitySlotOCR,
		domain.CapabilitySlotSubtitleErase,
	)
}

func TestValidateRunInputToolTranslate(t *testing.T) {
	base := CreateWorkflowRunInput{
		ProjectID:        "project-1",
		Name:             "翻译",
		Template:         templateToolTranslate,
		SourceLanguage:   "en",
		TargetLanguages:  []string{"zh"},
		InputRevisionIDs: []string{"asset-subtitle"},
	}
	if err := validateRunInput(base); err != nil {
		t.Fatalf("tool_translate valid input: %v", err)
	}
	noInputs := base
	noInputs.InputRevisionIDs = nil
	if err := validateRunInput(noInputs); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("tool_translate without inputs error = %v", err)
	}
	noTargets := base
	noTargets.TargetLanguages = nil
	if err := validateRunInput(noTargets); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("tool_translate without targets error = %v", err)
	}
}

func TestToolTranslateRequiredSlots(t *testing.T) {
	slots := slotsForTest(t, CreateWorkflowRunInput{Template: templateToolTranslate})
	assertSlots(t, slots, domain.CapabilitySlotTranslate)
}

func TestValidateRunInputVoiceClone(t *testing.T) {
	base := CreateWorkflowRunInput{
		ProjectID:        "project-1",
		Name:             "克隆",
		Template:         templateVoiceClone,
		SourceLanguage:   "en",
		TargetLanguages:  []string{"zh"},
		InputRevisionIDs: []string{"asset-audio"},
	}
	if err := validateRunInput(base); err != nil {
		t.Fatalf("voice_clone valid input: %v", err)
	}
	noInputs := base
	noInputs.InputRevisionIDs = nil
	if err := validateRunInput(noInputs); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("voice_clone without inputs error = %v", err)
	}
	multipleInputs := base
	multipleInputs.InputRevisionIDs = []string{"asset-a", "asset-b"}
	if err := validateRunInput(multipleInputs); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("voice_clone with multiple inputs error = %v", err)
	}
}

func TestVoiceCloneRequiredSlots(t *testing.T) {
	slots := slotsForTest(t, CreateWorkflowRunInput{Template: templateVoiceClone})
	assertSlots(t, slots, domain.CapabilitySlotVoiceClone)
}

func TestToolExtractRequiredSlots(t *testing.T) {
	t.Run("asr", func(t *testing.T) {
		slots := slotsForTest(t, CreateWorkflowRunInput{
			Template: templateToolExtract,
			Extract:  &domain.ExtractOptions{Mode: domain.ExtractModeASR},
		})
		assertSlots(t, slots, domain.CapabilitySlotFFmpeg, domain.CapabilitySlotASR)
	})
	t.Run("ocr", func(t *testing.T) {
		slots := slotsForTest(t, CreateWorkflowRunInput{
			Template: templateToolExtract,
			Extract:  &domain.ExtractOptions{Mode: domain.ExtractModeOCR, OCRLanguage: domain.OCRLanguageChinese},
		})
		assertSlots(t, slots, domain.CapabilitySlotFFmpeg, domain.CapabilitySlotOCR)
	})
	t.Run("asr_ocr", func(t *testing.T) {
		slots := slotsForTest(t, CreateWorkflowRunInput{
			Template: templateToolExtract,
			Extract:  &domain.ExtractOptions{Mode: domain.ExtractModeASROCR, OCRLanguage: domain.OCRLanguageEnglish},
		})
		assertSlots(t, slots, domain.CapabilitySlotFFmpeg, domain.CapabilitySlotASR, domain.CapabilitySlotOCR)
	})
}

func slotsForTest(t *testing.T, in CreateWorkflowRunInput) []domain.CapabilitySlotKey {
	t.Helper()
	tpl, err := pipelineTemplateForInput(in)
	if err != nil {
		t.Fatalf("pipelineTemplateForInput: %v", err)
	}
	return requiredSlots(in, tpl, tpl.Has(domain.NodeKeyExtract), tpl.Has(domain.NodeKeyTranslate))
}

func assertSlots(t *testing.T, got []domain.CapabilitySlotKey, want ...domain.CapabilitySlotKey) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("slots = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("slots = %v, want %v", got, want)
		}
	}
}

func TestCustomPipelineClosure(t *testing.T) {
	cases := []struct {
		name      string
		features  []string
		nodes     []domain.PipelineNodeKey
		features2 []string
	}{
		{"separation", []string{featureSeparation},
			[]domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeySeparate}, []string{featureSeparation}},
		{"erase", []string{featureErase},
			[]domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeyExtract, domain.NodeKeyErase}, []string{featureErase}},
		{"translate", []string{featureTranslate},
			[]domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeyExtract, domain.NodeKeyTranslate}, []string{featureTranslate}},
		{"clone", []string{featureClone},
			[]domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeyExtract, domain.NodeKeyTranslate, domain.NodeKeyClone},
			[]string{featureTranslate, featureClone}},
		{"mix", []string{featureMix},
			[]domain.PipelineNodeKey{domain.NodeKeyImport, domain.NodeKeyExtract, domain.NodeKeyTranslate, domain.NodeKeyMix},
			[]string{featureTranslate, featureMix}},
		{"all", []string{featureMix, featureClone, featureTranslate, featureErase, featureSeparation},
			[]domain.PipelineNodeKey{
				domain.NodeKeyImport, domain.NodeKeySeparate, domain.NodeKeyExtract,
				domain.NodeKeyErase, domain.NodeKeyTranslate, domain.NodeKeyClone, domain.NodeKeyMix,
			},
			[]string{featureSeparation, featureErase, featureTranslate, featureClone, featureMix}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tpl, err := pipelineTemplateForInput(CreateWorkflowRunInput{
				Template: templateCustomPipeline,
				Features: tc.features,
			})
			if err != nil {
				t.Fatalf("pipelineTemplateForInput: %v", err)
			}
			if tpl.ID != templateCustomPipeline || len(tpl.Nodes) != len(tc.nodes) {
				t.Fatalf("template = %+v", tpl)
			}
			for i, key := range tc.nodes {
				if tpl.Nodes[i] != key {
					t.Fatalf("node[%d] = %s, want %s", i, tpl.Nodes[i], key)
				}
			}
			if len(tpl.Features) != len(tc.features2) {
				t.Fatalf("features = %v", tpl.Features)
			}
			for i, feature := range tc.features2 {
				if tpl.Features[i] != feature {
					t.Fatalf("feature[%d] = %s, want %s", i, tpl.Features[i], feature)
				}
			}
			// 重建必须与创建时一致。
			nodes := make([]domain.PipelineNodeState, 0, len(tpl.Nodes))
			for _, key := range tpl.Nodes {
				nodes = append(nodes, domain.PipelineNodeState{Key: key})
			}
			rebuilt, err := pipelineTemplateForRun(domain.WorkflowRun{Template: templateCustomPipeline, Nodes: nodes})
			if err != nil {
				t.Fatalf("pipelineTemplateForRun: %v", err)
			}
			if len(rebuilt.Nodes) != len(tpl.Nodes) {
				t.Fatalf("rebuilt nodes = %v", rebuilt.Nodes)
			}
			for i, key := range tpl.Nodes {
				if rebuilt.Nodes[i] != key {
					t.Fatalf("rebuilt node[%d] = %s, want %s", i, rebuilt.Nodes[i], key)
				}
				if len(rebuilt.Predecessors[key]) != len(tpl.Predecessors[key]) {
					t.Fatalf("predecessors[%s] = %v, want %v", key, rebuilt.Predecessors[key], tpl.Predecessors[key])
				}
			}
		})
	}
}

func TestCustomPipelineValidation(t *testing.T) {
	base := CreateWorkflowRunInput{
		ProjectID:        "project",
		Name:             "custom",
		Template:         templateCustomPipeline,
		Features:         []string{featureTranslate},
		SourceLanguage:   "en",
		TargetLanguages:  []string{"zh"},
		InputRevisionIDs: []string{"asset"},
	}
	if err := validateRunInput(base); err != nil {
		t.Fatalf("valid custom input: %v", err)
	}
	noFeatures := base
	noFeatures.Features = nil
	if err := validateRunInput(noFeatures); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("missing features error = %v", err)
	}
	duplicate := base
	duplicate.Features = []string{featureTranslate, featureTranslate}
	if err := validateRunInput(duplicate); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("duplicate features error = %v", err)
	}
	unknown := base
	unknown.Features = []string{"unknown"}
	if err := validateRunInput(unknown); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("unknown feature error = %v", err)
	}
	nonCustom := base
	nonCustom.Template = templateToolTranslate
	nonCustom.Features = []string{featureTranslate}
	if err := validateRunInput(nonCustom); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("features on non-custom error = %v", err)
	}
	noTargets := base
	noTargets.TargetLanguages = nil
	if err := validateRunInput(noTargets); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("translate without targets error = %v", err)
	}
	separation := base
	separation.Features = []string{featureSeparation}
	separation.TargetLanguages = nil
	separation.SourceLanguage = "und"
	if err := validateRunInput(separation); err != nil {
		t.Fatalf("separation-only input: %v", err)
	}
	erase := base
	erase.Features = []string{featureErase}
	erase.SourceLanguage = "chinese"
	erase.TargetLanguages = nil
	if err := validateRunInput(erase); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("erase without mode/extract error = %v", err)
	}
	erase.EraseMode = domain.EraseModeDialogOnly
	erase.Extract = &domain.ExtractOptions{Mode: domain.ExtractModeOCR, OCRLanguage: domain.OCRLanguageChinese}
	if err := validateRunInput(erase); err != nil {
		t.Fatalf("valid erase input: %v", err)
	}
	clone := base
	clone.Features = []string{featureClone}
	if err := validateRunInput(clone); err != nil {
		t.Fatalf("valid clone input: %v", err)
	}
	twoInputs := separation
	twoInputs.InputRevisionIDs = []string{"a", "b"}
	if err := validateRunInput(twoInputs); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("two inputs error = %v", err)
	}
}

func TestCustomPipelineRequiredSlots(t *testing.T) {
	cases := []struct {
		name     string
		features []string
		extract  *domain.ExtractOptions
		want     []domain.CapabilitySlotKey
	}{
		{"separation", []string{featureSeparation}, nil,
			[]domain.CapabilitySlotKey{domain.CapabilitySlotSeparation, domain.CapabilitySlotFFmpeg}},
		{"erase", []string{featureErase},
			&domain.ExtractOptions{Mode: domain.ExtractModeOCR, OCRLanguage: domain.OCRLanguageChinese},
			[]domain.CapabilitySlotKey{domain.CapabilitySlotOCR, domain.CapabilitySlotSubtitleErase, domain.CapabilitySlotFFmpeg}},
		{"translate", []string{featureTranslate}, nil,
			[]domain.CapabilitySlotKey{domain.CapabilitySlotASR, domain.CapabilitySlotTranslate, domain.CapabilitySlotFFmpeg}},
		{"clone", []string{featureClone}, nil,
			[]domain.CapabilitySlotKey{
				domain.CapabilitySlotASR, domain.CapabilitySlotTranslate,
				domain.CapabilitySlotVoiceClone, domain.CapabilitySlotFFmpeg,
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := CreateWorkflowRunInput{Template: templateCustomPipeline, Features: tc.features, Extract: tc.extract}
			tpl, err := pipelineTemplateForInput(in)
			if err != nil {
				t.Fatalf("template: %v", err)
			}
			got := requiredSlots(in, tpl, tpl.Has(domain.NodeKeyExtract), tpl.Has(domain.NodeKeyTranslate))
			assertSlots(t, got, tc.want...)
		})
	}
	in := CreateWorkflowRunInput{Template: templateCustomPipeline, Features: []string{featureTranslate}}
	tpl, err := pipelineTemplateForInput(in)
	if err != nil {
		t.Fatalf("template: %v", err)
	}
	// 原文轨被复用时 04 不执行，不再要求 ASR/OCR。
	assertSlots(t, requiredSlots(in, tpl, false, true), domain.CapabilitySlotTranslate)
}
