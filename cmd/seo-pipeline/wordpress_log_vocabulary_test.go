package main

import (
	"context"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/logtest"
)

func TestWordPressLogsFollowVocabulary(t *testing.T) {
	var recorder logtest.Recorder
	deps, repository, client, _, _ := newWPPublishDeps()
	deps.logger = recorder.Logger()

	if err := runWordPressPublishPlan(context.Background(), deps, "16"); err != nil {
		t.Fatalf("сухой прогон: %v", err)
	}
	if err := runWordPressPublish(context.Background(), deps, "16"); err != nil {
		t.Fatalf("публикация: %v", err)
	}
	client.stored = wordpress.StoredPost{ID: 21593, Link: "https://example.test/blog/razryady/"}
	if err := runWordPressMarkPublished(context.Background(), deps, "17", 21593); err != nil {
		t.Fatalf("привязка: %v", err)
	}
	repository.publishable = []string{"16"}
	deps.assumeYes, deps.interactive, deps.in = false, true, strings.NewReader("нет\n")
	if err := runWordPressPublishAll(context.Background(), deps, func(context.Context, string) error { return nil }); err != nil {
		t.Fatalf("отказ подтверждения: %v", err)
	}

	logtest.AssertVocabulary(t, recorder.Records(), "wordpress_publish")
}
