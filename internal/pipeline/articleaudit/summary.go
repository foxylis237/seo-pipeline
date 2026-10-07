package articleaudit

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Summary — сводка по всей пачке: за какую страницу браться первой, где чего не хватает, что ломается чаще всего.
type Summary struct {
	// Pages — страницы пачки, худшие первыми.
	Pages []SummaryPage
	// MissingFields — где какое обязательное не заполнено, по полям.
	MissingFields []FieldGap
	// CommonIssues — самые частые находки, чаще первыми.
	CommonIssues []IssueCount
	// Criteria — баллы разбора, сложенные по всей пачке, слабое первым.
	Criteria []CriterionAverage
	// Failed — страницы, которые проверить не удалось: прогон дошёл до них и упал.
	Failed []SummaryPage
	// Pending — страницы, до которых прогон ещё не дошёл; это не отказ.
	Pending []SummaryPage
}

// SummaryPage — одна строка сводки.
type SummaryPage struct {
	ExternalID string
	Title      string
	URL        string
	Score      *int
	ScoreMax   *int
	Findings   int
	Missing    []string
	FAQ        FAQCheck
	// Links — перелинковка страницы, пересчитанная по сохранённой копии тела.
	Links LinkCheck
	// Advice — строки «Как исправить» из критических ошибок отчёта, как есть.
	Advice []string
	// RecordGaps — недостача по графам самой записи (рубрика, метки, название, обложка) по счёту прогона в базе.
	RecordGaps int
	// Criteria — разбор страницы по критериям из сохранённого ответа модели.
	Criteria []CriterionScore
	Error    string
}

// CriterionAverage — один критерий, сложенный по всей пачке; хранятся суммы, а не среднее.
type CriterionAverage struct {
	Name  string
	Score int
	Max   int
	Pages int
}

// Share — доля набранного по пачке: веса критериев разные, сравнивать их по баллу нельзя.
func (a CriterionAverage) Share() float64 {
	if a.Max <= 0 {
		return 0
	}
	return float64(a.Score) / float64(a.Max)
}

// FieldGap — обязательное, которого нет, и страницы, где его нет.
type FieldGap struct {
	Field string
	Pages []string
}

// IssueCount — находка и на скольких страницах она встретилась.
type IssueCount struct {
	Issue string
	Pages []string
}

// BuildSummary собирает сводку из сохранённых артефактов, без сети и модели; обязательное пересчитывается по сохранённым полям.
func BuildSummary(articles []Article, artifacts Artifacts, options Options) Summary {
	var summary Summary
	issues := map[string]*IssueCount{}
	gaps := map[string]*FieldGap{}
	criteria := map[string]*CriterionAverage{}
	var order []string

	for _, article := range articles {
		page := SummaryPage{
			ExternalID: article.ExternalID,
			Title:      titleOf(article),
			URL:        article.SourceURL,
			Score:      article.Score,
			ScoreMax:   article.ScoreMax,
			Findings:   article.Findings,
			Error:      article.ErrorMessage,
		}
		if !article.Audited() {
			if strings.TrimSpace(article.ErrorMessage) != "" {
				summary.Failed = append(summary.Failed, page)
			} else {
				summary.Pending = append(summary.Pending, page)
			}
			continue
		}
		fillFromArtifacts(&page, artifacts, article, options)
		for _, name := range page.Missing {
			gap, known := gaps[name]
			if !known {
				gap = &FieldGap{Field: name}
				gaps[name] = gap
			}
			gap.Pages = append(gap.Pages, article.ExternalID)
		}
		if parsed, ok := readAnswer(artifacts, article); ok {
			countIssues(issues, parsed, article)
			page.Advice = adviceOf(parsed)
			page.Criteria = parsed.Criteria
			countCriteria(criteria, &order, parsed)
		}
		summary.Pages = append(summary.Pages, page)
	}

	sort.SliceStable(summary.Pages, func(i, j int) bool {
		return scoreOf(summary.Pages[i]) < scoreOf(summary.Pages[j])
	})
	for _, gap := range gaps {
		summary.MissingFields = append(summary.MissingFields, *gap)
	}
	sort.Slice(summary.MissingFields, func(i, j int) bool {
		if len(summary.MissingFields[i].Pages) != len(summary.MissingFields[j].Pages) {
			return len(summary.MissingFields[i].Pages) > len(summary.MissingFields[j].Pages)
		}
		return summary.MissingFields[i].Field < summary.MissingFields[j].Field
	})
	for _, issue := range issues {
		if len(issue.Pages) < 2 {
			// Находка на одной странице уже лежит в её отчёте.
			continue
		}
		summary.CommonIssues = append(summary.CommonIssues, *issue)
	}
	sort.Slice(summary.CommonIssues, func(i, j int) bool {
		if len(summary.CommonIssues[i].Pages) != len(summary.CommonIssues[j].Pages) {
			return len(summary.CommonIssues[i].Pages) > len(summary.CommonIssues[j].Pages)
		}
		return summary.CommonIssues[i].Issue < summary.CommonIssues[j].Issue
	})
	for _, name := range order {
		summary.Criteria = append(summary.Criteria, *criteria[name])
	}
	sort.SliceStable(summary.Criteria, func(i, j int) bool {
		return summary.Criteria[i].Share() < summary.Criteria[j].Share()
	})
	return summary
}

