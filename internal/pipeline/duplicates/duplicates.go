// Package duplicates reads the duplicate-check table that gates generation of service pages.
//
// The table is written by the /dup-check skill: an agent compares every article of the book
// with the site catalog and records a verdict. The pipeline only reads it, so the judgment
// stays with the agent and the stop stays with the code.
package duplicates

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xuri/excelize/v2"
)

// FileName is the table name; it lives in Dir next to the import book.
const FileName = "duplicates.xlsx"

// Dir is a subdirectory because import accepts exactly one workbook in the input directory.
const Dir = "duplicates"

// Column headers, matched case-insensitively.
const (
	columnID      = "id"
	columnVerdict = "дубль"
	columnLinks   = "ссылки"
)

// Verdict is the decision for one article.
type Verdict struct {
	Duplicate bool
	Links     []string
}

// Table maps external_id to its verdict.
type Table map[string]Verdict

// ErrMissing means the check was not run for the task at all.
var ErrMissing = errors.New("таблица проверки дублей не найдена")

// Path returns the table path for a task input directory.
func Path(inputDir string) string {
	return filepath.Join(inputDir, Dir, FileName)
}

var linkSplitRE = regexp.MustCompile(`[\s,;]+`)

// Load reads the table at path.
func Load(path string) (Table, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrMissing, path)
	}
	file, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("открыть таблицу дублей: %w", err)
	}
	defer func() { _ = file.Close() }()

	sheets := file.GetSheetList()
	if len(sheets) == 0 {
		return nil, fmt.Errorf("таблица дублей %s пуста", path)
	}
	rows, err := file.GetRows(sheets[0])
	if err != nil {
		return nil, fmt.Errorf("прочитать таблицу дублей: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("таблица дублей %s пуста", path)
	}

	index := map[string]int{}
	for i, header := range rows[0] {
		index[strings.ToLower(strings.TrimSpace(header))] = i
	}
	for _, required := range []string{columnID, columnVerdict, columnLinks} {
		if _, ok := index[required]; !ok {
			return nil, fmt.Errorf("в таблице дублей нет колонки %q", required)
		}
	}

	table := Table{}
	for n, row := range rows[1:] {
		id := cell(row, index[columnID])
		if id == "" {
			continue
		}
		verdict := Verdict{}
		switch strings.ToLower(cell(row, index[columnVerdict])) {
		case "да":
			verdict.Duplicate = true
		case "нет":
		default:
			return nil, fmt.Errorf("таблица дублей, строка %d (id %s): в колонке «Дубль» ждём «да» или «нет»", n+2, id)
		}
		for _, link := range linkSplitRE.Split(cell(row, index[columnLinks]), -1) {
			if link != "" {
				verdict.Links = append(verdict.Links, link)
			}
		}
		if verdict.Duplicate && len(verdict.Links) == 0 {
			return nil, fmt.Errorf("таблица дублей, строка %d (id %s): дубль без ссылки", n+2, id)
		}
		table[id] = verdict
	}
	return table, nil
}

// Check returns the reason the article must not be generated, or nil.
func (t Table) Check(externalID string) error {
	verdict, ok := t[externalID]
	if !ok {
		return fmt.Errorf("статья %s не проверена на дубли — сначала проверка дублей (/dup-check)", externalID)
	}
	if verdict.Duplicate {
		return fmt.Errorf("статья %s дублирует страницу сайта: %s — генерация остановлена; если это не дубль, поставьте «нет» в %s",
			externalID, strings.Join(verdict.Links, ", "), FileName)
	}
	return nil
}

func cell(row []string, i int) string {
	if i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}
