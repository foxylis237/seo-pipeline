package generation

import (
	"strings"
	"testing"
)

func TestNormalizeHeadingsBringsEveryFormToOne(t *testing.T) {
	text := strings.Join([]string{
		"H1 - Профессия маляра",
		"",
		"Лид статьи.",
		"H2: Обязанности",
		"## Разряды",
		"**H3 — Документы**",
		"#### Условия труда",
		"H2 – Зарплата",
	}, "\n")

	want := strings.Join([]string{
		"H1 - Профессия маляра",
		"",
		"Лид статьи.",
		"H2 - Обязанности",
		"H2 - Разряды",
		"H3 - Документы",
		"H4 - Условия труда",
		"H2 - Зарплата",
	}, "\n")

	if got := NormalizeHeadings(text); got != want {
		t.Fatalf("заголовки приведены не к одной форме:\n%s", got)
	}
}

// Обычный текст под правило не попадает: признак заголовка — вся строка целиком.
func TestNormalizeHeadingsKeepsPlainText(t *testing.T) {
	text := "Разряд H2 - это второй уровень, его дают после экзамена.\nСмотри таблицу: H3 значений."
	if got := NormalizeHeadings(text); got != text {
		t.Fatalf("правило зацепило обычный текст:\n%s", got)
	}
}

func TestDropHeading1(t *testing.T) {
	markup := "<h1>Профессия маляра</h1>\n<p>Лид статьи.</p>"
	got := DropHeading1(markup)
	if strings.Contains(strings.ToLower(got), "<h1") {
		t.Fatalf("H1 остался: %q", got)
	}
	if !strings.Contains(got, "Лид статьи.") {
		t.Fatalf("вместе с H1 потерян текст: %q", got)
	}
}

func TestMissingInternalLinksNamesOnlyAbsent(t *testing.T) {
	links := "https://dpoprof.ru/obuchenie/malyar/\nhttps://dpoprof.ru/povyshenie/malyar/"
	markup := `<p>Учат на <a href="https://dpoprof.ru/obuchenie/malyar">маляра</a>.</p>`

	missing := MissingInternalLinks(markup, links)
	if len(missing) != 1 || missing[0] != "https://dpoprof.ru/povyshenie/malyar/" {
		t.Fatalf("пропущенные ссылки определены неверно: %v", missing)
	}
}

// Хвостовая косая адрес не меняет: во входной книге один и тот же адрес встречается и с ней,
// и с двумя.
func TestMissingInternalLinksIgnoresTrailingSlash(t *testing.T) {
	markup := `<p><a href="https://dpoprof.ru/perepodgotovka/gazosvarshhik/">газосварщик</a></p>`
	if missing := MissingInternalLinks(markup, "https://dpoprof.ru/perepodgotovka/gazosvarshhik//"); missing != nil {
		t.Fatalf("ссылка со сдвоенной косой сочтена пропущенной: %v", missing)
	}
}

func TestLeadKept(t *testing.T) {
	page := "H1 - Профессия маляра\n\nМаляр готовит поверхности и наносит лакокрасочные материалы на объектах любой сложности.\n\nH2 - Обязанности\n\nТекст раздела."
	full := `<p>Маляр готовит поверхности и наносит лакокрасочные материалы на объектах любой сложности.</p><h2>Обязанности</h2>`
	if !LeadKept(page, full) {
		t.Fatal("вводный абзац сочтён потерянным, хотя он на месте")
	}
	if LeadKept(page, `<h2>Обязанности</h2><p>Текст раздела.</p>`) {
		t.Fatal("разметка без вводного абзаца принята")
	}
}

func TestRepairHTMLPromptNamesMissingLinks(t *testing.T) {
	prompt := RepairHTMLPrompt([]string{"https://dpoprof.ru/obuchenie/malyar/"})
	if !strings.Contains(prompt, "https://dpoprof.ru/obuchenie/malyar/") {
		t.Fatalf("в просьбе нет пропущенной ссылки: %s", prompt)
	}
}

