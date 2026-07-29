// Package validator performs deterministic, local checks of generated articles.
package validator

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

type Severity string

const (
	SeverityPassed  Severity = "passed"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type Status string

const (
	StatusValid       Status = "VALID"
	StatusNeedsReview Status = "NEEDS_REVIEW"
	StatusInvalid     Status = "INVALID"
)

type Issue struct {
	Check    string
	Severity Severity
	Message  string
	Line     int
	Fragment string
}

type HeadingStats struct{ H1, H2, H3, H4 int }
type WordStat struct {
	Word  string
	Count int
}
type PhraseStat struct {
	Phrase string
	Count  int
}

type SentenceStats struct {
	Average, Minimum, Maximum float64
	Short, Long               int
}

type Report struct {
	Characters       int
	Words            int
	Sentences        int
	Headings         HeadingStats
	Issues           []Issue
	TopWords         []WordStat
	TrackedWords     []WordStat
	BoldPhrases      []PhraseStat
	UsedKeywords     []PhraseStat
	FrequentLSI      []PhraseStat
	SentenceLengths  SentenceStats
	StructureSkipped bool
	KeywordsSkipped  bool
	LSISkipped       bool
}

type Input struct {
	Article           string
	ExpectedStructure string
	Keywords          []string
	LSIWords          []string
	RequireFAQ        bool
	RequireTable      bool
}

var (
	wordRE             = regexp.MustCompile(`[\p{L}\p{N}]+(?:[-'’][\p{L}\p{N}]+)*`)
	sentenceRE         = regexp.MustCompile(`[.!?]+`)
	headingRE          = regexp.MustCompile(`(?i)^H([1-4])\s*([-—–])\s*(.*?)\s*$`)
	canonicalHeadingRE = regexp.MustCompile(`^H[1-4] - \S`)
	headingLikeRE      = regexp.MustCompile(`(?i)^H\d+(?:\s|:|-|—|–|$)`)
	expectedHeadingRE  = regexp.MustCompile(`(?i)^H([1-4])\s*[-:—–]\s*(.*?)\s*$`)
	htmlRE             = regexp.MustCompile(`(?i)</?[a-z][^>]*>`)
	markdownHeadingRE  = regexp.MustCompile(`(?m)^\s*#{1,6}\s+`)
	markdownLinkRE     = regexp.MustCompile(`\[[^\]]+\]\([^\)]+\)`)
	bulletRE           = regexp.MustCompile(`^\s*[-*]\s+\S`)
	numberedRE         = regexp.MustCompile(`^\s*\d+\.\s+\S`)
	boldRE             = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	multiBlankRE       = regexp.MustCompile(`\n[\t ]*\n[\t ]*\n`)
)

var forbiddenPhrases = []string{
	"следует отметить", "вышеупомянутый", "важно понимать", "на самом деле",
	"стоит сказать", "надо отметить", "в современном мире", "каждый знает, что",
	"существует множество различных видов",
}

var wateryPhrases = []string{
	"очень", "действительно", "крайне", "невероятно", "безусловно", "несомненно",
	"играет важную роль", "становится всё более востребованным", "целый мир", "настоящее призвание",
}

var hardAdvertising = []string{
	"гарантированное трудоустройство", "гарантируем трудоустройство", "зарплата от 300к",
	"зарплата от 300 000", "гарантированный доход",
}

var softAdvertising = []string{
	"запишитесь прямо сейчас", "оставьте заявку", "успейте записаться", "лучший курс",
	"уникальная программа", "получите профессию мечты", "измените свою жизнь",
}

var stopWords = map[string]struct{}{
	"и": {}, "или": {}, "но": {}, "как": {}, "что": {}, "это": {}, "для": {}, "при": {},
	"под": {}, "над": {}, "его": {}, "её": {}, "они": {}, "вы": {}, "мы": {}, "на": {},
	"из": {}, "по": {}, "от": {}, "до": {}, "со": {}, "без": {}, "уже": {}, "также": {},
	"же": {}, "бы": {}, "не": {}, "все": {}, "так": {}, "когда": {}, "если": {}, "где": {},
	"который": {}, "можно": {}, "нужно": {}, "есть": {}, "быть": {}, "был": {}, "была": {},
}

var trackedWords = []string{"работа", "работать", "профессия", "обучение", "специалист", "заниматься", "узнавать"}

type heading struct {
	Level, Line       int
	Title, Normalized string
}

func Validate(input Input) Report {
	text := strings.TrimSpace(strings.ReplaceAll(input.Article, "[[ARTICLE_COMPLETE]]", ""))
	report := Report{Characters: len([]rune(text))}
	if text == "" {
		add(&report, "empty_text", SeverityError, "Статья пустая", 0, "")
		report.StructureSkipped = strings.TrimSpace(input.ExpectedStructure) == ""
		report.KeywordsSkipped = len(input.Keywords) == 0
		report.LSISkipped = len(input.LSIWords) == 0
		return report
	}

	lines := strings.Split(text, "\n")
	words := wordRE.FindAllString(strings.ToLower(text), -1)
	report.Words = len(words)
	report.Sentences = len(sentenceRE.FindAllString(text, -1))
	if report.Characters < 8000 || report.Characters > 12000 {
		add(&report, "length", SeverityError, fmt.Sprintf("Объём вне диапазона 8000–12000: %d символов", report.Characters), 0, "")
	}

	headings := validateHeadings(lines, &report)
	validateExpectedStructure(input.ExpectedStructure, headings, &report)
	validateMarkupAndLinks(text, lines, headings, &report)
	validatePhrases(lines, &report)
	validateFAQ(lines, headings, input.RequireFAQ || structureRequiresFAQ(input.ExpectedStructure), &report)
	hasTable := detectTable(lines)
	if input.RequireTable && !hasTable {
		add(&report, "table", SeverityError, "Обязательная текстовая таблица не найдена", 0, "")
	}
	validatePracticalValue(text, lines, hasTable, &report)
	validateBold(text, &report)
	validateWords(words, &report)
	validateKeywords(text, report.Words, input.Keywords, &report)
	validateLSI(text, report.Characters, input.LSIWords, &report)
	validateParagraphs(text, lines, &report)
	validateSentenceLengths(text, &report)
	return report
}

func ResultStatus(report Report) Status {
	status := StatusValid
	for _, issue := range report.Issues {
		if issue.Severity == SeverityError {
			return StatusInvalid
		}
		if issue.Severity == SeverityWarning {
			status = StatusNeedsReview
		}
	}
	return status
}

func validateHeadings(lines []string, report *Report) []heading {
	var result []heading
	firstNonEmpty := 0
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if firstNonEmpty == 0 {
			firstNonEmpty = i + 1
		}
		match := headingRE.FindStringSubmatch(line)
		if match == nil {
			if headingLikeRE.MatchString(line) {
				add(report, "heading_format", SeverityError, "Строка похожа на заголовок, но имеет неверный формат", i+1, fragment(line))
			}
			continue
		}
		level := int(match[1][0] - '0')
		title := strings.TrimSpace(match[3])
		if title == "" {
			add(report, "heading_empty", SeverityError, "Пустой заголовок", i+1, fragment(line))
		}
		if !canonicalHeadingRE.MatchString(line) {
			add(report, "heading_style", SeverityWarning, "Заголовок отличается от формата H2 - Заголовок", i+1, fragment(line))
		}
		result = append(result, heading{Level: level, Line: i + 1, Title: title, Normalized: normalize(title)})
		switch level {
		case 1:
			report.Headings.H1++
		case 2:
			report.Headings.H2++
		case 3:
			report.Headings.H3++
		case 4:
			report.Headings.H4++
		}
	}
	if report.Headings.H1 != 1 {
		add(report, "h1_count", SeverityError, fmt.Sprintf("Ожидался ровно один H1, найдено: %d", report.Headings.H1), 0, "")
	}
	if report.Headings.H2 < 1 {
		add(report, "h2_count", SeverityError, "Не найден ни один H2", 0, "")
	}
	if len(result) == 0 || result[0].Level != 1 || result[0].Line != firstNonEmpty {
		add(report, "h1_position", SeverityError, "H1 должен быть первой непустой строкой", firstNonEmpty, "")
	}
	seen := map[string]int{}
	for i, h := range result {
		if previous, ok := seen[h.Normalized]; ok && h.Normalized != "" {
			add(report, "heading_duplicate", SeverityError, fmt.Sprintf("Дублирующийся заголовок (первый на строке %d)", previous), h.Line, fragment(h.Title))
		} else {
			seen[h.Normalized] = h.Line
		}
		if h.Level == 3 && !hasPriorLevel(result[:i], 2) {
			add(report, "heading_hierarchy", SeverityError, "H3 расположен до первого H2", h.Line, fragment(h.Title))
		}
		if h.Level == 4 && !hasPriorLevel(result[:i], 3) {
			add(report, "heading_hierarchy", SeverityError, "H4 расположен до первого H3", h.Line, fragment(h.Title))
		}
		if i > 0 && h.Level > result[i-1].Level+1 {
			add(report, "heading_hierarchy", SeverityError, fmt.Sprintf("Перескок уровня H%d → H%d", result[i-1].Level, h.Level), h.Line, fragment(h.Title))
		}
		end := len(lines)
		if i+1 < len(result) {
			end = result[i+1].Line - 1
		}
		hasContent := false
		for lineIndex := h.Line; lineIndex < end; lineIndex++ {
			if strings.TrimSpace(lines[lineIndex]) != "" {
				hasContent = true
				break
			}
		}
		if !hasContent {
			add(report, "heading_content", SeverityError, "После заголовка нет содержимого", h.Line, fragment(h.Title))
		}
	}
	if report.Headings.H4 > 15 {
		add(report, "h4_count", SeverityWarning, fmt.Sprintf("Возможно избыточная детализация: H4 = %d", report.Headings.H4), 0, "")
	}
	return result
}

