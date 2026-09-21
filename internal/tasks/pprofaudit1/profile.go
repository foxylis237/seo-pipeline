// Package pprofaudit1 задаёт конфигурацию задачи pprof_audit_1.
//
// Первая задача аудита: проверяет уже опубликованные статьи блога и складывает находки в
// отчёт для человека. В блог она не пишет ничего — ни текста, ни полей, ни меток.
//
// Технически это выражено непустым ArticleAudit: composition root уводит задачу на поток
// internal/pipeline/articleaudit, у которого интерфейс площадки состоит из двух читающих
// методов. Ни от задач правки, ни от pprofaudit2 пакет не зависит и зависеть не должен.
package pprofaudit1

import (
	"github.com/foxylis237/seo-pipeline/internal/pipeline/articleaudit"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// Имя задачи в двух формах: подчёркнутая идёт в логи, каталоги и схему PostgreSQL,
// дефисная — то, что человек пишет в CLI и в make.
const (
	Name    = "pprof_audit_1"
	Command = "pprof-audit-1"
)

// Stages — стадии, без которых схема задачи неполна. Стадия одна: проверка.
var Stages = []string{articleaudit.StageAudit}

// Пути задачи. Собраны здесь, а не разбросаны по вызовам: правило раскладки одно.
const (
	InputDir     = "input/pprof_audit_1"
	OutputDir    = "tasks/pprof_audit_1/output"
	PromptsDir   = "tasks/pprof_audit_1/prompts"
	TemplatePath = "tasks/pprof_audit_1/templates/result.md.tmpl"
	// AuditPromptPath — регламент проверки. Тот же файл называет и схема стадий: путь нужен
	// ещё и сборке промпта, поэтому имя одно на оба места.
	AuditPromptPath = "tasks/pprof_audit_1/prompts/audit.txt"
)

// RequiredFields — поля записи, которые обязаны быть заполнены.
//
// Набор называет человек, а не эвристика по данным: пустое поле бывает законным, и вывести
// список из живой записи нельзя. Здесь он закрытый — одиннадцать граф, которые у статьи блога
// заполняются всегда; всё остальное (prof_blue, метрики Yoast, шаблонные заглушки темы) не
// проверяется намеренно, его пустота ни о чём не говорит.
//
// Рубрика, метки и название записи полями не являются — они живут в самой записи, — но для
// человека это такие же графы админки, и разводить их по двум спискам незачем.
var RequiredFields = []string{
	articleaudit.RecordCategory,
	articleaudit.RecordTags,
	articleaudit.RecordTitle,
	"prof_title",
	"prof_name",
	"blog_tldr",
	"blog_read",
	"author_link",
	"related_courses",
	"_yoast_wpseo_focuskw",
	articleaudit.SEOTitleField,
	articleaudit.SEOMetaDescriptionField,
}

// MinFAQ — сколько вопросов обязано быть в блоке частых вопросов.
//
// Шесть: столько их у статей, написанных стадией info, и блок под статьёй тема рисует из
// этих же полей — неполный виден читателю сразу. Считаются заполненные вопросы, а не
// счётчик blog_faq: счётчик пишет админка, и он переживает вычищенный вопрос.
const MinFAQ = 6

// Имена полей блока вопросов у статьи блога.
//
// У страницы услуги они другие (репитер faq_loop), и различие это не косметическое: ошибись
// здесь — и проверка молча объявит блок пустым там, где он заполнен.
const (
	FAQQuestionField = "blog_faq_%d_question"
	FAQAnswerField   = "blog_faq_%d_answer"
)

// MinInternalLinks — сколько внутренних ссылок обязано быть в теле статьи.
//
// Три — та же норма, по которой перелинковку ставит генерация (pprof_1, obuch_1): статья,
// написанная с тремя ссылками на программы, и проверяться должна по ним же. Считает их код,
// а не модель: число ссылок — факт, и второго мнения о нём не бывает.
const MinInternalLinks = 3

// Profile возвращает конфигурацию pprof_audit_1.
func Profile() tasks.Profile {
	return tasks.Profile{
		Name:             Name,
		Command:          Command,
		InputDir:         InputDir,
		OutputDir:        OutputDir,
		PromptsDir:       PromptsDir,
		TemplatePath:     TemplatePath,
		LLMConfigPath:    "config/pprof_audit_1.yaml",
		LLMOverlayPath:   "",
		ImportReportsDir: "output/pprof_audit_1/import-reports",
		DiagnosticsDir:   "output/pprof_audit_1/debug",
		DBSchema:         Name,
		EnvPrefix:        "PPROF_AUDIT_1_",
		// Таблиц движка у задачи нет вовсе: её схема описана migrations/pprof_audit_1 и
		// проверяется ею самой. Метаданных она не пишет, как и задачи правки.
		WithoutMetadataStage: true,
		LLMStages:            Stages,
		// Непустое поле и есть признак задачи аудита: по нему composition root уводит её на
		// поток articleaudit, минуя таблицы и проверки движка генерации.
		ArticleAudit: &tasks.ArticleAudit{
			AuditPromptPath:  AuditPromptPath,
			RequiredFields:   RequiredFields,
			MinInternalLinks: MinInternalLinks,
			MinFAQ:           MinFAQ,
			FAQQuestionField: FAQQuestionField,
			FAQAnswerField:   FAQAnswerField,
		},
	}
}
