package obuch2

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Числа программы на странице услуги названы дважды: колонками книги они уходят в поля
// записи, и теми же словами их печатает текст — в плашке параметров, в модулях, в прозе и в
// частых вопросах. Разойтись им нельзя, а увидеть расхождение можно только глазами на самой
// странице: поля рисует тема над телом, текст идёт под ним, и в админке они не встречаются.
//
// На живых страницах площадки это уже случилось: у гида-переводчика в плашке темы 144 часа, а
// в тексте «программа объёмом 150 часов». Ровно поэтому числа и берутся из книги дословно —
// и ровно поэтому их сверяет код, а не обещание промпта.
//
// Проверяются три числа, названные владельцем: объём программы, срок и стоимость. Документ и
// аттестация сюда не входят — это длинные формулировки, и сверка по ним ловила бы синонимы.

var (
	// Часы: «150 часов», «72 ак. часа». Число может прийти с разделителем тысяч.
	factHoursRE = regexp.MustCompile(`(\d[\d\s\x{00a0}]*)\s*(?:ак\.?\s*)?час[а-яё]*`)
	// Деньги: «от 5 000 ₽», «5000 руб.».
	factMoneyRE = regexp.MustCompile(`(\d[\d\s\x{00a0}]*)\s*(?:₽|руб[а-яё.]*)`)
	// Срок: «от 2 недель», «от 3 дней». Проза вроде «в течение недели» сюда не попадает —
	// сверять её не с чем, единицу без числа книга не называет. Годы в форму не входят
	// намеренно: «возраст от 18 лет» — требование к слушателю, а не срок обучения, и
	// программ длиной в год у площадки нет. Книга, назвавшая срок годами, свою проверку
	// выключит — это дешевле, чем ронять публикацию на требованиях к возрасту.
	factTermRE = regexp.MustCompile(`(?i)от\s+(\d+)\s+(недел|дн|месяц)[а-яё]*`)
	// Ритм занятий — не объём программы: «по 4 часа в день» говорит о расписании, и совпадать
	// с колонкой книги оно не обязано.
	factPaceRE = regexp.MustCompile(`^\s*в\s+(день|сутки|недел[юи]|месяц)`)
	// Промежуток времени — тоже не объём: «пришлём в течение 24 часов» 29.09.2026 остановило
	// публикацию страницы 42. Объём программы так не называют.
	factSpanRE = regexp.MustCompile(`(?:в\s+течение|через|спустя)\s*$`)
	// Таблица заработка: три столбца с шапкой. Её цифры — доход по разрядам, а не цена курса,
	// и в сверку они не идут. Плашка параметров шапки не имеет — она проверяется как текст.
	factSalaryTableRE = regexp.MustCompile(`(?s)<table[^>]*>.*?</table>`)
	factTheadRE       = regexp.MustCompile(`(?i)<thead[\s>]`)
	// Шапку <thead> оформление блоков пересобирает обычной строкой, поэтому таблица заработка
	// узнаётся ещё и по второму столбцу: плашка параметров одноколоночная. Без этого 28.09.2026
	// доход «от 70 000 до 100 000 ₽» страницы 3 объявлялся ценой курса мимо книги.
	factRowRE  = regexp.MustCompile(`(?is)<tr[\s>].*?</tr>`)
	factCellRE = regexp.MustCompile(`(?i)<t[dh][\s>]`)
)

// ProgramFacts — программа так, как её назвала книга импорта: числа и две формулировки,
// которые текст печатает словами, а поля записи хранят служебным значением селекта.
type ProgramFacts struct {
	Hours       string
	Duration    string
	Price       string
	Document    string
	Attestation string
}

// presetWords — по какому слову книги узнаётся служебное значение селекта, по группам.
//
// Текст страницы и селект записи говорят об одном и том же разными словами: в тексте стоит
// формулировка книги («Итоговое тестирование»), в поле — служебное «test», и площадка рисует
// его своей подписью над телом. Сойтись они обязаны, а увидеть расхождение можно только
// глазами на самой странице: подпись селекта рисует тема, формулировку — текст под ней.
//
// Слово, а не строка целиком: книга перечисляет документы через запятую («Удостоверение …,
// свидетельство …»), и требовать от неё точной формы нельзя. Зато порядок значим — основной
// документ книга называет первым, и он же уходит селектом. Без этого правила «Свидетельство о
// повышении квалификации …, удостоверение …» проходило бы при селекте «удостоверение»: слово
// в строке есть, а вид документа у страницы другой.
var presetWords = []map[string]string{
	{"svidetelstvo": "свидетельств", "udostoverenie": "удостоверен"},
	{"exam": "экзамен", "test": "тестирован"},
}

