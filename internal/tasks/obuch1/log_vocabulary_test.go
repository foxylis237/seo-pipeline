package obuch1

import (
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/logtest"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
)

func TestFlowLogsFollowVocabulary(t *testing.T) {
	flow, chats, repository, publisher := newFlowFixture(t)
	var recorder logtest.Recorder
	flow.Base = taskflow.NewBase(repository, flow.writer, chats, &fakeRenderer{}, recorder.Logger(), publisher)

	runToHTML(t, flow, repository)
	logtest.AssertVocabulary(t, recorder.Records(),
		StageStructure, StageArticle, StageExpert, StageReview, StageInfo, StageHTML)
}
