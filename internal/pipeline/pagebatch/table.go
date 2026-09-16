package pagebatch

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// ParseKind — каким разбором прочитан входной файл.
//
// Это не деталь реализации, а то, что обязано попасть в лог: у книги с четырьмя колонками и
// у списка строк правила разные, и молча прочитать идентификатор записи как индекс статьи
// нельзя — ошибка обнаружится только тем, что проверена чужая страница.
type ParseKind string

const (
	// ParseColumns — книга разобрана по заголовкам колонок.
	ParseColumns ParseKind = "columns"
	// ParseLines — сработало построчное правило «первое число до первой ссылки».
	ParseLines ParseKind = "lines"
)

// Канонические имена колонок входного файла.
const (
	columnExternalID = "external_id"
	columnPostID     = "post_id"
	columnURL        = "url"
	columnTopic      = "topic"
)

// columnAliases сводит заголовки книги к именам, которыми колонки называет код.
//
// Таблицей, а не ветками в чтении строки, — по образцу importer.columnAliases: новая книга
// добавляет строку в карту, а не правку разбора. Ключи уже приведены к нижнему регистру и
// обрезаны.
//
// «id» отдан индексу статьи, а не записи: в книге он стоит первым столбцом и означает то же,
// что «индекс», — то, чем статью называют в командах. Идентификатор записи блога называется
// в заголовке явно, иначе два числа в строке не различить.
var columnAliases = map[string]string{
	"индекс":      columnExternalID,
	"номер":       columnExternalID,
	"№":           columnExternalID,
	"id":          columnExternalID,
	"external_id": columnExternalID,

	"post_id":   columnPostID,
	"postid":    columnPostID,
	"id записи": columnPostID,
	"id_записи": columnPostID,
	"запись":    columnPostID,
	"id поста":  columnPostID,

	"ссылка":   columnURL,
	"адрес":    columnURL,
	"url":      columnURL,
	"link":     columnURL,
	"страница": columnURL,

	"тема":     columnTopic,
	"topic":    columnTopic,
	"тематика": columnTopic,
}

// ReadTable читает вход задачи, разбирая книгу Excel по колонкам с заголовками.
//
// Отличие от ReadSources одно и содержательное: во входном файле может быть четыре значимых
// колонки — индекс, идентификатор записи, ссылка и тема, — а построчное правило «первое число
// до первой ссылки» на двух числах подряд ломается: оно возьмёт индексом то, что первым
// попалось. Поэтому у книги сначала спрашивают заголовки.
//
// Заголовков нет или они не опознаны — это не ошибка, а прежний вход: файл из двух колонок
// обязан читаться как раньше, и разбор откатывается на построчный. Он же остаётся
// единственным у форматов без колонок — HTML, CSV, текста.
func ReadTable(path string) ([]Source, ParseKind, error) {
	if !workbookPath(path) {
		sources, err := ReadSources(path)
		return sources, ParseLines, err
	}
	sources, ok, err := parseWorkbookColumns(path)
	if err != nil {
		return nil, ParseColumns, fmt.Errorf("%s: %w", path, err)
	}
	if ok {
		return sources, ParseColumns, nil
	}
	sources, err = ReadSources(path)
	return sources, ParseLines, err
}

func workbookPath(path string) bool {
	lowered := strings.ToLower(path)
	return strings.HasSuffix(lowered, ".xlsx") || strings.HasSuffix(lowered, ".xlsm")
}

// parseWorkbookColumns разбирает книгу по заголовкам.
//
// Второе возвращаемое значение отвечает, узнала ли шапка себя: без колонок индекса и ссылки
// это не таблица входа, а обычный список строк, и решать за него разбор не вправе.
func parseWorkbookColumns(path string) ([]Source, bool, error) {
	book, err := excelize.OpenFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("открыть книгу %q: %w", path, err)
	}
	defer func() { _ = book.Close() }()
	sheets := book.GetSheetList()
	if len(sheets) == 0 {
		return nil, false, fmt.Errorf("в книге %q нет листов", path)
	}
	sheet := sheets[0]
	rows, err := book.GetRows(sheet)
	if err != nil {
		return nil, false, fmt.Errorf("прочитать лист %q: %w", sheet, err)
	}
	if len(rows) < 2 {
		return nil, false, nil
	}
	indexes, err := columnIndexes(rows[0])
	if err != nil {
		return nil, false, err
	}
	if _, ok := indexes[columnExternalID]; !ok {
		return nil, false, nil
	}
	if _, ok := indexes[columnURL]; !ok {
		return nil, false, nil
	}
	sources, err := sourcesFromRows(book, sheet, rows, indexes)
	if err != nil {
		return nil, true, err
	}
	return sources, true, nil
}

