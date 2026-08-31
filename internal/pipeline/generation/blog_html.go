package generation

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

var (
	// heading1RE находит заголовок первого уровня вместе с содержимым.
	heading1RE = regexp.MustCompile(`(?is)<h1\b[^>]*>.*?</h1>\s*`)
	// heading1BareRE ловит метку заголовка голым текстом в начале ответа.
	heading1BareRE = regexp.MustCompile(`(?im)^\s*H1\s*[-:\x{2013}\x{2014}]\s.*$`)
	// leadingNoiseRE — то, что модель ставит перед первым блоком: комментарий или пробелы.
	leadingNoiseRE = regexp.MustCompile(`(?is)^\s*(?:<!--.*?-->\s*)*`)
	// firstOpenTagRE выделяет открывающий тег первого блока и его имя. Закрывающий ищется
	// потом строкой: обратных ссылок в regexp Go нет, а имя тега нужно то же самое.
	firstOpenTagRE = regexp.MustCompile(`(?is)^<([a-z][a-z0-9]*)\b[^>]*>`)
	// headingLabelTextRE узнаёт текст, который целиком является меткой заголовка.
	headingLabelTextRE = regexp.MustCompile(`(?is)^\s*H1\s*[-:\x{2013}\x{2014}]\s`)
	// hrefRE находит адрес ссылки в разметке.
	hrefRE = regexp.MustCompile(`(?i)href="([^"]*)"`)
	// linkURLRE вынимает адреса из списка перелинковки: он приходит из Excel одной строкой
	// и разделителем в нём служит перевод строки, но полагаться на это нельзя.
	linkURLRE = regexp.MustCompile(`https?://[^\s"'<>]+`)
)

// DropHeading1 убирает из разметки заголовок первого уровня.
//
// Название записи блог рисует сам, поэтому H1 в теле даёт на странице второй заголовок первого
// уровня. Промпт разметки его запрещает, но запрет держится не всегда — пять из двадцати
// восьми страниц pprof_1 пришли с H1. Признак машинный, значит и снимать его должен код.
func DropHeading1(markup string) string {
	markup = heading1RE.ReplaceAllString(markup, "")
	// Метку снимаем только в начале страницы: ниже «H1» может оказаться частью текста — в
	// статье про разметку это законное слово, и вырезать его посреди абзаца нельзя.
	markup = dropLeadingHeadingLabel(markup)
	if head := heading1BareRE.FindStringIndex(markup); head != nil && strings.TrimSpace(markup[:head[0]]) == "" {
		markup = markup[head[1]:]
	}
	return strings.TrimSpace(markup)
}

// dropLeadingHeadingLabel убирает первый блок разметки, если весь его текст — метка заголовка.
//
// Разбором, а не шаблоном строки: модель оборачивает метку по-разному — в абзац с классами, в
// span внутри абзаца, иногда после HTML-комментария, — и шаблон под каждый случай не написать.
// Значим здесь только текст блока, а не то, во сколько тегов он одет.
func dropLeadingHeadingLabel(markup string) string {
	noise := leadingNoiseRE.FindString(markup)
	rest := markup[len(noise):]
	open := firstOpenTagRE.FindStringSubmatchIndex(rest)
	if open == nil {
		return markup
	}
	closing := "</" + strings.ToLower(rest[open[2]:open[3]]) + ">"
	end := strings.Index(strings.ToLower(rest), closing)
	if end < open[1] {
		return markup
	}
	inner := rest[open[1]:end]
	text := strings.TrimSpace(html.UnescapeString(textTagRE.ReplaceAllString(inner, " ")))
	if !headingLabelTextRE.MatchString(text) {
		return markup
	}
	return strings.TrimLeft(rest[end+len(closing):], " \t\r\n")
}

// MissingInternalLinks возвращает адреса перелинковки, которых в разметке нет.
//
// Список ссылок задаёт человек во входной книге, и обязательны все: пропущенная ссылка — это
// не стилистическая вольность разметки, а потерянная перелинковка, ради которой стадия и
// открывает список.
func MissingInternalLinks(markup, links string) []string {
	wanted := LinkURLs(links)
	if len(wanted) == 0 {
		return nil
	}
	placed := make(map[string]struct{}, len(wanted))
	for _, match := range hrefRE.FindAllStringSubmatch(markup, -1) {
		placed[normalizeLinkURL(match[1])] = struct{}{}
	}
	var missing []string
	seen := make(map[string]struct{}, len(wanted))
	for _, link := range wanted {
		key := normalizeLinkURL(link)
		if _, done := seen[key]; done {
			continue
		}
		seen[key] = struct{}{}
		if _, ok := placed[key]; !ok {
			missing = append(missing, link)
		}
	}
	return missing
}

