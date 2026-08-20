package pprof1

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

func (c *fakeChat) Send(ctx context.Context, prompt string) (string, error) {
	return c.record(prompt)
}

func (c *fakeChat) Continue(ctx context.Context, prompt string) (string, error) {
	return c.record(prompt)
}

func (c *fakeChat) record(prompt string) (string, error) {
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
	input             article.GenerationInput
	saved             article.SavedGenerationInput
	savedInfo         string
	editedArticlePath string
	finalArticlePath  string
	htmlPath          string
	stages            []string
	failures          []error
}

func (r *fakeRepository) GetGenerationInput(context.Context, string) (article.GenerationInput, error) {
	return r.input, nil
}

func (r *fakeRepository) GetSavedGenerationInput(context.Context, string) (article.SavedGenerationInput, error) {
	return r.saved, nil
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

func (r *fakeRepository) SaveArticleInfo(_ context.Context, _ int64, rawText string, _ article.ArticleInfo) error {
	r.savedInfo = rawText
	return nil
}

// Слот называется review, но у pprof_1 в нём лежит статья после SEO-редактуры.
func (r *fakeRepository) SaveReviewPath(_ context.Context, _ int64, reviewPath string) error {
	r.editedArticlePath = reviewPath
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
	repository := &fakeRepository{
		input: article.GenerationInput{
			Article:             article.Article{ID: 7, ExternalID: "7", Title: "Как стать логопедом", Slug: "kak-stat-logopedom"},
			CompetitorStructure: "структура конкурента",
			WordstatKeywords:    []article.KeywordFrequency{{Query: "логопед", Frequency: 100}},
			LSIWords:            []string{"дефектолог"},
			Professions:         "логопед, дефектолог, педагог",
			Links:               "https://example.test/logoped",
		},
	}
	chats := &fakeChats{answers: map[string]string{
		StageInfo: "TLDR: коротко\nFAQ:\nВопрос: Где?\nОтвет: Тут.",
		StageHTML: "<h2>Заголовок</h2><p>текст</p>",
	}}
	publisher := &recordingPublisher{}
	flow := NewFlow(repository, writer, chats, &fakeRenderer{}, nil, publisher)
	return flow, chats, repository, publisher, writer
}

// Границы чатов — главный контракт потока: три чата, и сообщения распределены по ним так,
// как описан порядок стадий pprof_1.
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
		{StageExpert, StageSEOEditor, StageInfo, StageReview},
		{StageHTML},
	}
	for index, expected := range want {
		if strings.Join(chats.chats[index], ",") != strings.Join(expected, ",") {
			t.Fatalf("чат %d: %v, ожидалось %v", index+1, chats.chats[index], expected)
		}
	}
}

// Старый базовый промпт статьи собирается и уходит в публикацию, но модель его не получает.
func TestBaseArticlePromptIsPublishedButNeverSent(t *testing.T) {
	flow, chats, _, publisher, _ := newFlowFixture(t)

	if err := flow.RunStructure(context.Background(), "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(context.Background(), "7"); err != nil {
		t.Fatal(err)
	}

	for _, chat := range chats.chats {
		for _, stage := range chat {
			if stage == StageArticle {
				t.Fatal("базовый промпт статьи ушёл в модель, а он только артефакт и документ")
			}
		}
	}
	if len(publisher.jobs) != 1 {
		t.Fatalf("в публикацию попало %d заданий, ожидалось одно", len(publisher.jobs))
	}
	if publisher.jobs[0].Prompt != "промпт "+StageArticle {
		t.Fatalf("опубликован не базовый промпт статьи: %q", publisher.jobs[0].Prompt)
	}
	if !strings.HasSuffix(publisher.jobs[0].PromptPath, "article_prompt.txt") {
		t.Fatalf("промпт сохранён не в артефакт статьи: %q", publisher.jobs[0].PromptPath)
	}
}

// Чат 2 неделим: все его артефакты и метаданные появляются вместе.
func TestArticleChatSavesEveryArtifact(t *testing.T) {
	flow, _, repository, _, writer := newFlowFixture(t)

	if err := flow.RunStructure(context.Background(), "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(context.Background(), "7"); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"article":        repository.saved.ArticlePath,
		"edited article": repository.editedArticlePath,
		"final article":  repository.finalArticlePath,
	} {
		if strings.TrimSpace(path) == "" {
			t.Fatalf("артефакт %s не сохранён", name)
		}
		if _, err := writer.Read(path); err != nil {
			t.Fatalf("артефакт %s не читается: %v", name, err)
		}
	}
	if strings.TrimSpace(repository.savedInfo) == "" {
		t.Fatal("метаданные не сохранены")
	}
}

// HTML идёт от финального текста после ревью, а не от черновика специалиста.
func TestHTMLUsesReviewedArticle(t *testing.T) {
	flow, _, repository, _, writer := newFlowFixture(t)
	ctx := context.Background()
	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	repository.saved.FixedArticlePath = repository.finalArticlePath

	reviewed, err := writer.Read(repository.finalArticlePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reviewed, StageReview) {
		t.Fatalf("финальный текст собран не стадией review: %q", reviewed)
	}
	if err := flow.RunHTML(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(repository.htmlPath) == "" {
		t.Fatal("HTML не сохранён")
	}
}

// Без структуры чат 2 не начинается: писать статью не по чему.
func TestArticleChatRequiresStructure(t *testing.T) {
	flow, chats, _, _, _ := newFlowFixture(t)

	if err := flow.RunArticle(context.Background(), "7"); err == nil {
		t.Fatal("чат 2 стартовал без сохранённой структуры")
	}
	if len(chats.chats) != 0 {
		t.Fatalf("открыто %d чатов до проверки структуры", len(chats.chats))
	}
}

// Поток обязан переводить статью по этапам именами, которые понимает репозиторий. Ошибку
// в этом месте не видно ни в компиляторе, ни в логике потока — только в проде на первом
// прогоне, поэтому проверка отдельная.
func TestFlowMovesArticleThroughKnownSteps(t *testing.T) {
	flow, _, repository, _, _ := newFlowFixture(t)
	ctx := context.Background()

	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatalf("structure: %v", err)
	}
	// Структуру начинает BeginGeneration, отдельного перехода этапа у неё нет.
	if len(repository.stages) != 0 {
		t.Fatalf("structure запросила переход этапа %v, а его ставит BeginGeneration", repository.stages)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatalf("article: %v", err)
	}
	repository.saved.FixedArticlePath = repository.finalArticlePath
	if err := flow.RunHTML(ctx, "7"); err != nil {
		t.Fatalf("html: %v", err)
	}

	if got := strings.Join(repository.stages, ","); got != "article,html" {
		t.Fatalf("переходы этапов: %q, ожидалось \"article,html\"", got)
	}
}