func validateExpectedStructure(expected string, actual []heading, report *Report) {
	if strings.TrimSpace(expected) == "" {
		report.StructureSkipped = true
		return
	}
	var wanted []heading
	for i, raw := range strings.Split(expected, "\n") {
		m := expectedHeadingRE.FindStringSubmatch(strings.TrimSpace(raw))
		if m == nil {
			continue
		}
		level := int(m[1][0] - '0')
		if level < 2 {
			continue
		}
		wanted = append(wanted, heading{Level: level, Line: i + 1, Title: strings.TrimSpace(m[2]), Normalized: normalize(m[2])})
	}
	actualByKey := map[string]int{}
	for i, h := range actual {
		if h.Level >= 2 {
			actualByKey[fmt.Sprintf("%d:%s", h.Level, h.Normalized)] = i
		}
	}
	last := -1
	wantedKeys := map[string]struct{}{}
	for _, h := range wanted {
		key := fmt.Sprintf("%d:%s", h.Level, h.Normalized)
		wantedKeys[key] = struct{}{}
		position, ok := actualByKey[key]
		if !ok {
			severity := SeverityError
			if h.Level == 4 {
				severity = SeverityWarning
			}
			add(report, "expected_structure", severity, fmt.Sprintf("Отсутствует обязательный H%d: %s", h.Level, h.Title), 0, "")
			continue
		}
		if (h.Level == 2 || h.Level == 3) && position < last {
			add(report, "structure_order", SeverityError, fmt.Sprintf("Нарушен порядок заголовка: H%d - %s", h.Level, h.Title), actual[position].Line, "")
		}
		if h.Level == 2 || h.Level == 3 {
			last = position
		}
	}
	var extras []string
	for _, h := range actual {
		if h.Level >= 2 {
			if _, ok := wantedKeys[fmt.Sprintf("%d:%s", h.Level, h.Normalized)]; !ok {
				extras = append(extras, fmt.Sprintf("H%d - %s", h.Level, h.Title))
			}
		}
	}
	if len(extras) > 0 {
		add(report, "extra_headings", SeverityWarning, "Дополнительные заголовки: "+strings.Join(extras, "; "), 0, "")
	}
}

