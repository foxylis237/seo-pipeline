package pproftemplate1

import (
	"context"
	"errors"
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
	// failures роняет стадию по имени — так проверяется возобновление после упавшего ревью.
	failures map[string]error
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
func (c *fakeChat) Close() error { return nil }

func (c *fakeChat) record(prompt string) (string, error) {
	stage := c.stages[c.sent]
	c.sent++
	c.owner.chats[c.index] = append(c.owner.chats[c.index], stage)
	if err, found := c.owner.failures[stage]; found {
		return "", err
	}
	if answer, found := c.owner.answers[stage]; found {
		return answer, nil
	}
	return "ответ стадии " + stage + " на промпт " + prompt, nil
}

// fakeRenderer подставляет вместо шаблона имя стадии и запоминает данные промпта: тест
// проверяет, что уходит в стадию, а не как выглядит её текст.
type fakeRenderer struct{ data map[string]any }

func (r *fakeRenderer) Prepare(call llm.Call) (llm.PreparedCall, error) {
	if r.data == nil {
		r.data = make(map[string]any)
	}
	r.data[call.Stage] = call.Data
	return llm.PreparedCall{Prompt: "промпт " + call.Stage}, nil
}

type fakeRepository struct {
	input            article.GenerationInput
	saved            article.SavedGenerationInput
	savedInfo        string
	reviewPath       string
	finalArticlePath string
	htmlPath         string
	failures         []error
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

func (r *fakeRepository) SaveReviewPath(_ context.Context, _ int64, reviewPath string) error {
	r.reviewPath = reviewPath
	return nil
}

func (r *fakeRepository) SaveFixedArticlePath(_ context.Context, _ int64, fixedArticlePath string) error {
	r.finalArticlePath = fixedArticlePath
	r.saved.FixedArticlePath = fixedArticlePath
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

type flowFixture struct {
	flow       *Flow
	chats      *fakeChats
	repository *fakeRepository
	renderer   *fakeRenderer
	publisher  *recordingPublisher
	writer     *articleoutput.Writer
}

func newFlowFixture(t *testing.T) flowFixture {
	t.Helper()
	writer := articleoutput.NewWriter(t.TempDir())
	repository := &fakeRepository{
		input: article.GenerationInput{
			Article:             article.Article{ID: 7, ExternalID: "7", Title: "Как стать логопедом", Slug: "kak-stat-logopedom"},
			CompetitorStructure: "структура конкурента",
			WordstatKeywords:    []article.KeywordFrequency{{Query: "логопед", Frequency: 100}},
			LSIWords:            []string{"дефектолог"},
			Links:               "https://example.test/logoped",
		},
	}
	chats := &fakeChats{answers: map[string]string{
		StageInfo: "TLDR: коротко\nFAQ:\nВопрос: Где?\nОтвет: Тут.",
		StageHTML: htmlWithLink,
	}}
	renderer := &fakeRenderer{}
	publisher := &recordingPublisher{}
	return flowFixture{
		flow:       NewFlow(repository, writer, chats, renderer, nil, publisher, nil),
		chats:      chats,
		repository: repository,
		renderer:   renderer,
		publisher:  publisher,
		writer:     writer,
	}
}

// htmlWithLink — разметка с обязательной ссылкой перелинковки из входных данных фикстуры.
const htmlWithLink = `<h2>Заголовок</h2><p>текст со ссылкой на <a href="https://example.test/logoped">логопеда</a></p>`

// Границы чатов — главный контракт потока. Их четыре, и ревью живёт отдельно от статьи:
// в своей же беседе модель правит собственную работу и почти ничего в ней не меняет.
func TestFlowUsesFourChats(t *testing.T) {
	fixture := newFlowFixture(t)
	ctx := context.Background()

	if err := fixture.flow.RunStructure(ctx, "7"); err != nil {
		t.Fatalf("structure: %v", err)
	}
	if err := fixture.flow.RunArticle(ctx, "7"); err != nil {
		t.Fatalf("article: %v", err)
	}
	if err := fixture.flow.RunHTML(ctx, "7"); err != nil {
		t.Fatalf("html: %v", err)
	}

	want := [][]string{
		{StageStructure},
		{StageExpertSEO},
		{StageReview, StageInfo},
		{StageHTML},
	}
	if len(fixture.chats.chats) != len(want) {
		t.Fatalf("открыто чатов: %d, ожидалось %d — %v", len(fixture.chats.chats), len(want), fixture.chats.chats)
	}
	for index, expected := range want {
		if strings.Join(fixture.chats.chats[index], ",") != strings.Join(expected, ",") {
			t.Fatalf("чат %d: %v, ожидалось %v", index+1, fixture.chats.chats[index], expected)
		}
	}
}

// Чат ревью начинается с чистой истории, поэтому статью он обязан получить текстом: иначе
// модель правит пустоту, а поток этого не замечает.
func TestReviewChatReceivesArticleAsText(t *testing.T) {
	fixture := newFlowFixture(t)
	ctx := context.Background()
	if err := fixture.flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}

	data, ok := fixture.renderer.data[StageReview].(struct{ Article string })
	if !ok {
		t.Fatalf("стадия review получила данные другого вида: %#v", fixture.renderer.data[StageReview])
	}
	if !strings.Contains(data.Article, StageExpertSEO) {
		t.Fatalf("в ревью ушла не написанная статья: %q", data.Article)
	}
}

// Базовый промпт статьи собирается и уходит в выгрузку, но модель его не получает: текст
// пишет стадия expert_seo по эталону.
func TestBaseArticlePromptIsPublishedButNeverSent(t *testing.T) {
	fixture := newFlowFixture(t)
	ctx := context.Background()
	if err := fixture.flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}

	for _, chat := range fixture.chats.chats {
		for _, stage := range chat {
			if stage == StageArticle {
				t.Fatal("базовый промпт статьи ушёл в модель, а он только артефакт и документ")
			}
		}
	}
	if len(fixture.publisher.jobs) != 1 {
		t.Fatalf("в выгрузку попало %d заданий, ожидалось одно", len(fixture.publisher.jobs))
	}
	if fixture.publisher.jobs[0].Prompt != "промпт "+StageArticle {
		t.Fatalf("опубликован не базовый промпт статьи: %q", fixture.publisher.jobs[0].Prompt)
	}
	if !strings.HasSuffix(fixture.publisher.jobs[0].PromptPath, "article_prompt.txt") {
		t.Fatalf("промпт сохранён не в артефакт статьи: %q", fixture.publisher.jobs[0].PromptPath)
	}
}

// Упавшее ревью не стоит переписанной статьи: черновик уже сохранён, и повторный запуск
// начинает с чата ревью.
func TestRunAfterFailedReviewStartsFromReview(t *testing.T) {
	fixture := newFlowFixture(t)
	ctx := context.Background()
	if err := fixture.flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	fixture.chats.failures = map[string]error{StageReview: errors.New("провайдер занят")}
	if err := fixture.flow.RunArticle(ctx, "7"); err == nil {
		t.Fatal("упавшее ревью не вернуло ошибку")
	}
	if strings.TrimSpace(fixture.repository.saved.ArticlePath) == "" {
		t.Fatal("черновик статьи не сохранён, повторный прогон будет писать её заново")
	}

	fixture.chats.failures = nil
	before := len(fixture.chats.chats)
	if err := fixture.flow.RunArticle(ctx, "7"); err != nil {
		t.Fatalf("повторный прогон: %v", err)
	}
	for _, chat := range fixture.chats.chats[before:] {
		for _, stage := range chat {
			if stage == StageExpertSEO {
				t.Fatal("повторный прогон переписал статью заново вместо того, чтобы её отредактировать")
			}
		}
	}
	if strings.TrimSpace(fixture.repository.finalArticlePath) == "" {
		t.Fatal("после повторного прогона нет финального текста")
	}
}

// Оборванный ответ редактуры выглядит законченной статьёй. Признак один — он заметно короче
// исходного текста или потерял разделы; в этом случае в блог уходит черновик, а не половина.
func TestTruncatedReviewKeepsDraft(t *testing.T) {
	fixture := newFlowFixture(t)
	ctx := context.Background()
	fixture.chats.answers[StageExpertSEO] = "H2 - Первый\nдлинный текст статьи про логопеда\nH2 - Второй\nещё абзац текста"
	fixture.chats.answers[StageReview] = "H2 - Первый\nобрыв"
	if err := fixture.flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}

	final, err := fixture.writer.Read(fixture.repository.finalArticlePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(final, "H2 - Второй") {
		t.Fatalf("в финальный текст ушёл оборванный ответ редактуры: %q", final)
	}
}

// Правленый текст ложится в оба слота: списка замечаний ревью не отдаёт, третьего текста
// статьи у задачи нет, а пустой слот раннер считает невыполненным этапом.
func TestReviewFillsBothArtifactSlots(t *testing.T) {
	fixture := newFlowFixture(t)
	ctx := context.Background()
	if err := fixture.flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}

	if fixture.repository.reviewPath != fixture.repository.finalArticlePath {
		t.Fatalf("слоты разошлись: review=%q fixed=%q", fixture.repository.reviewPath, fixture.repository.finalArticlePath)
	}
	if strings.TrimSpace(fixture.repository.savedInfo) == "" {
		t.Fatal("метаданные не сохранены")
	}
	for name, path := range map[string]string{
		"черновик": fixture.repository.saved.ArticlePath,
		"финал":    fixture.repository.finalArticlePath,
	} {
		if _, err := fixture.writer.Read(path); err != nil {
			t.Fatalf("артефакт %s не читается: %v", name, err)
		}
	}
}

// Разметка идёт от текста после ревью, а не от черновика.
func TestHTMLUsesReviewedArticle(t *testing.T) {
	fixture := newFlowFixture(t)
	ctx := context.Background()
	if err := fixture.flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	reviewed, err := fixture.writer.Read(fixture.repository.finalArticlePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reviewed, StageReview) {
		t.Fatalf("финальный текст собран не стадией review: %q", reviewed)
	}
	if err := fixture.flow.RunHTML(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(fixture.repository.htmlPath) == "" {
		t.Fatal("HTML не сохранён")
	}
}

// Без структуры статья не пишется: писать не по чему, и до модели дело доходить не должно.
func TestArticleRequiresStructure(t *testing.T) {
	fixture := newFlowFixture(t)
	if err := fixture.flow.RunArticle(context.Background(), "7"); err == nil {
		t.Fatal("статья без структуры не вернула ошибку")
	}
	if len(fixture.chats.chats) != 0 {
		t.Fatalf("без структуры открыты чаты: %v", fixture.chats.chats)
	}
}
