package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// Пакетная demo-сборка обходит выборку GetAll: статус и сохранённая ошибка не исключают
// статью, а падение одной не прекращает обход — иначе одна сломанная статья лишила бы
// материалов все следующие.
func TestRunSelectedArticlesCoversEveryStateAndSurvivesFailure(t *testing.T) {
	errorMessage := "Keys.so timeout"
	selected := []article.Article{
		{ID: 1, ExternalID: "11", Status: "completed"},
		{ID: 2, ExternalID: "12", Status: "failed", ErrorMessage: &errorMessage},
		{ID: 3, ExternalID: "13", Status: "pending"},
	}
	before := append([]article.Article(nil), selected...)
	wantErr := errors.New("demo failed")
	var calls []string
	err := runSelectedArticles(context.Background(), selected, "demo-generate", func(_ context.Context, externalID string) error {
		calls = append(calls, externalID)
		if externalID == "12" {
			return wantErr
		}
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !reflect.DeepEqual(calls, []string{"11", "12", "13"}) {
		t.Fatalf("calls = %v, want all three articles", calls)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("batch error = %v, want wrapped %v", err, wantErr)
	}
	if !reflect.DeepEqual(selected, before) {
		t.Fatalf("article state changed: got %+v, want %+v", selected, before)
	}
}

type pendingArticles struct {
	operation string
	articles  []article.Article
	err       error
}

func (r *pendingArticles) GetPendingForOperation(_ context.Context, operation string) ([]article.Article, error) {
	r.operation = operation
	return r.articles, r.err
}

func TestRunBatchOperationProcessesStableSelectionAndContinuesAfterError(t *testing.T) {
	repository := &pendingArticles{articles: []article.Article{
		{ID: 1, ExternalID: "11", Status: "processing"},
		{ID: 2, ExternalID: "12", Status: "failed"},
		{ID: 3, ExternalID: "13", Status: "processing"},
	}}
	wantErr := errors.New("article failed")
	var calls []string
	err := runBatchOperation(context.Background(), repository, "generate", func(_ context.Context, externalID string) error {
		calls = append(calls, externalID)
		if externalID == "12" {
			return wantErr
		}
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if repository.operation != "generate" || !reflect.DeepEqual(calls, []string{"11", "12", "13"}) {
		t.Fatalf("operation = %q, calls = %v", repository.operation, calls)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("batch error = %v, want wrapped %v", err, wantErr)
	}
}

func TestRunBatchOperationReturnsSelectionErrorBeforeProcessing(t *testing.T) {
	wantErr := errors.New("postgres unavailable")
	repository := &pendingArticles{err: wantErr}
	called := false
	err := runBatchOperation(context.Background(), repository, "html", func(context.Context, string) error {
		called = true
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, wantErr) || called {
		t.Fatalf("error = %v, called = %v", err, called)
	}
}
