package obuch2

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/generation"
)

// ExportPromptPath — шаблон выгружаемого промпта страницы.
//
// Выгружаемый промпт — документ для человека: он ложится в prompts/article_prompt.txt и уходит
// в Google Docs, где по нему пишут страницу руками в чате. Форму ему задал владелец
// (29.09.2026) — свой текст и пять мест входных данных, структура одними заголовками. Модель
// в прогоне получает прежний article.txt: менять генерацию ради вида документа нельзя.
const ExportPromptPath = "tasks/obuch_2/templates/article_prompt_export.txt"

// exportPromptData — поля шаблона выгружаемого промпта.
type exportPromptData struct {
	Title     string
	Keywords  string
	LSIWords  string
	Structure string
	Facts     string
}

// RenderExportPrompt собирает выгружаемый промпт страницы по шаблону из templatePath.
func RenderExportPrompt(templatePath string, input article.GenerationInput, structure string) (string, error) {
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		return "", fmt.Errorf("read export prompt template: %w", err)
	}
	tmpl, err := template.New("export").Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return "", fmt.Errorf("parse export prompt template: %w", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, exportPromptData{
		Title:     input.Article.Title,
		Keywords:  article.FormatKeywords(input.WordstatKeywords),
		LSIWords:  strings.Join(input.LSIWords, "\n"),
		Structure: generation.HeadingsOnly(structure),
		Facts:     exportFacts(input),
	}); err != nil {
		return "", fmt.Errorf("render export prompt: %w", err)
	}
	return out.String(), nil
}

// exportFacts — факты об услуге из колонок книги, по строке на заполненную колонку. Пустая
// колонка строкой не становится: шаблон велит не выдумывать того, чего во входных данных нет.
func exportFacts(input article.GenerationInput) string {
	var lines []string
	for _, fact := range []struct{ label, value string }{
		{"Профессия", input.Profession},
		{"Объём программы", input.Hours},
		{"Срок обучения", input.Duration},
		{"Стоимость", input.Price},
		{"Документ по итогам", input.Document},
		{"Итоговая аттестация", input.Attestation},
	} {
		if value := strings.TrimSpace(fact.value); value != "" {
			lines = append(lines, fact.label+": "+value)
		}
	}
	return strings.Join(lines, "\n")
}
