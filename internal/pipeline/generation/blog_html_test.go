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
