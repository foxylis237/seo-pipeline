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
	fenceLineRE   = regexp.MustCompile("^[ \t]*```[a-zA-Z]*[ \t]*$")
	fenceMarkerRE = regexp.MustCompile("```[a-zA-Z]*")
)

// Виды снятой обёртки для WARN-лога. Обычный цельный блок ```html сюда не входит:
// это штатная форма ответа чат-модели, и шуметь о ней нечем.
const (
	htmlCleanupForeignLanguage = "foreign_fence_language"
	htmlCleanupStrippedMarkers = "fence_markers_stripped"
)

// htmlCleanup описывает снятую Markdown-обёртку. Тело ответа сюда не попадает: структура
// нужна для строки лога, а не для разбора содержимого.
type htmlCleanup struct {
	Kind       string
	SizeBefore int
	SizeAfter  int
}

// Applied сообщает, снималась ли обёртка тем способом, о котором стоит предупредить.
func (c htmlCleanup) Applied() bool { return c.Kind != "" }

// normalizeAndValidateHTML извлекает разметку из ответа модели и проверяет её.
//
// Чат-модель предваряет разметку фразой вроде «Вот готовая HTML-версия статьи:» и часто
// кладёт её в Markdown-блок — иногда незакрытый, с чужим языком обёртки или с лишним
// маркером внутри. Всё это оформление канала, а не свойство статьи, поэтому оно снимается
// здесь и никогда не останавливает пайплайн.
//
// Отказом остаются только содержательные признаки: пусто после очистки, ни одного HTML-тега,
// нет ни заголовка, ни абзаца. Иначе отказ модели («не могу выполнить запрос») тихо
// превратился бы в пустую валидную статью.
// NormalizeHTML снимает Markdown-обёртку и проверяет разметку. Экспортируется для потоков
// генерации, живущих вне этого пакета: правила проверки HTML должны быть одни на проект.
func NormalizeHTML(value string) (string, error) {
	html, _, err := normalizeAndValidateHTML(value)
	return html, err
}

func normalizeAndValidateHTML(value string) (string, htmlCleanup, error) {
	html := strings.TrimSpace(value)
	cleanup := htmlCleanup{SizeBefore: len([]rune(html))}
	if html == "" {
		return "", cleanup, fmt.Errorf("HTML-ответ пуст")
	}
	if strings.Contains(html, "```") {
		html, cleanup.Kind = stripMarkdownFence(html)
	}
	if !strings.HasPrefix(html, "<") {
		opening := htmlTagRE.FindStringIndex(html)
		if opening == nil {
			return "", cleanup, fmt.Errorf("ответ не содержит HTML-тегов")
		}
		html = strings.TrimSpace(html[opening[0]:])
	}
	cleanup.SizeAfter = len([]rune(html))
	if html == "" {
		return "", cleanup, fmt.Errorf("HTML-ответ пуст")
	}
	if !htmlTagRE.MatchString(html) {
		return "", cleanup, fmt.Errorf("ответ не содержит HTML-тегов")
	}
	if !htmlContentRE.MatchString(html) {
		return "", cleanup, fmt.Errorf("HTML не содержит заголовка или абзаца")
	}
	return html, cleanup, nil
}

// stripMarkdownFence снимает Markdown-обёртку и возвращает вид того, что было снято.
//
// Цельный блок берём только тогда, когда после него не осталось разметки: тогда за ним стоит
// пояснение модели («Готово!»), и отбросить его правильно. Если за закрывающим маркером есть
// теги, значит блок закрыт не там, где кончается статья, — вырезание по маркерам потеряло бы
// её часть. В этом случае и при нечётном числе маркеров они снимаются по месту, а весь
// остальной текст сохраняется.
func stripMarkdownFence(value string) (string, string) {
	if block := htmlFenceRE.FindStringSubmatchIndex(value); block != nil && !htmlTagRE.MatchString(value[block[1]:]) {
		kind := ""
		if language := value[block[2]:block[3]]; language != "" && !strings.EqualFold(language, "html") {
			kind = htmlCleanupForeignLanguage
		}
		return strings.TrimSpace(value[block[4]:block[5]]), kind
	}
	return dropFenceMarkers(value), htmlCleanupStrippedMarkers
}

// dropFenceMarkers removes fence markers wherever they appear and keeps every other line.
func dropFenceMarkers(value string) string {
	lines := strings.Split(value, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if fenceLineRE.MatchString(strings.TrimSuffix(line, "\r")) {
			continue
		}
		kept = append(kept, fenceMarkerRE.ReplaceAllString(line, ""))
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
