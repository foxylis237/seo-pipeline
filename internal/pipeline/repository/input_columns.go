package repository

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// baseInputColumns — колонки article_inputs, которые есть у каждой задачи; остальное объявляет задача.
var baseInputColumns = []string{
	"category", "header", "image_slug", "meta_description", "key_word",
	"reference_url",
}

// extraInputColumn — колонка article_inputs, которую заводит не каждая задача: тип для проверки
// схемы, источник для импорта и поле result.md в одном месте.
type extraInputColumn struct {
	typeName string
	nullable bool
	write    func(article.Input) string
	// read — поле result.md; nil у колонок, которые читают именованные запросы (см. inputColumn).
	read func(*article.ResultInput) *string
}

// SharedInputColumns — необщие колонки, которые две задачи вправе объявить одновременно:
// у обеих они значат одно и то же и уходят в одно поле площадки. На списке стоят проверки
// internal/tasks/*/profile_test.go.
var SharedInputColumns = []string{
	"seo_title", "profession", "course_url",
	"hours", "duration", "price", "document", "attestation",
}

// extraInputColumns — реестр необщих колонок; имя то же, что в ExtraInputColumns профиля и в миграции.
var extraInputColumns = map[string]extraInputColumn{
	"author": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.Author },
	},
	"links": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.Links },
	},
	"professions": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.Professions },
	},
	"tags": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.Tags },
	},
	"seo_title": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.SEOTitle },
		read:  func(result *article.ResultInput) *string { return &result.SEOTitle },
	},
	"section": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.Section },
		read:  func(result *article.ResultInput) *string { return &result.Section },
	},
	"profession": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.Profession },
		read:  func(result *article.ResultInput) *string { return &result.Profession },
	},
	"teachers": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.Teachers },
		read:  func(result *article.ResultInput) *string { return &result.Teachers },
	},
	"service_name": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.ServiceName },
		read:  func(result *article.ResultInput) *string { return &result.ServiceName },
	},
	"post_type": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.PostType },
		read:  func(result *article.ResultInput) *string { return &result.PostType },
	},
	"hours": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.Hours },
		read:  func(result *article.ResultInput) *string { return &result.Hours },
	},
	"duration": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.Duration },
		read:  func(result *article.ResultInput) *string { return &result.Duration },
	},
	"price": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.Price },
		read:  func(result *article.ResultInput) *string { return &result.Price },
	},
	"document": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.Document },
		read:  func(result *article.ResultInput) *string { return &result.Document },
	},
	"attestation": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.Attestation },
		read:  func(result *article.ResultInput) *string { return &result.Attestation },
	},
	"image_source_url": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.ImageSourceURL },
		read:  func(result *article.ResultInput) *string { return &result.ImageSourceURL },
	},
	// course_url читает поток генерации через GetGenerationInput, в result.md он не нужен.
	"course_url": {
		typeName: "text", nullable: true,
		write: func(input article.Input) string { return input.CourseURL },
	},
}

// IsOptionalInputColumn отвечает, объявляется ли колонка профилем задачи.
func IsOptionalInputColumn(name string) bool {
	_, found := extraInputColumns[name]
	return found
}

