package pprof1

import (
	"html"
	"regexp"
	"strings"
)

// FAQ статьи блога выводит тема из полей blog_faq_*, поэтому в теле его быть не должно.
// Промпт разметки просит вырезать блок, но модель иногда оставляет его, и вопросы на странице
// идут дважды.

var (
	h2RE         = regexp.MustCompile(`(?is)<h2\b[^>]*>(.*?)</h2>`)
	tagRE        = regexp.MustCompile(`<[^>]+>`)
	faqHeadingRE = regexp.MustCompile(`(?i)част\S* вопрос|часто задаваем|вопросы и ответы|ответы на вопросы|\bFAQ\b`)
)

// dropFAQSection вырезает раздел частых вопросов — от его H2 до следующего H2 или до конца
// разметки. Обычный раздел со словом «вопрос» в заголовке («Вопросы на собеседовании») не
// трогается: признак — именно формулировка блока частых вопросов.
func dropFAQSection(markup string) (string, bool) {
	headings := h2RE.FindAllStringSubmatchIndex(markup, -1)
	for i, loc := range headings {
		text := html.UnescapeString(tagRE.ReplaceAllString(markup[loc[2]:loc[3]], ""))
		if !faqHeadingRE.MatchString(text) {
			continue
		}
		end := len(markup)
		if i+1 < len(headings) {
			end = headings[i+1][0]
		}
		return strings.TrimSpace(markup[:loc[0]]) + "\n\n" + strings.TrimLeft(markup[end:], "\n"), true
	}
	return markup, false
}
