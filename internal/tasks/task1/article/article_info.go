package article

import (
	"fmt"
	"strings"
)

// ArticleInfo is publication metadata parsed from the LLM response.
type ArticleInfo struct {
	Tags string
	TLDR string
	FAQ  string
}

// ParseArticleInfo parses the strict, section-based article info format.
func ParseArticleInfo(text string) (ArticleInfo, error) {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	type section struct {
		name   string
		line   int
		inline string
	}
	want := []string{"Метки", "TLDR", "FAQ"}
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
		return ArticleInfo{}, fmt.Errorf("article info must contain the sections Метки, TLDR and FAQ in this order")
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

	return ArticleInfo{Tags: values[0], TLDR: values[1], FAQ: values[2]}, nil
}