// columnIndexes сводит шапку книги к номерам колонок.
//
// Повторный заголовок — ошибка, а не последнее выигравшее значение: две колонки «ссылка»
// означают, что человек не дописал шапку, и угадывать, какая из них настоящая, нельзя.
func columnIndexes(header []string) (map[string]int, error) {
	indexes := make(map[string]int, len(header))
	for index, value := range header {
		name := strings.TrimSpace(strings.ToLower(value))
		canonical, known := columnAliases[name]
		if !known {
			continue
		}
		if previous, exists := indexes[canonical]; exists {
			return nil, fmt.Errorf("повторяющийся заголовок %q в колонках %d и %d",
				canonical, previous+1, index+1)
		}
		indexes[canonical] = index
	}
	return indexes, nil
}

// sourcesFromRows читает строки книги по уже найденным колонкам.
//
// Строгость та же, что у построчного разбора: нераспознанная строка называется в ошибке с
// номером, а не пропускается молча. Пропущенная статья видна только по её отсутствию в
// отчётах, и обнаруживается через недели.
func sourcesFromRows(book *excelize.File, sheet string, rows [][]string, indexes map[string]int) ([]Source, error) {
	sources := make([]Source, 0, len(rows)-1)
	seen := make(map[string]int, len(rows))
	var problems []string
	for number := 1; number < len(rows); number++ {
		row := rows[number]
		if emptyRow(row) {
			continue
		}
		line := number + 1
		index := strings.TrimSpace(cell(row, indexes[columnExternalID]))
		address := cellURL(book, sheet, row, number, indexes[columnURL])
		if index == "" && address == "" {
			continue
		}
		if index == "" {
			problems = append(problems, fmt.Sprintf("строка %d: есть ссылка %s, но колонка индекса пуста", line, address))
			continue
		}
		if _, err := strconv.Atoi(index); err != nil {
			problems = append(problems, fmt.Sprintf("строка %d: индекс %q не число", line, index))
			continue
		}
		if address == "" {
			problems = append(problems, fmt.Sprintf("строка %d: у индекса %s нет ссылки", line, index))
			continue
		}
		if previous, duplicate := seen[index]; duplicate {
			problems = append(problems, fmt.Sprintf("строка %d: индекс %s уже встречался в строке %d", line, index, previous))
			continue
		}
		slug, err := slugFromURL(address)
		if err != nil {
			problems = append(problems, fmt.Sprintf("строка %d: %v", line, err))
			continue
		}
		source := Source{ExternalID: index, URL: address, Slug: slug}
		if position, ok := indexes[columnPostID]; ok {
			postID, err := parsePostID(cell(row, position))
			if err != nil {
				problems = append(problems, fmt.Sprintf("строка %d: %v", line, err))
				continue
			}
			source.PostID = postID
		}
		if position, ok := indexes[columnTopic]; ok {
			source.Topic = strings.TrimSpace(cell(row, position))
		}
		seen[index] = line
		sources = append(sources, source)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("входной файл разобран не полностью:\n  %s", strings.Join(problems, "\n  "))
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("во входном файле нет ни одной пары «индекс + ссылка»")
	}
	sort.Slice(sources, func(i, j int) bool {
		left, _ := strconv.Atoi(sources[i].ExternalID)
		right, _ := strconv.Atoi(sources[j].ExternalID)
		return left < right
	})
	return sources, nil
}

// parsePostID читает идентификатор записи. Пустая ячейка — законное состояние: тогда запись
// ищут по слагу, как раньше.
func parsePostID(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	postID, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("идентификатор записи %q не число", value)
	}
	if postID <= 0 {
		return 0, fmt.Errorf("идентификатор записи %q не положителен", value)
	}
	return postID, nil
}

// cellURL берёт адрес из ячейки ссылки: сначала текст, потом гиперссылку под ним.
//
// Порядок такой потому, что в книге адрес нередко спрятан под названием статьи, и тогда в
// тексте ячейки ссылки нет вовсе.
func cellURL(book *excelize.File, sheet string, row []string, rowIndex, columnIndex int) string {
	if address := urlPattern.FindString(cell(row, columnIndex)); address != "" {
		return strings.TrimRight(address, ".,;")
	}
	axis, err := excelize.CoordinatesToCellName(columnIndex+1, rowIndex+1)
	if err != nil {
		return ""
	}
	linked, target, err := book.GetCellHyperLink(sheet, axis)
	if err != nil || !linked {
		return ""
	}
	return strings.TrimSpace(target)
}

func cell(row []string, index int) string {
	if index < 0 || index >= len(row) {
		return ""
	}
	return row[index]
}

func emptyRow(row []string) bool {
	for _, value := range row {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}
