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

// boldQuestion — вопрос, выделенный жирным, вместе с ответом на той же строке.
//
// Ревью схлопывает заголовки вопросов в жирные ярлыки и приклеивает ответ к той же строке:
// «**Какой документ выдаётся?** Диплом установленного образца». Пара делится по закрывающей
// разметке, а не по вопросительному знаку: он встречается и внутри ответа.
var boldQuestion = regexp.MustCompile(`^(?:\*\*|__)\s*(.+?\?)\s*(?:\*\*|__)\s*(.*)$`)

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

// maxHeadingLevel — самый глубокий уровень, который распознаёт разбор заголовков.
// Блоку, открытому без маркера, назначается он же: тогда любой встреченный заголовок блок
// закрывает, потому что раздел с маркером внутри блока без маркеров — это уже другой раздел.
const maxHeadingLevel = 4

// maxPlainHeadingRunes отделяет заголовок, написанный обычной строкой, от абзаца, в котором
// слова «частые вопросы» просто упомянуты.
const maxPlainHeadingRunes = 120

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
	var block faqBlock
	var scan faqScan
	for _, raw := range strings.Split(strings.ReplaceAll(page, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if level, rawTitle, isHeading := parseHeading(line); isHeading {
			scan.readHeading(level, cleanFAQLine(rawTitle), &block)
			continue
		}
		scan.readLine(line, &block)
	}
	return block.String()
}

// faqScan — состояние разбора: внутри ли мы блока и чем он открыт.
type faqScan struct {
	inBlock bool
	// level — уровень заголовка, которым блок открыт. Заголовок того же или более высокого
	// уровня его закрывает.
	level int
	// plain — блок открыт обычной строкой, без маркера заголовка. Закрывать его тогда нечем,
	// кроме первой строки, которая на пару «вопрос — ответ» не похожа.
	plain bool
}

// readHeading обрабатывает строку-заголовок.
func (s *faqScan) readHeading(level int, title string, block *faqBlock) {
	switch {
	case !s.inBlock:
		// Блок ищется только среди верхних заголовков: «вопросы» в названии подраздела
		// внутри программы обучения блоком частых вопросов не являются.
		if level <= 2 && isFAQHeading(title) {
			s.inBlock, s.level, s.plain = true, level, false
		}
	case level <= s.level:
		// Начался следующий раздел страницы — блок закончился.
		s.inBlock = false
	case title != "":
		block.startPair(title, "")
	}
}

// readLine обрабатывает обычную строку страницы.
func (s *faqScan) readLine(line string, block *faqBlock) {
	cleaned := cleanFAQLine(line)
	if !s.inBlock {
		// Заголовок блока, пришедший обычной строкой. Ревью иногда снимает маркеры со всей
		// страницы разом, и тогда «Частые вопросы …» отличается от абзаца только смыслом:
		// статья 19 приехала так — блок в тексте есть, а разбор его не видел, и публикация
		// отказывала по пустому FAQ.
		if isFAQHeading(cleaned) && looksLikeHeading(cleaned) {
			s.inBlock, s.level, s.plain = true, maxHeadingLevel, true
		}
		return
	}
	// Вопрос и ответ, склеенные в одну строку. Разбирается до проверки ниже: там пара
	// узнаётся по вопросительному знаку в конце строки, а здесь конца у вопроса нет —
	// за ним сразу идёт ответ, и без разбора строка ушла бы в ответ предыдущей пары.
	if match := boldQuestion.FindStringSubmatch(line); match != nil {
		block.startPair(match[1], match[2])
		return
	}
	// Вопрос, написанный не заголовком, а обычной строкой: модель иногда оформляет их
	// жирным. Признак — вопросительный знак на месте, где ждём начало пары, и потому
	// разметку выделения приходится снять до проверки, а не после.
	if strings.HasSuffix(cleaned, "?") && block.lastAnswered() {
		block.startPair(cleaned, "")
		return
	}
	if block.empty() {
		// Вводный абзац блока до первого вопроса. Он относится к странице, а не к паре
		// «вопрос — ответ», и в FAQ не переносится.
		return
	}
	if s.plain && block.lastAnswered() {
		// Блок без маркеров закончился: следующий раздел страницы начинается такой же
		// обычной строкой, и отличить его от продолжения ответа больше нечем. Ответы в
		// таком блоке идут по одному абзацу — маркеры снимает ревью, а не человек, и
		// разбивку на абзацы оно сохраняет.
		s.inBlock, s.plain = false, false
		return
	}
	block.appendAnswer(cleaned)
}

// looksLikeHeading отсеивает абзац, в котором слова «частые вопросы» просто упомянуты:
// заголовок короток и точкой не заканчивается.
func looksLikeHeading(line string) bool {
	if line == "" || strings.HasSuffix(line, ".") {
		return false
	}
	return len([]rune(line)) <= maxPlainHeadingRunes
}

// faqPair — одна пара «вопрос — ответ»; ответ копится абзацами.
type faqPair struct {
	question string
	answer   []string
}

// faqBlock накапливает пары по мере чтения строк блока.
type faqBlock struct{ pairs []faqPair }

// startPair открывает новую пару. Пустой ответ означает, что он придёт следующими строками.
func (b *faqBlock) startPair(question, answer string) {
	pair := faqPair{question: strings.TrimSpace(question)}
	if answer := strings.TrimSpace(answer); answer != "" {
		pair.answer = append(pair.answer, answer)
	}
	b.pairs = append(b.pairs, pair)
}

// appendAnswer дописывает абзац к ответу последней пары.
func (b *faqBlock) appendAnswer(line string) {
	if len(b.pairs) == 0 {
		return
	}
	last := &b.pairs[len(b.pairs)-1]
	last.answer = append(last.answer, line)
}

func (b *faqBlock) empty() bool { return len(b.pairs) == 0 }

// lastAnswered отвечает, дописан ли ответ последней пары. Пустой блок считается дописанным:
// первая строка с вопросительным знаком открывает пару, а не продолжает несуществующую.
func (b *faqBlock) lastAnswered() bool {
	return len(b.pairs) == 0 || len(b.pairs[len(b.pairs)-1].answer) > 0
}

// String собирает хранимый вид блока — тот, который читает result.ParseFAQItems.
func (b *faqBlock) String() string {
	var faq strings.Builder
	for _, pair := range b.pairs {
		answer := strings.TrimSpace(strings.Join(pair.answer, "\n"))
		question := strings.TrimSpace(pair.question)
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
