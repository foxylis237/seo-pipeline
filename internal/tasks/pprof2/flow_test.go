package pprof2

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
	// queued — ответы стадии по порядку сообщений. Нужны там, где два сообщения одной
	// стадии отвечают по-разному: оборвавшаяся разметка и её продолжение.
	queued map[string][]string
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

// В слоте review у pprof_2 лежит та же страница, что и в слоте финального текста: ревью
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
	// Кнопка лежит в каталоге задачи, а тест пакета запускается в своём: путь подменяется
	// временным файлом с той же ролью — иначе стадия html отказала бы у всех тестов сразу.
	flow.ctaButtonPath = writeTestCTAButton(t)
	return flow, chats, repository, publisher, writer
}

// testCTAButton — кнопка тестов. От боевой ей нужен только узнаваемый признак: правила
// вставки от её разметки не зависят.
const testCTAButton = `<button type="button" class="service feedback__event" data-popup="popup-contact">ЗАЯВКА НА ОБУЧЕНИЕ</button>`

func writeTestCTAButton(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cta_button.html")
	if err := os.WriteFile(path, []byte(testCTAButton+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
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
		{StageArticle, StageReview},
		{StageHTML},
	}
	for index, expected := range want {
		if strings.Join(chats.chats[index], ",") != strings.Join(expected, ",") {
			t.Fatalf("чат %d: %v, ожидалось %v", index+1, chats.chats[index], expected)
		}
	}
}

// Отдельного запроса за метаданными у pprof_2 нет: FAQ берётся из уже написанной страницы —
// и именно из той, что вышла из редактуры, а не из черновика.
//
// Это не мелочь оформления: второй запрос к модели дал бы второй набор вопросов, отличный от
// опубликованного, а черновик даёт набор, который редактура успела переписать. Проверяется всё
// сразу — лишних стадий в чатах нет, FAQ в article_metadata попал, и он из финального текста.
func TestFlowTakesFAQFromReviewedPageWithoutExtraStage(t *testing.T) {
	flow, chats, repository, _, _ := newFlowFixture(t)
	chats.answers[StageArticle] = "H1 - Обучение\n\nчерновик\n\nH2 - Частые вопросы\n\n" +
		"H3 - Вопрос черновика?\n\nОтвет черновика."
	chats.answers[StageReview] = "H1 - Обучение\n\nтекст\n\nH2 - Частые вопросы\n\n" +
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
			if stage == "info" {
				t.Fatalf("выполнена стадия %q, которой нет в потоке pprof_2", stage)
			}
		}
	}
	for _, stage := range Stages {
		if stage == "info" {
			t.Fatalf("стадия %q объявлена в схеме pprof_2, хотя поток её не выполняет", stage)
		}
	}
	if !repository.infoSaved {
		t.Fatal("FAQ не сохранён: article_metadata осталась пустой")
	}
	if strings.Contains(repository.info.FAQ, "черновик") {
		t.Fatalf("FAQ разобран из черновика, а не из отредактированной страницы: %q", repository.info.FAQ)
	}
	if !strings.Contains(repository.info.FAQ, "Вопрос: Сколько длится обучение?") ||
		!strings.Contains(repository.info.FAQ, "Ответ: Удостоверение.") {
		t.Fatalf("FAQ разобран не из страницы: %q", repository.info.FAQ)
	}
	if repository.info.TLDR != "" {
		t.Fatalf("у pprof_2 появился TL;DR: %q", repository.info.TLDR)
	}
}

