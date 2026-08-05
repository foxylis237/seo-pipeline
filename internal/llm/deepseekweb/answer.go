package deepseekweb

import (
	"regexp"
	"strings"
)

// answerSources — то, что удалось прочитать со страницы для одного ответа.
type answerSources struct {
	// Clipboard — исходный текст из кнопки «Копировать»: единственный источник, который
	// отдаёт разметку модели как есть.
	Clipboard string
	// CodeBlock — содержимое блока кода, если ответ обёрнут в него.
	CodeBlock string
	// Rendered — видимый текст узла ответа. Markdown в нём уже потерян.
	Rendered string
	// DOMHasTable и DOMHasHeadings описывают то, что реально отрисовано на странице.
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

// containsMarkdownHeading reports a Markdown heading line in the text.
func containsMarkdownHeading(text string) bool { return markdownHeadingRE.MatchString(text) }

// containsMarkdownTable reports a Markdown table row in the text.
func containsMarkdownTable(text string) bool { return markdownTableRE.MatchString(text) }

// containsHTMLTags reports HTML markup in the text.
func containsHTMLTags(text string) bool { return htmlTagRE.MatchString(text) }

// selectAnswer picks the most faithful available source of the answer.
//
// Порядок один и тот же для всех стадий: буфер обмена сохраняет разметку модели дословно,
// блок кода — почти дословно, видимый текст теряет заголовки и таблицы, поэтому он последний.
func selectAnswer(sources answerSources) (string, string) {
	if text := strings.TrimSpace(sources.Clipboard); text != "" {
		return text, sourceClipboard
	}
	if text := strings.TrimSpace(sources.CodeBlock); text != "" {
		return text, sourceCodeBlock
	}
	return strings.TrimSpace(sources.Rendered), sourceRendered
}

// detectFormatLoss returns the formatting the page shows but the extracted text lost.
//
// Проверяется только видимый текст: у буфера обмена и блока кода разметка сохраняется по
// построению. Пустой результат означает, что терять было нечего.
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
