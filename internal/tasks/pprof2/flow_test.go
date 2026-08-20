package pprof2

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/generation"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
)

// fakeChats записывает границы чатов и порядок сообщений: именно это и есть контракт потока.
type fakeChats struct {
	chats   [][]string
	answers map[string]string
}

func (c *fakeChats) NewChat(_ context.Context, _ int64, stages ...string) (taskflow.Chat, error) {
	c.chats = append(c.chats, nil)
	return &fakeChat{owner: c, index: len(c.chats) - 1, stages: stages}, nil
}

type fakeChat struct {
	owner  *fakeChats
	index  int
	stages []string
	sent   int
}

func (c *fakeChat) Send(_ context.Context, prompt string) (string, error) { return c.record(prompt) }

func (c *fakeChat) Continue(_ context.Context, prompt string) (string, error) {
	return c.record(prompt)
}

func (c *fakeChat) record(prompt string) (string, error) {
	if c.sent >= len(c.stages) {
		return "", fmt.Errorf("в чате %d сообщений больше, чем стадий: %v", c.index+1, c.stages)
	}
	stage := c.stages[c.sent]
	c.sent++
	c.owner.chats[c.index] = append(c.owner.chats[c.index], stage)
	if answer, found := c.owner.answers[stage]; found {
		return answer, nil
	}
	return "ответ стадии " + stage + " на промпт " + prompt, nil
}

func (c *fakeChat) Close() error { return nil }

// fakeRenderer подставляет вместо шаблона имя стадии: тест проверяет порядок и границы, а не
// содержимое промптов.
type fakeRenderer struct{ rendered []string }

func (r *fakeRenderer) Prepare(call llm.Call) (llm.PreparedCall, error) {
	r.rendered = append(r.rendered, call.Stage)
	return llm.PreparedCall{Prompt: "промпт " + call.Stage}, nil
}

type fakeRepository struct {
	input            article.GenerationInput
	result           article.ResultInput
	saved            article.SavedGenerationInput
	reviewPath       string
	finalArticlePath string
	htmlPath         string
	stages           []string
	failures         []error
	info             article.ArticleInfo
	infoRawText      string
	infoSaved        bool
}

func (r *fakeRepository) GetGenerationInput(context.Context, string) (article.GenerationInput, error) {
	return r.input, nil
}

func (r *fakeRepository) GetSavedGenerationInput(context.Context, string) (article.SavedGenerationInput, error) {
	return r.saved, nil
}

func (r *fakeRepository) GetResultInput(context.Context, string) (article.ResultInput, error) {
	return r.result, nil
}

func (r *fakeRepository) BeginGeneration(context.Context, int64) error { return nil }

// BeginGenerationStage повторяет контракт репозитория: он принимает имена стадий, а не
// значения current_step. Фейк, принимающий что угодно, пропустил бы ошибку в прод.
func (r *fakeRepository) BeginGenerationStage(_ context.Context, _ int64, stage string) error {
	switch stage {
	case "article", "info", "review", "fix", "html":
		r.stages = append(r.stages, stage)
		return nil
	default:
		return fmt.Errorf("неподдерживаемый отдельный этап генерации %q", stage)
	}
}

func (r *fakeRepository) SaveStructurePath(_ context.Context, _ int64, structurePath string) error {
	r.saved.StructurePath = structurePath
	return nil
}

func (r *fakeRepository) SaveGenerationPaths(_ context.Context, _ int64, _, articlePath string) error {
	r.saved.ArticlePath = articlePath
	return nil
}

// Слот называется review, но у pprof_2 в нём лежит та же страница: ревью в потоке пока нет.
func (r *fakeRepository) SaveReviewPath(_ context.Context, _ int64, reviewPath string) error {
	r.reviewPath = reviewPath
	return nil
}

func (r *fakeRepository) SaveFixedArticlePath(_ context.Context, _ int64, fixedArticlePath string) error {
	r.finalArticlePath = fixedArticlePath
	return nil
}

func (r *fakeRepository) SaveHTMLPath(_ context.Context, _ int64, htmlPath string) error {
	r.htmlPath = htmlPath
	return nil
}

func (r *fakeRepository) SaveArticleInfo(_ context.Context, _ int64, rawText string, info article.ArticleInfo) error {
	r.info, r.infoRawText, r.infoSaved = info, rawText, true
	return nil
}

