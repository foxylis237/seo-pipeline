// Package pproffix7 задаёт конфигурацию задачи pprof_fix_7.
//
// Седьмая задача правки уже опубликованных страниц — статьи блога из пачки аудита
// pprof_audit_1. Меняет она в статье ровно одну строку: заголовок последнего раздела.
// «Вывод» и «Заключение» — это пересказ, а не призыв, тогда как сам раздел давно написан
// призывом и заканчивается «Оставьте заявку … в ДПО ПРОФ». Новый заголовок той же формы,
// что ставит генерация pprof_1: глагол в повелительном наклонении, тема статьи, «в ДПО ПРОФ»
// на конце.
//
// Заголовок записи при этом не меняется — как у pprof_fix_3 и pprof_fix_5: правится тело, а
// название статьи в блоге и её адрес остаются прежними.
//
// Источник правки — PreparedDir, как у pprof_fix_6: текст меняется на одну строку, и просить
// у модели вернуть статью целиком ради неё значило бы платить за риск потерять половину
// страницы. Заголовки разделов задача меняет по заданию, поэтому признак обрыва у неё
// структурный (HeadingsRewritten): дословная сверка последнего заголовка объявляла бы обрывом
// каждую статью.
//
// От остальных пакетов задач пакет не зависит и зависеть не должен.
package pproffix7

import (
	"github.com/foxylis237/seo-pipeline/internal/pipeline/articlefix"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// Имя задачи в двух формах: подчёркнутая идёт в логи, каталоги и схему PostgreSQL,
// дефисная — то, что человек пишет в CLI и в make.
const (
	Name    = "pprof_fix_7"
	Command = "pprof-fix-7"
)

// Stages — стадии, без которых схема задачи неполна. Стадия одна: правка.
var Stages = []string{articlefix.StageRewrite}

// Пути задачи. Собраны здесь, а не разбросаны по вызовам: правило раскладки одно.
const (
	InputDir     = "input/pprof_fix_7"
	OutputDir    = "tasks/pprof_fix_7/output"
	PromptsDir   = "tasks/pprof_fix_7/prompts"
	TemplatePath = "tasks/pprof_fix_7/templates/result.md.tmpl"
	// RewritePromptPath — регламент правки: по нему готовились правки из PreparedDir.
	RewritePromptPath = "tasks/pprof_fix_7/prompts/rewrite.txt"
	// PreparedDir — готовые правки статей. Лежат во входе задачи, а не в артефактах:
	// это данные для прогона, и reset, который чистит OUTPUT_DIR, стирать их права не имеет.
	PreparedDir = "input/pprof_fix_7/prepared"
)

// Profile возвращает конфигурацию pprof_fix_7.
func Profile() tasks.Profile {
	return tasks.Profile{
		Name:                 Name,
		Command:              Command,
		InputDir:             InputDir,
		OutputDir:            OutputDir,
		PromptsDir:           PromptsDir,
		TemplatePath:         TemplatePath,
		LLMConfigPath:        "config/pprof_fix_7.yaml",
		LLMOverlayPath:       "",
		ImportReportsDir:     "output/pprof_fix_7/import-reports",
		DiagnosticsDir:       "output/pprof_fix_7/debug",
		DBSchema:             Name,
		EnvPrefix:            "PPROF_FIX_7_",
		WithoutMetadataStage: true,
		LLMStages:            Stages,
		ArticleFix: &tasks.ArticleFix{
			RewritePromptPath: RewritePromptPath,
			// Пусто — заголовок записи остаётся как есть: меняется заголовок раздела внутри
			// статьи, а не её название.
			TitleRulePath: "",
			// Правка меняет заголовок последнего раздела, поэтому дословная сверка последнего
			// заголовка объявляла бы обрывом каждую статью.
			HeadingsRewritten: true,
			PreparedDir:       PreparedDir,
			// Пачка — статьи блога: обычные записи, их находит общий список типов.
			ServicePages: false,
		},
	}
}
