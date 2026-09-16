package articleaudit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
)

// fakeArticles — хранилище страницы в памяти. Запоминает порядок вызовов: он и есть контракт
// потока, а не деталь реализации.
type fakeArticles struct {
	article Article
	calls   []string
	failed  error
	// audited — то, что ушло в базу отметкой о проверке.
	audited struct {
		paths         Paths
		score         *int
		scoreMax      *int
		findings      int
		missingFields int
	}
}

func (f *fakeArticles) Get(context.Context, string) (Article, error) { return f.article, nil }

func (f *fakeArticles) MarkProcessing(context.Context, string) error {
	f.calls = append(f.calls, "processing")
	return nil
}

func (f *fakeArticles) SaveFetched(_ context.Context, _ string, postID int64,
	postType, originalPath, fieldsPath string) error {
	f.calls = append(f.calls, "fetched:"+originalPath+"+"+fieldsPath)
	f.article.PostID, f.article.PostType = postID, postType
	return nil
}

func (f *fakeArticles) MarkAudited(_ context.Context, _ string, paths Paths,
	score, scoreMax *int, findings, missingFields int) error {
	f.calls = append(f.calls, "audited:"+paths.ResultPath)
	f.audited.paths, f.audited.score, f.audited.scoreMax = paths, score, scoreMax
	f.audited.findings, f.audited.missingFields = findings, missingFields
	return nil
}

func (f *fakeArticles) MarkFailed(_ context.Context, _ string, cause error) error {
	f.failed = cause
	f.calls = append(f.calls, "failed")
	return nil
}

// fakeBlog — площадка в памяти. Записывает каждое обращение: тест обязан видеть, что поток
// только читал.
type fakeBlog struct {
	post  Post
	calls []string
	// found — что отдаёт поиск по слагу; ноль ID означает «искать нечего».
	found Post
	// findErr — отказ поиска.
	findErr error
}

func (b *fakeBlog) Find(_ context.Context, slug string) (Post, error) {
	b.calls = append(b.calls, "find:"+slug)
	if b.findErr != nil {
		return Post{}, b.findErr
	}
	return b.found, nil
}

func (b *fakeBlog) Read(_ context.Context, postID int64) (Post, error) {
	b.calls = append(b.calls, fmt.Sprintf("read:%d", postID))
	return b.post, nil
}

// fakeChat отдаёт заранее заданные ответы модели и считает сообщения.
type fakeChat struct {
	answers []string
	sent    int
	err     error
}

func (c *fakeChat) Send(_ context.Context, _ string) (string, error) {
	c.sent++
	if c.err != nil {
		return "", c.err
	}
	if len(c.answers) == 0 {
		return "", fmt.Errorf("у подделки кончились ответы")
	}
	answer := c.answers[0]
	c.answers = c.answers[1:]
	return answer, nil
}

// Continue у аудита не зовут вовсе: ответ помещается в одно сообщение. Подделка на этом
// падает намеренно — молчаливое дописывание было бы регрессом.
func (c *fakeChat) Continue(context.Context, string) (string, error) {
	return "", fmt.Errorf("аудит не дописывает ответ")
}

func (c *fakeChat) Close() error { return nil }

type fakeChats struct {
	chat   *fakeChat
	stages []string
}

func (f *fakeChats) NewChat(_ context.Context, _ int64, stages ...string) (taskflow.Chat, error) {
	f.stages = stages
	return f.chat, nil
}

const auditTemplate = `# Аудит: {{.Score}}

Страница: {{.Title}}
Ссылка: {{.URL}}
Тема: {{.Topic}}
Запись: {{.PostID}} ({{.PostType}})
Обязательные поля: {{.RequiredFields}}

## Критические ошибки
{{.Critical}}

## Все найденные ошибки
{{.Issues}}

## Не разобрано
{{.Unparsed}}
`

func newTestFlow(t *testing.T, articles Articles, blog Blog, chats taskflow.ChatFactory,
	required []string) (*Flow, string) {
	t.Helper()
	root := t.TempDir()
	promptPath := filepath.Join(root, "audit.txt")
	if err := os.WriteFile(promptPath,
		[]byte("Проверь статью {{.Title}}\nВОПРОСЫ\n{{.FAQ}}\nТЕКСТ\n{{.OriginalHTML}}"), 0o644); err != nil {
		t.Fatalf("подготовить промпт: %v", err)
	}
	templatePath := filepath.Join(root, "result.md.tmpl")
	if err := os.WriteFile(templatePath, []byte(auditTemplate), 0o644); err != nil {
		t.Fatalf("подготовить шаблон: %v", err)
	}
	flow, err := NewFlow(articles, blog, chats, NewArtifacts(root), promptPath, templatePath, required, nil)
	if err != nil {
		t.Fatalf("NewFlow: %v", err)
	}
	return flow, root
}

