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

type queuedArticles struct {
	articles []article.Article
	next     int
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
