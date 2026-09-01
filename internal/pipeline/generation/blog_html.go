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
	return "<p>" + inlineMarkup(lead) + "</p>\n" + strings.TrimSpace(markup)
}

// boldRE — жирное начертание в тексте статьи. Формат «**текст**» — договорённость стадий, по
// ней же расставляет теги разметка.
var boldRE = regexp.MustCompile(`\*\*([^*]+)\*\*`)

// inlineMarkup переводит строку статьи в безопасный HTML вместе с жирным начертанием.
//
// Экранирование идёт первым, замена — вторым: иначе «<» из текста попал бы в разметку тегом.
// Без этого лид уходил в блог со звёздочками — «<p>**Сантехник** — это специалист…», —
// потому что код вставлял абзац как есть, а преобразовать разметку было уже некому.
func inlineMarkup(text string) string {
	return boldRE.ReplaceAllString(html.EscapeString(text), "<strong>$1</strong>")
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
	// htmlCommentRE — служебные пометки модели вида «<!-- Блок D. Таблица -->». В блог они
	// уходят как есть и живут в записи навсегда: редактор WordPress их не показывает, а в
	// исходном коде страницы они видны всем. Промпт их запрещает, но запрет держится не
	// всегда — в статье 17 их пришло пятнадцать штук.
	htmlCommentRE = regexp.MustCompile(`(?s)<!--.*?-->`)
	// emptyTagREs — теги без содержимого, по одному выражению на тег. Обратных ссылок в Go
	// нет, поэтому «<(p|div)…</\1>» одним выражением не написать, и каждый тег получает своё.
	//
	// Перечень закрытый. Пустой абзац рисует на странице лишний отступ, пустой заголовок
	// ломает оглавление, пустая врезка — пустую плашку. Ячейки таблицы (td, th) сюда не
	// входят намеренно: удаление пустой ячейки сдвинуло бы столбцы у соседних строк.
	emptyTagREs = emptyTagPatterns("p", "span", "strong", "em", "b", "i", "div",
		"h1", "h2", "h3", "h4", "h5", "h6", "li", "ul", "ol", "blockquote")
	tagSpacingRE = regexp.MustCompile(`<([a-z][a-z0-9]*)\s+>`)
)

const (
	// noticeStyle — врезка теми же инлайновыми стилями, какими её и так рисует модель, когда
	// не берёт свой класс.
	noticeStyle = `<div style="border-left:4px solid #1a3d6d;background:#f5f7fa;padding:14px 18px;margin:22px 0;border-radius:0 8px 8px 0;">`
	// scrollStyle — обёртка таблицы: широкая таблица обязана прокручиваться внутри себя.
	scrollStyle = `<div style="overflow-x:auto;">`
)

// emptyTagPatterns собирает выражение «тег без содержимого» для каждого имени.
func emptyTagPatterns(tags ...string) []*regexp.Regexp {
	patterns := make([]*regexp.Regexp, 0, len(tags))
	for _, tag := range tags {
		patterns = append(patterns, regexp.MustCompile(
			`(?is)<`+tag+`(\s[^>]*)?>(\s|&nbsp;)*</`+tag+`>`))
	}
	return patterns
}

// dropEmptyTags снимает пустые теги, пока они не кончатся.
//
// Один проход не годится: «<div><p></p></div>» после снятия абзаца сам становится пустым,
// и остановка на первом проходе оставила бы в записи пустую обёртку. Пять кругов с запасом
// покрывают любую реальную вложенность, а цикл без предела на испорченной разметке завис бы.
func dropEmptyTags(markup string) string {
	for range 5 {
		cleaned := markup
		for _, pattern := range emptyTagREs {
			cleaned = pattern.ReplaceAllString(cleaned, "")
		}
		if cleaned == markup {
			return markup
		}
		markup = cleaned
	}
	return markup
}

