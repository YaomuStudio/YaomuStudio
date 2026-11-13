package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/YaomuStudio/YaomuStudio/app-server/internal/domain"
	"github.com/YaomuStudio/YaomuStudio/app-server/internal/store"
)

const (
	customPipelineTemplatesKey = "custom_pipeline.templates.v1"
	customPipelineSchema       = 1
	customPipelineMaxTemplates = 20
)

// CustomPipelineTemplate 是专属链路的本机配置模板（只含配置，不含文件）。
type CustomPipelineTemplate struct {
	ID                 string             `json:"id"`
	Name               string             `json:"name"`
	Features           []string           `json:"features"`
	Languages          []string           `json:"languages"`
	SourceLanguage     string             `json:"source_language"`
	EraseMode          domain.EraseMode   `json:"erase_mode,omitempty"`
	OCRLanguage        domain.OCRLanguage `json:"ocr_language,omitempty"`
	ValidateTimestamps bool               `json:"validate_timestamps"`
	AdjustDuration     bool               `json:"adjust_duration"`
	CreatedAt          time.Time          `json:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at"`
}

// CustomPipelineTemplateList 是模板整表读写形状。
type CustomPipelineTemplateList struct {
	Items []CustomPipelineTemplate `json:"items"`
}

type storedCustomPipelineTemplates struct {
	SchemaVersion int                      `json:"schema_version"`
	Templates     []CustomPipelineTemplate `json:"templates"`
}

var customTemplateIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// CustomPipelineTemplates 读取本机模板；损坏/未知版本按空列表处理，不阻塞启动。
func (s *Service) CustomPipelineTemplates(ctx context.Context) (CustomPipelineTemplateList, error) {
	raw, found, err := s.store.GetMeta(ctx, customPipelineTemplatesKey)
	if err != nil {
		return CustomPipelineTemplateList{}, err
	}
	if !found || strings.TrimSpace(raw) == "" {
		return CustomPipelineTemplateList{Items: []CustomPipelineTemplate{}}, nil
	}
	var stored storedCustomPipelineTemplates
	if err := json.Unmarshal([]byte(raw), &stored); err != nil ||
		stored.SchemaVersion != customPipelineSchema {
		s.log.Warn("service", "custom_pipeline.templates_unreadable", map[string]any{
			"schema_version": stored.SchemaVersion,
		})
		return CustomPipelineTemplateList{Items: []CustomPipelineTemplate{}}, nil
	}
	items := make([]CustomPipelineTemplate, 0, len(stored.Templates))
	for _, item := range stored.Templates {
		normalized, err := normalizeCustomTemplate(item)
		if err != nil {
			s.log.Warn("service", "custom_pipeline.template_skipped", map[string]any{
				"template_id": item.ID,
			})
			continue
		}
		items = append(items, normalized)
	}
	sortCustomTemplates(items)
	return CustomPipelineTemplateList{Items: items}, nil
}

// SaveCustomPipelineTemplates 整表替换本机模板（最多 20 条）。
func (s *Service) SaveCustomPipelineTemplates(
	ctx context.Context,
	idem Idem,
	in CustomPipelineTemplateList,
) (Response, error) {
	return s.write(ctx, "custom-pipeline.templates.save", idem, 200, func(ctx context.Context, tx *store.Tx) (any, error) {
		existing := map[string]CustomPipelineTemplate{}
		if raw, found, err := tx.GetMeta(ctx, customPipelineTemplatesKey); err != nil {
			return nil, err
		} else if found && strings.TrimSpace(raw) != "" {
			var stored storedCustomPipelineTemplates
			if json.Unmarshal([]byte(raw), &stored) == nil && stored.SchemaVersion == customPipelineSchema {
				for _, item := range stored.Templates {
					existing[item.ID] = item
				}
			}
		}
		items, err := s.validateCustomTemplates(in.Items, existing)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(storedCustomPipelineTemplates{
			SchemaVersion: customPipelineSchema,
			Templates:     items,
		})
		if err != nil {
			return nil, fmt.Errorf("encode custom pipeline templates: %w", err)
		}
		if err := tx.SetMeta(ctx, customPipelineTemplatesKey, string(encoded)); err != nil {
			return nil, err
		}
		out := append([]CustomPipelineTemplate{}, items...)
		sortCustomTemplates(out)
		return CustomPipelineTemplateList{Items: out}, nil
	})
}

