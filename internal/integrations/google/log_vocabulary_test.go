package google

import (
	"errors"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/logtest"
)

func TestObserverLogsFollowVocabulary(t *testing.T) {
	var recorder logtest.Recorder
	observer := SlogObserver{Logger: recorder.Logger()}
	job := Job{ArticleID: 9, ExternalID: "45"}

	observer.Succeeded(job, Result{Created: true, DocumentURL: "https://docs.google.com/document/d/new/edit"}, 1, time.Second)
	observer.Failed(job, 2, time.Second, true,
		&StageError{Stage: "create_document", Retryable: true, Err: errors.New("таймаут")})

	records := recorder.Records()
	logtest.AssertVocabulary(t, records, "google_publish")
	if got := records[1].Attr["step"]; got != "create_document" {
		t.Errorf("failed attempt step = %q, want create_document", got)
	}
}
