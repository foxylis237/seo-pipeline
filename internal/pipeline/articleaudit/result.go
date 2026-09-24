package articleaudit

import (
	"fmt"
	"strconv"
	"strings"
)

// ReportData — поля шаблона result.md.
//
// Отчёт состоит из четырёх вещей: оценки, разбора по критериям, самых важных ошибок и всех
// ошибок списком. Рекомендаций и готовых абзацев на замену в нём по-прежнему нет — правит
// страницу человек, и оплачивать место в ответе под чужую работу незачем.
//
// Разбор вернулся в формат: общая оценка «11/20» не отличает слабую экспертность от слабой
// структуры, а решение «что чинить» принимают именно по этому. Одна фраза на критерий стоит
// нескольких строк ответа и отвечает на вопрос, ради которого отчёт открывают.
type ReportData struct {
	// Score — «14/20» либо слова о том, что оценки в ответе не нашлось. Первой строкой отчёта:
	// по ней страницы сравнивают между собой и решают, за какую браться.
	Score string
	// Scores — тот же результат по критериям, одной строкой: «Структура 4/4 · Контент 4/5 · …».
	// Стоит в шапке рядом с оценкой, потому что читают их вместе: сначала сколько, потом где.
	Scores string
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
	// InternalLinks — перелинковка одной строкой: сколько ссылок нашлось и сколько требует
	// задача. В отличие от FAQ здесь норма есть, поэтому нехватка идёт ещё и в список ошибок.
	InternalLinks string
	// Разделы отчёта. Пустых среди них не бывает: пустой блок печатается словами «замечаний
	// нет», а не пропускается, — человек должен видеть, что проверка была и ничего не нашла.
	//
	// Breakdown — разбор по критериям, как его написала модель: балл и фраза на каждый.
	// Печатается дословно, потому что фраза и есть ответ на «почему 2 из 5».
	Breakdown string
	Critical  string
	Issues    string
	Unparsed  string
}

// BuildReport собирает отчёт из прочитанной страницы, проверки полей и ответа модели.
//
// Содержимое разделов не переписывается и не переформатируется: ответ уходит в отчёт как
// пришёл.
func BuildReport(article Article, current Post, check FieldCheck, parsed Answer, required []string) ReportData {
	return ReportData{
		Score:          scoreLine(parsed),
		Scores:         scoresLine(parsed),
		Title:          current.Title,
		URL:            linkOf(article, current),
		Topic:          orDefault(article.Topic, "не задана — основанием служил заголовок записи"),
		PostID:         strconv.FormatInt(current.ID, 10),
		PostType:       current.PostType,
		RequiredFields: requiredSummary(check, required),
		FAQ:            faqSummary(check.FAQ),
		InternalLinks:  linksSummary(check.Links),
		Breakdown:      orDefault(parsed.Section(SectionBreakdown), noBreakdown),
		Critical:       orDefault(parsed.Section(SectionCritical), noFindings),
		Issues:         issuesBlock(check, parsed.Section(SectionIssues)),
		Unparsed:       orDefault(parsed.Unparsed, "весь ответ разобран по разделам"),
	}
}

// noBreakdown — что печатать вместо разбора, которого в ответе не нашлось. Пустой блок
// выглядит как потерянные данные, а ответ модели лежит рядом и его можно посмотреть глазами.
const noBreakdown = "разбора по критериям в ответе нет — посмотреть generated/audit.txt"

// scoresLine — разбор по критериям одной строкой шапки.
//
// Числа берутся из разбора, а не считаются заново: сколько весит каждый критерий, решает
// промпт задачи, и второй копии этих весов в движке быть не должно.
//
// Сумма разбора сверяется с итоговой оценкой и расхождение называется прямо. Молчать здесь
// нельзя: пять чисел модель складывает в уме и ошибается, а человек, читающий «11/20» над
// разбором на 18, решит, что сломан отчёт. Пометка стоит рядом, а не вместо, — обе цифры
// настоящие, и какой верить, решает он сам.
func scoresLine(parsed Answer) string {
	if len(parsed.Criteria) == 0 {
		return "не разобран"
	}
	parts := make([]string, 0, len(parsed.Criteria))
	for _, criterion := range parsed.Criteria {
		parts = append(parts, criterion.Text())
	}
	line := strings.Join(parts, " · ")
	sum, limit := parsed.CriteriaTotal()
	if parsed.ScoreFound && sum != parsed.Score {
		line += fmt.Sprintf(" (в сумме %d/%d — расходится с итоговой)", sum, limit)
	}
	return line
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
// Сверху то, что нашёл код: нехватка внутренних ссылок, незаведённые и незаполненные
// обязательные поля. Ниже то, что нашла модель. Список один, потому что человек ищет ошибки в
// одном месте, а не в двух; но строки кода помечены явно — по ним видно, что это факт, а не
// суждение модели.
func issuesBlock(check FieldCheck, modelIssues string) string {
	var lines []string
	if !check.FAQ.Enough() {
		lines = append(lines, fmt.Sprintf(
			"— вопросов в блоке FAQ %d при минимуме %d: блок под статьёй выйдет неполным",
			check.FAQ.Count, check.FAQ.Min))
	}
	if !check.Links.Enough() {
		lines = append(lines, fmt.Sprintf(
			"— внутренних ссылок в тексте %d при минимуме %d: читателю некуда уйти со статьи",
			check.Links.Count(), check.Links.Min))
	}
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
	block := "Нашёл код:\n" + strings.Join(lines, "\n")
	// «Замечаний нет» от модели рядом с находками кода противоречило бы им: ошибки на
	// странице есть, просто нашла их не модель.
	if found == "" || strings.EqualFold(found, noFindings) {
		return block
	}
	return block + "\n\n" + found
}

// linksSummary — строка шапки про перелинковку.
//
// Выключенная проверка и пройденная — разные состояния: «0 ссылок» у задачи, которая их не
// требует, читалось бы как находка. Адреса печатаются рядом с числом: по ним человек видит,
// ведут ли ссылки на программы или статья ссылается сама на соседний абзац.
func linksSummary(check LinkCheck) string {
	if !check.Enabled() {
		return "проверка выключена — минимум для задачи не задан"
	}
	if !check.Enough() {
		return fmt.Sprintf("%d при минимуме %d — не хватает %d",
			check.Count(), check.Min, check.Min-check.Count())
	}
	return fmt.Sprintf("%d при минимуме %d: %s",
		check.Count(), check.Min, strings.Join(check.URLs, ", "))
}

// faqSummary — строка шапки про блок частых вопросов.
//
// Без нормы печатается одно число: сколько вопросов должно быть, у такой задачи никто не
// решал. С нормой видно и её — по этой строке человек понимает, дособирать блок или нет.
func faqSummary(check FAQCheck) string {
	if !check.Enabled() {
		if check.Count == 0 {
			return "блока нет"
		}
		return strconv.Itoa(check.Count)
	}
	if !check.Enough() {
		return fmt.Sprintf("%d при минимуме %d — не хватает %d",
			check.Count, check.Min, check.Min-check.Count)
	}
	return fmt.Sprintf("%d при минимуме %d", check.Count, check.Min)
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
