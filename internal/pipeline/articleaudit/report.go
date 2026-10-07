package articleaudit

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Разделы ответа — внутренние имена; в ответе разделы ищутся по словам (см. anchors).
const (
	SectionBreakdown = "breakdown"
	SectionCritical  = "critical"
	SectionIssues    = "issues"
)

// ErrEmptyAnswer — модель ответила пустотой; это отказ стадии.
var ErrEmptyAnswer = errors.New("модель вернула пустой ответ")

// anchor — заголовок раздела ответа; ищется по словам, номер не сверяется: модель его переставляет.
type anchor struct {
	section string
	prefix  string
}

// anchors — разделы, которые промпт просит и которые печатает отчёт; у разбора несколько имён, которые пишет модель.
var anchors = []anchor{
	{SectionBreakdown, "детальный разбор"},
	{SectionBreakdown, "разбор по критериям"},
	{SectionBreakdown, "разбор по пунктам"},
	{SectionCritical, "критические ошибки"},
	{SectionIssues, "все найденные ошибки"},
}

// strayPrefixes — разделы, которых промпт не просит, но модель пишет; уходят в «Не разобрано» целиком,
// а не подмешиваются в список ошибок.
var strayPrefixes = []string{
	"поля записи", "рекомендации", "что работает хорошо",
}

// droppedPrefixes — строки шапки, которых промпт не просит; выбрасываются без сохранения.
var droppedPrefixes = []string{
	"количество слов", "ключевые запросы", "ключевые слова", "отсутствующие блоки",
}

// Answer — разобранный ответ модели; разделы лежат дословно, отчёт печатает их как пришли.
type Answer struct {
	// Score и ScoreMax — итоговая оценка; ScoreFound отделяет «ноль баллов» от «оценки не нашлось».
	Score      int
	ScoreMax   int
	ScoreFound bool
	// Sections — разделы ответа по именам констант выше.
	Sections map[string]string
	// Criteria — баллы разбора в порядке модели; имена критериев задаёт промпт задачи, не движок.
	Criteria []CriterionScore
	// Unparsed — всё, что не легло ни в один раздел и не опознано как строка шапки.
	Unparsed string
}

// Section возвращает содержимое раздела; отсутствующий раздел — пустая строка, а не отказ.
func (a Answer) Section(name string) string { return a.Sections[name] }

// CriterionScore — балл за один критерий разбора; сводка складывает их по всей пачке.
type CriterionScore struct {
	Name  string
	Score int
	Max   int
}

// Share — доля набранного: критерии разного веса, и сравнивать их по баллу нельзя.
func (c CriterionScore) Share() float64 {
	if c.Max <= 0 {
		return 0
	}
	return float64(c.Score) / float64(c.Max)
}

func (c CriterionScore) Text() string {
	return fmt.Sprintf("%s %d/%d", c.Name, c.Score, c.Max)
}

// CriteriaTotal — сумма разбора: сколько набрано и из скольких; с итоговой её сверяет отчёт.
func (a Answer) CriteriaTotal() (int, int) {
	var score, limit int
	for _, criterion := range a.Criteria {
		score += criterion.Score
		limit += criterion.Max
	}
	return score, limit
}

// criterionNameLimit — предел длины имени критерия; длиннее — фраза комментария с баллом внутри.
const criterionNameLimit = 40

// parseCriteria вынимает из раздела разбора баллы по критериям: имя — то, что стоит перед баллом в той же строке.
func parseCriteria(section string) []CriterionScore {
	if strings.TrimSpace(section) == "" {
		return nil
	}
	var out []CriterionScore
	seen := map[string]struct{}{}
	for _, raw := range strings.Split(section, "\n") {
		place := scorePattern.FindStringIndex(raw)
		if place == nil {
			continue
		}
		name := criterionName(raw[:place[0]])
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, repeat := seen[key]; repeat {
			// Второе упоминание критерия — повтор балла в комментарии.
			continue
		}
		digits := scorePattern.FindStringSubmatch(raw[place[0]:place[1]])
		score, _ := strconv.Atoi(digits[1])
		limit, _ := strconv.Atoi(digits[2])
		if limit <= 0 {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, CriterionScore{Name: name, Score: score, Max: limit})
	}
	return out
}

// criterionName — имя критерия из начала строки: «— Контент: », «3. Структура — ».
// Пустое — это фраза комментария: отличают длина, нет знаков конца предложения, заглавная буква в начале.
func criterionName(prefix string) string {
	name := strings.TrimSpace(prefix)
	name = strings.TrimLeft(name, "-—–•*_#> \t")
	name = anchorNumber.ReplaceAllString(name, "")
	name = strings.Trim(name, "*_ \t")
	name = strings.TrimRight(name, ":—–- \t")
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > criterionNameLimit {
		return ""
	}
	if strings.ContainsAny(name, ".!?,;()") {
		return ""
	}
	first := []rune(name)[0]
	if !unicode.IsUpper(first) {
		return ""
	}
	return name
}

