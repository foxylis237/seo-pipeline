package generation

import (
	"regexp"
	"strings"
)

// Запись заголовка в тексте статьи — договорённость между стадиями, а не оформление: стадия
// html расставляет по ней теги, а разбор текста ищет заголовки строкой. Модель держит форму
// нетвёрдо: по сохранённым статьям pprof_1 видно все три сразу — «H2 - Название» у большинства,
// «H2:» у нескольких и Markdown «## Название» с жирным начертанием у одной. Промптом это не
// удержалось, поэтому запись приводится к одному виду кодом, до записи артефакта.
var (
	headingHashRE  = regexp.MustCompile(`^\s*(#{1,4})\s+(.+?)\s*$`)
	headingLabelRE = regexp.MustCompile(`^\s*\*{0,2}[Hh]([1-4])\*{0,2}\s*[-:\x{2013}\x{2014}]\s*(.+?)\s*$`)
)

// NormalizeHeadings приводит запись заголовков к виду «H2 - Название».
//
// Строки, которые заголовком не являются, остаются как есть: признак строгий — строка целиком
// состоит из метки уровня и названия.
func NormalizeHeadings(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = normalizeHeadingLine(line)
	}
	return strings.Join(lines, "\n")
}

func normalizeHeadingLine(line string) string {
	if match := headingHashRE.FindStringSubmatch(strings.TrimSuffix(line, "\r")); match != nil {
		// Метка уровня внутри Markdown-заголовка: «## H2 - Название». Модель пишет так,
		// смешивая обе привычные ей формы, и без разбора вложенной метки к строке
		// приписывался второй префикс — «H2 - H2 - Название». Уровень берётся из метки, а не
		// из числа решёток: названный уровень точнее посчитанного.
		if inner := headingLabelRE.FindStringSubmatch(match[2]); inner != nil {
			return heading(int(inner[1][0]-'0'), inner[2])
		}
		return heading(len(match[1]), match[2])
	}
	if match := headingLabelRE.FindStringSubmatch(strings.TrimSuffix(line, "\r")); match != nil {
		return heading(int(match[1][0]-'0'), match[2])
	}
	return line
}

// heading собирает канонический заголовок и снимает с названия жирное начертание: заголовок
// выделяют тегом, а не звёздочками.
func heading(level int, title string) string {
	title = strings.TrimSpace(strings.Trim(strings.TrimSpace(title), "*"))
	return "H" + string(rune('0'+level)) + " - " + title
}

// CountHeadings считает заголовки разделов в тексте статьи.
//
// Считаются строки в каноническом виде «H2 - Название», поэтому вызывать имеет смысл после
// NormalizeHeadings. Нужно это сверке двух версий одного текста: стадия, которая возвращает
// статью целиком, обрывается неотличимо от готового ответа, и потерянные разделы — один из
// двух доступных признаков обрыва (второй — длина).
func CountHeadings(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if headingLabelRE.MatchString(strings.TrimSuffix(line, "\r")) {
			count++
		}
	}
	return count
}

var structureBulletRE = regexp.MustCompile(`^\s*[-*•]\s+`)

// HeadingsOnly оставляет от текста одни заголовки в каноническом виде «H2 - Название».
//
// Ответ чата структуры — черновик страницы: под заголовками модель пишет лид, строки списков и
// абзацы. Выгружаемому промпту нужна только композиция, и всё, что не заголовок, снимается.
func HeadingsOnly(text string) string {
	var headings []string
	for _, line := range strings.Split(text, "\n") {
		// «- **H2:** Название»: чат структуры пишет заголовки и строкой списка с жирной меткой
		// (страница 35 obuch_2).
		line = strings.ReplaceAll(structureBulletRE.ReplaceAllString(strings.TrimSuffix(line, "\r"), ""), "**", "")
		if headingLabelRE.MatchString(line) || headingHashRE.MatchString(line) {
			headings = append(headings, normalizeHeadingLine(line))
		}
	}
	return strings.Join(headings, "\n")
}