func (r *fakeRepository) SaveError(_ context.Context, _ int64, processingErr error) error {
	r.failures = append(r.failures, processingErr)
	return nil
}

type recordingPublisher struct{ jobs []generation.ArticlePromptJob }

func (p *recordingPublisher) PublishArticlePrompt(job generation.ArticlePromptJob) {
	p.jobs = append(p.jobs, job)
}

func newFlowFixture(t *testing.T) (*Flow, *fakeChats, *fakeRepository, *recordingPublisher, *articleoutput.Writer) {
	t.Helper()
	writer := articleoutput.NewWriter(t.TempDir())
	selected := article.Article{ID: 7, ExternalID: "7", Title: "Обучение на стропальщика", Slug: "obuchenie-na-stropalshchika"}
	repository := &fakeRepository{
		input: article.GenerationInput{
			Article:             selected,
			CompetitorStructure: "структура конкурента",
			WordstatKeywords:    []article.KeywordFrequency{{Query: "обучение на стропальщика", Frequency: 100}},
			LSIWords:            []string{"удостоверение"},
			Links:               "https://example.test/stropalshchik",
		},
		result: article.ResultInput{
			Article:    selected,
			Category:   "Рабочие профессии",
			Section:    "Профессиональное обучение",
			Profession: "Стропальщик",
			Teachers:   "Иванов И. И.",
			Header:     "Обучение на стропальщика",
			Keyword:    "обучение на стропальщика",
		},
	}
	chats := &fakeChats{answers: map[string]string{
		StageHTML: "<h2>Заголовок</h2><p>текст</p>",
	}}
	publisher := &recordingPublisher{}
	flow := NewFlow(repository, writer, chats, &fakeRenderer{}, nil, publisher)
	return flow, chats, repository, publisher, writer
}

// Границы чатов — главный контракт потока: три чата, и сообщения распределены по ним так,
// как описан порядок стадий pprof_2.
func TestFlowUsesThreeChats(t *testing.T) {
	flow, chats, repository, _, _ := newFlowFixture(t)
	ctx := context.Background()

	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatalf("structure: %v", err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatalf("article: %v", err)
	}
	repository.saved.FixedArticlePath = repository.finalArticlePath
	if err := flow.RunHTML(ctx, "7"); err != nil {
		t.Fatalf("html: %v", err)
	}

	if len(chats.chats) != 3 {
		t.Fatalf("открыто чатов: %d, ожидалось 3 — %v", len(chats.chats), chats.chats)
	}
	want := [][]string{
		{StageStructure},
		{StageArticle},
		{StageHTML},
	}
	for index, expected := range want {
		if strings.Join(chats.chats[index], ",") != strings.Join(expected, ",") {
			t.Fatalf("чат %d: %v, ожидалось %v", index+1, chats.chats[index], expected)
		}
	}
}

// Отдельного запроса за метаданными у pprof_2 нет: FAQ берётся из уже написанной страницы.
//
// Это не мелочь оформления: второй запрос к модели дал бы второй набор вопросов, отличный от
// опубликованного на странице. Проверяется обе половины сразу — ни одна лишняя стадия в чаты
// не ушла, а FAQ в article_metadata всё равно попал.
func TestFlowTakesFAQFromPageWithoutExtraStage(t *testing.T) {
	flow, chats, repository, _, _ := newFlowFixture(t)
	chats.answers[StageArticle] = "H1 - Обучение\n\nтекст\n\nH2 - Частые вопросы\n\n" +
		"H3 - Сколько длится обучение?\n\nОт двух недель.\n\nH3 - Какой документ выдают?\n\nУдостоверение."
	ctx := context.Background()
	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	for _, chat := range chats.chats {
		for _, stage := range chat {
			if stage == "info" || stage == StageSEOEditor || stage == StageReview {
				t.Fatalf("выполнена стадия %q, которой нет в потоке pprof_2", stage)
			}
		}
	}
	for _, stage := range Stages {
		if stage == "info" || stage == StageSEOEditor || stage == StageReview {
			t.Fatalf("стадия %q объявлена в схеме pprof_2, хотя поток её не выполняет", stage)
		}
	}
	if !repository.infoSaved {
		t.Fatal("FAQ не сохранён: article_metadata осталась пустой")
	}
	if !strings.Contains(repository.info.FAQ, "Вопрос: Сколько длится обучение?") ||
		!strings.Contains(repository.info.FAQ, "Ответ: Удостоверение.") {
		t.Fatalf("FAQ разобран не из страницы: %q", repository.info.FAQ)
	}
	if repository.info.TLDR != "" {
		t.Fatalf("у pprof_2 появился TL;DR: %q", repository.info.TLDR)
	}
}

