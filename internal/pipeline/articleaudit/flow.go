// Package articleaudit проверяет уже опубликованные страницы и складывает находки в отчёт.
//
// Поток общий у всех задач аудита; различают их промпт, список обязательных полей и каталоги —
// всё это приходит профилем задачи, а не именем в switch. От потока правки
// (internal/pipeline/articlefix) он отличается одним и жёстко: в блог не пишет ничего, и
// держит это не обещание, а интерфейс Blog — метода записи у него нет вовсе.
package articleaudit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/output"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
)

// StageAudit — единственная стадия задачи.
const StageAudit = "audit"

// Post — запись блога в том объёме, который нужен проверке.
//
// Свой тип, а не тип интеграции: задача не должна знать ни про XML-RPC, ни про REST, ни про
// то, что площадка — WordPress. Переходник живёт в composition root.
type Post struct {
	ID    int64
	Title string
	// Slug и PostType сверяются с входным файлом, когда запись найдена по идентификатору:
	// чужая страница внешне неотличима от своей, а проверять чужую хуже, чем не проверить.
	Slug        string
	PostType    string
	ContentHTML string
	Link        string
	// Fields — поля записи. Их проверяет код: модели они не показываются вовсе, она оценивает
	// саму статью.
	Fields map[string]string
	// ThumbnailID и TermIDs — обложка и рубрики записи. Полями они не являются, но человек
	// ждёт их в том же списке обязательного: для него это такие же графы, которые бывают не
	// заполнены.
	ThumbnailID int64
	TermIDs     map[string][]int64
}

// Blog — то, что поток требует от площадки. Два действия, оба читающие.
//
// Метода записи здесь нет и быть не должно: «аудит в блог не пишет» — это главное требование
// задачи, и держать его обещанием нельзя. Интерфейс без Write превращает требование в ошибку
// компиляции.
type Blog interface {
	// Find находит запись по слагу из её адреса.
	Find(ctx context.Context, slug string) (Post, error)
	// Read читает запись в том виде, в каком она лежит в базе сайта.
	Read(ctx context.Context, postID int64) (Post, error)
}

// Articles — то, что поток требует от хранилища страниц.
//
// Интерфейс объявлен здесь, у потребителя: поток проверяем на подделках, без PostgreSQL, и
// порядок его шагов — «копия страницы на диск раньше запроса к модели» — иначе проверить было
// бы нечем.
type Articles interface {
	Get(ctx context.Context, externalID string) (Article, error)
	MarkProcessing(ctx context.Context, externalID string) error
	SaveFetched(ctx context.Context, externalID string, postID int64, postType, originalPath, fieldsPath string) error
	MarkAudited(ctx context.Context, externalID string, paths Paths, score, scoreMax *int, findings, missingFields int) error
	MarkFailed(ctx context.Context, externalID string, cause error) error
}

// Options — чем одна задача аудита отличается от другой на прогоне.
//
// Всё здесь приходит из профиля задачи: что обязано быть заполнено, сколько нужно ссылок и
// вопросов, как площадка называет поля блока вопросов. Нулевое значение любого поля —
// прежнее поведение, поэтому задача, которая о нём не знает, работает как раньше.
type Options struct {
	// Required — поля записи, которые обязаны быть заполнены.
	Required []string
	// MinInternalLinks — сколько внутренних ссылок обязано быть в теле. Ноль выключает.
	MinInternalLinks int
	// FAQ — имена полей блока частых вопросов и их минимальное число.
	FAQ FAQScheme
}

// Flow — прогон задачи: прочитать страницу, проверить поля, спросить модель, собрать отчёт.
type Flow struct {
	repository Articles
	blog       Blog
	chats      taskflow.ChatFactory
	artifacts  Artifacts
	prompt     *template.Template
	result     *template.Template
	options    Options
	logger     *slog.Logger
}

// NewFlow собирает поток.
//
// Оба шаблона читаются и разбираются здесь, до денег: ошибка в шаблоне отчёта обязана ронять
// команду на старте, а не после оплаченного ответа модели, когда сохранить его будет уже
// некуда.
func NewFlow(repository Articles, blog Blog, chats taskflow.ChatFactory, artifacts Artifacts,
	promptPath, resultTemplatePath string, options Options, logger *slog.Logger) (*Flow, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	prompt, err := parseFile("промпт аудита", promptPath)
	if err != nil {
		return nil, err
	}
	result, err := parseFile("шаблон result.md", resultTemplatePath)
	if err != nil {
		return nil, err
	}
	return &Flow{
		repository: repository, blog: blog, chats: chats, artifacts: artifacts,
		prompt: prompt, result: result, options: options, logger: logger,
	}, nil
}

