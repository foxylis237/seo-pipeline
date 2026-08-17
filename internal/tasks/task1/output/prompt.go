package output

import (
	"fmt"
	"os"
	"path/filepath"
)

// articlePromptFile — имя артефакта с промптом статьи. То же значение, что складывает
// articlePaths: там оно собирается для записи, здесь — для чтения.
const articlePromptFile = "article_prompt.txt"

// ArticlePromptText читает сохранённый промпт статьи и возвращает его вместе с путём
// относительно корня вывода.
//
// Это ровно тот текст, который пайплайн отправил модели на стадии article: writer записывает
// в этот файл articleResult.Prompt и ничего к нему не добавляет. Повторная публикация читает
// его, а не собирает промпт заново, — иначе выгруженное разошлось бы с тем, что видела модель.
func (w *Writer) ArticlePromptText(externalID string) (string, string, error) {
	directory, found, err := w.findArticleDirectoryForClear(externalID)
	if err != nil {
		return "", "", err
	}
	if !found {
		return "", "", fmt.Errorf("каталог статьи external_id %q не найден в %s", externalID, w.root)
	}
	relativePath := filepath.ToSlash(filepath.Join(directory, "prompts", articlePromptFile))
	data, err := os.ReadFile(filepath.Join(w.root, filepath.FromSlash(relativePath)))
	if err != nil {
		if os.IsNotExist(err) {
			return "", relativePath, fmt.Errorf("промпт статьи external_id %q ещё не сохранён: %s", externalID, relativePath)
		}
		return "", relativePath, fmt.Errorf("прочитать промпт статьи %s: %w", relativePath, err)
	}
	return string(data), relativePath, nil
}
