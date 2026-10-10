package duplicates

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func writeTable(t *testing.T, rows [][]any) string {
	t.Helper()
	file := excelize.NewFile()
	for i, row := range rows {
		cellName, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := file.SetSheetRow("Sheet1", cellName, &row); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := file.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAndCheck(t *testing.T) {
	path := writeTable(t, [][]any{
		{"id", "Тема", "Дубль", "Ссылки", "Почему"},
		{"1", "Сварщик", "да", "https://a.ru/x/\nhttps://a.ru/y/", "та же программа"},
		{"2", "Крановщик", "Нет", "", ""},
	})
	table, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Check("2"); err != nil {
		t.Fatalf("clean article stopped: %v", err)
	}
	err = table.Check("1")
	if err == nil || !strings.Contains(err.Error(), "https://a.ru/x/, https://a.ru/y/") {
		t.Fatalf("duplicate not stopped with links: %v", err)
	}
	if err := table.Check("3"); err == nil || !strings.Contains(err.Error(), "не проверена") {
		t.Fatalf("unchecked article passed: %v", err)
	}
}

func TestLoadRejectsBadRows(t *testing.T) {
	for name, row := range map[string][]any{
		"unknown verdict":        {"1", "Сварщик", "возможно", "", ""},
		"duplicate without link": {"1", "Сварщик", "да", "", ""},
	} {
		path := writeTable(t, [][]any{{"id", "Тема", "Дубль", "Ссылки"}, row})
		if _, err := Load(path); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), FileName)); !errors.Is(err, ErrMissing) {
		t.Fatalf("want ErrMissing, got %v", err)
	}
}
