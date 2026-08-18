package output

import (
	"fmt"
	"os"
	"path/filepath"
)

// Раскладка артефактов статьи. Значения экспортированы потому, что складывают эти пути два
// разных пакета — боевой writer и сборщик DEMO, — а читает их отсюда публикация промпта.
// Пока список был у каждого свой, боевой промпт и промпт DEMO лежали по разным правилам, и
// совпадение держалось на отдельной ветке чтения, а не на общем контракте.
const (
	// DemoFolder — каталог демо-сборки внутри каталога статьи.
	DemoFolder = "DEMO"
	// PromptsFolder — подкаталог промптов внутри каталога статьи и внутри DEMO.
	PromptsFolder = "prompts"
	// ArticlePromptFile — имя артефакта с промптом статьи.
	ArticlePromptFile = "article_prompt.txt"
	// ResultFileName — имя собранного result.md в каталоге статьи.
	//
	// Экспортирована по той же причине, что и соседи: путь складывает не только writer.
	// Публикация в WordPress читает отсюда готовое время чтения, и расхождение в имени она
	// не заметит — просто перестанет находить файл.
	ResultFileName = "result.md"
)

// ArticlePromptText читает сохранённый промпт статьи и возвращает его вместе с путём
// относительно корня вывода.
//
// Это ровно тот текст, который пайплайн отправил модели на стадии article: writer записывает
// в этот файл articleResult.Prompt и ничего к нему не добавляет. Повторная публикация читает
// его, а не собирает промпт заново, — иначе выгруженное разошлось бы с тем, что видела модель.
func (w *Writer) ArticlePromptText(externalID string) (string, string, error) {
	return w.promptText(externalID, PromptsFolder, ArticlePromptFile)
}

// DemoArticlePromptText читает промпт статьи из каталога DEMO.
//
// DEMO существует для ручного прогона: этот файл открывают и отправляют в чат руками. Он
// пишется даже тогда, когда стадия article не удалась, — промпт рендерится до обращения к
// модели, и именно поэтому его есть смысл выгружать после неудачной генерации.
//
// Внутри DEMO промпт лежит там же, где боевой, — в prompts/: путь отличается ровно одним
// каталогом DEMO, и второго правила именования у промпта статьи нет.
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
