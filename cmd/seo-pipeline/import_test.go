package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/importer"
	"github.com/xuri/excelize/v2"
)

type fakeArticleImporter struct {
	calls    int
	existing map[int]article.Article
	errorAt  int
}

func (f *fakeArticleImporter) Import(_ context.Context, input article.Input) (article.Article, bool, error) {
	f.calls++
	if f.errorAt > 0 && f.calls == f.errorAt {
		return article.Article{}, false, errors.New("PostgreSQL недоступен")
	}
	if f.existing != nil {
		if existing, ok := f.existing[input.ExcelID]; ok {
			return existing, false, nil
		}
		created := article.Article{ID: int64(len(f.existing) + 1), ExternalID: strconv.Itoa(input.ExcelID), Title: input.Title}
		f.existing[input.ExcelID] = created
		return created, true, nil
	}
	created := f.calls == 1
	return article.Article{ID: int64(f.calls), ExternalID: strconv.Itoa(input.ExcelID), Title: input.Title}, created, nil
}

func TestRunImportHonorsLimitAndRepeatedImportDoesNotCreateDuplicates(t *testing.T) {
	temporary := t.TempDir()
	path := filepath.Join(temporary, "input.xlsx")
	workbook := excelize.NewFile()
	defer func() { _ = workbook.Close() }()
	sheet := workbook.GetSheetName(0)
	for rowIndex, row := range [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{1, "Первая", "one", "https://example.test/1"},
		{2, "Вторая", "two", "https://example.test/2"},
		{3, "Третья", "three", "https://example.test/3"},
		{4, "Четвёртая", "four", "https://example.test/4"},
	} {
		cell, _ := excelize.CoordinatesToCellName(1, rowIndex+1)
		if err := workbook.SetSheetRow(sheet, cell, &row); err != nil {
			t.Fatal(err)
		}
	}
	if err := workbook.SaveAs(path); err != nil {
		t.Fatal(err)
	}

	repository := &fakeArticleImporter{existing: make(map[int]article.Article)}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	reportDirectory := filepath.Join(temporary, "task1", "import-reports")
	if err := runImport(context.Background(), repository, path, reportDirectory, 2, logger); err != nil {
		t.Fatal(err)
	}
	if err := runImport(context.Background(), repository, path, reportDirectory, 2, logger); err != nil {
		t.Fatal(err)
	}
	if len(repository.existing) != 4 {
		t.Fatalf("после возобновлённого импорта статей = %d, ожидалось 4", len(repository.existing))
	}
}

func TestRunImportLogsFoundAddedAndSkipped(t *testing.T) {
	temporary := t.TempDir()
	path := filepath.Join(temporary, "input.xlsx")
	workbook := excelize.NewFile()
	defer func() { _ = workbook.Close() }()
	sheet := workbook.GetSheetName(0)
	rows := [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{1, "Первая", "one", "https://example.test/1"},
		{2, "Вторая", "two", "https://example.test/2"},
	}
	for rowIndex, row := range rows {
		for columnIndex, value := range row {
			cell, _ := excelize.CoordinatesToCellName(columnIndex+1, rowIndex+1)
			if err := workbook.SetCellValue(sheet, cell, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := workbook.SaveAs(path); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	repository := &fakeArticleImporter{}
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	if err := runImport(context.Background(), repository, path, filepath.Join(temporary, "task1", "import-reports"), 0, logger); err != nil {
		t.Fatal(err)
	}
	if repository.calls != 2 {
		t.Fatalf("import calls = %d", repository.calls)
	}
	for _, expected := range []string{"viewed_count=2", "imported_count=1", "existing_count=1", "invalid_count=0", "empty_count=0", "limit_reached=false"} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("logs do not contain %q: %s", expected, logs.String())
		}
	}
}

func TestRunImportLimitCountsOnlyNewValidArticles(t *testing.T) {
	temporary := t.TempDir()
	path := filepath.Join(temporary, "input.xlsx")
	workbook := excelize.NewFile()
	defer func() { _ = workbook.Close() }()
	sheet := workbook.GetSheetName(0)
	rows := [][]any{{"id", "article_name", "image_slug", "reference_url"}}
	for id := 1; id <= 8; id++ {
		rows = append(rows, []any{id, "Статья", "slug", "https://example.test"})
	}
	for rowIndex, row := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, rowIndex+1)
		if err := workbook.SetSheetRow(sheet, cell, &row); err != nil {
			t.Fatal(err)
		}
	}
	if err := workbook.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	repository := &fakeArticleImporter{existing: map[int]article.Article{
		1: {ExternalID: "1"}, 2: {ExternalID: "2"}, 3: {ExternalID: "3"}, 6: {ExternalID: "6"},
	}}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	if err := runImport(context.Background(), repository, path, filepath.Join(temporary, "task1", "import-reports"), 3, logger); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{4, 5, 7} {
		if _, ok := repository.existing[id]; !ok {
			t.Fatalf("external_id=%d не импортирован", id)
		}
	}
	if _, ok := repository.existing[8]; ok {
		t.Fatal("external_id=8 импортирован сверх лимита")
	}
	report := readLatestReport(t, temporary)
	if report.Summary.Viewed != 7 || report.Summary.Imported != 3 || report.Summary.Existing != 4 || !report.Summary.LimitReached {
		t.Fatalf("неверная статистика лимита: %+v", report.Summary)
	}
}

