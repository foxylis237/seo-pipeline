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

// buildWithGoogleDocURL собирает result.md для статьи с указанным адресом документа.
func buildWithGoogleDocURL(t *testing.T, documentURL string) string {
	t.Helper()
	root := t.TempDir()
	input := article.ResultInput{
		Article:  article.Article{ID: 7, ExternalID: "37", Title: "Название", Slug: "image-slug"},
		Category: "Рубрика", Tags: "Метки", TLDR: "Кратко",
		Keyword: "Профессия", Header: "H1 статьи",
		ArticlePath:  "37-image-slug/generated/article.txt",
		HTMLPath:     "37-image-slug/article.html",
		GoogleDocURL: documentURL,
	}
	for _, relativePath := range []string{input.ArticlePath, input.HTMLPath} {
		full := filepath.Join(root, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("текст статьи"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(fakeRepository{input}, articleoutput.NewWriter(root), slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.templatePath = filepath.Join("..", "..", "..", "..", "tasks", "task_1", "templates", "result.md.tmpl")
	if _, err := service.Build(context.Background(), "37"); err != nil {
		t.Fatal(err)
	}
	rendered, err := os.ReadFile(filepath.Join(root, "37-image-slug", "result.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(rendered)
}

// Ссылка печатается последним разделом — именно там её и ждут.
func TestResultEndsWithGoogleDocSection(t *testing.T) {
	const documentURL = "https://docs.google.com/document/d/AbC123/edit"
	text := buildWithGoogleDocURL(t, documentURL)

	if !strings.Contains(text, "## Гугл Док") {
		t.Fatalf("нет раздела со ссылкой:\n%s", text)
	}
	if !strings.Contains(text, documentURL) {
		t.Fatalf("нет самого адреса:\n%s", text)
	}
	// Раздел обязан идти после HTML: он последний пункт документа.
	if strings.Index(text, "## Гугл Док") < strings.Index(text, "## HTML") {
		t.Fatalf("раздел со ссылкой должен идти после HTML:\n%s", text)
	}
	tail := strings.TrimSpace(text)
	if !strings.HasSuffix(tail, "```") || !strings.Contains(tail[strings.LastIndex(tail, "## "):], documentURL) {
		t.Fatalf("последний раздел документа не про ссылку:\n%s", tail[strings.LastIndex(tail, "## "):])
	}
}

// Без публикации раздел остаётся на месте, но пустой: так решено намеренно, чтобы состав
// разделов result.md не зависел от того, дошла ли публикация.
func TestResultKeepsEmptyGoogleDocSectionWithoutPublication(t *testing.T) {
	text := buildWithGoogleDocURL(t, "")

	if !strings.Contains(text, "## Гугл Док") {
		t.Fatalf("раздел должен выводиться даже без ссылки:\n%s", text)
	}
	section := text[strings.Index(text, "## Гугл Док"):]
	if strings.Contains(section, "http") {
		t.Fatalf("в пустом разделе не должно быть адреса:\n%s", section)
	}
	// Пустое значение — это пустая строка внутри блока, а не отсутствующий блок.
	if !strings.Contains(section, "```text\n\n```") {
		t.Fatalf("ожидался пустой text-блок:\n%q", section)
	}
}

// Сборка не должна падать, когда адреса нет: раздел просто пуст.
func TestResultBuildsWithoutGoogleDocURL(t *testing.T) {
	if text := buildWithGoogleDocURL(t, ""); strings.TrimSpace(text) == "" {
		t.Fatal("result.md пуст")
	}
}
