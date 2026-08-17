// Package pprof1 задаёт конфигурацию задачи pprof_1 и её поток генерации.
//
// Как работает пайплайн вообще — знает internal/pipeline. Здесь только то, чем pprof_1
// отличается: где лежат его файлы и в каком порядке идут его стадии.
package pprof1

import "github.com/foxylis237/seo-pipeline/internal/tasks"

// Имя задачи в двух формах: подчёркнутая идёт в логи, каталоги и схему PostgreSQL,
// дефисная — то, что человек пишет в CLI и в make.
const (
	Name    = "pprof_1"
	Command = "pprof-1"
)

// Имена стадий pprof_1.
//
// Они не совпадают с task_1 намеренно: поток другой, и переиспользование чужих имён
// («fix» для SEO-редактуры) означало бы два имени у одного понятия — ровно то, что
// docs/CONTEXT.md называет дефектом.
const (
	StageStructure = "structure"
	StageExpert    = "expert"
	StageSEOEditor = "seo_editor"
	StageInfo      = "info"
	StageReview    = "review"
	StageHTML      = "html"

	// StageArticle — старый базовый промпт статьи. В LLM он не уходит: стадия существует
	// только чтобы собрать промпт из входных данных и research, сохранить его артефактом и
	// выгрузить в Google Docs. Текст статьи пишет StageExpert.
	StageArticle = "article"

	// StageKeywords — резервный подбор исходных запросов для prepare. К генерации статьи
	// отношения не имеет, но схема без него неполна.
	StageKeywords = "keywords"
)

// Stages — стадии, без которых схема pprof_1 неполна.
var Stages = []string{
	StageKeywords, StageStructure, StageArticle,
	StageExpert, StageSEOEditor, StageInfo, StageReview, StageHTML,
}

// Profile возвращает конфигурацию pprof_1.
//
// Overlay пуст: pprof_1 сегодня DeepSeek-only, второй схемы стадий у него нет и выбирать
// между ними не из чего.
func Profile() tasks.Profile {
	return tasks.Profile{
		Name:             Name,
		Command:          Command,
		InputPath:        "input/task_1/input.xlsx",
		OutputDir:        "tasks/pprof_1/output",
		PromptsDir:       "tasks/pprof_1/prompts",
		TemplatePath:     "tasks/pprof_1/templates/result.md.tmpl",
		LLMConfigPath:    "config/pprof_1.yaml",
		LLMOverlayPath:   "",
		ImportReportsDir: "output/pprof_1/import-reports",
		DiagnosticsDir:   "output/pprof_1/debug",
		DBSchema:         Name,
		EnvPrefix:        "PPROF_1_",
		LLMStages:        Stages,
	}
}
