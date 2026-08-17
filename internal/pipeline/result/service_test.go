package result

import (
	"bytes"
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

// testTemplatePath — боевой шаблон task_1. Тесты рендерят именно его, а не копию: расхождение
// шаблона и ожиданий должно ломать сборку, а не проходить молча.
func testTemplatePath() string {
	return filepath.Join("..", "..", "..", "tasks", "task_1", "templates", TemplateFileName)
}

type fakeRepository struct{ input article.ResultInput }

func (r fakeRepository) GetResultInput(context.Context, string) (article.ResultInput, error) {
	return r.input, nil
}

func TestReadingTimeMinutes(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{"empty", " \n\t", 0},
		{"one", "слово", 1},
		{"180", strings.Repeat("слово ", 180), 1},
		{"181", strings.Repeat("слово ", 181), 2},
		{"360", strings.Repeat("слово ", 360), 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ReadingTimeMinutes(test.text); got != test.want {
				t.Fatalf("ReadingTimeMinutes() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestBuildUsesStructuredMetadataOrderEmptyHTMLAndOverwrites(t *testing.T) {
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	input := article.ResultInput{
		Article:  article.Article{ID: 7, ExternalID: "37", Title: "Название", Slug: "image-slug"},
		Category: "Рубрика", Tags: "ОТДЕЛЬНЫЕ МЕТКИ", TLDR: "ОТДЕЛЬНЫЙ TLDR", AdditionalInfo: "НЕРАСПОЗНАННЫЙ ТЕКСТ",
		Keyword:     "Профессия",
		Header:      "H1 статьи",
		FAQ:         "Вопрос: Первый вопрос?\nОтвет: Первый ответ.\n\nВопрос: Второй вопрос?\nОтвет: Второй ответ.",
		ArticlePath: "37-tema/generated/article.txt", HTMLPath: "37-tema/missing.html",
	}
	articleFile := filepath.Join(root, filepath.FromSlash(input.ArticlePath))
	if err := os.MkdirAll(filepath.Dir(articleFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(articleFile, []byte(strings.Repeat("слово ", 1441)), 0o644); err != nil {
		t.Fatal(err)
	}
	service := NewService(fakeRepository{input}, writer, slog.New(slog.NewTextHandler(io.Discard, nil)), testTemplatePath())
	if _, err := service.Build(context.Background(), "37"); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(root, "37-image-slug", "result.md")
	first, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(first)
	labels := []string{"## Название", "## Рубрика", "## Метки", "## TL;DR", "## Допинфо", "## Вопрос 1", "## Вопрос 2", "## Похожие профессии", "## HTML"}
	last := -1
	for _, label := range labels {
		index := strings.Index(text, label)
		if index <= last {
			t.Fatalf("block %q is out of order", label)
		}
		last = index
	}
	for _, value := range []string{"ОТДЕЛЬНЫЕ МЕТКИ", "ОТДЕЛЬНЫЙ TLDR", "НЕРАСПОЗНАННЫЙ ТЕКСТ", "Первый вопрос?", "Первый ответ.", "Второй вопрос?", "Второй ответ."} {
		if !strings.Contains(text, value) {
			t.Fatalf("result does not contain %q", value)
		}
	}
	for _, value := range []string{"Название", "Профессия", "image-slug", "9 мин"} {
		if !strings.Contains(text, value) {
			t.Fatalf("mapped result does not contain %q", value)
		}
	}
	assertResultField(t, text, "SEO-заголовок", "Название")
	assertResultField(t, text, "Название профессии", "Профессия")
	assertResultField(t, text, "Название картинки", "H1 статьи")
	assertResultField(t, text, "URL картинки", "image-slug")
	if strings.Contains(text, "9 мин.") {
		t.Fatal("reading time contains a trailing dot")
	}
	if strings.Contains(text, "missing.html") || strings.Contains(text, "<nil>") || strings.Contains(text, "null") {
		t.Fatalf("empty values rendered incorrectly: %q", text)
	}
	input.Article.Title = "Новое название"
	input.AdditionalInfo = ""
	service.repository = fakeRepository{input}
	if _, err := service.Build(context.Background(), "37"); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(second), "Новое название") || string(second) == string(first) {
		t.Fatal("result.md was not overwritten")
	}
	if strings.Contains(string(second), "## Допинфо") {
		t.Fatal("empty additional info section was rendered")
	}
}

func TestBuildAllowsEmptyMappedFieldsAndLogsWarnings(t *testing.T) {
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	input := article.ResultInput{
		Article:     article.Article{ID: 8, ExternalID: "38"},
		ArticlePath: "38-existing/generated/article.txt",
	}
	articleFile := filepath.Join(root, filepath.FromSlash(input.ArticlePath))
	if err := os.MkdirAll(filepath.Dir(articleFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(articleFile, []byte("текст"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	service := NewService(fakeRepository{input}, writer, slog.New(slog.NewTextHandler(&logs, nil)), testTemplatePath())
	if _, err := service.Build(context.Background(), "38"); err != nil {
		t.Fatal(err)
	}
	resultText, err := os.ReadFile(filepath.Join(root, "38-existing", "result.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"SEO-заголовок", "Название профессии", "Название картинки", "URL картинки"} {
		assertResultField(t, string(resultText), field, "")
		quotedField := `field="` + field + `"`
		plainField := `field=` + field
		if !strings.Contains(logs.String(), `article_id=8`) || !strings.Contains(logs.String(), `external_id=38`) ||
			(!strings.Contains(logs.String(), quotedField) && !strings.Contains(logs.String(), plainField)) {
			t.Fatalf("warning for %q is missing required context: %s", field, logs.String())
		}
	}
	if strings.Contains(string(resultText), "<nil>") {
		t.Fatalf("empty field rendered as nil: %s", resultText)
	}
}

// RenderForDemo обязан отдать result.md на любой статье: demo-папка собирается и до того,
// как статья прошла пайплайн, — иначе проверять результат было бы не по чему.
func TestRenderForDemoFillsKnownFieldsAndLeavesMissingEmpty(t *testing.T) {
	root := t.TempDir()
	input := article.ResultInput{
		Article:  article.Article{ID: 9, ExternalID: "39", Title: "Как стать логопедом", Slug: "logoped"},
		Category: "Профессии", Tags: "метки", TLDR: "коротко", Author: "Редакция", Keyword: "логопед",
		FAQ:      "Вопрос: Что?\nОтвет: Это.",
		HTMLPath: "39-logoped/article.html",
	}
	var logs bytes.Buffer
	service := NewService(fakeRepository{input}, articleoutput.NewWriter(root), slog.New(slog.NewTextHandler(&logs, nil)), testTemplatePath())

	rendered, err := service.RenderForDemo(context.Background(), "39", strings.Repeat("слово ", 180), nil)
	if err != nil {
		t.Fatal(err)
	}
	assertResultField(t, rendered, "Название", "Как стать логопедом")
	assertResultField(t, rendered, "Рубрика", "Профессии")
	assertResultField(t, rendered, "Время чтения", "1 мин")
	// Незаполненные поля остаются пустыми, а не срывают сборку.
	assertResultField(t, rendered, "Мета-описание", "")
	assertResultField(t, rendered, "Файл статьи", "")
	// HTML на диске нет — путь не выдаётся за существующий файл.
	assertResultField(t, rendered, "HTML", "")
	if !strings.Contains(rendered, "Вопрос: Что?") && !strings.Contains(rendered, "Что?") {
		t.Fatalf("FAQ не отрендерен: %q", rendered)
	}
	if strings.Contains(rendered, "<nil>") {
		t.Fatalf("пустое поле отрендерено как nil: %q", rendered)
	}
	if entries, readErr := os.ReadDir(root); readErr != nil || len(entries) != 0 {
		t.Fatalf("RenderForDemo записал файлы: %v, %v", entries, readErr)
	}
}

func TestRenderForDemoSurvivesUnparsableFAQ(t *testing.T) {
	input := article.ResultInput{
		Article: article.Article{ID: 10, ExternalID: "40", Title: "Заголовок", Slug: "slug"},
		FAQ:     "просто текст без маркеров",
	}
	var logs bytes.Buffer
	service := NewService(fakeRepository{input}, articleoutput.NewWriter(t.TempDir()), slog.New(slog.NewTextHandler(&logs, nil)), testTemplatePath())

	rendered, err := service.RenderForDemo(context.Background(), "40", "", nil)
	if err != nil {
		t.Fatalf("RenderForDemo() = %v, want отрендеренный result.md", err)
	}
	assertResultField(t, rendered, "Название", "Заголовок")
	if strings.Contains(rendered, "## Вопрос 1") {
		t.Fatalf("неразобранный FAQ попал в result.md: %q", rendered)
	}
	if !strings.Contains(logs.String(), "FAQ не разобран") {
		t.Fatalf("предупреждение о FAQ не записано: %s", logs.String())
	}
}

// Метаданные, уже собранные стадией info и сохранённые в PostgreSQL, попадают в result.md
// целиком: и метки, и TL;DR, и FAQ.
func TestRenderForDemoRendersStoredMetadata(t *testing.T) {
	input := article.ResultInput{
		Article: article.Article{ID: 11, ExternalID: "41", Title: "Заголовок", Slug: "slug"},
		Tags:    "логопед, обучение", TLDR: "Логопед ставит речь.",
		FAQ: "Вопрос: Где учиться?\nОтвет: В вузе.",
	}
	service := newDemoService(t, input)

	rendered, err := service.RenderForDemo(context.Background(), "41", "текст статьи", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertResultField(t, rendered, "Метки", "логопед, обучение")
	assertResultField(t, rendered, "TL;DR", "Логопед ставит речь.")
	assertResultField(t, rendered, "Вопрос", "Где учиться?")
	assertResultField(t, rendered, "Ответ", "В вузе.")
}

// Метаданные, разобранные demo из article_info.txt, вытесняют сохранённые целиком: пустое
// поле разбора остаётся пустым и не подменяется значением из PostgreSQL.
func TestRenderForDemoReplacesStoredMetadataWholesale(t *testing.T) {
	input := article.ResultInput{
		Article: article.Article{ID: 12, ExternalID: "42", Title: "Заголовок", Slug: "slug"},
		Tags:    "метки из БД", TLDR: "TL;DR из БД", FAQ: "Вопрос: Из БД?\nОтвет: Да.",
	}
	service := newDemoService(t, input)

	rendered, err := service.RenderForDemo(context.Background(), "42", "текст статьи", &article.ArticleInfo{
		Tags: "метки demo", TLDR: "TL;DR demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertResultField(t, rendered, "Метки", "метки demo")
	assertResultField(t, rendered, "TL;DR", "TL;DR demo")
	// FAQ разбора пуст — раздел вопросов пустой, а не взятый из БД.
	if strings.Contains(rendered, "Из БД?") || strings.Contains(rendered, "## Вопрос 1") {
		t.Fatalf("FAQ из БД подмешан к метаданным demo: %q", rendered)
	}
	for _, stored := range []string{"метки из БД", "TL;DR из БД"} {
		if strings.Contains(rendered, stored) {
			t.Fatalf("сохранённое значение %q осталось в result.md: %q", stored, rendered)
		}
	}
}

// Разобранные demo метаданные с FAQ рендерятся так же, как сохранённые: demo и боевой прогон
// дают один и тот же result.md из одних и тех же данных.
func TestRenderForDemoRendersParsedMetadataWithFAQ(t *testing.T) {
	input := article.ResultInput{Article: article.Article{ID: 13, ExternalID: "43", Title: "Заголовок", Slug: "slug"}}
	service := newDemoService(t, input)

	parsed, err := article.ParseArticleInfo("Метки: логопед\nTLDR: Коротко.\nFAQ:\nВопрос: Где учиться?\nОтвет: В вузе.")
	if err != nil {
		t.Fatal(err)
	}
	rendered, renderErr := service.RenderForDemo(context.Background(), "43", "текст статьи", &parsed)
	if renderErr != nil {
		t.Fatal(renderErr)
	}
	assertResultField(t, rendered, "Метки", "логопед")
	assertResultField(t, rendered, "TL;DR", "Коротко.")
	assertResultField(t, rendered, "Вопрос", "Где учиться?")
	assertResultField(t, rendered, "Ответ", "В вузе.")
}

func newDemoService(t *testing.T, input article.ResultInput) *Service {
	t.Helper()
	service := NewService(fakeRepository{input}, articleoutput.NewWriter(t.TempDir()), slog.New(slog.NewTextHandler(io.Discard, nil)), testTemplatePath())
	return service
}

func assertResultField(t *testing.T, result, label, want string) {
	t.Helper()
	block := "## " + label + "\n\n```text\n" + want + "\n```"
	if !strings.Contains(result, block) {
		t.Fatalf("field %q does not equal %q", label, want)
	}
}

func TestParseFAQItems(t *testing.T) {
	items, err := ParseFAQItems("Вопрос: Что?\r\nОтвет: Это.\r\n\r\nВопрос: Зачем?\r\nОтвет: Затем.\nПродолжение.")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0] != (FAQItem{Question: "Что?", Answer: "Это."}) || items[1] != (FAQItem{Question: "Зачем?", Answer: "Затем.\nПродолжение."}) {
		t.Fatalf("FAQ items = %#v", items)
	}
}
