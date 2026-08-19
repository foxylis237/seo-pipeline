package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestProjectInputWorkbookIsReadable(t *testing.T) {
	path := filepath.Join("..", "..", "..", "input", "task_1", "input.xlsx")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("project input workbook %q is unavailable: %v", path, err)
	}

	rows, err := ReadRows(path)
	if err != nil {
		t.Fatalf("read project input workbook %q: %v", path, err)
	}
	hasValidArticle := false
	for _, row := range rows {
		if !row.Empty && len(row.Errors) == 0 {
			hasValidArticle = true
			break
		}
	}
	if !hasValidArticle {
		t.Fatal("project input workbook contains no importable articles")
	}
}

func TestReadArticlesRejectsEmptyTitle(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{1, "   ", "slug", "https://example.test"},
	})

	_, err := ReadArticles(path)
	if err == nil || !strings.Contains(err.Error(), `поле "article_name": отсутствует или пусто`) {
		t.Fatalf("ожидалась ошибка пустого названия, получено: %v", err)
	}
}

func TestReadArticlesRejectsMissingRequiredColumn(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "header"},
		{1, "Заголовок"},
	})

	_, err := ReadArticles(path)
	if err == nil || !strings.Contains(err.Error(), `отсутствует обязательная колонка "article_name"`) {
		t.Fatalf("ожидалась ошибка отсутствующей колонки, получено: %v", err)
	}
}

func TestReadArticlesRejectsDuplicateHeaders(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", " Article_Name ", "image_slug", "reference_url"},
		{1, "Статья", "Дубликат", "slug", "https://example.test"},
	})

	_, err := ReadArticles(path)
	if err == nil || !strings.Contains(err.Error(), `повторяющийся заголовок "article_name" в колонках 2 и 3`) {
		t.Fatalf("ожидалась ошибка повторяющегося заголовка, получено: %v", err)
	}
}

func TestReadArticlesAllowsMissingOptionalColumns(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{7, "  Тестовая статья  ", "slug", "https://example.test"},
	})

	articles, err := ReadArticles(path)
	if err != nil {
		t.Fatalf("прочитать файл: %v", err)
	}
	if len(articles) != 1 {
		t.Fatalf("ожидалась одна статья, получено: %d", len(articles))
	}
	if articles[0].Title != "Тестовая статья" || articles[0].Header != "" {
		t.Fatalf("неожиданные данные статьи: %+v", articles[0])
	}
}

func TestReadRowsReportsDuplicateExternalIDAndContinues(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{7, "Первая статья", "first", "https://example.test/1"},
		{7, "Вторая статья", "second", "https://example.test/2"},
		{8, "Третья статья", "third", "https://example.test/3"},
	})

	rows, err := ReadRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || len(rows[1].Errors) != 1 || !strings.Contains(rows[1].Errors[0], "дубликат строки 2") {
		t.Fatalf("дубликат не отмечен: %+v", rows)
	}
	if len(rows[2].Errors) != 0 || rows[2].Input.ExcelID != 8 {
		t.Fatalf("чтение не продолжилось после дубликата: %+v", rows[2])
	}
}

func TestReadRowsListsEveryInvalidRequiredField(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{"NULL", "  ", "null", ""},
	})
	rows, err := ReadRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Errors) != 4 {
		t.Fatalf("ошибки обязательных полей: %+v", rows)
	}
}

func TestReadArticlesWithoutLimitReadsEveryDataRowIncludingLast(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{1, "Первая", "one", "https://example.test/1"},
		{2, "Вторая", "two", "https://example.test/2"},
		{3, "Последняя", "three", "https://example.test/3"},
	})

	articles, err := ReadArticles(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(articles) != 3 || articles[2].ExcelID != 3 || articles[2].Title != "Последняя" {
		t.Fatalf("прочитаны не все строки: %+v", articles)
	}
}

func TestReadArticlesLimitCountsDataRowsNotHeader(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{1, "Первая", "one", "https://example.test/1"},
		{2, "Вторая", "two", "https://example.test/2"},
		{3, "Третья", "three", "https://example.test/3"},
	})

	articles, err := ReadArticlesWithLimit(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(articles) != 2 || articles[0].ExcelID != 1 || articles[1].ExcelID != 2 {
		t.Fatalf("лимит применён неверно: %+v", articles)
	}
}