func TestRunImportWritesFatalDatabaseErrorToReport(t *testing.T) {
	temporary := t.TempDir()
	path := filepath.Join(temporary, "input.xlsx")
	workbook := excelize.NewFile()
	defer func() { _ = workbook.Close() }()
	row1 := []any{"id", "article_name", "image_slug", "reference_url"}
	row2 := []any{1, "Статья", "slug", "https://example.test"}
	if err := workbook.SetSheetRow(workbook.GetSheetName(0), "A1", &row1); err != nil {
		t.Fatal(err)
	}
	if err := workbook.SetSheetRow(workbook.GetSheetName(0), "A2", &row2); err != nil {
		t.Fatal(err)
	}
	if err := workbook.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	repository := &fakeArticleImporter{errorAt: 1}
	err := runImport(context.Background(), repository, path, filepath.Join(temporary, "task1", "import-reports"), 0, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL недоступен") {
		t.Fatalf("исходная ошибка PostgreSQL потеряна: %v", err)
	}
	latest, readErr := os.ReadFile(filepath.Join(temporary, "task1", "import-reports", "latest.json"))
	if readErr != nil || !strings.Contains(string(latest), "PostgreSQL недоступен") {
		t.Fatalf("fatal_error отсутствует в latest.json: %v\n%s", readErr, latest)
	}
}

func TestRunImportContinuesAfterInvalidAndEmptyRows(t *testing.T) {
	temporary := t.TempDir()
	path := filepath.Join(temporary, "input.xlsx")
	workbook := excelize.NewFile()
	defer func() { _ = workbook.Close() }()
	rows := [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{1, "", "", ""},
		{"", "", "", ""},
		{2, "Корректная", "valid", "https://example.test/valid"},
	}
	for index, row := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, index+1)
		if err := workbook.SetSheetRow(workbook.GetSheetName(0), cell, &row); err != nil {
			t.Fatal(err)
		}
	}
	if err := workbook.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	repository := &fakeArticleImporter{existing: make(map[int]article.Article)}
	if err := runImport(context.Background(), repository, path, filepath.Join(temporary, "task1", "import-reports"), 0, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))); err != nil {
		t.Fatal(err)
	}
	if len(repository.existing) != 1 || repository.existing[2].ExternalID != "2" {
		t.Fatalf("корректная строка после ошибки не импортирована: %+v", repository.existing)
	}
	report := readLatestReport(t, temporary)
	if report.Summary.Viewed != 3 || report.Summary.Invalid != 1 || report.Summary.Empty != 1 || report.Summary.Imported != 1 {
		t.Fatalf("неверная статистика: %+v", report.Summary)
	}
	if len(report.Errors) != 1 || len(report.Errors[0].Errors) != 3 {
		t.Fatalf("ошибки строки не перечислены полностью: %+v", report.Errors)
	}
}

func TestRunImportJoinsDatabaseAndReportErrors(t *testing.T) {
	temporary := t.TempDir()
	path := filepath.Join(temporary, "input.xlsx")
	workbook := excelize.NewFile()
	defer func() { _ = workbook.Close() }()
	header := []any{"id", "article_name", "image_slug", "reference_url"}
	data := []any{1, "Статья", "slug", "https://example.test"}
	_ = workbook.SetSheetRow(workbook.GetSheetName(0), "A1", &header)
	_ = workbook.SetSheetRow(workbook.GetSheetName(0), "A2", &data)
	if err := workbook.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	invalidOutputRoot := filepath.Join(temporary, "not-a-directory")
	if err := os.WriteFile(invalidOutputRoot, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runImport(context.Background(), &fakeArticleImporter{errorAt: 1}, path, invalidOutputRoot, 0, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL недоступен") || !strings.Contains(err.Error(), "директорию отчётов") {
		t.Fatalf("ошибки не объединены: %v", err)
	}
}

func readLatestReport(t *testing.T, outputRoot string) importer.Report {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(outputRoot, "task1", "import-reports", "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report importer.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	return report
}
