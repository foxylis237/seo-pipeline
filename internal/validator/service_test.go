package validator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/article"
)

type fakeArticleReader struct {
	text string
	err  error
}

func (r fakeArticleReader) Read(string) (string, error) { return r.text, r.err }

func TestServiceValidateBuildsCompleteResult(t *testing.T) {
	service := NewService(fakeArticleReader{text: "H1 - Тема\nH2 - Раздел\nКороткий текст"})
	result, err := service.Validate(context.Background(), Request{
		ArticleID: 7, ExternalID: "37", ArticlePath: "37-tema/generated/article.txt",
		ExpectedStructure: "H1 - Тема\nH2 - Раздел", Keywords: []article.KeywordFrequency{{Query: "ключ"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCount == 0 {
		t.Fatal("expected validation errors for a short article")
	}
	if !strings.Contains(result.FormattedReport, "article_id=7 external_id=37") {
		t.Fatalf("formatted report = %q", result.FormattedReport)
	}
}

func TestServiceValidateReturnsReadError(t *testing.T) {
	service := NewService(fakeArticleReader{err: errors.New("read failed")})
	_, err := service.Validate(context.Background(), Request{ArticlePath: "article.txt"})
	if err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("error = %v", err)
	}
}
