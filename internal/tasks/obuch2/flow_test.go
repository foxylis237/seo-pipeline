package obuch2

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
	// queued — ответы стадии по порядку сообщений. Нужны там, где сообщения одной стадии
	// отвечают по-разному: разметка, её продолжение и доспрос надписи кнопки.
	queued map[string][]string
	// failAfter — номер сообщения, начиная с которого чат отказывает. Ноль означает «не
	// отказывать»: так проверяется, что отказ доспроса стадию не роняет.
	failAfter int
	sent      int
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
	c.owner.sent++
	c.owner.chats[c.index] = append(c.owner.chats[c.index], stage)
	if c.owner.failAfter > 0 && c.owner.sent >= c.owner.failAfter {
		return "", errors.New("провайдер отказал")
	}
	if queue := c.owner.queued[stage]; len(queue) > 0 {
		c.owner.queued[stage] = queue[1:]
		return queue[0], nil
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
	input            article.GenerationInput
	saved            article.SavedGenerationInput
	reviewPath       string
	finalArticlePath string
	htmlPath         string
	stages           []string
	failures         []error
}

func (r *fakeRepository) GetGenerationInput(context.Context, string) (article.GenerationInput, error) {
	return r.input, nil
}

func (r *fakeRepository) GetSavedGenerationInput(context.Context, string) (article.SavedGenerationInput, error) {
	return r.saved, nil
}

func (r *fakeRepository) BeginGeneration(context.Context, int64) error { return nil }

// BeginGenerationStage повторяет контракт репозитория: он принимает имена этапов, а не
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

// В слоте review у obuch_2 лежит та же страница, что и в слоте финального текста: ревью
// возвращает готовый текст, а не список замечаний, и второго файла у него нет.
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

func (r *fakeRepository) SaveError(_ context.Context, _ int64, processingErr error) error {
	r.failures = append(r.failures, processingErr)
	return nil
}

type recordingPublisher struct{ jobs []generation.ArticlePromptJob }

func (p *recordingPublisher) PublishArticlePrompt(job generation.ArticlePromptJob) {
	p.jobs = append(p.jobs, job)
}

// testPage — страница, какой её отдаёт редактура: лид, строка плашки, разделы и призыв.
// От неё зависят сразу три проверки — покрытие разметки, восстановление плашки и место
// кнопки, — поэтому текст один на все тесты.
// Вводные абзацы намеренно короче строки плашки: так проверяется, что восстановление лида
// не принимает её за первый абзац страницы и не возвращает инструкцию вёрстке текстом.
const testPage = "H1 - Обучение на стропальщика\n\n" +
	"Стропальщик крепит и перемещает грузы кранами.\n\n" +
	"Обучение занимает несколько недель и заканчивается удостоверением.\n\n" +
	"ПЛАШКИ: 150 часов, от 2 недель | Срок ;; от 5 000 ₽ | Цена ;; Министерства образования РФ | Лицензия\n\n" +
	"H2 - Кому подойдёт эта программа\n\n" +
	"Программа подойдёт рабочим складов и строительных площадок без действующего удостоверения.\n\n" +
	"H2 - Запишитесь на обучение стропальщиков\n\n" +
	"Оставьте заявку, и куратор подберёт удобный график занятий по программе."

// testMarkup — размеченная страница, покрывающая текст целиком.
const testMarkup = `<p class="wp-block-paragraph">Стропальщик крепит и перемещает грузы кранами.</p>` +
	`<p class="wp-block-paragraph">Обучение занимает несколько недель и заканчивается удостоверением.</p>` +
	`<h2 class="wp-block-heading">Кому подойдёт эта программа</h2>` +
	`<p class="wp-block-paragraph">Программа подойдёт рабочим складов и строительных площадок без действующего удостоверения.</p>` +
	`<h2 class="wp-block-heading">Запишитесь на обучение стропальщиков</h2>` +
	`<p class="wp-block-paragraph">Оставьте заявку, и куратор подберёт удобный график занятий по программе.</p>`

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
			Profession:          "Стропальщик",
			Hours:               "150 академических часов",
			Duration:            "от 2 недель",
			Price:               "от 5 000 ₽",
			Document:            "свидетельство о профессии рабочего",
			Attestation:         "тестирование",
		},
	}
	chats := &fakeChats{answers: map[string]string{
		StageArticle: testPage,
		StageReview:  testPage,
		StageHTML:    testMarkup,
	}}
	publisher := &recordingPublisher{}
	flow := NewFlow(repository, writer, chats, &fakeRenderer{}, nil, publisher)
	// Карточка лежит в каталоге задачи, а тест пакета запускается в своём: путь подменяется
	// временным файлом с той же ролью — иначе стадия html отказала бы у всех тестов сразу.
	flow.ctaCardPath = writeTestCTACard(t)
	// Каталог шаблонов блоков — боевой, по абсолютному пути: оформление проверяется на тех же
	// файлах, что уйдут в блог, а относительный путь из потока от каталога пакета не ведёт
	// никуда, и оформление молча не срабатывало бы.
	flow.blockTemplatesDir = filepath.Join(projectRoot, filepath.FromSlash(tasks.CommonBlockTemplatesDir))
	return flow, chats, repository, publisher, writer
}

