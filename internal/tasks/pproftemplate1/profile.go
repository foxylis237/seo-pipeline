// Package pproftemplate1 задаёт конфигурацию задачи pprof_template_1 и её поток генерации.
//
// От pprof_1 задача отличается одним: форму статьи ей показывает эталон — образцовая статья,
// приложенная документом к стадиям structure и expert_seo. Всё, что можно увидеть в образце
// (композиция разделов, длина абзацев, манера, оформление блоков), из промптов убрано; в них
// осталось то, чего один пример не доказывает: входные данные, числовые нормы, формы записи,
// на которые завязан код разметки, и красные линии.
//
// Как работает пайплайн вообще — знает internal/pipeline. Здесь только то, чем задача
// отличается: где лежат её файлы и в каком порядке идут её стадии.
package pproftemplate1

import "github.com/foxylis237/seo-pipeline/internal/tasks"

// Имя задачи в двух формах: подчёркнутая идёт в логи, каталоги и схему PostgreSQL,
// дефисная — то, что человек пишет в CLI и в make.
const (
	Name    = "pprof_template_1"
	Command = "pprof-template-1"
)

// Имена стадий pprof_template_1.
//
// Словарь общий с pprof_1 там, где совпадают роли. Разошлась одна стадия: у pprof_1 текст
// пишет `expert`, а SEO-требования применяет отдельная редактура, здесь это одно и то же
// сообщение — специалист пишет статью и сразу расставляет запросы. Имя `expert_seo` называет
// именно слияние: под именем `expert` пришлось бы помнить, что оно значит у каждой задачи
// своё.
const (
	StageStructure = "structure"
	StageExpertSEO = "expert_seo"
	StageReview    = "review"
	StageInfo      = "info"
	StageHTML      = "html"

	// StageArticle — базовый промпт статьи. В LLM он не уходит: стадия существует только
	// чтобы собрать промпт из входных данных и research, сохранить его артефактом и отдать
	// в выгрузку. Текст статьи пишет StageExpertSEO по эталону.
	StageArticle = "article"

	// StageKeywords — резервный подбор исходных запросов для prepare. К генерации статьи
	// отношения не имеет, но схема без него неполна.
	StageKeywords = "keywords"
)

// Stages — стадии, без которых схема pprof_template_1 неполна.
var Stages = []string{
	StageKeywords, StageStructure, StageArticle,
	StageExpertSEO, StageReview, StageInfo, StageHTML,
}

// ReferenceDir — каталог эталонной статьи. Тот же путь называет схема стадий: документ
// прикрепляют structure и expert_seo, и имя каталога у них одно.
//
// Каталог, а не файл: имя эталона не фиксируется, его выбирает config.ResolveStageAttachments
// — ровно один документ в каталоге. Пустой каталог роняет стадию до обращения к модели.
const ReferenceDir = "input/pprof_template_1/reference/article"

// Profile возвращает конфигурацию pprof_template_1.
//
// Overlay пуст: задача DeepSeek-only, второй схемы стадий у неё нет.
func Profile() tasks.Profile {
	return tasks.Profile{
		Name:         Name,
		Command:      Command,
		InputDir:     "input/pprof_template_1",
		OutputDir:    "tasks/pprof_template_1/output",
		PromptsDir:   "tasks/pprof_template_1/prompts",
		TemplatePath: "tasks/pprof_template_1/templates/result.md.tmpl",
		// Под статьёй блога есть блок связанных курсов, и заполняем его мы: три услуги
		// подбираются по каталогу площадки (схема site).
		RelatedCourses:   true,
		LLMConfigPath:    "config/pprof_template_1.yaml",
		LLMOverlayPath:   "",
		ImportReportsDir: "output/pprof_template_1/import-reports",
		DiagnosticsDir:   "output/pprof_template_1/debug",
		DBSchema:         Name,
		EnvPrefix:        "PPROF_TEMPLATE_1_",
		// Папка Google Drive своя, но выгрузка пока выключена секцией pipeline конфига:
		// документ ищется по имени «Промт: <заголовок>», а заголовки статей этой задачи
		// совпадают с заголовками pprof_1, и общая папка означала бы перезапись чужого
		// промпта. Появится своя папка — сюда её адрес, и выгрузку можно включать.
		GoogleFolderURL: "",
		// Те же колонки, что и у pprof_1: задача пишет статьи блога, и автор, перелинковка,
		// похожие профессии и метки ей нужны.
		ExtraInputColumns: []string{"author", "links", "professions", "tags"},
		LLMStages:         Stages,
	}
}
