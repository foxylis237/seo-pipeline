package articleaudit

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Summary — сводка по всей пачке.
//
// Отдельный от отчётов статей взгляд: те отвечают на вопрос «что не так с этой страницей», а
// сводка — на вопросы «за какую браться первой», «где чего не хватает» и «что у нас ломается
// чаще всего». Последнее по одному отчёту не увидеть вовсе: одна и та же ошибка на сорока
// страницах и одна на одной выглядят в отчёте одинаково.
type Summary struct {
	// Pages — страницы пачки, худшие первыми.
	Pages []SummaryPage
	// MissingFields — где какое обязательное не заполнено, по полям.
	MissingFields []FieldGap
	// CommonIssues — самые частые находки, чаще первыми.
	CommonIssues []IssueCount
	// Failed — страницы, которые проверить не удалось: прогон дошёл до них и упал.
	Failed []SummaryPage
	// Pending — страницы, до которых прогон ещё не дошёл. Это не отказ, и путать их нельзя:
	// «не удалось проверить сто семь» и «сто семь в очереди» — разные новости.
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
	FAQ        int
	// RecordGaps — сколько не хватало из того, что сводка пересчитать не может: рубрики,
	// названия записи, обложки. Считал их прогон, и число взято из его отметки в базе.
	RecordGaps int
	Error      string
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

// BuildSummary собирает сводку из уже сохранённых артефактов.
//
// Ни сети, ни модели: всё, что нужно, лежит на диске после прогона — поля записи в
// original/fields.json, ответ модели в generated/audit.txt. Поэтому сводку можно пересобирать
// сколько угодно раз, в том числе после того, как список обязательного изменили: она
// пересчитает его по сохранённым полям, а не по тому, что было на момент прогона.
func BuildSummary(articles []Article, artifacts Artifacts, required []string) Summary {
	var summary Summary
	issues := map[string]*IssueCount{}
	gaps := map[string]*FieldGap{}

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
		if fields, err := readFields(artifacts, article.FieldsPath); err == nil {
			check := CheckRequired(Post{Fields: fields}, fieldNames(required))
			page.Missing = append(append([]string{}, check.Missing...), check.Empty...)
			page.FAQ = CountFAQ(fields)
			// Прогон считал и рубрику с обложкой, которых в артефактах нет. Расхождение
			// означает, что не хватало как раз их, — и промолчать об этом нельзя.
			if article.MissingFields > len(page.Missing) {
				page.RecordGaps = article.MissingFields - len(page.Missing)
			}
			for _, name := range page.Missing {
				gap, known := gaps[name]
				if !known {
					gap = &FieldGap{Field: name}
					gaps[name] = gap
				}
				gap.Pages = append(gap.Pages, article.ExternalID)
			}
		}
		countIssues(issues, artifacts, article)
		summary.Pages = append(summary.Pages, page)
	}

	// Худшие первыми: сводку читают, чтобы решить, за какую страницу браться.
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
			// Находка на одной странице — не «частая ошибка», а обычное замечание: оно уже
			// лежит в отчёте этой страницы, и повторять его в сводке незачем.
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
	return summary
}

// titleOf — как назвать страницу в сводке. Тема из книги точнее слага, но её может не быть.
func titleOf(article Article) string {
	if topic := strings.TrimSpace(article.Topic); topic != "" {
		return topic
	}
	return article.Slug
}

// fieldNames отбирает из списка обязательного то, что лежит полями записи.
//
// Рубрику, название и обложку сводка пересчитать не может: в артефактах сохранены поля, а эти
// три живут в самой записи, и после прогона их на диске нет. Их проверил сам прогон, и его
// счёт лежит в базе — если он больше пересчитанного, значит не хватало чего-то из этих трёх.
func fieldNames(required []string) []string {
	names := make([]string, 0, len(required))
	for _, name := range required {
		switch name {
		case RecordCategory, RecordTitle, RecordThumbnail:
			continue
		}
		names = append(names, name)
	}
	return names
}

// scoreOf — оценка для сортировки. Неразобранная считается худшей: её надо посмотреть глазами.
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

// countIssues складывает находки страницы в общий счёт.
func countIssues(counted map[string]*IssueCount, artifacts Artifacts, article Article) {
	answer, err := artifacts.Read(article.AuditPath)
	if err != nil {
		return
	}
	parsed, err := ParseAnswer(answer)
	if err != nil {
		return
	}
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

var issueBullet = regexp.MustCompile(`^\s*(?:[-—•*]|\d+[.)])\s*`)

// issueTheme — тема находки и слова, по которым она узнаётся.
//
// Тем ровно столько, сколько их называет сам промпт в разделе типовых ошибок: сводка отвечает
// на вопрос «что у нас ломается чаще всего», а ломается ровно то, что промпт велел искать.
//
// Это группировка, а не классификация: решений по теме никто не принимает, она нужна, чтобы
// сорок одинаковых по сути замечаний не выглядели сорока разными. Группировать по словам
// самой находки нельзя — формулировку модель каждый раз строит заново, и по первым словам не
// совпадает почти ничего (проверено на шести страницах: из 154 строк совпали две).
type issueTheme struct {
	label string
	words []string
}

// issueThemes перечислены от частного к общему: находка попадает в первую подошедшую тему,
// поэтому «противоречие по часам» не должно стоять после общего «часы».
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

// issueKey сводит строку находки к теме, по которой две находки считаются одной и той же.
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
//
// Порядок разделов — порядок вопросов, с которыми к сводке приходят: за какую страницу
// браться, где чего не хватает, что ломается чаще всего.
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
	out.WriteString("| Оценка | ID | Страница | Находок | Не заполнено | FAQ |\n")
	out.WriteString("|---|---|---|---|---|---|\n")
	for _, page := range s.Pages {
		fmt.Fprintf(&out, "| %s | %s | %s | %d | %s | %d |\n",
			scoreText(page), page.ExternalID, page.Title, page.Findings, gapsText(page), page.FAQ)
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

// SummaryFile — имя сводки в корне артефактов задачи.
const SummaryFile = "summary.md"
