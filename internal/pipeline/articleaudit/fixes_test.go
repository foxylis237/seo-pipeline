package articleaudit

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func score(value, max int) (*int, *int) { return &value, &max }

// Таблица правок отвечает на вопрос «что пойти и сделать»: сверху графы, которые дозаполняют
// в админке, ниже — что переписать в тексте.
func TestWriteFixesListsGapsAndAdvice(t *testing.T) {
	value, max := score(11, 20)
	summary := Summary{Pages: []SummaryPage{{
		ExternalID: "7",
		Title:      "оператор КОС",
		URL:        "https://dpoprof.ru/blog/operator-kos/",
		Score:      value,
		ScoreMax:   max,
		Missing:    []string{"blog_read", "author_link"},
		FAQ:        FAQCheck{Count: 5, Min: 6},
		Links:      LinkCheck{Min: 3, URLs: []string{"/obuchenie"}},
		Advice:     []string{"Свести часы в тексте и в блоке вопросов"},
	}}}
	path := filepath.Join(t.TempDir(), FixesFile)

	if err := summary.WriteFixes(path); err != nil {
		t.Fatalf("WriteFixes: %v", err)
	}

	rows := readFixes(t, path)
	if len(rows) != 2 {
		t.Fatalf("строк в книге %d, ожидались шапка и одна страница", len(rows))
	}
	cell := rows[1][0]
	for _, want := range []string{
		"ДОЗАПОЛНИТЬ:", "FAQ: 5 из 6", "перелинковка: 1 из 3",
		"время чтения (blog_read)", "автор статьи (author_link)",
		"ПРАВКИ:", "Свести часы в тексте и в блоке вопросов",
	} {
		if !strings.Contains(cell, want) {
			t.Fatalf("в ячейке нет %q:\n%s", want, cell)
		}
	}
	if rows[1][1] != "11/20" || rows[1][2] != "7" {
		t.Fatalf("шапка строки: %v", rows[1][:3])
	}
}

// Пустая ячейка выглядит как потерянные данные, поэтому «делать нечего» и «страница не
// проверена» называются словами — и это разные состояния.
func TestWriteFixesNamesEmptyStates(t *testing.T) {
	value, max := score(19, 20)
	summary := Summary{Pages: []SummaryPage{
		{ExternalID: "1", Score: value, ScoreMax: max},
		{ExternalID: "2", Error: "в WordPress нет записи"},
	}}
	path := filepath.Join(t.TempDir(), FixesFile)

	if err := summary.WriteFixes(path); err != nil {
		t.Fatalf("WriteFixes: %v", err)
	}

	rows := readFixes(t, path)
	if rows[1][0] != "правок нет" {
		t.Fatalf("страница без находок описана как %q", rows[1][0])
	}
	if !strings.HasPrefix(rows[2][0], "страница не проверена: в WordPress нет записи") {
		t.Fatalf("упавшая страница описана как %q", rows[2][0])
	}
}

// Совет модели бывает длиной в абзац: ячейку читают глазами, и обрезка идёт по границе слова.
func TestWriteFixesShortensLongAdvice(t *testing.T) {
	long := strings.Repeat("переписать вступление и убрать воду ", 12)
	summary := Summary{Pages: []SummaryPage{{ExternalID: "3", Advice: []string{long}}}}
	path := filepath.Join(t.TempDir(), FixesFile)

	if err := summary.WriteFixes(path); err != nil {
		t.Fatalf("WriteFixes: %v", err)
	}

	cell := readFixes(t, path)[1][0]
	if !strings.HasSuffix(cell, "…") {
		t.Fatalf("длинный совет не обрезан:\n%s", cell)
	}
	if strings.Contains(cell, " …") {
		t.Fatalf("обрезка оставила висящий пробел:\n%s", cell)
	}
}

func readFixes(t *testing.T, path string) [][]string {
	t.Helper()
	book, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatalf("книга не открылась: %v", err)
	}
	defer func() { _ = book.Close() }()
	rows, err := book.GetRows(fixesSheet)
	if err != nil {
		t.Fatalf("лист %q не прочитан: %v", fixesSheet, err)
	}
	return rows
}
