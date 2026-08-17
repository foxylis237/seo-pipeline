package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/integrations/google"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/generation"
)

func discardGoogleLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// newTestGooglePublisher собирает публикатор с подменённой публикацией: браузер не поднимается.
func newTestGooglePublisher(ctx context.Context, publish func(context.Context, google.Job, google.Observer) (google.Result, error)) *googlePublisher {
	return &googlePublisher{
		ctx:      ctx,
		publish:  publish,
		observer: google.SlogObserver{Logger: discardGoogleLogger()},
		logger:   discardGoogleLogger(),
	}
}

// savedURLs собирает адреса, которые публикатор записал в базу.
type savedURLs struct {
	mu    sync.Mutex
	byID  map[int64]string
	fails bool
}

func newSavedURLs() *savedURLs { return &savedURLs{byID: map[int64]string{}} }

func (s *savedURLs) save(_ context.Context, articleID int64, documentURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fails {
		return errors.New("база недоступна")
	}
	s.byID[articleID] = documentURL
	return nil
}

func (s *savedURLs) get(articleID int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byID[articleID]
}

// Успешная публикация запоминает адрес: без него result.md не напечатает ссылку.
func TestPublisherStoresDocumentURL(t *testing.T) {
	saved := newSavedURLs()
	publisher := newTestGooglePublisher(context.Background(),
		func(context.Context, google.Job, google.Observer) (google.Result, error) {
			return google.Result{Created: true, DocumentURL: "https://docs.google.com/document/d/AbC123/edit"}, nil
		})
	publisher.saveURL = saved.save

	publisher.PublishArticlePrompt(testPromptJob())
	publisher.Wait()

	if got := saved.get(9); got != "https://docs.google.com/document/d/AbC123/edit" {
		t.Fatalf("адрес не сохранён: %q", got)
	}
}

// Неудачная публикация не записывает адрес: пустая ссылка в result.md хуже отсутствующей.
func TestPublisherDoesNotStoreURLAfterFailure(t *testing.T) {
	saved := newSavedURLs()
	publisher := newTestGooglePublisher(context.Background(),
		func(context.Context, google.Job, google.Observer) (google.Result, error) {
			return google.Result{}, errors.New("Google недоступен")
		})
	publisher.saveURL = saved.save

	publisher.PublishArticlePrompt(testPromptJob())
	publisher.Wait()

	if got := saved.get(9); got != "" {
		t.Fatalf("после неудачи сохранён адрес %q", got)
	}
}

// Сбой записи адреса не должен ронять прогон: документ в Drive уже актуален.
func TestPublisherSurvivesFailedURLSave(t *testing.T) {
	saved := newSavedURLs()
	saved.fails = true
	publisher := newTestGooglePublisher(context.Background(),
		func(context.Context, google.Job, google.Observer) (google.Result, error) {
			return google.Result{DocumentURL: "https://docs.google.com/document/d/AbC123/edit"}, nil
		})
	publisher.saveURL = saved.save

	publisher.PublishArticlePrompt(testPromptJob())
	publisher.Wait()
}

// WaitPending — то же ожидание, что и Wait: конвейер зовёт его перед сборкой result.md,
// чтобы ссылка успела попасть в базу.
func TestWaitPendingDrainsQueueBeforeResult(t *testing.T) {
	saved := newSavedURLs()
	publisher := newTestGooglePublisher(context.Background(),
		func(context.Context, google.Job, google.Observer) (google.Result, error) {
			time.Sleep(20 * time.Millisecond)
			return google.Result{DocumentURL: "https://docs.google.com/document/d/AbC123/edit"}, nil
		})
	publisher.saveURL = saved.save

	publisher.PublishArticlePrompt(testPromptJob())
	publisher.WaitPending()

	if saved.get(9) == "" {
		t.Fatal("WaitPending вернулся до того, как адрес попал в базу")
	}
}

func testPromptJob() generation.ArticlePromptJob {
	return generation.ArticlePromptJob{
		ArticleID: 9, ExternalID: "45", Title: "Как выбрать фрезу",
		Prompt: "готовый промпт", PromptPath: "45-kak-vybrat-frezu/prompts/article_prompt.txt",
	}
}

// Публикация не должна задерживать генерацию: вызов возвращает управление до её конца.
func TestPublishArticlePromptDoesNotBlockCaller(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	publisher := newTestGooglePublisher(context.Background(),
		func(context.Context, google.Job, google.Observer) (google.Result, error) {
			close(started)
			<-release
			return google.Result{}, nil
		})

	returned := make(chan struct{})
	go func() {
		publisher.PublishArticlePrompt(testPromptJob())
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("PublishArticlePrompt заблокировал вызывающего")
	}
	<-started
	close(release)
	publisher.Wait()
}

