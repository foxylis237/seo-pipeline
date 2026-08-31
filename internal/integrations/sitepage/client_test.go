package sitepage

import "testing"

// H1 — то, что видит человек на странице, поэтому он и есть название программы.
func TestPageNamePrefersHeading(t *testing.T) {
	markup := `<html><head><title>Дистанционное обучение на Горнорабочего (ФИС ФРДО)</title></head>` +
		`<body><h1 class="page-title"><span>Дистанционное обучение Горнорабочий подземный</span></h1></body></html>`
	if got := PageName(markup); got != "Дистанционное обучение Горнорабочий подземный" {
		t.Fatalf("название взято не из H1: %q", got)
	}
}

// H1 рисуется темой из поля ACF и в разметке есть не всегда — тогда остаётся title, а
// хвостовая пометка в скобках к названию программы не относится.
func TestPageNameFallsBackToTitleWithoutTail(t *testing.T) {
	markup := `<html><head><title>Дистанционная переподготовка на Горнорабочего (ФИС ФРДО)</title></head><body></body></html>`
	if got := PageName(markup); got != "Дистанционная переподготовка на Горнорабочего" {
		t.Fatalf("название из title разобрано неверно: %q", got)
	}
}

// Приписка о внесении в реестр — часть SEO-заголовка, а не название программы: внутри
// предложения статьи она читается тяжело.
func TestPageNameDropsRegistryTail(t *testing.T) {
	for markup, want := range map[string]string{
		`<h1>Горнорабочий - дистанционная переподготовка с внесением в ФРДО</h1>`:                  "Горнорабочий - дистанционная переподготовка",
		`<h1>Горнорабочий — дистанционное повышение квалификации с внесением в ФИС ФРДО</h1>`:      "Горнорабочий — дистанционное повышение квалификации",
		`<html><head><title>Дистанционное обучение на Сигналиста (ФИС ФРДО)</title></head></html>`: "Дистанционное обучение на Сигналиста",
	} {
		if got := PageName(markup); got != want {
			t.Errorf("хвост снят неверно: %q, ожидалось %q", got, want)
		}
	}
}

func TestPageNameUnescapesEntities(t *testing.T) {
	if got := PageName(`<h1>Стропальщик &amp; сигналист</h1>`); got != "Стропальщик & сигналист" {
		t.Fatalf("HTML-сущности не раскрыты: %q", got)
	}
}

func TestPageNameEmptyWhenNothingToTake(t *testing.T) {
	if got := PageName(`<html><body><p>Страница без заголовка</p></body></html>`); got != "" {
		t.Fatalf("название выдумано на пустом месте: %q", got)
	}
}
