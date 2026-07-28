// Package prompts собирает промпты из встроенных текстовых шаблонов.
package prompts

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed templates/article.txt.tmpl
var articlePromptTemplate string

//go:embed templates/structure_prompt.txt.tmpl
var structurePromptTemplate string

type StructurePromptData struct {
	Title     string
	Structure string
}

// ArticlePromptData содержит значения для шаблона статьи.
type ArticlePromptData struct {
	Title              string
	Keywords           string
	LSIWords           string
	GeneratedStructure string
}

func BuildStructurePrompt(data StructurePromptData) (string, error) {
	return buildPrompt("structure", structurePromptTemplate, data.Title, data.Structure, data)
}

// BuildArticlePrompt собирает промпт одной статьи.
func BuildArticlePrompt(data ArticlePromptData) (string, error) {
	return buildPrompt("article", articlePromptTemplate, data.Title, data.GeneratedStructure, data)
}

func buildPrompt(name, promptTemplate, title, structure string, data any) (string, error) {
	if strings.TrimSpace(title) == "" {
		return "", fmt.Errorf("%s title is empty", name)
	}
	if strings.TrimSpace(structure) == "" {
		return "", fmt.Errorf("%s structure is empty", name)
	}
	tmpl, err := template.New(name).Parse(promptTemplate)
	if err != nil {
		return "", fmt.Errorf("parse %s prompt template: %w", name, err)
	}
	var buffer bytes.Buffer
	if err := tmpl.Execute(&buffer, data); err != nil {
		return "", fmt.Errorf("execute %s prompt template: %w", name, err)
	}
	return buffer.String(), nil
}
