package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

type fakeCurrentErrorsRepository struct {
	records    []article.ArticleError
	saved      article.Article
	externalID string
}

func (r *fakeCurrentErrorsRepository) ListArticlesWithErrors(context.Context) ([]article.ArticleError, error) {
	return r.records, nil
}

func (r *fakeCurrentErrorsRepository) GetArticleByExternalID(_ context.Context, externalID string) (article.Article, error) {
	r.externalID = externalID
	return r.saved, nil
}

func TestRunErrorsFormatsCurrentErrors(t *testing.T) {
	message, step, operation, retryable := "request\ntimeout", "article_generation", "llm_article_generation", true
	errorTime := time.Date(2026, 8, 4, 9, 40, 12, 0, time.Local)
	repository := &fakeCurrentErrorsRepository{records: []article.ArticleError{{
		Article:   article.Article{ID: 9, ExternalID: "124", Title: "Как стать сварщиком", Status: "failed", CurrentStep: &step, ErrorMessage: &message},
		Operation: &operation, Retryable: &retryable, ErrorTime: &errorTime,
	}}}
	var output bytes.Buffer
	if err := runErrors(context.Background(), repository, "", &output); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"ARTICLE_ID", "9", "124", "Как стать сварщиком", "failed", step, operation, "true", "request timeout"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %q in output:\n%s", expected, output.String())
		}
	}
}

func TestRunErrorsSingleArticleWithoutError(t *testing.T) {
	repository := &fakeCurrentErrorsRepository{saved: article.Article{ExternalID: "57"}}
	var output bytes.Buffer
	if err := runErrors(context.Background(), repository, "57", &output); err != nil {
		t.Fatal(err)
	}
	if repository.externalID != "57" || !strings.Contains(output.String(), "has no recorded error") {
		t.Fatalf("externalID=%q output=%q", repository.externalID, output.String())
	}
}

func TestRunErrorsEmptyList(t *testing.T) {
	var output bytes.Buffer
	if err := runErrors(context.Background(), &fakeCurrentErrorsRepository{}, "", &output); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != "No failed articles found." {
		t.Fatalf("output=%q", output.String())
	}
}
