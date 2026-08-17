package validator

import (
	"context"
	"fmt"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// ArticleReader provides generated article text without coupling validation to filesystem details.
type ArticleReader interface {
	Read(relativePath string) (string, error)
}

// Request contains all persisted inputs required for deterministic article validation.
type Request struct {
	ArticleID         int64
	ExternalID        string
	ArticlePath       string
	ExpectedStructure string
	Keywords          []article.KeywordFrequency
	LSIWords          []string
}

// Result contains both the machine-readable report and its terminal representation.
type Result struct {
	Report          Report
	FormattedReport string
	ErrorCount      int
}

// Service owns the complete programmatic article-validation stage.
type Service struct {
	reader ArticleReader
}

func NewService(reader ArticleReader) *Service {
	return &Service{reader: reader}
}

func (s *Service) Validate(ctx context.Context, request Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	text, err := s.reader.Read(request.ArticlePath)
	if err != nil {
		return Result{}, fmt.Errorf("прочитать статью для проверки: %w", err)
	}
	keywords := make([]string, 0, len(request.Keywords))
	for _, keyword := range request.Keywords {
		keywords = append(keywords, keyword.Query)
	}
	requireFAQ, requireTable := validationRequirements(request.ExpectedStructure)
	report := Validate(Input{
		Article:           text,
		ExpectedStructure: request.ExpectedStructure,
		Keywords:          keywords,
		LSIWords:          request.LSIWords,
		RequireFAQ:        requireFAQ,
		RequireTable:      requireTable,
	})
	errorCount := 0
	for _, issue := range report.Issues {
		if issue.Severity == SeverityError {
			errorCount++
		}
	}
	return Result{
		Report:          report,
		FormattedReport: FormatReport(request.ArticleID, request.ExternalID, report),
		ErrorCount:      errorCount,
	}, nil
}

func validationRequirements(structure string) (requireFAQ, requireTable bool) {
	normalized := strings.ToLower(structure)
	requireFAQ = strings.Contains(normalized, "faq") || strings.Contains(normalized, "частые вопросы") || strings.Contains(normalized, "вопросы и ответы")
	requireTable = strings.Contains(normalized, "таблиц")
	return requireFAQ, requireTable
}