func validateMarkupAndLinks(text string, lines []string, headings []heading, report *Report) {
	if loc := htmlRE.FindStringIndex(text); loc != nil {
		line, frag := location(text, loc[0])
		add(report, "html", SeverityError, "Найден HTML-тег", line, frag)
	}
	if loc := markdownHeadingRE.FindStringIndex(text); loc != nil {
		line, frag := location(text, loc[0])
		add(report, "markdown", SeverityError, "Найден Markdown-заголовок", line, frag)
	}
	if strings.Contains(text, "```") {
		line, frag := location(text, strings.Index(text, "```"))
		add(report, "markdown", SeverityError, "Найден fenced code block", line, frag)
	}
	lower := strings.ToLower(text)
	for _, token := range []string{"http://", "https://", "www."} {
		if index := strings.Index(lower, token); index >= 0 {
			line, frag := location(text, index)
			add(report, "external_link", SeverityError, "Найдена внешняя ссылка", line, frag)
			break
		}
	}
	if loc := markdownLinkRE.FindStringIndex(text); loc != nil {
		line, frag := location(text, loc[0])
		add(report, "external_link", SeverityError, "Найдена Markdown-ссылка", line, frag)
	}
	firstH1 := len(lines) + 1
	if len(headings) > 0 && headings[0].Level == 1 {
		firstH1 = headings[0].Line
	}
	for i := 0; i < firstH1-1; i++ {
		if strings.Contains(normalize(lines[i]), "вот готовая статья") {
			add(report, "service_text", SeverityError, "Служебное вступление модели до H1", i+1, fragment(lines[i]))
		}
	}
	for i, line := range lines {
		if strings.Contains(normalize(line), "надеюсь статья была полезна") {
			add(report, "service_text", SeverityError, "Служебное заключение модели", i+1, fragment(line))
		}
	}
}

