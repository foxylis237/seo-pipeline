package generation

import (
	"fmt"
	"regexp"
	"strings"
)

// LinkInsert — одна ссылка перелинковки, вписанная моделью: раздел, куда она встаёт, и
// предложение с готовым тегом ссылки.
type LinkInsert struct {
	URL      string
	Heading  string
	Sentence string
}

// PlacedInternalLinks считает адреса перелинковки, которые в разметке уже есть. Повтор одного
// адреса в списке считается один раз — так же, как его считает MissingInternalLinks.
func PlacedInternalLinks(markup, links string) int {
	unique := make(map[string]struct{})
	for _, url := range LinkURLs(links) {
		unique[normalizeLinkURL(url)] = struct{}{}
	}
	return len(unique) - len(MissingInternalLinks(markup, links))
}

// RepairLinksPrompt просит у модели недостающие ссылки короткими строками, а не страницу заново.
//
// Сообщение уходит продолжением чата разметки: страницу модель уже видела в истории, и второй
// раз её не передают. Вернуть всю разметку просить нельзя — у длинной статьи такой ответ
// упирается в предел длины сообщения, как и первый. Ответ — по строке на ссылку
// «адрес ;; заголовок раздела ;; предложение», место и вставку берёт на себя код
// (ParseLinkInserts, InsertLinkSentences).
func RepairLinksPrompt(markup, links string, missing []string) string {
	names := linkNames(links)
	var list strings.Builder
	for _, url := range missing {
		if name := names[normalizeLinkURL(url)]; name != "" {
			fmt.Fprintf(&list, "- %s — %s\n", name, url)
			continue
		}
		fmt.Fprintf(&list, "- %s\n", url)
	}
	var free strings.Builder
	if headings := FreeLinkHeadings(markup, links); len(headings) > 0 {
		free.WriteString("\nРазделы, где ссылок ещё нет:\n")
		for _, heading := range headings {
			fmt.Fprintf(&free, "- %s\n", heading)
		}
	}
	return fmt.Sprintf(`В разметке выше не хватает ссылок перелинковки:
%s
Впиши каждую в текст одним коротким предложением. Предложение встанет в конец первого абзаца раздела, который ты назовёшь, и должно продолжать его мысль — без «подробнее о программе», «узнайте больше» и призывов. Анкор — название программы дословно. Каждой ссылке — свой раздел.
%s
Страницу заново не присылай. В ответе только строки, по одной на ссылку, без нумерации и пояснений:
адрес ;; заголовок раздела как в разметке ;; предложение с <a href="адрес">названием программы</a>`,
		list.String(), free.String())
}

// ParseLinkInserts разбирает ответ на RepairLinksPrompt.
//
// Принимаются только адреса, о которых спрашивали, и каждый один раз. Строка без тега ссылки
// не отбрасывается, если в предложении есть название программы: его код оборачивает сам. Всё
// остальное — нумерация, пояснения, обёртка в код — строкой ответа не считается.
func ParseLinkInserts(answer, links string, missing []string) []LinkInsert {
	asked := make(map[string]string, len(missing))
	for _, url := range missing {
		asked[normalizeLinkURL(url)] = url
	}
	names := linkNames(links)
	seen := make(map[string]struct{}, len(missing))
	var inserts []LinkInsert
	for line := range strings.SplitSeq(strings.ReplaceAll(answer, "\r\n", "\n"), "\n") {
		parts := strings.Split(strings.ReplaceAll(line, "`", ""), ";;")
		if len(parts) < 3 {
			continue
		}
		key := normalizeLinkURL(strings.TrimRight(linkURLRE.FindString(parts[0]), ".,;)"))
		url, ok := asked[key]
		if _, done := seen[key]; !ok || done {
			continue
		}
		heading := strings.Trim(strings.TrimSpace(insertHeadingLabelRE.ReplaceAllString(stripTags(parts[1]), "")), `«»"*' `)
		sentence := anchoredSentence(paragraphTagRE.ReplaceAllString(strings.Join(parts[2:], ";;"), ""), url, names[key])
		if heading == "" || sentence == "" {
			continue
		}
		seen[key] = struct{}{}
		inserts = append(inserts, LinkInsert{URL: url, Heading: heading, Sentence: sentence})
	}
	return inserts
}

// anchoredSentence возвращает предложение, в котором есть ссылка на адрес, или пустую строку.
func anchoredSentence(sentence, url, name string) string {
	sentence = strings.TrimSpace(sentence)
	for _, match := range hrefRE.FindAllStringSubmatch(sentence, -1) {
		if normalizeLinkURL(match[1]) == normalizeLinkURL(url) {
			return sentence
		}
	}
	if name == "" || !strings.Contains(sentence, name) {
		return ""
	}
	return strings.Replace(sentence, name, fmt.Sprintf(`<a href="%s">%s</a>`, url, name), 1)
}

// InsertLinkSentences вписывает предложения моделей в названные разделы: в конец первого абзаца.
//
// Абзац, который заканчивается двоеточием, подводит к списку, и приписанное к нему предложение
// разорвало бы эту связку; такому разделу, как и разделу без абзацев, предложение достаётся
// отдельным абзацем сразу под заголовком. Раздел ищется по тексту заголовка без тегов, регистра
// и пунктуации, а если дословно не нашёлся — по вхождению: модель сокращает длинные заголовки.
// Возвращает разметку и вставки, которым места не нашлось.
func InsertLinkSentences(markup string, inserts []LinkInsert) (string, []LinkInsert) {
	var skipped []LinkInsert
	for _, insert := range inserts {
		if PlacedInternalLinks(markup, insert.URL) > 0 {
			continue
		}
		section, ok := findSection(markup, insert.Heading)
		if !ok {
			skipped = append(skipped, insert)
			continue
		}
		block := markup[section.start:section.end]
		if loc := paragraphCloseRE.FindStringIndex(block); loc != nil &&
			!strings.HasSuffix(strings.TrimSpace(stripTags(block[:loc[0]])), ":") {
			at := section.start + loc[0]
			markup = markup[:at] + " " + insert.Sentence + markup[at:]
			continue
		}
		markup = markup[:section.start] + "\n<p>" + insert.Sentence + "</p>" + markup[section.start:]
	}
	return markup, skipped
}

func findSection(markup, heading string) (linkSection, bool) {
	want := normalizedText(heading)
	if want == "" {
		return linkSection{}, false
	}
	sections := linkSections(markup, nil)
	for _, section := range sections {
		if normalizedText(section.heading) == want {
			return section, true
		}
	}
	for _, section := range sections {
		have := normalizedText(section.heading)
		if have != "" && (strings.Contains(have, want) || strings.Contains(want, have)) {
			return section, true
		}
	}
	return linkSection{}, false
}

var (
	// insertHeadingLabelRE снимает метку уровня, которую модель переносит из текста статьи:
	// «H2 - Название», «## Название».
	insertHeadingLabelRE = regexp.MustCompile(`(?i)^\s*(?:h[1-6]\s*[-–—:]\s*|#+\s*)`)
	paragraphTagRE       = regexp.MustCompile(`(?i)</?p(?:\s[^>]*)?>`)
	paragraphCloseRE     = regexp.MustCompile(`(?i)</p>`)
)
