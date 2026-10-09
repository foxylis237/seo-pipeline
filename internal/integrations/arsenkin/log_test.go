package arsenkin

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestLogRecordCarriesArticleStageAndStep(t *testing.T) {
	var logs bytes.Buffer
	service := New(Config{ArticleID: 46, ExternalID: "46"}, slog.New(slog.NewJSONHandler(&logs, nil)))

	service.log(context.Background(), slog.LevelInfo, "прогресс Wordstat", "wordstat_progress", "progress", 50)

	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"external_id": "46", "stage": "arsenkin", "step": "wordstat_progress"}
	for key, value := range want {
		if record[key] != value {
			t.Errorf("%s=%v, want %v", key, record[key], value)
		}
	}
	for _, key := range []string{"integration", "current_url", "duration_ms"} {
		if _, found := record[key]; found {
			t.Errorf("old key %s in %s", key, logs.String())
		}
	}
}
