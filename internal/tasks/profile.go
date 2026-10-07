// Package tasks описывает профиль задачи — всё, чем один пайплайн отличается от другого.
//
// Здесь только тип: значения задают пакеты задач, реестр собирает composition root.
// Движок и интеграции получают готовые значения (путь, каталог, DSN), а не Profile —
// иначе он стал бы service locator.
package tasks

import "path/filepath"

// CommonPromptsDir — каталог промптов, общих для всех задач.
const CommonPromptsDir = "tasks/common/prompts"

// CommonBlockTemplatesDir — каталог шаблонов визуальных блоков тела страницы (generation.DecorateBlocks).
const CommonBlockTemplatesDir = "tasks/common/templates/blocks"

// Profile — всё, что задача не делит с другими задачами.
// Пути перечислены явно, а не выводятся из Name: каталоги называются не как задача (output/task1).
type Profile struct {
	// Name — подчёркнутая форма (task_1), поле task в логах.
	Name string
	// Command — дефисная форма (task-1): первый аргумент CLI и цель Makefile.
	Command string

	// InputDir — каталог книги импорта; файл в нём выбирает importer.ResolveWorkbook.
	InputDir string
	// OutputDir — корень артефактов статей (OUTPUT_DIR).
	OutputDir string
	// PromptsDir — корень промптов задачи для кода, который собирает путь сам.
	PromptsDir string
	// TemplatePath — шаблон result.md.
	TemplatePath string
	// LLMConfigPath и LLMOverlayPath — схема стадий и наложение; пустой overlay — схема одна.
	LLMConfigPath  string
	LLMOverlayPath string
	// ImportReportsDir — отчёты импорта, вне OutputDir.
	ImportReportsDir string
	// DiagnosticsDir — корень диагностики браузерных интеграций.
	DiagnosticsDir string
	// DBSchema — схема PostgreSQL, на ней держится изоляция задач в базе.
	DBSchema string
	// EnvPrefix — префикс переменных окружения профиля; у task_1 пустой.
	EnvPrefix string
	// GoogleFolderURL — папка Drive для промптов задачи; пустая — общая по умолчанию.
	// Своя папка нужна, потому что документ ищется по имени, а имена статей у задач совпадают.
	GoogleFolderURL string
	// ExtraInputColumns — колонки article_inputs сверх базового набора, имена из реестра репозитория.
	ExtraInputColumns []string
	// WithoutMetadataStage — у задачи нет стадии, которая пишет article_metadata.
	WithoutMetadataStage bool
	// MetadataFAQOnly — публикация требует только FAQ, без TL;DR и времени чтения.
	// Раннер его не читает: для раннера FAQ необязателен.
	MetadataFAQOnly bool
	// RelatedCourses — публикация заполняет блок «Связанные курсы» под статьёй.
	// Признак, а не вывод из данных: пустое related_courses законно — тогда курсы подбирает тема.
	RelatedCourses bool
	// CommercialPages — публикация раскладывает запись как страницу услуги (таксономия, ACF, связь teacher).
	CommercialPages bool
	// PlainBlogPages — публикация раскладывает запись как статью блога площадки без полей ACF.
	// С CommercialPages не сочетается.
	PlainBlogPages bool
	// PlainServicePages — публикация раскладывает запись как страницу услуги площадки без ACF.
	// При выборе раскладки стоит выше CommercialPages и PlainBlogPages.
	PlainServicePages bool
	// CatalogSite — площадка, чей каталог услуг читает задача; пустое — dpoprof.
	// Названа площадка, а не схема: схему выбирает composition root, и пару «сайт одной, схема другой» не выразить.
	CatalogSite string
	// ArticleAudit — настройки задачи аудита опубликованных страниц; nil — задача не аудит.
	// Непустое снимает проверку схемы движка: таблиц article_inputs у аудита нет.
	ArticleAudit *ArticleAudit
	// LLMStages — стадии, без которых схема задачи неполна.
	LLMStages []string
}

// ArticleAudit — то, чем одна задача аудита отличается от другой.
type ArticleAudit struct {
	// AuditPromptPath — регламент проверки; тот же файл называет схема стадий.
	AuditPromptPath string
	// RequiredFields — поля записи, пустота которых — находка аудита; пустой список выключает проверку.
	RequiredFields []string
	// MinInternalLinks — норма внутренних ссылок в теле; 0 выключает проверку.
	MinInternalLinks int
	// MinFAQ — норма вопросов FAQ; 0 — считать, но не судить.
	MinFAQ int
	// FAQQuestionField и FAQAnswerField — имена полей FAQ с одним %d (номер вопроса);
	// пустые — формат страницы услуги, faq_loop_%d_faq_question.
	FAQQuestionField string
	FAQAnswerField   string
}

// DiagnosticsSubdir возвращает корень диагностики одного сервиса.
func (p Profile) DiagnosticsSubdir(service string) string {
	return filepath.Join(p.DiagnosticsDir, service)
}
