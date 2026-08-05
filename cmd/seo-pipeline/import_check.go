package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/importer"
)

// importIssue — одна найденная проблема переноса. Field, Excel и Stored заполняются, когда
// значение в базе разошлось со строкой Excel.
type importIssue struct {
	Text   string
	Field  string
	Excel  string
	Stored string
}

// importArticleReport — результат проверки одной статьи.
type importArticleReport struct {
	ExternalID string
	Title      string
	Issues     []importIssue
}

// ok reports that the article survived the import unchanged.
func (r importArticleReport) ok() bool { return len(r.Issues) == 0 }

// importReport — результат проверки всего импорта.
type importReport struct {
	ExcelPath      string
	ExcelRows      int
	Articles       int
	ArticleInputs  int
	MissingInDB    []string
	MissingInExcel []string
	GapIDs         []int
	Reports        []importArticleReport
}

// countsMatch reports that Excel, articles and article_inputs agree in size.
func (r importReport) countsMatch() bool {
	return r.ExcelRows == r.Articles && r.Articles == r.ArticleInputs
}

// failed returns the external ids of articles with problems, in report order.
func (r importReport) failed() []string {
	ids := make([]string, 0, len(r.Reports))
	for _, report := range r.Reports {
		if !report.ok() {
			ids = append(ids, report.ExternalID)
		}
	}
	return ids
}

// comparedField — поле Excel и его пара в базе. Сравниваются все поля, которые импортёр
// переносит: расхождение в любом из них означает дефект переноса.
type comparedField struct {
	Name  string
	Excel func(article.Input) string
	Store func(article.ImportedArticle) string
}

func comparedFields() []comparedField {
	return []comparedField{
		{"title", func(in article.Input) string { return in.Title },
			func(im article.ImportedArticle) string { return im.Article.Title }},
		{"header", func(in article.Input) string { return in.Header },
			func(im article.ImportedArticle) string { return im.Input.Header }},
		{"key_word", func(in article.Input) string { return in.Keyword },
			func(im article.ImportedArticle) string { return im.Input.Keyword }},
		{"image_slug", func(in article.Input) string { return in.ImageSlug },
			func(im article.ImportedArticle) string { return im.Input.ImageSlug }},
		{"reference_url", func(in article.Input) string { return in.ReferenceURL },
			func(im article.ImportedArticle) string { return im.Input.ReferenceURL }},
		{"category", func(in article.Input) string { return in.Category },
			func(im article.ImportedArticle) string { return im.Input.Category }},
		{"author", func(in article.Input) string { return in.Author },
			func(im article.ImportedArticle) string { return im.Input.Author }},
		{"meta_description", func(in article.Input) string { return in.MetaDescription },
			func(im article.ImportedArticle) string { return im.Input.MetaDescription }},
		{"links", func(in article.Input) string { return in.Links },
			func(im article.ImportedArticle) string { return im.Input.Links }},
		{"professions", func(in article.Input) string { return in.Professions },
			func(im article.ImportedArticle) string { return im.Input.Professions }},
	}
}

// checkImport сверяет перенос Excel → PostgreSQL. Функция чистая: ни базы, ни файлов,
// ни внешних вызовов — только сравнение уже прочитанных данных.
func checkImport(rows []importer.Row, imported []article.ImportedArticle) importReport {
	excelRows := make(map[string]importer.Row, len(rows))
	excelOrder := make([]string, 0, len(rows))
	report := importReport{}
	for _, row := range rows {
		if row.Empty {
			continue
		}
		report.ExcelRows++
		if row.ExternalID == "" {
			continue
		}
		if _, duplicate := excelRows[row.ExternalID]; !duplicate {
			excelOrder = append(excelOrder, row.ExternalID)
		}
		excelRows[row.ExternalID] = row
	}

	stored := make(map[string]article.ImportedArticle, len(imported))
	report.Articles = len(imported)
	for _, item := range imported {
		stored[item.Article.ExternalID] = item
		if item.HasInput {
			report.ArticleInputs++
		}
	}

	for _, externalID := range excelOrder {
		row := excelRows[externalID]
		item, found := stored[externalID]
		if !found {
			report.MissingInDB = append(report.MissingInDB, externalID)
			report.Reports = append(report.Reports, importArticleReport{
				ExternalID: externalID, Title: row.Title,
				Issues: []importIssue{{Text: "строка Excel не импортирована в PostgreSQL"}},
			})
			continue
		}
		report.Reports = append(report.Reports, checkImportedArticle(row, item))
	}
	for _, item := range imported {
		if _, found := excelRows[item.Article.ExternalID]; found {
			continue
		}
		report.MissingInExcel = append(report.MissingInExcel, item.Article.ExternalID)
		report.Reports = append(report.Reports, importArticleReport{
			ExternalID: item.Article.ExternalID, Title: item.Article.Title,
			Issues: []importIssue{{Text: "статья есть в PostgreSQL, но её строки нет в Excel"}},
		})
	}
	report.GapIDs = missingIDRange(excelOrder, stored)
	return report
}

// checkImportedArticle сверяет одну строку Excel с тем, что лежит в базе.
func checkImportedArticle(row importer.Row, item article.ImportedArticle) importArticleReport {
	report := importArticleReport{ExternalID: item.Article.ExternalID, Title: item.Article.Title}
	if len(row.Errors) > 0 {
		report.Issues = append(report.Issues, importIssue{
			Text: "строка Excel прочитана с ошибками: " + strings.Join(row.Errors, "; "),
		})
	}
	if !item.HasInput {
		report.Issues = append(report.Issues, importIssue{Text: "нет строки article_inputs"})
		return report
	}
	for _, field := range comparedFields() {
		excelValue := strings.TrimSpace(field.Excel(row.Input))
		storedValue := strings.TrimSpace(field.Store(item))
		if excelValue == storedValue {
			continue
		}
		text := fmt.Sprintf("%s отличается от Excel", field.Name)
		if storedValue == "" {
			text = fmt.Sprintf("%s не перенесён из Excel", field.Name)
		}
		report.Issues = append(report.Issues, importIssue{
			Text: text, Field: field.Name, Excel: excelValue, Stored: storedValue,
		})
	}
	return report
}

