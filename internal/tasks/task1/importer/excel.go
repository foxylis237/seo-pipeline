package importer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/xuri/excelize/v2"
)

const defaultSheetName = "Лист1"

type Row struct {
	Number     int
	ExternalID string
	Title      string
	Input      article.Input
	Empty      bool
	Errors     []string
}

// ReadArticles открывает Excel-файл и возвращает все заполненные строки данных.
func ReadArticles(path string) ([]article.Input, error) {
	return ReadArticlesWithLimit(path, 0)
}

// ReadArticlesWithLimit возвращает не больше limit строк данных.
// Нулевой limit означает отсутствие ограничения.
func ReadArticlesWithLimit(path string, limit int) ([]article.Input, error) {
	if limit < 0 {
		return nil, fmt.Errorf("лимит строк не может быть отрицательным")
	}
	rows, err := ReadRows(path)
	if err != nil {
		return nil, err
	}
	articles := make([]article.Input, 0)
	for _, row := range rows {
		if row.Empty {
			continue
		}
		if len(row.Errors) > 0 {
			return nil, fmt.Errorf("строка %d: %s", row.Number, strings.Join(row.Errors, "; "))
		}
		articles = append(articles, row.Input)
		if limit > 0 && len(articles) >= limit {
			break
		}
	}
	if len(articles) == 0 {
		return nil, fmt.Errorf("не найдено ни одной строки со статьями")
	}
	return articles, nil
}

// ReadRows читает все строки данных, сохраняя ошибки отдельных строк в результате.
func ReadRows(path string) ([]Row, error) {
	// Открываем Excel-файл.
	file, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("открыть Excel-файл %q: %w", path, err)
	}

	// Закрываем файл при выходе из функции.
	defer func() {
		_ = file.Close()
	}()

	// Пытаемся открыть лист с ожидаемым именем.
	sheetName := defaultSheetName

	// Если такого листа нет, используем первый найденный.
	if index, err := file.GetSheetIndex(sheetName); err != nil || index == -1 {
		sheets := file.GetSheetList()
		if len(sheets) == 0 {
			return nil, fmt.Errorf("в Excel-файле нет листов")
		}

		sheetName = sheets[0]
	}

	// Получаем все строки выбранного листа.
	rows, err := file.GetRows(sheetName)
	if err != nil {
		return nil, fmt.Errorf("прочитать лист %q: %w", sheetName, err)
	}

	// Первая строка — заголовки, поэтому данных должно быть минимум две строки.
	if len(rows) < 2 {
		return nil, fmt.Errorf("в Excel-файле нет строк с данными")
	}

	// Строим карту "имя колонки -> индекс".
	columnIndexes, err := buildColumnIndexes(rows[0])
	if err != nil {
		return nil, err
	}

	result := make([]Row, 0, len(rows)-1)
	seenExternalIDs := make(map[int]int)

	// Начинаем со второй строки, так как первая содержит заголовки.
	for rowIndex := 1; rowIndex < len(rows); rowIndex++ {
		row := rows[rowIndex]
		if isEmptyRow(row) {
			result = append(result, Row{Number: rowIndex + 1, Empty: true})
			continue
		}

		idText := cellValue(row, columnIndexes["id"])
		title := cellValue(row, columnIndexes["article_name"])
		parsed := Row{Number: rowIndex + 1, ExternalID: idText, Title: title}
		if isMissingRequired(idText) {
			parsed.Errors = append(parsed.Errors, `поле "id": отсутствует или пусто`)
		}
		id, idErr := strconv.Atoi(strings.TrimSpace(idText))
		if !isMissingRequired(idText) && (idErr != nil || id <= 0) {
			parsed.Errors = append(parsed.Errors, `поле "id": должно быть положительным целым числом`)
		}
		if isMissingRequired(title) {
			parsed.Errors = append(parsed.Errors, `поле "article_name": отсутствует или пусто`)
		}
		imageSlug := optionalCellValue(row, columnIndexes, "image_slug")
		if isMissingRequired(imageSlug) {
			parsed.Errors = append(parsed.Errors, `поле "image_slug": отсутствует или пусто`)
		}
		referenceURL := optionalCellValue(row, columnIndexes, "reference_url")
		if isMissingRequired(referenceURL) {
			parsed.Errors = append(parsed.Errors, `поле "reference_url": отсутствует или пусто`)
		}
		if idErr == nil && id > 0 {
			if previousRow, found := seenExternalIDs[id]; found {
				parsed.Errors = append(parsed.Errors, fmt.Sprintf("поле \"id\": дубликат строки %d", previousRow))
			} else if len(parsed.Errors) == 0 {
				seenExternalIDs[id] = rowIndex + 1
			}
		}

		parsed.Input = article.Input{
			ExcelID:         id,
			Title:           title,
			Header:          optionalCellValue(row, columnIndexes, "header"),
			ImageSlug:       imageSlug,
			MetaDescription: optionalCellValue(row, columnIndexes, "meta_description"),
			Keyword:         optionalCellValue(row, columnIndexes, "key_word"),
			ReferenceURL:    referenceURL,
			Category:        optionalCellValue(row, columnIndexes, "category"),
			Author:          optionalCellValue(row, columnIndexes, "authors"),
			Links:           optionalCellValue(row, columnIndexes, "links"),
			Professions:     optionalCellValue(row, columnIndexes, "professions"),
		}

		result = append(result, parsed)
	}
	return result, nil
}

// buildColumnIndexes проверяет наличие всех обязательных колонок
// и строит карту "имя колонки -> индекс".
func buildColumnIndexes(headerRow []string) (map[string]int, error) {
	requiredColumns := []string{"id", "article_name", "image_slug", "reference_url"}

	// Карта для быстрого поиска индекса колонки по её имени.
	indexes := make(map[string]int, len(headerRow))

	// Проходим по строке заголовков.
	for index, value := range headerRow {
		// Убираем пробелы и приводим название к нижнему регистру.
		columnName := strings.TrimSpace(strings.ToLower(value))

		// Поддерживаем старую опечатку в таблице.
		if columnName == "referense_url" {
			columnName = "reference_url"
		}
		if columnName == "" {
			continue
		}

		if previousIndex, exists := indexes[columnName]; exists {
			return nil, fmt.Errorf(
				"повторяющийся заголовок %q в колонках %d и %d",
				columnName,
				previousIndex+1,
				index+1,
			)
		}
		indexes[columnName] = index
	}

	// Проверяем наличие всех обязательных колонок.
	for _, column := range requiredColumns {
		if _, ok := indexes[column]; !ok {
			return nil, fmt.Errorf(
				"в Excel отсутствует обязательная колонка %q",
				column,
			)
		}
	}

	return indexes, nil
}

func isMissingRequired(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || strings.EqualFold(value, "null")
}

func optionalCellValue(row []string, indexes map[string]int, column string) string {
	index, ok := indexes[column]
	if !ok {
		return ""
	}
	return cellValue(row, index)
}

func isEmptyRow(row []string) bool {
	for _, value := range row {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}

// cellValue безопасно возвращает значение ячейки по индексу.
// Если индекс выходит за границы строки, возвращается пустая строка.
func cellValue(row []string, index int) string {
	if index < 0 || index >= len(row) {
		return ""
	}

	return strings.TrimSpace(row[index])
}
