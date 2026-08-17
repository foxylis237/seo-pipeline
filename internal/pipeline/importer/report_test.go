package importer

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveReportWritesHistoryAndLatest(t *testing.T) {
	root := t.TempDir()
	report := Report{
		StartedAt:  time.Date(2026, 8, 3, 12, 30, 45, 123, time.UTC),
		FinishedAt: time.Date(2026, 8, 3, 12, 30, 46, 0, time.UTC),
		InputFile:  "input.xlsx",
		Limit:      10,
		Summary:    Summary{Viewed: 4, Imported: 2, Existing: 1, Invalid: 1},
		Errors: []RowError{{
			ExcelRow: 4, ExternalID: "bad", Title: "Статья",
			Errors: []string{`поле "id": должно быть положительным целым числом`},
			Time:   reportTime(),
		}},
	}
	directory := filepath.Join(root, "task1", "import-reports")
	historyPath, err := SaveReport(directory, report)
	if err != nil {
		t.Fatal(err)
	}
	history, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := os.ReadFile(filepath.Join(directory, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(history, latest) {
		t.Fatal("latest.json отличается от исторического отчёта")
	}
	var decoded Report
	if err := json.Unmarshal(latest, &decoded); err != nil {
		t.Fatalf("latest.json невалиден: %v", err)
	}
	if decoded.Summary.Imported != 2 || len(decoded.Errors) != 1 {
		t.Fatalf("неполный отчёт: %+v", decoded)
	}
}

func reportTime() time.Time {
	return time.Date(2026, 8, 3, 12, 30, 45, 500, time.UTC)
}
