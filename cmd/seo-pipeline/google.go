package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/foxylis237/seo-pipeline/internal/integrations/google"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/generation"
)

// googleConfig собирает настройки публикации. Значения по умолчанию берутся из пакета
// интеграции, headless выключается только для ручного входа.
func googleConfig(headless bool, diagnosticsDir string) google.Config {
	cfg := google.DefaultConfig()
	cfg.Headless = headless
	cfg.DiagnosticsDir = diagnosticsDir
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
			"stage", "google_publish", "next_step", "make login google")
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
	GetAll(ctx context.Context) ([]article.Article, error)
	SaveGoogleDocURL(ctx context.Context, articleID int64, documentURL string) error
}

// googlePromptReader читает сохранённый промпт статьи — боевой или из DEMO.
type googlePromptReader interface {
	ArticlePromptText(externalID string) (text, relativePath string, err error)
	demoPromptReader
}

// articlePromptForPublication возвращает промпт, который нужно выгрузить.
//
// Боевой промпт приоритетнее, но статья, прошедшая только demo-generate, боевого не имеет —
// и без этого запаса она молча выпадала бы из выгрузки. Промпт там тот же самый, поэтому
// документ у них общий.
func articlePromptForPublication(prompts googlePromptReader, externalID string) (text, relativePath string, found bool) {
	if prompt, path, err := prompts.ArticlePromptText(externalID); err == nil && strings.TrimSpace(prompt) != "" {
		return prompt, path, true
	}
	if prompt, path, err := prompts.DemoArticlePromptText(externalID); err == nil && strings.TrimSpace(prompt) != "" {
		return prompt, path, true
	}
	return "", "", false
}

// runGooglePublish повторно публикует уже готовые промпты без запуска генерации.
//
// Ни LLM, ни Keys.so, ни Arsenkin здесь не вызываются: команда берёт текст из
// prompts/article_prompt.txt — того самого файла, который пайплайн записал из промпта,
// отправленного модели, — и кладёт его в документ.
//
// Без external_id обрабатываются все статьи, у которых промпт уже сохранён. Пустой ID здесь
// осознанно означает «все»: публикация идемпотентна — документ ищется по имени и
// перезаписывается, копий не появляется, — поэтому повторный запуск после обрыва просто
// доводит дело до конца.
func runGooglePublish(
	ctx context.Context,
	repository googlePublishRepository,
	prompts googlePromptReader,
	publish func(ctx context.Context, job google.Job, observer google.Observer) (google.Result, error),
	logger *slog.Logger,
	out io.Writer,
	taskCommand string,
	externalID string,
) error {
	if externalID == "" {
		return runGooglePublishAll(ctx, repository, prompts, publish, logger, out, taskCommand)
	}
	return publishArticlePrompt(ctx, repository, prompts, publish, logger, out, taskCommand, externalID)
}

