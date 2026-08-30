package pproffix1

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/template"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/generation"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
)

// Post — запись блога в том объёме, который нужен правке.
//
// Свой тип, а не тип интеграции: задача не должна знать ни про XML-RPC, ни про REST, ни про
// то, что площадка — WordPress. Переходник живёт в composition root.
type Post struct {
	ID          int64
	Title       string
	ContentHTML string
	Link        string
	// Fields и FieldIDs — поля записи и их идентификаторы. Нужны заголовку: тема рисует H1
	// не из Title, а из поля HeaderField, и правка, не тронувшая его, не меняет на странице
	// ничего.
	Fields   map[string]string
	FieldIDs map[string]string
}

// Field — поле записи, которое правка меняет вместе с текстом.
type Field struct {
	ID    string
	Key   string
	Value string
}

// Ключи полей, которые правка трогает помимо текста.
const (
	// HeaderField — видимый заголовок страницы: из него тема рисует H1. Имя поля общее у
	// страниц услуг и статей блога — это поле темы, а не одной задачи.
	HeaderField = "prof_title"
	// SEOTitleField — заголовок для поисковой выдачи. Меняется, только если правило к нему
	// подходит: формулировка у него своя и короче, и совпадать с видимой не обязана.
	SEOTitleField = "_yoast_wpseo_title"
)

// Blog — то, что поток требует от площадки. Три действия, все обязательные.
type Blog interface {
	// Find находит запись по слагу из её адреса.
	Find(ctx context.Context, slug string) (Post, error)
	// Read читает заголовок и тело записи в том виде, в каком они лежат в базе сайта.
	Read(ctx context.Context, postID int64) (Post, error)
	// Write переписывает заголовок, тело и названные поля существующей записи.
	Write(ctx context.Context, postID int64, title, contentHTML string, fields []Field) error
}

// Articles — то, что поток требует от хранилища статей.
//
// Интерфейс объявлен здесь, у потребителя, а не отдан конкретным *Repository: поток проверяем
// на подделках, без PostgreSQL, и порядок его шагов — «оригинал на диск раньше записи в
// блог» — иначе проверить было бы нечем.
type Articles interface {
	Get(ctx context.Context, externalID string) (Article, error)
	MarkProcessing(ctx context.Context, externalID string) error
	SaveFetched(ctx context.Context, externalID string, postID int64, oldTitle, newTitle, originalPath string) error
	SaveRewritten(ctx context.Context, externalID, promptPath, rewrittenPath string) error
	MarkUpdated(ctx context.Context, externalID, resultPath string) error
	MarkFailed(ctx context.Context, externalID string, cause error) error
}

// Flow — прогон задачи: скачать статью, поправить моделью, вернуть в блог.
type Flow struct {
	repository Articles
	blog       Blog
	chats      taskflow.ChatFactory
	artifacts  Artifacts
	rule       TitleRule
	prompt     *template.Template
	result     *template.Template
	logger     *slog.Logger
}

