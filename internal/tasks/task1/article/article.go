// Package article содержит модели SEO-пайплайна.
package article

import "time"

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

type Input struct {
	ExcelID         int
	Title           string
	Header          string
	ImageSlug       string
	MetaDescription string
	Keyword         string
	ReferenceURL    string
	Category        string
	Author          string
	Links           string
	Professions     string
}

// KeywordFrequency содержит запрос и его частотность Wordstat.
type KeywordFrequency struct {
	Query     string `json:"query"`
	Frequency int    `json:"frequency"`
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
	Professions     string
	Author          string
	Keyword         string
	SEOTitle        string
	MetaDescription string
	Header          string
	ProfessionName  string
	ImageName       string
	ImageURL        string
	ArticlePath     string
	HTMLPath        string
}
