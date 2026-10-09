package generation

import (
	"context"
	"log/slog"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
)

type capturedRecord struct {
	msg  string
	attr map[string]string
}

type captureHandler struct {
	attrs   []slog.Attr
	records *[]capturedRecord
}

func (h captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h captureHandler) Handle(_ context.Context, record slog.Record) error {
	captured := capturedRecord{msg: record.Message, attr: map[string]string{}}
	for _, attr := range h.attrs {
		captured.attr[attr.Key] = attr.Value.String()
	}
	record.Attrs(func(attr slog.Attr) bool {
		captured.attr[attr.Key] = attr.Value.String()
		return true
	})
	*h.records = append(*h.records, captured)
	return nil
}

func (h captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return h
}

func (h captureHandler) WithGroup(string) slog.Handler { return h }

var logStageNames = map[string]bool{
	"structure": true, "article": true, "review": true, "fix": true, "info": true, "html": true, "result": true,
}

func assertLogVocabulary(t *testing.T, records []capturedRecord) {
	t.Helper()
	if len(records) == 0 {
		t.Fatal("no log records captured")
	}
	for _, record := range records {
		if stage, ok := record.attr["stage"]; ok && !logStageNames[stage] {
			t.Errorf("record %q: stage=%q is not a stage name", record.msg, stage)
		}
		for _, key := range []string{"result_path", "structure_path"} {
			if _, ok := record.attr[key]; ok {
				t.Errorf("record %q: key %q, want path", record.msg, key)
			}
		}
		if first, _ := utf8.DecodeRuneInString(record.msg); first < utf8.RuneSelf && unicode.IsLetter(first) {
			t.Errorf("record %q: message is not in Russian", record.msg)
		}
	}
}

func TestPipelineLogsFollowVocabulary(t *testing.T) {
	input := article.GenerationInput{
		Article:          article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema"},
		WordstatKeywords: []article.KeywordFrequency{{Query: "ключ", Frequency: 100}},
	}
	var records []capturedRecord
	logger := slog.New(captureHandler{records: &records})
	pipeline := NewPipeline(&fakePipelineRepository{input: input}, testGenerationRouter(successfulPipelineClient(), logger),
		successfulChatFactory(), articleoutput.NewWriter(t.TempDir()), logger, newFakeResultBuilder(t, nil))

	if _, err := pipeline.RunByExternalID(context.Background(), "37"); err != nil {
		t.Fatal(err)
	}
	assertLogVocabulary(t, records)
}

func TestPipelineFailureLogsOperationAsErrorOperation(t *testing.T) {
	input := article.GenerationInput{Article: article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema"}}
	var records []capturedRecord
	logger := slog.New(captureHandler{records: &records})
	pipeline := NewPipeline(&fakePipelineRepository{input: input}, testGenerationRouter(successfulPipelineClient(), logger),
		successfulChatFactory(), articleoutput.NewWriter(t.TempDir()), logger)

	if _, err := pipeline.RunHTMLByExternalID(context.Background(), "37"); err == nil {
		t.Fatal("want error without saved fixed article")
	}
	assertLogVocabulary(t, records)
	for _, record := range records {
		if record.attr["error_operation"] != "" {
			return
		}
	}
	t.Fatalf("no failure record carries error_operation: %+v", records)
}