// Файл у страницы один: ревью и SEO-редактуры нет, и слоты движка указывают на текст статьи.
// Пустыми их оставлять нельзя — раннер полного прогона считал бы этап невыполненным вечно.
func TestFlowPointsReviewSlotsAtThePage(t *testing.T) {
	flow, _, repository, _, _ := newFlowFixture(t)
	ctx := context.Background()
	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	page := repository.saved.ArticlePath
	if page == "" {
		t.Fatal("страница не сохранена")
	}
	if repository.reviewPath != page || repository.finalArticlePath != page {
		t.Fatalf("слоты ревью и финального текста: %q и %q, ожидалась страница %q",
			repository.reviewPath, repository.finalArticlePath, page)
	}
}

// Основной промпт уходит и в модель, и в публикацию — это один и тот же текст.
//
// Здесь pprof_2 отличается от pprof_1: там базовый промпт статьи только артефакт, здесь он
// автор страницы. Разойтись двум копиям одного текста негде именно потому, что копия одна.
func TestMainPromptIsSentAndPublished(t *testing.T) {
	flow, chats, _, publisher, _ := newFlowFixture(t)
	ctx := context.Background()

	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}

	var sent bool
	for _, chat := range chats.chats {
		for _, stage := range chat {
			if stage == StageArticle {
				sent = true
			}
		}
	}
	if !sent {
		t.Fatal("основной промпт не ушёл в модель, а страницу пишет именно он")
	}
	if len(publisher.jobs) != 1 {
		t.Fatalf("в публикацию попало %d заданий, ожидалось одно", len(publisher.jobs))
	}
	if publisher.jobs[0].Prompt != "промпт "+StageArticle {
		t.Fatalf("опубликован не основной промпт: %q", publisher.jobs[0].Prompt)
	}
	if !strings.HasSuffix(publisher.jobs[0].PromptPath, "article_prompt.txt") {
		t.Fatalf("промпт сохранён не в артефакт статьи: %q", publisher.jobs[0].PromptPath)
	}
}

// Чат 2 неделим: артефакт страницы и её состояние появляются вместе.
func TestArticleChatSavesEveryArtifact(t *testing.T) {
	flow, _, repository, _, writer := newFlowFixture(t)
	ctx := context.Background()

	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"article":    repository.saved.ArticlePath,
		"final page": repository.finalArticlePath,
		"structure":  repository.saved.StructurePath,
	} {
		if strings.TrimSpace(path) == "" {
			t.Fatalf("артефакт %s не сохранён", name)
		}
		if _, err := writer.Read(path); err != nil {
			t.Fatalf("артефакт %s не читается: %v", name, err)
		}
	}
}

// HTML идёт от сохранённого текста страницы, а не от структуры или промпта.
func TestHTMLUsesSavedPage(t *testing.T) {
	flow, _, repository, _, writer := newFlowFixture(t)
	ctx := context.Background()
	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	repository.saved.FixedArticlePath = repository.finalArticlePath

	page, err := writer.Read(repository.finalArticlePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page, StageArticle) {
		t.Fatalf("в слоте финального текста лежит не страница: %q", page)
	}
	if err := flow.RunHTML(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(repository.htmlPath) == "" {
		t.Fatal("HTML не сохранён")
	}
}

// Без выполненного чата 1 разметка не начинается: структуры ещё нет, и продолжать нечего.
func TestHTMLRequiresFinalArticle(t *testing.T) {
	flow, _, repository, _, _ := newFlowFixture(t)
	err := flow.RunHTML(context.Background(), "7")
	if err == nil {
		t.Fatal("RunHTML без финального текста обязан отказать")
	}
	if len(repository.failures) == 0 {
		t.Fatal("ошибка не сохранена в состоянии статьи")
	}
}
