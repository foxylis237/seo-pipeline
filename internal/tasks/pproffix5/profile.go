// Package pproffix5 задаёт конфигурацию задачи pprof_fix_5.
//
// Пятая задача правки уже опубликованных статей. Устроена как pprof_fix_2, но заголовок не
// трогает вовсе — ни название записи, ни видимый H1, ни SEO-заголовок: меняется только текст
// статьи, и меняет его промпт правки. Поток, вход, раскладка артефактов и таблица общие с
// соседями и живут в internal/pipeline/articlefix.
//
// Технически это выражено пустым ArticleFix.TitleRulePath: composition root подставляет
// вместо файла articlefix.KeepTitle, как у pprof_fix_3.
//
// Отдельная задача, а не второй промпт у первой: у каждой своя пачка статей, своя схема
// PostgreSQL и своя отметка «переписана». Общий на двоих список статей означал бы, что
// прогон одной правки открывает статьи другой.
//
// От pproffix1…pproffix4 пакет не зависит и зависеть не должен.
package pproffix5

import (
	"github.com/foxylis237/seo-pipeline/internal/pipeline/articlefix"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// Имя задачи в двух формах: подчёркнутая идёт в логи, каталоги и схему PostgreSQL,
// дефисная — то, что человек пишет в CLI и в make.
const (
	Name    = "pprof_fix_5"
	Command = "pprof-fix-5"
)

// Stages — стадии, без которых схема задачи неполна. Стадия одна: правка.
var Stages = []string{articlefix.StageRewrite}

// Пути задачи. Собраны здесь, а не разбросаны по вызовам: правило раскладки одно.
//
// TitleRulePath среди них нет намеренно: файла с парой «было — стало» у задачи не
// существует, потому что переименовывать нечего.
const (
	InputDir     = "input/pprof_fix_5"
	OutputDir    = "tasks/pprof_fix_5/output"
	PromptsDir   = "tasks/pprof_fix_5/prompts"
	TemplatePath = "tasks/pprof_fix_5/templates/result.md.tmpl"
	// RewritePromptPath — регламент правки. Тот же файл называет и схема стадий: путь
	// нужен ещё и сборке промпта, поэтому имя одно на оба места.
	RewritePromptPath = "tasks/pprof_fix_5/prompts/rewrite.txt"
)

// Profile возвращает конфигурацию pprof_fix_5.
//
// ExtraInputColumns пуст по той же причине, что и у соседей: таблиц движка у задачи нет
// вовсе, её собственная схема описана migrations/pprof_fix_5 и проверяется самой задачей.
func Profile() tasks.Profile {
	return tasks.Profile{
		Name:                 Name,
		Command:              Command,
		InputDir:             InputDir,
		OutputDir:            OutputDir,
		PromptsDir:           PromptsDir,
		TemplatePath:         TemplatePath,
		LLMConfigPath:        "config/pprof_fix_5.yaml",
		LLMOverlayPath:       "",
		ImportReportsDir:     "output/pprof_fix_5/import-reports",
		DiagnosticsDir:       "output/pprof_fix_5/debug",
		DBSchema:             Name,
		EnvPrefix:            "PPROF_FIX_5_",
		WithoutMetadataStage: true,
		LLMStages:            Stages,
		// Непустое поле и есть признак задачи правки: по нему composition root уводит её на
		// поток articlefix, минуя таблицы и проверки движка генерации.
		ArticleFix: &tasks.ArticleFix{
			RewritePromptPath: RewritePromptPath,
			// Промпт переводит страницу на НМО и переписывает заголовки разделов вместе с
			// текстом — значит, обрыв ответа ловится структурой, а не дословной сверкой
			// последнего заголовка. Иначе каждая статья уходила бы на дописывание.
			HeadingsRewritten: true,
			// Пусто — заголовок остаётся как есть. См. doc пакета.
			TitleRulePath: "",
		},
	}
}