// Лид возвращает код, а не модель: у длинной статьи разметка не помещается в один ответ, и
// просьба «верни страницу заново» оборвалась бы так же, как первый ответ.
func TestRestoreLeadPutsParagraphBack(t *testing.T) {
	page := "H1 - Профессия маляра\n\nМаляр готовит поверхности и наносит лакокрасочные материалы на объектах любой сложности.\n\nH2 - Обязанности\n\nТекст раздела."
	markup := "<h2>Обязанности</h2><p>Текст раздела.</p>"

	restored := RestoreLead(page, markup)
	if !LeadKept(page, restored) {
		t.Fatalf("лид не вернулся: %s", restored)
	}
	if !strings.HasPrefix(restored, "<p>Маляр готовит поверхности") {
		t.Fatalf("лид встал не первым абзацем: %s", restored)
	}
	if !strings.Contains(restored, "<h2>Обязанности</h2>") {
		t.Fatalf("остальная разметка потеряна: %s", restored)
	}
}

// Разметку с лидом трогать не за чем: второго вводного абзаца быть не должно.
func TestRestoreLeadKeepsMarkupWithLead(t *testing.T) {
	page := "H1 - Профессия маляра\n\nМаляр готовит поверхности и наносит лакокрасочные материалы на объектах любой сложности.\n\nH2 - Обязанности"
	markup := "<p>Маляр готовит поверхности и наносит лакокрасочные материалы на объектах любой сложности.</p><h2>Обязанности</h2>"
	if got := RestoreLead(page, markup); got != markup {
		t.Fatalf("разметка с лидом изменена: %s", got)
	}
}

func TestCleanBlogMarkupRemovesInterfaceMarkup(t *testing.T) {
	markup := `<p class="ds-markdown-paragraph"><span class="">Маляр готовит поверхности.</span></p>` +
		`<h2><strong><span class="">Обязанности</span></strong></h2>` +
		`<p class="ds-markdown-paragraph" style="margin:0;"><span class="">С отступом.</span></p>`

	got := CleanBlogMarkup(markup)
	for _, forbidden := range []string{"ds-markdown-paragraph", `class=""`, "<span"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("в разметке осталось %q:\n%s", forbidden, got)
		}
	}
	for _, want := range []string{"<p>Маляр готовит поверхности.</p>", "<h2><strong>Обязанности</strong></h2>", `<p style="margin:0;">С отступом.</p>`} {
		if !strings.Contains(got, want) {
			t.Fatalf("потеряно %q:\n%s", want, got)
		}
	}
}

// Оформление, которое несли классы интерфейса, обязано сохраниться: врезка остаётся врезкой,
// а широкая таблица — прокручиваемой.
func TestCleanBlogMarkupKeepsStylingOfNoticeAndTable(t *testing.T) {
	markup := `<div class="ds-scroll-area ds-scroll-area--enabled _1210dd7"><table style="width:100%;"><tr><td>2 разряд</td></tr></table></div>` +
		`<div class="ds-notice ds-notice--info"><span class="ds-notice__label">Важно</span><span class="">Нужен допуск.</span></div>`

	got := CleanBlogMarkup(markup)
	if strings.Contains(got, "ds-") {
		t.Fatalf("классы интерфейса остались:\n%s", got)
	}
	if !strings.Contains(got, "overflow-x:auto") {
		t.Fatalf("таблица потеряла прокрутку:\n%s", got)
	}
	if !strings.Contains(got, "border-left:4px solid") {
		t.Fatalf("врезка потеряла оформление:\n%s", got)
	}
	if !strings.Contains(got, "<strong>Важно</strong>") {
		t.Fatalf("подпись врезки потеряла выделение:\n%s", got)
	}
	if !strings.Contains(got, "<table style=\"width:100%;\">") {
		t.Fatalf("таблица потеряна:\n%s", got)
	}
}

// Span со своим стилем — часть оформления страницы, а не обёртка интерфейса.
func TestCleanBlogMarkupKeepsStyledSpans(t *testing.T) {
	markup := `<div style="flex:1 1 150px;"><span style="font-weight:700;">от 300 т.р.</span><span class="">в месяц</span></div>`
	got := CleanBlogMarkup(markup)
	if !strings.Contains(got, `<span style="font-weight:700;">от 300 т.р.</span>`) {
		t.Fatalf("span со стилем снят:\n%s", got)
	}
	if strings.Contains(got, `<span class="">`) || strings.Count(got, "</span>") != 1 {
		t.Fatalf("пустые обёртки сняты неправильно:\n%s", got)
	}
	if !strings.Contains(got, "в месяц") {
		t.Fatalf("потерян текст:\n%s", got)
	}
}

