// Package task1 задаёт конфигурацию задачи task_1.
//
// Здесь нет ни одной строчки о том, как работает пайплайн: это знает internal/pipeline.
// Пакет отвечает только на вопрос «где лежит task_1 и чем он настроен».
package task1

import "github.com/foxylis237/seo-pipeline/internal/tasks"

// Имя задачи в двух формах: подчёркнутая идёт в логи, каталоги и схему PostgreSQL,
// дефисная — то, что человек пишет в CLI и в make.
const (
	Name    = "task_1"
	Command = "task-1"
)

// Stages — стадии схемы task_1. Кроме шести стадий генерации сюда входит keywords: она
// выполняется не в пайплайне, а в prepare, но промпт и маршрут ей нужны такие же
// обязательные — иначе отсутствие резервного источника запросов выяснится только тогда,
// когда Keys.so уже вернул пустой результат.
var Stages = []string{"structure", "article", "info", "review", "fix", "html", "keywords"}

// Profile возвращает конфигурацию task_1.
//
// Все значения здесь исторические и менять их нельзя: задача в проде, и любое расхождение
// молча уводит её в другие каталоги или в другую схему PostgreSQL.
func Profile() tasks.Profile {
	return tasks.Profile{
		Name:             Name,
		Command:          Command,
		InputDir:         "input/task_1",
		OutputDir:        "tasks/task_1/output",
		PromptsDir:       "tasks/task_1/prompts",
		TemplatePath:     "tasks/task_1/templates/result.md.tmpl",
		LLMConfigPath:    "config/config.yaml",
		LLMOverlayPath:   "config/config.deepseek.yaml",
		ImportReportsDir: "output/task1/import-reports",
		DiagnosticsDir:   "output/task1/debug",
		DBSchema:         "public",
		EnvPrefix:        "",
		// author, links, professions и tags были общими колонками, пока их не перестала
		// использовать третья задача. Для task_1 нужны все: автор статьи, перелинковка,
		// список похожих профессий в result.md и метки публикации.
		ExtraInputColumns: []string{"author", "links", "professions", "tags"},
		LLMStages:         Stages,
	}
}
