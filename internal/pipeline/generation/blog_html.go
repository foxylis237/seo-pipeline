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

// DropBlockMarkerLines убирает из текста страницы строки-маркеры визуальных блоков.
//
// Нужна там, где текст страницы служит образцом для сверки с разметкой. Маркер — не абзац, а
// инструкция вёрстке, и в готовой разметке его нет по построению: строка «ПЛАШКИ: 150 часов |
// Срок ;; …» стала таблицей. Но по длине она обгоняет вводные абзацы, поэтому поиск первого
// абзаца выбирает именно её — и восстановление лида возвращает в разметку саму инструкцию,
// текстом, в блог. Спрашивать лид у текста без маркеров дешевле, чем учить общий разбор
// абзаца отличать их: словарь маркеров живёт здесь же, рядом с blockMarkers.
func DropBlockMarkerLines(page string) string {
	lines := strings.Split(page, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		marked := false
		for _, marker := range blockMarkers {
			if strings.HasPrefix(trimmed, marker) {
				marked = true
				break
			}
		}
		if !marked {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

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

// InsertBeforeFirstHeading вставляет блок в конец вводной части — перед первым заголовком.
//
// Сестра InsertBeforeMiddleHeading, и различаются они не вкусом, а площадкой. У статьи блога
// картинка стоит в середине: там она не разрывает мысль и не прилипает к лиду. У
// коммерческой страницы услуги вводная часть — это лид и плашка с параметрами программы,
// картинка идёт сразу за ними, а середина страницы приходится на разбор модулей обучения, и
// картинка внутри него читается как потерянная.
//
// «Перед первым заголовком», а не «после первого абзаца»: вводных абзацев бывает два, между
// ними и заголовком стоит плашка, и любой счёт абзацев ломался бы на первой же странице с
// другим их числом. Заголовков нет вовсе — блок уходит в конец: потерять его хуже, чем
// поставить не там.
func InsertBeforeFirstHeading(markup, block string) string {
	if strings.TrimSpace(block) == "" {
		return markup
	}
	position := headingOpenRE.FindStringIndex(markup)
	if position == nil {
		return strings.TrimSpace(markup) + "\n" + block
	}
	return strings.TrimSpace(markup[:position[0]]) + "\n" + block + "\n" + markup[position[0]:]
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// linkSection — раздел разметки с точки зрения перелинковки: заголовок и ссылки внутри него.
type linkSection struct {
	heading string
	// start и end — границы содержимого раздела в разметке.
	start, end int
	urls       []string
}

// linkSections режет разметку по заголовкам и собирает внутренние ссылки каждого раздела.
// Текст до первого заголовка — тоже раздел: лид статьи ссылку принять может.
func linkSections(markup string, wanted map[string]struct{}) []linkSection {
	bounds := headingBlockRE.FindAllStringSubmatchIndex(markup, -1)
	sections := make([]linkSection, 0, len(bounds)+1)
	add := func(heading string, start, end int) {
		if start >= end {
			return
		}
		section := linkSection{heading: heading, start: start, end: end}
		for _, match := range hrefRE.FindAllStringSubmatchIndex(markup[start:end], -1) {
			url := markup[start+match[2] : start+match[3]]
			if _, ok := wanted[normalizeLinkURL(url)]; ok {
				section.urls = append(section.urls, url)
			}
		}
		sections = append(sections, section)
	}
	previousEnd := 0
	for index, match := range bounds {
		if index == 0 && match[0] > 0 {
			add("", 0, match[0])
		}
		end := len(markup)
		if index+1 < len(bounds) {
			end = bounds[index+1][0]
		}
		add(stripTags(markup[match[2]:match[3]]), match[1], end)
		previousEnd = end
	}
	if len(bounds) == 0 && previousEnd == 0 {
		add("", 0, len(markup))
	}
	return sections
}

// CrowdedLinks возвращает адреса, которые стоит переставить: те, что делят раздел с другой
// ссылкой. Первая в разделе остаётся на месте, лишние уезжают.
//
// Правило родилось на статье 4: все пять ссылок модель сложила в финальный раздел с призывом,
// в последние 7% страницы. Формально каждая ссылка была на месте — проверка на пропущенные их
// видела, — но перелинковки из этого не вышло: читатель до кучки не доходит, а поиску она
// выглядит рекламным блоком. Промпт правило держит не всегда, поэтому распределение проверяет
// код.
func CrowdedLinks(markup, links string) []string {
	wantedURLs := LinkURLs(links)
	if len(wantedURLs) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(wantedURLs))
	for _, link := range wantedURLs {
		wanted[normalizeLinkURL(link)] = struct{}{}
	}
	var crowded []string
	for _, section := range linkSections(markup, wanted) {
		crowded = append(crowded, section.urls[min(1, len(section.urls)):]...)
	}
	return crowded
}

// FreeLinkHeadings перечисляет заголовки разделов, в которых внутренних ссылок ещё нет.
// Список уходит в промпт ремонта: без него модель складывает перенесённые ссылки туда же,
// откуда их только что сняли.
func FreeLinkHeadings(markup, links string) []string {
	wantedURLs := LinkURLs(links)
	wanted := make(map[string]struct{}, len(wantedURLs))
	for _, link := range wantedURLs {
		wanted[normalizeLinkURL(link)] = struct{}{}
	}
	var free []string
	for _, section := range linkSections(markup, wanted) {
		if len(section.urls) == 0 && strings.TrimSpace(section.heading) != "" {
			free = append(free, strings.TrimSpace(section.heading))
		}
	}
	return free
}

// UnwrapLinks снимает тег ссылки с названных адресов, оставляя её текст. Нужен перед переносом:
// абзац остаётся целым, а ссылка уезжает в другой раздел отдельным предложением.
func UnwrapLinks(markup string, urls []string) string {
	if len(urls) == 0 {
		return markup
	}
	drop := make(map[string]struct{}, len(urls))
	for _, url := range urls {
		drop[normalizeLinkURL(url)] = struct{}{}
	}
	return anchorRE.ReplaceAllStringFunc(markup, func(anchor string) string {
		match := anchorRE.FindStringSubmatch(anchor)
		if len(match) < 3 {
			return anchor
		}
		if _, ok := drop[normalizeLinkURL(match[1])]; !ok {
			return anchor
		}
		return match[2]
	})
}

// anchorRE — ссылка целиком вместе с текстом. Вложенных <a> в разметке не бывает, поэтому
// нежадного разбора достаточно.
var anchorRE = regexp.MustCompile(`(?is)<a\s[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)

// SpreadInternalLinks расставляет перелинковку по статье силами кода, без единого вопроса к
// модели.
//
// Делает две вещи. Сгрудившиеся ссылки — те, что делят раздел с другой, — переезжают вместе со
// своим предложением в разделы, где ссылок ещё нет: предложение уже написано моделью, оно
// законченное, и переносить его безопаснее, чем просить новое. Ссылки, которых в разметке нет
// вовсе, дописываются отдельным предложением с названием программы: название приходит с сайта
// вместе с адресом, поэтому анкор коду известен и придумывать нечего.
//
// Случай снят со статьи 4: пять ссылок из пяти модель сложила в финальный раздел с призывом,
// в последние 7% страницы. Формально каждая была на месте, но перелинковки из этого не вышло.
//
// Возвращает разметку и число тронутых ссылок. Свободных разделов не хватило — лишние ссылки
// остаются там, где стояли: статья без идеального распределения полезнее сломанной.
func SpreadInternalLinks(markup, links string) (string, int) {
	return spreadLinks(markup, links, true)
}

// SpreadCrowdedLinks — SpreadInternalLinks без дописывания: сгрудившиеся ссылки переезжают, а
// недостающим код своего предложения не пишет. Шаблонная строка «Подробнее о программе» —
// страховка, а не текст для читателя, и задаче, которая доспрашивает ссылки у модели, она не
// нужна.
func SpreadCrowdedLinks(markup, links string) (string, int) {
	return spreadLinks(markup, links, false)
}

func spreadLinks(markup, links string, addMissing bool) (string, int) {
	wantedURLs := LinkURLs(links)
	if len(wantedURLs) == 0 {
		return markup, 0
	}
	wanted := make(map[string]struct{}, len(wantedURLs))
	for _, link := range wantedURLs {
		wanted[normalizeLinkURL(link)] = struct{}{}
	}
	names := linkNames(links)

	// Разметка до правок: если разложить удалось не всё, статья возвращается к ней целиком.
	// Вырезанное, но не вставленное предложение — потерянная ссылка, а это хуже кучки.
	original := markup
	// Повтор одного адреса промпт запрещает («каждая появляется ровно один раз»), но запрет
	// держится не всегда: в статье 2 ссылка на обучение сантехника пришла дважды. Вторую
	// снимает код — текст предложения при этом остаётся на месте.
	markup = dropRepeatedLinks(markup, wanted)
	// Сначала собираются предложения, и только потом выбираются места: раскладывать их по
	// первым свободным разделам подряд — значит собрать все ссылки в начале статьи, что от
	// кучки в конце отличается только стороной страницы (статья 1: 8%, 10%, 22%, 28%, 38%).
	var sentences []string
	// Вырезанная ссылка на время становится пропущенной: свой адрес она уносит вместе с
	// предложением. Без этой отметки второй цикл дописал бы ей ещё одно предложение, и в
	// статье оказались бы две ссылки на одну программу.
	carried := make(map[string]struct{})
	for _, url := range CrowdedLinks(markup, links) {
		sentence, trimmed, ok := cutLinkSentence(markup, url)
		if !ok {
			continue
		}
		markup = trimmed
		carried[normalizeLinkURL(url)] = struct{}{}
		sentences = append(sentences, sentence)
	}
	for _, url := range MissingInternalLinks(markup, links) {
		if !addMissing {
			break
		}
		if _, moved := carried[normalizeLinkURL(url)]; moved {
			continue
		}
		name := names[normalizeLinkURL(url)]
		if strings.TrimSpace(name) == "" {
			continue
		}
		sentences = append(sentences, fmt.Sprintf(`Подробнее о программе — <a href="%s">%s</a>.`, url, name))
	}
	if len(sentences) == 0 {
		return markup, 0
	}
	spread, placed := placeSentencesEvenly(markup, sentences, wanted)
	if placed < len(sentences) {
		return original, 0
	}
	return spread, placed
}

// placeSentencesEvenly раскладывает предложения по свободным разделам с равным шагом, сверху
// вниз. Вставка меняет разметку, поэтому места выбираются заново на каждом шаге, а уже занятые
// разделы отсеиваются сами: раздел со ссылкой свободным больше не считается.
func placeSentencesEvenly(markup string, sentences []string, wanted map[string]struct{}) (string, int) {
	placed := 0
	for index, sentence := range sentences {
		free := freeSectionOffsets(markup, wanted)
		if len(free) == 0 {
			break
		}
		// Каждому предложению — своя доля свободных разделов, и внутри доли берётся середина.
		// Мест на шаг становится меньше (занятый раздел свободным больше не считается),
		// поэтому доля пересчитывается каждый раз, а ссылки расходятся по всей длине статьи.
		left := len(sentences) - index
		share := len(free) / max(1, left)
		at := free[min(share/2, len(free)-1)]
		markup = markup[:at] + "\n<p>" + sentence + "</p>" + markup[at:]
		placed++
	}
	return markup, placed
}

// freeSectionOffsets возвращает позиции сразу за первым абзацем каждого раздела без ссылок.
func freeSectionOffsets(markup string, wanted map[string]struct{}) []int {
	var offsets []int
	for _, section := range linkSections(markup, wanted) {
		if len(section.urls) > 0 || strings.TrimSpace(section.heading) == "" {
			continue
		}
		end := strings.Index(markup[section.start:section.end], "</p>")
		if end < 0 {
			continue
		}
		offsets = append(offsets, section.start+end+len("</p>"))
	}
	return offsets
}

// linkNames разбирает список перелинковки в пары «адрес — название». Строка приходит в виде
// «Название — https://…», как её собирает поток: название снято со страницы программы, и
// другого источника анкора у кода нет.
func linkNames(links string) map[string]string {
	names := make(map[string]string)
	for _, line := range strings.Split(strings.ReplaceAll(links, "\r\n", "\n"), "\n") {
		url := linkURLRE.FindString(line)
		if url == "" {
			continue
		}
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.Replace(line, url, "", 1)), "—"))
		names[normalizeLinkURL(url)] = strings.TrimSpace(strings.Trim(name, "—-–:"))
	}
	return names
}

// cutLinkSentence вырезает из разметки предложение со ссылкой и возвращает его вместе с
// разметкой без него. Границы предложения ищутся внутри одного абзаца или пункта списка:
// перенос через границу блока порвал бы вёрстку.
func cutLinkSentence(markup, url string) (sentence, trimmed string, ok bool) {
	for _, match := range textBlockRE.FindAllStringSubmatchIndex(markup, -1) {
		inner := markup[match[2]:match[3]]
		anchor := strings.Index(inner, url)
		if anchor < 0 {
			continue
		}
		from, to := sentenceBounds(inner, anchor)
		sentence = strings.TrimSpace(inner[from:to])
		if sentence == "" {
			return "", "", false
		}
		rest := strings.TrimSpace(inner[:from] + " " + inner[to:])
		rest = strings.TrimSpace(strings.ReplaceAll(rest, "  ", " "))
		if stripTags(rest) == "" {
			// В блоке не осталось текста — убираем блок целиком, пустой абзац вёрстке не нужен.
			return sentence, strings.TrimSpace(markup[:match[0]]) + "\n" + strings.TrimSpace(markup[match[1]:]), true
		}
		return sentence, markup[:match[2]] + rest + markup[match[3]:], true
	}
	return "", "", false
}

// sentenceBounds возвращает границы предложения, внутри которого стоит позиция pos. Точки
// внутри тегов границей не считаются: «href="https://dpoprof.ru/a/"» иначе резал бы ссылку.
func sentenceBounds(text string, pos int) (int, int) {
	inTag := false
	starts := []int{0}
	for index, symbol := range text {
		switch {
		case symbol == '<':
			inTag = true
		case symbol == '>':
			inTag = false
		case inTag:
		case symbol == '.' || symbol == '!' || symbol == '?':
			next := index + 1
			for next < len(text) && (text[next] == ' ' || text[next] == '<') {
				if text[next] == '<' {
					break
				}
				next++
			}
			if next > pos {
				return lastStart(starts, pos), min(next, len(text))
			}
			starts = append(starts, next)
		}
	}
	return lastStart(starts, pos), len(text)
}

func lastStart(starts []int, pos int) int {
	from := 0
	for _, start := range starts {
		if start <= pos {
			from = start
			continue
		}
		break
	}
	return from
}

// textBlockRE — абзац или пункт списка: только внутри них живут предложения со ссылками.
var textBlockRE = regexp.MustCompile(`(?is)<(?:p|li)[^>]*>(.*?)</(?:p|li)>`)

func stripTags(text string) string { return anyTagRE.ReplaceAllString(text, "") }

var (
	// headingBlockRE — заголовок любого уровня вместе с содержимым: по нему разметка режется
	// на разделы, а разделы — единица распределения перелинковки.
	headingBlockRE = regexp.MustCompile(`(?is)<h[1-6][^>]*>(.*?)</h[1-6]>`)
	anyTagRE       = regexp.MustCompile(`(?s)<[^>]+>`)
)

// dropRepeatedLinks оставляет у каждого адреса перелинковки одно вхождение — первое. Тег со
// второго и следующих снимается, текст ссылки остаётся частью предложения.
func dropRepeatedLinks(markup string, wanted map[string]struct{}) string {
	seen := make(map[string]struct{}, len(wanted))
	return anchorRE.ReplaceAllStringFunc(markup, func(anchor string) string {
		match := anchorRE.FindStringSubmatch(anchor)
		if len(match) < 3 {
			return anchor
		}
		key := normalizeLinkURL(match[1])
		if _, ours := wanted[key]; !ours {
			return anchor
		}
		if _, repeated := seen[key]; repeated {
			return match[2]
		}
		seen[key] = struct{}{}
		return anchor
	})
}
