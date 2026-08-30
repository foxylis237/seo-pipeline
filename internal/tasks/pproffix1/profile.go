// Package pproffix1 задаёт конфигурацию задачи pprof_fix_1 и её поток правки.
//
// Задача отличается от остальных не промптами, а направлением: она не пишет статью, а
// изменяет уже опубликованную. Отсюда и всё остальное — свой вход (ссылки, а не книга
// Excel), свои таблицы, свой короткий поток fetch → rewrite → update и полное отсутствие
// research, структуры, метаданных и обложек.
//
// От pprof_1 и pprof_2 пакет не зависит и зависеть не должен.
package pproffix1

import "github.com/foxylis237/seo-pipeline/internal/tasks"

// Имя задачи в двух формах: подчёркнутая идёт в логи, каталоги и схему PostgreSQL,
// дефисная — то, что человек пишет в CLI и в make.
const (
	Name    = "pprof_fix_1"
	Command = "pprof-fix-1"
)

// StageRewrite — единственная стадия задачи: правка опубликованного текста.
//
// Она же единственный запрос к модели за прогон. Заголовок моделью не спрашивается: его
// меняет правило переименования (TitleRule), выведенное из одной пары «было — стало».
const StageRewrite = "rewrite"

// Stages — стадии, без которых схема задачи неполна.
var Stages = []string{StageRewrite}

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
	}
}
