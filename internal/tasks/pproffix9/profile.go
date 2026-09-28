// Package pproffix9 задаёт конфигурацию задачи pprof_fix_9.
//
// Девятая задача правки уже опубликованных страниц — 88 статей блога, написанных задачей
// pprof_1 до появления у неё карточки призыва. Меняет она в статье ровно последний раздел:
// он становится финалом той же формы, что теперь пишет генерация pprof_1, — H2-призыв с
// «в ДПО ПРОФ» на конце, один абзац и под ним карточка ДПО ПРОФ с кнопкой «Записаться на
// обучение» по шаблону tasks/pprof_1/templates/cta_card.html. Если последний раздел
// содержательный, а не призыв, он остаётся, и финал встаёт после него.
//
// Заголовок записи, её адрес и поля не меняются — как у pprof_fix_7: правится только тело.
//
// Источник правки — PreparedDir: финалы написаны заранее, без модели в прогоне, а карточка
// собрана из того же шаблона, что ставит поток pprof_1. Последний заголовок правка меняет,
// поэтому признак обрыва у задачи структурный (HeadingsRewritten).
//
// От остальных пакетов задач пакет не зависит и зависеть не должен.
package pproffix9

import (
	"github.com/foxylis237/seo-pipeline/internal/pipeline/articlefix"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// Имя задачи в двух формах: подчёркнутая идёт в логи, каталоги и схему PostgreSQL,
// дефисная — то, что человек пишет в CLI и в make.
const (
	Name    = "pprof_fix_9"
	Command = "pprof-fix-9"
)

// Stages — стадии, без которых схема задачи неполна. Стадия одна: правка.
var Stages = []string{articlefix.StageRewrite}

// Пути задачи. Собраны здесь, а не разбросаны по вызовам: правило раскладки одно.
const (
	InputDir     = "input/pprof_fix_9"
	OutputDir    = "tasks/pprof_fix_9/output"
	PromptsDir   = "tasks/pprof_fix_9/prompts"
	TemplatePath = "tasks/pprof_fix_9/templates/result.md.tmpl"
	// RewritePromptPath — регламент правки: по нему готовились правки из PreparedDir.
	RewritePromptPath = "tasks/pprof_fix_9/prompts/rewrite.txt"
	// PreparedDir — готовые правки статей. Лежат во входе задачи, а не в артефактах:
	// это данные для прогона, и reset, который чистит OUTPUT_DIR, стирать их права не имеет.
	PreparedDir = "input/pprof_fix_9/prepared"
)

// Profile возвращает конфигурацию pprof_fix_9.
func Profile() tasks.Profile {
	return tasks.Profile{
		Name:                 Name,
		Command:              Command,
		InputDir:             InputDir,
		OutputDir:            OutputDir,
		PromptsDir:           PromptsDir,
		TemplatePath:         TemplatePath,
		LLMConfigPath:        "config/pprof_fix_9.yaml",
		LLMOverlayPath:       "",
		ImportReportsDir:     "output/pprof_fix_9/import-reports",
		DiagnosticsDir:       "output/pprof_fix_9/debug",
		DBSchema:             Name,
		EnvPrefix:            "PPROF_FIX_9_",
		WithoutMetadataStage: true,
		LLMStages:            Stages,
		ArticleFix: &tasks.ArticleFix{
			RewritePromptPath: RewritePromptPath,
			// Пусто — заголовок записи остаётся как есть: меняется последний раздел статьи,
			// а не её название.
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