// countCriteria складывает разбор страницы в общий счёт; порядок первого появления хранится отдельно от map.
func countCriteria(counted map[string]*CriterionAverage, order *[]string, parsed Answer) {
	for _, criterion := range parsed.Criteria {
		key := strings.ToLower(criterion.Name)
		average, known := counted[key]
		if !known {
			average = &CriterionAverage{Name: criterion.Name}
			counted[key] = average
			*order = append(*order, key)
		}
		average.Score += criterion.Score
		average.Max += criterion.Max
		average.Pages++
	}
}

// fillFromArtifacts дополняет страницу полями, блоком вопросов и перелинковкой с диска;
// нечитаемый артефакт оставляет её без этой части.
func fillFromArtifacts(page *SummaryPage, artifacts Artifacts, article Article, options Options) {
	if fields, err := readFields(artifacts, article.FieldsPath); err == nil {
		check := CheckRequired(Post{Fields: fields}, fieldNames(options.Required))
		page.Missing = append(append([]string{}, check.Missing...), check.Empty...)
		page.FAQ = CountFAQ(fields, options.FAQ)
		// Прогон считал и графы самой записи, которых в артефактах нет.
		if article.MissingFields > len(page.Missing) {
			page.RecordGaps = article.MissingFields - len(page.Missing)
		}
	}
	if body, err := artifacts.Read(article.OriginalPath); err == nil {
		page.Links = CollectInternalLinks(body, article.SourceURL, options.MinInternalLinks)
	}
}

// titleOf называет страницу темой из книги, а без неё — слагом.
func titleOf(article Article) string {
	if topic := strings.TrimSpace(article.Topic); topic != "" {
		return topic
	}
	return article.Slug
}

// fieldNames отбирает из списка обязательного то, что лежит полями записи: графы самой записи на диске не сохранены.
func fieldNames(required []string) []string {
	names := make([]string, 0, len(required))
	for _, name := range required {
		switch name {
		case RecordCategory, RecordTags, RecordTitle, RecordThumbnail:
			continue
		}
		names = append(names, name)
	}
	return names
}

// scoreOf — оценка для сортировки; неразобранная считается худшей.
func scoreOf(page SummaryPage) int {
	if page.Score == nil {
		return -1
	}
	return *page.Score
}

func readFields(artifacts Artifacts, path string) (map[string]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("путь к полям записи пуст")
	}
	content, err := artifacts.Read(path)
	if err != nil {
		return nil, err
	}
	fields := map[string]string{}
	if err := json.Unmarshal([]byte(content), &fields); err != nil {
		return nil, fmt.Errorf("разобрать поля записи %q: %w", path, err)
	}
	return fields, nil
}

func countIssues(counted map[string]*IssueCount, parsed Answer, article Article) {
	seen := map[string]struct{}{}
	for _, section := range []string{SectionIssues, SectionCritical} {
		for _, line := range strings.Split(parsed.Section(section), "\n") {
			key, label := issueKey(line)
			if key == "" {
				continue
			}
			if _, repeat := seen[key]; repeat {
				continue
			}
			seen[key] = struct{}{}
			issue, known := counted[key]
			if !known {
				issue = &IssueCount{Issue: label}
				counted[key] = issue
			}
			issue.Pages = append(issue.Pages, article.ExternalID)
		}
	}
}

