package generation

// Визуальные блоки тела страницы.
//
// Оформление ставит код, а не модель, — тот же приём, что у карточки призыва: у блоков
// фиксированные инлайновые стили в оранжевой теме площадки, и модель воспроизводила бы их
// каждый раз по-своему, тратя на них длину ответа, которой у разметки и так не хватает.
// Модель размечает блок классом (sp-note, sp-stats, sp-steps) и пишет только текст.
//
// Разворачиваются блоки ПОСЛЕ чистки: CleanPlainBlogMarkup снимает <div> и style: у площадки
// в теле страницы их нет, и единственные законные — эти, поставленные кодом.
//
// Живёт оформление в движке, а не в задаче, потому что задач у него две: статьи блога
// (obuch_1) и страницы услуг (obuch_2) одной и той же площадки. Правила у них общие до
// последнего пикселя, и вторая копия разошлась бы с первой молча. Каталог шаблонов приходит
// параметром: сам путь — дело задачи, а не движка.

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// BlockTemplates — разобранные шаблоны. Пустое значение означает «оформлять нечем»:
// разметка остаётся как есть, статья от этого не ломается.
type BlockTemplates struct {
	note    *template.Template
	stats   *template.Template
	steps   *template.Template
	source  *template.Template
	faq     *template.Template
	modules *template.Template
	reviews *template.Template
}

// blockTemplateFiles — имена файлов, по одному на блок. Список закрытый: незнакомый файл в
// каталоге не подхватывается молча.
var blockTemplateFiles = []string{
	"note.html", "stats.html", "steps.html", "source.html", "faq.html",
	"modules.html", "reviews.html",
}

// ReadBlockTemplates читает шаблоны блоков.
//
// Отказ файла стадию не роняет — в отличие от карточки призыва: без карточки статья
// обрывается на абзаце с призывом, а без оформления блоков она просто выглядит проще.
func ReadBlockTemplates(dir string) (*BlockTemplates, error) {
	parsed := make(map[string]*template.Template, len(blockTemplateFiles))
	for _, name := range blockTemplateFiles {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("шаблон блока не прочитан (%s): %w", path, err)
		}
		if strings.TrimSpace(string(raw)) == "" {
			return nil, fmt.Errorf("шаблон блока пуст: %s", path)
		}
		tmpl, err := template.New(name).Parse(string(raw))
		if err != nil {
			return nil, fmt.Errorf("шаблон блока не разобран (%s): %w", path, err)
		}
		parsed[name] = tmpl
	}
	return &BlockTemplates{
		note:    parsed["note.html"],
		stats:   parsed["stats.html"],
		steps:   parsed["steps.html"],
		source:  parsed["source.html"],
		faq:     parsed["faq.html"],
		modules: parsed["modules.html"],
		reviews: parsed["reviews.html"],
	}, nil
}

