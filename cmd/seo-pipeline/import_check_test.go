package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/importer"
)

func excelRow(number int, externalID, title string) importer.Row {
	return importer.Row{
		Number: number, ExternalID: externalID, Title: title,
		Input: article.Input{
			Title: title, Header: "Заголовок " + externalID, ImageSlug: "slug-" + externalID,
			MetaDescription: "описание " + externalID, Keyword: "ключ " + externalID,
			ReferenceURL: "https://example.test/" + externalID, Category: "Педагогика",
			Author: "Иванова", Links: "/professions/" + externalID, Professions: "Профессия",
		},
	}
}

func importedFromRow(row importer.Row) article.ImportedArticle {
	return article.ImportedArticle{
		Article:  article.Article{ExternalID: row.ExternalID, Title: row.Input.Title, Status: "pending"},
		Input:    row.Input,
		HasInput: true,
	}
}

func TestCheckImportAcceptsExactTransfer(t *testing.T) {
	rows := []importer.Row{excelRow(2, "37", "Как стать логопедом"), excelRow(3, "38", "Как стать поваром")}
	imported := []article.ImportedArticle{importedFromRow(rows[0]), importedFromRow(rows[1])}

	report := checkImport(rows, imported)
	if !report.countsMatch() {
		t.Fatalf("количества не совпали: excel=%d articles=%d inputs=%d",
			report.ExcelRows, report.Articles, report.ArticleInputs)
	}
	if failed := report.failed(); len(failed) != 0 {
		t.Fatalf("ошибки на корректном импорте: %v", failed)
	}
}

func TestCheckImportFindsShiftedFields(t *testing.T) {
	rows := []importer.Row{excelRow(2, "37", "Как стать логопедом")}
	imported := []article.ImportedArticle{importedFromRow(rows[0])}
	// Данные соседней статьи попали в строку этой: ровно тот сдвиг, который ищем.
	imported[0].Input.Keyword = "инструктор по физической культуре"
	imported[0].Input.ReferenceURL = ""

	report := checkImport(rows, imported)
	failed := report.failed()
	if !reflect.DeepEqual(failed, []string{"37"}) {
		t.Fatalf("статьи с ошибками = %v", failed)
	}
	issues := report.Reports[0].Issues
	if len(issues) != 2 {
		t.Fatalf("найдено %d проблем: %+v", len(issues), issues)
	}
	if issues[0].Field != "key_word" || issues[0].Excel != "ключ 37" || issues[0].Stored != "инструктор по физической культуре" {
		t.Fatalf("проблема key_word = %+v", issues[0])
	}
	if issues[1].Field != "reference_url" || !strings.Contains(issues[1].Text, "не перенесён") {
		t.Fatalf("проблема reference_url = %+v", issues[1])
	}
}

func TestCheckImportFindsMissingArticleInputs(t *testing.T) {
	rows := []importer.Row{excelRow(2, "37", "Как стать логопедом")}
	imported := []article.ImportedArticle{importedFromRow(rows[0])}
	imported[0].HasInput = false
	imported[0].Input = article.Input{}

	report := checkImport(rows, imported)
	if report.ArticleInputs != 0 || report.countsMatch() {
		t.Fatalf("отсутствие строки ввода не отражено: inputs=%d", report.ArticleInputs)
	}
	issues := report.Reports[0].Issues
	if len(issues) != 1 || issues[0].Text != "нет строки article_inputs" {
		t.Fatalf("проблемы = %+v", issues)
	}
}

func TestCheckImportFindsRowsLostAndExtra(t *testing.T) {
	rows := []importer.Row{excelRow(2, "37", "Как стать логопедом"), excelRow(3, "38", "Как стать поваром")}
	// 38 не доехала до базы, зато в базе осталась 41 от прошлого файла.
	imported := []article.ImportedArticle{
		importedFromRow(rows[0]),
		{Article: article.Article{ExternalID: "41", Title: "Старая статья"}, HasInput: true},
	}

	report := checkImport(rows, imported)
	if !reflect.DeepEqual(report.MissingInDB, []string{"38"}) {
		t.Fatalf("потерянные при импорте = %v", report.MissingInDB)
	}
	if !reflect.DeepEqual(report.MissingInExcel, []string{"41"}) {
		t.Fatalf("лишние в базе = %v", report.MissingInExcel)
	}
	if failed := report.failed(); !reflect.DeepEqual(failed, []string{"38", "41"}) {
		t.Fatalf("статьи с ошибками = %v", failed)
	}
	if !reflect.DeepEqual(report.GapIDs, []int{39, 40}) {
		t.Fatalf("пропуски в диапазоне = %v", report.GapIDs)
	}
}

func TestCheckImportReportsExcelRowErrors(t *testing.T) {
	row := excelRow(2, "37", "Как стать логопедом")
	row.Errors = []string{`поле "id": дубликат строки 5`}
	imported := []article.ImportedArticle{importedFromRow(row)}

	report := checkImport([]importer.Row{row}, imported)
	issues := report.Reports[0].Issues
	if len(issues) != 1 || !strings.Contains(issues[0].Text, "дубликат строки 5") {
		t.Fatalf("проблемы = %+v", issues)
	}
}

func TestCheckImportSkipsEmptyExcelRows(t *testing.T) {
	rows := []importer.Row{excelRow(2, "37", "Тема"), {Number: 3, Empty: true}}
	report := checkImport(rows, []article.ImportedArticle{importedFromRow(rows[0])})
	if report.ExcelRows != 1 {
		t.Fatalf("строк Excel = %d, ожидалась одна", report.ExcelRows)
	}
}

func TestRenderImportReportListsFailedArticles(t *testing.T) {
	rows := []importer.Row{excelRow(2, "37", "Как стать логопедом"), excelRow(3, "38", "Как стать поваром")}
	imported := []article.ImportedArticle{importedFromRow(rows[0]), importedFromRow(rows[1])}
	imported[1].Input.Keyword = "чужой ключ"

	var output bytes.Buffer
	report := checkImport(rows, imported)
	report.ExcelPath = "input/task_1/input.xlsx"
	renderImportReport(&output, report)

	text := output.String()
	for _, want := range []string{
		"✅ external_id=37", "❌ external_id=38", "key_word отличается от Excel",
		"Excel: ключ 38", "БД:    чужой ключ",
		"Всего: 2", "OK: 1", "Ошибки: 1", "Статьи с ошибками: 38",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в отчёте нет %q:\n%s", want, text)
		}
	}
}

func TestRenderImportArticleReportPrintsOneArticle(t *testing.T) {
	rows := []importer.Row{excelRow(2, "37", "Как стать логопедом"), excelRow(3, "38", "Как стать поваром")}
	imported := []article.ImportedArticle{importedFromRow(rows[0]), importedFromRow(rows[1])}
	report := checkImport(rows, imported)

	var output bytes.Buffer
	if err := renderImportArticleReport(&output, report, "38"); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.Contains(text, "external_id=38") || strings.Contains(text, "external_id=37") {
		t.Fatalf("вывод одной статьи = %q", text)
	}
	if err := renderImportArticleReport(&output, report, "99"); err == nil {
		t.Fatal("несуществующая статья не дала ошибки")
	}
}
