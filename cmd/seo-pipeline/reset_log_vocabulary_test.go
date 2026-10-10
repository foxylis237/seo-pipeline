package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/logtest"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/repository"
)

func TestResetAndClearLogsFollowVocabulary(t *testing.T) {
	var recorder logtest.Recorder
	var out bytes.Buffer

	if err := runClear(context.Background(), newClearRepository(), newClearWriter(),
		newClearOptions(&out, "clear\n", true), recorder.Logger(), "23"); err != nil {
		t.Fatalf("runClear: %v", err)
	}
	if err := runClear(context.Background(), newClearRepository(), newClearWriter(),
		newClearOptions(&out, "нет\n", true), recorder.Logger(), "23"); err != nil {
		t.Fatalf("runClear без подтверждения: %v", err)
	}
	if err := runResetArticle(context.Background(), newResetArticleRepository(), newClearWriter(),
		newResetArticleOptions(&out, "reset\n", true), recorder.Logger(), "23"); err != nil {
		t.Fatalf("runResetArticle: %v", err)
	}
	newResetProject(t)
	resetRepository := &fakeResetRepository{counts: []repository.ResetCount{{Table: "articles", Rows: 24}}}
	options := newResetOptions(&strings.Builder{}, strings.NewReader("reset\n"), true)
	if err := runReset(context.Background(), resetRepository, options, recorder.Logger()); err != nil {
		t.Fatalf("runReset: %v", err)
	}

	logtest.AssertVocabulary(t, recorder.Records())
}
