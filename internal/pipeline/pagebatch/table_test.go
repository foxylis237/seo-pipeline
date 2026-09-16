package pagebatch

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/xuri/excelize/v2"
)

// writeWorkbook собирает книгу из строк и возвращает путь к ней.
func writeWorkbook(t *testing.T, rows [][]string) string {
	t.Helper()
	book := excelize.NewFile()
	defer func() { _ = book.Close() }()
	sheet := book.GetSheetList()[0]
	for index, row := range rows {
		values := row
		if err := book.SetSheetRow(sheet, "A"+strconv.Itoa(index+1), &values); err != nil {
			t.Fatalf("подготовить книгу: %v", err)
		}
	}
	path := filepath.Join(t.TempDir(), "input.xlsx")
	if err := book.SaveAs(path); err != nil {
		t.Fatalf("сохранить книгу: %v", err)
	}
	return path
}

// Книга с четырьмя колонками разбирается по заголовкам: два числа подряд построчное правило
// «первое число до первой ссылки» перепутало бы, взяв индексом идентификатор записи.
func TestReadTableReadsFourColumns(t *testing.T) {
	path := writeWorkbook(t, [][]string{
		{"индекс", "post_id", "ссылка", "тема"},
		{"2", "22314", "https://dpoprof.ru/obuchenie-medpersonala/logoped/", "Логопед"},
		{"1", "22200", "https://dpoprof.ru/obuchenie-medpersonala/sanitar/", "Санитар"},
	})
	sources, kind, err := ReadTable(path)
	if err != nil {
		t.Fatalf("ReadTable: %v", err)
	}
	if kind != ParseColumns {
		t.Fatalf("разбор %q, ожидался %q", kind, ParseColumns)
	}
	if len(sources) != 2 {
		t.Fatalf("прочитано %d строк, ожидалось 2: %+v", len(sources), sources)
	}
	// Сортировка по числовому индексу — та же, что у построчного разбора.
	if sources[0].ExternalID != "1" || sources[1].ExternalID != "2" {
		t.Fatalf("строки не отсортированы по индексу: %+v", sources)
	}
	if sources[1].PostID != 22314 {
		t.Fatalf("идентификатор записи прочитан как %d, ожидался 22314", sources[1].PostID)
	}
	if sources[1].Topic != "Логопед" {
		t.Fatalf("тема прочитана как %q", sources[1].Topic)
	}
	if sources[1].Slug != "logoped" {
		t.Fatalf("слаг прочитан как %q", sources[1].Slug)
	}
}

// Прежний вход из двух колонок без шапки обязан читаться как раньше: заголовков нет, и разбор
// откатывается на построчный. Иначе появление аудита сломало бы вход задач правки.
func TestReadTableFallsBackToLines(t *testing.T) {
	path := writeWorkbook(t, [][]string{
		{"1", "https://dpoprof.ru/obuchenie-medpersonala/medsestra-v-shkole/"},
		{"2", "https://dpoprof.ru/obuchenie-medpersonala/medsestra-v-nevrologii/"},
	})
	sources, kind, err := ReadTable(path)
	if err != nil {
		t.Fatalf("ReadTable: %v", err)
	}
	if kind != ParseLines {
		t.Fatalf("разбор %q, ожидался %q", kind, ParseLines)
	}
	if len(sources) != 2 || sources[0].Slug != "medsestra-v-shkole" {
		t.Fatalf("книга без шапки разобрана как %+v", sources)
	}
	// Первая строка не должна потеряться: при откате файл читается целиком заново.
	if sources[0].ExternalID != "1" {
		t.Fatalf("первая строка потеряна: %+v", sources)
	}
	if sources[0].PostID != 0 || sources[0].Topic != "" {
		t.Fatalf("построчный разбор придумал колонки: %+v", sources[0])
	}
}

// Колонки идентификатора и темы необязательны: книга «индекс + ссылка» с шапкой читается
// по колонкам, а недостающие поля остаются нулевыми.
func TestReadTableAllowsMissingOptionalColumns(t *testing.T) {
	path := writeWorkbook(t, [][]string{
		{"индекс", "ссылка"},
		{"5", "https://dpoprof.ru/obuchenie-medpersonala/biolog/"},
	})
	sources, kind, err := ReadTable(path)
	if err != nil {
		t.Fatalf("ReadTable: %v", err)
	}
	if kind != ParseColumns {
		t.Fatalf("разбор %q, ожидался %q", kind, ParseColumns)
	}
	if len(sources) != 1 || sources[0].PostID != 0 || sources[0].Topic != "" {
		t.Fatalf("книга без необязательных колонок разобрана как %+v", sources)
	}
}

