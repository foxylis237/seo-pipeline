package output

import (
	"fmt"
	"os"
	"path/filepath"
)

// Раскладка артефактов статьи, общая для writer, сборщика DEMO и публикации промпта.
const (
	// DemoFolder — каталог демо-сборки внутри каталога статьи.
	DemoFolder = "DEMO"
	// PromptsFolder — подкаталог промптов внутри каталога статьи и внутри DEMO.
	PromptsFolder = "prompts"
	// ArticlePromptFile — имя артефакта с промптом статьи.
	ArticlePromptFile = "article_prompt.txt"
	// ResultFileName — имя собранного result.md; из него публикация читает время чтения.
	ResultFileName = "result.md"
)

// ArticlePromptText возвращает сохранённый промпт статьи и путь к нему относительно корня вывода.
func (w *Writer) ArticlePromptText(externalID string) (string, string, error) {
	return w.promptText(externalID, PromptsFolder, ArticlePromptFile)
}

// DemoArticlePromptText читает промпт статьи из DEMO/prompts/; он есть и после неудачной стадии article.
func (w *Writer) DemoArticlePromptText(externalID string) (string, string, error) {
	return w.promptText(externalID, DemoFolder, PromptsFolder, ArticlePromptFile)
}

// promptText читает промпт по пути внутри каталога статьи.
func (w *Writer) promptText(externalID string, parts ...string) (string, string, error) {
	directory, found, err := w.findArticleDirectoryForClear(externalID)
	if err != nil {
		return "", "", err
	}
	if !found {
		return "", "", fmt.Errorf("каталог статьи external_id %q не найден в %s", externalID, w.root)
	}
	relativePath := filepath.ToSlash(filepath.Join(append([]string{directory}, parts...)...))
	data, err := os.ReadFile(filepath.Join(w.root, filepath.FromSlash(relativePath)))
	if err != nil {
		if os.IsNotExist(err) {
			return "", relativePath, fmt.Errorf("промпт статьи external_id %q ещё не сохранён: %s", externalID, relativePath)
		}
		return "", relativePath, fmt.Errorf("прочитать промпт статьи %s: %w", relativePath, err)
	}
	return string(data), relativePath, nil
}
