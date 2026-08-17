package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/foxylis237/seo-pipeline/internal/integrations/google"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/generation"
)

// googleConfig собирает настройки публикации. Значения по умолчанию берутся из пакета
// интеграции, headless выключается только для ручного входа.
func googleConfig(headless bool) google.Config {
	cfg := google.DefaultConfig()
	cfg.Headless = headless
	return cfg
}

// googlePublisher выполняет публикацию промпта рядом с генерацией, не задерживая её.
//
// Почему не просто go func() на каждый вызов: за persistent-профилем Chromium держится flock,
// и две одновременные публикации отвалились бы с «профиль занят». Поэтому задания
// выстраиваются в очередь и выполняются по одному, а генерация тем временем идёт дальше.
type googlePublisher struct {
	// ctx — корневой контекст команды. Отмена по Ctrl+C доходит до браузера.
	ctx      context.Context
	publish  func(ctx context.Context, job google.Job, observer google.Observer) (google.Result, error)
	observer google.Observer
	// saveURL запоминает адрес документа. Без него result.md не смог бы напечатать ссылку:
	// идентификатор документа назначает Google, и вывести его из данных статьи нельзя.
	saveURL   func(ctx context.Context, articleID int64, documentURL string) error
	logger    *slog.Logger
	wait      sync.WaitGroup
	serialize sync.Mutex
}

// googleDocURLRepository запоминает адрес опубликованного документа.
type googleDocURLRepository interface {
	SaveGoogleDocURL(ctx context.Context, articleID int64, documentURL string) error
}

// newGooglePublisher собирает публикатор для боевого прогона.
func newGooglePublisher(ctx context.Context, cfg google.Config, repository googleDocURLRepository, logger *slog.Logger) *googlePublisher {
	publisher := google.NewPublisher(
		func(sessionCtx context.Context) (google.Session, error) {
			return google.NewSessionFactory(cfg, logger, 0)(sessionCtx)
		},
		google.DefaultRetryPolicy(),
	)
	return &googlePublisher{
		ctx:      ctx,
		publish:  publisher.Publish,
		observer: google.SlogObserver{Logger: logger},
		saveURL:  repository.SaveGoogleDocURL,
		logger:   logger,
	}
}

// PublishArticlePrompt ставит задание в очередь и сразу возвращает управление.
func (p *googlePublisher) PublishArticlePrompt(job generation.ArticlePromptJob) {
	if p == nil {
		return
	}
	p.wait.Add(1)
	go func() {
		defer p.wait.Done()
		p.run(job)
	}()
}

// run выполняет одно задание под общим мьютексом: одновременно открыт не больше одного
// браузера на профиль.
func (p *googlePublisher) run(job generation.ArticlePromptJob) {
	p.serialize.Lock()
	defer p.serialize.Unlock()
	if err := p.ctx.Err(); err != nil {
		p.logger.Warn("публикация промпта отменена вместе с командой",
			"article_id", job.ArticleID, "external_id", job.ExternalID,
			"stage", "google_publish", "error", err)
		return
	}
	result, err := p.publish(p.ctx, google.Job{
		ArticleID:    job.ArticleID,
		ExternalID:   job.ExternalID,
		ArticleTitle: job.Title,
		Prompt:       job.Prompt,
	}, p.observer)
	if err == nil {
		p.rememberURL(job, result.DocumentURL)
		return
	}
	// Ошибка уже записана observer'ом со всеми полями. Здесь остаётся подсказка человеку:
	// её видно в конце прогона, когда лог стадий уже прокручен.
	if google.NeedsManualLogin(err) {
		p.logger.Error("промпт не опубликован: нужен ручной вход",
			"article_id", job.ArticleID, "external_id", job.ExternalID,
			"stage", "google_publish", "next_step", "make task-1 google-login")
	}
}