func validatePhrases(lines []string, report *Report) {
	checkPhraseList(lines, forbiddenPhrases, "forbidden_phrase", SeverityError, report)
	checkPhraseList(lines, wateryPhrases, "watery_language", SeverityWarning, report)
	checkPhraseList(lines, hardAdvertising, "advertising_promise", SeverityError, report)
	checkPhraseList(lines, softAdvertising, "advertising_style", SeverityWarning, report)
}

func checkPhraseList(lines, phrases []string, check string, severity Severity, report *Report) {
	for _, phrase := range phrases {
		count, firstLine, firstFragment := 0, 0, ""
		needle := normalize(phrase)
		for i, line := range lines {
			occurrences := strings.Count(normalize(line), needle)
			if occurrences > 0 {
				count += occurrences
				if firstLine == 0 {
					firstLine = i + 1
					firstFragment = fragment(line)
				}
			}
		}
		if count > 0 {
			add(report, check, severity, fmt.Sprintf("«%s» — %d вхожд.", phrase, count), firstLine, firstFragment)
		}
	}
}

func validateFAQ(lines []string, headings []heading, required bool, report *Report) {
	start, end := -1, len(lines)
	for i, h := range headings {
		if isFAQ(h.Title) {
			start = h.Line
			for _, next := range headings[i+1:] {
				if next.Level <= h.Level {
					end = next.Line - 1
					break
				}
			}
			break
		}
	}
	if start < 0 {
		if required {
			add(report, "faq", SeverityError, "Обязательный FAQ не найден", 0, "")
		}
		return
	}
	questions := 0
	for i := start; i < end; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		m := headingRE.FindStringSubmatch(line)
		title := line
		if m != nil {
			title = strings.TrimSpace(m[3])
		}
		if !strings.HasSuffix(title, "?") {
			continue
		}
		questions++
		answer := ""
		for j := i + 1; j < end; j++ {
			candidate := strings.TrimSpace(lines[j])
			if candidate == "" {
				continue
			}
			cm := headingRE.FindStringSubmatch(candidate)
			if strings.HasSuffix(candidate, "?") || (cm != nil && strings.HasSuffix(strings.TrimSpace(cm[3]), "?")) {
				break
			}
			if cm == nil {
				answer = candidate
			}
			break
		}
		if utf8.RuneCountInString(answer) < 20 {
			add(report, "faq_answer", SeverityError, "После вопроса нет содержательного ответа", i+1, fragment(title))
		}
	}
	if questions < 3 || questions > 6 {
		add(report, "faq_count", SeverityError, fmt.Sprintf("FAQ должен содержать 3–6 вопросов, найдено: %d", questions), start, "")
	}
	if questions == 0 {
		add(report, "faq_format", SeverityWarning, "FAQ найден, но формат вопросов определить не удалось", start, "")
	}
}

