package importer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/article"
	"github.com/xuri/excelize/v2"
)

const (
	// Имя листа, которое ожидаем по умолчанию.
	defaultSheetName = "Лист1"

	// Для MVP читаем только первые две статьи.
	defaultLimit = 2
)

// ReadArticles открывает Excel-файл, проверяет его структуру
// и возвращает первые две статьи для дальнейшей обработки.
func ReadArticles(path string) ([]article.Input, error) {
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

	// Заранее выделяем память под две статьи.
	articles := make([]article.Input, 0, defaultLimit)

	// Начинаем со второй строки, так как первая содержит заголовки.
	for rowIndex := 1; rowIndex < len(rows) && len(articles) < defaultLimit; rowIndex++ {
		row := rows[rowIndex]
		if isEmptyRow(row) {
			continue
		}

		// ID обязателен для каждой непустой строки.
		idText := cellValue(row, columnIndexes["id"])
		if strings.TrimSpace(idText) == "" {
			return nil, fmt.Errorf("строка %d: обязательная колонка %q пуста", rowIndex+1, "id")
		}

		// Преобразуем ID из строки в число.
		id, err := strconv.Atoi(strings.TrimSpace(idText))
		if err != nil {
			return nil, fmt.Errorf(
				"строка %d: некорректный id %q: %w",
				rowIndex+1,
				idText,
				err,
			)
		}

		// Заполняем доменную структуру данными из Excel.
		title := cellValue(row, columnIndexes["article_name"])
		if title == "" {
			return nil, fmt.Errorf("строка %d: обязательная колонка %q пуста", rowIndex+1, "article_name")
		}

		input := article.Input{
			ExcelID:         id,
			Title:           title,
			Header:          optionalCellValue(row, columnIndexes, "header"),
			ImageSlug:       optionalCellValue(row, columnIndexes, "image_slug"),
			MetaDescription: optionalCellValue(row, columnIndexes, "meta_description"),
			Keyword:         optionalCellValue(row, columnIndexes, "key_word"),
			ReferenceURL:    optionalCellValue(row, columnIndexes, "reference_url"),
			Category:        optionalCellValue(row, columnIndexes, "category"),
			Author:          optionalCellValue(row, columnIndexes, "authors"),
			Links:           optionalCellValue(row, columnIndexes, "links"),
			Professions:     optionalCellValue(row, columnIndexes, "professions"),
		}

		// Добавляем статью в результат.
		articles = append(articles, input)
	}

	// Если после обработки не нашли ни одной статьи — возвращаем ошибку.
	if len(articles) == 0 {
		return nil, fmt.Errorf("не найдено ни одной строки со статьями")
	}

	return articles, nil
}

// buildColumnIndexes проверяет наличие всех обязательных колонок
// и строит карту "имя колонки -> индекс".
func buildColumnIndexes(headerRow []string) (map[string]int, error) {
	requiredColumns := []string{
		"id",
		"article_name",
	}

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
