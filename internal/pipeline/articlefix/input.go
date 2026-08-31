package articlefix

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
	// Slug — слаг из адреса. Считается один раз здесь, потому что им называется и каталог
	// артефактов, и поиск записи в блоге.
	Slug string
}

// rowSeparators разрезают файл на строки таблицы. Их несколько, потому что формат входа не
// фиксирован: человек отдаёт то выгрузку таблицы в HTML, то список строк. Разбор обязан читать оба,
// иначе смена способа выгрузки ломает импорт.
var rowSeparators = regexp.MustCompile(`(?i)</tr>|</li>|<br\s*/?>|\r?\n`)

// urlPattern находит адрес статьи в строке.
var urlPattern = regexp.MustCompile(`https?://[^\s"'<>)\]]+`)

// indexPattern находит индекс — целое число, стоящее в строке до адреса.
var indexPattern = regexp.MustCompile(`\d+`)

// ParseSources читает вход задачи: индексы и ссылки.
//
// Разбор намеренно не привязан к разметке: файл режется на строки по границам строк таблицы
// и переводам строки, а в каждой берётся первое число до первой ссылки. Так одинаково
// читаются выгрузка таблицы в HTML, CSV и просто список «12 https://…». Требование одно и
// проверяемое: в строке есть и число, и ссылка — иначе строка названа в ошибке, а не
// пропущена молча, потому что пропущенная статья видна только по её отсутствию в блоге.
func ParseSources(content string) ([]Source, error) {
	rows := rowSeparators.Split(content, -1)
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, stripTags(row))
	}
	return sourcesFromLines(lines)
}

// sourcesFromLines собирает вход из уже разрезанных строк.
//
// Точка сборки одна на все форматы: книгу Excel и текстовый файл различает только способ
// получить строки, а правило «первое число до первой ссылки» у них общее — второй копии
// этого правила в проекте быть не должно.
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

// tagPattern снимает разметку, оставляя текст ячеек. Адреса при этом не теряются: в
// href="…" ссылка стоит внутри тега, поэтому теги заменяются пробелом, а не вырезаются.
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

// slugFromURL вынимает слаг из адреса статьи.
//
// Слаг — последний непустой сегмент пути. Он же имя каталога артефактов и он же то, чем
// статья ищется в блоге: заголовок для поиска не годится, потому что его задача и меняет.
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

// ParseWorkbook читает вход из книги Excel.
//
// Разбор тот же, что у текстового файла: строка книги склеивается в одну строку и идёт через
// общее правило. Отдельно добавляются адреса гиперссылок — в книге ссылка нередко спрятана
// под текстом ячейки, и без них строка выглядела бы как «12 Медсестра» без адреса.
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

// documentationFile отвечает, описывает ли файл сам каталог, а не пачку статей.
//
// README лежит рядом со входом у всех задач правки и объясняет, что сюда класть. Входным
// файлом он не является ни при каком расширении, поэтому в выбор не попадает: иначе каталог
// с README и книгой считается неоднозначным, и импорт не начинается вовсе.
func documentationFile(name string) bool {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	return strings.EqualFold(base, "README")
}

// ResolveInputFile выбирает единственный входной файл задачи.
//
// Правило то же, что у книги Excel у остальных задач: каталог задаёт профиль, имя файла не
// фиксируется, явный путь его перекрывает. Файлов в каталоге должно быть ровно столько,
// чтобы выбор был однозначен — иначе прогон пошёл бы по вчерашнему списку.
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

// ReadSources читает и разбирает входной файл целиком.
//
// Формат выбирается по расширению: книга Excel читается excelize, всё остальное — как текст.
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