var (
	// Блоки модель помечает классом: без метки обычный список не отличить от ряда плашек,
	// а перекрасить обычный список — хуже, чем не перекрасить плашки.
	noteRE  = regexp.MustCompile(`(?s)<blockquote[^>]*\bsp-note\b[^>]*>(.*?)</blockquote>`)
	statsRE = regexp.MustCompile(`(?s)<ul[^>]*\bsp-stats\b[^>]*>(.*?)</ul>`)
	stepsRE = regexp.MustCompile(`(?s)<(?:ul|ol)[^>]*\bsp-steps\b[^>]*>(.*?)</(?:ul|ol)>`)
	// Прямоугольники: модули программы и отзывы. Портреты аудитории оформления не получают
	// намеренно — решение владельца: жирный ярлык в обычном списке читается лучше, чем
	// карточка, в которой ярлык уезжает на свою строку, а предложение начинается с запятой.
	modulesRE = regexp.MustCompile(`(?s)<(?:ul|ol)[^>]*\bsp-modules\b[^>]*>(.*?)</(?:ul|ol)>`)
	reviewsRE = regexp.MustCompile(`(?s)<(?:ul|ol)[^>]*\bsp-reviews\b[^>]*>(.*?)</(?:ul|ol)>`)
	// Подпись отзыва. Форм две, и обе приходят от модели: промпт просит её в конце строки
	// после тире, но модель устойчиво ставит имя первым предложением — так вышло на первой
	// же странице. Узнаются обе, иначе подпись остаётся внутри текста, а строка под чертой
	// уходит в блог пустой.
	reviewAuthorRE = regexp.MustCompile(`(?s)\s*[—–-]\s*([^—–]{3,80})\s*$`)
	// «Михаил, 29 лет, г. Москва.» в начале строки: имя, возраст, город.
	reviewLeadAuthorRE = regexp.MustCompile(`^\s*([^.,]{2,40},\s*\d{1,2}\s+(?:год|года|лет)[^.]{0,40},\s*г\.\s*[^.]{2,40})\.\s*`)

	listItemRE  = regexp.MustCompile(`(?s)<li[^>]*>(.*?)</li>`)
	leadRE      = regexp.MustCompile(`(?s)^\s*<strong[^>]*>(.*?)</strong>\s*`)
	paragraphRE = regexp.MustCompile(`(?s)<p[^>]*>(.*?)</p>`)

	// Источник модель пишет отдельной строкой «Источник: домен»; LinkSources к этому моменту
	// уже мог обернуть домен ссылкой, поэтому хвост строки берётся как есть.
	sourceRE = regexp.MustCompile(`(?s)<p[^>]*>\s*(Источник:\s*.*?)</p>`)

	// Раздел FAQ и пара «вопрос — ответ» внутри него.
	//
	// Заголовок узнаётся в обеих формах, что ходят по площадке: «Часто задаваемые вопросы» у
	// статей блога и «Частые вопросы» у страниц услуг. Форму диктует скелет задачи, а не
	// оформление, и узкое правило означало бы, что у половины страниц блок молча не собран.
	faqHeadingRE = regexp.MustCompile(`(?s)<h2[^>]*>[^<]*[Чч]аст[а-яё]*(?:\s+задаваемы[а-яё]+)?\s+вопрос.*?</h2>`)
	// Основная форма: список, у строки жирный вопрос и ответ за ним. Так размечены живые
	// страницы площадки — и блога (stekloduv), и услуг (gid-perevodchik).
	faqListRE     = regexp.MustCompile(`(?s)<(ol|ul)[^>]*>(.*?)</(?:ol|ul)>`)
	faqListItemRE = regexp.MustCompile(`(?s)<li[^>]*>\s*<strong[^>]*>(.*?)</strong>\s*(?:<br\s*/?>)?\s*(.*?)\s*</li>`)
	// Прежняя форма: вопрос заголовком H3, ответ абзацем под ним. Узнаётся ради уже
	// написанных страниц — на площадке она тоже встречается (kuznecz), — но промпты обеих
	// задач с неё сняты: вопрос заголовком не является.
	faqPairRE = regexp.MustCompile(`(?s)<h3[^>]*>(.*?)</h3>\s*<p[^>]*>(.*?)</p>`)
	nextH2RE  = regexp.MustCompile(`(?s)<h2[^>]*>`)

	// Таблица площадки: <figure class="wp-block-table"><table …>. Оформление ей
	// дописывается, а разметка не пересобирается — строки и ячейки остаются модели.
	tableRE = regexp.MustCompile(`(?s)<figure[^>]*\bwp-block-table\b[^>]*>\s*<table[^>]*>(.*?)</table>\s*</figure>`)
	rowRE   = regexp.MustCompile(`(?s)<tr[^>]*>(.*?)</tr>`)
	cellRE  = regexp.MustCompile(`(?s)<t([dh])[^>]*>(.*?)</t[dh]>`)

	// Метка таблицы плюсов и минусов. В ней первая ячейка — такой же длинный текст, как
	// вторая, и жирной она быть не должна: иначе левая колонка уходит в блог целиком
	// жирной, а правая обычной, и симметричный разбор читается как перекос. Отличить её
	// от таблицы ступеней по форме нечем — обе двухстолбцовые, — поэтому метка, тем же
	// приёмом, что sp-note и sp-stats.
	prosConsRE = regexp.MustCompile(`\bsp-proscons\b`)
)