// Черновик и отредактированная страница — разные файлы, а слот review_path указывает на
// финальный: списка замечаний ревью не отдаёт, а пустым слот оставлять нельзя — раннер полного
// прогона считал бы этап невыполненным вечно.
func TestFlowSeparatesDraftFromReviewedPage(t *testing.T) {
	flow, _, repository, _, writer := newFlowFixture(t)
	ctx := context.Background()
	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	draft := repository.saved.ArticlePath
	if !strings.HasSuffix(draft, "generated/article.txt") {
		t.Fatalf("черновик основного промпта сохранён как %q", draft)
	}
	final := repository.finalArticlePath
	if !strings.HasSuffix(final, "generated/fixed_article.txt") {
		t.Fatalf("отредактированная страница сохранена как %q", final)
	}
	if repository.reviewPath != final {
		t.Fatalf("слот ревью %q, ожидался финальный текст %q", repository.reviewPath, final)
	}
	draftText, err := writer.Read(draft)
	if err != nil {
		t.Fatal(err)
	}
	finalText, err := writer.Read(final)
	if err != nil {
		t.Fatal(err)
	}
	if draftText == finalText {
		t.Fatal("черновик и финальный текст совпали: редактура не сохранена отдельным файлом")
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
		"article draft": repository.saved.ArticlePath,
		"final page":    repository.finalArticlePath,
		"structure":     repository.saved.StructurePath,
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
	if !strings.Contains(page, StageReview) {
		t.Fatalf("в слоте финального текста лежит не отредактированная страница: %q", page)
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

// Оборванная разметка дописывается продолжением того же чата, а не уходит в блог половиной
// страницы.
//
// Ответ веб-интерфейса обрывается по двум причинам сразу: длинная страница упирается в предел
// длины сообщения, а прерванный стрим оставляет на странице только то, что успело прийти. И то,
// и другое выглядит исправным HTML — теги закрыты, заголовок и абзац на месте, — поэтому
// обычные проверки разметки такую страницу пропускают.
func TestFlowCompletesCutHTML(t *testing.T) {
	flow, chats, repository, _, writer := newFlowFixture(t)
	page := "H1: Патологическая анатомия\n\n" +
		"Курс даёт практикующему врачу системные знания для морфологической диагностики материала.\n\n" +
		"H2: Подайте заявку\n\n" +
		"Для уточнения деталей программы, дат практики и условий зачисления оставьте заявку на сайте."
	chats.answers[StageReview] = page
	chats.queued = map[string][]string{StageHTML: {
		"<p>Курс даёт практикующему врачу системные знания для морфологической диагностики материала.</p>\n<p class=\"",
		"<h2>Подайте заявку</h2>\n<p>Для уточнения деталей программы, дат практики и условий зачисления оставьте заявку на сайте.</p>",
	}}

	ctx := context.Background()
	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	repository.saved.FixedArticlePath = repository.finalArticlePath
	if err := flow.RunHTML(ctx, "7"); err != nil {
		t.Fatalf("html: %v", err)
	}

	html, err := writer.Read(repository.htmlPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := generation.ValidateHTMLCoversPage(page, html); err != nil {
		t.Fatalf("сохранена оборванная страница: %v\n%s", err, html)
	}
	if strings.Contains(html, "<p class=\"") {
		t.Fatalf("недописанный элемент попал в артефакт: %s", html)
	}
	if len(chats.chats[2]) != 2 {
		t.Fatalf("в чате разметки %d сообщений, ожидалось два: %v", len(chats.chats[2]), chats.chats[2])
	}
}

// Разметка, которая так и не дошла до конца страницы, обязана уронить стадию: артефакт с
// половиной страницы и статус completed — худшее из возможных состояний.
func TestFlowFailsWhenHTMLStaysCut(t *testing.T) {
	flow, chats, repository, _, _ := newFlowFixture(t)
	chats.answers[StageReview] = "H1: Тема\n\n" +
		"Курс даёт практикующему врачу системные знания для морфологической диагностики материала.\n\n" +
		"Для уточнения деталей программы, дат практики и условий зачисления оставьте заявку на сайте."
	chats.answers[StageHTML] = "<p>Курс даёт практикующему врачу системные знания для морфологической диагностики материала.</p>"

	ctx := context.Background()
	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	repository.saved.FixedArticlePath = repository.finalArticlePath
	err := flow.RunHTML(ctx, "7")
	if !errors.Is(err, generation.ErrHTMLIncomplete) {
		t.Fatalf("оборванная разметка принята: %v", err)
	}
	if repository.htmlPath != "" {
		t.Fatalf("путь HTML сохранён при оборванной разметке: %q", repository.htmlPath)
	}
	if len(repository.failures) == 0 {
		t.Fatal("ошибка стадии не сохранена в состоянии статьи")
	}
}

// Кнопка заявки дописывается кодом и стоит последней: у неё фиксированная разметка со
// стилями и классом формы, и просить её у модели на каждом прогоне — значит получать каждый
// раз новую.
func TestHTMLEndsWithCTAButton(t *testing.T) {
	flow, chats, repository, _, writer := newFlowFixture(t)
	chats.answers[StageHTML] = "<h2>Заголовок</h2>\n\n<p>текст</p>"

	ctx := context.Background()
	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	repository.saved.FixedArticlePath = repository.finalArticlePath
	if err := flow.RunHTML(ctx, "7"); err != nil {
		t.Fatalf("html: %v", err)
	}

	html, err := writer.Read(repository.htmlPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.TrimSpace(html), testCTAButton) {
		t.Fatalf("кнопка заявки не дописана в конец разметки:\n%s", html)
	}
	if strings.Count(html, "<button") != 1 {
		t.Fatalf("кнопок в разметке %d, ожидалась одна:\n%s", strings.Count(html, "<button"), html)
	}
}

// Кнопка от модели вторую не порождает: две подряд заметнее, чем чужая вёрстка одной.
func TestHTMLKeepsSingleCTAButton(t *testing.T) {
	flow, chats, repository, _, writer := newFlowFixture(t)
	chats.answers[StageHTML] = "<h2>Заголовок</h2>\n\n<p>текст</p>\n\n<button type=\"button\">Заявка</button>"

	ctx := context.Background()
	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	repository.saved.FixedArticlePath = repository.finalArticlePath
	if err := flow.RunHTML(ctx, "7"); err != nil {
		t.Fatalf("html: %v", err)
	}

	html, err := writer.Read(repository.htmlPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(html, "<button") != 1 {
		t.Fatalf("кнопок в разметке %d, ожидалась одна:\n%s", strings.Count(html, "<button"), html)
	}
}

// Без файла кнопки стадия отказывает до первого сообщения модели: страница без кнопки хуже
// понятной остановки, а платить за разметку, которую всё равно не сохранить, незачем.
func TestHTMLFailsWithoutCTAButtonFile(t *testing.T) {
	flow, chats, repository, _, _ := newFlowFixture(t)
	flow.ctaButtonPath = filepath.Join(t.TempDir(), "missing.html")

	ctx := context.Background()
	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatal(err)
	}
	repository.saved.FixedArticlePath = repository.finalArticlePath
	if err := flow.RunHTML(ctx, "7"); err == nil {
		t.Fatal("стадия html без файла кнопки обязана отказать")
	}
	if len(chats.chats) > 2 {
		t.Fatalf("чат разметки открыт при отсутствующей кнопке: %v", chats.chats)
	}
	if repository.htmlPath != "" {
		t.Fatalf("путь HTML сохранён без кнопки: %q", repository.htmlPath)
	}
}
