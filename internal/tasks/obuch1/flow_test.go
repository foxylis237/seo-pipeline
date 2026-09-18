package obuch1

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	// queue отдаёт ответы стадии по порядку: у стадии html их два — разметка и тексты
	// кнопки призыва.
	queue map[string][]string
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

func (c *fakeChat) Send(_ context.Context, prompt string) (string, error) {
	return c.record(prompt)
}

func (c *fakeChat) Continue(_ context.Context, prompt string) (string, error) {
	return c.record(prompt)
}

// record повторяет ограничение боевого чата: сообщений принимается ровно столько, сколько
// стадий названо при создании. Иначе тест пропустил бы в прод чат, которому не хватает
// сообщения на кнопку.
func (c *fakeChat) record(prompt string) (string, error) {
	if c.sent >= len(c.stages) {
		return "", fmt.Errorf("чату не хватило стадий: сообщение %d при %d стадиях", c.sent+1, len(c.stages))
	}
	stage := c.stages[c.sent]
	c.sent++
	c.owner.chats[c.index] = append(c.owner.chats[c.index], stage)
	if pending := c.owner.queue[stage]; len(pending) > 0 {
		c.owner.queue[stage] = pending[1:]
		return pending[0], nil
	}
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
// значения current_step.
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

// Слот называется review, но лежит в нём статья после редактуры — финальный текст.
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

// htmlWithLink — разметка с обязательной ссылкой перелинковки из входных данных фикстуры.
const htmlWithLink = `<h2 class="wp-block-heading">Заголовок</h2>` +
	`<p class="wp-block-paragraph">текст со ссылкой на <a href="https://example.test/logoped">логопеда</a></p>`

// ctaAnswer — ответ модели на доспрос надписи и адреса кнопки, в том виде, в каком он
// приходит: с вступлением и нумерацией, которые разбор обязан пережить.
const ctaAnswer = `Вот они:
1. кнопка ;; Выбрать программу
адрес ;; https://example.test/logoped`

// writeTestCTAButton кладёт шаблон кнопки во временный файл и возвращает путь.
//
// Плейсхолдеры — те же, что в боевом файле: разойдутся имена полей структуры и шаблона —
// шаблон молча подставит пустую строку, и тест обязан это поймать.
func writeTestCTAButton(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cta_button.html")
	button := `<p class="wp-block-paragraph">` +
		`<a href="{{.ButtonURL}}" class="sp-cta-button" style="background:#ff7500;">{{.ButtonText}}</a></p>`
	if err := os.WriteFile(path, []byte(button), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newFlowFixture(t *testing.T) (*Flow, *fakeChats, *fakeRepository, *recordingPublisher) {
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
	chats := &fakeChats{
		answers: map[string]string{
			StageInfo: "TLDR: коротко\nFAQ:\nВопрос: Где?\nОтвет: Тут.",
		},
		queue: map[string][]string{StageHTML: {htmlWithLink, ctaAnswer}},
	}
	publisher := &recordingPublisher{}
	flow := NewFlow(repository, writer, chats, &fakeRenderer{}, nil, publisher, nil)
	flow.ctaButtonPath = writeTestCTAButton(t)
	return flow, chats, repository, publisher
}

// runToHTML проводит статью по всем трём чатам.
func runToHTML(t *testing.T, flow *Flow, repository *fakeRepository) {
	t.Helper()
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
}

// Границы чатов — главный контракт потока. Третий чат принимает два сообщения: разметку и
// надпись и адрес кнопки призыва; бюджет чата обязан их вместить.
func TestFlowUsesThreeChats(t *testing.T) {
	flow, chats, repository, _ := newFlowFixture(t)

	runToHTML(t, flow, repository)

	if len(chats.chats) != 3 {
		t.Fatalf("открыто чатов: %d, ожидалось 3 — %v", len(chats.chats), chats.chats)
	}
	want := [][]string{
		{StageStructure},
		{StageExpert, StageReview, StageInfo},
		{StageHTML, StageHTML},
	}
	for index, expected := range want {
		if strings.Join(chats.chats[index], ",") != strings.Join(expected, ",") {
			t.Fatalf("чат %d: %v, ожидалось %v", index+1, chats.chats[index], expected)
		}
	}
}

// Кнопка стоит последним элементом статьи, сразу за абзацем раздела с призывом, и собрана
// из ответа модели, а не из умолчаний.
func TestHTMLEndsWithCTAButton(t *testing.T) {
	flow, _, repository, _ := newFlowFixture(t)

	runToHTML(t, flow, repository)

	html := readArtifact(t, flow, repository.htmlPath)
	if !strings.HasSuffix(strings.TrimSpace(html), "</p>") || !strings.Contains(html, ctaButtonMarker) {
		t.Fatalf("кнопка призыва не дописана в конец разметки:\n%s", html)
	}
	if index := strings.Index(html, ctaButtonMarker); index < strings.Index(html, "<h2") {
		t.Fatalf("кнопка стоит выше текста статьи:\n%s", html)
	}
	for _, want := range []string{`href="https://example.test/logoped"`, "Выбрать программу"} {
		if !strings.Contains(html, want) {
			t.Fatalf("в кнопке нет ответа модели %q:\n%s", want, html)
		}
	}
}

// Кнопку, нарисованную моделью вопреки промпту, не дублируем: двух подряд быть не должно.
func TestHTMLKeepsSingleCTAButton(t *testing.T) {
	flow, chats, repository, _ := newFlowFixture(t)
	chats.queue[StageHTML] = []string{
		htmlWithLink + `<p class="wp-block-paragraph"><a class="sp-cta-button" href="https://example.test/a">своя кнопка модели</a></p>`,
		ctaAnswer,
	}

	runToHTML(t, flow, repository)

	html := readArtifact(t, flow, repository.htmlPath)
	if got := strings.Count(html, ctaButtonMarker); got != 1 {
		t.Fatalf("кнопок в разметке %d, ожидалась одна:\n%s", got, html)
	}
}

// Недостающие слоты заменяются умолчаниями, а не роняют стадию: за разметку уже заплачено.
func TestHTMLFallsBackToDefaultCTAButton(t *testing.T) {
	flow, chats, repository, _ := newFlowFixture(t)
	chats.queue[StageHTML] = []string{htmlWithLink, "не могу ответить"}

	runToHTML(t, flow, repository)

	html := readArtifact(t, flow, repository.htmlPath)
	defaults := defaultCTAButton(ctaFallbackURL)
	if !strings.Contains(html, defaults.ButtonText) || !strings.Contains(html, ctaFallbackURL) {
		t.Fatalf("кнопка не собралась на умолчаниях:\n%s", html)
	}
}

// Отсутствующий шаблон роняет стадию ДО первого сообщения модели: отказ файла обязан стоить
// нисколько, а не оплаченный ответ разметки.
func TestHTMLFailsBeforeModelWithoutCTAFile(t *testing.T) {
	flow, chats, repository, _ := newFlowFixture(t)
	flow.ctaButtonPath = filepath.Join(t.TempDir(), "нет-такого-файла.html")

	ctx := context.Background()
	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatalf("structure: %v", err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatalf("article: %v", err)
	}
	repository.saved.FixedArticlePath = repository.finalArticlePath
	chatsBefore := len(chats.chats)

	err := flow.RunHTML(ctx, "7")

	if err == nil {
		t.Fatal("стадия html прошла без шаблона кнопки")
	}
	if len(chats.chats) != chatsBefore {
		t.Fatalf("чат разметки открыт при отсутствующем шаблоне: %v", chats.chats)
	}
	if len(repository.stages) != 1 || repository.stages[0] != "article" {
		t.Fatalf("этап html начат до проверки шаблона: %v", repository.stages)
	}
}

// Пустой файл шаблона — тоже отказ: без кнопки раздел призыва обрывается на абзаце.
func TestHTMLFailsOnEmptyCTAFile(t *testing.T) {
	flow, _, repository, _ := newFlowFixture(t)
	empty := filepath.Join(t.TempDir(), "cta_button.html")
	if err := os.WriteFile(empty, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	flow.ctaButtonPath = empty

	ctx := context.Background()
	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatalf("structure: %v", err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatalf("article: %v", err)
	}
	repository.saved.FixedArticlePath = repository.finalArticlePath

	if err := flow.RunHTML(ctx, "7"); err == nil {
		t.Fatal("стадия html прошла с пустым шаблоном кнопки")
	}
}

// Разметка площадки чистится своей чисткой: инлайновых стилей и div в ней не остаётся.
func TestHTMLIsCleanedForPlainBlog(t *testing.T) {
	flow, chats, repository, _ := newFlowFixture(t)
	chats.queue[StageHTML] = []string{
		`<div class="ds-scroll-area"><table style="width:100%;"><tbody><tr><td>a</td><td>b</td></tr></tbody></table></div>` + htmlWithLink,
		ctaAnswer,
	}

	runToHTML(t, flow, repository)

	html := readArtifact(t, flow, repository.htmlPath)
	body, _, _ := strings.Cut(html, `<p class="wp-block-paragraph"><a href=`)
	if strings.Contains(body, "style=") || strings.Contains(body, "<div") {
		t.Fatalf("в теле статьи остались стили или обёртки:\n%s", body)
	}
	if !strings.Contains(body, `<figure class="wp-block-table">`) {
		t.Fatalf("таблица не завёрнута в figure:\n%s", body)
	}
}

// Набор полей кнопки и набор плейсхолдеров боевого шаблона обязаны совпадать: расхождение
// даёт пустую строку в опубликованной статье, а не ошибку. Класс проверяется там же: по нему
// код узнаёт кнопку, нарисованную моделью, и не ставит вторую.
func TestLiveCTATemplateMatchesButtonFields(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(CTAButtonPath)))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"ButtonURL", "ButtonText"} {
		if !strings.Contains(string(raw), "{{."+field+"}}") {
			t.Fatalf("в шаблоне кнопки нет плейсхолдера {{.%s}}", field)
		}
	}
	if !strings.Contains(string(raw), ctaButtonMarker) {
		t.Fatalf("в шаблоне кнопки нет класса %s", ctaButtonMarker)
	}
}

func readArtifact(t *testing.T, flow *Flow, path string) string {
	t.Helper()
	if strings.TrimSpace(path) == "" {
		t.Fatal("путь артефакта пуст")
	}
	content, err := flow.writer.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

// stubPrograms — каталог услуг площадки в тесте: знает ровно перечисленные адреса.
type stubPrograms struct {
	known map[string]bool
	err   error
	asked []string
}

func (s *stubPrograms) HasProgram(_ context.Context, url string) (bool, error) {
	s.asked = append(s.asked, url)
	if s.err != nil {
		return false, s.err
	}
	return s.known[url], nil
}

// Адрес, которого нет в каталоге, в карточку не попадает: ctaURL проверяет только префикс
// http, и правдоподобный выдуманный адрес ушёл бы в опубликованную статью ссылкой в никуда.
func TestCTAURLOutsideCatalogFallsBackToSection(t *testing.T) {
	flow, chats, repository, _ := newFlowFixture(t)
	chats.queue[StageHTML] = []string{htmlWithLink, ctaAnswer}
	programs := &stubPrograms{known: map[string]bool{}}
	flow.UseProgramCatalog(programs)

	runToHTML(t, flow, repository)

	// Смотрим на саму карточку, а не на страницу: тот же адрес стоит ссылкой в тексте, и
	// перелинковку сверка адреса кнопки трогать не должна.
	button := buttonMarkup(t, readArtifact(t, flow, repository.htmlPath))
	if strings.Contains(button, "https://example.test/logoped") {
		t.Fatalf("в кнопку ушёл адрес вне каталога:\n%s", button)
	}
	if !strings.Contains(button, ctaFallbackURL) {
		t.Fatalf("кнопка не увела в раздел рабочих профессий:\n%s", button)
	}
	if len(programs.asked) != 1 {
		t.Fatalf("каталог спрошен %d раз", len(programs.asked))
	}
}

// Адрес из каталога остаётся как есть: сверка нужна против выдумки, а не против модели.
func TestCTAURLFromCatalogIsKept(t *testing.T) {
	flow, chats, repository, _ := newFlowFixture(t)
	chats.queue[StageHTML] = []string{htmlWithLink, ctaAnswer}
	flow.UseProgramCatalog(&stubPrograms{known: map[string]bool{"https://example.test/logoped": true}})

	runToHTML(t, flow, repository)

	if button := buttonMarkup(t, readArtifact(t, flow, repository.htmlPath)); !strings.Contains(button, "https://example.test/logoped") {
		t.Fatalf("адрес из каталога заменён разделом:\n%s", button)
	}
}

// buttonMarkup вырезает кнопку призыва из готовой страницы.
//
// От начала абзаца, а не от класса: класс стоит после href, и срез по нему потерял бы адрес —
// ровно то, что эти тесты и проверяют.
func buttonMarkup(t *testing.T, html string) string {
	t.Helper()
	at := strings.Index(html, ctaButtonMarker)
	if at < 0 {
		t.Fatalf("в разметке нет кнопки призыва:\n%s", html)
	}
	if start := strings.LastIndex(html[:at], "<p"); start >= 0 {
		at = start
	}
	return html[at:]
}

// Молчащий каталог адрес не трогает: отменять возможно верную ссылку из-за недоступной базы
// хуже, чем оставить её. Прежнее поведение задачи без каталога — то же самое.
func TestCTAURLKeptWhenCatalogUnavailable(t *testing.T) {
	for name, programs := range map[string]ProgramCatalog{
		"каталог отвечает ошибкой": &stubPrograms{err: errors.New("база недоступна")},
		"каталога нет вовсе":       nil,
	} {
		t.Run(name, func(t *testing.T) {
			flow, chats, repository, _ := newFlowFixture(t)
			chats.queue[StageHTML] = []string{htmlWithLink, ctaAnswer}
			if programs != nil {
				flow.UseProgramCatalog(programs)
			}

			runToHTML(t, flow, repository)

			if button := buttonMarkup(t, readArtifact(t, flow, repository.htmlPath)); !strings.Contains(button, "https://example.test/logoped") {
				t.Fatalf("адрес заменён без ответа каталога:\n%s", button)
			}
		})
	}
}