// Стили таблицы. Держатся здесь, а не в шаблоне: таблица не собирается заново, а
// доукрашивается по месту, и подставлять в неё шаблон было бы негде.
const (
	tableStyle      = `width:100%;border-collapse:collapse;font-size:14.5px;`
	headRowStyle    = `background:#fff4e8;`
	zebraRowStyle   = `background:#fbfcfd;`
	headCellStyle   = `padding:11px 14px;border-bottom:2px solid #ff7500;font-weight:700;color:#16202b;text-align:left;`
	firstCellStyle  = `padding:11px 14px;border-bottom:1px solid #edf1f5;font-weight:700;color:#16202b;`
	cellStyle       = `padding:11px 14px;border-bottom:1px solid #edf1f5;color:#2d3a47;`
	tableWrapPrefix = `<figure class="wp-block-table"><div style="overflow-x:auto;">`
	// Обёртка таблицы плюсов и минусов: та же, плюс метка, по которой видно, что первая
	// колонка не жирная намеренно.
	prosConsWrapPrefix = `<figure class="wp-block-table sp-proscons"><div style="overflow-x:auto;">`
	tableWrapSuffix    = `</div></figure>`
)

type noteData struct {
	Label template.HTML
	Text  template.HTML
}

type statItem struct {
	Value   template.HTML
	Caption template.HTML
}

type stepItem struct {
	Number int
	Text   template.HTML
	Last   bool
}

type listData struct{ Items any }

type sourceData struct{ Text template.HTML }

// leadItem — строка списка, разобранная на жирный зачин и остальной текст. Общая у модулей
// и портретов: оформление у них разное, а форма строки одна.
type leadItem struct {
	Lead template.HTML
	Text template.HTML
}

// reviewItem — отзыв: текст и подпись. Подпись отделяется от текста, потому что в оформлении
// она стоит отдельной строкой под чертой, а не хвостом абзаца.
type reviewItem struct {
	Text   template.HTML
	Author template.HTML
}

type faqData struct {
	Question template.HTML
	Answer   template.HTML
}

// DecorateBlocks разворачивает помеченные блоки в оформление площадки.
//
// Неузнанный блок остаётся как есть: помеченный список без пригодных строк полезнее
// пустого места. Шаблоны могут быть nil — тогда разметка возвращается нетронутой.
func DecorateBlocks(markup string, tmpl *BlockTemplates) string {
	if tmpl == nil {
		return markup
	}
	markup = noteRE.ReplaceAllStringFunc(markup, func(block string) string {
		return renderNote(block, tmpl.note, noteRE)
	})
	markup = statsRE.ReplaceAllStringFunc(markup, func(block string) string {
		return renderStats(block, tmpl.stats)
	})
	markup = stepsRE.ReplaceAllStringFunc(markup, func(block string) string {
		return renderSteps(block, tmpl.steps)
	})
	markup = modulesRE.ReplaceAllStringFunc(markup, func(block string) string {
		return renderLeadList(block, tmpl.modules, modulesRE)
	})
	markup = reviewsRE.ReplaceAllStringFunc(markup, func(block string) string {
		return renderReviews(block, tmpl.reviews)
	})
	markup = sourceRE.ReplaceAllStringFunc(markup, func(block string) string {
		return renderSource(block, tmpl.source)
	})
	markup = decorateTables(markup)
	return decorateFAQ(markup, tmpl.faq)
}

