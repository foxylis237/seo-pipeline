package deepseekweb

import (
	"regexp"
	"strings"
)

// answerSources — то, что удалось прочитать со страницы для одного ответа.
type answerSources struct {
	// Clipboard — текст из кнопки «Копировать»: единственный источник с разметкой модели как есть.
	Clipboard string
	CodeBlock string
	// Rendered — видимый текст узла ответа; Markdown в нём уже потерян.
	Rendered string
	// DOMHasTable и DOMHasHeadings — что реально отрисовано на странице.
	DOMHasTable    bool
	DOMHasHeadings bool
}

// Источники ответа в порядке убывания точности.
const (
	sourceClipboard = "clipboard"
	sourceCodeBlock = "code_block"
	sourceRendered  = "rendered_text"
)

var (
	markdownHeadingRE = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s+\S`)
	markdownTableRE   = regexp.MustCompile(`(?m)^\s*\|.*\|\s*$`)
	htmlTagRE         = regexp.MustCompile(`(?is)<(?:h[1-6]|p|table|ul|ol|li|div|span|strong|em)\b[^>]*>`)
)

func containsMarkdownHeading(text string) bool { return markdownHeadingRE.MatchString(text) }

func containsMarkdownTable(text string) bool { return markdownTableRE.MatchString(text) }

func containsHTMLTags(text string) bool { return htmlTagRE.MatchString(text) }

// selectAnswer picks the most faithful available source of the answer.
func selectAnswer(sources answerSources) (string, string) {
	if text := strings.TrimSpace(sources.Clipboard); text != "" {
		return text, sourceClipboard
	}
	if text := strings.TrimSpace(sources.CodeBlock); text != "" {
		return text, sourceCodeBlock
	}
	return strings.TrimSpace(sources.Rendered), sourceRendered
}

// detectFormatLoss returns the formatting the page shows but the visible text lost.
func detectFormatLoss(sources answerSources, text, source string) []string {
	if source != sourceRendered {
		return nil
	}
	var lost []string
	if sources.DOMHasHeadings && !containsMarkdownHeading(text) && !containsHTMLTags(text) {
		lost = append(lost, "headings")
	}
	if sources.DOMHasTable && !containsMarkdownTable(text) && !containsHTMLTags(text) {
		lost = append(lost, "markdown_table")
	}
	return lost
}

// acceptClipboardValue reports whether the clipboard holds a new answer rather than the
// marker written before the click or a leftover from a previous copy.
func acceptClipboardValue(marker, value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == strings.TrimSpace(marker) {
		return "", false
	}
	return trimmed, true
}
