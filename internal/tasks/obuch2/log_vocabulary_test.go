package obuch2

import (
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/logtest"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
)

func TestFlowLogsFollowVocabulary(t *testing.T) {
	flow, chats, repository, publisher, writer := newFlowFixture(t)
	var recorder logtest.Recorder
	flow.Base = taskflow.NewBase(repository, flow.writer, chats, &fakeRenderer{}, recorder.Logger(), publisher)

	if _, err := runToHTML(t, flow, repository, writer); err != nil {
		t.Fatalf("html: %v", err)
	}
	logtest.AssertVocabulary(t, recorder.Records(), StageStructure, StageArticle, StageReview, StageHTML)
}