// renderNote собирает врезку: ярлык — жирное начало абзаца, остальное — текст.
func renderNote(block string, tmpl *template.Template, outer *regexp.Regexp) string {
	inner := outer.FindStringSubmatch(block)
	if len(inner) < 2 {
		return block
	}
	text := inner[1]
	if paragraph := paragraphRE.FindStringSubmatch(text); len(paragraph) > 1 {
		text = paragraph[1]
	}
	label := "Из практики"
	if lead := leadRE.FindStringSubmatch(text); len(lead) > 1 {
		label = strings.TrimRight(strings.TrimSpace(lead[1]), ".")
		text = leadRE.ReplaceAllString(text, "")
	}
	return render(tmpl, noteData{Label: template.HTML(label), Text: template.HTML(strings.TrimSpace(text))}, block)
}

// renderStats собирает ряд плашек из строк «<strong>значение</strong> — подпись».
func renderStats(block string, tmpl *template.Template) string {
	var items []statItem
	for _, item := range listItemRE.FindAllStringSubmatch(block, -1) {
		value, caption := splitLead(item[1])
		if value == "" || caption == "" {
			return block
		}
		items = append(items, statItem{Value: template.HTML(value), Caption: template.HTML(caption)})
	}
	if len(items) == 0 {
		return block
	}
	return render(tmpl, listData{Items: items}, block)
}

// renderSteps собирает шаги: номер даёт порядок строки, а не текст «Шаг 1.» внутри неё —
// модель нумерует не всегда, а пропуск в нумерации читается как потерянный шаг.
func renderSteps(block string, tmpl *template.Template) string {
	var items []stepItem
	for index, item := range listItemRE.FindAllStringSubmatch(block, -1) {
		text := strings.TrimSpace(stepNumberRE.ReplaceAllString(item[1], ""))
		if text == "" {
			return block
		}
		items = append(items, stepItem{Number: index + 1, Text: template.HTML(text)})
	}
	if len(items) == 0 {
		return block
	}
	items[len(items)-1].Last = true
	return render(tmpl, listData{Items: items}, block)
}

// stepNumberRE снимает служебное «Шаг 1.» из начала строки: номер рисует блок.
var stepNumberRE = regexp.MustCompile(`(?s)^\s*<strong[^>]*>\s*Шаг\s*\d+\.?\s*</strong>\s*`)

// renderLeadList оформляет список, у которого каждая строка начинается жирным зачином.
//
// Строка без зачина или без текста роняет весь блок обратно в исходный вид: половина строк
// в рамках, половина без — хуже, чем ни одной.
//
// Тег <li> и жирный зачин первым внутри него сохраняются намеренно: по этой форме публикация
// разбирает модули программы в поля записи (obuch2.ParseModules). Обернёшь зачин во что-то
// ещё — оформление станет красивее, а аккордеон на странице опустеет.
func renderLeadList(block string, tmpl *template.Template, outer *regexp.Regexp) string {
	inner := outer.FindStringSubmatch(block)
	if len(inner) < 2 {
		return block
	}
	var items []leadItem
	for _, item := range listItemRE.FindAllStringSubmatch(inner[1], -1) {
		lead, text := splitLead(item[1])
		if lead == "" || text == "" {
			return block
		}
		items = append(items, leadItem{Lead: template.HTML(lead), Text: template.HTML(text)})
	}
	if len(items) == 0 {
		return block
	}
	return render(tmpl, listData{Items: items}, block)
}

// renderReviews оформляет отзывы: текст истории и подпись под чертой.
//
// Подпись вырезается из конца строки — «— Елена С., 34 года, г. Екатеринбург». Строка без
// подписи не роняет блок: отзыв без имени остаётся отзывом, просто подпись пуста.
func renderReviews(block string, tmpl *template.Template) string {
	inner := reviewsRE.FindStringSubmatch(block)
	if len(inner) < 2 {
		return block
	}
	var items []reviewItem
	for _, item := range listItemRE.FindAllStringSubmatch(inner[1], -1) {
		text := strings.TrimSpace(item[1])
		author := ""
		if found := reviewAuthorRE.FindStringSubmatch(text); len(found) > 1 {
			author = strings.TrimSpace(found[1])
			text = strings.TrimSpace(reviewAuthorRE.ReplaceAllString(text, ""))
		} else if found := reviewLeadAuthorRE.FindStringSubmatch(text); len(found) > 1 {
			author = strings.TrimSpace(found[1])
			text = strings.TrimSpace(reviewLeadAuthorRE.ReplaceAllString(text, ""))
		}
		if text == "" {
			return block
		}
		items = append(items, reviewItem{Text: template.HTML(text), Author: template.HTML(author)})
	}
	if len(items) == 0 {
		return block
	}
	return render(tmpl, listData{Items: items}, block)
}

