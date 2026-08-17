package article

import (
	"fmt"
	"strings"
)

// ArticleInfo is publication metadata parsed from the LLM response.
//
// Меток здесь нет намеренно: они приходят колонкой tags из Excel и моделью не генерируются
// (миграция 000003). Стадия info отвечает за TL;DR и FAQ.
type ArticleInfo struct {
	TLDR           string
	FAQ            string
	AdditionalInfo string
	FallbackUsed   bool
}

// ParseArticleInfo preserves the legacy strict format and falls back to a
// tolerant, heading-based parser for non-empty model responses.
func ParseArticleInfo(text string) (ArticleInfo, error) {
	if strings.TrimSpace(text) == "" {
		return ArticleInfo{}, fmt.Errorf("article info returned an empty response")
	}
	if info, err := parseStrictArticleInfo(text); err == nil {
		tolerant := parseTolerantArticleInfo(text)
		if tolerant.AdditionalInfo == "" && tolerant.TLDR == info.TLDR && tolerant.FAQ == info.FAQ {
			return info, nil
		}
	}
	info := parseTolerantArticleInfo(text)
	info.FallbackUsed = true
	return info, nil
}

func parseStrictArticleInfo(text string) (ArticleInfo, error) {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	type section struct {
		name   string
		line   int
		inline string
	}
	want := []string{"TLDR", "FAQ"}
	sections := make([]section, 0, len(want))
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(sections) == len(want) {
			continue
		}
		name := want[len(sections)]
		prefix := name + ":"
		if strings.HasPrefix(trimmed, prefix) {
			sections = append(sections, section{name: name, line: index, inline: strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))})
		}
	}

	if len(sections) != len(want) {
		return ArticleInfo{}, fmt.Errorf("article info must contain the sections TLDR and FAQ in this order")
	}

	values := make([]string, len(sections))
	for index, current := range sections {
		end := len(lines)
		if index+1 < len(sections) {
			end = sections[index+1].line
		}
		parts := make([]string, 0, end-current.line)
		if current.inline != "" {
			parts = append(parts, current.inline)
		}
		parts = append(parts, lines[current.line+1:end]...)
		values[index] = strings.TrimSpace(strings.Join(parts, "\n"))
		if values[index] == "" {
			return ArticleInfo{}, fmt.Errorf("article info section %s is empty", current.name)
		}
	}

	return ArticleInfo{TLDR: values[0], FAQ: values[1]}, nil
}

type infoSection int

const (
	sectionUnknown infoSection = iota
	sectionTLDR
	sectionFAQ
)

func parseTolerantArticleInfo(text string) ArticleInfo {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	values := map[infoSection][]string{}
	var additional []string
	current := sectionUnknown

	for _, line := range lines {
		if section, inline, recognized := parseInfoHeading(line); recognized {
			if section == current && inline != "" {
				values[current] = append(values[current], line)
				continue
			}
			current = section
			if inline != "" {
				values[current] = append(values[current], inline)
			}
			continue
		}
		if isMarkdownHeading(line) {
			current = sectionUnknown
		}
		if current == sectionUnknown {
			additional = append(additional, line)
		} else {
			values[current] = append(values[current], line)
		}
	}

	return ArticleInfo{
		TLDR:           strings.TrimSpace(strings.Join(values[sectionTLDR], "\n")),
		FAQ:            strings.TrimSpace(strings.Join(values[sectionFAQ], "\n")),
		AdditionalInfo: strings.TrimSpace(strings.Join(additional, "\n")),
	}
}

func parseInfoHeading(line string) (infoSection, string, bool) {
	trimmed := strings.TrimSpace(line)
	for strings.HasPrefix(trimmed, "#") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
	}
	name, inline, hasColon := strings.Cut(trimmed, ":")
	if !hasColon {
		name = trimmed
		inline = ""
	}
	normalizedName := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(name), ";", ""))
	switch normalizedName {
	// Метки моделью больше не запрашиваются. Заголовок «Метки» в ответе не распознаётся и
	// уходит в AdditionalInfo — так видно, что промпт разошёлся с контрактом стадии.
	case "TLDR":
		return sectionTLDR, strings.TrimSpace(inline), true
	case "FAQ":
		return sectionFAQ, strings.TrimSpace(inline), true
	default:
		return sectionUnknown, "", false
	}
}

func isMarkdownHeading(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "#") && strings.TrimSpace(strings.TrimLeft(trimmed, "#")) != ""
}
