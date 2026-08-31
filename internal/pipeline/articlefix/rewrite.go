package articlefix

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/generation"
)

// minRewriteRatio — какую долю исходного текста обязана сохранить правка.
//
// Порог низкий намеренно: правка вправе выбросить устаревший абзац, и придираться к объёму
// мы не должны. Ловит он не редактуру, а обрыв в самом начале ответа, когда до конца статьи
// не дошла даже половина. Основной признак обрыва — не он, а пропавший последний заголовок.
const minRewriteRatio = 0.6

// headingRE находит заголовки разделов. Именно h2 и h3: h1 в теле записи не бывает — его
// рисует тема из заголовка записи.
var headingRE = regexp.MustCompile(`(?is)<h([23])[^>]*>(.*?)</h[23]>`)

// ValidateRewriteCovers проверяет, что правка дошла до конца статьи.
//
// Признак свой, а не общий generation.ValidateHTMLCoversPage: тот ищет в ответе последний
// абзац исходного текста, а здесь исходный текст как раз и правится — переписанный хвост
// означал бы ложный обрыв на каждой статье. Структуру же промпт менять запрещает, поэтому
// проверяется она: последний заголовок исходной статьи обязан найтись в ответе, а заголовков
// в ответе должно быть не меньше, чем было.
//
// Ошибка оборачивает generation.ErrHTMLIncomplete: по этому типу сборщик разметки решает,
// что ответ надо дописать продолжением того же чата, а не считать стадию проваленной.
func ValidateRewriteCovers(original, rewritten string) error {
	originalHeadings := headings(original)
	if len(originalHeadings) > 0 {
		last := originalHeadings[len(originalHeadings)-1]
		if !strings.Contains(normalizeForCompare(rewritten), last) {
			return fmt.Errorf("%w: последнего заголовка статьи («%s») в ответе нет",
				generation.ErrHTMLIncomplete, last)
		}
	}
	return ValidateRewriteStructure(original, rewritten)
}

// ValidateRewriteStructure — тот же признак обрыва без сверки последнего заголовка:
// разделов в ответе не меньше, чем было, и текста не меньше 60%.
//
// Нужен задаче, чей промпт переписывает сами заголовки: pprof_fix_5 переводит страницу на
// НМО, и «Пройдите профессиональную переподготовку…» обязан смениться. Дословной сверки там
// не выдержала бы ни одна статья — каждая уходила бы на дописывание, а потом роняла стадию.
//
// Обрыв ловится и без неё: оборванный ответ теряет вместе с хвостом и его заголовки, а
// оборванный в самом начале — не набирает объёма.
func ValidateRewriteStructure(original, rewritten string) error {
	originalHeadings := headings(original)
	rewrittenHeadings := headings(rewritten)
	if len(originalHeadings) > 0 && len(rewrittenHeadings) < len(originalHeadings) {
		return fmt.Errorf("%w: заголовков было %d, в ответе %d",
			generation.ErrHTMLIncomplete, len(originalHeadings), len(rewrittenHeadings))
	}
	originalLength := len([]rune(normalizeForCompare(original)))
	rewrittenLength := len([]rune(normalizeForCompare(rewritten)))
	if originalLength > 0 && float64(rewrittenLength) < float64(originalLength)*minRewriteRatio {
		return fmt.Errorf("%w: в исходной статье %d символов текста, в ответе %d",
			generation.ErrHTMLIncomplete, originalLength, rewrittenLength)
	}
	return nil
}

// headings отдаёт тексты заголовков разделов, приведённые к сравнимому виду.
func headings(markup string) []string {
	matches := headingRE.FindAllStringSubmatch(markup, -1)
	result := make([]string, 0, len(matches))
	for _, match := range matches {
		text := normalizeForCompare(match[2])
		if text != "" {
			result = append(result, text)
		}
	}
	return result
}

var (
	compareTagRE   = regexp.MustCompile(`(?is)<[^>]*>`)
	compareNoiseRE = regexp.MustCompile(`[^\p{L}\p{N}\s]+`)
	compareSpaceRE = regexp.MustCompile(`\s+`)
)

// normalizeForCompare сводит разметку к тексту без тегов, пунктуации и регистра.
func normalizeForCompare(value string) string {
	text := compareTagRE.ReplaceAllString(value, " ")
	text = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "ё", "е", "Ё", "Е").Replace(text)
	text = compareNoiseRE.ReplaceAllString(text, " ")
	return strings.ToLower(strings.TrimSpace(compareSpaceRE.ReplaceAllString(text, " ")))
}
