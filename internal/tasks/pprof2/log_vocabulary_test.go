package pprof2

import (
	"context"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/logtest"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
)

func TestFlowLogsFollowVocabulary(t *testing.T) {
	flow, chats, repository, publisher, _ := newFlowFixture(t)
	var recorder logtest.Recorder
	flow.Base = taskflow.NewBase(repository, flow.writer, chats, &fakeRenderer{}, recorder.Logger(), publisher)
	ctx := context.Background()

	if err := flow.RunStructure(ctx, "7"); err != nil {
		t.Fatalf("structure: %v", err)
	}
	if err := flow.RunArticle(ctx, "7"); err != nil {
		t.Fatalf("article: %v", err)
	}
	repository.saved.FixedArticlePath = repository.finalArticlePath
	if err := flow.RunHTML(ctx, "7"); err != nil {
		t.Fatalf("html: %v", err)
	}
	logtest.AssertVocabulary(t, recorder.Records(), StageStructure, StageArticle, StageReview, StageHTML)
}
