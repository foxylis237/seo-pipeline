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

// Состояния публикации статьи в WordPress — значения колонки articles.wordpress_status (NOT NULL, CHECK).
const (
	// WordPressNotPublished — статьи в блоге нет.
	WordPressNotPublished = "not_published"
	// WordPressPublished — запись создал и сверил обратным чтением наш publisher.
	WordPressPublished = "published"
	// WordPressLinked — существовавшая запись, привязанная командой mark-published; её содержимое не проверялось.
	WordPressLinked = "linked"
)

// Publication — состояние публикации одной статьи; PostID и URL пусты, пока запись неизвестна.
type Publication struct {
	Status string
	PostID *int64
	URL    string
}

// InWordPress отвечает, есть ли статья в блоге: да и для published, и для linked.
func (p Publication) InWordPress() bool {
	return p.Status == WordPressPublished || p.Status == WordPressLinked
}

// CreatedByPipeline отличает запись, собранную нашим publisher, от привязанной вручную.
func (p Publication) CreatedByPipeline() bool { return p.Status == WordPressPublished }

// PublicationInput — всё, из чего собирается запись WordPress.
// Времени чтения здесь нет: оно не хранится в базе, вызывающий читает его из result.md.
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
	// Поля ниже есть не у каждой задачи; требует их раскладка площадки, а не общая проверка.
	SEOTitle   string
	Teachers   string
	Profession string
	// PostType — тип записи площадки, по нему же выбирается таксономия рубрики; пустой — обычная запись блога.
	PostType string
	// ImageSourceURL — адрес фотографии на стоке; пустой — без абзаца-ссылки под картинкой.
	ImageSourceURL string
	// Числа программы из книги для плашки параметров страницы услуги; пустые — параметра нет.
	Hours       string
	Duration    string
	Price       string
	Document    string
	Attestation string
	// Author — авторы статьи через запятую; по ним ищется карточка автора на сайте.
	Author string
	// Professions — похожие профессии через запятую для блока связанных курсов; первая — главная.
	Professions string
	// Links — программы, уже стоящие ссылками в тексте; блок связанных курсов их не повторяет.
	Links string
	// HTMLPath — путь к article.html относительно OUTPUT_DIR; пуст, пока стадия html не завершена.
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

	// Поля ниже хранятся только у задач, чей профиль объявил колонку (Profile.ExtraInputColumns).

	// SEOTitle — колонка «сео-заголовок»: заголовок страницы для выдачи, отдельный от Title.
	SEOTitle string `json:"seo_title"`
	// Section — колонка «раздел»: рубрика верхнего уровня, крупнее Category.
	Section string `json:"section"`
	// Profession — профессия, о которой страница; не путать со списком Professions.
	Profession string `json:"profession"`
	// Teachers — преподаватели программы; из той же колонки Excel authors, что и Author.
	Teachers string `json:"teachers"`
	// ServiceName — короткое название услуги («Медицинский массаж»), без уточнений из Title.
	ServiceName string `json:"service_name"`

	// Поля коммерческих страниц: числа программы берутся из книги, а не у модели — их же человек ставит в поля записи.

	// PostType — тип записи площадки (rabprof, povyshenie, …); таксономия рубрики — cat_<тип>.
	PostType string `json:"post_type"`
	// Hours — объём программы в академических часах.
	Hours string `json:"hours"`
	// Duration — срок обучения.
	Duration string `json:"duration"`
	// Price — стоимость обучения.
	Price string `json:"price"`
	// Document — документ, выдаваемый по итогам обучения.
	Document string `json:"document"`
	// Attestation — форма итоговой аттестации.
	Attestation string `json:"attestation"`
	// ImageSourceURL — адрес кадра на стоке, с которого взята обложка; пустой — без абзаца-ссылки.
	ImageSourceURL string `json:"image_source_url"`

	// CourseURL — адрес курса для кнопки призыва в конце статьи; выбирает человек, не модель.
	CourseURL string `json:"course_url"`
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

// FormatKeywords готовит ключи для промпта: запрос, табуляция, частотность.
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
type GenerationInput struct {
	Article             Article
	CompetitorStructure string
	WordstatKeywords    []KeywordFrequency
	LSIWords            []string
	Professions         string
	Links               string
	Teachers            string
	// Поля коммерческой страницы есть не у каждой задачи; числа программы модель не придумывает.
	Profession  string
	Hours       string
	Duration    string
	Price       string
	Document    string
	Attestation string
	// CourseURL — адрес курса для кнопки призыва.
	CourseURL string
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
	Article        Article
	Category       string
	Tags           string
	TLDR           string
	FAQ            string
	AdditionalInfo string
	Professions    string
	// Links — программы, стоящие ссылками в тексте; блок связанных курсов их не повторяет.
	Links           string
	Author          string
	Keyword         string
	MetaDescription string
	Header          string
	ArticlePath     string
	// FixedArticlePath — финальный текст после правки (ревью или fix); пуст, пока стадия не отработала.
	// Какой из путей показать, ArticlePath или этот, выбирает шаблон result.md задачи.
	FixedArticlePath string
	HTMLPath         string
	// GoogleDocURL — адрес документа с промптом статьи; пуст, пока промпт не опубликован.
	GoogleDocURL string
	// WordPressURL — адрес опубликованной записи; пуст, пока запись неизвестна.
	WordPressURL string

	// Поля ниже есть не у каждой задачи.
	SEOTitle    string
	Section     string
	Profession  string
	Teachers    string
	ServiceName string

	// Числа программы коммерческой страницы — для сверки книги с полями записи глазами.
	PostType       string
	Hours          string
	Duration       string
	Price          string
	Document       string
	Attestation    string
	ImageSourceURL string
}