// normalizeHeading снимает с строки markdown, звёздочки и номер раздела.
func normalizeHeading(line string) string {
	normalized := strings.ToLower(strings.TrimSpace(line))
	normalized = strings.Trim(normalized, "#*_ \t")
	normalized = anchorNumber.ReplaceAllString(normalized, "")
	return strings.TrimLeft(normalized, "*_ \t")
}

var (
	// anchorNumber — номер раздела: «4.» или «4)».
	anchorNumber = regexp.MustCompile(`^(\d{1,2})\s*[.)]\s*`)
	scorePattern = regexp.MustCompile(`(\d{1,3})\s*/\s*(\d{1,3})`)
)

// ParseAnswer разбирает ответ модели по якорям разделов без потерь: не легшее ни в один раздел
// уходит в Unparsed, ненайденный раздел просто пуст.
func ParseAnswer(text string) (Answer, error) {
	if strings.TrimSpace(text) == "" {
		return Answer{}, ErrEmptyAnswer
	}
	parsed := Answer{Sections: make(map[string]string, len(anchors))}
	buffers := make(map[string]*strings.Builder, len(anchors))
	var preamble strings.Builder

	current := ""
	for _, raw := range strings.Split(text, "\n") {
		if fenceLine(raw) || droppedLine(raw) {
			continue
		}
		if strings.TrimSpace(raw) == "" {
			// Пустые строки разделяют находки внутри раздела.
			if current != "" {
				buffers[current].WriteString("\n")
			}
			continue
		}
		if matchStray(raw) {
			// Заголовок сохраняется: по нему видно, что модель написала сверх формата.
			current = ""
			preamble.WriteString(strings.TrimSpace(raw))
			preamble.WriteString("\n")
			continue
		}
		if section, ok := matchAnchor(raw); ok {
			current = section
			if _, exists := buffers[section]; !exists {
				buffers[section] = &strings.Builder{}
			} else {
				// Повторный заголовок раздела: содержимое дописывается, а не заменяется.
				buffers[section].WriteString("\n")
			}
			continue
		}
		if current != "" {
			buffers[current].WriteString(raw)
			buffers[current].WriteString("\n")
			continue
		}
		preamble.WriteString(raw)
		preamble.WriteString("\n")
	}

	for section, builder := range buffers {
		parsed.Sections[section] = strings.TrimSpace(builder.String())
	}
	parsed.Unparsed = strings.TrimSpace(preamble.String())
	parsed.Criteria = parseCriteria(parsed.Sections[SectionBreakdown])
	readScore(&parsed, text)
	return parsed, nil
}

// fenceLine отвечает, состоит ли строка из одного маркера блока кода: модель оборачивает ответ
// вопреки промпту, снимается только маркер.
func fenceLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "```") {
		return false
	}
	// Строка вида «```html <тег>» несёт содержимое.
	return !strings.Contains(strings.TrimPrefix(trimmed, "```"), " ")
}

func matchAnchor(line string) (string, bool) {
	normalized := normalizeHeading(line)
	if normalized == "" {
		return "", false
	}
	for _, candidate := range anchors {
		if strings.HasPrefix(normalized, candidate.prefix) {
			return candidate.section, true
		}
	}
	return "", false
}

func matchStray(line string) bool {
	normalized := normalizeHeading(line)
	if normalized == "" {
		return false
	}
	for _, prefix := range strayPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

// droppedLine выбрасывает строки шапки вне формата и строку итоговой оценки: её код печатает в шапке отчёта.
func droppedLine(line string) bool {
	if strings.Contains(strings.ToLower(line), "итоговая оценка") {
		return true
	}
	heading := normalizeHeading(line)
	if heading == "" {
		return false
	}
	for _, prefix := range droppedPrefixes {
		if strings.HasPrefix(heading, prefix) {
			return true
		}
	}
	return false
}

// readScore ищет итоговую оценку по всему ответу: модель ставит её и в конец, и внутрь раздела.
func readScore(parsed *Answer, text string) {
	for _, raw := range strings.Split(text, "\n") {
		if !strings.Contains(strings.ToLower(raw), "итоговая оценка") {
			continue
		}
		match := scorePattern.FindStringSubmatch(raw)
		if match == nil {
			continue
		}
		parsed.Score, _ = strconv.Atoi(match[1])
		parsed.ScoreMax, _ = strconv.Atoi(match[2])
		parsed.ScoreFound = true
		return
	}
}

// noFindings — ответ раздела, где нечего сказать, как его просит промпт.
const noFindings = "замечаний нет"

// CountIssues считает находки раздела «все найденные ошибки» по строкам; подписи с двоеточием не считаются.
func CountIssues(section string) int {
	section = strings.TrimSpace(section)
	if section == "" || strings.EqualFold(section, noFindings) {
		return 0
	}
	var count int
	for _, raw := range strings.Split(section, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasSuffix(line, ":") {
			continue
		}
		if strings.EqualFold(line, noFindings) {
			continue
		}
		count++
	}
	return count
}
