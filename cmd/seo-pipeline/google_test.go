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
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/generation"
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
	all       []article.Article
	savedURL  string
	savedID   int64
	saveError error
	savedURLs map[string]string
}

func (r *fakePublishRepository) GetArticleByExternalID(_ context.Context, externalID string) (article.Article, error) {
	if r.selected.ExternalID != externalID {
		return article.Article{}, errors.New("статья не найдена")
	}
	return r.selected, nil
}

// GetAll отдаёт все статьи фикстуры: массовой публикации нужен именно список.
func (r *fakePublishRepository) GetAll(context.Context) ([]article.Article, error) {
	if r.all != nil {
		return r.all, nil
	}
	return []article.Article{r.selected}, nil
}

func (r *fakePublishRepository) SaveGoogleDocURL(_ context.Context, articleID int64, documentURL string) error {
	if r.saveError != nil {
		return r.saveError
	}
	r.savedID = articleID
	r.savedURL = documentURL
	if r.savedURLs == nil {
		r.savedURLs = map[string]string{}
	}
	for _, selected := range append([]article.Article{r.selected}, r.all...) {
		if selected.ID == articleID {
			r.savedURLs[selected.ExternalID] = documentURL
		}
	}
	return nil
}

type fakePromptReader struct {
	text string
	path string
	err  error
	// byExternalID задаёт промпт на статью. Нужен массовой публикации: статья без промпта
	// должна пропускаться, а не срывать прогон.
	byExternalID map[string]string
	// demoByExternalID — промпт из DEMO, запасной источник для статей без боевого прогона.
	demoByExternalID map[string]string
}

func (r *fakePromptReader) ArticlePromptText(externalID string) (string, string, error) {
	if r.byExternalID != nil {
		return r.byExternalID[externalID], r.path, nil
	}
	return r.text, r.path, r.err
}