func validatePracticalValue(text string, lines []string, hasTable bool, report *Report) {
	hasList := false
	for _, line := range lines {
		if bulletRE.MatchString(line) || numberedRE.MatchString(line) {
			hasList = true
			break
		}
	}
	lower := normalize(text)
	hasExample := false
	for _, phrase := range []string{"например", "пример", "на практике", "разберём ситуацию"} {
		if strings.Contains(lower, phrase) {
			hasExample = true
			break
		}
	}
	if !hasList && !hasTable && !hasExample {
		add(report, "practical_value", SeverityWarning, "В статье не найдена практическая подача материала", 0, "")
	}
}

func validateBold(text string, report *Report) {
	if strings.Count(text, "**")%2 != 0 {
		add(report, "bold_format", SeverityError, "Найдена незакрытая пара **", 0, "")
	}
	counts := map[string]int{}
	for _, m := range boldRE.FindAllStringSubmatch(text, -1) {
		counts[normalize(m[1])]++
	}
	for phrase, count := range counts {
		report.BoldPhrases = append(report.BoldPhrases, PhraseStat{phrase, count})
		if count > 3 {
			add(report, "bold_repeat", SeverityWarning, fmt.Sprintf("Фраза выделена жирным %d раз: %s", count, phrase), 0, "")
		}
	}
	sortPhraseStats(report.BoldPhrases)
	total := len(boldRE.FindAllStringSubmatch(text, -1))
	if total > 20 {
		add(report, "bold_count", SeverityWarning, fmt.Sprintf("Слишком много жирных выделений: %d", total), 0, "")
	}
}

func validateWords(words []string, report *Report) {
	counts := map[string]int{}
	meaningful := 0
	for _, word := range words {
		if utf8.RuneCountInString(word) < 3 {
			continue
		}
		if _, skip := stopWords[word]; skip {
			continue
		}
		counts[word]++
		meaningful++
	}
	stats := make([]WordStat, 0, len(counts))
	for word, count := range counts {
		stats = append(stats, WordStat{word, count})
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Count == stats[j].Count {
			return stats[i].Word < stats[j].Word
		}
		return stats[i].Count > stats[j].Count
	})
	if len(stats) > 15 {
		stats = stats[:15]
	}
	report.TopWords = stats
	for _, word := range trackedWords {
		report.TrackedWords = append(report.TrackedWords, WordStat{word, counts[word]})
	}
	for word, count := range counts {
		share := 0.0
		if meaningful > 0 {
			share = float64(count) * 100 / float64(meaningful)
		}
		if count > 20 || share > 2 {
			add(report, "word_repetition", SeverityWarning, fmt.Sprintf("Возможный переспам: %s — %d (%.1f%% значимых слов)", word, count, share), 0, "")
		}
	}
}

