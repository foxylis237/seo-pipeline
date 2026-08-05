package deepseekweb

import (
	"strings"
	"testing"
)

const markdownAnswer = `## Проверка

| Колонка 1 | Колонка 2 |
|-----------|-----------|
| Данные 1  | Данные 2  |

- Пункт первый
- Пункт второй`

// renderedAnswer — то, что отдаёт innerText для того же ответа: маркеры разметки потеряны.
const renderedAnswer = "Проверка\nКолонка 1\tКолонка 2\nДанные 1\tДанные 2\n\nПункт первый\n\nПункт второй"

func TestSelectAnswerKeepsMarkdownFromClipboard(t *testing.T) {
	text, source := selectAnswer(answerSources{
		Clipboard: markdownAnswer, Rendered: renderedAnswer,
		DOMHasTable: true, DOMHasHeadings: true,
	})
	if source != sourceClipboard {
		t.Fatalf("source = %q, want %q", source, sourceClipboard)
	}
	if !containsMarkdownHeading(text) || !containsMarkdownTable(text) {
		t.Fatalf("разметка потеряна: %q", text)
	}
	if !strings.Contains(text, "- Пункт первый") {
		t.Fatalf("список потерян: %q", text)
	}
}

func TestSelectAnswerKeepsHTMLFromCodeBlock(t *testing.T) {
	html := "<h1>Тема</h1>\n<p>Текст</p>"
	text, source := selectAnswer(answerSources{CodeBlock: html, Rendered: "Тема\nТекст"})
	if source != sourceCodeBlock || text != html {
		t.Fatalf("source = %q, text = %q", source, text)
	}
}

func TestSelectAnswerUsesRenderedTextOnlyAsLastResort(t *testing.T) {
	text, source := selectAnswer(answerSources{Rendered: renderedAnswer})
	if source != sourceRendered || text != renderedAnswer {
		t.Fatalf("source = %q, text = %q", source, text)
	}
	// Буфер важнее блока кода, блок кода важнее видимого текста.
	if _, source := selectAnswer(answerSources{Clipboard: "из буфера", CodeBlock: "из блока", Rendered: "видимый"}); source != sourceClipboard {
		t.Fatalf("при доступном буфере выбран %q", source)
	}
	if _, source := selectAnswer(answerSources{CodeBlock: "из блока", Rendered: "видимый"}); source != sourceCodeBlock {
		t.Fatalf("при доступном блоке кода выбран %q", source)
	}
	// Пробельный буфер источником не считается.
	if _, source := selectAnswer(answerSources{Clipboard: "   \n ", Rendered: "видимый"}); source != sourceRendered {
		t.Fatalf("пустой буфер принят как источник: %q", source)
	}
}

func TestAcceptClipboardValueRejectsEmptyAndStale(t *testing.T) {
	if _, ok := acceptClipboardValue(clipboardMarker, ""); ok {
		t.Fatal("пустой буфер принят")
	}
	if _, ok := acceptClipboardValue(clipboardMarker, "   "); ok {
		t.Fatal("буфер из пробелов принят")
	}
	if _, ok := acceptClipboardValue(clipboardMarker, clipboardMarker); ok {
		t.Fatal("прежнее значение буфера принято как ответ")
	}
	text, ok := acceptClipboardValue(clipboardMarker, "  ## Ответ  ")
	if !ok || text != "## Ответ" {
		t.Fatalf("text = %q, ok = %t", text, ok)
	}
}

func TestDetectFormatLossOnlyForRenderedText(t *testing.T) {
	sources := answerSources{Rendered: renderedAnswer, DOMHasTable: true, DOMHasHeadings: true}
	lost := detectFormatLoss(sources, renderedAnswer, sourceRendered)
	if len(lost) != 2 || lost[0] != "headings" || lost[1] != "markdown_table" {
		t.Fatalf("потери = %v", lost)
	}
	// У буфера и блока кода разметка сохраняется по построению.
	if lost := detectFormatLoss(sources, markdownAnswer, sourceClipboard); lost != nil {
		t.Fatalf("для буфера обнаружены потери: %v", lost)
	}
	// Терять нечего: на странице не было ни таблиц, ни заголовков.
	if lost := detectFormatLoss(answerSources{}, "просто текст", sourceRendered); lost != nil {
		t.Fatalf("потери на пустой странице: %v", lost)
	}
	// HTML-ответ в видимом тексте разметку сохранил.
	html := answerSources{DOMHasHeadings: true, DOMHasTable: true}
	if lost := detectFormatLoss(html, "<h1>Тема</h1><table><tr><td>1</td></tr></table>", sourceRendered); lost != nil {
		t.Fatalf("потери у HTML-ответа: %v", lost)
	}
}

func TestFormatFeatureDetection(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		heading  bool
		table    bool
		htmlTags bool
	}{
		{name: "markdown", text: markdownAnswer, heading: true, table: true},
		{name: "видимый текст", text: renderedAnswer},
		{name: "html", text: "<h2>Тема</h2><p>Текст</p>", htmlTags: true},
		{name: "решётка внутри строки", text: "цена #1 на рынке"},
		{name: "таблица с отступом", text: "  | A | B |\n  |---|---|", table: true},
		{name: "заголовок третьего уровня", text: "### Подзаголовок", heading: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := containsMarkdownHeading(test.text); got != test.heading {
				t.Errorf("containsMarkdownHeading = %t, want %t", got, test.heading)
			}
			if got := containsMarkdownTable(test.text); got != test.table {
				t.Errorf("containsMarkdownTable = %t, want %t", got, test.table)
			}
			if got := containsHTMLTags(test.text); got != test.htmlTags {
				t.Errorf("containsHTMLTags = %t, want %t", got, test.htmlTags)
			}
		})
	}
}
