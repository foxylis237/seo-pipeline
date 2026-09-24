package obuch2

import (
	"regexp"
	"strconv"
	"strings"
)

// Программа обучения на странице услуги живёт дважды: списком в теле записи и аккордеоном,
// который площадка рисует над текстом из полей записи. Аккордеон и есть то, за чем читатель
// приходит, — но собирается он не из тела, а из репитера prog_moduli_*, и до сих пор его
// заполнял человек руками в админке.
//
// Здесь модули разбираются из уже написанной страницы. Источник один — тело записи, и это
// то же решение, по которому FAQ у pprof_2 берётся из текста, а не у модели вторым запросом:
// второй набор отличался бы от опубликованного, а заметить это можно только глазами на самой
// странице. Часы стоят в жирном зачине строки именно затем, чтобы их было откуда взять.

var (
	// Заголовок раздела программы. Слово «программа» в нём есть у всех просмотренных живых
	// страниц площадки, а формулировку целиком задаёт скелет задачи и правит чат 1 под топ
	// выдачи — поэтому узнаётся корень, а не строка.
	programHeadingRE = regexp.MustCompile(`(?s)<h2[^>]*>[^<]*[Пп]рограмм[а-яё]*.*?</h2>`)
	programNextH2RE  = regexp.MustCompile(`(?s)<h2[^>]*>`)
	programListRE    = regexp.MustCompile(`(?s)<(?:ol|ul)[^>]*>(.*?)</(?:ol|ul)>`)
	programItemRE    = regexp.MustCompile(`(?s)<li[^>]*>\s*<strong[^>]*>(.*?)</strong>\s*(.*?)\s*</li>`)

	// Зачин строки: «Модуль 3. Электролечение — 18 часов.» Номер модуля отбрасывается —
	// порядок строк надёжнее напечатанной цифры, а пропуск в нумерации читался бы как
	// потерянный модуль. Часы необязательны: книга без объёма программы запрещает их
	// выдумывать, и тогда строка остаётся одной темой.
	moduleNumberRE = regexp.MustCompile(`^\s*Модул[а-яё]*\s*\d*\s*[.:]?\s*`)
	moduleHoursRE  = regexp.MustCompile(`\s*[—–-]\s*(\d+)\s*(?:ак\.?\s*)?час[а-яё]*\.?\s*$`)
	tagRE          = regexp.MustCompile(`(?s)<[^>]+>`)
)

// Module — модуль программы так, как его ждёт репитер площадки.
type Module struct {
	// Topic — prog_moduli_N_tema: название модуля без служебного «Модуль N.» и без часов.
	Topic string
	// Hours — prog_moduli_N_chasy. Пустые означают «часов у модуля нет»: поле не отправляется.
	Hours string
	// Text — prog_moduli_N_opisanie: раскрытие модуля.
	Text string
}

// ParseModules разбирает модули программы из готовой разметки страницы.
//
// Разбор идёт по первому списку раздела программы, а не по всей странице: списков на странице
// несколько, и строка «Свидетельство о профессии…» из раздела документов с виду неотличима от
// модуля без часов.
//
// Пустой результат — законный исход, а не отказ: раздел мог не найтись, список оказаться
// другой формы, а страница всё равно уже написана и оплачена. Что делать с пустым результатом,
// решает публикация.
func ParseModules(markup string) []Module {
	heading := programHeadingRE.FindStringIndex(markup)
	if heading == nil {
		return nil
	}
	start := heading[1]
	end := len(markup)
	if next := programNextH2RE.FindStringIndex(markup[start:]); next != nil {
		end = start + next[0]
	}
	list := programListRE.FindStringSubmatch(markup[start:end])
	if len(list) < 2 {
		return nil
	}
	var modules []Module
	for _, item := range programItemRE.FindAllStringSubmatch(list[1], -1) {
		topic, hours := splitModuleLead(plainText(item[1]))
		text := plainText(item[2])
		if topic == "" || text == "" {
			continue
		}
		modules = append(modules, Module{Topic: topic, Hours: hours, Text: text})
	}
	return modules
}

// TotalHours — сумма часов модулей. Ноль означает, что часов нет ни у одного: у страницы без
// объёма программы в книге их не бывает по построению.
//
// Нужна публикации, чтобы сверить программу с колонкой книги: те же часы человек ставит в
// поле записи, и разойтись им нельзя.
func TotalHours(modules []Module) int {
	total := 0
	for _, module := range modules {
		hours, err := strconv.Atoi(module.Hours)
		if err != nil {
			continue
		}
		total += hours
	}
	return total
}

// splitModuleLead разбирает зачин строки на тему и часы.
func splitModuleLead(lead string) (topic, hours string) {
	lead = strings.TrimSpace(moduleNumberRE.ReplaceAllString(lead, ""))
	if found := moduleHoursRE.FindStringSubmatch(lead); len(found) > 1 {
		hours = found[1]
		lead = moduleHoursRE.ReplaceAllString(lead, "")
	}
	return strings.TrimRight(strings.TrimSpace(lead), ".;,"), hours
}

// plainText снимает теги и схлопывает пробелы: в поле записи уходит текст, а не разметка —
// репитер выводит его сам, своей вёрсткой.
func plainText(value string) string {
	return strings.Join(strings.Fields(tagRE.ReplaceAllString(value, " ")), " ")
}

// ProgramPreset — служебные значения блока программы, свои у каждого типа записи.
//
// Это не свободный текст: document, attestation и format у площадки выбираются из списка, и
// чужое значение отрисуется пустым. Heading тоже служебный — он подписывает аккордеон, и у
// повышения квалификации он другой.
type ProgramPreset struct {
	Document    string
	Attestation string
	// Format — набор флажков: значение уходит списком, а сериализует его WordPress сам.
	// Готовую строку отправлять нельзя — она сериализуется повторно и ложится строкой
	// вместо массива (измерено первой публикацией, запись 19540). Берётся «дистанционно»:
	// у части живых страниц отмечено ещё и очно, но обещать очный формат за центр нельзя.
	Format  []string
	Heading string
}

// ProgramPresetOf отдаёт значения по типу записи. Второй результат — известен ли тип.
//
// Таблица закрытая и снята с живых страниц площадки 21.09.2026: по 12 страниц на тип,
// значения единогласные (мусор одной тестовой записи не в счёт). Незамеренный тип не
// подставляет ничего, и поля тогда не уходят вовсе: угаданный селект отрисуется пустым, а в
// записи его уже не отличить от выбранного человеком.
func ProgramPresetOf(postType string) (ProgramPreset, bool) {
	switch strings.TrimSpace(strings.ToLower(postType)) {
	case "rabprof":
		return ProgramPreset{
			Document:    "svidetelstvo",
			Attestation: "exam",
			Format:      []string{"dist"},
			Heading:     "Программа обучения",
		}, true
	case "povyshenie":
		return ProgramPreset{
			Document:    "udostoverenie",
			Attestation: "test",
			Format:      []string{"dist"},
			Heading:     "Программа повышения квалификации",
		}, true
	default:
		return ProgramPreset{}, false
	}
}
