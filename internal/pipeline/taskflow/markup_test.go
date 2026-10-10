package taskflow

import (
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/logtest"
)

const leadPage = "H1 - Профессия маляра\n\nМаляр готовит поверхности и наносит лакокрасочные материалы на объектах любой сложности.\n\nH2 - Обязанности\n\nТекст раздела."

func TestKeepLeadRestoresDroppedLead(t *testing.T) {
	var recorder logtest.Recorder
	got := KeepLead(recorder.Logger(), "html", leadPage, "<h2>Обязанности</h2><p>Текст раздела.</p>")
	if !strings.HasPrefix(got, "<p>Маляр готовит поверхности") {
		t.Fatalf("лид не вернулся первым абзацем: %s", got)
	}
	records := recorder.Records()
	if len(records) != 1 || records[0].Attr["stage"] != "html" {
		t.Fatalf("ждали одно предупреждение со stage=html, получили %+v", records)
	}
	logtest.AssertVocabulary(t, records, "html")
}

func TestKeepLeadLeavesMarkupWithLead(t *testing.T) {
	var recorder logtest.Recorder
	markup := "<p>Маляр готовит поверхности и наносит лакокрасочные материалы на объектах любой сложности.</p><h2>Обязанности</h2>"
	if got := KeepLead(recorder.Logger(), "html", leadPage, markup); got != markup {
		t.Fatalf("разметка с лидом изменена: %s", got)
	}
	if records := recorder.Records(); len(records) != 0 {
		t.Fatalf("лишние записи: %+v", records)
	}
}

func TestWarnLeftoverMarkersNamesMarkers(t *testing.T) {
	var recorder logtest.Recorder
	WarnLeftoverMarkers(recorder.Logger(), "html", "<p>ЗАМЕТКА: проверьте допуск.</p>")
	records := recorder.Records()
	if len(records) != 1 || records[0].Attr["markers"] != "ЗАМЕТКА:" {
		t.Fatalf("ждали предупреждение с markers=ЗАМЕТКА:, получили %+v", records)
	}
	logtest.AssertVocabulary(t, records, "html")
}

func TestWarnLeftoverMarkersQuietOnCleanMarkup(t *testing.T) {
	var recorder logtest.Recorder
	WarnLeftoverMarkers(recorder.Logger(), "html", "<p>Обычный абзац.</p>")
	if records := recorder.Records(); len(records) != 0 {
		t.Fatalf("лишние записи: %+v", records)
	}
}
