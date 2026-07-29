package generation

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	htmlTagRE     = regexp.MustCompile(`(?is)<[a-z][^>]*>`)
	htmlContentRE = regexp.MustCompile(`(?is)<(?:h[1-6]|p)\b[^>]*>`)
)

func normalizeAndValidateHTML(value string) (string, error) {
	html := strings.TrimSpace(value)
	if strings.HasPrefix(html, "```") {
		firstLineEnd := strings.IndexByte(html, '\n')
		if firstLineEnd < 0 || !strings.HasSuffix(html, "```") {
			return "", fmt.Errorf("незавершённая Markdown-обёртка HTML")
		}
		opening := strings.TrimSpace(html[:firstLineEnd])
		if opening != "```" && !strings.EqualFold(opening, "```html") {
			return "", fmt.Errorf("неподдерживаемая Markdown-обёртка %q", opening)
		}
		html = strings.TrimSpace(strings.TrimSuffix(html[firstLineEnd+1:], "```"))
	}
	if html == "" {
		return "", fmt.Errorf("HTML-ответ пуст")
	}
	if strings.Contains(strings.ToLower(html), "```html") || strings.Contains(html, "```") {
		return "", fmt.Errorf("HTML содержит Markdown-обёртку")
	}
	if !strings.HasPrefix(html, "<") {
		return "", fmt.Errorf("перед HTML обнаружен поясняющий текст")
	}
	if !htmlTagRE.MatchString(html) {
		return "", fmt.Errorf("ответ не содержит HTML-тегов")
	}
	if !htmlContentRE.MatchString(html) {
		return "", fmt.Errorf("HTML не содержит заголовка или абзаца")
	}
	return html, nil
}