// demoByExternalID — промпты статей, прошедших только demo-generate.
func (r *fakePromptReader) DemoArticlePromptText(externalID string) (string, string, error) {
	if r.demoByExternalID == nil {
		return "", "", errors.New("промпт DEMO не сохранён")
	}
	return r.demoByExternalID[externalID], externalID + "/DEMO/prompts/article_prompt.txt", nil
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
		}, discardGoogleLogger(), &out, "task-1", "45")
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
		}, discardGoogleLogger(), &bytes.Buffer{}, "task-1", "45")
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
		}, discardGoogleLogger(), &bytes.Buffer{}, "task-1", "45")
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
		}, discardGoogleLogger(), &bytes.Buffer{}, "task-1", "45")
	if err == nil {
		t.Fatal("ожидалась ошибка истёкшей сессии")
	}
	// Вход стал глобальным, поэтому подсказка называет make login google, а не задачную форму.
	if !strings.Contains(err.Error(), "login google") {
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
		// Без ID публикуются все статьи с сохранённым промптом: публикация идемпотентна,
		// документ ищется по имени и перезаписывается.
		{name: "google-publish без ID", args: []string{"seo-pipeline", "task-1", "google-publish"}, wantName: "google-publish"},
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

// newBatchPublishFixtures готовит три статьи, из которых промпт есть только у двух.
func newBatchPublishFixtures() (*fakePublishRepository, *fakePromptReader) {
	all := []article.Article{
		{ID: 1, ExternalID: "1", Title: "Первая"},
		{ID: 2, ExternalID: "2", Title: "Вторая"},
		{ID: 3, ExternalID: "3", Title: "Третья"},
	}
	return &fakePublishRepository{all: all},
		&fakePromptReader{byExternalID: map[string]string{"1": "промпт один", "3": "промпт три"}}
}

// Статья без сохранённого промпта — обычное состояние после импорта, а не ошибка прогона.
func TestGooglePublishAllSkipsArticlesWithoutPrompt(t *testing.T) {
	repository, prompts := newBatchPublishFixtures()
	var published []string
	var out bytes.Buffer

	err := runGooglePublish(context.Background(), repository, prompts,
		func(_ context.Context, job google.Job, _ google.Observer) (google.Result, error) {
			published = append(published, job.ExternalID)
			return google.Result{Created: true, DocumentURL: "https://docs.google.com/document/d/" + job.ExternalID + "/edit"}, nil
		}, discardGoogleLogger(), &out, "task-1", "")
	if err != nil {
		t.Fatalf("массовая публикация вернула ошибку: %v", err)
	}
	if strings.Join(published, ",") != "1,3" {
		t.Fatalf("опубликованы %v, ожидались только статьи с промптом 1 и 3", published)
	}
	if !strings.Contains(out.String(), "пропущено без промпта: 1") {
		t.Fatalf("отчёт не назвал пропущенные статьи:\n%s", out.String())
	}
	if repository.savedURLs["1"] == "" || repository.savedURLs["3"] == "" {
		t.Fatalf("адреса документов не сохранены: %v", repository.savedURLs)
	}
}

// Отказ на одной статье не должен оставлять неопубликованными все следующие за ней.
func TestGooglePublishAllContinuesAfterArticleFailure(t *testing.T) {
	repository, prompts := newBatchPublishFixtures()
	var attempted []string
	var out bytes.Buffer

	err := runGooglePublish(context.Background(), repository, prompts,
		func(_ context.Context, job google.Job, _ google.Observer) (google.Result, error) {
			attempted = append(attempted, job.ExternalID)
			if job.ExternalID == "1" {
				return google.Result{}, errors.New("Google недоступен")
			}
			return google.Result{DocumentURL: "https://docs.google.com/document/d/3/edit"}, nil
		}, discardGoogleLogger(), &out, "task-1", "")
	if err == nil {
		t.Fatal("ошибка отдельной статьи должна возвращаться наружу")
	}
	if strings.Join(attempted, ",") != "1,3" {
		t.Fatalf("после отказа на статье 1 прогон не дошёл до статьи 3: %v", attempted)
	}
	if repository.savedURLs["3"] == "" {
		t.Fatal("успешная статья после отказа не сохранила адрес")
	}
}

// Требование ручного входа одинаково для всех статей, поэтому прогон обрывается сразу.
func TestGooglePublishAllStopsWhenLoginIsRequired(t *testing.T) {
	repository, prompts := newBatchPublishFixtures()
	var attempted []string
	var out bytes.Buffer

	err := runGooglePublish(context.Background(), repository, prompts,
		func(_ context.Context, job google.Job, _ google.Observer) (google.Result, error) {
			attempted = append(attempted, job.ExternalID)
			return google.Result{}, &google.StageError{Stage: "find_document", Err: google.ErrSessionExpired}
		}, discardGoogleLogger(), &out, "task-1", "")
	if err == nil {
		t.Fatal("ожидалась ошибка истёкшей сессии")
	}
	if len(attempted) != 1 {
		t.Fatalf("после требования входа прогон продолжился: %v", attempted)
	}
	if !strings.Contains(out.String(), "make login google") {
		t.Fatalf("отчёт не подсказал вход:\n%s", out.String())
	}
}

// Повторный запуск не создаёт копий: документ ищется по имени, и второй прогон обновляет
// тот же документ. Идемпотентность держится на этом, а не на состоянии в базе.
func TestGooglePublishAllIsIdempotentAcrossRuns(t *testing.T) {
	repository, prompts := newBatchPublishFixtures()
	existing := map[string]string{}

	publish := func(_ context.Context, job google.Job, _ google.Observer) (google.Result, error) {
		title := google.DocumentTitle(job.ArticleTitle)
		if url, found := existing[title]; found {
			return google.Result{Created: false, DocumentURL: url}, nil
		}
		url := "https://docs.google.com/document/d/" + job.ExternalID + "/edit"
		existing[title] = url
		return google.Result{Created: true, DocumentURL: url}, nil
	}

	var first, second bytes.Buffer
	if err := runGooglePublish(context.Background(), repository, prompts, publish,
		discardGoogleLogger(), &first, "task-1", ""); err != nil {
		t.Fatalf("первый прогон: %v", err)
	}
	if err := runGooglePublish(context.Background(), repository, prompts, publish,
		discardGoogleLogger(), &second, "task-1", ""); err != nil {
		t.Fatalf("второй прогон: %v", err)
	}

	if len(existing) != 2 {
		t.Fatalf("после двух прогонов документов %d, ожидалось 2: появились копии", len(existing))
	}
	if !strings.Contains(first.String(), "создано 2") {
		t.Fatalf("первый прогон должен был создать оба документа:\n%s", first.String())
	}
	if !strings.Contains(second.String(), "создано 0") {
		t.Fatalf("второй прогон создал документы заново:\n%s", second.String())
	}
}

// fakeDemoPromptReader отдаёт промпт DEMO по статье.
type fakeDemoPromptReader struct {
	byExternalID map[string]string
	err          error
}

func (r *fakeDemoPromptReader) DemoArticlePromptText(externalID string) (string, string, error) {
	if r.err != nil {
		return "", "", r.err
	}
	return r.byExternalID[externalID], externalID + "/DEMO/prompts/article_prompt.txt", nil
}

// recordingPromptPublisher запоминает поставленные в очередь задания.
type recordingPromptPublisher struct{ jobs []generation.ArticlePromptJob }

func (p *recordingPromptPublisher) PublishArticlePrompt(job generation.ArticlePromptJob) {
	p.jobs = append(p.jobs, job)
}

// DEMO выгружается под тем же именем документа, что и боевой прогон: промпт статьи один и
// тот же, и второй копии тех же данных в Drive быть не должно.
func TestDemoPromptGoesToProductionDocumentTitle(t *testing.T) {
	repository := &fakePublishRepository{selected: article.Article{ID: 9, ExternalID: "45", Title: "Как выбрать фрезу"}}
	prompts := &fakeDemoPromptReader{byExternalID: map[string]string{"45": "промпт DEMO"}}
	publisher := &recordingPromptPublisher{}

	publishDemoArticlePrompt(context.Background(), repository, prompts, publisher, discardGoogleLogger(), "45")

	if len(publisher.jobs) != 1 {
		t.Fatalf("в очередь попало %d заданий, ожидалось одно", len(publisher.jobs))
	}
	job := publisher.jobs[0]
	if job.Prompt != "промпт DEMO" {
		t.Fatalf("опубликован не промпт DEMO: %q", job.Prompt)
	}
	if google.DocumentTitle(job.Title) != "Промт: Как выбрать фрезу" {
		t.Fatalf("имя документа разошлось с боевым: %q", google.DocumentTitle(job.Title))
	}
	if job.ArticleID != 9 || job.ExternalID != "45" {
		t.Fatalf("задание собрано не по той статье: %+v", job)
	}
}

// Статья без промпта DEMO пропускается молча: сборки ещё не было, публиковать нечего.
func TestDemoPromptSkippedWhenFileIsMissing(t *testing.T) {
	repository := &fakePublishRepository{selected: article.Article{ID: 9, ExternalID: "45", Title: "Как выбрать фрезу"}}
	publisher := &recordingPromptPublisher{}

	publishDemoArticlePrompt(context.Background(), repository,
		&fakeDemoPromptReader{byExternalID: map[string]string{}}, publisher, discardGoogleLogger(), "45")
	publishDemoArticlePrompt(context.Background(), repository,
		&fakeDemoPromptReader{err: errors.New("файла нет")}, publisher, discardGoogleLogger(), "45")

	if len(publisher.jobs) != 0 {
		t.Fatalf("без промпта в очередь попало %d заданий", len(publisher.jobs))
	}
}

// Ненайденная статья не должна ронять demo-generate: функция ничего не возвращает и только
// пишет предупреждение.
func TestDemoPromptSurvivesMissingArticle(t *testing.T) {
	repository := &fakePublishRepository{selected: article.Article{ID: 9, ExternalID: "other"}}
	publisher := &recordingPromptPublisher{}

	publishDemoArticlePrompt(context.Background(), repository,
		&fakeDemoPromptReader{byExternalID: map[string]string{"45": "промпт DEMO"}},
		publisher, discardGoogleLogger(), "45")

	if len(publisher.jobs) != 0 {
		t.Fatal("для ненайденной статьи публикация не ставится в очередь")
	}
}

// Статья, прошедшая только demo-generate, боевого промпта не имеет. Без запасного источника
// она молча выпадала бы из массовой выгрузки — а именно так выглядит половина таблицы, пока
// боевая генерация не отработала.
func TestGooglePublishAllFallsBackToDemoPrompt(t *testing.T) {
	repository, prompts := newBatchPublishFixtures()
	prompts.demoByExternalID = map[string]string{"2": "промпт DEMO"}
	var published []string
	var out bytes.Buffer

	err := runGooglePublish(context.Background(), repository, prompts,
		func(_ context.Context, job google.Job, _ google.Observer) (google.Result, error) {
			published = append(published, job.ExternalID)
			return google.Result{DocumentURL: "https://docs.google.com/document/d/" + job.ExternalID + "/edit"}, nil
		}, discardGoogleLogger(), &out, "task-1", "")
	if err != nil {
		t.Fatalf("массовая публикация вернула ошибку: %v", err)
	}
	if strings.Join(published, ",") != "1,2,3" {
		t.Fatalf("опубликованы %v, ожидалась и статья 2 из DEMO", published)
	}
	if !strings.Contains(out.String(), "пропущено без промпта: 0") {
		t.Fatalf("статья с промптом DEMO попала в пропущенные:\n%s", out.String())
	}
}

// Боевой промпт приоритетнее: он и есть то, что видела модель.
func TestGooglePublishPrefersProductionPromptOverDemo(t *testing.T) {
	repository, prompts := newBatchPublishFixtures()
	prompts.demoByExternalID = map[string]string{"1": "промпт DEMO"}
	var received string

	err := runGooglePublish(context.Background(), repository, prompts,
		func(_ context.Context, job google.Job, _ google.Observer) (google.Result, error) {
			if job.ExternalID == "1" {
				received = job.Prompt
			}
			return google.Result{DocumentURL: "https://docs.google.com/document/d/1/edit"}, nil
		}, discardGoogleLogger(), &bytes.Buffer{}, "task-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if received != "промпт один" {
		t.Fatalf("опубликован %q, ожидался боевой промпт", received)
	}
}