// Заголовок модель выводит и обычным абзацем с текстовой меткой — «<p>H1 - Название</p>».
// Тега тут нет, а на странице блога это лишняя строка перед лидом.
func TestDropHeading1RemovesTextLabel(t *testing.T) {
	markup := "<p>H1 - Профессиональная переподготовка по рабочей специальности</p>\n<p>Лид статьи о переподготовке.</p>"
	got := DropHeading1(markup)
	if strings.Contains(got, "H1 -") {
		t.Fatalf("метка заголовка осталась: %q", got)
	}
	if !strings.HasPrefix(got, "<p>Лид статьи") {
		t.Fatalf("лид не стал первым абзацем: %q", got)
	}
}

// Ту же метку без всякого тега снимаем только в самом начале ответа.
func TestDropHeading1RemovesBareLabelOnlyAtStart(t *testing.T) {
	if got := DropHeading1("H1 - Тема статьи\n<p>Лид статьи о переподготовке.</p>"); strings.Contains(got, "H1 -") {
		t.Fatalf("голая метка в начале осталась: %q", got)
	}
	inside := "<p>Разметка начинается с тега H1 - его ставит блог.</p>"
	if got := DropHeading1(inside); got != inside {
		t.Fatalf("правило зацепило текст абзаца: %q", got)
	}
}

// Метку блока вёрстка обязана превратить в оформление. Дошедшая до разметки текстом — повод
// предупредить человека: в блоге она будет голой строкой.
func TestLeftoverBlockMarkers(t *testing.T) {
	markup := `<p>ЗАМЕТКА: Важно. Без удостоверения к работе не допустят.</p><p>Обычный абзац.</p>`
	left := LeftoverBlockMarkers(markup)
	if len(left) != 1 || left[0] != "ЗАМЕТКА:" {
		t.Fatalf("метка не найдена: %v", left)
	}
	verstka := `<div style="border-left:4px solid #1a3d6d;"><p><strong>Важно</strong> Без удостоверения не допустят.</p></div>`
	if left := LeftoverBlockMarkers(verstka); left != nil {
		t.Fatalf("свёрстанная заметка сочтена меткой: %v", left)
	}
}

// Метку заголовка модель одевает по-разному, и шаблон под каждый случай не написать: важно,
// что весь текст первого блока — это метка.
func TestDropHeading1RemovesLabelInAnyWrapping(t *testing.T) {
	cases := map[string]string{
		`<p>H1 - Тема статьи</p><p>Лид статьи.</p>`:                                                                 "<p>Лид статьи.</p>",
		`<p class="ds-markdown-paragraph"><span class="">H1 - Тема статьи</span></p>` + "\n" + `<p>Лид статьи.</p>`: "<p>Лид статьи.</p>",
		`<!-- HTML-код статьи -->` + "\n" + `<p><strong>H1 — Тема статьи</strong></p><p>Лид статьи.</p>`:            "<p>Лид статьи.</p>",
		`<div><span>H1: Тема статьи</span></div><p>Лид статьи.</p>`:                                                 "<p>Лид статьи.</p>",
	}
	for markup, want := range cases {
		if got := DropHeading1(markup); got != want {
			t.Errorf("метка не снята:\n вход:  %s\n выход: %s\n ждали: %s", markup, got, want)
		}
	}
}

// Первый блок со связным текстом трогать нельзя, даже если в нём встречается «H1».
func TestDropHeading1KeepsRealParagraph(t *testing.T) {
	markup := `<p>Разметку начинают с H1 - его ставит блог.</p><p>Второй абзац.</p>`
	if got := DropHeading1(markup); got != markup {
		t.Fatalf("правило зацепило обычный абзац: %s", got)
	}
}

