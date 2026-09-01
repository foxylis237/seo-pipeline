// Package article содержит модели SEO-пайплайна.
package article

import (
	"fmt"
	"strings"
	"time"
)

// Article представляет статью, сохранённую в PostgreSQL.
type Article struct {
	ID           int64
	ExternalID   string
	Title        string
	Slug         string
	ReferenceURL string
	Status       string
	CurrentStep  *string
	ErrorMessage *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Состояния публикации статьи в WordPress — значения колонки articles.wordpress_status.
//
// NULL среди них нет: колонка NOT NULL с умолчанием WordPressNotPublished, а CHECK-констрейнт
// не пропускает ничего постороннего. Состояние «а тут NULL» означало бы лишнюю ветку в каждом
// предикате выборки и в каждом сравнении.
const (
	// WordPressNotPublished — статьи в блоге нет.
	WordPressNotPublished = "not_published"
	// WordPressPublished — запись создал наш publisher: полный набор полей ушёл одним
	// вызовом и был сверен обратным чтением.
	WordPressPublished = "published"
	// WordPressLinked — запись существовала до нас, её нашли и привязали командой
	// mark-published. Чем она заполнена, приложение не знает и не проверяло.
	//
	// Отдельное состояние, а не то же published: иначе исчез бы ответ на вопрос «эта запись
	// собрана нами или руками», а он определяет, чему верить при расхождении записи с
	// артефактами статьи.
	WordPressLinked = "linked"
)

// Publication — состояние публикации одной статьи.
//
// PostID и URL пусты, пока запись неизвестна: у привязанной вручную без указания записи они
// так и остаются пустыми.
type Publication struct {
	Status string
	PostID *int64
	URL    string
}

// InWordPress отвечает на вопрос, ради которого состояние и хранится: есть ли статья в блоге.
//
// Оба непустых состояния отвечают «да». Защита от дублей их не различает — повторная
// публикация одинаково недопустима и для созданной нами записи, и для привязанной.
func (p Publication) InWordPress() bool {
	return p.Status == WordPressPublished || p.Status == WordPressLinked
}

// CreatedByPipeline отличает запись, собранную нашим publisher, от привязанной вручную.
func (p Publication) CreatedByPipeline() bool { return p.Status == WordPressPublished }

// PublicationInput — всё, из чего собирается запись WordPress.
//
// Собирается репозиторием одним запросом. Времени чтения здесь нет намеренно: оно нигде не
// хранится, а готовое значение лежит в result.md — читать его файлом обязан вызывающий.
type PublicationInput struct {
	Article         Article
	Publication     Publication
	Category        string
	Tags            string
	Keyword         string
	MetaDescription string
	Header          string
	TLDR            string
	FAQ             string
	// Поля ниже приходят из колонок, которые заводит не каждая задача, и у задачи без такой
	// колонки остаются пустыми. Требовать их обязана та раскладка полей площадки, которой
	// они действительно нужны, а не общая проверка публикации.
	SEOTitle   string
	Teachers   string
	Profession string
	// Author — авторы статьи из книги импорта, через запятую. Публикация ищет по ним карточку
	// на сайте и связывает с ней запись.
	Author string
	// HTMLPath — путь к article.html относительно OUTPUT_DIR. Пуст, если стадия html не
	// доходила до конца: тогда публиковать нечего.
	HTMLPath string
}

// ErrorRecord is one immutable processing failure stored for an article.
type ErrorRecord struct {
	ID           int64
	ArticleID    int64
	ExternalID   string
	ArticleTitle string
	Step         *string
	Operation    *string
	ErrorMessage string
	Retryable    bool
	CreatedAt    time.Time
}

// ArticleError describes the current blocking error and, when available, its latest history entry.
type ArticleError struct {
	Article
	Operation *string
	Retryable *bool
	ErrorTime *time.Time
}

// Input is one imported Excel row. The tags are what prepare diagnostics writes to
// prepare/input.json; nothing decodes this type from JSON.
type Input struct {
	ExcelID         int    `json:"excel_id"`
	Title           string `json:"title"`
	Header          string `json:"header"`
	ImageSlug       string `json:"image_slug"`
	MetaDescription string `json:"meta_description"`
	Keyword         string `json:"key_word"`
	ReferenceURL    string `json:"reference_url"`
	Category        string `json:"category"`
	Author          string `json:"author"`
	Links           string `json:"links"`
	Professions     string `json:"professions"`
	// Tags — метки публикации, готовые данные из Excel. Моделью не генерируются.
	Tags string `json:"tags"`

	// Поля ниже хранятся не у всякой задачи: колонки под них заводит только та схема
	// PostgreSQL, чей профиль их объявил (Profile.ExtraInputColumns). Импортёр читает их
	// всегда — колонки в книге просто нет, и значение остаётся пустым, — а до базы они
	// доходят лишь там, где колонка существует. Так набор полей одной задачи не протекает
	// в таблицы другой.

	// SEOTitle — колонка «сео-заголовок»: заголовок страницы для выдачи, отдельный от Title.
	SEOTitle string `json:"seo_title"`
	// Section — колонка «раздел»: рубрика верхнего уровня, крупнее Category.
	Section string `json:"section"`
	// Profession — колонка profession: название профессии, о которой страница. Не путать с
	// Professions — там список похожих профессий для перелинковки.
	Profession string `json:"profession"`
	// Teachers — преподаватели программы. Приходит из той же колонки Excel authors, что и
	// Author: имя поля отражает смысл у задачи, которая его завела.
	Teachers string `json:"teachers"`
	// ServiceName — колонка service_name: короткое название самой услуги. Отдельно от Title:
	// там полное название страницы с уточнениями («… — дистанционное обучение с практикой»),
	// здесь только услуга («Медицинский массаж»).
	ServiceName string `json:"service_name"`
}

// ImportedArticle связывает статью с сохранённой при импорте строкой Excel.
// HasInput отличает отсутствующую строку article_inputs от строки с пустыми полями.
type ImportedArticle struct {
	Article  Article
	Input    Input
	HasInput bool
}

// KeywordFrequency содержит запрос и его частотность Wordstat.
type KeywordFrequency struct {
	Query     string `json:"query"`
	Frequency int    `json:"frequency"`
}

// FormatKeywords готовит ключи для подстановки в промпт: запрос, табуляция, частотность.
// Формат принадлежит модели, потому что его подставляют и боевые стадии, и demo-сборка.
func FormatKeywords(keywords []KeywordFrequency) string {
	var result strings.Builder
	for index, keyword := range keywords {
		if index > 0 {
			result.WriteByte('\n')
		}
		fmt.Fprintf(&result, "%s\t%d", keyword.Query, keyword.Frequency)
	}
	return result.String()
}

// GenerationInput contains persisted research required by implemented generation stages.
//
// Professions, Links и Teachers приходят из колонок, которые заводит не каждая задача:
// у задачи без такой колонки поле остаётся пустым, а её промпты на него не ссылаются.
type GenerationInput struct {
	Article             Article
	CompetitorStructure string
	WordstatKeywords    []KeywordFrequency
	LSIWords            []string
	Professions         string
	Links               string
	Teachers            string
}

// SavedGenerationInput contains persisted artifacts required to resume one LLM stage.
type SavedGenerationInput struct {
	Article          Article
	Professions      string
	Links            string
	StructurePath    string
	ArticlePath      string
	ReviewPath       string
	FixedArticlePath string
}

// ResultInput contains persisted fields used to assemble result.md.
type ResultInput struct {
	Article         Article
	Category        string
	Tags            string
	TLDR            string
	FAQ             string
	AdditionalInfo  string
	Professions     string
	Author          string
	Keyword         string
	MetaDescription string
	Header          string
	ArticlePath     string
	// FixedArticlePath — финальный текст статьи, если задача правит написанное отдельной
	// стадией. У pprof_1 и pprof_2 это результат ревью, у task_1 — результат fix; пусто, пока
	// стадия не отработала. ArticlePath рядом остаётся тем, что написала стадия article, и
	// шаблон result.md задачи сам выбирает, какой из двух путей показать человеку.
	FixedArticlePath string
	HTMLPath         string
	// GoogleDocURL — адрес документа с промптом статьи. Пуст, пока промпт не опубликован:
	// result.md печатает раздел в любом случае, но с пустым значением.
	GoogleDocURL string
	// WordPressURL — адрес опубликованной записи. Пуст, пока статья не опубликована, и
	// остаётся пустым у статей, отмеченных вручную: у них записи мы не знаем.
	WordPressURL string

	// Поля ниже приходят из колонок, которые заводит не каждая задача. У задачи без такой
	// колонки они остаются пустыми, и её шаблон result.md на них не ссылается.
	SEOTitle    string
	Section     string
	Profession  string
	Teachers    string
	ServiceName string
}