// missingIDRange возвращает числовые идентификаторы, которых нет ни в Excel, ни в базе,
// внутри диапазона от минимального до максимального.
func missingIDRange(excelIDs []string, stored map[string]article.ImportedArticle) []int {
	present := make(map[int]struct{}, len(excelIDs)+len(stored))
	minID, maxID := 0, 0
	add := func(value string) {
		number, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || number <= 0 {
			return
		}
		present[number] = struct{}{}
		if minID == 0 || number < minID {
			minID = number
		}
		if number > maxID {
			maxID = number
		}
	}
	for _, value := range excelIDs {
		add(value)
	}
	for externalID := range stored {
		add(externalID)
	}
	if minID == 0 {
		return nil
	}
	var gaps []int
	for number := minID; number <= maxID; number++ {
		if _, found := present[number]; !found {
			gaps = append(gaps, number)
		}
	}
	sort.Ints(gaps)
	return gaps
}

// renderImportReport печатает отчёт по всем статьям.
func renderImportReport(writer io.Writer, report importReport) {
	fmt.Fprintln(writer, "IMPORT CHECK")
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "Excel: %s\n", report.ExcelPath)
	fmt.Fprintf(writer, "Строк Excel: %d\n", report.ExcelRows)
	fmt.Fprintf(writer, "articles: %d\n", report.Articles)
	fmt.Fprintf(writer, "article_inputs: %d\n", report.ArticleInputs)
	if !report.countsMatch() {
		fmt.Fprintln(writer, "❌ Импорт неполный: количества не совпадают")
	}
	if len(report.GapIDs) > 0 {
		fmt.Fprintf(writer, "Пропуски в диапазоне ID: %s\n", joinInts(report.GapIDs))
	}
	fmt.Fprintln(writer)
	for _, item := range report.Reports {
		renderImportArticle(writer, item)
	}
	renderImportTotals(writer, report)
}

// renderImportArticle печатает результат одной статьи.
func renderImportArticle(writer io.Writer, report importArticleReport) {
	mark := "✅"
	if !report.ok() {
		mark = "❌"
	}
	fmt.Fprintf(writer, "%s external_id=%s\n", mark, report.ExternalID)
	fmt.Fprintln(writer, "Название:")
	fmt.Fprintln(writer, orPlaceholder(report.Title, "<пусто>"))
	if report.ok() {
		fmt.Fprintln(writer, "Статус:")
		fmt.Fprintln(writer, "OK")
		fmt.Fprintln(writer)
		return
	}
	fmt.Fprintln(writer, "Проблемы:")
	for _, issue := range report.Issues {
		fmt.Fprintf(writer, "- %s\n", issue.Text)
		if issue.Field == "" {
			continue
		}
		fmt.Fprintf(writer, "  Excel: %s\n", orPlaceholder(issue.Excel, "<пусто>"))
		fmt.Fprintf(writer, "  БД:    %s\n", orPlaceholder(issue.Stored, "<пусто>"))
	}
	fmt.Fprintln(writer)
}

// renderImportTotals печатает итог и список проблемных статей.
func renderImportTotals(writer io.Writer, report importReport) {
	failed := report.failed()
	fmt.Fprintln(writer, "ИТОГ")
	fmt.Fprintf(writer, "Всего: %d\n", len(report.Reports))
	fmt.Fprintf(writer, "OK: %d\n", len(report.Reports)-len(failed))
	fmt.Fprintf(writer, "Ошибки: %d\n", len(failed))
	if len(failed) > 0 {
		fmt.Fprintf(writer, "Статьи с ошибками: %s\n", strings.Join(failed, ", "))
	}
}

// renderImportArticleReport печатает результат одной статьи для режима с external_id.
func renderImportArticleReport(writer io.Writer, report importReport, externalID string) error {
	for _, item := range report.Reports {
		if item.ExternalID == externalID {
			renderImportArticle(writer, item)
			return nil
		}
	}
	return fmt.Errorf("статья с external_id %q не найдена ни в Excel, ни в PostgreSQL", externalID)
}

func orPlaceholder(value, placeholder string) string {
	if strings.TrimSpace(value) == "" {
		return placeholder
	}
	return value
}

func joinInts(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ", ")
}

// importCheckRepository читает импортированные статьи. Ничего не пишет.
type importCheckRepository interface {
	ListImportedArticles(ctx context.Context) ([]article.ImportedArticle, error)
}

// runImportCheck сверяет перенос Excel → PostgreSQL и печатает отчёт. Команда только
// читает: ни базы не меняет, ни внешних сервисов не вызывает.
func runImportCheck(
	ctx context.Context,
	repository importCheckRepository,
	inputFilePath string,
	writer io.Writer,
	externalID string,
) error {
	rows, err := importer.ReadRows(inputFilePath)
	if err != nil {
		return err
	}
	imported, err := repository.ListImportedArticles(ctx)
	if err != nil {
		return err
	}
	report := checkImport(rows, imported)
	report.ExcelPath = inputFilePath
	if externalID != "" {
		return renderImportArticleReport(writer, report, externalID)
	}
	renderImportReport(writer, report)
	return nil
}