// PromptPlaceholder — метка незаполненного промпта аудита.
const PromptPlaceholder = "<!-- ЗАПОЛНИТЬ -->"

// EnsurePromptFilled отвечает, готов ли промпт задачи к прогону.
//
// Спрашивается перед первым запросом к модели, а не при сборке потока: `run plan` промпт не
// отправляет и обязан работать на незаполненной задаче — он затем и нужен, чтобы посмотреть
// пачку и пустые обязательные поля, пока регламент проверки ещё согласуют.
func EnsurePromptFilled(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("прочитать промпт аудита %q: %w", path, err)
	}
	if strings.Contains(string(content), PromptPlaceholder) {
		return fmt.Errorf("промпт аудита %q не заполнен: в нём осталась метка %s. "+
			"За каждый ответ модели платим, и по несогласованному регламенту запускать проверку нельзя",
			path, PromptPlaceholder)
	}
	return nil
}

func parseFile(name, path string) (*template.Template, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("прочитать %s %q: %w", name, path, err)
	}
	if strings.TrimSpace(string(content)) == "" {
		return nil, fmt.Errorf("%s %q пуст", name, path)
	}
	parsed, err := template.New(path).Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("разобрать %s %q: %w", name, path, err)
	}
	return parsed, nil
}

// PromptData — поля промпта аудита.
//
// Статья уходит модели текстом промпта, а не вложением: вложение — свойство стадии
// (attachments_dir) и работает только при mode: default, а тут и подставлять нечего, кроме
// того, что уже прочитано из блога.
//
// Оценивается сама статья, а не страница целиком: OriginalHTML — это тело записи, без меню,
// подвала и карточек соседних услуг. Судить о том, чего в теле нет, модель не должна — иначе
// находки уезжают туда, где их никто не подтвердит.
type PromptData struct {
	// Title — название статьи.
	Title        string
	OriginalHTML string
	// FAQ — блок частых вопросов страницы. Отдельным полем потому, что в теле записи его нет
	// вовсе: вопросы живут полями записи, а на странице их рисует тема. Для читателя это часть
	// статьи, и проверяются они вместе с ней.
	FAQ string
}

// Run проводит одну страницу весь путь: из блога в модель и в отчёт.
//
// Порядок шагов — не деталь реализации, а требование: сначала на диск ложится копия страницы
// и её полей, и только потом за проверку платят. Иначе отчёт не с чем было бы сверить, а
// запись в блоге к тому времени может уже измениться.
func (f *Flow) Run(ctx context.Context, externalID string) error {
	article, err := f.repository.Get(ctx, externalID)
	if err != nil {
		return err
	}
	if article.Audited() {
		f.logger.Info("страница уже проверена, пропускаем",
			"external_id", externalID, "post_id", article.PostID, "checked_at", article.CheckedAt)
		return nil
	}
	if err := f.repository.MarkProcessing(ctx, externalID); err != nil {
		return err
	}
	if err := f.run(ctx, article); err != nil {
		if markErr := f.repository.MarkFailed(ctx, externalID, err); markErr != nil {
			f.logger.Error("не удалось сохранить ошибку страницы", "external_id", externalID, "error", markErr)
		}
		return err
	}
	return nil
}

func (f *Flow) run(ctx context.Context, article Article) error {
	logger := f.logger.With("external_id", article.ExternalID, "slug", article.Slug)

	current, foundBy, err := f.fetch(ctx, article)
	if err != nil {
		return err
	}
	check := f.checkFields(current, linkOf(article, current))
	logger.Info("страница прочитана из блога", "stage", "fetch", "found_by", foundBy,
		"post_id", current.ID, "post_type", current.PostType, "title", current.Title,
		"html_runes", len([]rune(current.ContentHTML)),
		"required_fields", len(f.options.Required), "fields_missing", len(check.Missing),
		"fields_empty", len(check.Empty), "fields_too_long", len(check.TooLong),
		"internal_links", check.Links.Count(), "internal_links_min", check.Links.Min,
		"faq_questions", check.FAQ.Count, "faq_min", check.FAQ.Min)

	if err := f.saveOriginal(ctx, article, current); err != nil {
		return err
	}

	prompt, answer, err := f.audit(ctx, article, current, logger)
	if err != nil {
		return err
	}
	parsed, err := ParseAnswer(answer)
	if err != nil {
		return err
	}
	logger.Info("проверка получена", "stage", StageAudit,
		"score", parsed.Score, "score_found", parsed.ScoreFound,
		"findings", CountIssues(parsed.Section(SectionIssues)),
		"answer_runes", len([]rune(answer)))

	report, err := f.render(article, current, check, parsed)
	if err != nil {
		return err
	}
	pending, paths, err := f.artifacts.StageReport(article.ExternalID, article.Slug, prompt, answer, report)
	if err != nil {
		return err
	}
	score, scoreMax := scorePointers(parsed)
	return output.Commit(func() error {
		return f.repository.MarkAudited(ctx, article.ExternalID, paths, score, scoreMax,
			CountIssues(parsed.Section(SectionIssues)), check.RequiredProblems())
	}, pending)
}

