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
// Пусто намеренно: набор называет человек, а вывести его из живой записи нельзя — пустое поле
// бывает законным. Пустой список означает «проверка заполненности выключена», и это рабочее
// состояние задачи, а не недоделка.
var RequiredFields []string

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
			AuditPromptPath: AuditPromptPath,
			RequiredFields:  RequiredFields,
		},
	}
}
