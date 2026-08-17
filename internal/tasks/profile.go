// Package tasks описывает профиль задачи — всё, чем один пайплайн отличается от другого.
//
// Здесь живёт только тип. Значения задают пакеты самих задач (internal/tasks/task1,
// internal/tasks/pprof1), а собирает их в реестр composition root. Наружу из него уходят
// конкретные значения: путь, каталог, DSN. Ни один пакет движка (internal/pipeline) и ни одна
// интеграция профиль не принимают и о существовании задач не знают — иначе Profile превратился
// бы в service locator, а граница между задачей и движком исчезла бы.
package tasks

import "path/filepath"

// Profile — полный набор того, что задача не делит с другими задачами.
//
// Поля намеренно перечислены явно, а не выводятся из Name: InputPath у task_1 и pprof_1
// сегодня общий, и вычисление по имени сделало бы разведение путей позже невозможным без
// правки движка.
type Profile struct {
	// Name — подчёркнутая форма (task_1). Идёт в логи как поле task.
	Name string
	// Command — дефисная форма (task-1). Это первый аргумент CLI и цель Makefile.
	Command string

	// InputPath — Excel с исходными статьями. Общий у task_1 и pprof_1.
	InputPath string
	// OutputDir — корень артефактов статей, он же OUTPUT_DIR.
	OutputDir string
	// PromptsDir — каталог промптов задачи. Сами пути к промптам перечислены в LLM-конфиге;
	// здесь корень нужен тем частям, которые собирают путь сами (например, промпт DEMO).
	PromptsDir string
	// TemplatePath — шаблон result.md.
	TemplatePath string
	// LLMConfigPath и LLMOverlayPath — базовая схема стадий и наложение. Пустой overlay
	// означает, что у задачи одна схема и выбирать не из чего.
	LLMConfigPath  string
	LLMOverlayPath string
	// ImportReportsDir — отчёты импорта, лежат вне OutputDir.
	ImportReportsDir string
	// DiagnosticsDir — корень диагностики браузерных интеграций. Подкаталог сервиса выдаёт
	// DiagnosticsSubdir: сами интеграции про задачи не знают.
	DiagnosticsDir string
	// DBSchema — схема PostgreSQL. Изоляция задач в базе держится только на ней.
	DBSchema string
	// EnvPrefix — префикс переменных окружения, которыми профиль можно переопределить.
	// Пустой у task_1: у него исторические имена без префикса, и менять их нельзя.
	EnvPrefix string
	// LLMStages — стадии, без которых схема задачи неполна. Набор у задач разный: task_1
	// генерирует статью шестью стадиями, pprof_1 — своими. Валидация конфигурации сверяется
	// с этим списком, а не с общим на весь проект.
	LLMStages []string
}

// DiagnosticsSubdir возвращает корень диагностики одного сервиса.
//
// Интеграции получают уже готовый путь и остаются task-agnostic: в них не должно быть ни
// имени задачи, ни знания о том, что задач больше одной.
func (p Profile) DiagnosticsSubdir(service string) string {
	return filepath.Join(p.DiagnosticsDir, service)
}
