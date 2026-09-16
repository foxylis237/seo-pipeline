// Package pproffix6 задаёт конфигурацию задачи pprof_fix_6.
//
// Шестая задача правки уже опубликованных страниц — страниц услуг из пачки аудита
// pprof_audit_2. Меняет она две вещи: часы, срок и стоимость программы приводит к плашкам
// самой страницы, а описание обучения — к дистанционному формату, который назван на сайте.
// Заголовок не меняется, как у pprof_fix_3.
//
// От соседей её отличает источник правки: текст и ответы FAQ готовятся заранее и лежат в
// PreparedDir, а run их только сверяет с живой записью и записывает. Пачку правили
// параллельно, а браузерный профиль DeepSeek один на все процессы.
//
// От остальных пакетов задач пакет не зависит и зависеть не должен.
package pproffix6

import (
	"github.com/foxylis237/seo-pipeline/internal/pipeline/articlefix"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// Имя задачи в двух формах: подчёркнутая идёт в логи, каталоги и схему PostgreSQL,
// дефисная — то, что человек пишет в CLI и в make.
const (
	Name    = "pprof_fix_6"
	Command = "pprof-fix-6"
)

// Stages — стадии, без которых схема задачи неполна. Стадия одна: правка.
var Stages = []string{articlefix.StageRewrite}

// Пути задачи. Собраны здесь, а не разбросаны по вызовам: правило раскладки одно.
const (
	InputDir     = "input/pprof_fix_6"
	OutputDir    = "tasks/pprof_fix_6/output"
	PromptsDir   = "tasks/pprof_fix_6/prompts"
	TemplatePath = "tasks/pprof_fix_6/templates/result.md.tmpl"
	// RewritePromptPath — регламент правки: по нему готовились правки из PreparedDir.
	RewritePromptPath = "tasks/pprof_fix_6/prompts/rewrite.txt"
	// PreparedDir — готовые правки страниц. Лежат во входе задачи, а не в артефактах:
	// это данные для прогона, и reset, который чистит OUTPUT_DIR, стирать их права не имеет.
	PreparedDir = "input/pprof_fix_6/prepared"
)

// Profile возвращает конфигурацию pprof_fix_6.
func Profile() tasks.Profile {
	return tasks.Profile{
		Name:                 Name,
		Command:              Command,
		InputDir:             InputDir,
		OutputDir:            OutputDir,
		PromptsDir:           PromptsDir,
		TemplatePath:         TemplatePath,
		LLMConfigPath:        "config/pprof_fix_6.yaml",
		LLMOverlayPath:       "",
		ImportReportsDir:     "output/pprof_fix_6/import-reports",
		DiagnosticsDir:       "output/pprof_fix_6/debug",
		DBSchema:             Name,
		EnvPrefix:            "PPROF_FIX_6_",
		WithoutMetadataStage: true,
		LLMStages:            Stages,
		ArticleFix: &tasks.ArticleFix{
			RewritePromptPath: RewritePromptPath,
			// Пусто — заголовок остаётся как есть.
			TitleRulePath: "",
			PreparedDir:   PreparedDir,
			// Пачка — страницы услуг из аудита pprof_audit_2: они живут в типах каталога.
			ServicePages: true,
		},
	}
}
