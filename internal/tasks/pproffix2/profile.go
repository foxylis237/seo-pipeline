// Package pproffix2 задаёт конфигурацию задачи pprof_fix_2.
//
// Вторая задача правки уже опубликованных статей. От pprof_fix_1 она отличается ровно двумя
// файлами — промптом правки и правилом переименования заголовков; поток, вход, раскладка
// артефактов и таблица у них общие и живут в internal/pipeline/articlefix.
//
// Отдельная задача, а не второй промпт у первой: у каждой своя пачка статей, своя схема
// PostgreSQL и своя отметка «переписана». Общий на двоих список статей означал бы, что
// прогон второй правки открывает статьи первой.
//
// От pproffix1 пакет не зависит и зависеть не должен.
package pproffix2

import (
	"github.com/foxylis237/seo-pipeline/internal/pipeline/articlefix"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// Имя задачи в двух формах: подчёркнутая идёт в логи, каталоги и схему PostgreSQL,
// дефисная — то, что человек пишет в CLI и в make.
const (
	Name    = "pprof_fix_2"
	Command = "pprof-fix-2"
)

// Stages — стадии, без которых схема задачи неполна. Стадия одна: правка.
var Stages = []string{articlefix.StageRewrite}

// Пути задачи. Собраны здесь, а не разбросаны по вызовам: правило раскладки одно.
const (
	InputDir     = "input/pprof_fix_2"
	OutputDir    = "tasks/pprof_fix_2/output"
	PromptsDir   = "tasks/pprof_fix_2/prompts"
	TemplatePath = "tasks/pprof_fix_2/templates/result.md.tmpl"
	// TitleRulePath — файл с примером переименования: одна пара «было — стало» на всю пачку.
	TitleRulePath = "tasks/pprof_fix_2/title_rule.txt"
	// RewritePromptPath — регламент правки. Тот же файл называет и схема стадий: путь
	// нужен ещё и сборке промпта, поэтому имя одно на оба места.
	RewritePromptPath = "tasks/pprof_fix_2/prompts/rewrite.txt"
)

// Profile возвращает конфигурацию pprof_fix_2.
//
// ExtraInputColumns пуст по той же причине, что и у pprof_fix_1: таблиц движка у задачи нет
// вовсе, её собственная схема описана migrations/pprof_fix_2 и проверяется самой задачей.
func Profile() tasks.Profile {
	return tasks.Profile{
		Name:                 Name,
		Command:              Command,
		InputDir:             InputDir,
		OutputDir:            OutputDir,
		PromptsDir:           PromptsDir,
		TemplatePath:         TemplatePath,
		LLMConfigPath:        "config/pprof_fix_2.yaml",
		LLMOverlayPath:       "",
		ImportReportsDir:     "output/pprof_fix_2/import-reports",
		DiagnosticsDir:       "output/pprof_fix_2/debug",
		DBSchema:             Name,
		EnvPrefix:            "PPROF_FIX_2_",
		WithoutMetadataStage: true,
		LLMStages:            Stages,
		// Непустое поле и есть признак задачи правки: по нему composition root уводит её на
		// поток articlefix, минуя таблицы и проверки движка генерации.
		ArticleFix: &tasks.ArticleFix{
			RewritePromptPath: RewritePromptPath,
			TitleRulePath:     TitleRulePath,
		},
	}
}
