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
