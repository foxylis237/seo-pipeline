// Package pagebatch разбирает входной файл пачки опубликованных страниц и решает, когда прогон пора бросить.
package pagebatch

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// Source — одна строка входного файла: индекс статьи и ссылка на неё.
type Source struct {
	ExternalID string
	URL        string
	// Slug — слаг из адреса: имя каталога артефактов и ключ поиска записи в блоге.
	Slug string
	// PostID — идентификатор записи в блоге; ноль — искать по слагу.
	// Найденную по нему запись всё равно сверяют со ссылкой: опечатка даёт правдоподобную чужую страницу.
	PostID int64
	// Topic — тема страницы, если её дала книга с колонками; пустая — основанием служит заголовок записи.
	Topic string
}

// rowSeparators разрезают файл на строки: вход бывает и таблицей HTML, и списком строк.
var rowSeparators = regexp.MustCompile(`(?i)</tr>|</li>|<br\s*/?>|\r?\n`)

// urlPattern находит адрес статьи в строке.
var urlPattern = regexp.MustCompile(`https?://[^\s"'<>)\]]+`)

// indexPattern находит индекс — целое число, стоящее в строке до адреса.
var indexPattern = regexp.MustCompile(`\d+`)

// ParseSources читает индексы и ссылки из текста: в каждой строке — первое число до первой ссылки.
// Строка без ссылки пропускается, ссылка без индекса — ошибка с номером строки.
func ParseSources(content string) ([]Source, error) {
	rows := rowSeparators.Split(content, -1)
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, stripTags(row))
	}
	return sourcesFromLines(lines)
}

// sourcesFromLines собирает вход из разрезанных строк; общая для книги Excel и текстового файла.
func sourcesFromLines(lines []string) ([]Source, error) {
	sources := make([]Source, 0, len(lines))
	seen := make(map[string]int, len(lines))
	var problems []string
	for number, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		address := urlPattern.FindString(line)
		if address == "" {
			continue
		}
		address = strings.TrimRight(address, ".,;")
		index := indexPattern.FindString(line[:strings.Index(line, address)])
		if index == "" {
			problems = append(problems, fmt.Sprintf("строка %d: есть ссылка %s, но нет индекса перед ней", number+1, address))
			continue
		}
		if _, err := strconv.Atoi(index); err != nil {
			problems = append(problems, fmt.Sprintf("строка %d: индекс %q не число", number+1, index))
			continue
		}
		if previous, duplicate := seen[index]; duplicate {
			problems = append(problems, fmt.Sprintf("строка %d: индекс %s уже встречался в строке %d", number+1, index, previous))
			continue
		}
		slug, err := slugFromURL(address)
		if err != nil {
			problems = append(problems, fmt.Sprintf("строка %d: %v", number+1, err))
			continue
		}
		seen[index] = number + 1
		sources = append(sources, Source{ExternalID: index, URL: address, Slug: slug})
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

// tagPattern снимает разметку; адрес из href внутри тега сохраняется (см. stripTags).
var tagPattern = regexp.MustCompile(`<[^>]*>`)

func stripTags(row string) string {
	withoutTags := tagPattern.ReplaceAllStringFunc(row, func(tag string) string {
		if address := urlPattern.FindString(tag); address != "" {
			return " " + address + " "
		}
		return " "
	})
	return strings.Join(strings.Fields(withoutTags), " ")
}

// slugFromURL возвращает последний непустой сегмент пути адреса.
func slugFromURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("разобрать ссылку %q: %w", raw, err)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index := len(segments) - 1; index >= 0; index-- {
		segment := strings.TrimSpace(segments[index])
		if segment == "" {
			continue
		}
		if unescaped, unescapeErr := url.PathUnescape(segment); unescapeErr == nil {
			return unescaped, nil
		}
		return segment, nil
	}
	return "", fmt.Errorf("в ссылке %q нет слага статьи", raw)
}

// ParseWorkbook читает вход из первого листа книги Excel по тому же правилу, что ParseSources.
// Адреса гиперссылок добавляются отдельно: ссылка бывает спрятана под текстом ячейки.
func ParseWorkbook(path string) ([]Source, error) {
	book, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("открыть книгу %q: %w", path, err)
	}
	defer func() { _ = book.Close() }()
	sheets := book.GetSheetList()
	if len(sheets) == 0 {
		return nil, fmt.Errorf("в книге %q нет листов", path)
	}
	sheet := sheets[0]
	rows, err := book.GetRows(sheet)
	if err != nil {
		return nil, fmt.Errorf("прочитать лист %q: %w", sheet, err)
	}
	lines := make([]string, 0, len(rows))
	for rowIndex, row := range rows {
		parts := make([]string, 0, len(row)*2)
		for cellIndex, cell := range row {
			parts = append(parts, cell)
			axis, axisErr := excelize.CoordinatesToCellName(cellIndex+1, rowIndex+1)
			if axisErr != nil {
				continue
			}
			if linked, target, linkErr := book.GetCellHyperLink(sheet, axis); linkErr == nil && linked {
				parts = append(parts, target)
			}
		}
		lines = append(lines, strings.Join(parts, " "))
	}
	return sourcesFromLines(lines)
}

// documentationFile отвечает, README ли это рядом со входом.
func documentationFile(name string) bool {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	return strings.EqualFold(base, "README")
}

// ResolveInputFile выбирает единственный входной файл в каталоге; явный путь перекрывает поиск.
func ResolveInputFile(explicitPath, directory string) (string, error) {
	if strings.TrimSpace(explicitPath) != "" {
		if _, err := os.Stat(explicitPath); err != nil {
			return "", fmt.Errorf("входной файл %q недоступен: %w", explicitPath, err)
		}
		return explicitPath, nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", fmt.Errorf("прочитать каталог входных данных %q: %w", directory, err)
	}
	var candidates []string
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || documentationFile(entry.Name()) {
			continue
		}
		switch strings.ToLower(filepath.Ext(entry.Name())) {
		case ".xlsx", ".xlsm", ".html", ".htm", ".xml", ".csv", ".txt", ".md":
			candidates = append(candidates, filepath.Join(directory, entry.Name()))
		}
	}
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("в каталоге %q нет входного файла (.xlsx, .html, .xml, .csv, .txt)", directory)
	case 1:
		return candidates[0], nil
	default:
		return "", fmt.Errorf("в каталоге %q больше одного входного файла: %s — оставьте нужный или задайте INPUT_FILE_PATH",
			directory, strings.Join(candidates, ", "))
	}
}

// ReadSources читает и разбирает входной файл; формат выбирается по расширению.
func ReadSources(path string) ([]Source, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".xlsx", ".xlsm":
		sources, err := ParseWorkbook(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return sources, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("прочитать входной файл %q: %w", path, err)
	}
	sources, err := ParseSources(string(content))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return sources, nil
}