func validateKeywords(text string, words int, keywords []string, report *Report) {
	if len(keywords) == 0 {
		report.KeywordsSkipped = true
		return
	}
	normalizedText := normalize(text)
	unique := map[string]struct{}{}
	totalOccurrences := 0
	for _, raw := range keywords {
		key := normalize(raw)
		if key == "" {
			continue
		}
		unique[key] = struct{}{}
	}
	for key := range unique {
		count := strings.Count(normalizedText, key)
		if count > 0 {
			report.UsedKeywords = append(report.UsedKeywords, PhraseStat{key, count})
			totalOccurrences += count
		}
		if count > 3 {
			add(report, "keyword_repetition", SeverityError, fmt.Sprintf("Ключ «%s» использован %d раз", key, count), 0, "")
		}
	}
	sortPhraseStats(report.UsedKeywords)
	share := float64(len(report.UsedKeywords)) * 100 / float64(len(unique))
	if share < 10 || share > 25 {
		add(report, "keyword_share", SeverityError, fmt.Sprintf("Использовано %.1f%% уникальных ключей; допустимо 10–25%%", share), 0, "")
	}
	if words > 0 {
		density := float64(totalOccurrences) * 100 / float64(words)
		if density < 0.8 || density > 1.2 {
			add(report, "keyword_density", SeverityWarning, fmt.Sprintf("Приблизительная плотность точных ключей: %.2f%%", density), 0, "")
		}
	}
}

func validateLSI(text string, characters int, lsi []string, report *Report) {
	if len(lsi) == 0 {
		report.LSISkipped = true
		return
	}
	normalizedText := normalize(text)
	unique := map[string]struct{}{}
	for _, raw := range lsi {
		term := normalize(raw)
		if term != "" {
			unique[term] = struct{}{}
		}
	}
	used := 0
	for term := range unique {
		count := strings.Count(normalizedText, term)
		if count > 0 {
			used++
			report.FrequentLSI = append(report.FrequentLSI, PhraseStat{term, count})
		}
	}
	sortPhraseStats(report.FrequentLSI)
	blocks := semanticBlocks(text)
	if len(blocks) >= 2 {
		for term := range unique {
			present := 0
			for _, block := range blocks {
				if strings.Contains(block, term) {
					present++
				}
			}
			if present >= len(blocks)-1 {
				add(report, "lsi_blocks", SeverityWarning, fmt.Sprintf("LSI «%s» встречается почти в каждом смысловом блоке (%d из %d)", term, present, len(blocks)), 0, "")
			}
		}
	}
	if characters > 0 && float64(used)*1000/float64(characters) > 7 {
		add(report, "lsi_density", SeverityWarning, fmt.Sprintf("Более 7 разных LSI на 1000 символов: %.1f", float64(used)*1000/float64(characters)), 0, "")
	}
}

func validateParagraphs(text string, lines []string, report *Report) {
	if multiBlankRE.MatchString(text) {
		add(report, "blank_lines", SeverityWarning, "Найдено несколько пустых строк подряд", 0, "")
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || headingRE.MatchString(trimmed) || bulletRE.MatchString(trimmed) || numberedRE.MatchString(trimmed) || strings.Contains(trimmed, "|") || strings.Contains(trimmed, "\t") {
			continue
		}
		length := utf8.RuneCountInString(trimmed)
		if length > 600 {
			add(report, "paragraph_length", SeverityWarning, fmt.Sprintf("Длинный абзац: %d символов", length), i+1, fragment(trimmed))
		}
		if length < 20 && len(sentenceRE.FindAllString(trimmed, -1)) == 1 {
			add(report, "short_paragraph", SeverityWarning, "Очень короткий абзац из одного предложения", i+1, fragment(trimmed))
		}
	}
	for i, line := range lines {
		if !headingRE.MatchString(strings.TrimSpace(line)) {
			continue
		}
		blanks := 0
		for j := i + 1; j < len(lines) && strings.TrimSpace(lines[j]) == ""; j++ {
			blanks++
		}
		if blanks > 1 {
			add(report, "heading_spacing", SeverityWarning, "После заголовка более одной пустой строки", i+1, fragment(line))
		}
	}
}

