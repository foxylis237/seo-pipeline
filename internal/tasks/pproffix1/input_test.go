package pproffix1

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/xuri/excelize/v2"
)

// Выгрузка таблицы в HTML — основной формат входа.
func TestParseSourcesReadsHTMLTable(t *testing.T) {
	sources, err := ParseSources(`<table>
		<tr><td>12</td><td><a href="https://dpo-prof.ru/blog/kak-stat-hr/">Как стать HR</a></td></tr>
		<tr><td>13</td><td><a href="https://dpo-prof.ru/blog/medsestra/">Медсестра</a></td></tr>
	</table>`)
	if err != nil {
		t.Fatalf("ParseSources: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("прочитано %d строк, ожидалось 2: %+v", len(sources), sources)
	}
	if sources[0].ExternalID != "12" || sources[0].Slug != "kak-stat-hr" {
		t.Fatalf("первая строка разобрана как %+v", sources[0])
	}
	if sources[1].URL != "https://dpo-prof.ru/blog/medsestra/" {
		t.Fatalf("вторая строка разобрана как %+v", sources[1])
	}
}

// Тот же вход списком строк — читается тем же разбором.
func TestParseSourcesReadsPlainLines(t *testing.T) {
	sources, err := ParseSources("12 https://dpo-prof.ru/blog/kak-stat-hr/\n13,https://dpo-prof.ru/blog/medsestra/\n")
	if err != nil {
		t.Fatalf("ParseSources: %v", err)
	}
	if len(sources) != 2 || sources[0].ExternalID != "12" || sources[1].Slug != "medsestra" {
		t.Fatalf("строки разобраны как %+v", sources)
	}
}

// Ссылка без индекса — отказ с указанием строки: пропущенная статья видна только по её
// отсутствию в блоге, а это замечают через недели.
func TestParseSourcesFailsOnURLWithoutIndex(t *testing.T) {
	if _, err := ParseSources("https://dpo-prof.ru/blog/kak-stat-hr/\n"); err == nil {
		t.Fatal("ParseSources принял ссылку без индекса")
	}
}

func TestParseSourcesFailsOnDuplicateIndex(t *testing.T) {
	_, err := ParseSources("12 https://dpo-prof.ru/blog/a/\n12 https://dpo-prof.ru/blog/b/\n")
	if err == nil {
		t.Fatal("ParseSources принял два одинаковых индекса")
	}
}

// Книга Excel — тот же вход, только другим файлом: две колонки, индекс и ссылка, без шапки.
func TestParseWorkbookReadsIndexAndLink(t *testing.T) {
	book := excelize.NewFile()
	defer func() { _ = book.Close() }()
	sheet := book.GetSheetList()[0]
	rows := [][]string{
		{"1", "https://dpoprof.ru/obuchenie-medpersonala/medsestra-v-kosmetologii/"},
		{"2", "https://dpoprof.ru/obuchenie-medpersonala/medsestra-v-nevrologii/"},
	}
	for index, row := range rows {
		if err := book.SetSheetRow(sheet, "A"+strconv.Itoa(index+1), &row); err != nil {
			t.Fatalf("подготовить книгу: %v", err)
		}
	}
	path := filepath.Join(t.TempDir(), "input.xlsx")
	if err := book.SaveAs(path); err != nil {
		t.Fatalf("сохранить книгу: %v", err)
	}
	sources, err := ParseWorkbook(path)
	if err != nil {
		t.Fatalf("ParseWorkbook: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("прочитано %d строк, ожидалось 2: %+v", len(sources), sources)
	}
	if sources[0].ExternalID != "1" || sources[0].Slug != "medsestra-v-kosmetologii" {
		t.Fatalf("первая строка разобрана как %+v", sources[0])
	}
}

// Ссылка, спрятанная под текстом ячейки, тоже находится: в книге так бывает чаще всего.
func TestParseWorkbookReadsHyperlinkBehindText(t *testing.T) {
	book := excelize.NewFile()
	defer func() { _ = book.Close() }()
	sheet := book.GetSheetList()[0]
	if err := book.SetCellValue(sheet, "A1", "7"); err != nil {
		t.Fatalf("подготовить книгу: %v", err)
	}
	if err := book.SetCellValue(sheet, "B1", "Медсестра в школе"); err != nil {
		t.Fatalf("подготовить книгу: %v", err)
	}
	if err := book.SetCellHyperLink(sheet, "B1",
		"https://dpoprof.ru/obuchenie-medpersonala/medsestra-v-shkole/", "External"); err != nil {
		t.Fatalf("подготовить ссылку: %v", err)
	}
	path := filepath.Join(t.TempDir(), "input.xlsx")
	if err := book.SaveAs(path); err != nil {
		t.Fatalf("сохранить книгу: %v", err)
	}
	sources, err := ParseWorkbook(path)
	if err != nil {
		t.Fatalf("ParseWorkbook: %v", err)
	}
	if len(sources) != 1 || sources[0].Slug != "medsestra-v-shkole" {
		t.Fatalf("книга с гиперссылкой разобрана как %+v", sources)
	}
}
