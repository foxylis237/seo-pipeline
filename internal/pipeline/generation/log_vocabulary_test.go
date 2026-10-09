package generation

import (
	"context"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/logtest"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
)

var logStages = []string{"structure", "article", "review", "fix", "info", "html", "result"}

func TestPipelineLogsFollowVocabulary(t *testing.T) {
	input := article.GenerationInput{
		Article:          article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema"},
		WordstatKeywords: []article.KeywordFrequency{{Query: "ключ", Frequency: 100}},
	}
	var recorder logtest.Recorder
	logger := recorder.Logger()
	pipeline := NewPipeline(&fakePipelineRepository{input: input}, testGenerationRouter(successfulPipelineClient(), logger),
		successfulChatFactory(), articleoutput.NewWriter(t.TempDir()), logger, newFakeResultBuilder(t, nil))

	if _, err := pipeline.RunByExternalID(context.Background(), "37"); err != nil {
		t.Fatal(err)
	}
	logtest.AssertVocabulary(t, recorder.Records(), logStages...)
}

func TestPipelineFailureLogsOperationAsErrorOperation(t *testing.T) {
	input := article.GenerationInput{Article: article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema"}}
	var recorder logtest.Recorder
	logger := recorder.Logger()
	pipeline := NewPipeline(&fakePipelineRepository{input: input}, testGenerationRouter(successfulPipelineClient(), logger),
		successfulChatFactory(), articleoutput.NewWriter(t.TempDir()), logger)

	if _, err := pipeline.RunHTMLByExternalID(context.Background(), "37"); err == nil {
		t.Fatal("want error without saved fixed article")
	}
	logtest.AssertVocabulary(t, recorder.Records(), logStages...)
	for _, record := range recorder.Records() {
		if record.Attr["error_operation"] != "" {
			return
		}
	}
	t.Fatalf("no failure record carries error_operation: %+v", recorder.Records())
}
