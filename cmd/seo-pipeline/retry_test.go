package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
)

type fakeRetryRepository struct {
	articles    []article.ArticleError
	saved       article.Article
	cleared     []int64
	runsAtClear []int
	savedErrors []int64
}

func (r *fakeRetryRepository) SaveError(_ context.Context, id int64, _ error) error {
	r.savedErrors = append(r.savedErrors, id)
	return nil
}

func (r *fakeRetryRepository) ListArticlesWithErrors(context.Context) ([]article.ArticleError, error) {
	return r.articles, nil
}
func (r *fakeRetryRepository) GetArticleByExternalID(context.Context, string) (article.Article, error) {
	return r.saved, nil
}
func (r *fakeRetryRepository) ClearArticleErrorForRetry(_ context.Context, id int64) (bool, error) {
	r.cleared = append(r.cleared, id)
	return true, nil
}

func TestRunRetryClearsSequentiallyAndContinuesAfterFailure(t *testing.T) {
	message := "old error"
	repository := &fakeRetryRepository{articles: []article.ArticleError{
		{Article: article.Article{ID: 1, ExternalID: "51", Status: "failed", ErrorMessage: &message}},
		{Article: article.Article{ID: 2, ExternalID: "52", Status: "failed", ErrorMessage: &message}},
	}}
	var runs []string
	err := runRetry(context.Background(), repository, "", func(_ context.Context, externalID string) error {
		runs = append(runs, externalID)
		repository.runsAtClear = append(repository.runsAtClear, len(repository.cleared))
		if externalID == "51" {
			return errors.New("new failure")
		}
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !reflect.DeepEqual(repository.cleared, []int64{1, 2}) || !reflect.DeepEqual(runs, []string{"51", "52"}) {
		t.Fatalf("err=%v cleared=%v runs=%v", err, repository.cleared, runs)
	}
	if !reflect.DeepEqual(repository.runsAtClear, []int{1, 2}) {
		t.Fatalf("runsAtClear=%v", repository.runsAtClear)
	}
}

func TestRunRetrySingleUsesExternalIDAndSkipsWithoutError(t *testing.T) {
	repository := &fakeRetryRepository{saved: article.Article{ID: 9, ExternalID: "57", Status: "failed"}}
	called := false
	if err := runRetry(context.Background(), repository, "57", func(context.Context, string) error { called = true; return nil }, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if called || len(repository.cleared) != 0 {
		t.Fatalf("pipeline called=%v cleared=%v", called, repository.cleared)
	}
}

func TestRunRetrySkipsCompletedArticleWithError(t *testing.T) {
	message := "inconsistent error"
	repository := &fakeRetryRepository{saved: article.Article{ID: 9, ExternalID: "57", Status: "completed", ErrorMessage: &message}}
	called := false
	if err := runRetry(context.Background(), repository, "57", func(context.Context, string) error { called = true; return nil }, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if called || len(repository.cleared) != 0 {
		t.Fatalf("pipeline called=%v cleared=%v", called, repository.cleared)
	}
}