// LinkURLs вынимает адреса из списка перелинковки. Разбор один на всех: по нему и промпт
// собирается, и разметка проверяется — разойдясь, они спорили бы о числе ссылок.
func LinkURLs(links string) []string {
	return linkURLRE.FindAllString(links, -1)
}

// normalizeLinkURL снимает различия записи, которых адрес не меняют: регистр и хвостовые
// косые. Во входной книге встречается и «…/gazosvarshhik//», и тот же адрес без хвоста.
func normalizeLinkURL(value string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(value), "/"))
}

// LeadKept сообщает, дошёл ли до разметки вводный абзац статьи.
//
// Лид обязателен на каждой странице: он идёт сразу после H1 и первым отвечает на запрос
// читателя. Убирая H1, модель заодно уносит и его — поэтому абзац проверяется отдельно, тем
// же способом, что и конец страницы: по первым словам, очищенным от тегов и пунктуации.
func LeadKept(page, markup string) bool {
	probe := pageOpeningProbe(page)
	if probe == "" {
		return true
	}
	return strings.Contains(normalizedText(markup), probe)
}

// blockMarkers — метки визуальных блоков, которые ставит автор строкой текста, а стадия
// вёрстки превращает в оформление. В готовой разметке их быть не должно: метка, дошедшая до
// блога текстом, — признак того, что вёрстка её не узнала.
var blockMarkers = []string{"ЗАМЕТКА:", "ПЛАШКИ:", "ШАГИ:"}

// LeftoverBlockMarkers возвращает метки блоков, оставшиеся в разметке текстом.
//
// Ошибкой это не считается: страница уже написана и оплачена, а метку человек уберёт руками.
// Но знать о ней он обязан — иначе в блоге окажется строка «ЗАМЕТКА: Важно. …» без всякого
// оформления, ровно как уехала строка «H1 - …».
func LeftoverBlockMarkers(markup string) []string {
	text := textTagRE.ReplaceAllString(markup, " ")
	var left []string
	for _, marker := range blockMarkers {
		if strings.Contains(text, marker) {
			left = append(left, marker)
		}
	}
	return left
}

// RestoreLead возвращает в разметку потерянный вводный абзац.
//
// Модель уносит его вместе с заголовком H1, который стадия обязана снять. Просить её вернуть
// страницу заново ради одного абзаца нечем: у длинной статьи разметка не помещается в один
// ответ, и ремонтный запрос оборвался бы ровно так же, как первый. Абзац известен, место
// известно — начало страницы, — поэтому вставляет его код.
func RestoreLead(page, markup string) string {
	lead := pageLead(page)
	if lead == "" || LeadKept(page, markup) {
		return markup
	}
	return "<p>" + html.EscapeString(lead) + "</p>\n" + strings.TrimSpace(markup)
}