// Лид — первый абзац после заголовка, а не сам заголовок. Длинный H1 сходил за абзац, и
// восстановление вставляло в разметку заголовок вместо лида.
func TestRestoreLeadTakesParagraphNotHeading(t *testing.T) {
	page := "H1 - Что дает повышение квалификации по рабочей профессии в 2026 году\n\n" +
		"Повышение квалификации подтверждает разряд и открывает доступ к более сложным работам на производстве.\n\n" +
		"H2 - Кому это нужно"
	markup := "<h2>Кому это нужно</h2><p>Текст раздела.</p>"

	restored := RestoreLead(page, markup)
	if strings.Contains(restored, "H1 -") {
		t.Fatalf("в разметку вставлен заголовок вместо лида: %s", restored)
	}
	if !strings.HasPrefix(restored, "<p>Повышение квалификации подтверждает") {
		t.Fatalf("лид не встал первым абзацем: %s", restored)
	}
}

// И проверка «лид на месте» не должна засчитывать заголовок.
func TestLeadKeptIgnoresHeading(t *testing.T) {
	page := "H1 - Что дает повышение квалификации по рабочей профессии в 2026 году\n\n" +
		"Повышение квалификации подтверждает разряд и открывает доступ к более сложным работам.\n"
	onlyHeading := "<p>H1 - Что дает повышение квалификации по рабочей профессии в 2026 году</p><h2>Раздел</h2>"
	if LeadKept(page, onlyHeading) {
		t.Fatal("заголовок засчитан за лид")
	}
}

// Служебные пометки модели и пустые теги в записи блога не нужны: комментарии видны в коде
// страницы, пустой абзац рисует лишний отступ. Промпт это запрещает, но в статье 17 пришло
// пятнадцать комментариев — значит держать обязан код.
func TestCleanBlogMarkupDropsCommentsAndEmptyTags(t *testing.T) {
	markup := `<!-- Блок D. Таблица --><p>текст</p><p></p><div><p>  </p></div><h2></h2>` +
		`<table><tr><td></td><td>значение</td></tr></table>`
	cleaned := CleanBlogMarkup(markup)

	for _, unwanted := range []string{"<!--", "<p></p>", "<h2></h2>"} {
		if strings.Contains(cleaned, unwanted) {
			t.Fatalf("в разметке остался %q: %s", unwanted, cleaned)
		}
	}
	if strings.Contains(cleaned, "<div>") {
		t.Fatalf("обёртка, опустевшая после снятия абзаца, осталась: %s", cleaned)
	}
	// Пустая ячейка таблицы остаётся: без неё у соседних строк разъедутся столбцы.
	if !strings.Contains(cleaned, "<td></td>") {
		t.Fatalf("пустая ячейка таблицы удалена, столбцы разъедутся: %s", cleaned)
	}
	if !strings.Contains(cleaned, "<p>текст</p>") {
		t.Fatalf("текст статьи пострадал: %s", cleaned)
	}
}

// Источник — единственная внешняя ссылка статьи, и без неё цифрам нечем подтвердиться.
// Стадия разметки узнаёт только строку, начинающуюся с «Источник:», а модель приписывает его
// хвостом к абзацу — так в статье 17 не осталось ни одной ссылки.
func TestLinkSourcesBuildsNofollowLinks(t *testing.T) {
	markup := `<p>Вывод: Москва платит выше. Источник: hh.ru, SuperJob, 2026 г.</p>` +
		`<p>Источник: <a href="https://rosstat.gov.ru/" rel="nofollow noopener">rosstat.gov.ru</a></p>`
	linked := LinkSources(markup)

	if !strings.Contains(linked, `<a href="https://hh.ru/" rel="nofollow noindex noopener" target="_blank">hh.ru</a>`) {
		t.Fatalf("домен в хвосте абзаца не стал ссылкой: %s", linked)
	}
	if strings.Count(linked, "rosstat.gov.ru</a>") != 1 {
		t.Fatalf("готовая ссылка обёрнута повторно: %s", linked)
	}
	if strings.Contains(linked, "SuperJob</a>") {
		t.Fatalf("название без домена принято за адрес: %s", linked)
	}
}

