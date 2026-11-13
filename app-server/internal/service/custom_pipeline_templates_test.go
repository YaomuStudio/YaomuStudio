package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/logging"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

func newCustomTemplateService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "yaomu.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logger, err := logging.New(logging.Options{Path: filepath.Join(t.TempDir(), "core.log")})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	return New(Options{Store: st, Log: logger, WorkspaceID: "ws-custom", ServerEpoch: 1, DataDir: dataDir}), st
}

func TestCustomPipelineTemplatesRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, _ := newCustomTemplateService(t)
	empty, err := svc.CustomPipelineTemplates(ctx)
	if err != nil {
		t.Fatalf("empty read: %v", err)
	}
	if len(empty.Items) != 0 {
		t.Fatalf("empty items = %+v", empty.Items)
	}

	created := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	in := CustomPipelineTemplateList{Items: []CustomPipelineTemplate{
		{
			ID: "tpl-sep", Name: "纯分离", Features: []string{featureSeparation},
			SourceLanguage: "und", CreatedAt: created,
		},
		{
			ID: "tpl-lang", Name: "翻译成片", Features: []string{featureMix, featureTranslate},
			Languages: []string{"en", "ja"}, SourceLanguage: "zh",
			ValidateTimestamps: true, AdjustDuration: true,
		},
	}}
	resp, err := svc.SaveCustomPipelineTemplates(ctx, Idem{}, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	saved := resp.Body.(CustomPipelineTemplateList)
	if len(saved.Items) != 2 || saved.Items[0].Name != "纯分离" {
		t.Fatalf("saved = %+v", saved.Items)
	}
	if saved.Items[1].CreatedAt.IsZero() || saved.Items[1].UpdatedAt.IsZero() {
		t.Fatalf("timestamps = %+v", saved.Items[1])
	}
	if len(saved.Items[1].Features) != 2 || saved.Items[1].Features[0] != featureTranslate {
		t.Fatalf("features normalized = %v", saved.Items[1].Features)
	}

	read, err := svc.CustomPipelineTemplates(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(read.Items) != 2 || read.Items[0].ID != "tpl-sep" {
		t.Fatalf("read = %+v", read.Items)
	}
	if !read.Items[0].CreatedAt.Equal(created) {
		t.Fatalf("created_at changed: %v", read.Items[0].CreatedAt)
	}
}

func TestCustomPipelineTemplatesValidation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newCustomTemplateService(t)
	base := CustomPipelineTemplate{
		ID: "tpl", Name: "模板", Features: []string{featureTranslate},
		Languages: []string{"en"}, SourceLanguage: "zh",
	}
	cases := []struct {
		name  string
		items []CustomPipelineTemplate
	}{
		{"empty name", []CustomPipelineTemplate{{ID: "a", Name: " ", Features: []string{featureSeparation}, SourceLanguage: "und"}}},
		{"bad id", []CustomPipelineTemplate{{ID: "../x", Name: "x", Features: []string{featureSeparation}, SourceLanguage: "und"}}},
		{"no features", []CustomPipelineTemplate{{ID: "a", Name: "x", SourceLanguage: "und"}}},
		{"unknown feature", []CustomPipelineTemplate{{ID: "a", Name: "x", Features: []string{"x"}, SourceLanguage: "und"}}},
		{"duplicate name", []CustomPipelineTemplate{base, {ID: "b", Name: " 模板 ", Features: []string{featureTranslate}, Languages: []string{"ja"}, SourceLanguage: "zh"}}},
		{"duplicate language", []CustomPipelineTemplate{{ID: "a", Name: "x", Features: []string{featureTranslate}, Languages: []string{"en", "en"}, SourceLanguage: "zh"}}},
		{"missing target", []CustomPipelineTemplate{{ID: "a", Name: "x", Features: []string{featureClone}, SourceLanguage: "zh"}}},
		{"erase missing options", []CustomPipelineTemplate{{ID: "a", Name: "x", Features: []string{featureErase}, SourceLanguage: "chinese"}}},
		{"erase options without feature", []CustomPipelineTemplate{{ID: "a", Name: "x", Features: []string{featureTranslate}, Languages: []string{"en"}, SourceLanguage: "zh", EraseMode: domain.EraseModeDialogOnly}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.SaveCustomPipelineTemplates(ctx, Idem{}, CustomPipelineTemplateList{Items: tc.items}); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}

	tooMany := make([]CustomPipelineTemplate, 0, customPipelineMaxTemplates+1)
	for i := 0; i <= customPipelineMaxTemplates; i++ {
		tooMany = append(tooMany, CustomPipelineTemplate{
			ID: "tpl-" + string(rune('a'+i)), Name: "模板" + string(rune('a'+i)),
			Features: []string{featureSeparation}, SourceLanguage: "und",
		})
	}
	if _, err := svc.SaveCustomPipelineTemplates(ctx, Idem{}, CustomPipelineTemplateList{Items: tooMany}); err == nil {
		t.Fatalf("expected max template error")
	}
}

func TestCustomPipelineTemplatesCorrupt(t *testing.T) {
	ctx := context.Background()
	svc, st := newCustomTemplateService(t)
	if err := st.SetMeta(ctx, customPipelineTemplatesKey, "{broken"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	read, err := svc.CustomPipelineTemplates(ctx)
	if err != nil {
		t.Fatalf("read corrupt: %v", err)
	}
	if len(read.Items) != 0 {
		t.Fatalf("corrupt items = %+v", read.Items)
	}

	encoded, err := json.Marshal(storedCustomPipelineTemplates{
		SchemaVersion: 99,
		Templates: []CustomPipelineTemplate{{
			ID: "x", Name: "x", Features: []string{featureSeparation}, SourceLanguage: "und",
		}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := st.SetMeta(ctx, customPipelineTemplatesKey, string(encoded)); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	read, err = svc.CustomPipelineTemplates(ctx)
	if err != nil {
		t.Fatalf("read unknown version: %v", err)
	}
	if len(read.Items) != 0 {
		t.Fatalf("unknown version items = %+v", read.Items)
	}
}

func TestCustomPipelineTemplatesDuplicateName(t *testing.T) {
	ctx := context.Background()
	svc, _ := newCustomTemplateService(t)
	_, err := svc.SaveCustomPipelineTemplates(ctx, Idem{}, CustomPipelineTemplateList{Items: []CustomPipelineTemplate{
		{ID: "a", Name: "周更剧集链路", Features: []string{featureTranslate}, Languages: []string{"en"}, SourceLanguage: "zh"},
		{ID: "b", Name: " 周更剧集链路 ", Features: []string{featureSeparation}, SourceLanguage: "und"},
	}})
	if err == nil {
		t.Fatalf("expected duplicate name error")
	}
}
