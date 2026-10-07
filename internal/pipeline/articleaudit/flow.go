// Package articleaudit проверяет уже опубликованные страницы и складывает находки в отчёт.
//
// Поток общий у задач аудита; различает их профиль. В блог поток не пишет: у интерфейса Blog нет метода записи.
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

// Post — запись блога в том объёме, который нужен проверке; переходник из интеграции — в composition root.
type Post struct {
	ID    int64
	Title string
	// Slug сверяется с входным файлом, когда запись найдена по идентификатору.
	Slug        string
	PostType    string
	ContentHTML string
	Link        string
	// Fields — поля записи; их проверяет код, модели они не показываются.
	Fields map[string]string
	// ThumbnailID и TermIDs — обложка и рубрики; проверяются в одном списке с обязательными полями.
	ThumbnailID int64
	TermIDs     map[string][]int64
}

// Blog — то, что поток требует от площадки; метода записи нет, чтобы запись в блог не компилировалась.
type Blog interface {
	// Find находит запись по слагу из её адреса.
	Find(ctx context.Context, slug string) (Post, error)
	// Read читает запись в том виде, в каком она лежит в базе сайта.
	Read(ctx context.Context, postID int64) (Post, error)
}

// Articles — то, что поток требует от хранилища страниц.
type Articles interface {
	Get(ctx context.Context, externalID string) (Article, error)
	MarkProcessing(ctx context.Context, externalID string) error
	SaveFetched(ctx context.Context, externalID string, postID int64, postType, originalPath, fieldsPath string) error
	SaveAnswer(ctx context.Context, externalID, promptPath, auditPath string) error
	MarkAudited(ctx context.Context, externalID string, paths Paths, score, scoreMax *int, findings, missingFields int) error
	MarkFailed(ctx context.Context, externalID string, cause error) error
}

// Options — чем одна задача аудита отличается от другой на прогоне; приходит из профиля задачи.
type Options struct {
	// Required — поля записи, которые обязаны быть заполнены.
	Required []string
	// MinInternalLinks — минимум внутренних ссылок в теле; ноль выключает проверку.
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

// NewFlow собирает поток; шаблоны разбираются здесь, чтобы ошибка в них роняла команду до оплаты модели.
func NewFlow(repository Articles, blog Blog, chats taskflow.ChatFactory, artifacts Artifacts,
	promptPath, resultTemplatePath string, options Options, logger *slog.Logger) (*Flow, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if err := options.FAQ.validate(); err != nil {
		return nil, err
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
// Не зовётся при сборке потока: `run plan` работает и с незаполненным промптом.
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

// PromptData — поля промпта аудита. Статья уходит текстом промпта, а не вложением:
// вложения работают только при mode: default. OriginalHTML — тело записи, без меню и подвала.
type PromptData struct {
	Title        string
	OriginalHTML string
	// FAQ — блок частых вопросов из полей записи: в теле его нет, на странице его рисует тема.
	FAQ string
}

// Run проводит одну страницу весь путь: из блога в модель и в отчёт.
// Копия страницы и полей ложится на диск до запроса к модели: запись в блоге может измениться.
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
		if markErr := f.repository.MarkFailed(context.WithoutCancel(ctx), externalID, err); markErr != nil {
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

	paths, answer, err := f.answer(ctx, article, current, logger)
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
	pending, resultPath, err := f.artifacts.StageReport(article.ExternalID, article.Slug, report)
	if err != nil {
		return err
	}
	paths.ResultPath = resultPath
	score, scoreMax := scorePointers(parsed)
	return output.Commit(func() error {
		return f.repository.MarkAudited(ctx, article.ExternalID, paths, score, scoreMax,
			CountIssues(parsed.Section(SectionIssues)), check.RequiredProblems())
	}, pending)
}

// scorePointers отдаёт nil для ненайденной оценки: в базе это NULL, а не ноль.
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

// checkFields — всё, что код находит в записи сам, не спрашивая модель; результат виден и в отчёте, и в плане.
func (f *Flow) checkFields(current Post, pageURL string) FieldCheck {
	check := CheckRequired(current, f.options.Required)
	check.Measured = MeasureSEO(current.Fields)
	check.TooLong = CheckLength(current.Fields)
	check.FAQ = CountFAQ(current.Fields, f.options.FAQ)
	check.Links = CollectInternalLinks(current.ContentHTML, pageURL, f.options.MinInternalLinks)
	return check
}

// Как запись нашлась; идёт в лог и в план.
const (
	foundByID   = "id"
	foundBySlug = "slug"
)

// fetch читает страницу из блога: по известному идентификатору одним запросом вместо перебора
// по слагу, но со сверкой слага со ссылкой.
func (f *Flow) fetch(ctx context.Context, article Article) (Post, string, error) {
	foundBy := foundByID
	postID := article.PostID
	var link string
	if postID == 0 {
		found, err := f.blog.Find(ctx, article.Slug)
		if err != nil {
			return Post{}, "", fmt.Errorf("найти страницу %q в блоге: %w", article.SourceURL, err)
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

// matchesSource сверяет слаг записи, найденной по идентификатору, со ссылкой: опечатка в ID даёт правдоподобную чужую страницу.
func matchesSource(current Post, article Article) error {
	if !strings.EqualFold(strings.TrimSpace(current.Slug), strings.TrimSpace(article.Slug)) {
		return fmt.Errorf("запись %d — это %q, а во входном файле у индекса %s ссылка на %q: "+
			"проверьте идентификатор записи в книге",
			current.ID, current.Slug, article.ExternalID, article.Slug)
	}
	return nil
}

// answer reuses an answer saved by a failed run, so each page is paid for once.
func (f *Flow) answer(ctx context.Context, article Article, current Post, logger *slog.Logger) (Paths, string, error) {
	if article.AuditPath != "" {
		saved, err := f.artifacts.Read(article.AuditPath)
		if err == nil && strings.TrimSpace(saved) != "" {
			logger.Info("ответ модели взят из прошлого прогона", "stage", StageAudit, "file", article.AuditPath)
			return Paths{PromptPath: article.PromptPath, AuditPath: article.AuditPath}, saved, nil
		}
		logger.Warn("сохранённый ответ модели не прочитан, спрашиваем заново",
			"stage", StageAudit, "file", article.AuditPath, "error", err)
	}
	prompt, answer, err := f.audit(ctx, article, current, logger)
	if err != nil {
		return Paths{}, "", err
	}
	if strings.TrimSpace(answer) == "" {
		return Paths{}, "", ErrEmptyAnswer
	}
	pending, paths, err := f.artifacts.StageAnswer(article.ExternalID, article.Slug, prompt, answer)
	if err != nil {
		return Paths{}, "", err
	}
	if err := output.Commit(func() error {
		return f.repository.SaveAnswer(ctx, article.ExternalID, paths.PromptPath, paths.AuditPath)
	}, pending); err != nil {
		return Paths{}, "", err
	}
	return paths, answer, nil
}

// audit отдаёт страницу модели одним сообщением и возвращает промпт и сырой ответ.
// Дописывания оборванного ответа нет: отчёт — десяток строк и помещается всегда.
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

// Plan показывает, что будет проверено, ничего не меняя и не спрашивая модель; проверка полей кодом входит целиком.
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

func countHeadings(markup string) int { return len(headingRE.FindAllString(markup, -1)) }

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

func scoreLine(parsed Answer) string {
	if !parsed.ScoreFound {
		return "оценка не разобрана"
	}
	return strconv.Itoa(parsed.Score) + "/" + strconv.Itoa(parsed.ScoreMax)
}