// Wait обязан дождаться очереди: иначе команда завершится с живым Chromium.
func TestWaitDrainsQueuedPublications(t *testing.T) {
	var done atomic.Int32
	publisher := newTestGooglePublisher(context.Background(),
		func(context.Context, google.Job, google.Observer) (google.Result, error) {
			time.Sleep(10 * time.Millisecond)
			done.Add(1)
			return google.Result{}, nil
		})

	for range 5 {
		publisher.PublishArticlePrompt(testPromptJob())
	}
	publisher.Wait()

	if got := done.Load(); got != 5 {
		t.Fatalf("выполнено %d публикаций из 5: Wait не дождался очереди", got)
	}
}

// За профилем держится flock, поэтому две публикации одновременно идти не должны.
func TestPublicationsRunOneAtATime(t *testing.T) {
	var running, peak atomic.Int32
	publisher := newTestGooglePublisher(context.Background(),
		func(context.Context, google.Job, google.Observer) (google.Result, error) {
			current := running.Add(1)
			for {
				recorded := peak.Load()
				if current <= recorded || peak.CompareAndSwap(recorded, current) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			running.Add(-1)
			return google.Result{}, nil
		})

	for range 4 {
		publisher.PublishArticlePrompt(testPromptJob())
	}
	publisher.Wait()

	if peak.Load() != 1 {
		t.Fatalf("одновременно шло %d публикаций: профиль браузера этого не переживёт", peak.Load())
	}
}

// Отмена команды прекращает публикацию, а не оставляет фоновую работу висеть.
func TestCancelledCommandSkipsPendingPublications(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	publisher := newTestGooglePublisher(ctx,
		func(context.Context, google.Job, google.Observer) (google.Result, error) {
			calls.Add(1)
			return google.Result{}, nil
		})

	publisher.PublishArticlePrompt(testPromptJob())
	publisher.Wait()

	if calls.Load() != 0 {
		t.Fatal("при отменённой команде публикация выполняться не должна")
	}
}

// Публикация получает ровно тот промпт, который пайплайн отправил модели.
func TestPublisherForwardsPromptUnchanged(t *testing.T) {
	var received google.Job
	var mu sync.Mutex
	publisher := newTestGooglePublisher(context.Background(),
		func(_ context.Context, job google.Job, _ google.Observer) (google.Result, error) {
			mu.Lock()
			defer mu.Unlock()
			received = job
			return google.Result{}, nil
		})

	job := testPromptJob()
	publisher.PublishArticlePrompt(job)
	publisher.Wait()

	mu.Lock()
	defer mu.Unlock()
	if received.Prompt != job.Prompt {
		t.Fatalf("промпт изменён: %q", received.Prompt)
	}
	if received.ArticleTitle != job.Title || received.ExternalID != job.ExternalID || received.ArticleID != job.ArticleID {
		t.Fatalf("сведения о статье потеряны: %+v", received)
	}
}

// Ошибка публикации не должна валить генерацию: она остаётся в логе.
func TestPublicationFailureDoesNotPanicOrBlock(t *testing.T) {
	publisher := newTestGooglePublisher(context.Background(),
		func(context.Context, google.Job, google.Observer) (google.Result, error) {
			return google.Result{}, errors.New("Google недоступен")
		})

	publisher.PublishArticlePrompt(testPromptJob())
	publisher.Wait()
}

// --- ручная публикация ---

type fakePublishRepository struct {
	selected  article.Article
	savedURL  string
	savedID   int64
	saveError error
}

func (r *fakePublishRepository) GetArticleByExternalID(_ context.Context, externalID string) (article.Article, error) {
	if r.selected.ExternalID != externalID {
		return article.Article{}, errors.New("статья не найдена")
	}
	return r.selected, nil
}

func (r *fakePublishRepository) SaveGoogleDocURL(_ context.Context, articleID int64, documentURL string) error {
	if r.saveError != nil {
		return r.saveError
	}
	r.savedID = articleID
	r.savedURL = documentURL
	return nil
}

type fakePromptReader struct {
	text string
	path string
	err  error
}

func (r *fakePromptReader) ArticlePromptText(string) (string, string, error) {
	return r.text, r.path, r.err
}

func newPublishFixtures() (*fakePublishRepository, *fakePromptReader) {
	return &fakePublishRepository{
			selected: article.Article{ID: 9, ExternalID: "45", Title: "Как выбрать фрезу"},
		}, &fakePromptReader{
			text: "готовый промпт", path: "45-kak-vybrat-frezu/prompts/article_prompt.txt",
		}
}

// Ручная публикация берёт сохранённый промпт и никуда больше не ходит.
func TestGooglePublishUsesSavedPrompt(t *testing.T) {
	repository, prompts := newPublishFixtures()
	var received google.Job
	var out bytes.Buffer

	err := runGooglePublish(context.Background(), repository, prompts,
		func(_ context.Context, job google.Job, _ google.Observer) (google.Result, error) {
			received = job
			return google.Result{Created: true, DocumentURL: "https://docs.google.com/document/d/new/edit"}, nil
		}, discardGoogleLogger(), &out, "45")
	if err != nil {
		t.Fatalf("runGooglePublish вернул ошибку: %v", err)
	}
	if received.Prompt != "готовый промпт" {
		t.Fatalf("опубликован не сохранённый промпт: %q", received.Prompt)
	}
	if received.ArticleTitle != "Как выбрать фрезу" {
		t.Fatalf("название статьи потеряно: %q", received.ArticleTitle)
	}
	if repository.savedURL != "https://docs.google.com/document/d/new/edit" || repository.savedID != 9 {
		t.Fatalf("ручная публикация не сохранила адрес: id=%d url=%q", repository.savedID, repository.savedURL)
	}
	report := out.String()
	if !strings.Contains(report, "Промт: Как выбрать фрезу") {
		t.Fatalf("в отчёте нет имени документа:\n%s", report)
	}
	if !strings.Contains(report, "создан") {
		t.Fatalf("в отчёте нет действия:\n%s", report)
	}
}

// Пустой промпт до Google не доходит: пустой документ затёр бы прошлый.
func TestGooglePublishRejectsEmptyPrompt(t *testing.T) {
	repository, prompts := newPublishFixtures()
	prompts.text = "   "
	published := false

	err := runGooglePublish(context.Background(), repository, prompts,
		func(context.Context, google.Job, google.Observer) (google.Result, error) {
			published = true
			return google.Result{}, nil
		}, discardGoogleLogger(), &bytes.Buffer{}, "45")
	if err == nil {
		t.Fatal("ожидалась ошибка на пустом промпте")
	}
	if published {
		t.Fatal("пустой промпт публиковать нельзя")
	}
}

// Несохранённый промпт — понятная ошибка, а не поход в браузер.
func TestGooglePublishReportsMissingPrompt(t *testing.T) {
	repository, prompts := newPublishFixtures()
	prompts.err = errors.New("промпт статьи external_id \"45\" ещё не сохранён")
	published := false

	err := runGooglePublish(context.Background(), repository, prompts,
		func(context.Context, google.Job, google.Observer) (google.Result, error) {
			published = true
			return google.Result{}, nil
		}, discardGoogleLogger(), &bytes.Buffer{}, "45")
	if err == nil {
		t.Fatal("ожидалась ошибка отсутствующего промпта")
	}
	if published {
		t.Fatal("без промпта публикация запускаться не должна")
	}
}

// Истёкшая сессия подсказывает ровно ту команду, которая её чинит.
func TestGooglePublishSuggestsLoginOnExpiredSession(t *testing.T) {
	repository, prompts := newPublishFixtures()

	err := runGooglePublish(context.Background(), repository, prompts,
		func(context.Context, google.Job, google.Observer) (google.Result, error) {
			return google.Result{}, &google.StageError{Stage: "find_document", Err: google.ErrSessionExpired}
		}, discardGoogleLogger(), &bytes.Buffer{}, "45")
	if err == nil {
		t.Fatal("ожидалась ошибка истёкшей сессии")
	}
	if !strings.Contains(err.Error(), "google-login") {
		t.Fatalf("ошибка не подсказывает вход: %v", err)
	}
}

func TestParseGoogleCommands(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantError  bool
		wantName   string
		externalID string
	}{
		{name: "google-login", args: []string{"seo-pipeline", "task-1", "google-login"}, wantName: "google-login"},
		{name: "google-login с аргументом", args: []string{"seo-pipeline", "task-1", "google-login", "45"}, wantError: true},
		{name: "google-publish с ID", args: []string{"seo-pipeline", "task-1", "google-publish", "45"}, wantName: "google-publish", externalID: "45"},
		// ID обязателен: массовая публикация в чужой Drive одним словом недопустима.
		{name: "google-publish без ID", args: []string{"seo-pipeline", "task-1", "google-publish"}, wantError: true},
		{name: "google-publish с нулём", args: []string{"seo-pipeline", "task-1", "google-publish", "0"}, wantError: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			command, err := parseCommand(testCase.args)
			if testCase.wantError {
				if err == nil {
					t.Fatalf("ожидалась ошибка, получено %+v", command)
				}
				return
			}
			if err != nil {
				t.Fatalf("разбор завершился ошибкой: %v", err)
			}
			if command.Name != testCase.wantName {
				t.Fatalf("операция %q, ожидалась %q", command.Name, testCase.wantName)
			}
			if command.ExternalID != testCase.externalID {
				t.Fatalf("ExternalID=%q, ожидался %q", command.ExternalID, testCase.externalID)
			}
		})
	}
}
