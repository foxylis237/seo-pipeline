package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/xuri/excelize/v2"
)

type fakeArticleImporter struct{ calls int }

func (f *fakeArticleImporter) Import(_ context.Context, input article.Input) (article.Article, bool, error) {
	f.calls++
	created := f.calls == 1
	return article.Article{ID: int64(f.calls), ExternalID: strconv.Itoa(input.ExcelID), Title: input.Title}, created, nil
}

func TestRunImportLogsFoundAddedAndSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.xlsx")
	workbook := excelize.NewFile()
	defer func() { _ = workbook.Close() }()
	sheet := workbook.GetSheetName(0)
	rows := [][]any{
		{"id", "article_name", "image_slug"},
		{1, "Первая", "one"},
		{2, "Вторая", "two"},
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
	if err := runImport(context.Background(), repository, path, logger); err != nil {
		t.Fatal(err)
	}
	if repository.calls != 2 {
		t.Fatalf("import calls = %d", repository.calls)
	}
	for _, expected := range []string{"found_count=2", "added_count=1", "skipped_count=1"} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("logs do not contain %q: %s", expected, logs.String())
		}
	}
}