// Строку плашек стадия разметки теряет молча: в статье 17 заметки и таблицы она сверстала,
// а ряд плашек выбросила вместе со строкой. Значения известны — блок собирает код.
func TestRestoreStatsRebuildsLostRow(t *testing.T) {
	page := "H1 - Заголовок\n\nЛид статьи.\n\nПЛАШКИ: от 320 ч | объём обучения ;; 2–4 мес. | срок ;; 2–6 разряд | уровень\n\nH2 - Раздел"
	markup := "<p>Лид статьи.</p>\n<h2>Раздел</h2>"

	restored := RestoreStats(page, markup)
	if strings.Count(restored, "flex:1 1 150px") != 3 {
		t.Fatalf("собрано не три плашки: %s", restored)
	}
	if !strings.Contains(restored, "от 320 ч") || !strings.Contains(restored, "объём обучения") {
		t.Fatalf("значение или подпись потеряны: %s", restored)
	}
	if strings.Index(restored, "flex:1 1 150px") < strings.Index(restored, "<h2>") {
		return
	}
	t.Fatalf("ряд плашек стоит после первого заголовка, а не сразу за лидом: %s", restored)
}

// Лид вставляет код, и жирное начертание из текста статьи он обязан перевести в теги: без
// этого в блог уходит «<p>**Сантехник** — это специалист…» со звёздочками.
func TestRestoreLeadConvertsBold(t *testing.T) {
	page := "H1 - Заголовок\n\n**Сантехник** — это специалист, который отвечает за монтаж и ремонт систем <водоснабжения> и отопления в зданиях.\n\nH2 - Раздел"
	restored := RestoreLead(page, "<h2>Раздел</h2>")

	if !strings.Contains(restored, "<strong>Сантехник</strong>") {
		t.Fatalf("жирное не переведено в тег: %s", restored)
	}
	if strings.Contains(restored, "**") {
		t.Fatalf("звёздочки ушли в разметку: %s", restored)
	}
	if !strings.Contains(restored, "&lt;водоснабжения&gt;") {
		t.Fatalf("угловые скобки из текста не экранированы: %s", restored)
	}
}

// Ряд плашек, свёрстанный моделью, повторно не собирается. Признак ищется по стилю с любыми
// пробелами: код пишет «flex:1 1 150px», модель — «flex: 1 1 150px», и дословное сравнение
// пропустило её блок мимо — в статье 17 плашки встали дважды.
func TestRestoreStatsSkipsExistingRow(t *testing.T) {
	page := "H1 - Заголовок\n\nЛид.\n\nПЛАШКИ: от 320 ч | объём ;; 2–4 мес. | срок ;; 2–6 разряд | уровень"
	markup := `<p>Лид.</p><div style="display: flex;"><div style="flex: 1 1 150px; background: #f5f7fa;">` +
		`<span>от 320 ч</span><span>объём</span></div></div>`

	if got := RestoreStats(page, markup); got != markup {
		t.Fatalf("ряд плашек собран поверх готового: %s", got)
	}
}

// Картинка ставится в середину статьи и перед заголовком, а не внутри раздела: так она не
// разрывает мысль и не прилипает к лиду. Заголовок выбирается ближайший к середине по длине
// разметки — разделы у нас неравные, и третий из девяти может оказаться в первой четверти.
func TestInsertBeforeMiddleHeadingPicksMiddleSection(t *testing.T) {
	markup := "<h2>Первый</h2><p>" + strings.Repeat("текст ", 40) + "</p>" +
		"<h2>Второй</h2><p>" + strings.Repeat("текст ", 40) + "</p>" +
		"<h2>Третий</h2><p>конец</p>"
	block := `<img src="a.webp" />`

	got := InsertBeforeMiddleHeading(markup, block)
	if strings.Count(got, block) != 1 {
		t.Fatalf("блок вставлен не один раз: %s", got)
	}
	before, _, _ := strings.Cut(got, block)
	if !strings.Contains(before, "<h2>Первый</h2>") || strings.Contains(before, "<h2>Третий</h2>") {
		t.Fatalf("блок встал не в середине: %s", before)
	}
	if !strings.HasSuffix(strings.TrimSpace(before), "</p>") {
		t.Fatalf("блок встал не после абзаца: %s", before)
	}
}

// Разметка без заголовков — законный случай: блок уходит в конец, потерять его хуже.
func TestInsertBeforeMiddleHeadingWithoutHeadings(t *testing.T) {
	got := InsertBeforeMiddleHeading("<p>только абзац</p>", `<img src="a.webp" />`)
	if !strings.HasSuffix(got, `<img src="a.webp" />`) {
		t.Fatalf("блок потерян или встал не в конец: %s", got)
	}
}
