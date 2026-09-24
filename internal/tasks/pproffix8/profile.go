// Package pproffix8 задаёт конфигурацию задачи pprof_fix_8.
//
// Восьмая задача правки — точечные правки отдельных страниц dpoprof.ru по просьбе владельца:
// пачки у неё нет, страницы приходят по одной и правятся каждая по-своему. Поэтому правка
// готовится заранее и лежит в PreparedDir: общего регламента, по которому модель могла бы
// пройти всю пачку, у такой задачи не бывает, а проверять готовый текст глазами дешевле, чем
// описывать правило ради одной страницы.
//
// Заголовок записи не меняется, как у pprof_fix_3, pprof_fix_5 и pprof_fix_7.
//
// Страницы у задачи — услуги площадки (ServicePages): они заведены типами каталога
// (obuch, perepod, povysh …), и тремя типами обычной правки не нашлась бы ни одна.
//
// От остальных пакетов задач пакет не зависит и зависеть не должен.
package pproffix8

import (
	"github.com/foxylis237/seo-pipeline/internal/pipeline/articlefix"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// Имя задачи в двух формах: подчёркнутая идёт в логи, каталоги и схему PostgreSQL,
// дефисная — то, что человек пишет в CLI и в make.
const (
	Name    = "pprof_fix_8"
	Command = "pprof-fix-8"
)

// Stages — стадии, без которых схема задачи неполна. Стадия одна: правка.
var Stages = []string{articlefix.StageRewrite}

// Пути задачи. Собраны здесь, а не разбросаны по вызовам: правило раскладки одно.
const (
	InputDir     = "input/pprof_fix_8"
	OutputDir    = "tasks/pprof_fix_8/output"
	PromptsDir   = "tasks/pprof_fix_8/prompts"
	TemplatePath = "tasks/pprof_fix_8/templates/result.md.tmpl"
	// RewritePromptPath — регламент правки: по нему готовились правки из PreparedDir.
	RewritePromptPath = "tasks/pprof_fix_8/prompts/rewrite.txt"
	// PreparedDir — готовые правки страниц. Лежат во входе задачи, а не в артефактах:
	// это данные для прогона, и reset, который чистит OUTPUT_DIR, стирать их права не имеет.
	PreparedDir = "input/pprof_fix_8/prepared"
)

// Profile возвращает конфигурацию pprof_fix_8.
func Profile() tasks.Profile {
	return tasks.Profile{
		Name:                 Name,
		Command:              Command,
		InputDir:             InputDir,
		OutputDir:            OutputDir,
		PromptsDir:           PromptsDir,
		TemplatePath:         TemplatePath,
		LLMConfigPath:        "config/pprof_fix_8.yaml",
		LLMOverlayPath:       "",
		ImportReportsDir:     "output/pprof_fix_8/import-reports",
		DiagnosticsDir:       "output/pprof_fix_8/debug",
		DBSchema:             Name,
		EnvPrefix:            "PPROF_FIX_8_",
		WithoutMetadataStage: true,
		LLMStages:            Stages,
		ArticleFix: &tasks.ArticleFix{
			RewritePromptPath: RewritePromptPath,
			// Пусто — название записи остаётся как есть: правится тело страницы.
			TitleRulePath: "",
			PreparedDir:   PreparedDir,
			// Страницы задачи — услуги площадки, они живут в типах каталога.
			ServicePages: true,
		},
	}
}