func TestReadArticlesLimitLargerThanFileReadsToEnd(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{1, "Первая", "one", "https://example.test/1"},
		{2, "Последняя", "two", "https://example.test/2"},
	})

	articles, err := ReadArticlesWithLimit(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(articles) != 2 || articles[1].ExcelID != 2 {
		t.Fatalf("ожидались обе строки: %+v", articles)
	}
}

func TestReadArticlesLimitSkipsEmptyRows(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{1, "Первая", "one", "https://example.test/1"},
		{"", "", "", ""},
		{2, "Вторая", "two", "https://example.test/2"},
	})

	articles, err := ReadArticlesWithLimit(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(articles) != 2 || articles[1].ExcelID != 2 {
		t.Fatalf("пустая строка вошла в лимит: %+v", articles)
	}
}

func writeWorkbook(t *testing.T, rows [][]any) string {
	t.Helper()

	file := excelize.NewFile()
	defer func() { _ = file.Close() }()

	for rowIndex, row := range rows {
		cell, err := excelize.CoordinatesToCellName(1, rowIndex+1)
		if err != nil {
			t.Fatalf("получить адрес ячейки: %v", err)
		}
		if err := file.SetSheetRow("Sheet1", cell, &row); err != nil {
			t.Fatalf("записать строку: %v", err)
		}
	}

	path := filepath.Join(t.TempDir(), "input.xlsx")
	if err := file.SaveAs(path); err != nil {
		t.Fatalf("сохранить тестовый Excel: %v", err)
	}
	return path
}

// Метки — готовые данные Excel, а не результат генерации. Колонка необязательна: её
// отсутствие не должно ломать импорт, потому что в рабочем файле её может ещё не быть.
func TestReadRowsReadsTagsColumn(t *testing.T) {
	withTags := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url", "tags"},
		{"37", "Как стать логопедом", "kak-stat-logopedom", "https://example.test/a", "Логопед, Переподготовка, Как стать"},
	})
	rows, err := ReadRows(withTags)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Errors) != 0 {
		t.Fatalf("строка не импортирована: %+v", rows)
	}
	if rows[0].Input.Tags != "Логопед, Переподготовка, Как стать" {
		t.Fatalf("метки не прочитаны: %q", rows[0].Input.Tags)
	}

	withoutTags := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{"38", "Как стать поваром", "kak-stat-povarom", "https://example.test/b"},
	})
	rows, err = ReadRows(withoutTags)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Errors) != 0 {
		t.Fatalf("файл без колонки tags должен импортироваться: %+v", rows)
	}
	if rows[0].Input.Tags != "" {
		t.Fatalf("метки взялись ниоткуда: %q", rows[0].Input.Tags)
	}
}

// Книга pprof_2 называет часть колонок по-русски, а «преподаватели» приходят той же колонкой
// authors, что и автор. Разбирает это таблица псевдонимов: заголовки пишет человек, и они уже
// разошлись между задачами.
func TestReadRowsReadsPProf2Columns(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "header", "image_slug", "сео-заголовок", "meta_description",
			"key_word", "reference_url", "category", "authors", "раздел", "profession"},
		{"5", "Обучение на стропальщика", "Обучение на стропальщика", "obuchenie-na-stropalshchika",
			"Обучение на стропальщика — курсы и удостоверение", "Дистанционное обучение стропальщиков",
			"обучение на стропальщика", "https://example.test/a", "Рабочие профессии",
			"Иванов И. И.", "Профессиональное обучение", "Стропальщик"},
	})
	rows, err := ReadRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Errors) != 0 {
		t.Fatalf("строка не импортирована: %+v", rows)
	}
	input := rows[0].Input
	for name, got := range map[string]string{
		"seo_title":  input.SEOTitle,
		"section":    input.Section,
		"profession": input.Profession,
		"teachers":   input.Teachers,
	} {
		if strings.TrimSpace(got) == "" {
			t.Fatalf("поле %s не прочитано из книги", name)
		}
	}
	if input.SEOTitle != "Обучение на стропальщика — курсы и удостоверение" {
		t.Fatalf("сео-заголовок: %q", input.SEOTitle)
	}
	if input.Section != "Профессиональное обучение" {
		t.Fatalf("раздел: %q", input.Section)
	}
	if input.Profession != "Стропальщик" {
		t.Fatalf("профессия: %q", input.Profession)
	}
	// Одна колонка Excel, два поля модели: как её называет задача — вопрос её единого языка.
	if input.Teachers != input.Author || input.Teachers != "Иванов И. И." {
		t.Fatalf("преподаватели %q, автор %q", input.Teachers, input.Author)
	}
}

