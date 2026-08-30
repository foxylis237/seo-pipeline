package pproffix1

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
)

// fakeArticles — хранилище статьи в памяти. Запоминает порядок вызовов: он и есть контракт
// потока, а не деталь реализации.
type fakeArticles struct {
	article Article
	calls   []string
	failed  error
}

func (f *fakeArticles) Get(context.Context, string) (Article, error) { return f.article, nil }

func (f *fakeArticles) MarkProcessing(context.Context, string) error {
	f.calls = append(f.calls, "processing")
	return nil
}

func (f *fakeArticles) SaveFetched(_ context.Context, _ string, postID int64, oldTitle, newTitle, originalPath string) error {
	f.calls = append(f.calls, "fetched:"+originalPath)
	f.article.PostID = postID
	f.article.OldTitle, f.article.NewTitle = oldTitle, newTitle
	return nil
}

func (f *fakeArticles) SaveRewritten(_ context.Context, _, promptPath, rewrittenPath string) error {
	f.calls = append(f.calls, "rewritten:"+rewrittenPath)
	return nil
}

func (f *fakeArticles) MarkUpdated(_ context.Context, _, resultPath string) error {
	f.calls = append(f.calls, "updated:"+resultPath)
	return nil
}

func (f *fakeArticles) MarkFailed(_ context.Context, _ string, cause error) error {
	f.failed = cause
	f.calls = append(f.calls, "failed")
	return nil
}

// fakeBlog — площадка в памяти. Запись меняет её состояние, поэтому обратная сверка потока
// проверяется по-настоящему.
type fakeBlog struct {
	post    Post
	writes  int
	writeAt int // на какой по счёту записи вернуть ошибку; 0 — не возвращать
	// written — поля, ушедшие последней записью: их проверяют тесты заголовка.
	written []Field
}

func (b *fakeBlog) Find(context.Context, string) (Post, error) {
	return Post{ID: b.post.ID, Link: b.post.Link}, nil
}

func (b *fakeBlog) Read(context.Context, int64) (Post, error) { return b.post, nil }

func (b *fakeBlog) Write(_ context.Context, _ int64, title, contentHTML string, fields []Field) error {
	b.writes++
	if b.writeAt == b.writes {
		return fmt.Errorf("площадка отказала")
	}
	b.post.Title, b.post.ContentHTML = title, contentHTML
	b.written = fields
	if b.post.Fields == nil {
		b.post.Fields = make(map[string]string)
	}
	for _, field := range fields {
		b.post.Fields[field.Key] = field.Value
	}
	return nil
}

// fakeChat отдаёт заранее заданные ответы модели.
type fakeChat struct{ answers []string }

func (c *fakeChat) Send(_ context.Context, _ string) (string, error) { return c.next() }

func (c *fakeChat) Continue(_ context.Context, _ string) (string, error) { return c.next() }

func (c *fakeChat) Close() error { return nil }

func (c *fakeChat) next() (string, error) {
	if len(c.answers) == 0 {
		return "", fmt.Errorf("у подделки кончились ответы")
	}
	answer := c.answers[0]
	c.answers = c.answers[1:]
	return answer, nil
}

type fakeChats struct{ chat *fakeChat }

func (f fakeChats) NewChat(context.Context, int64, ...string) (taskflow.Chat, error) {
	return f.chat, nil
}

func newTestFlow(t *testing.T, articles Articles, blog Blog, chat *fakeChat) (*Flow, string) {
	t.Helper()
	root := t.TempDir()
	promptPath := filepath.Join(root, "rewrite.txt")
	if err := os.WriteFile(promptPath, []byte("Правь статью {{.URL}}\n{{.OriginalHTML}}"), 0o644); err != nil {
		t.Fatalf("подготовить промпт: %v", err)
	}
	templatePath := filepath.Join(root, "result.md.tmpl")
	if err := os.WriteFile(templatePath, []byte("{{.Title}}\n{{.URL}}"), 0o644); err != nil {
		t.Fatalf("подготовить шаблон: %v", err)
	}
	rule, err := NewTitleRule("Курс с внесением в ФИС ФРДО", "Курс с практикой и внесением в ФИС ФРДО")
	if err != nil {
		t.Fatalf("NewTitleRule: %v", err)
	}
	flow, err := NewFlow(articles, blog, fakeChats{chat: chat}, NewArtifacts(root), rule,
		promptPath, templatePath, nil)
	if err != nil {
		t.Fatalf("NewFlow: %v", err)
	}
	return flow, root
}

