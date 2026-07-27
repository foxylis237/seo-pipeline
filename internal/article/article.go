// Package article содержит модели SEO-пайплайна.
package article

import "time"

// Article представляет статью, сохранённую в PostgreSQL.
type Article struct {
	ID           int64
	ExternalID   string
	Title        string
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