// scorePointers переводит оценку в то, что ложится в базу. Ненайденная оценка обязана лечь
// как NULL, а не как ноль: по оценке сортируют пачку, и «не разобрано» встало бы впереди
// худшей страницы.
func scorePointers(parsed Answer) (*int, *int) {
	if !parsed.ScoreFound {
		return nil, nil
	}
	score, scoreMax := parsed.Score, parsed.ScoreMax
	return &score, &scoreMax
}

// saveOriginal кладёт копию страницы и её полей на диск одной транзакцией с записью путей.
func (f *Flow) saveOriginal(ctx context.Context, article Article, current Post) error {
	pending, paths, err := f.artifacts.StageOriginal(article.ExternalID, article.Slug,
		current.ContentHTML, current.Fields)
	if err != nil {
		return err
	}
	return output.Commit(func() error {
		return f.repository.SaveFetched(ctx, article.ExternalID, current.ID, current.PostType,
			paths.OriginalPath, paths.FieldsPath)
	}, pending)
}

// checkFields — всё, что код находит в полях записи сам, не спрашивая модель.
//
// Две независимые проверки в одном месте: обязательные поля заполнены и поля выдачи в неё
// помещаются. Обе про факт, а не про суждение, и обе бесплатны — поэтому их результат виден
// и в отчёте, и в плане, который к модели не ходит вовсе.
func (f *Flow) checkFields(current Post, pageURL string) FieldCheck {
	check := CheckRequired(current, f.options.Required)
	check.Measured = MeasureSEO(current.Fields)
	check.TooLong = CheckLength(current.Fields)
	check.FAQ = CountFAQ(current.Fields, f.options.FAQ)
	// Перелинковка живёт в теле, а не в полях, но проверяет её тот же код и по той же
	// причине: число ссылок — факт, и платить за него модели незачем.
	check.Links = CollectInternalLinks(current.ContentHTML, pageURL, f.options.MinInternalLinks)
	return check
}

// Как запись нашлась. Идёт в лог и в план: по одной статье это секунда против минуты, и
// человек должен видеть, какой путь сработал.
const (
	foundByID   = "id"
	foundBySlug = "slug"
)

// fetch читает страницу из блога.
//
// Известный идентификатор записи отменяет поиск по слагу: FindPostBySlug перебирает до
// тридцати страниц по сотне записей, а чтение по идентификатору — один запрос. Но найденную
// по нему запись всё равно сверяют со ссылкой: разошлись слаг или тип — это отказ страницы, а
// не молчаливая проверка чужой.
func (f *Flow) fetch(ctx context.Context, article Article) (Post, string, error) {
	foundBy := foundByID
	postID := article.PostID
	var link string
	if postID == 0 {
		found, err := f.blog.Find(ctx, article.Slug)
		if err != nil {
			return Post{}, "", fmt.Errorf("найти страницу %s в блоге: %w", article.SourceURL, err)
		}
		foundBy, postID, link = foundBySlug, found.ID, found.Link
	}
	current, err := f.blog.Read(ctx, postID)
	if err != nil {
		return Post{}, "", fmt.Errorf("прочитать запись %d: %w", postID, err)
	}
	if current.Link == "" {
		current.Link = link
	}
	if foundBy == foundByID {
		if err := matchesSource(current, article); err != nil {
			return Post{}, "", err
		}
	}
	if strings.TrimSpace(current.ContentHTML) == "" {
		return Post{}, "", fmt.Errorf("запись %d пришла с пустым телом", postID)
	}
	if current.Fields == nil {
		current.Fields = map[string]string{}
	}
	return current, foundBy, nil
}

