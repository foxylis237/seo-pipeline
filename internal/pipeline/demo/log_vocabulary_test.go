package demo

import (
	"context"
	"errors"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/logtest"
)

func TestBuildLogsFollowVocabulary(t *testing.T) {
	var recorder logtest.Recorder

	incomplete := newFixture(t)
	incomplete.builder.logger = recorder.Logger()
	incomplete.repository.researchErr = errors.New("research не собран")
	incomplete.preparer.err = errors.New("Keys.so недоступен")
	incomplete.repository.demoInput = article.GenerationInput{
		Article: article.Article{ID: 7, ExternalID: testExternalID, Slug: testSlug},
	}
	_ = incomplete.builder.Build(context.Background(), testExternalID)

	complete := newFixture(t)
	complete.builder.logger = recorder.Logger()
	complete.writeCompleteProduction(t)
	complete.generator.missing["info"] = true
	if err := complete.builder.Build(context.Background(), testExternalID); err != nil {
		t.Fatalf("Build() = %v", err)
	}

	logtest.AssertVocabulary(t, recorder.Records(), "prepare", "structure", "info")
}
