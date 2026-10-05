package google

import (
	"html"
	"regexp"
	"strings"
)

// Промпт уходит в Docs текстом, и заголовки разделов, списки и разделители без разметки
// вставляются простыми строками. Задача, которой нужен документ с оформлением, помечает
// заголовки разделов строкой «# » / «## », и тогда вместе с текстом в буфер кладётся HTML: Docs
// при вставке берёт из него стили заголовков, маркированные и нумерованные списки. Промпт без
// такой строки уходит как раньше — одним текстом: у остальных задач документ не меняется.
var (
	richHeadingRE  = regexp.MustCompile(`^(#{1,2})\s+(.+?)\s*$`)
	richBulletRE   = regexp.MustCompile(`^\s*[*•]\s+(.+?)\s*$`)
	richNumberedRE = regexp.MustCompile(`^\s*\d+\.\s+(.+?)\s*$`)
	richRuleRE     = regexp.MustCompile(`^\s*_{5,}\s*$`)
)

// promptHTML собирает HTML для вставки в Docs. ok=false означает, что промпт размечен не был и
// вставляется текстом.
func promptHTML(body string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	marked := false
	for _, line := range lines {
		if richHeadingRE.MatchString(line) {
			marked = true
			break
		}
	}
	if !marked {
		return "", false
	}

	var out strings.Builder
	list := ""
	closeList := func() {
		if list != "" {
			out.WriteString("</" + list + ">")
			list = ""
		}
	}
	openList := func(tag string) {
		if list != tag {
			closeList()
			out.WriteString("<" + tag + ">")
			list = tag
		}
	}
	for _, line := range lines {
		switch {
		case strings.TrimSpace(line) == "":
			closeList()
		case richHeadingRE.MatchString(line):
			closeList()
			match := richHeadingRE.FindStringSubmatch(line)
			tag := "h" + string(rune('0'+len(match[1])))
			out.WriteString("<" + tag + ">" + html.EscapeString(match[2]) + "</" + tag + ">")
		case richRuleRE.MatchString(line):
			closeList()
			out.WriteString("<hr>")
		case richBulletRE.MatchString(line):
			openList("ul")
			out.WriteString("<li>" + html.EscapeString(richBulletRE.FindStringSubmatch(line)[1]) + "</li>")
		case richNumberedRE.MatchString(line):
			openList("ol")
			out.WriteString("<li>" + html.EscapeString(richNumberedRE.FindStringSubmatch(line)[1]) + "</li>")
		default:
			closeList()
			text := html.EscapeString(strings.TrimRight(line, " "))
			out.WriteString("<p>" + strings.ReplaceAll(text, "\t", " — ") + "</p>")
		}
	}
	closeList()
	return out.String(), true
}