// CheckProgramPreset сверяет формулировки книги со служебными значениями, которые уйдут в
// поля записи по типу страницы.
//
// Незамеренный тип записи проверять нечем: пресет у него пустой, и поля не уходят вовсе.
// Пустая колонка книги свою проверку выключает — так же, как у чисел.
func CheckProgramPreset(preset ProgramPreset, facts ProgramFacts) []string {
	var issues []string
	pairs := []struct {
		name, preset, book string
		words              map[string]string
	}{
		{"документ", preset.Document, facts.Document, presetWords[0]},
		{"аттестация", preset.Attestation, facts.Attestation, presetWords[1]},
	}
	for _, pair := range pairs {
		want, known := pair.words[pair.preset]
		book := strings.ToLower(strings.TrimSpace(pair.book))
		if !known || book == "" {
			continue
		}
		if leadingWord(book, pair.words) == want {
			continue
		}
		issues = append(issues, fmt.Sprintf("%s: в книге «%s», а в поле записи уйдёт %q",
			pair.name, strings.TrimSpace(pair.book), pair.preset))
	}
	return issues
}

// leadingWord — слово группы, названное в строке первым. Пустая строка означает, что книга не
// назвала ни одного из них.
func leadingWord(book string, words map[string]string) string {
	leading, at := "", -1
	for _, word := range words {
		found := strings.Index(book, word)
		if found < 0 {
			continue
		}
		if at < 0 || found < at {
			leading, at = word, found
		}
	}
	return leading
}

// multiColumnTable reports a table whose first row has more than one cell.
func multiColumnTable(table string) bool {
	row := factRowRE.FindString(table)
	return len(factCellRE.FindAllStringIndex(row, 2)) > 1
}

// CheckProgramFacts сверяет числа в тексте страницы с числами книги.
//
// Возвращает расхождения строками — по одной на каждое найденное. Пустой результат означает,
// что каждое названное в тексте число программы совпало с тем, что уйдёт в поля записи.
//
// Пустая колонка книги свою проверку выключает: колонок не было вовсе, когда писались первые
// страницы, и требовать по ним сверки задним числом нельзя.
func CheckProgramFacts(markup string, facts ProgramFacts, modules []Module) []string {
	markup = factBlockEndRE.ReplaceAllString(markup, "$0 "+factBlockMark+" ")
	text := plainText(factSalaryTableRE.ReplaceAllStringFunc(markup, func(table string) string {
		if factTheadRE.MatchString(table) || multiColumnTable(table) {
			return " "
		}
		return table
	}))

	var issues []string
	issues = append(issues, checkHours(text, facts.Hours, modules)...)
	issues = append(issues, checkPrice(text, facts.Price)...)
	issues = append(issues, checkTerm(text, facts.Duration)...)
	return issues
}

// checkHours: в тексте допустимы объём программы и часы модулей — больше часам взяться
// неоткуда. Чужое число здесь и есть рассинхрон: страница обещает один объём, поле показывает
// другой.
func checkHours(text, bookHours string, modules []Module) []string {
	want := factNumber(bookHours)
	if want == 0 {
		return nil
	}
	allowed := map[int]bool{want: true}
	for _, module := range modules {
		if hours, err := strconv.Atoi(module.Hours); err == nil {
			allowed[hours] = true
		}
	}
	var issues []string
	seen := map[int]bool{}
	for _, found := range factHoursRE.FindAllStringSubmatchIndex(text, -1) {
		value := factDigits(text[found[2]:found[3]])
		if value == 0 || allowed[value] || seen[value] {
			continue
		}
		if factPaceRE.MatchString(text[found[1]:]) || factSpanRE.MatchString(text[:found[0]]) {
			continue
		}
		seen[value] = true
		issues = append(issues, fmt.Sprintf("в тексте %d часов, а объём программы в книге — %d (%s)",
			value, want, factQuote(text, found[0])))
	}
	return issues
}