func testArticle() Article {
	return Article{ID: 1, ExternalID: "2", SourceURL: "https://dpoprof.ru/obuchenie-medpersonala/logoped/",
		Slug: "logoped", Topic: "Логопед"}
}

func testPost() Post {
	return Post{
		ID: 22314, Title: "Обучение на логопеда", Slug: "logoped", PostType: "obuch_med",
		ContentHTML: "<h2>О профессии</h2><p>Текст страницы.</p>",
		Link:        "https://dpoprof.ru/obuchenie-medpersonala/logoped/",
		Fields: map[string]string{
			"prof_name":  "Логопед",
			"prof_title": "Курсы [blue]логопеда[/blue]",
			"faq_loop":   "5",
			"docs_title": "",
		},
	}
}

// Полный проход: копия страницы и полей ложится на диск до модели, отчёт собирается из
// разобранного ответа, отметка о проверке уходит в базу вместе с оценкой.
func TestFlowAuditsPageEndToEnd(t *testing.T) {
	articles := &fakeArticles{article: testArticle()}
	blog := &fakeBlog{post: testPost()}
	chat := &fakeChat{answers: []string{fullAnswer}}
	chats := &fakeChats{chat: chat}
	flow, root := newTestFlow(t, articles, blog, chats, []string{"prof_name", "docs_title", "price_now"})

	if err := flow.Run(context.Background(), "2"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Копия страницы обязана лечь раньше запроса к модели: сверять отчёт иначе не с чем, а
	// запись в блоге к следующему прогону может уже измениться.
	if got := strings.Join(articles.calls, " "); !strings.HasPrefix(got, "processing fetched:") {
		t.Fatalf("порядок шагов %q", got)
	}
	original, err := os.ReadFile(filepath.Join(root, "2-logoped", OriginalFolder, OriginalHTMLFile))
	if err != nil {
		t.Fatalf("копия страницы не сохранена: %v", err)
	}
	if string(original) != testPost().ContentHTML {
		t.Fatal("сохранена не та копия страницы")
	}
	fields, err := os.ReadFile(filepath.Join(root, "2-logoped", OriginalFolder, OriginalFieldsFile))
	if err != nil {
		t.Fatalf("поля записи не сохранены: %v", err)
	}
	if !strings.Contains(string(fields), "prof_name") {
		t.Fatalf("в поля записи не попало прочитанное: %s", fields)
	}
	// Сырой ответ хранится обязательно: по нему видно, что модель на самом деле сказала.
	answer, err := os.ReadFile(filepath.Join(root, "2-logoped", GeneratedFolder, AuditFile))
	if err != nil {
		t.Fatalf("сырой ответ не сохранён: %v", err)
	}
	if string(answer) != fullAnswer {
		t.Fatal("сохранён не тот ответ")
	}
	report, err := os.ReadFile(filepath.Join(root, "2-logoped", ResultFile))
	if err != nil {
		t.Fatalf("result.md не собран: %v", err)
	}
	text := string(report)
	// Оценка — первой строкой: по ней страницы сравнивают между собой.
	if !strings.HasPrefix(text, "# Аудит: 14/20") {
		t.Fatalf("шапка отчёта: %s", strings.SplitN(text, "\n", 2)[0])
	}
	// Критические ошибки уходят в отчёт дословно.
	if !strings.Contains(text, "Ошибка: на странице нет блока о документе") {
		t.Fatalf("в отчёте нет критических ошибок:\n%s", text)
	}
	// Незаполненные обязательные поля — такая же ошибка страницы, и стоят они в том же
	// списке: человек ищет ошибки в одном месте, а не в двух.
	if !strings.Contains(text, "поле docs_title — обязательное, но не заполнено") ||
		!strings.Contains(text, "поле price_now — обязательное, но в записи его нет вовсе") {
		t.Fatalf("в списке ошибок нет находок кода по полям:\n%s", text)
	}
	if !strings.Contains(text, "text_spec_1") {
		t.Fatalf("в списке ошибок нет находок модели по полям:\n%s", text)
	}
	if score := articles.audited.score; score == nil || *score != 14 {
		t.Fatalf("в базу ушла оценка %v", score)
	}
	if articles.audited.missingFields != 2 {
		t.Fatalf("незаполненных полей в базе %d, ожидалось 2", articles.audited.missingFields)
	}
	if articles.audited.findings != 4 {
		t.Fatalf("находок в базе %d, ожидалось 4", articles.audited.findings)
	}
	// Одна стадия, одно сообщение: дописывания у аудита нет.
	if chat.sent != 1 {
		t.Fatalf("модель спрошена %d раз", chat.sent)
	}
	if len(chats.stages) != 1 || chats.stages[0] != StageAudit {
		t.Fatalf("чат открыт на стадии %v", chats.stages)
	}
}

// Поток обязан только читать площадку. Проверяется дважды: подделка записывает каждый вызов,
// а метода записи в интерфейсе Blog нет вовсе — этого не обойти и по недосмотру.
func TestFlowNeverWritesToBlog(t *testing.T) {
	articles := &fakeArticles{article: testArticle()}
	blog := &fakeBlog{post: testPost()}
	flow, _ := newTestFlow(t, articles, blog, &fakeChats{chat: &fakeChat{answers: []string{fullAnswer}}}, nil)

	if err := flow.Run(context.Background(), "2"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, call := range blog.calls {
		if !strings.HasPrefix(call, "read:") && !strings.HasPrefix(call, "find:") {
			t.Fatalf("поток обратился к площадке не за чтением: %q (все вызовы: %v)", call, blog.calls)
		}
	}
}

// Известный идентификатор записи отменяет поиск по слагу: это один запрос вместо перебора
// тридцати страниц по сотне записей.
func TestFlowSkipsSearchWhenPostIDKnown(t *testing.T) {
	article := testArticle()
	article.PostID = 22314
	articles := &fakeArticles{article: article}
	blog := &fakeBlog{post: testPost()}
	flow, _ := newTestFlow(t, articles, blog, &fakeChats{chat: &fakeChat{answers: []string{fullAnswer}}}, nil)

	if err := flow.Run(context.Background(), "2"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, call := range blog.calls {
		if strings.HasPrefix(call, "find:") {
			t.Fatalf("поиск по слагу состоялся при известном идентификаторе: %v", blog.calls)
		}
	}
	if blog.calls[0] != "read:22314" {
		t.Fatalf("запись прочитана как %q", blog.calls[0])
	}
}

// Без идентификатора запись ищут по слагу — прежним путём.
func TestFlowFindsBySlugWithoutPostID(t *testing.T) {
	articles := &fakeArticles{article: testArticle()}
	blog := &fakeBlog{post: testPost(), found: Post{ID: 22314, Link: "https://dpoprof.ru/obuchenie-medpersonala/logoped/"}}
	flow, _ := newTestFlow(t, articles, blog, &fakeChats{chat: &fakeChat{answers: []string{fullAnswer}}}, nil)

	if err := flow.Run(context.Background(), "2"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if blog.calls[0] != "find:logoped" {
		t.Fatalf("первым вызовом был %q", blog.calls[0])
	}
}

// Идентификатор человек переносит руками, и опечатка в нём даёт правдоподобную чужую
// страницу. Ловится это слагом — и обязано быть отказом, а не молчаливой проверкой чужого.
func TestFlowRejectsPostIDPointingElsewhere(t *testing.T) {
	article := testArticle()
	article.PostID = 22315
	articles := &fakeArticles{article: article}
	other := testPost()
	other.ID, other.Slug = 22315, "sanitar"
	blog := &fakeBlog{post: other}
	chat := &fakeChat{answers: []string{fullAnswer}}
	flow, _ := newTestFlow(t, articles, blog, &fakeChats{chat: chat}, nil)

	err := flow.Run(context.Background(), "2")
	if err == nil {
		t.Fatal("чужая запись проверена молча")
	}
	if !strings.Contains(err.Error(), "проверьте идентификатор записи") {
		t.Fatalf("отказ не объясняет причину: %v", err)
	}
	// За чужую страницу платить не должны: модель не спрашивается вовсе.
	if chat.sent != 0 {
		t.Fatalf("модель спрошена %d раз при расхождении", chat.sent)
	}
	if articles.failed == nil {
		t.Fatal("причина отказа не сохранена")
	}
}

// Уже проверенную страницу прогон пропускает: за ответ заплачено, и второй проход платил бы
// снова за тот же отчёт.
func TestFlowSkipsAuditedPage(t *testing.T) {
	article := testArticle()
	checked := articleTime()
	article.CheckedAt = &checked
	articles := &fakeArticles{article: article}
	blog := &fakeBlog{post: testPost()}
	chat := &fakeChat{answers: []string{fullAnswer}}
	flow, _ := newTestFlow(t, articles, blog, &fakeChats{chat: chat}, nil)

	if err := flow.Run(context.Background(), "2"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(blog.calls) != 0 || chat.sent != 0 || len(articles.calls) != 0 {
		t.Fatalf("проверенная страница тронута: блог %v, сообщений %d, база %v",
			blog.calls, chat.sent, articles.calls)
	}
}

// Ответ, в котором нет ни одного якоря, не теряется: он целиком уходит в «Не разобрано», и
// пустые блоки отчёта печатаются словами, а не пропускаются.
func TestFlowRendersReportWithEmptySections(t *testing.T) {
	articles := &fakeArticles{article: testArticle()}
	blog := &fakeBlog{post: testPost()}
	chats := &fakeChats{chat: &fakeChat{answers: []string{"Страница хорошая, замечаний у меня нет."}}}
	flow, root := newTestFlow(t, articles, blog, chats, nil)

	if err := flow.Run(context.Background(), "2"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	report, err := os.ReadFile(filepath.Join(root, "2-logoped", ResultFile))
	if err != nil {
		t.Fatalf("result.md не собран: %v", err)
	}
	text := string(report)
	if !strings.Contains(text, "оценка не разобрана") {
		t.Fatalf("ненайденная оценка подменена числом:\n%s", text)
	}
	if strings.Count(text, noFindings) < 2 {
		t.Fatalf("пустые блоки не подписаны словами:\n%s", text)
	}
	if !strings.Contains(text, "Страница хорошая, замечаний у меня нет.") {
		t.Fatalf("текст без якорей потерян:\n%s", text)
	}
	// Проверка полей выключена, и отчёт обязан сказать это словами, а не «0 не заполнено».
	if !strings.Contains(text, "проверка выключена") {
		t.Fatalf("выключенная проверка полей выглядит как пройденная:\n%s", text)
	}
	if articles.audited.score != nil {
		t.Fatalf("в базу ушла придуманная оценка %v", *articles.audited.score)
	}
}

// Пустой ответ — отказ стадии: отчёт ни о чём хуже, чем его отсутствие.
func TestFlowFailsOnEmptyAnswer(t *testing.T) {
	articles := &fakeArticles{article: testArticle()}
	blog := &fakeBlog{post: testPost()}
	flow, root := newTestFlow(t, articles, blog, &fakeChats{chat: &fakeChat{answers: []string{"   "}}}, nil)

	err := flow.Run(context.Background(), "2")
	if !errors.Is(err, ErrEmptyAnswer) {
		t.Fatalf("пустой ответ принят: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "2-logoped", ResultFile)); !os.IsNotExist(statErr) {
		t.Fatalf("отчёт собран по пустому ответу: %v", statErr)
	}
	// Копия страницы при этом остаётся: она легла до модели и стоила ноль.
	if _, statErr := os.Stat(filepath.Join(root, "2-logoped", OriginalFolder, OriginalHTMLFile)); statErr != nil {
		t.Fatalf("копия страницы потеряна: %v", statErr)
	}
}

// Пустое тело записи — отказ до модели: проверять нечего, а платить пришлось бы.
func TestFlowFailsOnEmptyBody(t *testing.T) {
	articles := &fakeArticles{article: testArticle()}
	post := testPost()
	post.ContentHTML = "   "
	blog := &fakeBlog{post: post, found: Post{ID: 22314}}
	chat := &fakeChat{answers: []string{fullAnswer}}
	flow, _ := newTestFlow(t, articles, blog, &fakeChats{chat: chat}, nil)

	if err := flow.Run(context.Background(), "2"); err == nil {
		t.Fatal("пустая запись проверена")
	}
	if chat.sent != 0 {
		t.Fatalf("модель спрошена %d раз по пустой записи", chat.sent)
	}
}

// План читает блог, считает поля и не спрашивает модель.
func TestPlanReadsWithoutModel(t *testing.T) {
	article := testArticle()
	article.PostID = 22314
	articles := &fakeArticles{article: article}
	blog := &fakeBlog{post: testPost()}
	chat := &fakeChat{answers: []string{fullAnswer}}
	flow, root := newTestFlow(t, articles, blog, &fakeChats{chat: chat}, []string{"prof_name", "docs_title"})

	planned, err := flow.Plan(context.Background(), article)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if planned.FoundBy != foundByID || planned.PostID != 22314 || planned.PostType != "obuch_med" {
		t.Fatalf("план разобран как %+v", planned)
	}
	if planned.Headings != 1 {
		t.Fatalf("заголовков насчитано %d, ожидался 1", planned.Headings)
	}
	if len(planned.Fields.Empty) != 1 || planned.Fields.Empty[0] != "docs_title" {
		t.Fatalf("план не показал пустые поля: %+v", planned.Fields)
	}
	if chat.sent != 0 {
		t.Fatalf("план спросил модель %d раз", chat.sent)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 2 {
		t.Fatalf("план оставил артефакты: %d записей в каталоге", len(entries))
	}
}

// Незаполненный промпт останавливает прогон до модели: за каждый ответ платим.
func TestEnsurePromptFilled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.txt")
	if err := os.WriteFile(path, []byte("Проверь страницу\n"+PromptPlaceholder), 0o644); err != nil {
		t.Fatalf("подготовить промпт: %v", err)
	}
	if err := EnsurePromptFilled(path); err == nil {
		t.Fatal("незаполненный промпт разрешён к прогону")
	}
	if err := os.WriteFile(path, []byte("Проверь страницу по регламенту"), 0o644); err != nil {
		t.Fatalf("подготовить промпт: %v", err)
	}
	if err := EnsurePromptFilled(path); err != nil {
		t.Fatalf("заполненный промпт отвергнут: %v", err)
	}
}

// Переполненное поле выдачи попадает в отчёт как обычная ошибка страницы — и попадает даже
// тогда, когда список обязательных полей пуст: длина к обязательности отношения не имеет.
func TestFlowReportsOverlongSEOFields(t *testing.T) {
	post := testPost()
	post.Fields[SEOMetaDescriptionField] = strings.Repeat("а", SEOMetaDescriptionLimit+40)
	articles := &fakeArticles{article: testArticle()}
	blog := &fakeBlog{post: post}
	flow, root := newTestFlow(t, articles, blog, &fakeChats{chat: &fakeChat{answers: []string{fullAnswer}}}, nil)

	if err := flow.Run(context.Background(), "2"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	report, err := os.ReadFile(filepath.Join(root, "2-logoped", ResultFile))
	if err != nil {
		t.Fatalf("result.md не собран: %v", err)
	}
	text := string(report)
	if !strings.Contains(text, SEOMetaDescriptionField) || !strings.Contains(text, "в выдаче обрежется") {
		t.Fatalf("переполненное описание не попало в отчёт:\n%s", text)
	}
	// Счётчик обязательных полей при этом не вырос: списка обязательных нет вовсе.
	if articles.audited.missingFields != 0 {
		t.Fatalf("переполнение посчитано как незаполненное поле: %d", articles.audited.missingFields)
	}
}

// Поле выдачи в пределах нормы отчёт не упоминает: проверка была и молчит.
func TestFlowKeepsQuietOnFittingSEOFields(t *testing.T) {
	post := testPost()
	post.Fields[SEOTitleField] = "Обучение на логопеда — курс с практикой"
	post.Fields[SEOMetaDescriptionField] = "Дистанционный курс логопеда с практикой и внесением в ФИС ФРДО."
	articles := &fakeArticles{article: testArticle()}
	blog := &fakeBlog{post: post}
	flow, root := newTestFlow(t, articles, blog, &fakeChats{chat: &fakeChat{answers: []string{fullAnswer}}}, nil)

	if err := flow.Run(context.Background(), "2"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	report, err := os.ReadFile(filepath.Join(root, "2-logoped", ResultFile))
	if err != nil {
		t.Fatalf("result.md не собран: %v", err)
	}
	if strings.Contains(string(report), "в выдаче обрежется") {
		t.Fatalf("отчёт пожаловался на поля, которые в пределах:\n%s", report)
	}
}
