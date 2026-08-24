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

// Шаблон pprof_2 печатает свои поля из своих колонок, а не приближения: SEO-заголовок не
// равен названию страницы, профессия не равна фокусному ключу, преподаватели — своя колонка.
//
// Разделы проверяются вместе со значением целиком, а не по одному заголовку: человек
// заполняет форму блога копированием, и раздел с чужим значением хуже отсутствующего.
// Особенно это важно для «Заголовка» и «Заголовка H1» — первый печатает название страницы,
// второй заголовок H1, — и для картинки, у которой alt и slug приходят из разных колонок.
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
		Header:          "Курсы [blue]стропальщика[/blue]",
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
	for _, want := range []string{
		"## Заголовок\n\n```text\nОбучение на стропальщика\n```",
		"## Категории\n\n```text\nРабочие профессии\n```",
		"## Фокусное ключевое слово\n\n```text\nобучение на стропальщика\n```",
		"## SEO-заголовок\n\n```text\nОбучение на стропальщика — курсы и удостоверение\n```",
		"## Мета-описание\n\n```text\nДистанционное обучение стропальщиков\n```",
		"## Преподаватели\n\n```text\nИванов И. И.\n```",
		"## Атрибут \"alt\" у главной картинки\n\n```text\nобучение на стропальщика\n```",
		"## slug картинки\n\n```text\nobuchenie\n```",
		"## Заголовок H1\n\n```text\nКурсы [blue]стропальщика[/blue]\n```",
		"## Название профессии\n\n```text\nСтропальщик\n```",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("в result.md нет раздела:\n%s\n\nfile:\n%s", want, rendered)
		}
	}
	// Синий блок печатается одним и тем же текстом у каждой страницы: он не приходит из
	// книги и не зависит от статьи. Проверяется целиком — потерянная строка блока в блоге
	// видна не сразу.
	if !strings.Contains(rendered, "## Синий блок со стоимостью\n\n```text\n"+
		"Цена курсов: от 5 000 ₽\nКод программы 1011\nГрафик — гибкий\nДистанционное обучение\n```") {
		t.Fatalf("синий блок со стоимостью потерян или изменён:\n%s", rendered)
	}
	// TL;DR, время чтения и метки pprof_2 не генерирует и не публикует. Рубрика, название
	// услуги и URL картинки убраны из листа: рубрику блог берёт из категории, а адрес
	// картинки собирается из её slug. FAQ здесь пуст, поэтому вопросов тоже нет: раздел
	// печатается только вместе с содержимым.
	for _, absent := range []string{
		"## TL;DR", "## Вопрос 1", "## Время чтения", "## Метки",
		"## Рубрика", "## Название услуги", "## URL картинки",
	} {
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