// testCTACard — кнопка тестов. От боевой ей нужны только узнаваемые классы и подстановка
// надписи: правила вставки от остальной вёрстки не зависят.
const testCTACard = `<div style="margin-top: 25px;">` +
	`<button class="service btn-primary feedback__event" data-popup="popup-contact">{{.ButtonText}}</button></div>`

// ctaAnswer — ответ модели на доспрос строк карточки, в том виде, в каком он приходит: с
// вступлением и нумерацией, которые разбор обязан пережить.
const ctaAnswer = `Вот они:
1. бейдж ;; Профстандарты и ЕТКС, выпуск 1
2. заголовок ;; Обучение и аттестация стропальщиков
описание ;; Готовим к работе с грузоподъёмными механизмами и выдаём документ установленного образца.
квалификация ;; Свидетельство стропальщика
кнопка ;; Обучение для стропальщика`

func writeTestCTACard(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cta_card.html")
	if err := os.WriteFile(path, []byte(testCTACard+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runToHTML прогоняет все три чата и отдаёт получившуюся разметку.
func runToHTML(t *testing.T, flow *Flow, repository *fakeRepository, writer *articleoutput.Writer) (string, error) {
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
		return "", err
	}
	markup, err := writer.Read(repository.htmlPath)
	if err != nil {
		t.Fatal(err)
	}
	return markup, nil
}

// Границы чатов — главный контракт потока: три чата, и сообщения распределены по ним так,
// как описан порядок стадий obuch_2. Четвёртое сообщение третьего чата — доспрос надписи
// кнопки: перелинковки у страницы услуги нет, и доспрашивать ссылки, как у obuch_1, нечего.
func TestFlowUsesThreeChats(t *testing.T) {
	flow, chats, repository, _, writer := newFlowFixture(t)

	if _, err := runToHTML(t, flow, repository, writer); err != nil {
		t.Fatalf("html: %v", err)
	}

	if len(chats.chats) != 3 {
		t.Fatalf("открыто чатов: %d, ожидалось 3 — %v", len(chats.chats), chats.chats)
	}
	want := [][]string{
		{StageStructure},
		{StageArticle, StageReview},
		// Первое сообщение разметки прошло с первого раза, продолжения не понадобились;
		// второе — доспрос надписи кнопки.
		{StageHTML, StageHTML},
	}
	for index, expected := range want {
		if strings.Join(chats.chats[index], ",") != strings.Join(expected, ",") {
			t.Fatalf("чат %d: %v, ожидалось %v", index+1, chats.chats[index], expected)
		}
	}
}

// Стадии info у задачи нет ни в схеме, ни в потоке: поля под частые вопросы у площадки не
// существует, и вопросы остаются в теле страницы. Интерфейс Repository метода SaveArticleInfo
// не объявляет вовсе — это держит компилятор, а тест держит схему стадий.
func TestFlowHasNoMetadataStage(t *testing.T) {
	flow, chats, repository, _, writer := newFlowFixture(t)
	if _, err := runToHTML(t, flow, repository, writer); err != nil {
		t.Fatalf("html: %v", err)
	}
	for _, chat := range chats.chats {
		for _, stage := range chat {
			if stage == "info" {
				t.Fatalf("выполнена стадия %q, которой нет в потоке obuch_2", stage)
			}
		}
	}
	for _, stage := range Stages {
		if stage == "info" {
			t.Fatalf("стадия %q объявлена в схеме obuch_2, хотя поток её не выполняет", stage)
		}
	}
}

// Черновик и отредактированная страница — разные файлы, а слот review_path указывает на
// финальный: списка замечаний ревью не отдаёт, а пустым слот оставлять нельзя — раннер
// полного прогона считал бы этап невыполненным вечно.
func TestFlowSeparatesDraftFromReviewedPage(t *testing.T) {
	flow, _, repository, _, _ := newFlowFixture(t)
	ctx := context.Background()
	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if draft := repository.saved.ArticlePath; !strings.HasSuffix(draft, "generated/article.txt") {
		t.Fatalf("черновик основного промпта сохранён как %q", draft)
	}
	if final := repository.finalArticlePath; !strings.HasSuffix(final, "generated/fixed_article.txt") {
		t.Fatalf("отредактированная страница сохранена как %q", final)
	}
	if repository.reviewPath != repository.finalArticlePath {
		t.Fatalf("слот review_path указывает на %q, а финальный текст лежит в %q",
			repository.reviewPath, repository.finalArticlePath)
	}
}

// Шаблон кнопки читается до первого сообщения модели: отказ файла обязан стоить нисколько, а
// не оплаченный ответ разметки.
func TestFlowReadsCTAButtonBeforeAskingModel(t *testing.T) {
	flow, chats, repository, _, _ := newFlowFixture(t)
	ctx := context.Background()
	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	repository.saved.FixedArticlePath = repository.finalArticlePath
	flow.ctaCardPath = filepath.Join(t.TempDir(), "no-such-card.html")

	opened := len(chats.chats)
	if err := flow.RunHTML(ctx, "7"); err == nil {
		t.Fatal("стадия html прошла без шаблона карточки")
	}
	if len(chats.chats) != opened {
		t.Fatalf("чат разметки открыт при нечитаемом шаблоне карточки: %v", chats.chats)
	}
	if len(repository.stages) != 1 || repository.stages[0] != "article" {
		t.Fatalf("этап html начат до чтения шаблона карточки: %v", repository.stages)
	}
}

// Карточку ставит код, а пять строк внутри неё даёт модель. Ответ приходит с вступлением и
// нумерацией — разбор обязан их пережить.
func TestFlowAppendsCTAButtonWithModelText(t *testing.T) {
	flow, chats, repository, _, writer := newFlowFixture(t)
	chats.queued = map[string][]string{
		StageHTML: {testMarkup, ctaAnswer},
	}
	markup, err := runToHTML(t, flow, repository, writer)
	if err != nil {
		t.Fatalf("html: %v", err)
	}
	if !strings.Contains(markup, "Обучение для стропальщика</button>") {
		t.Fatalf("надпись кнопки от модели не попала в разметку: %q", markup)
	}
	if strings.Count(markup, ctaCardMarker) != 1 {
		t.Fatalf("карточек в разметке не одна: %q", markup)
	}
}

// Отказ доспроса стадию не роняет — карточка собирается на умолчаниях: за разметку уже
// заплачено, и страница с типовой карточкой полезнее отказа.
func TestFlowKeepsPageWhenCTAAskFails(t *testing.T) {
	flow, chats, repository, _, writer := newFlowFixture(t)
	// Пятое сообщение прогона — доспрос карточки: structure, article, review, html, доспрос.
	chats.failAfter = 5
	markup, err := runToHTML(t, flow, repository, writer)
	if err != nil {
		t.Fatalf("отказ доспроса уронил стадию: %v", err)
	}
	fallback := defaultCTACard()
	if !strings.Contains(markup, fallback.ButtonText) {
		t.Fatalf("кнопка не собралась на умолчании: %q", markup)
	}
}

// Названная моделью надпись берётся, неназванная заменяется умолчанием: страница без кнопки
// заканчивается абзацем, за которым некуда нажать.
func TestFlowUsesModelButtonText(t *testing.T) {
	flow, chats, repository, _, writer := newFlowFixture(t)
	chats.queued = map[string][]string{
		StageHTML: {testMarkup, "кнопка ;; Обучение для стропальщика"},
	}
	markup, err := runToHTML(t, flow, repository, writer)
	if err != nil {
		t.Fatalf("html: %v", err)
	}
	if !strings.Contains(markup, "Обучение для стропальщика</button>") {
		t.Fatalf("названная моделью надпись потеряна: %q", markup)
	}
	// Вёрстка кнопки — из шаблона, а не от модели: за эти классы цепляется форма площадки.
	for _, want := range []string{"feedback__event", "btn-primary", `data-popup="popup-contact"`} {
		if !strings.Contains(markup, want) {
			t.Fatalf("в кнопке нет %q: %q", want, markup)
		}
	}
}

// Второй призыв код не дописывает: промпт карточку и тег button запрещает, но если модель всё
// же нарисовала свой, двух подряд на странице быть не должно.
func TestFlowKeepsModelCTAInsteadOfSecond(t *testing.T) {
	flow, chats, repository, _, writer := newFlowFixture(t)
	chats.answers[StageHTML] = testMarkup + `<button class="service">Своя кнопка</button>`
	markup, err := runToHTML(t, flow, repository, writer)
	if err != nil {
		t.Fatalf("html: %v", err)
	}
	if strings.Count(markup, "<button") != 1 {
		t.Fatalf("в разметке две кнопки подряд: %q", markup)
	}
	if !strings.Contains(markup, "Своя кнопка") {
		t.Fatalf("кнопка модели потеряна: %q", markup)
	}
	if strings.Contains(markup, ctaCardMarker) {
		t.Fatalf("код дописал свою карточку поверх призыва модели: %q", markup)
	}
}

// Оборванная разметка роняет стадию: AcceptIncomplete у задачи не включён. У коммерческой
// страницы половина текста в блоге хуже отказа — читатель видит обрыв на середине, а цена
// стоит в плашке над ним.
func TestFlowFailsOnTruncatedMarkup(t *testing.T) {
	flow, chats, repository, _, writer := newFlowFixture(t)
	truncated := `<p class="wp-block-paragraph">Стропальщик крепит и перемещает грузы кранами.</p>` +
		`<h2 class="wp-block-heading">Кому подойдёт эта программа</h2>`
	// Первое сообщение и оба продолжения возвращают одно и то же: страница так и не покрыта.
	chats.queued = map[string][]string{StageHTML: {truncated, truncated, truncated}}
	if _, err := runToHTML(t, flow, repository, writer); err == nil {
		t.Fatal("оборванная разметка сохранена как готовая")
	} else if !errors.Is(err, generation.ErrHTMLIncomplete) {
		t.Fatalf("обрыв разметки не опознан как обрыв: %v", err)
	}
	if repository.htmlPath != "" {
		t.Fatalf("путь к оборванной разметке записан в состояние: %q", repository.htmlPath)
	}
}

// Плашку параметров возвращает код, если её потеряла вёрстка: значения известны целиком, а
// просить страницу заново — значит упереться в тот же предел длины ответа.
//
// Таблица у неё одноколоночная, без шапки, подпись впереди жирной — так свёрстаны живые
// страницы площадки. Список, как у статей блога той же площадки, здесь был бы чужой вёрсткой.
func TestFlowRestoresParametersTable(t *testing.T) {
	flow, _, repository, _, writer := newFlowFixture(t)
	markup, err := runToHTML(t, flow, repository, writer)
	if err != nil {
		t.Fatalf("html: %v", err)
	}
	if !strings.Contains(markup, `<figure class="wp-block-table">`) {
		t.Fatalf("плашка параметров не восстановлена таблицей: %q", markup)
	}
	if strings.Contains(markup, "<thead") {
		t.Fatalf("у плашки появилась шапка: %q", markup)
	}
	if !strings.Contains(markup, "<strong>Срок:</strong> 150 часов, от 2 недель") {
		t.Fatalf("строка плашки свёрстана не так, как на живых страницах: %q", markup)
	}
	// Шапки у одноколоночной плашки нет и в оформлении: её первая строка — такое же значение,
	// как прочие, и покрашенная шапкой строка «Срок: …» объявила бы заголовком обычную графу.
	if strings.Contains(markup, "border-bottom:2px solid #ff7500") {
		t.Fatalf("у одноколоночной плашки появилась шапка: %q", markup)
	}
	// Плашка стоит после лида и перед первым заголовком.
	table := strings.Index(markup, `<figure class="wp-block-table">`)
	heading := strings.Index(markup, "<h2")
	if table < 0 || heading < 0 || table > heading {
		t.Fatalf("плашка стоит не между лидом и первым заголовком: %q", markup)
	}
	if strings.Contains(markup, "ПЛАШКИ:") {
		t.Fatalf("строка-маркер осталась в разметке: %q", markup)
	}
}

// Вёрстка модели снимается кодом, а не промптом, и только после чистки код ставит своё
// оформление. Порядок здесь и проверяется: от обёрток и стилей модели не остаётся ничего, а
// инлайновые стили таблицы на месте — их поставил decorate уже после чистки. Поставь
// оформление раньше — чистка сняла бы и его, и таблица ушла бы в блог голой.
//
// Строгая проверка самой чистки живёт у CleanPlainBlogMarkup в internal/pipeline/generation:
// здесь её повторять нельзя, иначе тест запрещает ровно то, ради чего блоки и заведены.
func TestFlowCleansMarkupToThemeClasses(t *testing.T) {
	flow, chats, repository, _, writer := newFlowFixture(t)
	chats.answers[StageHTML] = `<div class="ds-markdown"><h1>Лишний заголовок</h1>` + testMarkup +
		`<p style="color:red">хвост</p></div>`
	markup, err := runToHTML(t, flow, repository, writer)
	if err != nil {
		t.Fatalf("html: %v", err)
	}
	// Тело — всё, что до карточки призыва: её инлайновые стили законны, их ставит код.
	body, _, _ := strings.Cut(markup, `<div class="`+ctaCardMarker+`"`)
	for _, own := range []string{"ds-markdown", `style="color:red"`, "<h1"} {
		if strings.Contains(body, own) {
			t.Fatalf("в разметке осталась вёрстка модели (%s): %q", own, body)
		}
	}
	if !strings.Contains(body, "overflow-x:auto") {
		t.Fatalf("таблица не оформлена кодом после чистки: %q", body)
	}
}

// Оформление блоков у задачи общее с obuch_1 и живёт в движке. На странице услуги оно
// достаёт три вещи: таблицу заработка, пары «вопрос — ответ» и строку источника. Промпты
// задачи меток sp-note, sp-stats и sp-steps не просят, и это сознательно — на такой разметке
// DecorateBlocks ничего не меняет.
func TestFlowDecoratesSalaryTableAndFAQ(t *testing.T) {
	flow, chats, repository, _, writer := newFlowFixture(t)
	chats.answers[StageHTML] = testMarkup +
		`<h2 class="wp-block-heading">Уровень дохода и карьерный трек</h2>` +
		`<figure class="wp-block-table"><table class="has-fixed-layout"><thead>` +
		`<tr><td><strong>Опыт</strong></td><td><strong>Средний доход (руб.)</strong></td><td><strong>Где работает</strong></td></tr>` +
		`</thead><tbody>` +
		`<tr><td>До года</td><td>45 000 — 60 000</td><td>Складская площадка</td></tr>` +
		`</tbody></table></figure>` +
		`<p class="wp-block-paragraph">Источник: hh.ru, выборка за 2025 год.</p>` +
		`<h2 class="wp-block-heading">Частые вопросы об обучении стропальщиков</h2>` +
		`<h3 class="wp-block-heading">Нужно ли профильное образование?</h3>` +
		`<p class="wp-block-paragraph">Нет, достаточно основного общего образования.</p>`

	markup, err := runToHTML(t, flow, repository, writer)
	if err != nil {
		t.Fatalf("html: %v", err)
	}
	// Таблица заработка многоколоночная, и шапка у неё покрашена — в отличие от плашки.
	if !strings.Contains(markup, "border-bottom:2px solid #ff7500") {
		t.Fatalf("шапка таблицы заработка не оформлена: %q", markup)
	}
	if !strings.Contains(markup, "Складская площадка") {
		t.Fatalf("третья колонка таблицы заработка потеряна: %q", markup)
	}
	// Строка источника получает свою отметку, а не остаётся обычным абзацем.
	if !strings.Contains(markup, "border-radius:50%;background:#ff7500") {
		t.Fatalf("строка источника не оформлена: %q", markup)
	}
	// Пара «вопрос — ответ» собрана, а заголовок остался h3: его читают поиск и оглавление.
	if !strings.Contains(markup, "border-left:2px solid #ffd9b8") {
		t.Fatalf("пары вопросов не оформлены: %q", markup)
	}
	if !strings.Contains(markup, "Нужно ли профильное образование?") {
		t.Fatalf("вопрос потерян: %q", markup)
	}
}

// Основной промпт уходит и в модель, и в очередь публикации: копия одна, расходиться ей негде.
func TestFlowPublishesArticlePrompt(t *testing.T) {
	flow, _, repository, publisher, writer := newFlowFixture(t)
	if _, err := runToHTML(t, flow, repository, writer); err != nil {
		t.Fatalf("html: %v", err)
	}
	if len(publisher.jobs) != 1 {
		t.Fatalf("промпт страницы в очередь публикации не попал: %d заданий", len(publisher.jobs))
	}
	if !strings.HasSuffix(publisher.jobs[0].PromptPath, "prompts/article_prompt.txt") {
		t.Fatalf("промпт сохранён как %q", publisher.jobs[0].PromptPath)
	}
}
