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

// ArticlePromptData содержит значения для шаблона статьи.
type ArticlePromptData struct {
	Title     string
	Keywords  string
	LSIWords  string
	Structure string
}

// BuildArticlePrompt собирает промпт одной статьи.
func BuildArticlePrompt(data ArticlePromptData) (string, error) {
	if strings.TrimSpace(data.Title) == "" {
		return "", fmt.Errorf("article title is empty")
	}
	if strings.TrimSpace(data.Structure) == "" {
		return "", fmt.Errorf("article structure is empty")
	}

	tmpl, err := template.New("article").Parse(articlePromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parse article prompt template: %w", err)
	}
	var buffer bytes.Buffer
	if err := tmpl.Execute(&buffer, data); err != nil {
		return "", fmt.Errorf("execute article prompt template: %w", err)
	}
	return buffer.String(), nil
}