func (s *Service) validateCustomTemplates(
	raw []CustomPipelineTemplate,
	existing map[string]CustomPipelineTemplate,
) ([]CustomPipelineTemplate, error) {
	if len(raw) > customPipelineMaxTemplates {
		return nil, invalidRequestf("custom pipeline templates exceed %d", customPipelineMaxTemplates)
	}
	now := s.Now().UTC()
	ids := make(map[string]struct{}, len(raw))
	names := make(map[string]struct{}, len(raw))
	items := make([]CustomPipelineTemplate, 0, len(raw))
	for _, item := range raw {
		normalized, err := normalizeCustomTemplate(item)
		if err != nil {
			return nil, err
		}
		if _, ok := ids[normalized.ID]; ok {
			return nil, invalidRequestf("duplicate custom template id %q", normalized.ID)
		}
		ids[normalized.ID] = struct{}{}
		nameKey := strings.ToLower(normalized.Name)
		if _, ok := names[nameKey]; ok {
			return nil, invalidRequestf("duplicate custom template name %q", normalized.Name)
		}
		names[nameKey] = struct{}{}
		if old, ok := existing[normalized.ID]; ok && !old.CreatedAt.IsZero() {
			normalized.CreatedAt = old.CreatedAt
		} else if normalized.CreatedAt.IsZero() {
			normalized.CreatedAt = now
		}
		normalized.UpdatedAt = now
		items = append(items, normalized)
	}
	return items, nil
}

// normalizeCustomTemplate 做无状态校验与去空白；返回的 slices 不与输入共享。
func normalizeCustomTemplate(item CustomPipelineTemplate) (CustomPipelineTemplate, error) {
	item.ID = strings.TrimSpace(item.ID)
	if !customTemplateIDPattern.MatchString(item.ID) {
		return CustomPipelineTemplate{}, invalidRequestf("invalid custom template id %q", item.ID)
	}
	item.Name = strings.TrimSpace(item.Name)
	if count := utf8.RuneCountInString(item.Name); count == 0 || count > 30 {
		return CustomPipelineTemplate{}, invalidRequestf("custom template name must be 1-30 characters")
	}
	features, err := normalizeCustomFeatures(item.Features)
	if err != nil {
		return CustomPipelineTemplate{}, err
	}
	item.Features = features
	item.Languages, err = normalizeCustomLanguages(item.Languages)
	if err != nil {
		return CustomPipelineTemplate{}, err
	}
	item.SourceLanguage = strings.TrimSpace(item.SourceLanguage)
	if item.SourceLanguage == "" {
		return CustomPipelineTemplate{}, invalidRequestf("custom template source_language required")
	}
	if customNeedsLanguages(features) && len(item.Languages) == 0 {
		return CustomPipelineTemplate{}, invalidRequestf("custom template target languages required")
	}
	if hasCustomFeature(features, featureErase) {
		if !item.EraseMode.Valid() {
			return CustomPipelineTemplate{}, invalidRequestf("custom template erase_mode required")
		}
		if !item.OCRLanguage.Valid() {
			return CustomPipelineTemplate{}, invalidRequestf("custom template ocr_language required")
		}
	} else if item.EraseMode != "" || item.OCRLanguage != "" {
		return CustomPipelineTemplate{}, invalidRequestf("custom template erase options require erase feature")
	}
	return item, nil
}

func normalizeCustomLanguages(raw []string) ([]string, error) {
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		language := strings.TrimSpace(value)
		if language == "" {
			return nil, invalidRequestf("custom template languages contain an empty entry")
		}
		if _, ok := seen[language]; ok {
			return nil, invalidRequestf("duplicate custom template language %q", language)
		}
		seen[language] = struct{}{}
		out = append(out, language)
	}
	return out, nil
}

func sortCustomTemplates(items []CustomPipelineTemplate) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
}
