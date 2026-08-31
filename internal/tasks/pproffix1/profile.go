// Package pproffix1 задаёт конфигурацию задачи pprof_fix_1.
//
// Кода потока здесь нет: правку опубликованных статей целиком делает общий движок
// internal/pipeline/articlefix. Своими у задачи остаются каталоги, схема PostgreSQL, текст
// промпта и правило переименования заголовков — всё это данные, а не логика.
//
// От остальных задач пакет не зависит и зависеть не должен.
package pproffix1

import (
	"github.com/foxylis237/seo-pipeline/internal/pipeline/articlefix"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// Имя задачи в двух формах: подчёркнутая идёт в логи, каталоги и схему PostgreSQL,
// дефисная — то, что человек пишет в CLI и в make.
const (
	Name    = "pprof_fix_1"
	Command = "pprof-fix-1"
)

// Stages — стадии, без которых схема задачи неполна. Стадия одна: правка.
var Stages = []string{articlefix.StageRewrite}

// Пути задачи. Собраны здесь, а не разбросаны по вызовам: правило раскладки одно.
const (
	InputDir     = "input/pprof_fix_1"
	OutputDir    = "tasks/pprof_fix_1/output"
	PromptsDir   = "tasks/pprof_fix_1/prompts"
	TemplatePath = "tasks/pprof_fix_1/templates/result.md.tmpl"
	// TitleRulePath — файл с примером переименования: одна пара «было — стало» на всю пачку.
	TitleRulePath = "tasks/pprof_fix_1/title_rule.txt"
	// RewritePromptPath — регламент правки. Тот же файл называет и схема стадий: путь
	// нужен ещё и сборке промпта, поэтому имя одно на оба места.
	RewritePromptPath = "tasks/pprof_fix_1/prompts/rewrite.txt"
)

// Profile возвращает конфигурацию pprof_fix_1.
//
// ExtraInputColumns пуст, и это не упущение: таблиц движка у задачи нет вовсе, article_inputs
// она не заполняет и проверку схемы движка не проходит — её собственная схема описана
// migrations/pprof_fix_1 и проверяется самой задачей.
func Profile() tasks.Profile {
	return tasks.Profile{
		Name:                 Name,
		Command:              Command,
		InputDir:             InputDir,
		OutputDir:            OutputDir,
		PromptsDir:           PromptsDir,
		TemplatePath:         TemplatePath,
		LLMConfigPath:        "config/pprof_fix_1.yaml",
		LLMOverlayPath:       "",
		ImportReportsDir:     "output/pprof_fix_1/import-reports",
		DiagnosticsDir:       "output/pprof_fix_1/debug",
		DBSchema:             Name,
		EnvPrefix:            "PPROF_FIX_1_",
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
