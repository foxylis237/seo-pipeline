// Package pprof2 задаёт конфигурацию задачи pprof_2 и её поток генерации.
//
// Как работает пайплайн вообще — знает internal/pipeline. Здесь только то, чем pprof_2
// отличается: где лежат его файлы, какие колонки есть в его схеме PostgreSQL и в каком
// порядке идут его стадии.
//
// От pprof_1 пакет не зависит и зависеть не должен: это два независимых профиля, и общее у
// них — движок, а не друг друга. Совпадение имён стадий тут случайно и ничего не связывает.
package pprof2

import "github.com/foxylis237/seo-pipeline/internal/tasks"

// Имя задачи в двух формах: подчёркнутая идёт в логи, каталоги и схему PostgreSQL,
// дефисная — то, что человек пишет в CLI и в make.
const (
	Name    = "pprof_2"
	Command = "pprof-2"
)

// Имена стадий pprof_2.
const (
	// StageStructure — чат 1: структура коммерческой страницы по структуре конкурентов.
	StageStructure = "structure"
	// StageArticle — чат 2: основной промпт, он же автор страницы.
	//
	// В отличие от pprof_1 этот промпт уходит в модель: текст пишет именно он. Тот же
	// отрендеренный текст сохраняется артефактом и выгружается в Google Docs — второго
	// «базового промпта, который никуда не отправляется» у задачи нет.
	StageArticle = "article"
	// StageSEOEditor и StageReview объявлены, но в поток не входят: сегодня страницу пишет
	// один промпт article, а SEO-редактура и ревью вернутся отдельными сообщениями после
	// него. Их промпты — заглушки, настройки стадий ждут в config/pprof_2.yaml.
	StageSEOEditor = "seo_editor"
	StageReview    = "review"
	// StageHTML — чат 3: разметка по регламенту. Перелинковки у pprof_2 нет — промпт
	// запрещает любые ссылки.
	StageHTML = "html"

	// StageKeywords — резервный подбор исходных запросов для prepare. К генерации страницы
	// отношения не имеет, но схема без него неполна.
	StageKeywords = "keywords"
)

// Stages — стадии, без которых схема pprof_2 неполна.
//
// Набор описывает сегодняшний поток: structure → article → html. Из него же строится
// проверка схемы и отчёт dry-run, поэтому seo_editor и review сюда не входят — их промпты
// заглушки, и требовать от схемы рабочие настройки для стадий, которые никто не вызывает,
// значит обещать несуществующее.
//
// Стадии info здесь нет и не появится: частые вопросы уже написаны в тексте страницы, и FAQ
// вынимается из него разбором (ExtractFAQ), а не отдельным запросом к модели.
var Stages = []string{
	StageKeywords, StageStructure, StageArticle, StageHTML,
}

// InputColumns — колонки article_inputs, которые есть только в схеме pprof_2.
//
// Базовый набор колонок общий у всех задач — это контракт движка. Здесь перечислено то, чего
// нет ни у task_1, ни у pprof_1, и заводит их отдельная миграция, применяемая только к схеме
// pprof_2: поля одной задачи не имеют права появляться в таблицах другой.
//
// Колонок links, professions и tags здесь нет, и это тоже объявление: перелинковки у задачи
// нет, похожих профессий она не печатает, меток в блог не публикует — значит и в схеме их
// быть не должно. Их удаляет собственная миграция задачи.
//
//	seo_title    — «сео-заголовок» из книги, отдельный от названия страницы;
//	section      — «раздел», рубрика верхнего уровня, крупнее категории;
//	profession   — название профессии, о которой страница;
//	teachers     — преподаватели программы; приходят колонкой teachers, она же authors;
//	service_name — короткое название услуги, отдельно от полного названия страницы.
var InputColumns = []string{"seo_title", "section", "profession", "teachers", "service_name"}

// GoogleFolderURL — папка Drive, в которой живут промпты pprof_2.
//
// Своя, а не общая: документ ищется по имени «Промт: <заголовок>», а заголовки страниц разных
// задач совпадают — одна папка на двоих означала бы перезапись чужого промпта.
const GoogleFolderURL = "https://drive.google.com/drive/folders/1Ty0ZEHp4IENjsjbM_KAeQZsiIuq2sb8g"

// Profile возвращает конфигурацию pprof_2.
//
// Overlay пуст: pprof_2 DeepSeek-only, второй схемы стадий у него нет и выбирать между ними
// не из чего.
func Profile() tasks.Profile {
	return tasks.Profile{
		Name:                 Name,
		Command:              Command,
		InputDir:             "input/pprof_2",
		OutputDir:            "tasks/pprof_2/output",
		PromptsDir:           "tasks/pprof_2/prompts",
		TemplatePath:         "tasks/pprof_2/templates/result.md.tmpl",
		LLMConfigPath:        "config/pprof_2.yaml",
		LLMOverlayPath:       "",
		ImportReportsDir:     "output/pprof_2/import-reports",
		DiagnosticsDir:       "output/pprof_2/debug",
		DBSchema:             Name,
		EnvPrefix:            "PPROF_2_",
		GoogleFolderURL:      GoogleFolderURL,
		ExtraInputColumns:    InputColumns,
		WithoutMetadataStage: true,
		MetadataFAQOnly:      true,
		LLMStages:            Stages,
	}
}