// Книга pprof_2 называет slug и преподавателей по-своему, а короткое название услуги несёт
// отдельной колонкой рядом с полным названием страницы. Обе попадают в строку: заголовком
// статьи остаётся article_name, service_name живёт своим полем.
func TestReadRowsReadsPProf2Workbook(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "service_name", "slug", "header", "seo_title", "meta_description",
			"key_word", "reference_url", "category", " teachers", "section", "profession"},
		{"1", "Медицинский массаж — дистанционное обучение", "Медицинский массаж",
			"medicinskij-massazh", "Курсы массажиста", "Медицинский массаж — курс с дипломом",
			"Обучение дистанционно с практикой", "медицинский массаж",
			"https://example.test/a", "Обучение медперсонала", "Перов Андрей Валерьевич",
			"Медицина", "Массажист"},
	})
	rows, err := ReadRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Errors) != 0 {
		t.Fatalf("строка не импортирована: %+v", rows)
	}
	input := rows[0].Input
	if input.Title != "Медицинский массаж — дистанционное обучение" {
		t.Fatalf("заголовок статьи: %q", input.Title)
	}
	if input.ServiceName != "Медицинский массаж" {
		t.Fatalf("название услуги: %q", input.ServiceName)
	}
	// slug книги — это image_slug движка: имя каталога артефактов берётся из него.
	if input.ImageSlug != "medicinskij-massazh" {
		t.Fatalf("слаг картинки: %q", input.ImageSlug)
	}
	// Колонка teachers книги — та же authors: одна колонка, два поля модели.
	if input.Teachers != "Перов Андрей Валерьевич" || input.Author != input.Teachers {
		t.Fatalf("преподаватели %q, автор %q", input.Teachers, input.Author)
	}
}

// Русский синоним колонки услуги разбирается той же таблицей: заголовок пишет человек, и
// называть её он может по-русски.
func TestReadRowsReadsServiceNameAlias(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "Услуга-нейм", "image_slug", "reference_url"},
		{"1", "Медицинский массаж — дистанционное обучение", "Медицинский массаж",
			"medicinskij-massazh", "https://example.test/a"},
	})
	rows, err := ReadRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Errors) != 0 {
		t.Fatalf("строка не импортирована: %+v", rows)
	}
	if rows[0].Input.ServiceName != "Медицинский массаж" {
		t.Fatalf("название услуги: %q", rows[0].Input.ServiceName)
	}
}

// Слаг остаётся обязательным под любым из двух имён: без него у статьи нет каталога.
func TestReadArticlesRequiresSlugUnderBookName(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "slug", "reference_url"},
		{"1", "Медицинский массаж", "", "https://example.test/a"},
	})
	_, err := ReadArticles(path)
	if err == nil || !strings.Contains(err.Error(), `поле "image_slug": отсутствует или пусто`) {
		t.Fatalf("ожидалась ошибка пустого слага, получено: %v", err)
	}
}

// Книга без этих колонок обязана импортироваться как прежде: у task_1 и pprof_1 их нет, и
// появление новых полей не имеет права ломать их импорт.
func TestReadRowsWithoutPProf2ColumnsStaysValid(t *testing.T) {
	path := writeWorkbook(t, [][]any{
		{"id", "article_name", "image_slug", "reference_url"},
		{"37", "Как стать логопедом", "kak-stat-logopedom", "https://example.test/a"},
	})
	rows, err := ReadRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Errors) != 0 {
		t.Fatalf("книга без новых колонок не импортирована: %+v", rows)
	}
	input := rows[0].Input
	if input.SEOTitle != "" || input.Section != "" || input.Profession != "" || input.Teachers != "" {
		t.Fatalf("поля взялись ниоткуда: %+v", input)
	}
}