// ValidateExtraInputColumns проверяет, что профиль назвал существующие колонки.
func ValidateExtraInputColumns(names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, found := extraInputColumns[name]; !found {
			return fmt.Errorf("unknown article_inputs column %q; known columns: %s",
				name, strings.Join(knownExtraInputColumns(), ", "))
		}
		for _, base := range baseInputColumns {
			if name == base {
				return fmt.Errorf("column %q is a base article_inputs column and must not be declared as an extra one", name)
			}
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("article_inputs column %q is declared twice", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func knownExtraInputColumns() []string {
	names := make([]string, 0, len(extraInputColumns))
	for name := range extraInputColumns {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// insertInputColumns собирает колонки и значения строки article_inputs одним проходом.
func (r *ArticleRepository) insertInputColumns(input article.Input) (columns []string, values []any) {
	columns = make([]string, 0, len(baseInputColumns)+len(r.extraInputs))
	values = make([]any, 0, cap(columns))
	baseValues := map[string]string{
		"category": input.Category, "header": input.Header, "image_slug": input.ImageSlug,
		"meta_description": input.MetaDescription, "key_word": input.Keyword,
		"reference_url": input.ReferenceURL,
	}
	for _, name := range baseInputColumns {
		columns = append(columns, name)
		values = append(values, baseValues[name])
	}
	for _, name := range r.extraInputs {
		columns = append(columns, name)
		values = append(values, extraInputColumns[name].write(input))
	}
	return columns, values
}

// resultInputProjection дописывает необщие колонки к выборке result.md и цели сканирования в том же порядке.
func (r *ArticleRepository) resultInputProjection(input *article.ResultInput) (projection string, targets []any) {
	if len(r.extraInputs) == 0 {
		return "", nil
	}
	var builder strings.Builder
	targets = make([]any, 0, len(r.extraInputs))
	for _, name := range r.extraInputs {
		read := extraInputColumns[name].read
		if read == nil {
			continue
		}
		fmt.Fprintf(&builder, ", COALESCE(i.%s, '')", name)
		targets = append(targets, read(input))
	}
	return builder.String(), targets
}

// inputColumn возвращает чтение колонки или пустую строку, если её нет в схеме задачи,
// чтобы порядок и число целей сканирования не зависели от набора колонок.
func (r *ArticleRepository) inputColumn(name string) string {
	if r.hasInputColumn(name) {
		return "COALESCE(i." + name + ", '')"
	}
	return "''"
}

func (r *ArticleRepository) hasInputColumn(name string) bool {
	if slices.Contains(baseInputColumns, name) {
		return true
	}
	return slices.Contains(r.extraInputs, name)
}

// metadataTLDR возвращает выражение article_metadata.tldr или пустую строку у схемы без колонки.
func (r *ArticleRepository) metadataTLDR() string {
	if r.withoutTLDR {
		return "''"
	}
	return "COALESCE(m.tldr, '')"
}

// articleMetadataUpsert собирает запись article_metadata; у схемы без tldr колонка не называется.
func (r *ArticleRepository) articleMetadataUpsert(articleID int64, rawText string, info article.ArticleInfo) (string, []any) {
	if r.withoutTLDR {
		return `
		INSERT INTO article_metadata (article_id, faq, metadata_text, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (article_id) DO UPDATE
		SET faq = EXCLUDED.faq,
			metadata_text = EXCLUDED.metadata_text,
			updated_at = NOW()
	`, []any{articleID, info.FAQ, rawText}
	}
	return `
		INSERT INTO article_metadata (article_id, tldr, faq, metadata_text, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (article_id) DO UPDATE
		SET tldr = EXCLUDED.tldr,
			faq = EXCLUDED.faq,
			metadata_text = EXCLUDED.metadata_text,
			updated_at = NOW()
	`, []any{articleID, info.TLDR, info.FAQ, rawText}
}

// placeholders возвращает $from..$from+count-1 через запятую.
func placeholders(from, count int) string {
	parts := make([]string, 0, count)
	for index := 0; index < count; index++ {
		parts = append(parts, fmt.Sprintf("$%d", from+index))
	}
	return strings.Join(parts, ", ")
}

// coalesceAssignments собирает `колонка = COALESCE(article_inputs.колонка, EXCLUDED.колонка)`:
// импорт дозаполняет только NULL-колонки и не трогает заполненные; пустая строка — значение.
func coalesceAssignments(table string, columns []string) string {
	parts := make([]string, 0, len(columns))
	for _, name := range columns {
		parts = append(parts, fmt.Sprintf("%s = COALESCE(%s.%s, EXCLUDED.%s)", name, table, name, name))
	}
	return strings.Join(parts, ", ")
}