// readAnswer читает и разбирает сохранённый ответ модели; отказ оставляет страницу без ошибок и советов.
func readAnswer(artifacts Artifacts, article Article) (Answer, bool) {
	answer, err := artifacts.Read(article.AuditPath)
	if err != nil {
		return Answer{}, false
	}
	parsed, err := ParseAnswer(answer)
	if err != nil {
		return Answer{}, false
	}
	return parsed, true
}

// adviceOf вынимает из критических ошибок только строки «Как исправить».
func adviceOf(parsed Answer) []string {
	var advice []string
	for _, line := range strings.Split(parsed.Section(SectionCritical), "\n") {
		text, found := strings.CutPrefix(strings.TrimSpace(line), "Как исправить:")
		if !found {
			continue
		}
		if text = strings.TrimSpace(text); text != "" {
			advice = append(advice, text)
		}
	}
	return advice
}

var issueBullet = regexp.MustCompile(`^\s*(?:[-—•*]|\d+[.)])\s*`)

// issueTheme — тема находки и слова, по которым она узнаётся; темы взяты из типовых ошибок промпта.
// Группировать по формулировке нельзя: модель строит её каждый раз заново.
type issueTheme struct {
	label string
	words []string
}

// issueThemes идут от частного к общему: находка попадает в первую подошедшую тему.
var issueThemes = []issueTheme{
	{"противоречие в параметрах программы (часы, сроки, цена, документ)", []string{"противореч", " vs ", "не соответствует заявленн"}},
	{"выдуманные цифры: зарплаты, сроки, число выпускников", []string{"выдуман", "зарплат", "без источник", "без подтвержден"}},
	{"отзывы без имени, города или конкретного результата", []string{"отзыв"}},
	{"преподаватели без имён или не по профилю курса", []string{"преподавател", "специалист"}},
	{"документ описан размыто", []string{"государственного образца", "документ описан", "размытая формулировка", "какой документ"}},
	{"обязательного блока нет вовсе", []string{"нет блока", "отсутствует блок", "отсутствие блока", "не хватает блока"}},
	{"слабый CTA", []string{"cta", "призыв к действию"}},
	{"вода: абзац подходит к любой профессии", []string{"вода", "подходит к люб", "общие фраз", "ни о чём"}},
	{"повтор и дублирование блоков", []string{"дублиру", "повтор", "дважды"}},
	{"заголовки-этикетки без раскрытия", []string{"этикетк", "заголовки-этикетки"}},
	{"стена текста: длинные абзацы", []string{"абзац", "стена текста", "строк"}},
	{"переспам ключа", []string{"переспам", "неестественн", "именительном падеже"}},
	{"нет таблицы там, где идёт сравнение", []string{"таблиц"}},
	{"жирным выделено слишком много или ничего", []string{"жирным", "выделен"}},
	{"картинки: нет, не по теме или без alt", []string{"alt", "картинк", "изображен", "фотограф"}},
	{"грамматика, опечатки, падежи", []string{"грамматич", "опечат", "падеж", "орфограф", "пунктуац"}},
	{"программа обучения без детализации по модулям", []string{"модул", "программа обучения"}},
}

// issueKey сводит строку находки к теме.
func issueKey(line string) (string, string) {
	label := strings.TrimSpace(issueBullet.ReplaceAllString(line, ""))
	if len([]rune(label)) < 15 || strings.HasSuffix(label, ":") {
		return "", ""
	}
	lowered := strings.ToLower(label)
	for _, prefix := range []string{"почему это проблема", "как исправить", "ошибка:"} {
		if strings.HasPrefix(lowered, prefix) {
			if prefix != "ошибка:" {
				return "", ""
			}
			lowered = strings.TrimSpace(strings.TrimPrefix(lowered, prefix))
		}
	}
	for _, theme := range issueThemes {
		for _, word := range theme.words {
			if strings.Contains(lowered, word) {
				return theme.label, theme.label
			}
		}
	}
	return "", ""
}

