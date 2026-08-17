package output

import (
	"fmt"
	"os"
	"path/filepath"
)

// articlePromptFile — имя артефакта с промптом статьи. То же значение, что складывает
// articlePaths: там оно собирается для записи, здесь — для чтения.
const articlePromptFile = "article_prompt.txt"

// demoFolder — каталог демо-сборки внутри каталога статьи. Значение повторяет demo.FolderName
// намеренно: пакет demo зависит от output, и импорт в обратную сторону замкнул бы цикл.
const demoFolder = "DEMO"

// ArticlePromptText читает сохранённый промпт статьи и возвращает его вместе с путём
// относительно корня вывода.
//
// Это ровно тот текст, который пайплайн отправил модели на стадии article: writer записывает
// в этот файл articleResult.Prompt и ничего к нему не добавляет. Повторная публикация читает
// его, а не собирает промпт заново, — иначе выгруженное разошлось бы с тем, что видела модель.
func (w *Writer) ArticlePromptText(externalID string) (string, string, error) {
	return w.promptText(externalID, "prompts", articlePromptFile)
}

// DemoArticlePromptText читает промпт статьи из каталога DEMO.
//
// DEMO существует для ручного прогона: этот файл открывают и отправляют в чат руками. Он
// пишется даже тогда, когда стадия article не удалась, — промпт рендерится до обращения к
// модели, и именно поэтому его есть смысл выгружать после неудачной генерации.
func (w *Writer) DemoArticlePromptText(externalID string) (string, string, error) {
	return w.promptText(externalID, demoFolder, articlePromptFile)
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