// Адрес под текстом ячейки находится и в колоночном разборе: в книге так бывает чаще всего.
func TestReadTableReadsHyperlinkBehindText(t *testing.T) {
	book := excelize.NewFile()
	defer func() { _ = book.Close() }()
	sheet := book.GetSheetList()[0]
	header := []string{"индекс", "ссылка"}
	if err := book.SetSheetRow(sheet, "A1", &header); err != nil {
		t.Fatalf("подготовить книгу: %v", err)
	}
	if err := book.SetCellValue(sheet, "A2", "7"); err != nil {
		t.Fatalf("подготовить книгу: %v", err)
	}
	if err := book.SetCellValue(sheet, "B2", "Медсестра в школе"); err != nil {
		t.Fatalf("подготовить книгу: %v", err)
	}
	if err := book.SetCellHyperLink(sheet, "B2",
		"https://dpoprof.ru/obuchenie-medpersonala/medsestra-v-shkole/", "External"); err != nil {
		t.Fatalf("подготовить ссылку: %v", err)
	}
	path := filepath.Join(t.TempDir(), "input.xlsx")
	if err := book.SaveAs(path); err != nil {
		t.Fatalf("сохранить книгу: %v", err)
	}
	sources, kind, err := ReadTable(path)
	if err != nil {
		t.Fatalf("ReadTable: %v", err)
	}
	if kind != ParseColumns || len(sources) != 1 || sources[0].Slug != "medsestra-v-shkole" {
		t.Fatalf("книга с гиперссылкой разобрана как %+v (%s)", sources, kind)
	}
}

// Строка без ссылки называется в ошибке с номером, а не пропускается молча: пропущенная
// статья обнаруживается только тем, что её нет в отчётах.
func TestReadTableNamesRowWithoutURL(t *testing.T) {
	path := writeWorkbook(t, [][]string{
		{"индекс", "ссылка"},
		{"1", "https://dpoprof.ru/obuchenie-medpersonala/logoped/"},
		{"2", ""},
	})
	_, _, err := ReadTable(path)
	if err == nil {
		t.Fatal("строка без ссылки прошла молча")
	}
	if got := err.Error(); !contains(got, "строка 3") || !contains(got, "нет ссылки") {
		t.Fatalf("ошибка не называет строку: %v", err)
	}
}

// Нечисловой идентификатор записи роняет импорт: по нему пришлось бы читать чужую страницу.
func TestReadTableRejectsNonNumericPostID(t *testing.T) {
	path := writeWorkbook(t, [][]string{
		{"индекс", "post_id", "ссылка"},
		{"1", "22314x", "https://dpoprof.ru/obuchenie-medpersonala/logoped/"},
	})
	_, _, err := ReadTable(path)
	if err == nil {
		t.Fatal("нечисловой идентификатор записи прошёл молча")
	}
	if !contains(err.Error(), "не число") {
		t.Fatalf("ошибка не объясняет причину: %v", err)
	}
}

// Повторный индекс останавливает импорт с номерами обеих строк — как и в построчном разборе.
func TestReadTableRejectsDuplicateIndex(t *testing.T) {
	path := writeWorkbook(t, [][]string{
		{"индекс", "ссылка"},
		{"4", "https://dpoprof.ru/obuchenie-medpersonala/logoped/"},
		{"4", "https://dpoprof.ru/obuchenie-medpersonala/sanitar/"},
	})
	_, _, err := ReadTable(path)
	if err == nil {
		t.Fatal("повторный индекс прошёл молча")
	}
	if !contains(err.Error(), "уже встречался") {
		t.Fatalf("ошибка не объясняет причину: %v", err)
	}
}

// Повторяющийся заголовок — недописанная шапка, и угадывать настоящую колонку нельзя.
func TestReadTableRejectsDuplicateHeader(t *testing.T) {
	path := writeWorkbook(t, [][]string{
		{"индекс", "ссылка", "url"},
		{"1", "https://dpoprof.ru/obuchenie-medpersonala/logoped/", "https://example.com/"},
	})
	_, _, err := ReadTable(path)
	if err == nil {
		t.Fatal("повторяющийся заголовок прошёл молча")
	}
	if !contains(err.Error(), "повторяющийся заголовок") {
		t.Fatalf("ошибка не объясняет причину: %v", err)
	}
}

// Форматы без колонок разбираются построчно и об этом честно сообщают.
func TestReadTableReportsLinesForText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.txt")
	if err := writeFile(path, "1 https://dpoprof.ru/obuchenie-medpersonala/logoped/\n"); err != nil {
		t.Fatalf("подготовить файл: %v", err)
	}
	sources, kind, err := ReadTable(path)
	if err != nil {
		t.Fatalf("ReadTable: %v", err)
	}
	if kind != ParseLines || len(sources) != 1 {
		t.Fatalf("текстовый файл разобран как %+v (%s)", sources, kind)
	}
}
