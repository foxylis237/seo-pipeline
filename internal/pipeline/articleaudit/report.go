package articleaudit

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Разделы ответа. Их два, и это весь вывод модели: оценка, самые важные ошибки и все ошибки
// списком. Имена внутренние — в самом ответе разделы называются словами, и по словам же
// ищутся (см. anchors).
const (
	SectionCritical = "critical"
	SectionIssues   = "issues"
)

// ErrEmptyAnswer — модель ответила пустотой. Это отказ стадии: платить второй раз дешевле,
// чем сохранить отчёт ни о чём.
var ErrEmptyAnswer = errors.New("модель вернула пустой ответ")

// anchor — заголовок раздела ответа.
//
// Ищется по словам, а номер разбирается и отбрасывается. Номер намеренно не сверяется: он
// задан промптом, но модель переносит нумерацию из привычного ей порядка, и раздел
// «Критические ошибки» приходит то первым, то четвёртым. Терять из-за цифры целый раздел,
// за который заплачено, нельзя.
type anchor struct {
	section string
	prefix  string
}

// anchors — разделы, которые промпт просит и которые печатает отчёт.
var anchors = []anchor{
	{SectionCritical, "критические ошибки"},
	{SectionIssues, "все найденные ошибки"},
}

// strayPrefixes — разделы, которых промпт больше не просит.
//
// Модель их всё равно пишет: разбор по критериям и рекомендации она считает частью хорошего
// ответа, а прежние версии промпта их и требовали. Узнавать их надо затем, чтобы такой раздел
// ушёл в «Не разобрано» целиком, а не подмешался в список ошибок, — иначе человек читал бы в
// перечне находок пересказ критериев оценки.
var strayPrefixes = []string{
	"поля записи", "рекомендации", "разбор по критериям", "что работает хорошо",
}

// droppedPrefixes — строки шапки, которых промпт больше не просит.
//
// В отличие от чужого раздела это одна строка, и хранить её незачем: количество слов и список
// ключей в отчёт не идут. Строка оценки тоже сюда: её значение код печатает первой строкой
// отчёта, и второй раз, посреди находок, она не нужна.
var droppedPrefixes = []string{
	"количество слов", "ключевые запросы", "ключевые слова", "отсутствующие блоки",
}

// Answer — разобранный ответ модели.
//
// Содержимое разделов лежит здесь дословно: отчёт печатает его как пришло. Переписывать или
// переформатировать нельзя — самое ценное в ответе это готовый абзац, список или таблица на
// замену, и любое «приведение к виду» их портит.
type Answer struct {
	// Score и ScoreMax — итоговая оценка. ScoreFound отделяет «ноль баллов» от «строки с
	// оценкой в ответе не нашлось»: подставлять ноль вместо второго нельзя, по оценке
	// сортируют пачку.
	Score      int
	ScoreMax   int
	ScoreFound bool
	// Sections — разделы ответа по именам констант выше.
	Sections map[string]string
	// Unparsed — всё, что не легло ни в один раздел и не опознано как строка шапки.
	Unparsed string
}

// Section возвращает содержимое раздела; отсутствующий раздел — пустая строка, а не отказ.
func (a Answer) Section(name string) string { return a.Sections[name] }

// normalizeHeading снимает с строки всё, чем модель украшает заголовок: markdown, звёздочки,
// номер раздела. Номер отбрасывается намеренно — он задан промптом, но модель переносит
// нумерацию из привычного ей порядка, и терять из-за цифры целый раздел нельзя.
func normalizeHeading(line string) string {
	normalized := strings.ToLower(strings.TrimSpace(line))
	normalized = strings.Trim(normalized, "#*_ \t")
	normalized = anchorNumber.ReplaceAllString(normalized, "")
	return strings.TrimLeft(normalized, "*_ \t")
}

var (
	// anchorNumber — номер раздела в начале заголовка: «4.» или «4)».
	anchorNumber = regexp.MustCompile(`^(\d{1,2})\s*[.)]\s*`)
	scorePattern = regexp.MustCompile(`(\d{1,3})\s*/\s*(\d{1,3})`)
)

// ParseAnswer разбирает ответ модели по якорям разделов.
//
// Снисходительно и без потерь. Снисходительно — потому что модель ставит то
// «4. Критические ошибки (ТОП-3):», то «Критические ошибки», то заголовок markdown; без
// потерь — потому что за ответ заплачено, и всё, что не легло ни в один раздел, обязано
// оказаться в отчёте, а не пропасть. Ненайденный раздел отказом не считается: он просто
// пуст, и отчёт скажет об этом словами.
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
			// Пустые строки внутри раздела сохраняются: ими разделены находки, и без них
			// список слипся бы в абзац. До первого раздела они не значат ничего.
			if current != "" {
				buffers[current].WriteString("\n")
			}
			continue
		}
		if matchStray(raw) {
			// Сам заголовок тоже сохраняем: по нему видно, что именно модель написала сверх
			// формата, и что это не потерялось, а отложено.
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
				// Повторный заголовок того же раздела: содержимое дописывается, а не
				// заменяется. Модель иногда возвращается к разделу ниже по ответу, и вторая
				// половина там не менее полезна, чем первая.
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
	readScore(&parsed, text)
	return parsed, nil
}

// fenceLine отвечает, состоит ли строка из одного маркера блока кода.
//
// Промпт просит не оборачивать ответ в блок кода, но модель это делает регулярно, и обёртка
// внешне неотличима от находки: маркер попал бы в раздел отчёта, а «```markdown» первой
// строкой — в «Не разобрано». Содержимое при этом не теряется — снимается только сам маркер,
// поэтому готовый текст на замену, который модель прислала блоком кода, остаётся на месте.
func fenceLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "```") {
		return false
	}
	// Одинокий маркер — да, строка вида «```html <тег>» — нет: в ней есть содержимое.
	return !strings.Contains(strings.TrimPrefix(trimmed, "```"), " ")
}

// matchAnchor отвечает, заголовок ли это раздела, и какого.
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

// matchStray отвечает, заголовок ли это раздела, которого промпт не просит.
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

// droppedLine отвечает, выбрасывается ли строка целиком.
//
// Выбрасываются строки шапки, которых формат больше не просит, и строка итоговой оценки: её
// значение код печатает первой строкой отчёта, а вторая копия посреди находок только мешает.
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

// readScore вынимает из ответа итоговую оценку.
//
// Ищет по всему ответу, а не только до первого раздела: модель нередко ставит оценку в конец
// или внутрь раздела, и требовать её строго в шапке значило бы объявлять «оценка не
// разобрана» там, где она есть.
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

// noFindings — как промпт просит отвечать на разделе, где нечего сказать.
const noFindings = "замечаний нет"

// CountIssues считает находки раздела «все найденные ошибки».
//
// Счёт по строкам, а не по смыслу: строка списка — одна находка, подписи вроде
// «По содержанию:» находками не считаются. Это оценка масштаба для сводки и сортировки
// пачки, а не классификация: решений по этому числу не принимают, по нему решают, за какую
// страницу браться раньше.
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