// NewFlow собирает поток. Все зависимости обязательные: необязательных у него нет.
func NewFlow(repository Articles, blog Blog, chats taskflow.ChatFactory, artifacts Artifacts,
	rule TitleRule, promptPath, resultTemplatePath string, logger *slog.Logger) (*Flow, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	prompt, err := parseFile("промпт правки", promptPath)
	if err != nil {
		return nil, err
	}
	result, err := parseFile("шаблон result.md", resultTemplatePath)
	if err != nil {
		return nil, err
	}
	return &Flow{
		repository: repository, blog: blog, chats: chats, artifacts: artifacts,
		rule: rule, prompt: prompt, result: result, logger: logger,
	}, nil
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

// PromptData — поля промпта правки.
type PromptData struct {
	Title        string
	URL          string
	OriginalHTML string
}

// resultData — поля result.md. Их два: задача меняет заголовок и текст, и ровно это
// человек проверяет в блоге.
type resultData struct {
	Title string
	URL   string
}

// Run проводит одну статью весь путь: из блога в модель и обратно в блог.
//
// Порядок шагов — не деталь реализации, а требование: сначала на диск ложится оригинал, и
// только потом что-либо уходит в блог. Пока оригинал не сохранён, отменить правку было бы
// нечем — записи в живом блоге приложение не восстанавливает.
func (f *Flow) Run(ctx context.Context, externalID string) error {
	article, err := f.repository.Get(ctx, externalID)
	if err != nil {
		return err
	}
	if article.Rewritten() {
		f.logger.Info("статья уже переписана, пропускаем",
			"external_id", externalID, "post_id", article.PostID, "updated_post_at", article.UpdatedPostAt)
		return nil
	}
	if err := f.repository.MarkProcessing(ctx, externalID); err != nil {
		return err
	}
	if err := f.run(ctx, article); err != nil {
		if markErr := f.repository.MarkFailed(ctx, externalID, err); markErr != nil {
			f.logger.Error("не удалось сохранить ошибку статьи", "external_id", externalID, "error", markErr)
		}
		return err
	}
	return nil
}

func (f *Flow) run(ctx context.Context, article Article) error {
	logger := f.logger.With("external_id", article.ExternalID, "slug", article.Slug)

	current, newTitle, err := f.fetch(ctx, article)
	if err != nil {
		return err
	}
	logger.Info("статья прочитана из блога", "stage", "fetch",
		"post_id", current.ID, "title", current.Title, "new_title", newTitle,
		"html_runes", len([]rune(current.ContentHTML)))

	prompt, rewritten, err := f.rewrite(ctx, article, current, logger)
	if err != nil {
		return err
	}
	promptPath, err := f.artifacts.Save(article.ExternalID, article.Slug, PromptsFolder, PromptFile, prompt)
	if err != nil {
		return err
	}
	rewrittenPath, err := f.artifacts.Save(article.ExternalID, article.Slug, GeneratedFolder, RewrittenHTMLFile, rewritten)
	if err != nil {
		return err
	}
	if err := f.repository.SaveRewritten(ctx, article.ExternalID, promptPath, rewrittenPath); err != nil {
		return err
	}
	logger.Info("правка получена", "stage", "rewrite", "html_runes", len([]rune(rewritten)))

	fields, err := f.renamedFields(current)
	if err != nil {
		return err
	}
	if err := f.publish(ctx, current, newTitle, rewritten, fields); err != nil {
		return err
	}
	logger.Info("статья обновлена в блоге", "stage", "update", "post_id", current.ID, "title", newTitle)

	resultPath, err := f.saveResult(article, newTitle, current.Link)
	if err != nil {
		return err
	}
	return f.repository.MarkUpdated(ctx, article.ExternalID, resultPath)
}

// fetch читает статью из блога, считает новый заголовок и сохраняет оригинал на диск.
func (f *Flow) fetch(ctx context.Context, article Article) (Post, string, error) {
	found, err := f.blog.Find(ctx, article.Slug)
	if err != nil {
		return Post{}, "", fmt.Errorf("найти статью %s в блоге: %w", article.SourceURL, err)
	}
	current, err := f.blog.Read(ctx, found.ID)
	if err != nil {
		return Post{}, "", fmt.Errorf("прочитать запись %d: %w", found.ID, err)
	}
	current.Link = found.Link
	if strings.TrimSpace(current.ContentHTML) == "" {
		return Post{}, "", fmt.Errorf("запись %d пришла с пустым телом", found.ID)
	}
	// Заголовки считаются до правки текста намеренно: правило может к статье не подойти, и
	// узнать об этом надо до того, как за ответ модели заплачено.
	newTitle, err := f.rule.Apply(current.Title)
	if err != nil {
		return Post{}, "", err
	}
	if _, err := f.renamedFields(current); err != nil {
		return Post{}, "", err
	}
	originalPath, err := f.artifacts.Save(article.ExternalID, article.Slug,
		OriginalFolder, OriginalHTMLFile, current.ContentHTML)
	if err != nil {
		return Post{}, "", err
	}
	if _, err := f.artifacts.Save(article.ExternalID, article.Slug,
		OriginalFolder, OriginalTitleFile, current.Title); err != nil {
		return Post{}, "", err
	}
	if err := f.repository.SaveFetched(ctx, article.ExternalID, found.ID,
		current.Title, newTitle, originalPath); err != nil {
		return Post{}, "", err
	}
	return current, newTitle, nil
}

// rewrite отдаёт статью модели и возвращает отрендеренный промпт и исправленный текст.
func (f *Flow) rewrite(ctx context.Context, article Article, current Post, logger *slog.Logger) (string, string, error) {
	prompt, err := render(f.prompt, PromptData{
		Title: current.Title, URL: article.SourceURL, OriginalHTML: current.ContentHTML,
	})
	if err != nil {
		return "", "", fmt.Errorf("собрать промпт правки: %w", err)
	}
	// Чат открывается сразу на несколько сообщений: длинная статья не помещается в один
	// ответ веб-интерфейса, и продолжение идёт той же стадией, в той же беседе.
	chat, err := f.chats.NewChat(ctx, article.ID, generation.HTMLChatStages(StageRewrite)...)
	if err != nil {
		return "", "", fmt.Errorf("открыть диалог правки: %w", err)
	}
	defer func() {
		if closeErr := chat.Close(); closeErr != nil {
			logger.Warn("не удалось закрыть диалог правки", "error", closeErr)
		}
	}()
	rewritten, err := generation.BuildHTMLPage(ctx, generation.HTMLPageRequest{
		Page:     current.ContentHTML,
		Prompt:   prompt,
		Send:     chat.Send,
		Continue: chat.Continue,
		Logger:   logger,
		Complete: func(markup string) error { return ValidateRewriteCovers(current.ContentHTML, markup) },
	})
	if err != nil {
		return prompt, "", fmt.Errorf("правка статьи: %w", err)
	}
	return prompt, rewritten, nil
}

// publish записывает правку в блог и сверяет записанное чтением.
//
// Сверка обязательна по той же причине, что и у публикации: молча отброшенное поле внешне
// неотличимо от успеха, а исправлять пришлось бы вручную в админке.
func (f *Flow) publish(ctx context.Context, current Post, title, rewritten string, fields []Field) error {
	if err := f.blog.Write(ctx, current.ID, title, rewritten, fields); err != nil {
		return fmt.Errorf("записать статью в блог: %w", err)
	}
	stored, err := f.blog.Read(ctx, current.ID)
	if err != nil {
		return fmt.Errorf("сверить запись %d после правки: %w", current.ID, err)
	}
	if normalizeForCompare(stored.Title) != normalizeForCompare(title) {
		return fmt.Errorf("в блоге остался заголовок %q, ожидался %q", stored.Title, title)
	}
	// Поля сверяются наравне с заголовком: молча отброшенное поле выглядит как успех, а на
	// странице остаётся прежний H1 — то есть статья с новым текстом и старым названием.
	for _, field := range fields {
		if normalizeForCompare(stored.Fields[field.Key]) != normalizeForCompare(field.Value) {
			return fmt.Errorf("поле %s в блоге осталось прежним (%q), ожидалось %q",
				field.Key, stored.Fields[field.Key], field.Value)
		}
	}
	if err := generation.ValidateHTMLCoversPage(rewritten, stored.ContentHTML); err != nil {
		return fmt.Errorf("в блоге лёг не весь текст: %w", err)
	}
	return nil
}

// renamedFields собирает поля записи, которые правило переименования меняет.
//
// Видимый заголовок обязателен: если поле у записи есть, а правило к нему не подходит —
// это отказ, потому что иначе статья получила бы новый текст и старое название. SEO-заголовок
// необязателен: формулировка у него своя, и не подошедшее правило означает, что менять там
// нечего, а не ошибку.
func (f *Flow) renamedFields(current Post) ([]Field, error) {
	var fields []Field
	if header := strings.TrimSpace(current.Fields[HeaderField]); header != "" {
		renamed, err := f.rule.Apply(header)
		if err != nil {
			return nil, fmt.Errorf("видимый заголовок страницы (%s): %w", HeaderField, err)
		}
		if renamed != header {
			fields = append(fields, Field{ID: current.FieldIDs[HeaderField], Key: HeaderField, Value: renamed})
		}
	}
	if seoTitle := strings.TrimSpace(current.Fields[SEOTitleField]); seoTitle != "" {
		renamed, err := f.rule.Apply(seoTitle)
		if err == nil && renamed != seoTitle {
			fields = append(fields, Field{ID: current.FieldIDs[SEOTitleField], Key: SEOTitleField, Value: renamed})
		}
	}
	return fields, nil
}

func (f *Flow) saveResult(article Article, title, link string) (string, error) {
	if strings.TrimSpace(link) == "" {
		link = article.SourceURL
	}
	content, err := render(f.result, resultData{Title: title, URL: link})
	if err != nil {
		return "", fmt.Errorf("собрать result.md: %w", err)
	}
	return f.artifacts.Save(article.ExternalID, article.Slug, ".", ResultFile, content)
}

// Plan показывает, что произойдёт со статьёй, ничего не меняя.
//
// Читает блог и считает новый заголовок — то есть отвечает на единственный вопрос, ответ на
// который после записи уже не отменить: правильно ли правило переименования сработало на
// этой статье. Ни модель, ни блог при этом не пишутся.
func (f *Flow) Plan(ctx context.Context, article Article) (PlannedChange, error) {
	found, err := f.blog.Find(ctx, article.Slug)
	if err != nil {
		return PlannedChange{}, fmt.Errorf("найти статью %s в блоге: %w", article.SourceURL, err)
	}
	current, err := f.blog.Read(ctx, found.ID)
	if err != nil {
		return PlannedChange{}, fmt.Errorf("прочитать запись %d: %w", found.ID, err)
	}
	planned := PlannedChange{
		ExternalID: article.ExternalID,
		URL:        article.SourceURL,
		PostID:     found.ID,
		OldTitle:   current.Title,
		HTMLRunes:  len([]rune(current.ContentHTML)),
		Headings:   len(headings(current.ContentHTML)),
		Rewritten:  article.Rewritten(),
	}
	// Видимый заголовок показывается отдельно от названия записи: это разные значения, и
	// человек проверяет именно первое — его читают на странице, второе видно только в админке.
	planned.OldHeader = strings.TrimSpace(current.Fields[HeaderField])
	// Заголовок, к которому правило не подходит, — не отказ плана, а его содержание: план
	// затем и нужен, чтобы такие статьи стали видны все сразу, а не по одной на прогоне.
	newTitle, err := f.rule.Apply(current.Title)
	if err != nil {
		planned.TitleProblem = err.Error()
		return planned, nil //nolint:nilerr // причина названа выше: это строка отчёта, а не отказ
	}
	planned.NewTitle = newTitle
	fields, err := f.renamedFields(current)
	if err != nil {
		planned.TitleProblem = err.Error()
		return planned, nil //nolint:nilerr // то же: план показывает вопросы, а не падает на них
	}
	for _, field := range fields {
		if field.Key == HeaderField {
			planned.NewHeader = field.Value
		}
	}
	return planned, nil
}

// PlannedChange — то, что покажет `run plan` по одной статье.
type PlannedChange struct {
	ExternalID string
	URL        string
	PostID     int64
	OldTitle   string
	NewTitle   string
	// OldHeader и NewHeader — видимый заголовок страницы (поле HeaderField), тот самый H1.
	// Пустой NewHeader при непустом OldHeader означает, что правило его не меняет.
	OldHeader    string
	NewHeader    string
	TitleProblem string
	HTMLRunes    int
	Headings     int
	Rewritten    bool
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