// renderSource оформляет строку источника.
func renderSource(block string, tmpl *template.Template) string {
	inner := sourceRE.FindStringSubmatch(block)
	if len(inner) < 2 {
		return block
	}
	return render(tmpl, sourceData{Text: template.HTML(strings.TrimSpace(inner[1]))}, block)
}

// decorateFAQ собирает раздел частых вопросов в оформленный список.
//
// Вопрос заголовком не является, и это решение владельца площадки: заголовок у блока один —
// его H2. Поэтому вопросы приходят строками списка с жирным зачином, и такими же остаются
// после оформления — меняется только вид строки.
//
// Форм входа две, и обе живут на площадке. Основная — список (stekloduv, gid-perevodchik),
// её и просят промпты обеих задач. Прежняя — «H3 вопрос, абзац ответ» (kuznecz): она
// узнаётся ради страниц, написанных до смены правила, и переводится в тот же список.
// Ни одна форма не обязательна: не разобрав раздел, функция возвращает его как есть —
// блок украшает текст, а не несёт его.
func decorateFAQ(markup string, tmpl *template.Template) string {
	heading := faqHeadingRE.FindStringIndex(markup)
	if heading == nil {
		return markup
	}
	start := heading[1]
	end := len(markup)
	if next := nextH2RE.FindStringIndex(markup[start:]); next != nil {
		end = start + next[0]
	}
	section := markup[start:end]
	if decorated, ok := decorateFAQList(section, tmpl); ok {
		return markup[:start] + decorated + markup[end:]
	}
	if decorated, ok := decorateFAQHeadings(section, tmpl); ok {
		return markup[:start] + decorated + markup[end:]
	}
	return markup
}

// decorateFAQList перебирает строки первого списка раздела: жирный зачин — вопрос,
// остальное — ответ.
func decorateFAQList(section string, tmpl *template.Template) (string, bool) {
	list := faqListRE.FindStringIndex(section)
	if list == nil {
		return "", false
	}
	items := faqItems(faqListItemRE.FindAllStringSubmatch(section[list[0]:list[1]], -1))
	if len(items) == 0 {
		return "", false
	}
	return section[:list[0]] + render(tmpl, listData{Items: items}, section[list[0]:list[1]]) +
		section[list[1]:], true
}

// decorateFAQHeadings переводит прежнюю форму — «H3 вопрос, абзац ответ» — в тот же список.
//
// Раздел заменяется целиком, поэтому сначала проверяется, что кроме самих пар в нём ничего
// нет: абзац между вопросами написан человеком, и потерять его ради оформления нельзя.
func decorateFAQHeadings(section string, tmpl *template.Template) (string, bool) {
	pairs := faqPairRE.FindAllStringSubmatch(section, -1)
	items := faqItems(pairs)
	if len(items) == 0 {
		return "", false
	}
	if strings.TrimSpace(faqPairRE.ReplaceAllString(section, "")) != "" {
		return "", false
	}
	return render(tmpl, listData{Items: items}, section), true
}

// faqItems отбрасывает пары, у которых пуст вопрос или ответ: строка без одной половины —
// это не вопрос, а обычный пункт списка, и оформлять её как пару нельзя.
func faqItems(pairs [][]string) []faqData {
	items := make([]faqData, 0, len(pairs))
	for _, pair := range pairs {
		if len(pair) < 3 {
			continue
		}
		question := strings.TrimSpace(pair[1])
		answer := strings.TrimSpace(pair[2])
		if question == "" || answer == "" {
			continue
		}
		items = append(items, faqData{
			Question: template.HTML(question),
			Answer:   template.HTML(answer),
		})
	}
	return items
}

