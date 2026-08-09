// Package article содержит модели SEO-пайплайна.
package article

import (
	"fmt"
	"strings"
	"time"
)

// Article представляет статью, сохранённую в PostgreSQL.
type Article struct {
	ID           int64
	ExternalID   string
	Title        string
	Slug         string
	ReferenceURL string
	Status       string
	CurrentStep  *string
	ErrorMessage *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ErrorRecord is one immutable processing failure stored for an article.
type ErrorRecord struct {
	ID           int64
	ArticleID    int64
	ExternalID   string
	ArticleTitle string
	Step         *string
	Operation    *string
	ErrorMessage string
	Retryable    bool
	CreatedAt    time.Time
}

// ArticleError describes the current blocking error and, when available, its latest history entry.
type ArticleError struct {
	Article
	Operation *string
	Retryable *bool
	ErrorTime *time.Time
}

// Input is one imported Excel row. The tags are what prepare diagnostics writes to
// prepare/input.json; nothing decodes this type from JSON.
type Input struct {
	ExcelID         int    `json:"excel_id"`
	Title           string `json:"title"`
	Header          string `json:"header"`
	ImageSlug       string `json:"image_slug"`
	MetaDescription string `json:"meta_description"`
	Keyword         string `json:"key_word"`
	ReferenceURL    string `json:"reference_url"`
	Category        string `json:"category"`
	Author          string `json:"author"`
	Links           string `json:"links"`
	Professions     string `json:"professions"`
}

// ImportedArticle связывает статью с сохранённой при импорте строкой Excel.
// HasInput отличает отсутствующую строку article_inputs от строки с пустыми полями.
type ImportedArticle struct {
	Article  Article
	Input    Input
	HasInput bool
}

// KeywordFrequency содержит запрос и его частотность Wordstat.
type KeywordFrequency struct {
	Query     string `json:"query"`
	Frequency int    `json:"frequency"`
}

// FormatKeywords готовит ключи для подстановки в промпт: запрос, табуляция, частотность.
// Формат принадлежит модели, потому что его подставляют и боевые стадии, и demo-сборка.
func FormatKeywords(keywords []KeywordFrequency) string {
	var result strings.Builder
	for index, keyword := range keywords {
		if index > 0 {
			result.WriteByte('\n')
		}
		fmt.Fprintf(&result, "%s\t%d", keyword.Query, keyword.Frequency)
	}
	return result.String()
}

// GenerationInput contains persisted research required by implemented generation stages.
type GenerationInput struct {
	Article             Article
	CompetitorStructure string
	WordstatKeywords    []KeywordFrequency
	LSIWords            []string
	Professions         string
	Links               string
}

// SavedGenerationInput contains persisted artifacts required to resume one LLM stage.
type SavedGenerationInput struct {
	Article          Article
	Professions      string
	Links            string
	StructurePath    string
	ArticlePath      string
	ReviewPath       string
	FixedArticlePath string
}

// ResultInput contains persisted fields used to assemble result.md.
type ResultInput struct {
	Article         Article
	Category        string
	Tags            string
	TLDR            string
	FAQ             string
	AdditionalInfo  string
	Professions     string
	Author          string
	Keyword         string
	MetaDescription string
	Header          string
	ArticlePath     string
	HTMLPath        string
}
