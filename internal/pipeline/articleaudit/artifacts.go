package articleaudit

import (
	"encoding/json"
	"fmt"
	"path"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/output"
)

// Раскладка артефактов одной проверки: каталог <индекс>-<слаг>, внутри исходник, промпт и результат.
// Сырой ответ модели хранится отдельно от отчёта — по нему видно, что она сказала, когда разбор дал «Не разобрано».
const (
	OriginalFolder     = "original"
	PromptsFolder      = "prompts"
	GeneratedFolder    = "generated"
	OriginalHTMLFile   = "article.html"
	OriginalFieldsFile = "fields.json"
	PromptFile         = "audit_prompt.txt"
	AuditFile          = "audit.txt"
	ResultFile         = "result.md"
)

// Paths — пути артефактов проверки относительно OUTPUT_DIR.
type Paths struct {
	OriginalPath string
	FieldsPath   string
	PromptPath   string
	AuditPath    string
	ResultPath   string
}

// Artifacts пишет файлы проверки в OUTPUT_DIR через output.Writer: отчёт публикуется вместе с записью в базу.
type Artifacts struct{ writer *output.Writer }

func NewArtifacts(root string) Artifacts { return Artifacts{writer: output.NewWriter(root)} }

// DirectoryName — каталог статьи относительно корня артефактов.
func DirectoryName(externalID, slug string) string { return externalID + "-" + slug }

// StageOriginal готовит копию прочитанной записи: текст страницы и её поля.
func (a Artifacts) StageOriginal(externalID, slug, html string, fields map[string]string) (
	*output.PendingArtifact, Paths, error) {
	encoded, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return nil, Paths{}, fmt.Errorf("сохранить поля записи: %w", err)
	}
	paths := Paths{
		OriginalPath: artifactPath(externalID, slug, OriginalFolder, OriginalHTMLFile),
		FieldsPath:   artifactPath(externalID, slug, OriginalFolder, OriginalFieldsFile),
	}
	pending, err := a.writer.StageFiles(
		output.File{Path: paths.OriginalPath, Content: []byte(html)},
		output.File{Path: paths.FieldsPath, Content: encoded},
	)
	if err != nil {
		return nil, Paths{}, fmt.Errorf("подготовить копию записи: %w", err)
	}
	return pending, paths, nil
}

// StageAnswer готовит промпт и сырой ответ модели.
func (a Artifacts) StageAnswer(externalID, slug, prompt, answer string) (
	*output.PendingArtifact, Paths, error) {
	paths := Paths{
		PromptPath: artifactPath(externalID, slug, PromptsFolder, PromptFile),
		AuditPath:  artifactPath(externalID, slug, GeneratedFolder, AuditFile),
	}
	pending, err := a.writer.StageFiles(
		output.File{Path: paths.PromptPath, Content: []byte(prompt)},
		output.File{Path: paths.AuditPath, Content: []byte(answer)},
	)
	if err != nil {
		return nil, Paths{}, fmt.Errorf("подготовить ответ модели: %w", err)
	}
	return pending, paths, nil
}

// StageReport готовит отчёт и возвращает его путь.
func (a Artifacts) StageReport(externalID, slug, report string) (*output.PendingArtifact, string, error) {
	resultPath := artifactPath(externalID, slug, ".", ResultFile)
	pending, err := a.writer.StageFiles(output.File{Path: resultPath, Content: []byte(report)})
	if err != nil {
		return nil, "", fmt.Errorf("подготовить отчёт: %w", err)
	}
	return pending, resultPath, nil
}

// Read читает сохранённый артефакт по пути из базы.
func (a Artifacts) Read(relative string) (string, error) { return a.writer.Read(relative) }

// artifactPath собирает путь через path, а не filepath: в базе пути лежат в одной форме на любой ОС.
func artifactPath(externalID, slug, folder, name string) string {
	if folder == "." {
		return path.Join(DirectoryName(externalID, slug), name)
	}
	return path.Join(DirectoryName(externalID, slug), folder, name)
}
