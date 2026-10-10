// Package logtest captures slog records in tests and checks them against the log vocabulary.
package logtest

import (
	"context"
	"log/slog"
	"testing"
	"unicode"
	"unicode/utf8"
)

// Record is one captured log record with its attributes rendered as strings.
type Record struct {
	Msg  string
	Attr map[string]string
}

// Recorder collects records written through its logger.
type Recorder struct {
	records []Record
}

// Logger returns a logger that writes into the recorder.
func (r *Recorder) Logger() *slog.Logger {
	return slog.New(handler{recorder: r})
}

// Records returns everything captured so far.
func (r *Recorder) Records() []Record { return r.records }

type handler struct {
	attrs    []slog.Attr
	recorder *Recorder
}

func (h handler) Enabled(context.Context, slog.Level) bool { return true }

func (h handler) Handle(_ context.Context, record slog.Record) error {
	captured := Record{Msg: record.Message, Attr: map[string]string{}}
	for _, attr := range h.attrs {
		captured.Attr[attr.Key] = attr.Value.String()
	}
	record.Attrs(func(attr slog.Attr) bool {
		captured.Attr[attr.Key] = attr.Value.String()
		return true
	})
	h.recorder.records = append(h.recorder.records, captured)
	return nil
}

func (h handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return h
}

func (h handler) WithGroup(string) slog.Handler { return h }

// retiredKeys maps a key the vocabulary replaced to its replacement.
var retiredKeys = map[string]string{
	"result_path":      "path",
	"structure_path":   "path",
	"prompt_path":      "path",
	"demo_path":        "path",
	"file":             "path",
	"document_url":     "url",
	"folder_url":       "url",
	"old_status":       "status_before",
	"old_current_step": "current_step_before",
}

// AssertVocabulary fails the test when a record names a non-stage in stage, uses a retired
// key or carries an English message.
func AssertVocabulary(t testing.TB, records []Record, stages ...string) {
	t.Helper()
	if len(records) == 0 {
		t.Fatal("no log records captured")
	}
	allowed := make(map[string]bool, len(stages))
	for _, stage := range stages {
		allowed[stage] = true
	}
	for _, record := range records {
		if stage, ok := record.Attr["stage"]; ok && !allowed[stage] {
			t.Errorf("record %q: stage=%q is not a stage name", record.Msg, stage)
		}
		for key, want := range retiredKeys {
			if _, ok := record.Attr[key]; ok {
				t.Errorf("record %q: key %q, want %s", record.Msg, key, want)
			}
		}
		if first, _ := utf8.DecodeRuneInString(record.Msg); first < utf8.RuneSelf && unicode.IsLetter(first) {
			t.Errorf("record %q: message is not in Russian", record.Msg)
		}
	}
}