// decorateTables дописывает таблице оформление, не трогая её содержимое.
func decorateTables(markup string) string {
	return tableRE.ReplaceAllStringFunc(markup, func(block string) string {
		inner := tableRE.FindStringSubmatch(block)
		if len(inner) < 2 {
			return block
		}
		// Метка переносится в готовую разметку, хотя оформление к этому моменту уже
		// вписано инлайновыми стилями. Стоит она ради человека и повторного прогона: по
		// собранному файлу иначе не отличить таблицу, которую модель пометила, от той,
		// где первая колонка не жирная по другой причине.
		boldFirstCell := !prosConsRE.MatchString(block)
		rows := rowRE.FindAllStringSubmatch(inner[1], -1)
		// Шапка бывает только у таблицы с несколькими колонками. Плашка параметров страницы
		// услуги одноколоночная, и её первая строка — «Срок: 150 часов», а не заголовок:
		// покрасив её шапкой, оформление объявило бы заголовком обычное значение. Таблицы
		// статей блога двух- и трёхстолбцовые, и для них правило ничего не меняет.
		headed := len(rows) > 0 && len(cellRE.FindAllStringSubmatch(rows[0][1], -1)) > 1
		var out strings.Builder
		if boldFirstCell {
			out.WriteString(tableWrapPrefix)
		} else {
			out.WriteString(prosConsWrapPrefix)
		}
		out.WriteString(`<table style="`)
		out.WriteString(tableStyle)
		out.WriteString(`"><tbody>`)
		for index, row := range rows {
			out.WriteString(rowMarkup(index, row[1], boldFirstCell, headed))
		}
		out.WriteString(`</tbody></table>`)
		out.WriteString(tableWrapSuffix)
		return out.String()
	})
}

// rowMarkup собирает строку таблицы: первая — шапка, дальше чередование и жирная первая
// ячейка, как во всех таблицах этой площадки. boldFirstCell снимается у таблицы плюсов и
// минусов: там первая ячейка — не заголовок строки, а половина разбора. headed снимается у
// одноколоночной таблицы: шапки у неё нет, и нулевая строка — такое же значение, как прочие.
func rowMarkup(index int, row string, boldFirstCell, headed bool) string {
	head := headed && index == 0
	style := ""
	switch {
	case head:
		style = headRowStyle
	case index%2 == 0:
		style = zebraRowStyle
	}
	var out strings.Builder
	out.WriteString(`<tr style="`)
	out.WriteString(style)
	out.WriteString(`">`)
	for cellIndex, cell := range cellRE.FindAllStringSubmatch(row, -1) {
		cellCSS := cellStyle
		switch {
		case head:
			cellCSS = headCellStyle
		case cellIndex == 0 && boldFirstCell:
			cellCSS = firstCellStyle
		}
		out.WriteString(`<td style="`)
		out.WriteString(cellCSS)
		out.WriteString(`">`)
		out.WriteString(strings.TrimSpace(cell[2]))
		out.WriteString(`</td>`)
	}
	out.WriteString(`</tr>`)
	return out.String()
}

// splitLead делит строку плашки на значение и подпись.
func splitLead(item string) (string, string) {
	lead := leadRE.FindStringSubmatch(item)
	if len(lead) < 2 {
		return "", ""
	}
	caption := strings.TrimSpace(leadRE.ReplaceAllString(item, ""))
	caption = strings.TrimLeft(caption, "—–- ")
	return strings.TrimSpace(lead[1]), strings.TrimSpace(caption)
}

// render подставляет данные в шаблон. Отказ шаблона возвращает исходный блок: статья с
// неоформленным списком полезнее статьи с дырой на его месте.
func render(tmpl *template.Template, data any, fallback string) string {
	if tmpl == nil {
		return fallback
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		return fallback
	}
	return strings.TrimSpace(out.String())
}
