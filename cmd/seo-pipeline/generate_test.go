package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
)

func TestRunDemoWithoutRecordedErrorSkipsArticleWithoutChangingState(t *testing.T) {
	step := "arsenkin_collection"
	errorMessage := "Keys.so timeout"
	selected := article.Article{
		ID: 37, ExternalID: "57", Status: "failed",
		CurrentStep: &step, ErrorMessage: &errorMessage,
	}
	before := selected
	called := false
	var logs bytes.Buffer
	err := runDemoWithoutRecordedError(selected, slog.New(slog.NewTextHandler(&logs, nil)), func() error {
		called = true
		return nil
	})
	if err != nil || called {
		t.Fatalf("err = %v, pipeline called = %v", err, called)
	}
	if !reflect.DeepEqual(selected, before) {
		t.Fatalf("article state changed: got %+v, want %+v", selected, before)
	}
	for _, expected := range []string{"article skipped because it has a recorded error", "article_id=37", "external_id=57", `error_message="Keys.so timeout"`} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("log %q does not contain %q", logs.String(), expected)
		}
	}
}

func TestRunDemoWithoutRecordedErrorRunsArticleWithNoMeaningfulError(t *testing.T) {
	for _, errorMessage := range []*string{nil, stringPointer(""), stringPointer("   \t")} {
		called := false
		err := runDemoWithoutRecordedError(
			article.Article{ID: 1, ExternalID: "11", ErrorMessage: errorMessage},
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			func() error { called = true; return nil },
		)
		if err != nil || !called {
			t.Fatalf("error_message = %v: err = %v, pipeline called = %v", errorMessage, err, called)
		}
	}
}

func stringPointer(value string) *string { return &value }

type queuedArticles struct {
	articles []article.Article
	next     int
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

func (q *queuedArticles) ClaimNextIncomplete(context.Context) (article.Article, bool, error) {
	if q.next >= len(q.articles) {
		return article.Article{}, false, nil
	}
	selected := q.articles[q.next]
	q.next++
	return selected, true, nil
}

func TestRunAllDemoProcessesInOrder(t *testing.T) {
	repository := &queuedArticles{articles: []article.Article{
		{ID: 1, ExternalID: "11", Title: "Первая"},
		{ID: 2, ExternalID: "12", Title: "Вторая"},
	}}
	var calls []string
	err := runAllDemo(context.Background(), repository, func(_ context.Context, externalID string) error {
		calls = append(calls, externalID)
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"11", "12"}) {
		t.Fatalf("calls = %v", calls)
	}
}

func TestRunAllDemoStopsOnFirstError(t *testing.T) {
	repository := &queuedArticles{articles: []article.Article{
		{ID: 1, ExternalID: "11", Title: "Первая"},
		{ID: 2, ExternalID: "12", Title: "Вторая"},
	}}
	var calls []string
	wantErr := errors.New("generation failed")
	err := runAllDemo(context.Background(), repository, func(_ context.Context, externalID string) error {
		calls = append(calls, externalID)
		return wantErr
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, wantErr) || !reflect.DeepEqual(calls, []string{"11"}) {
		t.Fatalf("err = %v, calls = %v", err, calls)
	}
}
