package generation

import (
	"html"
	"regexp"
	"strings"
)

// Вёрстка площадки без инлайновых стилей.
//
// Здесь живёт то, чем разметка такой площадки отличается от разметки блога dpoprof: там
// оформление несут инлайновые стили и обёртки <div>, здесь — классы темы, а стилей в теле
// статьи нет ни одного (измерено на 37 опубликованных статьях: ноль атрибутов style).
//
// Функции CleanBlogMarkup и RestoreStats рядом не трогаются и остаются как были: у них своя
// площадка со своей вёрсткой, и одна функция на две несовместимые вёрстки означала бы флаг
// внутри чистки разметки.

const (
	// plainQuoteOpen — врезка на такой площадке это цитата, а не div с рамкой. Классы взяты
	// со страницы блога дословно: их рисует редактор WordPress, и тема опирается на них.
	plainQuoteOpen = `<blockquote class="wp-block-quote is-layout-flow wp-block-quote-is-layout-flow">`

	plainParagraphClass = `class="wp-block-paragraph"`
	plainHeadingClass   = `class="wp-block-heading"`
	plainListClass      = `class="wp-block-list"`
	plainTableClass     = `class="has-fixed-layout"`
	plainFigureOpen     = `<figure class="wp-block-table">`
	plainFigureClose    = `</figure>`
)

var (
	// styleAttrRE — любой инлайновый стиль. На этой площадке их в теле статьи нет вовсе,
	// поэтому выражение не разбирает содержимое: снимается атрибут целиком.
	styleAttrRE = regexp.MustCompile(`(?i)\s*style="[^"]*"`)
	// plainNoClassREs — теги без атрибута class, которым класс темы дописывает код.
	// Промпт этих классов требует, но требование держится не всегда, а тема без них рисует
	// абзац и заголовок в другой типографике.
	plainNoClassREs = map[string]*regexp.Regexp{
		"p":  regexp.MustCompile(`(?i)<p>`),
		"h2": regexp.MustCompile(`(?i)<h2>`),
		"h3": regexp.MustCompile(`(?i)<h3>`),
		"h4": regexp.MustCompile(`(?i)<h4>`),
		"ul": regexp.MustCompile(`(?i)<ul>`),
		"ol": regexp.MustCompile(`(?i)<ol>`),
	}
	// plainTableOpenRE — открывающий тег таблицы в любом виде: с атрибутами и без.
	plainTableOpenRE = regexp.MustCompile(`(?i)<table(\s[^>]*)?>`)
)

// divKind — что сделать с парой div-тегов.
type divKind int

const (
	// divUnwrap — обёртка снимается целиком: и открывающий тег, и его пара.
	divUnwrap divKind = iota
	// divQuote — обёртка врезки становится цитатой.
	divQuote
)

// CleanPlainBlogMarkup приводит разметку к вёрстке площадки, где нет инлайновых стилей.
//
// Отличий от CleanBlogMarkup три, и все три — про площадку:
//
//   - <div> не остаётся ни одного: врезка становится <blockquote>, любая другая обёртка
//     снимается вместе со своей парой. Прокручивать широкую таблицу здесь незачем — за это
//     отвечает <figure class="wp-block-table">;
//   - атрибуты style снимаются полностью;
//   - тегам без класса класс темы дописывается, а таблица заворачивается в <figure>.
//
// Функция чинит, но не отказывает: испорченная разметка возвращается как есть, а не роняет
// стадию — за генерацию уже заплачено.
func CleanPlainBlogMarkup(markup string) string {
	markup = htmlCommentRE.ReplaceAllString(markup, "")
	markup = cleanDivs(markup)
	markup = cleanSpans(markup)
	markup = dsClassAttrRE.ReplaceAllString(markup, "")
	markup = emptyClassRE.ReplaceAllString(markup, "")
	markup = styleAttrRE.ReplaceAllString(markup, "")
	markup = dropEmptyTags(markup)
	markup = ensurePlainBlockClasses(markup)
	return strings.TrimSpace(tagSpacingRE.ReplaceAllString(markup, "<$1>"))
}

// cleanDivs проходит разметку разом по открывающим и закрывающим div: решение об открывающем
// теге обязано повторяться на его паре, иначе от врезки останется «</div>» без начала.
//
// Устроено так же, как cleanSpans: стек видов, по одному на каждый открытый тег. Непарный
// «</div>» просто пропускается — разметку это не портит, а сорванный ответ модели приходит и
// в таком виде.
func cleanDivs(markup string) string {
	var out strings.Builder
	out.Grow(len(markup))
	var stack []divKind
	for i := 0; i < len(markup); {
		switch {
		case strings.HasPrefix(markup[i:], "</div>"):
			kind := divUnwrap
			if len(stack) > 0 {
				kind, stack = stack[len(stack)-1], stack[:len(stack)-1]
			}
			if kind == divQuote {
				out.WriteString("</blockquote>")
			}
			i += len("</div>")
		case strings.HasPrefix(markup[i:], "<div"):
			end := strings.IndexByte(markup[i:], '>')
			if end < 0 {
				out.WriteString(markup[i:])
				return out.String()
			}
			kind := divTagKind(markup[i : i+end+1])
			stack = append(stack, kind)
			if kind == divQuote {
				out.WriteString(plainQuoteOpen)
			}
			i += end + 1
		default:
			out.WriteByte(markup[i])
			i++
		}
	}
	return out.String()
}

