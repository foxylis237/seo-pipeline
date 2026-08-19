package pprof2

import (
	"regexp"
	"strconv"
	"strings"
)

// headingLine распознаёт заголовок в форме, которую требуют промпты: «H2 - Заголовок».
// Разделитель допускается любой из привычных: человек и модель одинаково легко пишут дефис,
// тире или двоеточие.
var headingLine = regexp.MustCompile(`^[Hh]([1-4])\s*[-–—:.]?\s*(.*)$`)

// markdownHeading — та же строка, написанная решётками.
//
// Промпт Markdown-заголовки запрещает, но запрет адресован модели, а разбор читает то, что
// она прислала на самом деле. Сорванный формат не должен стоить статье всего блока вопросов.
var markdownHeading = regexp.MustCompile(`^(#{1,4})\s+(.*)$`)

// parseHeading возвращает уровень и текст заголовка, если строка им является.
func parseHeading(line string) (level int, title string, ok bool) {
	if match := headingLine.FindStringSubmatch(line); match != nil {
		level, _ = strconv.Atoi(match[1])
		return level, strings.TrimSpace(match[2]), true
	}
	if match := markdownHeading.FindStringSubmatch(line); match != nil {
		return len(match[1]), strings.TrimSpace(match[2]), true
	}
	return 0, "", false
}

// faqHeadings — слова, по которым узнаётся блок частых вопросов.
//
// Список тот же, каким блок ищет проверка разметки, плюс форма «часто задаваемые вопросы»:
// промпт структуры просит начинать заголовок со слов «Частые вопросы», но модель вольна
// дописать к ним тему страницы, а иногда переформулирует всё целиком.
var faqHeadings = []string{"частые вопросы", "часто задаваемые вопросы", "вопросы и ответы", "faq"}

// ExtractFAQ вынимает частые вопросы из написанной страницы.
//
// Отдельной стадии, которая просила бы у модели FAQ, у pprof_2 нет намеренно: вопросы уже
// написаны в тексте — их требует структура, — и второй запрос к модели дал бы второй,
// отличающийся от опубликованного, набор. Здесь тот же самый текст просто перекладывается в
// формат хранения: «Вопрос:» и «Ответ:», как их читает result.ParseFAQItems.
//
// Пустая строка — законный ответ: она означает «блока в тексте нет». Ронять из-за этого
// генерацию нельзя (страница написана и оплачена), а публиковать статью без FAQ не даст
// проверка публикации.
func ExtractFAQ(page string) string {
	type item struct {
		question string
		answer   []string
	}
	var items []item
	inBlock, blockLevel := false, 0
	for _, raw := range strings.Split(strings.ReplaceAll(page, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if level, rawTitle, isHeading := parseHeading(line); isHeading {
			title := cleanFAQLine(rawTitle)
			switch {
			case !inBlock:
				// Блок ищется только среди верхних заголовков: «вопросы» в названии
				// подраздела внутри программы обучения блоком частых вопросов не являются.
				if level <= 2 && isFAQHeading(title) {
					inBlock, blockLevel = true, level
				}
			case level <= blockLevel:
				// Начался следующий раздел страницы — блок закончился.
				inBlock = false
			case title != "":
				items = append(items, item{question: title})
			}
			continue
		}
		if !inBlock {
			continue
		}
		// Вопрос, написанный не заголовком, а обычной строкой: модель иногда оформляет их
		// жирным. Признак — вопросительный знак на месте, где ждём начало пары, и потому
		// разметку выделения приходится снять до проверки, а не после.
		cleaned := cleanFAQLine(line)
		if strings.HasSuffix(cleaned, "?") && (len(items) == 0 || len(items[len(items)-1].answer) > 0) {
			items = append(items, item{question: cleaned})
			continue
		}
		if len(items) == 0 {
			// Вводный абзац блока до первого вопроса. Он относится к странице, а не к паре
			// «вопрос — ответ», и в FAQ не переносится.
			continue
		}
		last := &items[len(items)-1]
		last.answer = append(last.answer, cleaned)
	}

	var faq strings.Builder
	for _, entry := range items {
		answer := strings.TrimSpace(strings.Join(entry.answer, "\n"))
		question := strings.TrimSpace(entry.question)
		// Пара без ответа не сохраняется: разбор FAQ на чтении такую пару отвергает целиком,
		// и один оборванный вопрос унёс бы с собой весь блок.
		if question == "" || answer == "" {
			continue
		}
		if faq.Len() > 0 {
			faq.WriteString("\n\n")
		}
		faq.WriteString("Вопрос: " + question + "\nОтвет: " + answer)
	}
	return faq.String()
}

// isFAQHeading отвечает, назван ли так блок частых вопросов.
func isFAQHeading(title string) bool {
	lowered := strings.ToLower(title)
	for _, name := range faqHeadings {
		if strings.Contains(lowered, name) {
			return true
		}
	}
	return false
}

// cleanFAQLine снимает разметку выделения, которую модель ставит вокруг вопросов.
func cleanFAQLine(line string) string {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "*_"))
}
