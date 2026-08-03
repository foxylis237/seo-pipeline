package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestProjectInputWorkbookIsReadable(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "input", "task_1", "input.xlsx")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("project input workbook %q is unavailable: %v", path, err)
	}

	rows, err := ReadRows(path)
	if err != nil {
		t.Fatalf("read project input workbook %q: %v", path, err)
	}
	hasValidArticle := false
	for _, row := range rows {
		if !row.Empty && len(row.Errors) == 0 {
			hasValidArticle = true
			break
		}
	}
	if !hasValidArticle {
		t.Fatal("project input workbook contains no importable articles")
	}
}

func TestReadArticlesRejectsEmptyTitle(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{1, "   ", "slug", "https://example.test"},
	})

	_, err := ReadArticles(path)
	if err == nil || !strings.Contains(err.Error(), `поле "article_name": отсутствует или пусто`) {
		t.Fatalf("ожидалась ошибка пустого названия, получено: %v", err)
	}
}

func TestReadArticlesRejectsMissingRequiredColumn(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "header"},
		{1, "Заголовок"},
	})

	_, err := ReadArticles(path)
	if err == nil || !strings.Contains(err.Error(), `отсутствует обязательная колонка "article_name"`) {
		t.Fatalf("ожидалась ошибка отсутствующей колонки, получено: %v", err)
	}
}

func TestReadArticlesRejectsDuplicateHeaders(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", " Article_Name ", "image_slug", "reference_url"},
		{1, "Статья", "Дубликат", "slug", "https://example.test"},
	})

	_, err := ReadArticles(path)
	if err == nil || !strings.Contains(err.Error(), `повторяющийся заголовок "article_name" в колонках 2 и 3`) {
		t.Fatalf("ожидалась ошибка повторяющегося заголовка, получено: %v", err)
	}
}

func TestReadArticlesAllowsMissingOptionalColumns(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{7, "  Тестовая статья  ", "slug", "https://example.test"},
	})

	articles, err := ReadArticles(path)
	if err != nil {
		t.Fatalf("прочитать файл: %v", err)
	}
	if len(articles) != 1 {
		t.Fatalf("ожидалась одна статья, получено: %d", len(articles))
	}
	if articles[0].Title != "Тестовая статья" || articles[0].Header != "" {
		t.Fatalf("неожиданные данные статьи: %+v", articles[0])
	}
}

func TestReadRowsReportsDuplicateExternalIDAndContinues(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{7, "Первая статья", "first", "https://example.test/1"},
		{7, "Вторая статья", "second", "https://example.test/2"},
		{8, "Третья статья", "third", "https://example.test/3"},
	})

	rows, err := ReadRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || len(rows[1].Errors) != 1 || !strings.Contains(rows[1].Errors[0], "дубликат строки 2") {
		t.Fatalf("дубликат не отмечен: %+v", rows)
	}
	if len(rows[2].Errors) != 0 || rows[2].Input.ExcelID != 8 {
		t.Fatalf("чтение не продолжилось после дубликата: %+v", rows[2])
	}
}

func TestReadRowsListsEveryInvalidRequiredField(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{"NULL", "  ", "null", ""},
	})
	rows, err := ReadRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Errors) != 4 {
		t.Fatalf("ошибки обязательных полей: %+v", rows)
	}
}

func TestReadArticlesWithoutLimitReadsEveryDataRowIncludingLast(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{1, "Первая", "one", "https://example.test/1"},
		{2, "Вторая", "two", "https://example.test/2"},
		{3, "Последняя", "three", "https://example.test/3"},
	})

	articles, err := ReadArticles(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(articles) != 3 || articles[2].ExcelID != 3 || articles[2].Title != "Последняя" {
		t.Fatalf("прочитаны не все строки: %+v", articles)
	}
}

func TestReadArticlesLimitCountsDataRowsNotHeader(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{1, "Первая", "one", "https://example.test/1"},
		{2, "Вторая", "two", "https://example.test/2"},
		{3, "Третья", "three", "https://example.test/3"},
	})

	articles, err := ReadArticlesWithLimit(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(articles) != 2 || articles[0].ExcelID != 1 || articles[1].ExcelID != 2 {
		t.Fatalf("лимит применён неверно: %+v", articles)
	}
}

func TestReadArticlesLimitLargerThanFileReadsToEnd(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{1, "Первая", "one", "https://example.test/1"},
		{2, "Последняя", "two", "https://example.test/2"},
	})

	articles, err := ReadArticlesWithLimit(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(articles) != 2 || articles[1].ExcelID != 2 {
		t.Fatalf("ожидались обе строки: %+v", articles)
	}
}

func TestReadArticlesLimitSkipsEmptyRows(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{1, "Первая", "one", "https://example.test/1"},
		{"", "", "", ""},
		{2, "Вторая", "two", "https://example.test/2"},
	})

	articles, err := ReadArticlesWithLimit(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(articles) != 2 || articles[1].ExcelID != 2 {
		t.Fatalf("пустая строка вошла в лимит: %+v", articles)
	}
}

func writeWorkbook(t *testing.T, rows [][]any) string {
	t.Helper()

	file := excelize.NewFile()
	defer func() { _ = file.Close() }()

	for rowIndex, row := range rows {
		cell, err := excelize.CoordinatesToCellName(1, rowIndex+1)
		if err != nil {
			t.Fatalf("получить адрес ячейки: %v", err)
		}
		if err := file.SetSheetRow("Sheet1", cell, &row); err != nil {
			t.Fatalf("записать строку: %v", err)
		}
	}

	path := filepath.Join(t.TempDir(), "input.xlsx")
	if err := file.SaveAs(path); err != nil {
		t.Fatalf("сохранить тестовый Excel: %v", err)
	}
	return path
}
