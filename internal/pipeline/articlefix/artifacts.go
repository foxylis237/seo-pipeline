package articlefix

import (
	"fmt"
	"os"
	"path/filepath"
)

// Раскладка артефактов одной статьи. Та же по смыслу, что у остальных задач: каталог
// <индекс>-<слаг>, внутри промпт, исходник и результат. Отличие одно и оно содержательное —
// у этой задачи есть «до»: старый текст и старый заголовок, которых больше нигде не взять,
// потому что в блоге их заменит наш же ответ.
const (
	OriginalFolder    = "original"
	PromptsFolder     = "prompts"
	GeneratedFolder   = "generated"
	OriginalHTMLFile  = "article.html"
	OriginalTitleFile = "title.txt"
	PromptFile        = "rewrite_prompt.txt"
	RewrittenHTMLFile = "article.html"
	ResultFile        = "result.md"
)

// Artifacts пишет файлы статьи в OUTPUT_DIR.
type Artifacts struct{ root string }

func NewArtifacts(root string) Artifacts { return Artifacts{root: root} }

// DirectoryName — каталог статьи относительно корня артефактов.
func DirectoryName(externalID, slug string) string { return externalID + "-" + slug }

// Save записывает файл статьи и возвращает путь относительно OUTPUT_DIR.
//
// Относительный путь, а не абсолютный: в базе задачи пути хранятся так же, как у остальных
// задач, иначе перенос каталога артефактов сделал бы записи в базе бессмысленными.
func (a Artifacts) Save(externalID, slug, folder, name, content string) (string, error) {
	relative := filepath.Join(DirectoryName(externalID, slug), folder, name)
	full := filepath.Join(a.root, relative)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("создать каталог артефактов %q: %w", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("записать %q: %w", relative, err)
	}
	return relative, nil
}

// Read читает сохранённый артефакт по пути из базы.
func (a Artifacts) Read(relative string) (string, error) {
	content, err := os.ReadFile(filepath.Join(a.root, relative))
	if err != nil {
		return "", fmt.Errorf("прочитать %q: %w", relative, err)
	}
	return string(content), nil
}