// Render печатает сводку в Markdown.
func (s Summary) Render(task string) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# Сводка аудита: %s\n\n", task)
	fmt.Fprintf(&out, "Проверено страниц: %d", len(s.Pages))
	if len(s.Failed) > 0 {
		fmt.Fprintf(&out, ", упало: %d", len(s.Failed))
	}
	if len(s.Pending) > 0 {
		fmt.Fprintf(&out, ", ещё не проверено: %d", len(s.Pending))
	}
	out.WriteString("\n\n")

	out.WriteString("## Оценки\n\nХудшие первыми — с них и начинают.\n\n")
	out.WriteString("| Оценка | Слабое | ID | Страница | Находок | Не заполнено | FAQ |\n")
	out.WriteString("|---|---|---|---|---|---|---|\n")
	for _, page := range s.Pages {
		fmt.Fprintf(&out, "| %s | %s | %s | %s | %d | %s | %s |\n",
			scoreText(page), weakestText(page), page.ExternalID, page.Title,
			page.Findings, gapsText(page), faqText(page))
	}

	out.WriteString("\n## Где теряются баллы\n\n")
	out.WriteString("Разбор по критериям, сложенный по всей пачке. Слабое первым: " +
		"на одной странице это замечание к ней, на сорока — вопрос к тому, кто их писал.\n\n")
	if len(s.Criteria) == 0 {
		out.WriteString("Разбора по критериям в сохранённых ответах нет.\n")
	} else {
		out.WriteString("| Критерий | Средний балл | Доля | Страниц |\n|---|---|---|---|\n")
		for _, criterion := range s.Criteria {
			fmt.Fprintf(&out, "| %s | %.1f из %.0f | %.0f%% | %d |\n",
				criterion.Name,
				float64(criterion.Score)/float64(criterion.Pages),
				float64(criterion.Max)/float64(criterion.Pages),
				criterion.Share()*100, criterion.Pages)
		}
	}

	out.WriteString("\n## Чего не хватает в записях\n\n")
	if len(s.MissingFields) == 0 {
		out.WriteString("Обязательное заполнено везде.\n")
	}
	for _, gap := range s.MissingFields {
		fmt.Fprintf(&out, "- **%s** — не заполнено на %d: %s\n",
			Label(gap.Field), len(gap.Pages), strings.Join(gap.Pages, ", "))
	}

	out.WriteString("\n## Самые частые ошибки\n\nТо, что встретилось больше чем на одной странице.\n\n")
	if len(s.CommonIssues) == 0 {
		out.WriteString("Повторяющихся находок нет.\n")
	}
	for _, issue := range s.CommonIssues {
		fmt.Fprintf(&out, "- **%d страниц** — %s\n  ID: %s\n",
			len(issue.Pages), issue.Issue, strings.Join(issue.Pages, ", "))
	}

	if len(s.Failed) > 0 {
		out.WriteString("\n## Упало\n\n")
		for _, page := range s.Failed {
			fmt.Fprintf(&out, "- %s — %s\n  %s\n", page.ExternalID, page.Title, page.Error)
		}
	}
	return out.String()
}

func scoreText(page SummaryPage) string {
	if page.Score == nil {
		return "—"
	}
	if page.ScoreMax == nil {
		return fmt.Sprintf("%d", *page.Score)
	}
	return fmt.Sprintf("%d/%d", *page.Score, *page.ScoreMax)
}

// weakestText — колонка «Слабое»: критерий, где страница потеряла больше всего; у полного балла — прочерк.
func weakestText(page SummaryPage) string {
	var worst CriterionScore
	found := false
	for _, criterion := range page.Criteria {
		if criterion.Max <= 0 || criterion.Score >= criterion.Max {
			continue
		}
		if !found || criterion.Share() < worst.Share() {
			worst, found = criterion, true
		}
	}
	if !found {
		return "—"
	}
	return worst.Text()
}

func gapsText(page SummaryPage) string {
	parts := append([]string{}, page.Missing...)
	if page.RecordGaps > 0 {
		parts = append(parts, fmt.Sprintf("+%d в самой записи", page.RecordGaps))
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, ", ")
}

// faqText — колонка блока вопросов: число, а при норме — норма и пометка нехватки.
func faqText(page SummaryPage) string {
	if !page.FAQ.Enabled() {
		return strconv.Itoa(page.FAQ.Count)
	}
	if !page.FAQ.Enough() {
		return fmt.Sprintf("%d из %d ❗", page.FAQ.Count, page.FAQ.Min)
	}
	return fmt.Sprintf("%d из %d", page.FAQ.Count, page.FAQ.Min)
}

// SummaryFile — имя сводки в корне артефактов задачи.
const SummaryFile = "summary.md"
