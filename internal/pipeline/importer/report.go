package importer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Summary struct {
	Viewed       int  `json:"viewed"`
	Imported     int  `json:"imported"`
	Existing     int  `json:"existing"`
	Invalid      int  `json:"invalid"`
	Empty        int  `json:"empty"`
	LimitReached bool `json:"limit_reached"`
}

type RowError struct {
	ExcelRow   int       `json:"excel_row"`
	ExternalID string    `json:"external_id,omitempty"`
	Title      string    `json:"title,omitempty"`
	Errors     []string  `json:"errors"`
	Time       time.Time `json:"time"`
}

type Report struct {
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt time.Time  `json:"finished_at"`
	InputFile  string     `json:"input_file"`
	Limit      int        `json:"limit"`
	Summary    Summary    `json:"summary"`
	Errors     []RowError `json:"errors"`
	FatalError string     `json:"fatal_error,omitempty"`
}

// SaveReport атомарно публикует исторический отчёт и latest.json.
func SaveReport(directory string, report Report) (string, error) {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("создать директорию отчётов импорта: %w", err)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("сформировать отчёт импорта: %w", err)
	}
	data = append(data, '\n')
	name := "import-" + report.StartedAt.UTC().Format("20060102T150405.000000000Z") + ".json"
	historyPath := filepath.Join(directory, name)
	if err := atomicWrite(historyPath, data); err != nil {
		return "", fmt.Errorf("сохранить исторический отчёт импорта: %w", err)
	}
	if err := atomicWrite(filepath.Join(directory, "latest.json"), data); err != nil {
		return historyPath, fmt.Errorf("сохранить актуальный отчёт импорта: %w", err)
	}
	return historyPath, nil
}

func atomicWrite(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".import-report-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o644); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}