// rememberURL сохраняет адрес документа для result.md.
//
// Неудача записи не отменяет успешной публикации: документ в Drive уже актуален, потерян
// только адрес, и его вернёт повторный google-publish. Поэтому здесь предупреждение, а не
// ошибка наверх — валить из-за этого прогон нечем.
func (p *googlePublisher) rememberURL(job generation.ArticlePromptJob, documentURL string) {
	if p.saveURL == nil || strings.TrimSpace(documentURL) == "" {
		return
	}
	if err := p.saveURL(p.ctx, job.ArticleID, documentURL); err != nil {
		p.logger.Warn("адрес документа Google не сохранён, ссылки в result.md не будет",
			"article_id", job.ArticleID, "external_id", job.ExternalID,
			"stage", "google_publish", "document_url", documentURL, "error", err)
	}
}

// WaitPending реализует generation.PromptPublisher: конвейер зовёт его перед сборкой
// result.md, чтобы ссылка на документ успела попасть в базу.
func (p *googlePublisher) WaitPending() { p.Wait() }

// Wait дожидается очереди публикаций. Вызывается перед выходом из команды, иначе процесс
// завершится с живым Chromium и недописанным документом.
func (p *googlePublisher) Wait() {
	if p == nil {
		return
	}
	p.wait.Wait()
}

// googlePublishRepository — то, что ручная публикация требует от хранилища.
type googlePublishRepository interface {
	GetArticleByExternalID(ctx context.Context, externalID string) (article.Article, error)
	SaveGoogleDocURL(ctx context.Context, articleID int64, documentURL string) error
}

// googlePromptReader читает сохранённый промпт статьи.
type googlePromptReader interface {
	ArticlePromptText(externalID string) (text, relativePath string, err error)
}

// runGooglePublish повторно публикует уже готовый промпт без запуска генерации.
//
// Ни LLM, ни Keys.so, ни Arsenkin здесь не вызываются: команда берёт текст из
// prompts/article_prompt.txt — того самого файла, который пайплайн записал из промпта,
// отправленного модели, — и кладёт его в документ.
func runGooglePublish(
	ctx context.Context,
	repository googlePublishRepository,
	prompts googlePromptReader,
	publish func(ctx context.Context, job google.Job, observer google.Observer) (google.Result, error),
	logger *slog.Logger,
	out io.Writer,
	externalID string,
) error {
	selected, err := repository.GetArticleByExternalID(ctx, externalID)
	if err != nil {
		return err
	}
	prompt, promptPath, err := prompts.ArticlePromptText(externalID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("промпт статьи external_id=%s пуст: сначала выполните генерацию", externalID)
	}
	logger.Info("публикация сохранённого промпта начата",
		"article_id", selected.ID, "external_id", selected.ExternalID,
		"stage", "google_publish", "prompt_path", promptPath)

	result, err := publish(ctx, google.Job{
		ArticleID:    selected.ID,
		ExternalID:   selected.ExternalID,
		ArticleTitle: selected.Title,
		Prompt:       prompt,
	}, google.SlogObserver{Logger: logger})
	if err != nil {
		if google.NeedsManualLogin(err) {
			return fmt.Errorf("%w\nДальше: make task-1 google-login", err)
		}
		return err
	}

	// Адрес запоминается и при ручной публикации: это тот же способ вернуть ссылку в
	// result.md для статьи, у которой публикация раньше не удалась.
	if saveErr := repository.SaveGoogleDocURL(ctx, selected.ID, result.DocumentURL); saveErr != nil {
		logger.Warn("адрес документа Google не сохранён, ссылки в result.md не будет",
			"article_id", selected.ID, "external_id", selected.ExternalID,
			"stage", "google_publish", "document_url", result.DocumentURL, "error", saveErr)
	}

	action := "обновлён"
	if result.Created {
		action = "создан"
	}
	fmt.Fprintf(out, "Документ %s: %s\n", action, google.DocumentTitle(selected.Title))
	fmt.Fprintf(out, "  %s\n", result.DocumentURL)
	fmt.Fprintf(out, "Ссылка попадёт в result.md при следующей сборке: make task-1 result %s\n", externalID)
	return nil
}
