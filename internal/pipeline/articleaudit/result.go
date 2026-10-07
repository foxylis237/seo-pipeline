package articleaudit

import (
	"fmt"
	"strconv"
	"strings"
)

// ReportData — поля шаблона result.md: оценка, разбор по критериям, важные ошибки и все ошибки списком.
type ReportData struct {
	// Score — «14/20» либо слова о том, что оценки в ответе не нашлось.
	Score string
	// Scores — результат по критериям одной строкой: «Структура 4/4 · Контент 4/5 · …».
	Scores string
	// Шапка: что именно проверено.
	Title    string
	URL      string
	Topic    string
	PostID   string
	PostType string
	// RequiredFields — итог проверки обязательных полей; имена полей печатаются в списке ошибок.
	RequiredFields string
	// FAQ — число вопросов в блоке частых вопросов и норма, если она задана.
	FAQ string
	// InternalLinks — перелинковка одной строкой: сколько ссылок нашлось и сколько требует задача.
	InternalLinks string
	// Разделы отчёта; пустой печатается словами, а не пропускается. Breakdown — разбор модели дословно.
	Breakdown string
	Critical  string
	Issues    string
	Unparsed  string
}

// BuildReport собирает отчёт из прочитанной страницы, проверки полей и ответа модели; разделы ответа идут как пришли.
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

const noBreakdown = "разбора по критериям в ответе нет — посмотреть generated/audit.txt"

// scoresLine — разбор по критериям одной строкой шапки. Веса критериев задаёт промпт, а не движок;
// сумму модель считает в уме с ошибками, поэтому расхождение с итоговой печатается рядом.
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

// linkOf предпочитает ссылку из блога адресу из книги: слаг площадка могла дополнить сама.
func linkOf(article Article, current Post) string {
	if strings.TrimSpace(current.Link) != "" {
		return current.Link
	}
	return article.SourceURL
}

// requiredSummary — строка шапки про обязательные поля; выключенная проверка не печатается как пройденная.
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

// issuesBlock — список всех ошибок: сверху помеченные находки кода, ниже находки модели.
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
	// «Замечаний нет» от модели противоречило бы находкам кода.
	if found == "" || strings.EqualFold(found, noFindings) {
		return block
	}
	return block + "\n\n" + found
}

// linksSummary — строка шапки про перелинковку: число, норма и адреса.
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

// faqSummary — строка шапки про блок частых вопросов: без нормы — одно число.
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
