package result

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/tasks/task1/output"
)

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
		Article:  article.Article{ID: 7, ExternalID: "37", Title: "Название", Slug: "tema"},
		Category: "Рубрика", Tags: "ОТДЕЛЬНЫЕ МЕТКИ", TLDR: "ОТДЕЛЬНЫЙ TLDR", AdditionalInfo: "НЕРАСПОЗНАННЫЙ ТЕКСТ",
		SEOTitle: "Название", ProfessionName: "Профессия", ImageName: "Заголовок картинки", ImageURL: "image-slug",
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
	service := NewService(fakeRepository{input}, writer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.templatePath = filepath.Join("..", "..", "..", "..", "tasks", "task_1", "templates", "result.md.tmpl")
	if _, err := service.Build(context.Background(), "37"); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(root, "37-tema", "result.md")
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
	for _, value := range []string{"Название", "Профессия", "Заголовок картинки", "image-slug", "9 мин"} {
		if !strings.Contains(text, value) {
			t.Fatalf("mapped result does not contain %q", value)
		}
	}
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

func TestParseFAQItems(t *testing.T) {
	items, err := ParseFAQItems("Вопрос: Что?\r\nОтвет: Это.\r\n\r\nВопрос: Зачем?\r\nОтвет: Затем.\nПродолжение.")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0] != (FAQItem{Question: "Что?", Answer: "Это."}) || items[1] != (FAQItem{Question: "Зачем?", Answer: "Затем.\nПродолжение."}) {
		t.Fatalf("FAQ items = %#v", items)
	}
}