func testArticle() Article {
	return Article{ID: 1, ExternalID: "12", SourceURL: "https://dpo-prof.ru/blog/medsestra/", Slug: "medsestra"}
}

// Полный проход: оригинал ложится на диск, в блог уходит новый заголовок и правленый текст,
// в result.md — название и ссылка.
func TestFlowRewritesArticleEndToEnd(t *testing.T) {
	rewritten := strings.Replace(originalArticle, "Удостоверение о повышении квалификации",
		"Диплом об обучении", 1)
	articles := &fakeArticles{article: testArticle()}
	blog := &fakeBlog{post: Post{
		ID: 777, Title: "Медсестра - обучение Курс с внесением в ФИС ФРДО",
		ContentHTML: originalArticle, Link: "https://dpo-prof.ru/blog/medsestra/",
	}}
	flow, root := newTestFlow(t, articles, blog, &fakeChat{answers: []string{rewritten}})

	if err := flow.Run(context.Background(), "12"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if blog.post.Title != "Медсестра - обучение Курс с практикой и внесением в ФИС ФРДО" {
		t.Fatalf("в блоге заголовок %q", blog.post.Title)
	}
	if !strings.Contains(blog.post.ContentHTML, "Диплом об обучении") {
		t.Fatal("в блог ушёл не правленый текст")
	}
	original, err := os.ReadFile(filepath.Join(root, "12-medsestra", OriginalFolder, OriginalHTMLFile))
	if err != nil {
		t.Fatalf("оригинал не сохранён: %v", err)
	}
	if string(original) != originalArticle {
		t.Fatal("сохранён не тот оригинал")
	}
	result, err := os.ReadFile(filepath.Join(root, "12-medsestra", ResultFile))
	if err != nil {
		t.Fatalf("result.md не собран: %v", err)
	}
	if !strings.Contains(string(result), "https://dpo-prof.ru/blog/medsestra/") {
		t.Fatalf("в result.md нет ссылки на статью: %s", result)
	}
	if got := strings.Join(articles.calls, " "); !strings.HasPrefix(got, "processing fetched:") {
		t.Fatalf("порядок шагов %q: оригинал обязан сохраниться до правки", got)
	}
}

// Уже переписанная статья второй раз в блог не уходит: иначе модель получила бы свой же вывод.
func TestFlowSkipsAlreadyRewritten(t *testing.T) {
	moment := timeNow()
	article := testArticle()
	article.UpdatedPostAt = &moment
	articles := &fakeArticles{article: article}
	blog := &fakeBlog{post: Post{ID: 777, Title: "Курс с внесением в ФИС ФРДО", ContentHTML: originalArticle}}
	flow, _ := newTestFlow(t, articles, blog, &fakeChat{})

	if err := flow.Run(context.Background(), "12"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if blog.writes != 0 {
		t.Fatalf("в блог ушло %d правок, ожидалось 0", blog.writes)
	}
}

// Заголовок, к которому правило не подходит, останавливает статью до запроса к модели: за
// ответ уже было бы заплачено, а публиковать его всё равно нельзя.
func TestFlowStopsBeforeModelWhenTitleRuleDoesNotMatch(t *testing.T) {
	articles := &fakeArticles{article: testArticle()}
	blog := &fakeBlog{post: Post{ID: 777, Title: "Совсем другой заголовок", ContentHTML: originalArticle}}
	chat := &fakeChat{answers: []string{originalArticle}}
	flow, _ := newTestFlow(t, articles, blog, chat)

	if err := flow.Run(context.Background(), "12"); err == nil {
		t.Fatal("Run вернул успех при неподходящем правиле переименования")
	}
	if len(chat.answers) != 1 {
		t.Fatal("модель всё-таки спросили")
	}
	if blog.writes != 0 {
		t.Fatalf("в блог ушло %d правок, ожидалось 0", blog.writes)
	}
	if articles.failed == nil {
		t.Fatal("ошибка статьи не сохранена")
	}
}

// Оборванный ответ дописывается продолжением того же чата, а не роняет стадию.
func TestFlowContinuesTruncatedAnswer(t *testing.T) {
	head := originalArticle[:strings.Index(originalArticle, "<h2>Какой документ")]
	tail := originalArticle[strings.Index(originalArticle, "<h2>Какой документ"):]
	articles := &fakeArticles{article: testArticle()}
	blog := &fakeBlog{post: Post{ID: 777, Title: "Курс с внесением в ФИС ФРДО", ContentHTML: originalArticle}}
	flow, root := newTestFlow(t, articles, blog, &fakeChat{answers: []string{head, tail}})

	if err := flow.Run(context.Background(), "12"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	saved, err := os.ReadFile(filepath.Join(root, "12-medsestra", GeneratedFolder, RewrittenHTMLFile))
	if err != nil {
		t.Fatalf("правка не сохранена: %v", err)
	}
	if !strings.Contains(string(saved), "Какой документ") {
		t.Fatalf("страница не дописана: %s", saved)
	}
}

func timeNow() time.Time { return time.Now() }

// Видимый заголовок страницы живёт в поле темы, а не в названии записи: правка, не тронувшая
// его, оставила бы на странице новый текст под старым H1.
func TestFlowRenamesVisibleHeaderField(t *testing.T) {
	articles := &fakeArticles{article: testArticle()}
	blog := &fakeBlog{post: Post{
		ID:          777,
		Title:       "Медсестра - Курс с внесением в ФИС ФРДО",
		ContentHTML: originalArticle,
		Fields: map[string]string{
			HeaderField:   "Медсестра в косметологии - Курс с внесением в ФИС ФРДО",
			SEOTitleField: "Медсестра - Курс с внесением в ФРДО",
		},
		FieldIDs: map[string]string{HeaderField: "10", SEOTitleField: "11"},
	}}
	flow, _ := newTestFlow(t, articles, blog, &fakeChat{answers: []string{originalArticle}})

	if err := flow.Run(context.Background(), "12"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var header *Field
	for index, field := range blog.written {
		if field.Key == HeaderField {
			header = &blog.written[index]
		}
	}
	if header == nil {
		t.Fatalf("поле %s не отправлено, ушли только: %+v", HeaderField, blog.written)
	}
	if header.ID != "10" {
		t.Fatalf("поле отправлено без идентификатора (%q) — площадка завела бы второе с тем же ключом", header.ID)
	}
	want := "Медсестра в косметологии - Курс с практикой и внесением в ФИС ФРДО"
	if header.Value != want {
		t.Fatalf("видимый заголовок стал %q, ожидался %q", header.Value, want)
	}
}

// Правило не подошло к видимому заголовку — статья останавливается до запроса к модели:
// новый текст под старым названием хуже, чем нетронутая статья.
func TestFlowStopsWhenVisibleHeaderDoesNotMatchRule(t *testing.T) {
	articles := &fakeArticles{article: testArticle()}
	blog := &fakeBlog{post: Post{
		ID:          777,
		Title:       "Медсестра - Курс с внесением в ФИС ФРДО",
		ContentHTML: originalArticle,
		Fields:      map[string]string{HeaderField: "Совсем другое название страницы"},
		FieldIDs:    map[string]string{HeaderField: "10"},
	}}
	chat := &fakeChat{answers: []string{originalArticle}}
	flow, _ := newTestFlow(t, articles, blog, chat)

	if err := flow.Run(context.Background(), "12"); err == nil {
		t.Fatal("Run вернул успех при неподходящем правиле для видимого заголовка")
	}
	if len(chat.answers) != 1 {
		t.Fatal("модель всё-таки спросили")
	}
	if blog.writes != 0 {
		t.Fatalf("в блог ушло %d правок, ожидалось 0", blog.writes)
	}
}