// CleanBlogMarkup убирает из разметки вёрстку веб-интерфейса модели.
//
// Оформление, которое эти классы несли, сохраняется инлайновыми стилями: врезка остаётся
// врезкой, таблица — прокручиваемой. Пустые span-обёртки снимаются вместе с закрывающими
// тегами, а не по одному открывающему: иначе разметка осталась бы с лишними «</span>».
//
// Здесь же снимаются HTML-комментарии: своих мы не ставим, а чужие — это служебные пометки
// модели, которым в опубликованной записи не место.
func CleanBlogMarkup(markup string) string {
	markup = htmlCommentRE.ReplaceAllString(markup, "")
	markup = dsNoticeRE.ReplaceAllString(markup, noticeStyle)
	markup = dsScrollAreaRE.ReplaceAllString(markup, scrollStyle)
	markup = cleanSpans(markup)
	markup = dsClassAttrRE.ReplaceAllString(markup, "")
	markup = emptyClassRE.ReplaceAllString(markup, "")
	markup = dropEmptyTags(markup)
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

// sourceDomains — домены, которые разрешено ставить источником. Список закрытый: ссылка из
// статьи уходит наружу навсегда, и решать, куда она ведёт, промпту нельзя.
var sourceDomains = []string{
	"publication.pravo.gov.ru", "rosstat.gov.ru", "mintrud.gov.ru",
	"superjob.ru", "consultant.ru", "garant.ru", "hh.ru",
}

// sourceTailRE — хвост строки после слова «Источник:». Ограничен строкой и тегом: источник
// живёт в одном абзаце, и захватывать половину статьи выражению незачем.
var sourceTailRE = regexp.MustCompile(`(?i)Источник:\s*[^<\n]{0,160}`)

// LinkSources превращает домены после слова «Источник:» в ссылки с rel="nofollow".
//
// Стадия разметки должна делать это сама, но правило держится не всегда: в статье 17 модель
// приписала «Источник: hh.ru» хвостом к абзацу «Вывод:», шаблон вёрстки такую строку не
// узнал, и в записи не осталось ни одной внешней ссылки. Подтверждение цифр — то, ради чего
// источник и ставится, поэтому ссылку доделывает код.
//
// Уже готовые ссылки не трогаются: хвост, в котором есть «<a», пропускается целиком.
func LinkSources(markup string) string {
	return sourceTailRE.ReplaceAllStringFunc(markup, func(tail string) string {
		if strings.Contains(strings.ToLower(tail), "<a ") {
			return tail
		}
		for _, domain := range sourceDomains {
			index := strings.Index(strings.ToLower(tail), domain)
			if index < 0 {
				continue
			}
			// Домен внутри более длинного (hh.ru в publication.pravo.gov.ru) не ссылка.
			if index+len(domain) < len(tail) && isDomainChar(tail[index+len(domain)]) {
				continue
			}
			link := `<a href="https://` + domain + `/" rel="nofollow noindex noopener" target="_blank">` +
				tail[index:index+len(domain)] + `</a>`
			tail = tail[:index] + link + tail[index+len(domain):]
		}
		return tail
	})
}

// isDomainChar сообщает, продолжается ли доменное имя этим байтом.
func isDomainChar(char byte) bool {
	return char == '.' || char == '-' ||
		(char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9')
}

// statsLineRE — строка плашек в тексте статьи.
var statsLineRE = regexp.MustCompile(`(?m)^ПЛАШКИ:\s*(.+)$`)

// statsPresentRE — признак уже свёрстанного ряда плашек. Сравнивать стиль дословно нельзя:
// код пишет «flex:1 1 150px», модель — «flex: 1 1 150px» с пробелами, и дословная проверка
// пропустила бы её блок мимо. В статье 17 из-за этого ряд плашек встал дважды.
var statsPresentRE = regexp.MustCompile(`(?i)flex:\s*1\s+1\s+150px`)

const (
	// statsRowStyle и statsCardStyle — ряд цифровых плашек. Копия шаблона из промпта стадии
	// html: разметка одна и та же, но собрать её должен уметь и код — модель эту строку
	// теряет молча, и в статье 17 она пропала целиком.
	statsRowStyle   = `<div style="display:flex;flex-wrap:wrap;gap:12px;margin:24px 0;">`
	statsCardStyle  = `<div style="flex:1 1 150px;background:#f5f7fa;border-radius:8px;padding:14px 16px;">`
	statsValueStyle = `<span style="display:block;font-size:20px;font-weight:bold;color:#1a3d6d;">`
	statsLabelStyle = `<span style="font-size:14px;color:#555;">`
)

// RestoreStats возвращает в разметку потерянный ряд плашек.
//
// Строка «ПЛАШКИ: значение | подпись ;; …» стоит в тексте сразу после лида, но стадия разметки
// её теряет: в статье 17 заметки и таблицы она сверстала, а плашки выбросила вместе со
// строкой. Значения известны, место известно — после первого абзаца, — поэтому блок собирает
// код. Просить страницу заново нечем: длинная разметка не помещается в один ответ.
//
// Уже свёрстанный ряд не трогается: признак — наши же стили карточки в разметке.
func RestoreStats(page, markup string) string {
	match := statsLineRE.FindStringSubmatch(page)
	if match == nil || statsPresentRE.MatchString(markup) {
		return markup
	}
	cards := make([]string, 0, 3)
	for _, item := range strings.Split(match[1], ";;") {
		value, label, found := strings.Cut(item, "|")
		value, label = strings.TrimSpace(value), strings.TrimSpace(label)
		if !found || value == "" || label == "" {
			continue
		}
		cards = append(cards, statsCardStyle+statsValueStyle+html.EscapeString(value)+"</span>\n"+
			statsLabelStyle+html.EscapeString(label)+"</span></div>")
	}
	if len(cards) == 0 {
		return markup
	}
	block := statsRowStyle + "\n" + strings.Join(cards, "\n") + "\n</div>"
	// Место блока — сразу после вводного абзаца и перед первым заголовком.
	if index := strings.Index(markup, "</p>"); index >= 0 {
		cut := index + len("</p>")
		return markup[:cut] + "\n" + block + markup[cut:]
	}
	return block + "\n" + markup
}

// headingOpenRE — начало заголовка второго уровня в готовой разметке.
var headingOpenRE = regexp.MustCompile(`(?i)<h2[\s>]`)

// InsertBeforeMiddleHeading вставляет блок перед заголовком, ближайшим к середине страницы.
//
// Место выбирается так, чтобы блок стоял после текстового абзаца и перед разделом: в середине
// длинной статьи это единственная точка, где картинка не разрывает мысль и не прилипает к
// лиду. Заголовок ищется ближайший к середине по длине разметки, а не по счёту разделов:
// разделы у нас неравные, и третий из девяти может оказаться в первой четверти страницы.
//
// Если заголовков нет вовсе, блок уходит в конец: потерять его хуже, чем поставить не там.
func InsertBeforeMiddleHeading(markup, block string) string {
	if strings.TrimSpace(block) == "" {
		return markup
	}
	positions := headingOpenRE.FindAllStringIndex(markup, -1)
	if len(positions) == 0 {
		return strings.TrimSpace(markup) + "\n" + block
	}
	middle := len(markup) / 2
	best := positions[0][0]
	for _, position := range positions {
		if abs(position[0]-middle) < abs(best-middle) {
			best = position[0]
		}
	}
	return strings.TrimSpace(markup[:best]) + "\n" + block + "\n" + markup[best:]
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