// checkPrice: цена на странице одна — та, что уходит в поле записи. Доход по разрядам в
// сверку не идёт, его таблица снята выше.
func checkPrice(text, bookPrice string) []string {
	want := factNumber(bookPrice)
	if want == 0 {
		return nil
	}
	var issues []string
	seen := map[int]bool{}
	for _, found := range factMoneyRE.FindAllStringSubmatchIndex(text, -1) {
		value := factDigits(text[found[2]:found[3]])
		// Сумма за период — заработок: цену курса помесячно не называют. «Коммунальные
		// службы предлагают от 65 000 до 120 000 рублей в месяц» 29.09.2026 остановило
		// публикацию страницы 32 — слов о зарплате в предложении нет.
		perPeriod := factPaceRE.MatchString(text[found[1]:])
		if value == 0 || value == want || seen[value] || perPeriod || aboutEarnings(text, found[0]) {
			continue
		}
		seen[value] = true
		issues = append(issues, fmt.Sprintf("в тексте цена %d, а в книге — %d (%s)",
			value, want, factQuote(text, found[0])))
	}
	return issues
}

// factEarningsRE — слова о заработке. Сумма после них — доход, а не цена курса: «работодатели
// предлагают зарплату от 200 000 рублей» 28.09.2026 остановило публикацию страницы 16.
var factEarningsRE = regexp.MustCompile(`(?i)(зарплат|заработ|доход|оклад)[а-яё]*`)

// factBlockEndRE и factBlockMark отмечают конец абзаца, строки списка и заголовка: текст
// сверки собирается без тегов, и без метки заголовок «Уровень дохода» оказывался в одном
// предложении с ценой курса из следующего абзаца.
var factBlockEndRE = regexp.MustCompile(`(?i)</(?:p|li|h[1-6]|td|th)>`)

const factBlockMark = "¶"

// factEarningsWindowRunes bounds the earnings lookback; the sentence boundary usually comes first.
const factEarningsWindowRunes = 160

// aboutEarnings reports that the money amount at offset follows earnings wording in the same
// sentence.
func aboutEarnings(text string, offset int) bool {
	window := text[:offset]
	if runes := []rune(window); len(runes) > factEarningsWindowRunes {
		window = string(runes[len(runes)-factEarningsWindowRunes:])
	}
	if dot := strings.LastIndexAny(window, ".!?"+factBlockMark); dot >= 0 {
		_, size := utf8.DecodeRuneInString(window[dot:])
		window = window[dot+size:]
	}
	return factEarningsRE.MatchString(window)
}

// checkTerm: срок обучения называется формой «от N недель» — той же, что стоит в колонке
// книги и в плашке темы.
func checkTerm(text, bookDuration string) []string {
	wantValue, wantUnit := factTerm(bookDuration)
	if wantValue == 0 {
		return nil
	}
	var issues []string
	seen := map[string]bool{}
	for _, found := range factTermRE.FindAllStringSubmatch(text, -1) {
		value, err := strconv.Atoi(found[1])
		if err != nil {
			continue
		}
		if value == wantValue && strings.ToLower(found[2]) == wantUnit {
			continue
		}
		key := found[0]
		if seen[key] {
			continue
		}
		seen[key] = true
		issues = append(issues, fmt.Sprintf("в тексте срок «%s», а в книге — «%s»",
			strings.TrimSpace(found[0]), strings.TrimSpace(bookDuration)))
	}
	return issues
}

// factNumber — первое число значения книги: «150 часов» → 150, «от 5 000 ₽» → 5000.
func factNumber(value string) int {
	found := regexp.MustCompile(`\d[\d\s\x{00a0}]*`).FindString(value)
	return factDigits(found)
}

// factTerm разбирает срок книги на число и единицу: «от 2 недель» → 2, «недел».
func factTerm(value string) (int, string) {
	found := factTermRE.FindStringSubmatch(strings.ToLower(value))
	if len(found) < 3 {
		return 0, ""
	}
	number, err := strconv.Atoi(found[1])
	if err != nil {
		return 0, ""
	}
	return number, found[2]
}

// factDigits складывает число из строки с разделителями тысяч.
func factDigits(value string) int {
	var digits strings.Builder
	for _, symbol := range value {
		if symbol >= '0' && symbol <= '9' {
			digits.WriteRune(symbol)
		}
	}
	number, err := strconv.Atoi(digits.String())
	if err != nil {
		return 0
	}
	return number
}

// factQuote — кусок текста вокруг находки: человек правит страницу руками, и найти в ней
// место по одному числу тяжело.
func factQuote(text string, at int) string {
	runes := []rune(text)
	from := len([]rune(text[:at]))
	start := from - 40
	if start < 0 {
		start = 0
	}
	end := from + 40
	if end > len(runes) {
		end = len(runes)
	}
	return "…" + strings.TrimSpace(string(runes[start:end])) + "…"
}
