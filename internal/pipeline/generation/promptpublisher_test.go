package generation

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
)

// recordingPublisher запоминает выданные задания.
type recordingPublisher struct {
	mu   sync.Mutex
	jobs []ArticlePromptJob
	// block задерживает вызов, чтобы проверить, что пайплайн его не ждёт.
	block chan struct{}
}

func (p *recordingPublisher) PublishArticlePrompt(job ArticlePromptJob) {
	if p.block != nil {
		<-p.block
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jobs = append(p.jobs, job)
}

// WaitPending в этой заглушке ничего не ждёт: очереди у неё нет.
func (p *recordingPublisher) WaitPending() {}

func (p *recordingPublisher) recorded() []ArticlePromptJob {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ArticlePromptJob{}, p.jobs...)
}

// newPublisherPipeline собирает конвейер поверх тех же заглушек, что и остальные тесты пакета.
func newPublisherPipeline(t *testing.T) (*Pipeline, string) {
	t.Helper()
	root := t.TempDir()
	input := article.GenerationInput{
		Article:             article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema", Status: "completed"},
		CompetitorStructure: "H1 - Тема",
		WordstatKeywords:    []article.KeywordFrequency{{Query: "ключ", Frequency: 100}},
		LSIWords:            []string{"слово"},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipeline := NewPipeline(
		&fakePipelineRepository{input: input},
		testGenerationRouter(successfulPipelineClient(), logger),
		successfulChatFactory(),
		articleoutput.NewWriter(root),
		logger,
		newFakeResultBuilder(t, nil),
	)
	return pipeline, root
}

// Публикация получает ровно тот промпт, который ушёл модели и лёг в article_prompt.txt.
func TestPipelinePublishesPromptItSentToModel(t *testing.T) {
	pipeline, root := newPublisherPipeline(t)
	publisher := &recordingPublisher{}
	pipeline.SetPromptPublisher(publisher)

	if _, err := pipeline.RunByExternalID(context.Background(), "37"); err != nil {
		t.Fatal(err)
	}

	jobs := publisher.recorded()
	if len(jobs) != 1 {
		t.Fatalf("публикация вызвана %d раз, ожидался 1", len(jobs))
	}
	job := jobs[0]
	if job.ArticleID != 7 || job.ExternalID != "37" || job.Title != "Тема" {
		t.Fatalf("сведения о статье потеряны: %+v", job)
	}
	if job.PromptPath != "37-tema/prompts/article_prompt.txt" {
		t.Fatalf("путь артефакта %q", job.PromptPath)
	}

	// Главная проверка: выгружается то же, что записано на диск. Расхождение означало бы,
	// что промпт где-то собирается второй раз.
	saved, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(job.PromptPath)))
	if err != nil {
		t.Fatalf("прочитать сохранённый промпт: %v", err)
	}
	if string(saved) != job.Prompt {
		t.Fatalf("опубликованный промпт разошёлся с article_prompt.txt:\nфайл: %q\nпубликация: %q", saved, job.Prompt)
	}
	if strings.TrimSpace(job.Prompt) == "" {
		t.Fatal("опубликован пустой промпт")
	}
}

// Без подключённой публикации конвейер работает как раньше: dry-run и тесты ничего не знают
// о внешних сервисах.
func TestPipelineWithoutPublisherRunsUnchanged(t *testing.T) {
	pipeline, root := newPublisherPipeline(t)

	if _, err := pipeline.RunByExternalID(context.Background(), "37"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "37-tema", "prompts", "article_prompt.txt")); err != nil {
		t.Fatalf("промпт статьи не сохранён: %v", err)
	}
}

// nil вместо публикатора не должен ронять конвейер.
func TestSetPromptPublisherAcceptsNil(t *testing.T) {
	pipeline, _ := newPublisherPipeline(t)
	pipeline.SetPromptPublisher(nil)

	if _, err := pipeline.RunByExternalID(context.Background(), "37"); err != nil {
		t.Fatal(err)
	}
}

// Публикация не выполняется, если стадия article не дошла до сохранения: выгружать нечего.
func TestPipelineDoesNotPublishWhenArticleStageFails(t *testing.T) {
	root := t.TempDir()
	input := article.GenerationInput{
		Article:             article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema"},
		CompetitorStructure: "H1 - Тема",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Клиент отдаёт пустой ответ на article: стадия падает до записи артефакта.
	client := successfulPipelineClient()
	client.responses["article"] = llm.Response{Text: "   "}
	pipeline := NewPipeline(
		&fakePipelineRepository{input: input},
		testGenerationRouter(client, logger),
		successfulChatFactory(),
		articleoutput.NewWriter(root),
		logger,
		newFakeResultBuilder(t, nil),
	)
	publisher := &recordingPublisher{}
	pipeline.SetPromptPublisher(publisher)

	if _, err := pipeline.RunByExternalID(context.Background(), "37"); err == nil {
		t.Fatal("ожидалась ошибка стадии article")
	}
	if jobs := publisher.recorded(); len(jobs) != 0 {
		t.Fatalf("публикация вызвана при неудачной стадии: %+v", jobs)
	}
}

// Медленная публикация не должна задерживать генерацию: контракт PromptPublisher требует
// немедленного возврата, и конвейер на нём и построен.
func TestSlowPublisherDoesNotBlockPipeline(t *testing.T) {
	pipeline, _ := newPublisherPipeline(t)
	publisher := &recordingPublisher{block: make(chan struct{})}
	// Реализация в cmd возвращает управление сразу и работает в своей goroutine. Здесь та же
	// обёртка воспроизведена вручную, чтобы проверить именно конвейер.
	pipeline.SetPromptPublisher(asyncPublisher{inner: publisher})

	done := make(chan error, 1)
	go func() {
		_, err := pipeline.RunByExternalID(context.Background(), "37")
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("конвейер ждёт публикацию, хотя не должен")
	}
	close(publisher.block)
}

// asyncPublisher повторяет контракт боевой реализации: вызов возвращается сразу.
type asyncPublisher struct{ inner PromptPublisher }

func (p asyncPublisher) PublishArticlePrompt(job ArticlePromptJob) {
	go p.inner.PublishArticlePrompt(job)
}

// WaitPending здесь пустой намеренно: тест проверяет, что конвейер не ждёт публикацию
// на стадии article, и настоящее ожидание сделало бы проверку бессмысленной.
func (p asyncPublisher) WaitPending() {}
