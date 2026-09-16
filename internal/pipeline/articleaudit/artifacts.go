package articleaudit

import (
	"encoding/json"
	"fmt"
	"path"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/output"
)

// Раскладка артефактов одной проверки. По форме та же, что у остальных задач: каталог
// <индекс>-<слаг>, внутри исходник, промпт и результат.
//
// Сырой ответ модели хранится обязательно и отдельно от отчёта: по нему видно, что модель на
// самом деле сказала, когда разбор дал «Не разобрано». Поля записи хранятся по той же
// причине — по ним видно, что она вообще видела.
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
//
// Относительные, а не абсолютные: в базе задачи они лежат так же, как у остальных задач,
// иначе перенос каталога артефактов сделал бы записи в базе бессмысленными.
type Paths struct {
	OriginalPath string
	FieldsPath   string
	PromptPath   string
	AuditPath    string
	ResultPath   string
}

// Artifacts пишет файлы проверки в OUTPUT_DIR.
//
// Поверх output.Writer, а не своим os.WriteFile, как у задач правки: отчёт — единственный
// результат оплаченного прогона, и публиковаться он обязан вместе с записью в базу. Оборванная
// на середине запись оставила бы половину отчёта под именем готового, а в базе — путь,
// который на него указывает.
type Artifacts struct{ writer *output.Writer }

func NewArtifacts(root string) Artifacts { return Artifacts{writer: output.NewWriter(root)} }

// DirectoryName — каталог статьи относительно корня артефактов.
func DirectoryName(externalID, slug string) string { return externalID + "-" + slug }

// StageOriginal готовит копию прочитанной записи: текст страницы и её поля.
//
// Ложится на диск до обращения к модели — по образцу задач правки. Смысл тот же: то, что
// проверяют, обязано быть сохранено раньше, чем за проверку заплачено.
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

// StageReport готовит то, что осталось после модели: промпт, сырой ответ и отчёт.
func (a Artifacts) StageReport(externalID, slug, prompt, answer, report string) (
	*output.PendingArtifact, Paths, error) {
	paths := Paths{
		PromptPath: artifactPath(externalID, slug, PromptsFolder, PromptFile),
		AuditPath:  artifactPath(externalID, slug, GeneratedFolder, AuditFile),
		ResultPath: artifactPath(externalID, slug, ".", ResultFile),
	}
	pending, err := a.writer.StageFiles(
		output.File{Path: paths.PromptPath, Content: []byte(prompt)},
		output.File{Path: paths.AuditPath, Content: []byte(answer)},
		output.File{Path: paths.ResultPath, Content: []byte(report)},
	)
	if err != nil {
		return nil, Paths{}, fmt.Errorf("подготовить отчёт: %w", err)
	}
	return pending, paths, nil
}

// Read читает сохранённый артефакт по пути из базы.
func (a Artifacts) Read(relative string) (string, error) { return a.writer.Read(relative) }

// artifactPath собирает путь артефакта. Слэши, а не разделитель платформы: в базе пути лежат
// в одной форме независимо от того, где шёл прогон.
func artifactPath(externalID, slug, folder, name string) string {
	if folder == "." {
		return path.Join(DirectoryName(externalID, slug), name)
	}
	return path.Join(DirectoryName(externalID, slug), folder, name)
}