func validateSentenceLengths(text string, report *Report) {
	parts := sentenceRE.Split(text, -1)
	min, max, total, count := 0, 0, 0, 0
	lengthCounts := map[int]int{}
	for _, part := range parts {
		n := len(wordRE.FindAllString(part, -1))
		if n == 0 {
			continue
		}
		if min == 0 || n < min {
			min = n
		}
		if n > max {
			max = n
		}
		total += n
		count++
		lengthCounts[n]++
		if n >= 3 && n <= 5 {
			report.SentenceLengths.Short++
		}
		if n >= 15 && n <= 20 {
			report.SentenceLengths.Long++
		}
	}
	if count > 0 {
		report.SentenceLengths.Average = float64(total) / float64(count)
		report.SentenceLengths.Minimum = float64(min)
		report.SentenceLengths.Maximum = float64(max)
	}
	if report.Words > 200 && (report.SentenceLengths.Short == 0 || report.SentenceLengths.Long == 0) {
		add(report, "sentence_variability", SeverityWarning, "Недостаточное чередование коротких и длинных предложений", 0, "")
	}
	mostCommon := 0
	for _, frequency := range lengthCounts {
		if frequency > mostCommon {
			mostCommon = frequency
		}
	}
	if count >= 10 && mostCommon*100/count >= 60 {
		add(report, "sentence_uniformity", SeverityWarning, "Большинство предложений имеет одинаковую длину", 0, "")
	}
}

func detectTable(lines []string) bool {
	consecutive := 0
	for _, line := range lines {
		if strings.Contains(line, "|") || strings.Contains(line, "\t") {
			consecutive++
			if consecutive >= 2 {
				return true
			}
		} else if strings.TrimSpace(line) != "" {
			consecutive = 0
		}
	}
	return false
}
func structureRequiresFAQ(s string) bool {
	lower := normalize(s)
	return strings.Contains(lower, "faq") || strings.Contains(lower, "частые вопросы") || strings.Contains(lower, "вопросы и ответы")
}
func isFAQ(s string) bool { return structureRequiresFAQ(s) }
func semanticBlocks(text string) []string {
	var blocks []string
	var current strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if headingRE.MatchString(strings.TrimSpace(line)) && current.Len() > 0 {
			blocks = append(blocks, normalize(current.String()))
			current.Reset()
		}
		current.WriteString(line)
		current.WriteByte('\n')
	}
	if current.Len() > 0 {
		blocks = append(blocks, normalize(current.String()))
	}
	return blocks
}
func hasPriorLevel(headings []heading, level int) bool {
	for _, h := range headings {
		if h.Level == level {
			return true
		}
	}
	return false
}
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("—", " ", "–", " ", "-", " ", "\t", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}
func fragment(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > 100 {
		r = r[:100]
	}
	return string(r)
}
func location(text string, index int) (int, string) {
	line := strings.Count(text[:index], "\n") + 1
	lines := strings.Split(text, "\n")
	if line <= len(lines) {
		return line, fragment(lines[line-1])
	}
	return line, ""
}
func add(r *Report, check string, severity Severity, message string, line int, fragment string) {
	r.Issues = append(r.Issues, Issue{check, severity, message, line, fragment})
}
func sortPhraseStats(stats []PhraseStat) {
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Count == stats[j].Count {
			return stats[i].Phrase < stats[j].Phrase
		}
		return stats[i].Count > stats[j].Count
	})
}