// pageLead возвращает первый содержательный абзац текста: заголовок и пустые строки в лид не
// годятся, а признак содержательности тот же, что у проверки покрытия.
func pageLead(page string) string {
	for line := range strings.SplitSeq(page, "\n") {
		if lineProbe(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// RepairHTMLPrompt просит вернуть разметку целиком, назвав, чего в ней не хватает.
//
// Ответ на это сообщение заменяет разметку, а не дописывает её: пропущенная ссылка и потерянный
// лид — не обрыв, места склейки у них нет.
func RepairHTMLPrompt(missingLinks []string) string {
	return fmt.Sprintf(`В разметке нет обязательных ссылок перелинковки: %s.

Верни исправленную разметку статьи целиком, от первого абзаца до последнего, по тому же регламенту вёрстки. Каждую названную ссылку вставь в подходящий по смыслу фрагмент текста, по одной на фрагмент, не в заголовке. Текст статьи не переписывай и не сокращай. В ответе только HTML, без вступлений и комментариев.`,
		strings.Join(missingLinks, ", "))
}

// Разметка приходит из веб-интерфейса DeepSeek вместе с его собственной вёрсткой: классами
// «ds-*» и пустыми span-обёртками. На сайте стилей под них нет, поэтому в блоге они дают либо
// мусор в исходнике, либо — у врезок и обёртки таблиц — потерянное оформление: врезка теряет
// рамку, а широкая таблица перестаёт прокручиваться на телефоне. Чистится это кодом, а не
// промптом: модель воспроизводит привычную ей разметку и делает это устойчивее, чем держит
// запрет.
var (
	dsNoticeRE     = regexp.MustCompile(`(?i)<div class="ds-(?:markdown-)?notice[^"]*"[^>]*>`)
	dsScrollAreaRE = regexp.MustCompile(`(?i)<div class="ds-scroll-area[^"]*"[^>]*>`)
	dsClassAttrRE  = regexp.MustCompile(`(?i)\s*class="ds-[^"]*"`)
	emptyClassRE   = regexp.MustCompile(`(?i)\s*class=""`)
	tagSpacingRE   = regexp.MustCompile(`<([a-z][a-z0-9]*)\s+>`)
)

const (
	// noticeStyle — врезка теми же инлайновыми стилями, какими её и так рисует модель, когда
	// не берёт свой класс.
	noticeStyle = `<div style="border-left:4px solid #1a3d6d;background:#f5f7fa;padding:14px 18px;margin:22px 0;border-radius:0 8px 8px 0;">`
	// scrollStyle — обёртка таблицы: широкая таблица обязана прокручиваться внутри себя.
	scrollStyle = `<div style="overflow-x:auto;">`
)

// CleanBlogMarkup убирает из разметки вёрстку веб-интерфейса модели.
//
// Оформление, которое эти классы несли, сохраняется инлайновыми стилями: врезка остаётся
// врезкой, таблица — прокручиваемой. Пустые span-обёртки снимаются вместе с закрывающими
// тегами, а не по одному открывающему: иначе разметка осталась бы с лишними «</span>».
func CleanBlogMarkup(markup string) string {
	markup = dsNoticeRE.ReplaceAllString(markup, noticeStyle)
	markup = dsScrollAreaRE.ReplaceAllString(markup, scrollStyle)
	markup = cleanSpans(markup)
	markup = dsClassAttrRE.ReplaceAllString(markup, "")
	markup = emptyClassRE.ReplaceAllString(markup, "")
	return strings.TrimSpace(tagSpacingRE.ReplaceAllString(markup, "<$1>"))
}

// spanKind — что сделать с парой span-тегов.
type spanKind int

const (
	spanKeep spanKind = iota
	spanDrop
	spanBold
)

// cleanSpans проходит разметку разом по открывающим и закрывающим span: решение об открывающем
// теге обязано повторяться на его паре, а вложенность span в span здесь обычное дело.
func cleanSpans(markup string) string {
	var out strings.Builder
	out.Grow(len(markup))
	var stack []spanKind
	for i := 0; i < len(markup); {
		switch {
		case strings.HasPrefix(markup[i:], "</span>"):
			kind := spanKeep
			if len(stack) > 0 {
				kind, stack = stack[len(stack)-1], stack[:len(stack)-1]
			}
			switch kind {
			case spanDrop:
			case spanBold:
				out.WriteString("</strong>")
			default:
				out.WriteString("</span>")
			}
			i += len("</span>")
		case strings.HasPrefix(markup[i:], "<span"):
			end := strings.IndexByte(markup[i:], '>')
			if end < 0 {
				out.WriteString(markup[i:])
				return out.String()
			}
			tag := markup[i : i+end+1]
			kind := spanTagKind(tag)
			stack = append(stack, kind)
			switch kind {
			case spanDrop:
			case spanBold:
				out.WriteString("<strong>")
			default:
				out.WriteString(tag)
			}
			i += end + 1
		default:
			out.WriteByte(markup[i])
			i++
		}
	}
	return out.String()
}

// spanTagKind различает обёртки без оформления, подпись врезки и span со своим стилем.
func spanTagKind(tag string) spanKind {
	attributes := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(tag, "<span"), ">"))
	switch {
	case attributes == "" || attributes == `class=""`:
		return spanDrop
	case strings.Contains(attributes, "ds-notice__label"):
		return spanBold
	default:
		return spanKeep
	}
}