// divTagKind отличает врезку от любой другой обёртки.
//
// Признак берётся и из класса модели, и из инлайнового стиля: врезку она рисует то классом
// ds-notice, то полосой слева через border-left, и оба раза это одно и то же — заметка.
func divTagKind(tag string) divKind {
	lower := strings.ToLower(tag)
	if strings.Contains(lower, "notice") || strings.Contains(lower, "border-left") {
		return divQuote
	}
	return divUnwrap
}

// ensurePlainBlockClasses дописывает классы темы тегам, которые пришли без них, и заворачивает
// таблицу в <figure>.
//
// Тег со своим классом не трогается: модель могла написать правильный класс сама, а второй
// атрибут class в одном теге браузер читает как ошибку.
func ensurePlainBlockClasses(markup string) string {
	for tag, pattern := range plainNoClassREs {
		class := plainParagraphClass
		switch tag {
		case "h2", "h3", "h4":
			class = plainHeadingClass
		case "ul", "ol":
			class = plainListClass
		}
		markup = pattern.ReplaceAllString(markup, "<"+tag+" "+class+">")
	}
	return wrapPlainTables(markup)
}

// wrapPlainTables заворачивает таблицу в <figure class="wp-block-table"> и дописывает ей класс
// фиксированной раскладки.
//
// Обёртка значима: на этой площадке она есть у всех 27 таблиц, и по ней тема даёт таблице
// прокрутку на телефоне. Таблица, уже стоящая в <figure>, второй обёртки не получает.
func wrapPlainTables(markup string) string {
	var out strings.Builder
	out.Grow(len(markup))
	rest := markup
	for {
		open := plainTableOpenRE.FindStringIndex(rest)
		if open == nil {
			out.WriteString(rest)
			return out.String()
		}
		before := rest[:open[0]]
		tag := rest[open[0]:open[1]]
		if !strings.Contains(strings.ToLower(tag), "class=") {
			tag = "<table " + plainTableClass + ">"
		}
		closeIndex := strings.Index(strings.ToLower(rest[open[1]:]), "</table>")
		if closeIndex < 0 {
			// Оборванная таблица: дальше искать нечего, остаток уходит как есть.
			out.WriteString(before)
			out.WriteString(tag)
			out.WriteString(rest[open[1]:])
			return out.String()
		}
		body := rest[open[1] : open[1]+closeIndex+len("</table>")]
		out.WriteString(before)
		if wrappedInFigure(before) {
			out.WriteString(tag)
			out.WriteString(body)
		} else {
			out.WriteString(plainFigureOpen)
			out.WriteString(tag)
			out.WriteString(body)
			out.WriteString(plainFigureClose)
		}
		rest = rest[open[1]+closeIndex+len("</table>"):]
	}
}

// wrappedInFigure отвечает, открыт ли перед таблицей <figure>, который её и оборачивает.
func wrappedInFigure(before string) bool {
	tail := strings.ToLower(strings.TrimSpace(before))
	openIndex := strings.LastIndex(tail, "<figure")
	if openIndex < 0 {
		return false
	}
	return !strings.Contains(tail[openIndex:], "</figure>")
}

// RestoreStatsList возвращает в разметку ряд плашек списком.
//
// Аналог RestoreStats для площадки без инлайновых стилей: строка «ПЛАШКИ: значение | подпись»
// из текста статьи становится обычным списком с жирным значением. Как и там, блок собирает
// код: он известен целиком, место его известно, а просить у модели страницу заново — значит
// упереться в тот же предел длины ответа.
//
// Уже свёрстанный моделью список не дублируется: признак — первое значение плашки, стоящее в
// разметке жирным.
func RestoreStatsList(page, markup string) string {
	match := statsLineRE.FindStringSubmatch(page)
	if match == nil {
		return markup
	}
	items := make([]string, 0, 3)
	for _, item := range strings.Split(match[1], ";;") {
		value, label, found := strings.Cut(item, "|")
		value, label = strings.TrimSpace(value), strings.TrimSpace(label)
		if !found || value == "" || label == "" {
			continue
		}
		if len(items) == 0 && strings.Contains(markup, "<strong>"+html.EscapeString(value)+"</strong>") {
			return markup
		}
		items = append(items, "<li><strong>"+html.EscapeString(value)+"</strong> — "+html.EscapeString(label)+".</li>")
	}
	if len(items) == 0 {
		return markup
	}
	block := "<ul " + plainListClass + ">" + strings.Join(items, "") + "</ul>"
	// Место блока — сразу после вводного абзаца и перед первым заголовком, как и у RestoreStats.
	if index := strings.Index(markup, "</p>"); index >= 0 {
		cut := index + len("</p>")
		return markup[:cut] + "\n" + block + markup[cut:]
	}
	return block + "\n" + markup
}