// publishArticlePrompt публикует промпт одной статьи.
func publishArticlePrompt(
	ctx context.Context,
	repository googlePublishRepository,
	prompts googlePromptReader,
	publish func(ctx context.Context, job google.Job, observer google.Observer) (google.Result, error),
	logger *slog.Logger,
	out io.Writer,
	taskCommand string,
	externalID string,
) error {
	selected, err := repository.GetArticleByExternalID(ctx, externalID)
	if err != nil {
		return err
	}
	prompt, promptPath, found := articlePromptForPublication(prompts, externalID)
	if !found {
		return fmt.Errorf("промпт статьи external_id=%s не сохранён: сначала выполните генерацию или demo-generate", externalID)
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
			return fmt.Errorf("%w\nДальше: make login google", err)
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
	fmt.Fprintf(out, "Ссылка попадёт в result.md при следующей сборке: make %s result %s\n", taskCommand, externalID)
	return nil
}

// runGooglePublishAll публикует промпты всех статей, у которых они есть.
//
// Три правила, ради которых команда и написана отдельно от одиночной публикации:
//
//   - Статья без сохранённого промпта пропускается, а не ломает прогон: она просто ещё не
//     генерировалась, и это нормальное состояние половины таблицы после импорта.
//   - Отказ на одной статье не останавливает остальные — иначе одна сломанная статья
//     оставляла бы неопубликованными все следующие за ней.
//   - Требование ручного входа, наоборот, прекращает прогон немедленно: оно одинаково для
//     всех статей, и следующие сорок попыток подняли бы Chromium впустую.
func runGooglePublishAll(
	ctx context.Context,
	repository googlePublishRepository,
	prompts googlePromptReader,
	publish func(ctx context.Context, job google.Job, observer google.Observer) (google.Result, error),
	logger *slog.Logger,
	out io.Writer,
	taskCommand string,
) error {
	articles, err := repository.GetAll(ctx)
	if err != nil {
		return err
	}

	// Промпты читаются до первого запуска браузера: объём работы должен быть виден сразу, а
	// не выясняться по ходу сорока запусков Chromium.
	type publishTarget struct {
		article article.Article
		prompt  string
	}
	var targets []publishTarget
	var skipped int
	for _, selected := range articles {
		prompt, _, found := articlePromptForPublication(prompts, selected.ExternalID)
		if !found {
			skipped++
			logger.Info("статья пропущена: промпт не сохранён",
				"article_id", selected.ID, "external_id", selected.ExternalID, "stage", "google_publish")
			continue
		}
		targets = append(targets, publishTarget{article: selected, prompt: prompt})
	}

	fmt.Fprintf(out, "К публикации: %d статей, пропущено без промпта: %d\n", len(targets), skipped)
	if len(targets) == 0 {
		fmt.Fprintln(out, "Публиковать нечего: ни у одной статьи нет сохранённого промпта.")
		return nil
	}
	fmt.Fprintln(out, "Документ ищется по имени и перезаписывается, поэтому повторный запуск безопасен.")
	fmt.Fprintln(out)

	var failures []error
	published, created := 0, 0
	for index, target := range targets {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return errors.Join(append(failures, ctxErr)...)
		}
		logger.Info("публикация сохранённого промпта начата",
			"article_id", target.article.ID, "external_id", target.article.ExternalID,
			"stage", "google_publish", "position", index+1, "total", len(targets))

		result, publishErr := publish(ctx, google.Job{
			ArticleID:    target.article.ID,
			ExternalID:   target.article.ExternalID,
			ArticleTitle: target.article.Title,
			Prompt:       target.prompt,
		}, google.SlogObserver{Logger: logger})
		if publishErr != nil {
			failures = append(failures, fmt.Errorf("external_id=%s: %w", target.article.ExternalID, publishErr))
			fmt.Fprintf(out, "[%d/%d] ошибка external_id=%s: %v\n", index+1, len(targets), target.article.ExternalID, publishErr)
			if google.NeedsManualLogin(publishErr) {
				fmt.Fprintf(out, "\nПрогон остановлен: нужен ручной вход. Дальше: make login google\n")
				fmt.Fprintf(out, "Опубликовано до остановки: %d из %d. Повторный запуск продолжит с того же места.\n", published, len(targets))
				return errors.Join(failures...)
			}
			continue
		}

		published++
		action := "обновлён"
		if result.Created {
			action = "создан"
			created++
		}
		// Адрес запоминается сразу после каждой статьи: обрыв на середине не должен стоить
		// уже сделанной работы — ссылки на опубликованное останутся в базе.
		if saveErr := repository.SaveGoogleDocURL(ctx, target.article.ID, result.DocumentURL); saveErr != nil {
			logger.Warn("адрес документа Google не сохранён, ссылки в result.md не будет",
				"article_id", target.article.ID, "external_id", target.article.ExternalID,
				"stage", "google_publish", "document_url", result.DocumentURL, "error", saveErr)
		}
		fmt.Fprintf(out, "[%d/%d] %s external_id=%s: %s\n",
			index+1, len(targets), action, target.article.ExternalID, result.DocumentURL)
	}

	fmt.Fprintf(out, "\nГотово: опубликовано %d из %d (создано %d, обновлено %d), с ошибкой %d.\n",
		published, len(targets), created, published-created, len(failures))
	fmt.Fprintf(out, "Ссылки попадут в result.md при следующей сборке: make %s result\n", taskCommand)
	return errors.Join(failures...)
}

// demoPromptReader читает промпт статьи из каталога DEMO.
type demoPromptReader interface {
	DemoArticlePromptText(externalID string) (text, relativePath string, err error)
}

// demoArticleLoader находит статью, промпт которой публикуется.
type demoArticleLoader interface {
	GetArticleByExternalID(ctx context.Context, externalID string) (article.Article, error)
}

// demoPromptPublisher — очередь публикации. Интерфейс объявлен здесь, у потребителя, чтобы
// проверять выгрузку DEMO без настоящего браузера.
type demoPromptPublisher interface {
	PublishArticlePrompt(generation.ArticlePromptJob)
}

// publishDemoArticlePrompt ставит промпт из DEMO в ту же очередь публикации, что и боевой прогон.
//
// Имя документа общее с боевым — «Промт: <заголовок>»: это один и тот же промпт статьи, и
// разводить его по двум документам значило бы держать в Drive две копии одних данных.
//
// Функция ничего не возвращает намеренно. Публикация не имеет права влиять на результат
// demo-generate: за сборку DEMO уже заплачено обращениями к модели, и отказ Google не должен
// её ронять — тот же контракт, что у PromptPublisher в конвейере.
func publishDemoArticlePrompt(
	ctx context.Context,
	repository demoArticleLoader,
	prompts demoPromptReader,
	publisher demoPromptPublisher,
	logger *slog.Logger,
	externalID string,
) {
	if publisher == nil {
		return
	}
	selected, err := repository.GetArticleByExternalID(ctx, externalID)
	if err != nil {
		logger.Warn("промпт DEMO не опубликован: статья не найдена",
			"external_id", externalID, "stage", "google_publish", "error", err)
		return
	}
	prompt, promptPath, err := prompts.DemoArticlePromptText(externalID)
	if err != nil || strings.TrimSpace(prompt) == "" {
		logger.Info("промпт DEMO не опубликован: файл не сохранён",
			"article_id", selected.ID, "external_id", externalID, "stage", "google_publish",
			"prompt_path", promptPath, "error", err)
		return
	}
	logger.Info("промпт DEMO поставлен в очередь публикации",
		"article_id", selected.ID, "external_id", externalID,
		"stage", "google_publish", "prompt_path", promptPath)
	publisher.PublishArticlePrompt(generation.ArticlePromptJob{
		ArticleID:  selected.ID,
		ExternalID: selected.ExternalID,
		Title:      selected.Title,
		Prompt:     prompt,
		PromptPath: promptPath,
	})
}
