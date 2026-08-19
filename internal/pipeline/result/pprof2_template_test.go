package result

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
)

// pprof2TemplatePath — боевой шаблон pprof_2. Как и у task_1, рендерится именно он: копия в
// тесте разошлась бы с шаблоном молча.
func pprof2TemplatePath() string {
	return filepath.Join("..", "..", "..", "tasks", "pprof_2", "templates", TemplateFileName)
}

func stagedArticle(t *testing.T) (*articleoutput.Writer, string) {
	t.Helper()
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	path := filepath.Join("5-obuchenie", "generated", "article.txt")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), []byte("текст страницы"), 0o600); err != nil {
		t.Fatal(err)
	}
	return writer, filepath.ToSlash(path)
}

// Шаблон pprof_2 печатает свои поля из своих колонок, а не приближения: рубрика и категория
// живут порознь, SEO-заголовок не равен названию, профессия не равна фокусному ключу.
func TestPProf2TemplateRendersOwnFields(t *testing.T) {
	writer, articlePath := stagedArticle(t)
	input := article.ResultInput{
		Article:         article.Article{ID: 5, ExternalID: "5", Title: "Обучение на стропальщика", Slug: "obuchenie"},
		Category:        "Рабочие профессии",
		Section:         "Профессиональное обучение",
		SEOTitle:        "Обучение на стропальщика — курсы и удостоверение",
		Profession:      "Стропальщик",
		ServiceName:     "Обучение стропальщиков",
		Teachers:        "Иванов И. И.",
		Keyword:         "обучение на стропальщика",
		MetaDescription: "Дистанционное обучение стропальщиков",
		Header:          "Обучение на стропальщика",
		ArticlePath:     articlePath,
	}
	service := NewService(fakeRepository{input}, writer, slog.New(slog.NewTextHandler(io.Discard, nil)), pprof2TemplatePath())
	if _, err := service.Build(context.Background(), "5"); err != nil {
		t.Fatal(err)
	}
	rendered, err := writer.Read(filepath.ToSlash(filepath.Join("5-obuchenie", "result.md")))
	if err != nil {
		t.Fatal(err)
	}
	for heading, want := range map[string]string{
		"## Рубрика":                 "Профессиональное обучение",
		"## Категория":               "Рабочие профессии",
		"## SEO-заголовок":           "Обучение на стропальщика — курсы и удостоверение",
		"## Название профессии":      "Стропальщик",
		"## Название услуги":         "Обучение стропальщиков",
		"## Преподаватели":           "Иванов И. И.",
		"## Фокусное ключевое слово": "обучение на стропальщика",
		"## Alt главной картинки":    "Обучение на стропальщика",
	} {
		if !strings.Contains(rendered, heading) {
			t.Fatalf("в result.md нет раздела %q", heading)
		}
		if !strings.Contains(rendered, want) {
			t.Fatalf("в result.md нет значения %q раздела %q:\n%s", want, heading, rendered)
		}
	}
	// TL;DR, время чтения и метки pprof_2 не генерирует и не публикует — их разделов в
	// шаблоне быть не должно. FAQ здесь пуст, поэтому вопросов тоже нет: раздел печатается
	// только вместе с содержимым.
	for _, absent := range []string{"## TL;DR", "## Вопрос 1", "## Время чтения", "## Метки"} {
		if strings.Contains(rendered, absent) {
			t.Fatalf("в result.md появился раздел %q, которого у pprof_2 нет", absent)
		}
	}
}

// FAQ у pprof_2 берётся из текста страницы, но печатается тем же разделом, что и у задач со
// стадией info: человек читает result.md одинаково, откуда бы вопросы ни пришли.
func TestPProf2TemplateRendersFAQ(t *testing.T) {
	writer, articlePath := stagedArticle(t)
	input := article.ResultInput{
		Article:     article.Article{ID: 5, ExternalID: "5", Title: "Обучение на стропальщика", Slug: "obuchenie"},
		FAQ:         "Вопрос: Какой документ выдают?\nОтвет: Удостоверение стропальщика.",
		ArticlePath: articlePath,
	}
	service := NewService(fakeRepository{input}, writer, slog.New(slog.NewTextHandler(io.Discard, nil)), pprof2TemplatePath())
	if _, err := service.Build(context.Background(), "5"); err != nil {
		t.Fatal(err)
	}
	rendered, err := writer.Read(filepath.ToSlash(filepath.Join("5-obuchenie", "result.md")))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Вопрос 1", "Какой документ выдают?", "Удостоверение стропальщика."} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("в result.md нет %q:\n%s", want, rendered)
		}
	}
	// TL;DR и время чтения при этом не появляются: их у задачи по-прежнему нет.
	for _, absent := range []string{"## TL;DR", "## Время чтения"} {
		if strings.Contains(rendered, absent) {
			t.Fatalf("в result.md появился раздел %q, которого у pprof_2 нет", absent)
		}
	}
}

// У задачи без этих колонок значения пусты, и подставляются прежние приближения: так
// result.md task_1 и pprof_1 остаётся тем же, что был до появления колонок.
func TestSEOTitleAndProfessionFallBackToOldApproximations(t *testing.T) {
	writer, articlePath := stagedArticle(t)
	input := article.ResultInput{
		Article:     article.Article{ID: 5, ExternalID: "5", Title: "Название статьи", Slug: "obuchenie"},
		Keyword:     "Фокусный ключ",
		Header:      "H1",
		ArticlePath: articlePath,
	}
	service := NewService(fakeRepository{input}, writer, slog.New(slog.NewTextHandler(io.Discard, nil)), testTemplatePath())
	if _, err := service.Build(context.Background(), "5"); err != nil {
		t.Fatal(err)
	}
	rendered, err := writer.Read(filepath.ToSlash(filepath.Join("5-obuchenie", "result.md")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "## SEO-заголовок\n\n```text\nНазвание статьи\n```") {
		t.Fatalf("SEO-заголовок перестал быть названием статьи:\n%s", rendered)
	}
	if !strings.Contains(rendered, "## Название профессии\n\n```text\nФокусный ключ\n```") {
		t.Fatalf("название профессии перестало быть фокусным ключом:\n%s", rendered)
	}
}
