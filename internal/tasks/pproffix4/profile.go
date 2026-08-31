// Package pproffix4 задаёт конфигурацию задачи pprof_fix_4.
//
// Четвёртая задача правки уже опубликованных статей. Устроена как pprof_fix_2: тот же поток
// fetch → rewrite → update из internal/pipeline/articlefix, та же одна таблица, те же три
// операции. Своими у задачи остаются промпт правки, правило переименования, каталоги и схема
// PostgreSQL.
//
// Сейчас задача меняет только заголовок: «в ФИС ФРДО в Москве» → «сведений в ЕГИСЗ». Текст
// статьи не трогается и модель не спрашивается — за это отвечает ArticleFix.TextUnchanged.
// Промпт правки лежит на диске черновиком (копия pprof_fix_2) и ждёт своего регламента.
//
// Отдельная задача, а не второй промпт у соседа: у каждой своя пачка статей, своя схема и
// своя отметка «переписана». Общий на двоих список означал бы, что прогон одной правки
// открывает статьи другой.
//
// От pproffix1, pproffix2 и pproffix3 пакет не зависит и зависеть не должен.
package pproffix4

import (
	"github.com/foxylis237/seo-pipeline/internal/pipeline/articlefix"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// Имя задачи в двух формах: подчёркнутая идёт в логи, каталоги и схему PostgreSQL,
// дефисная — то, что человек пишет в CLI и в make.
const (
	Name    = "pprof_fix_4"
	Command = "pprof-fix-4"
)

// Stages — стадии, без которых схема задачи неполна. Стадия одна: правка.
var Stages = []string{articlefix.StageRewrite}

// Пути задачи. Собраны здесь, а не разбросаны по вызовам: правило раскладки одно.
const (
	InputDir     = "input/pprof_fix_4"
	OutputDir    = "tasks/pprof_fix_4/output"
	PromptsDir   = "tasks/pprof_fix_4/prompts"
	TemplatePath = "tasks/pprof_fix_4/templates/result.md.tmpl"
	// TitleRulePath — файл с примером переименования: одна пара «было — стало» на всю пачку.
	TitleRulePath = "tasks/pprof_fix_4/title_rule.txt"
	// RewritePromptPath — регламент правки. Тот же файл называет и схема стадий: путь
	// нужен ещё и сборке промпта, поэтому имя одно на оба места.
	RewritePromptPath = "tasks/pprof_fix_4/prompts/rewrite.txt"
)

// Profile возвращает конфигурацию pprof_fix_4.
//
// ExtraInputColumns пуст по той же причине, что и у соседей: таблиц движка у задачи нет
// вовсе, её собственная схема описана migrations/pprof_fix_4 и проверяется самой задачей.
func Profile() tasks.Profile {
	return tasks.Profile{
		Name:                 Name,
		Command:              Command,
		InputDir:             InputDir,
		OutputDir:            OutputDir,
		PromptsDir:           PromptsDir,
		TemplatePath:         TemplatePath,
		LLMConfigPath:        "config/pprof_fix_4.yaml",
		LLMOverlayPath:       "",
		ImportReportsDir:     "output/pprof_fix_4/import-reports",
		DiagnosticsDir:       "output/pprof_fix_4/debug",
		DBSchema:             Name,
		EnvPrefix:            "PPROF_FIX_4_",
		WithoutMetadataStage: true,
		LLMStages:            Stages,
		// Непустое поле и есть признак задачи правки: по нему composition root уводит её на
		// поток articlefix, минуя таблицы и проверки движка генерации.
		ArticleFix: &tasks.ArticleFix{
			RewritePromptPath: RewritePromptPath,
			TitleRulePath:     TitleRulePath,
			// Пока задача меняет только заголовок: правило переименования согласовано, а
			// регламент правок текста ещё пишется. Промпт на диске лежит черновиком
			// (копия pprof_fix_2) и в модель не уходит — снимается признак вместе с
			// готовым промптом.
			TextUnchanged: true,
		},
	}
}
