// Package pproffix3 задаёт конфигурацию задачи pprof_fix_3.
//
// Третья задача правки уже опубликованных статей. От pprof_fix_1 и pprof_fix_2 она
// отличается одним: заголовок она не трогает вовсе — ни название записи, ни видимый H1,
// ни SEO-заголовок. Меняется только текст статьи, и меняет его промпт правки.
//
// Технически это выражено пустым ArticleFix.TitleRulePath: composition root подставляет
// вместо файла articlefix.KeepTitle, и поток остаётся тем же самым. Ни строчки потока под
// эту задачу править не пришлось — переименование с самого начала было интерфейсом.
//
// От pproffix1 и pproffix2 пакет не зависит и зависеть не должен.
package pproffix3

import (
	"github.com/foxylis237/seo-pipeline/internal/pipeline/articlefix"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// Имя задачи в двух формах: подчёркнутая идёт в логи, каталоги и схему PostgreSQL,
// дефисная — то, что человек пишет в CLI и в make.
const (
	Name    = "pprof_fix_3"
	Command = "pprof-fix-3"
)

// Stages — стадии, без которых схема задачи неполна. Стадия одна: правка.
var Stages = []string{articlefix.StageRewrite}

// Пути задачи. Собраны здесь, а не разбросаны по вызовам: правило раскладки одно.
//
// TitleRulePath среди них нет намеренно: файла с парой «было — стало» у задачи не
// существует, потому что переименовывать нечего.
const (
	InputDir     = "input/pprof_fix_3"
	OutputDir    = "tasks/pprof_fix_3/output"
	PromptsDir   = "tasks/pprof_fix_3/prompts"
	TemplatePath = "tasks/pprof_fix_3/templates/result.md.tmpl"
	// RewritePromptPath — регламент правки. Тот же файл называет и схема стадий: путь
	// нужен ещё и сборке промпта, поэтому имя одно на оба места.
	RewritePromptPath = "tasks/pprof_fix_3/prompts/rewrite.txt"
)

// Profile возвращает конфигурацию pprof_fix_3.
//
// ExtraInputColumns пуст по той же причине, что и у соседей: таблиц движка у задачи нет
// вовсе, её собственная схема описана migrations/pprof_fix_3 и проверяется самой задачей.
func Profile() tasks.Profile {
	return tasks.Profile{
		Name:                 Name,
		Command:              Command,
		InputDir:             InputDir,
		OutputDir:            OutputDir,
		PromptsDir:           PromptsDir,
		TemplatePath:         TemplatePath,
		LLMConfigPath:        "config/pprof_fix_3.yaml",
		LLMOverlayPath:       "",
		ImportReportsDir:     "output/pprof_fix_3/import-reports",
		DiagnosticsDir:       "output/pprof_fix_3/debug",
		DBSchema:             Name,
		EnvPrefix:            "PPROF_FIX_3_",
		WithoutMetadataStage: true,
		LLMStages:            Stages,
		// Непустое поле и есть признак задачи правки: по нему composition root уводит её на
		// поток articlefix, минуя таблицы и проверки движка генерации.
		ArticleFix: &tasks.ArticleFix{
			RewritePromptPath: RewritePromptPath,
			// Пусто — заголовок остаётся как есть. См. doc пакета.
			TitleRulePath: "",
		},
	}
}
