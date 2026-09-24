package articleaudit

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Разделы ответа. Их три, и это весь вывод модели: разбор по критериям, самые важные ошибки и
// все ошибки списком. Имена внутренние — в самом ответе разделы называются словами, и по
// словам же ищутся (см. anchors).
const (
	SectionBreakdown = "breakdown"
	SectionCritical  = "critical"
	SectionIssues    = "issues"
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
//
// У разбора два имени: «Детальный разбор» просит промпт, «Разбор по критериям» модель пишет по
// привычке от прежних версий. Оба ведут в один раздел — отличать их незачем, а терять ответ
// из-за формулировки заголовка нельзя.
var anchors = []anchor{
	{SectionBreakdown, "детальный разбор"},
	{SectionBreakdown, "разбор по критериям"},
	{SectionBreakdown, "разбор по пунктам"},
	{SectionCritical, "критические ошибки"},
	{SectionIssues, "все найденные ошибки"},
}

// strayPrefixes — разделы, которых промпт больше не просит.
//
// Модель их всё равно пишет: рекомендации она считает частью хорошего ответа, а прежние версии
// промпта их и требовали. Узнавать их надо затем, чтобы такой раздел ушёл в «Не разобрано»
// целиком, а не подмешался в список ошибок, — иначе человек читал бы в перечне находок
// пересказ критериев оценки.
//
// Разбора по критериям здесь больше нет: он снова часть формата и разбирается якорем выше.
var strayPrefixes = []string{
	"поля записи", "рекомендации", "что работает хорошо",
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
	// Criteria — баллы разбора, по одному на критерий, в том порядке, в каком их написала
	// модель. Имена критериев у задач разные (у статей блога «Контент», у страниц услуг
	// «Программа обучения»), поэтому здесь они не названы: движок берёт то, что стоит в строке
	// перед баллом, и о задачах по-прежнему ничего не знает.
	Criteria []CriterionScore
	// Unparsed — всё, что не легло ни в один раздел и не опознано как строка шапки.
	Unparsed string
}

// Section возвращает содержимое раздела; отсутствующий раздел — пустая строка, а не отказ.
func (a Answer) Section(name string) string { return a.Sections[name] }

// CriterionScore — балл за один критерий разбора.
//
// Нужен затем, что общая оценка «11/20» не отличает слабую экспертность от слабой структуры, а
// решение «что чинить раньше» принимают как раз по этому. Разбор модель пишет словами, и
// человек их читает; отдельно разобранные числа нужны машине — сводке, которая складывает их
// по всей пачке.
type CriterionScore struct {
	Name  string
	Score int
	Max   int
}

// Share — доля набранного. Сравнивать критерии по самому баллу нельзя: они разного веса, и
// «2 из 3» сильнее, чем «3 из 5».
func (c CriterionScore) Share() float64 {
	if c.Max <= 0 {
		return 0
	}
	return float64(c.Score) / float64(c.Max)
}

// Text — как критерий печатается человеку.
func (c CriterionScore) Text() string {
	return fmt.Sprintf("%s %d/%d", c.Name, c.Score, c.Max)
}

// CriteriaTotal — сумма разбора: сколько набрано и из скольких.
//
// Сверять её с итоговой оценкой обязан отчёт, а не разбор: модель складывает пять чисел в уме
// и ошибается, а расхождение «в разборе 18, в итоговой 11» человек молча не заметит.
func (a Answer) CriteriaTotal() (int, int) {
	var score, limit int
	for _, criterion := range a.Criteria {
		score += criterion.Score
		limit += criterion.Max
	}
	return score, limit
}

// criterionNameLimit — предел длины имени критерия. Всё длиннее это фраза комментария, в
// которой модель повторила балл, а не строка разбора.
const criterionNameLimit = 40

// parseCriteria вынимает из раздела разбора баллы по критериям.
//
// Разбирается только то, что стоит перед баллом в той же строке: промпт просит по строке на
// критерий, но комментарий модель пишет тут же, и резать его нельзя — в отчёт он уходит
// дословно. Неразобранная строка не теряется: раздел печатается целиком как пришёл, а числа
// это надстройка над ним.
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
			// Второе упоминание того же критерия — повтор балла внутри комментария, а не
			// новая строка разбора.
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
//
// Пустое имя означает, что это не строка разбора, а предложение комментария с числом вида
// «2/3» внутри. Отличают их три признака: длина, отсутствие знаков конца предложения и
// заглавная буква в начале — имена критериев пишутся с неё во всех промптах, а зачин фразы
// («оценка 2/3 снята за…») со строчной.
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
	parsed.Criteria = parseCriteria(parsed.Sections[SectionBreakdown])
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
