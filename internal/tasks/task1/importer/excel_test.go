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

	articles, err := ReadArticles(path)
	if err != nil {
		t.Fatalf("read project input workbook %q: %v", path, err)
	}
	if len(articles) == 0 {
		t.Fatal("project input workbook contains no importable articles")
	}
}

func TestReadArticlesRejectsEmptyTitle(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name"},
		{1, "   "},
	})

	_, err := ReadArticles(path)
	if err == nil || !strings.Contains(err.Error(), `строка 2: обязательная колонка "article_name" пуста`) {
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
		{"id", "article_name", " Article_Name "},
		{1, "Статья", "Дубликат"},
	})

	_, err := ReadArticles(path)
	if err == nil || !strings.Contains(err.Error(), `повторяющийся заголовок "article_name" в колонках 2 и 3`) {
		t.Fatalf("ожидалась ошибка повторяющегося заголовка, получено: %v", err)
	}
}

func TestReadArticlesAllowsMissingOptionalColumns(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "header", "category", "reference_url"},
		{7, "  Тестовая статья  ", "", "", ""},
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

func TestReadArticlesRejectsDuplicateExternalID(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name"},
		{7, "Первая статья"},
		{7, "Вторая статья"},
	})

	_, err := ReadArticles(path)
	if err == nil || !strings.Contains(err.Error(), "строка 3: id 7 уже использован в строке 2") {
		t.Fatalf("ожидалась ошибка повторного external_id, получено: %v", err)
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
