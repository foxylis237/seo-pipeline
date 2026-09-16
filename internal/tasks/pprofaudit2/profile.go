// Package pprofaudit2 задаёт конфигурацию задачи pprof_audit_2.
//
// Вторая задача аудита: проверяет уже опубликованные страницы услуг — курсы, программы
// обучения, страницы профессий. От pprof_audit_1 отличается ровно двумя вещами: своим
// регламентом проверки и своим списком обязательных полей. В блог она не пишет ничего.
//
// Отдельная задача, а не второй промпт у соседа, потому что у неё своя пачка страниц, своя
// схема PostgreSQL и своя отметка о проверке: общий список означал бы, что один прогон
// открывает страницы другого. Ни от задач правки, ни от pprofaudit1 пакет не зависит.
package pprofaudit2

import (
	"github.com/foxylis237/seo-pipeline/internal/pipeline/articleaudit"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// Имя задачи в двух формах: подчёркнутая идёт в логи, каталоги и схему PostgreSQL,
// дефисная — то, что человек пишет в CLI и в make.
const (
	Name    = "pprof_audit_2"
	Command = "pprof-audit-2"
)

// Stages — стадии, без которых схема задачи неполна. Стадия одна: проверка.
var Stages = []string{articleaudit.StageAudit}

// Пути задачи. Собраны здесь, а не разбросаны по вызовам: правило раскладки одно.
const (
	InputDir     = "input/pprof_audit_2"
	OutputDir    = "tasks/pprof_audit_2/output"
	PromptsDir   = "tasks/pprof_audit_2/prompts"
	TemplatePath = "tasks/pprof_audit_2/templates/result.md.tmpl"
	// AuditPromptPath — регламент проверки. Тот же файл называет и схема стадий: путь нужен
	// ещё и сборке промпта, поэтому имя одно на оба места.
	AuditPromptPath = "tasks/pprof_audit_2/prompts/audit.txt"
)

// RequiredFields — что в записи услуги обязано быть заполнено.
//
// Список назвал человек, и он закрытый: проверяется ровно это и ничего сверх. Часть из него
// полями записи не является — рубрика, название и обложка живут в самой записи, — но для
// человека это такие же графы админки, и разводить их по двум спискам незачем.
//
// Всё остальное, чем запись заполнена, код не проверяет намеренно: text_spec_*, prof_blue и
// прочее либо шаблонные заглушки, одинаковые у разных услуг, либо служебные значения плагинов.
// Их пустота ни о чём не говорит, а в отчёте они были бы шумом.
var RequiredFields = []string{
	articleaudit.RecordCategory,  // рубрика записи
	articleaudit.RecordTitle,     // название статьи
	articleaudit.RecordThumbnail, // обложка
	"prof_title",                 // видимый заголовок страницы
	"prof_name",                  // название профессии в карточке
	"_yoast_wpseo_focuskw",       // фокусное слово
	"_yoast_wpseo_title",         // SEO-заголовок
	"_yoast_wpseo_metadesc",      // мета-описание, оно же «дискипшн»
	"teachers",                   // преподаватели
	"faq_loop",                   // блок частых вопросов
}

// Profile возвращает конфигурацию pprof_audit_2.
func Profile() tasks.Profile {
	return tasks.Profile{
		Name:             Name,
		Command:          Command,
		InputDir:         InputDir,
		OutputDir:        OutputDir,
		PromptsDir:       PromptsDir,
		TemplatePath:     TemplatePath,
		LLMConfigPath:    "config/pprof_audit_2.yaml",
		LLMOverlayPath:   "",
		ImportReportsDir: "output/pprof_audit_2/import-reports",
		DiagnosticsDir:   "output/pprof_audit_2/debug",
		DBSchema:         Name,
		EnvPrefix:        "PPROF_AUDIT_2_",
		// Таблиц движка у задачи нет вовсе: её схема описана migrations/pprof_audit_2 и
		// проверяется ею самой. Метаданных она не пишет, как и задачи правки.
		WithoutMetadataStage: true,
		LLMStages:            Stages,
		// Непустое поле и есть признак задачи аудита: по нему composition root уводит её на
		// поток articleaudit, минуя таблицы и проверки движка генерации.
		ArticleAudit: &tasks.ArticleAudit{
			AuditPromptPath: AuditPromptPath,
			RequiredFields:  RequiredFields,
		},
	}
}
