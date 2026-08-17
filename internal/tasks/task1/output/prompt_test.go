package output

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newPromptArticle(t *testing.T, content string) *Writer {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "45-professiya-bioinformatik", "prompts")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("создать каталог промптов: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, articlePromptFile), []byte(content), 0o644); err != nil {
		t.Fatalf("создать промпт: %v", err)
	}
	return NewWriter(root)
}

// Читается ровно то, что лежит в файле: повторная публикация обязана выгружать тот текст,
// который видела модель, а не собранный заново.
func TestArticlePromptTextReturnsFileContentVerbatim(t *testing.T) {
	content := "Заголовок статьи\n\nСтруктура:\nH2 — раздел\n\nКлючи: фреза 100\n"
	writer := newPromptArticle(t, content)

	text, path, err := writer.ArticlePromptText("45")
	if err != nil {
		t.Fatalf("ArticlePromptText вернул ошибку: %v", err)
	}
	if text != content {
		t.Fatalf("текст изменён при чтении:\nбыло: %q\nстало: %q", content, text)
	}
	if path != "45-professiya-bioinformatik/prompts/article_prompt.txt" {
		t.Fatalf("путь артефакта %q", path)
	}
}

// Промпта ещё нет — понятная ошибка вместо пустой строки, которой затёрли бы документ.
func TestArticlePromptTextReportsMissingFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "45-professiya-bioinformatik"), 0o755); err != nil {
		t.Fatalf("создать каталог статьи: %v", err)
	}
	writer := NewWriter(root)

	_, _, err := writer.ArticlePromptText("45")
	if err == nil {
		t.Fatal("ожидалась ошибка отсутствующего промпта")
	}
	if !strings.Contains(err.Error(), "не сохранён") {
		t.Fatalf("непонятная ошибка: %v", err)
	}
}

func TestArticlePromptTextReportsMissingArticleDirectory(t *testing.T) {
	writer := NewWriter(t.TempDir())

	if _, _, err := writer.ArticlePromptText("45"); err == nil {
		t.Fatal("ожидалась ошибка отсутствующего каталога статьи")
	}
}

// Префикс external_id не должен цеплять соседей: 4 и 45 — разные статьи.
func TestArticlePromptTextDoesNotMatchByPrefix(t *testing.T) {
	writer := newPromptArticle(t, "промпт статьи 45")

	if _, _, err := writer.ArticlePromptText("4"); err == nil {
		t.Fatal("статья 4 не должна находиться по каталогу статьи 45")
	}
}
