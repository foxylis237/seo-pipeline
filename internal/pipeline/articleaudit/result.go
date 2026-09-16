package articleaudit

import (
	"fmt"
	"strconv"
	"strings"
)

// ReportData — поля шаблона result.md.
//
// Отчёт состоит из трёх вещей: оценки, самых важных ошибок и всех ошибок списком. Ни
// рекомендаций, ни разбора по критериям, ни сильных сторон в нём нет — их не читают, а
// оплачивать их генерацию и место в ответе незачем.
type ReportData struct {
	// Score — «14/20» либо слова о том, что оценки в ответе не нашлось. Первой строкой отчёта:
	// по ней страницы сравнивают между собой и решают, за какую браться.
	Score string
	// Шапка: что именно проверено.
	Title    string
	URL      string
	Topic    string
	PostID   string
	PostType string
	// RequiredFields — итог проверки обязательных полей одной строкой. Сами имена полей
	// печатаются в списке ошибок: незаполненное обязательное поле — такая же ошибка страницы,
	// как и остальные, и искать её человек будет там же.
	RequiredFields string
	// FAQ — сколько вопросов в блоке частых вопросов. Число, а не находка: сколько их должно
	// быть, никто не решал, и человек смотрит на него сам.
	FAQ string
	// Разделы отчёта. Пустых среди них не бывает: пустой блок печатается словами «замечаний
	// нет», а не пропускается, — человек должен видеть, что проверка была и ничего не нашла.
	Critical string
	Issues   string
	Unparsed string
}

// BuildReport собирает отчёт из прочитанной страницы, проверки полей и ответа модели.
//
// Содержимое разделов не переписывается и не переформатируется: ответ уходит в отчёт как
// пришёл.
func BuildReport(article Article, current Post, check FieldCheck, parsed Answer, required []string) ReportData {
	return ReportData{
		Score:          scoreLine(parsed),
		Title:          current.Title,
		URL:            linkOf(article, current),
		Topic:          orDefault(article.Topic, "не задана — основанием служил заголовок записи"),
		PostID:         strconv.FormatInt(current.ID, 10),
		PostType:       current.PostType,
		RequiredFields: requiredSummary(check, required),
		FAQ:            faqSummary(check.FAQ),
		Critical:       orDefault(parsed.Section(SectionCritical), noFindings),
		Issues:         issuesBlock(check, parsed.Section(SectionIssues)),
		Unparsed:       orDefault(parsed.Unparsed, "весь ответ разобран по разделам"),
	}
}

// linkOf — адрес страницы. Ссылка из блога точнее той, что записана во входном файле: слаг
// площадка могла дополнить сама. Её нет — печатаем то, что было в книге.
func linkOf(article Article, current Post) string {
	if strings.TrimSpace(current.Link) != "" {
		return current.Link
	}
	return article.SourceURL
}

// requiredSummary — строка шапки про обязательные поля.
//
// Выключенная проверка и пройденная проверка — разные состояния, и путать их нельзя: «не
// заполнено 0» у задачи, которой список ещё не назвали, читалось бы как «всё в порядке».
func requiredSummary(check FieldCheck, required []string) string {
	if len(required) == 0 {
		return "проверка выключена — список обязательных полей не задан"
	}
	if check.RequiredProblems() == 0 {
		return fmt.Sprintf("все %d заполнены", len(required))
	}
	return fmt.Sprintf("не в порядке %d из %d — перечислены в списке ошибок",
		check.RequiredProblems(), len(required))
}

// issuesBlock — список всех найденных ошибок.
//
// Сверху то, что нашёл код: незаведённые и незаполненные обязательные поля. Ниже то, что
// нашла модель. Список один, потому что человек ищет ошибки в одном месте, а не в двух; но
// строки кода помечены явно — по ним видно, что это факт, а не суждение модели.
func issuesBlock(check FieldCheck, modelIssues string) string {
	var lines []string
	for _, name := range check.Missing {
		lines = append(lines, fmt.Sprintf("— %s — обязательное, но в записи его нет вовсе", Label(name)))
	}
	for _, name := range check.Empty {
		lines = append(lines, fmt.Sprintf("— %s — обязательное, но не заполнено", Label(name)))
	}
	for _, problem := range check.TooLong {
		lines = append(lines, fmt.Sprintf(
			"— поле %s — %d знаков при пределе %d: в выдаче обрежется на середине",
			problem.Field, problem.Length, problem.Limit))
	}
	found := strings.TrimSpace(modelIssues)
	if len(lines) == 0 {
		return orDefault(found, noFindings)
	}
	block := "Чего не хватает в записи (нашёл код):\n" + strings.Join(lines, "\n")
	// «Замечаний нет» от модели рядом с находками кода противоречило бы им: ошибки на
	// странице есть, просто нашла их не модель.
	if found == "" || strings.EqualFold(found, noFindings) {
		return block
	}
	return block + "\n\n" + found
}

// faqSummary — сколько вопросов в блоке частых вопросов.
func faqSummary(count int) string {
	if count == 0 {
		return "блока нет"
	}
	return strconv.Itoa(count)
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
