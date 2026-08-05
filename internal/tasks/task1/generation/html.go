package generation

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	htmlTagRE     = regexp.MustCompile(`(?is)<[a-z][^>]*>`)
	htmlContentRE = regexp.MustCompile(`(?is)<(?:h[1-6]|p)\b[^>]*>`)
	htmlFenceRE   = regexp.MustCompile("(?s)```([a-zA-Z]*)\r?\n(.*?)```")
)

// normalizeAndValidateHTML извлекает разметку из ответа модели и проверяет её.
//
// Чат-модель предваряет разметку фразой вроде «Вот готовая HTML-версия статьи:» и часто
// кладёт её в Markdown-блок. И то и другое снимается здесь, а не в промпте. Всё, что
// осталось после очистки, проходит прежние проверки: пустой ответ, отсутствие тегов и
// отсутствие заголовка или абзаца остаются ошибками — очистка не должна превращать
// отказ модели («не могу выполнить запрос») в пустую валидную статью.
func normalizeAndValidateHTML(value string) (string, error) {
	html := strings.TrimSpace(value)
	if html == "" {
		return "", fmt.Errorf("HTML-ответ пуст")
	}
	if strings.Contains(html, "```") {
		block := htmlFenceRE.FindStringSubmatch(html)
		if block == nil {
			return "", fmt.Errorf("незавершённая Markdown-обёртка HTML")
		}
		if language := block[1]; language != "" && !strings.EqualFold(language, "html") {
			return "", fmt.Errorf("неподдерживаемая Markdown-обёртка %q", "```"+language)
		}
		html = strings.TrimSpace(block[2])
	}
	if !strings.HasPrefix(html, "<") {
		opening := htmlTagRE.FindStringIndex(html)
		if opening == nil {
			return "", fmt.Errorf("ответ не содержит HTML-тегов")
		}
		html = strings.TrimSpace(html[opening[0]:])
	}
	if html == "" {
		return "", fmt.Errorf("HTML-ответ пуст")
	}
	if strings.Contains(html, "```") {
		return "", fmt.Errorf("HTML содержит Markdown-обёртку")
	}
	if !htmlTagRE.MatchString(html) {
		return "", fmt.Errorf("ответ не содержит HTML-тегов")
	}
	if !htmlContentRE.MatchString(html) {
		return "", fmt.Errorf("HTML не содержит заголовка или абзаца")
	}
	return html, nil
}
