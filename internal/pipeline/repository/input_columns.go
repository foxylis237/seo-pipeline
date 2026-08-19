package repository

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// Колонки article_inputs, которые есть у каждой задачи.
//
// Это контракт движка в узком смысле: без них не собрать ни каталог артефактов, ни один
// промпт, и убрать любую из них нельзя. Всё остальное объявляет задача — и то, что есть
// только у неё, и то, чего у неё нет, хотя есть у соседей (см. extraInputColumns).
var baseInputColumns = []string{
	"category", "header", "image_slug", "meta_description", "key_word",
	"reference_url",
}

// extraInputColumn — колонка article_inputs, которую заводит не каждая задача.
//
// Здесь собрано всё, что о ней нужно знать трём местам сразу: проверке схемы (тип и
// nullable), импорту (откуда взять значение) и сборке result.md (куда его положить).
// Разложить это по трём файлам значило бы, что колонку можно завести, но забыть прочитать, —
// и обнаружилось бы это пустым полем в result.md, а не отказом на старте.
type extraInputColumn struct {
	typeName string
	nullable bool
	// write отдаёт значение колонки из импортированной строки Excel.
	write func(article.Input) string
	// read указывает, в какое поле сборки result.md кладётся прочитанное значение.
	//
	// Пустой read — не забывчивость: колонку читают не одна выборка result.md, а несколько
	// запросов, каждый в своё поле и в своём порядке. Такие колонки называются в SQL по
	// имени, а их отсутствие у задачи подставляет пустую строку — см. inputColumn.
	read func(*article.ResultInput) *string
}

// extraInputColumns — реестр необщих колонок.
//
// Имя колонки здесь — то же имя, которым её называет профиль задачи в ExtraInputColumns и
// миграция в migrations/. Задача, не объявившая колонку, не пишет её и не читает: у неё этой
// колонки в схеме PostgreSQL нет, и запрос с ней просто не выполнился бы.
var extraInputColumns = map[string]extraInputColumn{
	// Колонки, которые раньше были общими. Задача, которой они нужны, объявляет их наравне
	// со своими: у pprof_2 перелинковки нет, похожих профессий он не печатает, меток в блог
	// не публикует, а автора у страницы услуги нет вовсе — раздел result.md называется
	// «Преподаватели» и читает свою колонку.
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
}

// IsOptionalInputColumn отвечает, объявляется ли колонка профилем задачи.
//
// Нужна сверке импорта: поле, которого нет в схеме задачи, сравнивать с книгой бессмысленно —
// оно всегда будет «не перенесено». Знание одно на весь проект и живёт здесь же, рядом с
// реестром: второй список необязательных колонок разошёлся бы с первым молча.
func IsOptionalInputColumn(name string) bool {
	_, found := extraInputColumns[name]
	return found
}

// ValidateExtraInputColumns проверяет, что профиль назвал существующие колонки.
//
// Отдельная функция нужна затем, чтобы опечатка в профиле роняла команду на старте, а не
// оборачивалась пустым полем в result.md через два часа генерации.
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

// insertInputColumns собирает список колонок и значений для записи строки article_inputs.
//
// Порядок колонок и порядок значений задаётся здесь одним проходом: разъехаться они не могут
// даже теоретически, потому что собираются вместе.
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

// resultInputProjection дописывает необщие колонки к выборке данных result.md.
//
// Возвращает готовый кусок SELECT и цели сканирования в том же порядке: как и при записи,
// список и порядок собираются одним проходом.
func (r *ArticleRepository) resultInputProjection(input *article.ResultInput) (projection string, targets []any) {
	if len(r.extraInputs) == 0 {
		return "", nil
	}
	var builder strings.Builder
	targets = make([]any, 0, len(r.extraInputs))
	for _, name := range r.extraInputs {
		read := extraInputColumns[name].read
		// Колонку без read эта выборка не дописывает: её читают именованные запросы, каждый
		// в своё поле. Дописать её сюда значило бы прочитать значение в никуда.
		if read == nil {
			continue
		}
		fmt.Fprintf(&builder, ", COALESCE(i.%s, '')", name)
		targets = append(targets, read(input))
	}
	return builder.String(), targets
}

// inputColumn возвращает выражение колонки article_inputs для запроса этой задачи.
//
// У задачи, объявившей колонку, это обычное чтение; у задачи без неё — пустая строка вместо
// значения. Так один и тот же запрос работает в обеих схемах, а порядок и число целей
// сканирования не зависят от набора колонок: расходятся схемы, а не разбор ответа.
func (r *ArticleRepository) inputColumn(name string) string {
	if r.hasInputColumn(name) {
		return "COALESCE(i." + name + ", '')"
	}
	return "''"
}

// hasInputColumn отвечает, есть ли колонка в схеме этой задачи.
func (r *ArticleRepository) hasInputColumn(name string) bool {
	if slices.Contains(baseInputColumns, name) {
		return true
	}
	return slices.Contains(r.extraInputs, name)
}

// metadataTLDR возвращает выражение колонки article_metadata.tldr.
//
// Колонки нет у задачи, которая TL;DR не генерирует: держать её пустой во всех строках
// значит обещать раздел, которого не будет. Чтение при этом остаётся одним и тем же.
func (r *ArticleRepository) metadataTLDR() string {
	if r.withoutTLDR {
		return "''"
	}
	return "COALESCE(m.tldr, '')"
}

// articleMetadataUpsert собирает запись article_metadata под схему этой задачи.
//
// Возвращает готовый запрос и аргументы к нему: у задачи без колонки tldr она не называется
// ни в списке колонок, ни в SET — иначе первый же вызов упал бы на «column does not exist».
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

// excludedAssignments собирает `колонка = EXCLUDED.колонка` для ON CONFLICT DO UPDATE.
func excludedAssignments(columns []string) string {
	parts := make([]string, 0, len(columns))
	for _, name := range columns {
		parts = append(parts, name+" = EXCLUDED."+name)
	}
	return strings.Join(parts, ", ")
}
