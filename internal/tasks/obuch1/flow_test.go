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
	"github.com/foxylis237/seo-pipeline/internal/tasks"
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

// ctaAnswer — ответ модели на доспрос строк карточки, в том виде, в каком он приходит: с
// вступлением и нумерацией, которые разбор обязан пережить.
const ctaAnswer = `Вот они:
1. бейдж ;; Профстандарты образования • ЕТКС Выпуск 1
2. заголовок ;; Профессиональное обучение и аттестация логопедов
описание ;; Учим работе с речевыми нарушениями и выдаём свидетельство установленного образца.
квалификация ;; Свидетельство о профессии рабочего
кнопка ;; Выбрать программу`

// writeTestCTACard кладёт шаблон карточки во временный файл и возвращает путь.
//
// Плейсхолдеры — те же, что в боевом файле: разойдутся имена полей структуры и шаблона —
// шаблон молча подставит пустую строку, и тест обязан это поймать.
func writeTestCTACard(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cta_card.html")
	card := `<div class="sp-cta-card"><span>{{.Badge}}</span><h3>{{.Heading}}</h3>` +
		`<p>{{.Intro}}</p><div>{{.Qualification}}</div>` +
		`<a href="{{.ButtonURL}}" class="sp-cta-button">{{.ButtonText}}</a></div>`
	if err := os.WriteFile(path, []byte(card), 0o600); err != nil {
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
			// Адрес курса приходит книгой импорта: кнопку призыва ставит код по нему, а не
			// по ответу модели.
			CourseURL: "https://example.test/logoped",
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
	flow.ctaCardPath = writeTestCTACard(t)
	// Каталог шаблонов блоков подменяется боевым по абсолютному пути. Без этого тесты шли
	// мимо оформления молча: путь в потоке относителен корня репозитория, а go test работает
	// в каталоге пакета — чтение падало, decorate писал предупреждение и возвращал разметку
	// как есть, и сквозная связка «чистка → оформление → карточка» не проверялась вовсе.
	flow.blockTemplatesDir = filepath.Join(projectRoot, filepath.FromSlash(tasks.CommonBlockTemplatesDir))
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

// Карточка стоит последним элементом статьи, сразу за абзацем раздела с призывом, и собрана
// из ответа модели, а не из умолчаний.
func TestHTMLEndsWithCTACard(t *testing.T) {
	flow, _, repository, _ := newFlowFixture(t)

	runToHTML(t, flow, repository)

	html := readArtifact(t, flow, repository.htmlPath)
	if !strings.HasSuffix(strings.TrimSpace(html), "</div>") || !strings.Contains(html, ctaCardMarker) {
		t.Fatalf("карточка призыва не дописана в конец разметки:\n%s", html)
	}
	if index := strings.Index(html, ctaCardMarker); index < strings.Index(html, "<h2") {
		t.Fatalf("карточка стоит выше текста статьи:\n%s", html)
	}
	for _, want := range []string{"Профессиональное обучение и аттестация логопедов", "Свидетельство о профессии рабочего"} {
		if !strings.Contains(html, want) {
			t.Fatalf("в карточке нет строки модели %q:\n%s", want, html)
		}
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
	defaults := defaultCTACard()
	if !strings.Contains(html, defaults.ButtonText) {
		t.Fatalf("кнопка не собралась на умолчаниях:\n%s", html)
	}
	// Адрес умолчанием не заменяется: он пришёл книгой и к ответу модели отношения не имеет.
	if !strings.Contains(html, `href="https://example.test/logoped"`) {
		t.Fatalf("кнопка потеряла адрес курса из книги:\n%s", html)
	}
}

// Отсутствующий шаблон роняет стадию ДО первого сообщения модели: отказ файла обязан стоить
// нисколько, а не оплаченный ответ разметки.
func TestHTMLFailsBeforeModelWithoutCTAFile(t *testing.T) {
	flow, chats, repository, _ := newFlowFixture(t)
	flow.ctaCardPath = filepath.Join(t.TempDir(), "нет-такого-файла.html")

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
	empty := filepath.Join(t.TempDir(), "cta_card.html")
	if err := os.WriteFile(empty, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	flow.ctaCardPath = empty

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

// Вёрстка модели снимается чисткой, и только после неё код ставит своё оформление. Порядок
// здесь и проверяется: от обёртки и стилей, которые прислала модель, в теле не остаётся
// ничего, а инлайновые стили таблицы на месте — их поставил decorate уже после чистки.
// Поставь оформление раньше — чистка сняла бы и его, и таблица ушла бы в блог голой.
//
// Строгая проверка самой чистки живёт у CleanPlainBlogMarkup в internal/pipeline/generation:
// здесь её повторять нельзя, иначе тест запрещает ровно то, ради чего блоки и заведены.
func TestHTMLIsCleanedForPlainBlog(t *testing.T) {
	flow, chats, repository, _ := newFlowFixture(t)
	chats.queue[StageHTML] = []string{
		`<div class="ds-scroll-area"><table style="width:100%;"><tbody><tr><td>a</td><td>b</td></tr></tbody></table></div>` + htmlWithLink,
		ctaAnswer,
	}

	runToHTML(t, flow, repository)

	html := readArtifact(t, flow, repository.htmlPath)
	body, _, _ := strings.Cut(html, `<div class="`+ctaCardMarker+`"`)
	for _, own := range []string{"ds-scroll-area", `style="width:100%;"`} {
		if strings.Contains(body, own) {
			t.Fatalf("в теле статьи осталась вёрстка модели (%s):\n%s", own, body)
		}
	}
	if !strings.Contains(body, `<figure class="wp-block-table">`) {
		t.Fatalf("таблица не завёрнута в figure:\n%s", body)
	}
	if !strings.Contains(body, "overflow-x:auto") {
		t.Fatalf("таблица не оформлена кодом после чистки:\n%s", body)
	}
}

// Набор полей карточки и набор плейсхолдеров боевого шаблона обязаны совпадать: расхождение
// даёт пустую строку в опубликованной статье, а не ошибку. Классы проверяются там же: по ним
// код узнаёт призыв, нарисованный моделью, и не ставит второй.
func TestLiveCTATemplateMatchesCardFields(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(CTACardPath)))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"Badge", "Heading", "Intro", "Qualification", "ButtonURL", "ButtonText"} {
		if !strings.Contains(string(raw), "{{."+field+"}}") {
			t.Fatalf("в шаблоне карточки нет плейсхолдера {{.%s}}", field)
		}
	}
	for _, marker := range []string{ctaCardMarker, ctaButtonMarker} {
		if !strings.Contains(string(raw), marker) {
			t.Fatalf("в шаблоне карточки нет класса %s", marker)
		}
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

// Адрес из книги, которого нет в каталоге, роняет статью ДО первого сообщения модели:
// опечатка в колонке иначе уводит читателя ссылкой в никуда, а отказ здесь стоит нисколько.
func TestCourseURLOutsideCatalogFailsBeforeModel(t *testing.T) {
	flow, chats, _, _ := newFlowFixture(t)
	programs := &stubPrograms{known: map[string]bool{}}
	flow.UseProgramCatalog(programs)

	err := flow.RunStructure(context.Background(), "7")

	if err == nil {
		t.Fatal("статья с адресом вне каталога прошла стадию structure")
	}
	if !strings.Contains(err.Error(), "каталог") {
		t.Fatalf("ошибка не называет причину: %v", err)
	}
	if len(chats.chats) != 0 {
		t.Fatalf("открыто %d чатов, ожидался ноль: отказ обязан быть до модели", len(chats.chats))
	}
	if len(programs.asked) != 1 {
		t.Fatalf("каталог спрошен %d раз", len(programs.asked))
	}
}

// Незаполненная колонка книги роняет статью там же и по той же причине: адрес кнопки
// называет человек, и подставлять вместо него раздел значит молча менять решение.
func TestEmptyCourseURLFailsBeforeModel(t *testing.T) {
	flow, chats, repository, _ := newFlowFixture(t)
	repository.input.CourseURL = ""

	err := flow.RunStructure(context.Background(), "7")

	if err == nil {
		t.Fatal("статья без адреса курса прошла стадию structure")
	}
	if !strings.Contains(err.Error(), "course_url") {
		t.Fatalf("ошибка не называет колонку: %v", err)
	}
	if len(chats.chats) != 0 {
		t.Fatalf("открыто %d чатов, ожидался ноль: отказ обязан быть до модели", len(chats.chats))
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