// matchesSource сверяет запись, найденную по идентификатору, со ссылкой из входного файла.
//
// Идентификатор человек переносит руками, и опечатка в нём даёт совершенно правдоподобную
// чужую страницу: она прочитается, проверится и ляжет в отчёт под чужим индексом. Слаг — то
// единственное, чем это ловится.
func matchesSource(current Post, article Article) error {
	if !strings.EqualFold(strings.TrimSpace(current.Slug), strings.TrimSpace(article.Slug)) {
		return fmt.Errorf("запись %d — это %q, а во входном файле у индекса %s ссылка на %q: "+
			"проверьте идентификатор записи в книге",
			current.ID, current.Slug, article.ExternalID, article.Slug)
	}
	return nil
}

// audit отдаёт страницу модели и возвращает отрендеренный промпт и сырой ответ.
//
// Одно сообщение и один чат: ответ аудита — десяток строк, он помещается всегда. Дописывания
// оборванного ответа здесь нет намеренно, и заводить его заранее не нужно — оно существует у
// генерации потому, что переписанная статья в одно сообщение не влезает.
func (f *Flow) audit(ctx context.Context, article Article, current Post, logger *slog.Logger) (string, string, error) {
	prompt, err := render(f.prompt, PromptData{
		Title:        current.Title,
		OriginalHTML: current.ContentHTML,
		FAQ:          FormatFAQ(current.Fields, f.options.FAQ),
	})
	if err != nil {
		return "", "", fmt.Errorf("собрать промпт аудита: %w", err)
	}
	chat, err := f.chats.NewChat(ctx, article.ID, StageAudit)
	if err != nil {
		return "", "", fmt.Errorf("открыть диалог аудита: %w", err)
	}
	defer func() {
		if closeErr := chat.Close(); closeErr != nil {
			logger.Warn("не удалось закрыть диалог аудита", "error", closeErr)
		}
	}()
	answer, err := chat.Send(ctx, prompt)
	if err != nil {
		return prompt, "", fmt.Errorf("проверка страницы: %w", err)
	}
	return prompt, answer, nil
}

// PlannedCheck — то, что покажет `run plan` по одной странице.
type PlannedCheck struct {
	ExternalID string
	URL        string
	Topic      string
	PostID     int64
	PostType   string
	// FoundBy — как нашлась запись: по идентификатору из книги или перебором по слагу.
	FoundBy   string
	Title     string
	HTMLRunes int
	Headings  int
	Fields    FieldCheck
	Audited   bool
}

// Plan показывает, что будет проверено, ничего не меняя и не спрашивая модель.
//
// Проверка полей кодом попадает сюда целиком: она ничего не стоит, а половину пользы отчёта
// даёт до первого рубля за модель.
func (f *Flow) Plan(ctx context.Context, article Article) (PlannedCheck, error) {
	current, foundBy, err := f.fetch(ctx, article)
	if err != nil {
		return PlannedCheck{}, err
	}
	return PlannedCheck{
		ExternalID: article.ExternalID,
		URL:        article.SourceURL,
		Topic:      article.Topic,
		PostID:     current.ID,
		PostType:   current.PostType,
		FoundBy:    foundBy,
		Title:      current.Title,
		HTMLRunes:  len([]rune(current.ContentHTML)),
		Headings:   countHeadings(current.ContentHTML),
		Fields:     f.checkFields(current, linkOf(article, current)),
		Audited:    article.Audited(),
	}, nil
}

var headingRE = regexp.MustCompile(`(?i)<h[1-6][^>]*>`)

// countHeadings считает заголовки разметки. Нужен плану: по числу разделов и длине текста
// видно, что за страница пришла, ещё до того, как её кто-то читал.
func countHeadings(markup string) int { return len(headingRE.FindAllString(markup, -1)) }

// render собирает текст отчёта по шаблону задачи.
func (f *Flow) render(article Article, current Post, check FieldCheck, parsed Answer) (string, error) {
	report, err := render(f.result, BuildReport(article, current, check, parsed, f.options.Required))
	if err != nil {
		return "", fmt.Errorf("собрать result.md: %w", err)
	}
	return report, nil
}

func render(tpl *template.Template, data any) (string, error) {
	var builder strings.Builder
	if err := tpl.Execute(&builder, data); err != nil {
		return "", err
	}
	text := strings.TrimSpace(builder.String())
	if text == "" {
		return "", errors.New("шаблон дал пустой текст")
	}
	return text, nil
}

// scoreLine — как оценка печатается в шапке отчёта.
func scoreLine(parsed Answer) string {
	if !parsed.ScoreFound {
		return "оценка не разобрана"
	}
	return strconv.Itoa(parsed.Score) + "/" + strconv.Itoa(parsed.ScoreMax)
}
