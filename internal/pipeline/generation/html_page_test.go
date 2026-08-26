package generation

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Страница, на которой проверяется покрытие. Последний абзац длинный намеренно: короткие
// строки приметой конца страницы не служат.
const coveragePage = `H1: Патологическая анатомия

Курс даёт практикующему врачу системные знания для морфологической диагностики материала.

H2: Кто проводит обучение

Теоретическую и практическую части ведёт практикующий специалист с педагогическим опытом.

H2: Подайте заявку

Для уточнения деталей программы, дат практики и условий зачисления оставьте заявку на сайте.`

// Обрыв ответа выглядит исправным HTML: теги закрыты, заголовок и абзац на месте. Отличить
// его от целой страницы можно только по самой странице — этим и занимается проверка.
func TestValidateHTMLCoversPage(t *testing.T) {
	full := `<p>Курс даёт практикующему врачу системные знания для морфологической диагностики материала.</p>` +
		`<h2>Кто проводит обучение</h2>` +
		`<p>Теоретическую и практическую части ведёт практикующий специалист с педагогическим опытом.</p>` +
		`<h2>Подайте заявку</h2>` +
		`<p>Для уточнения деталей программы, дат практики и условий зачисления оставьте заявку на сайте.</p>`
	if err := ValidateHTMLCoversPage(coveragePage, full); err != nil {
		t.Fatalf("целая страница признана оборванной: %v", err)
	}

	cut := `<p>Курс даёт практикующему врачу системные знания для морфологической диагностики материала.</p>` +
		`<h2>Кто проводит обучение</h2>` +
		`<p>Теоретическую и практическую части ведёт практикующий специалист с педагогическим опытом.</p>`
	err := ValidateHTMLCoversPage(coveragePage, cut)
	if !errors.Is(err, ErrHTMLIncomplete) {
		t.Fatalf("обрыв не опознан: %v", err)
	}

	// Вёрстка вправе разбить абзац, сменить типографику и убрать пунктуацию — это не обрыв.
	reflowed := strings.ReplaceAll(full, "Для уточнения деталей программы, дат практики",
		`<p>Для&nbsp;уточнения деталей программы — дат практики`)
	if err := ValidateHTMLCoversPage(coveragePage, reflowed); err != nil {
		t.Fatalf("переверстанный конец страницы признан обрывом: %v", err)
	}
}

// Страница без единого длинного абзаца сверяться не с чем: выдумывать отказ на пустом месте
// нельзя, иначе стадия падала бы на коротких страницах вместо обрыва.
func TestValidateHTMLCoversPageWithoutProbe(t *testing.T) {
	if err := ValidateHTMLCoversPage("H1: Тема\n\nКоротко.", "<h1>Тема</h1>"); err != nil {
		t.Fatalf("страница без длинных абзацев признана оборванной: %v", err)
	}
}

func TestTrimIncompleteHTML(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "обрыв на середине тега", value: "<p>Первый</p>\n<p class=\"", want: "<p>Первый</p>"},
		{name: "обрыв на середине текста", value: "<p>Первый</p>\n<p>Второй, недопис", want: "<p>Первый</p>"},
		{name: "элемент дописан", value: "<p>Первый</p>\n<div><p>Второй</p></div>", want: "<p>Первый</p>\n<div><p>Второй</p></div>"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := trimIncompleteHTML(test.value); got != test.want {
				t.Fatalf("обрезано до %q, ожидалось %q", got, test.want)
			}
		})
	}
}

// Модель нередко начинает продолжение с уже выданного хвоста: повтор снимается, иначе он
// уходит в блог дважды.
func TestJoinHTMLPartsDropsOverlap(t *testing.T) {
	accepted := "<h2>Кто проводит обучение</h2>\n<p>Теоретическую часть ведёт практикующий специалист.</p>"
	part := "<p>Теоретическую часть ведёт практикующий специалист.</p>\n<p>Дальше идёт заявка.</p>"
	joined, err := joinHTMLParts(coveragePage, accepted, part)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(joined, "Теоретическую часть ведёт практикующий специалист") != 1 {
		t.Fatalf("повтор на стыке не снят: %q", joined)
	}
	if !strings.Contains(joined, "Дальше идёт заявка") {
		t.Fatalf("продолжение потеряно: %q", joined)
	}
}

// Продолжение, начатое с начала страницы, склеивать нельзя: страница вышла бы двойной, а
// проверка покрытия такую склейку пропустила бы — конец в ней есть.
func TestJoinHTMLPartsRejectsRestart(t *testing.T) {
	accepted := "<p>Курс даёт практикующему врачу системные знания для морфологической диагностики материала.</p>"
	part := "<p>Курс даёт практикующему врачу системные знания для морфологической диагностики материала.</p>" +
		"<p>Для уточнения деталей программы, дат практики и условий зачисления оставьте заявку на сайте.</p>"
	if _, err := joinHTMLParts(coveragePage, accepted, part); !errors.Is(err, ErrHTMLIncomplete) {
		t.Fatalf("продолжение с начала страницы принято: %v", err)
	}
}

// Хвост страницы вправе оказаться одним закрывающим блоком: требовать от продолжения
// заголовок или абзац значит выбросить почти собранную страницу.
func TestNormalizeHTMLPartAcceptsTailWithoutContent(t *testing.T) {
	got, err := NormalizeHTMLPart("Продолжаю:\n```html\n</div>\n```")
	if err != nil {
		t.Fatalf("хвост страницы отвергнут: %v", err)
	}
	if got != "</div>" {
		t.Fatalf("продолжение очищено до %q", got)
	}
	if _, err := NormalizeHTMLPart("Страница уже закончена."); err == nil {
		t.Fatal("ответ без разметки принят за продолжение")
	}
}

// Главный сценарий: первый ответ оборвался, продолжение дописало страницу, склейка ушла
// дальше как целая разметка.
func TestBuildHTMLPageCompletesCutAnswer(t *testing.T) {
	cut := "<p>Курс даёт практикующему врачу системные знания для морфологической диагностики материала.</p>\n" +
		"<h2>Кто проводит обучение</h2>\n<p>Теоретическую и практическую части ведёт практикующий специалист с педагогическим опытом.</p>\n" +
		"<p class=\""
	tail := "<h2>Подайте заявку</h2>\n<p>Для уточнения деталей программы, дат практики и условий зачисления оставьте заявку на сайте.</p>"

	var prompts []string
	page, err := BuildHTMLPage(context.Background(), HTMLPageRequest{
		Page:   coveragePage,
		Prompt: "промпт html",
		Send: func(_ context.Context, prompt string) (string, error) {
			prompts = append(prompts, prompt)
			return cut, nil
		},
		Continue: func(_ context.Context, prompt string) (string, error) {
			prompts = append(prompts, prompt)
			return tail, nil
		},
	})
	if err != nil {
		t.Fatalf("страница не собрана: %v", err)
	}
	if len(prompts) != 2 {
		t.Fatalf("сообщений в чате %d, ожидалось два", len(prompts))
	}
	// Место обрыва называется явно: без него модель продолжает по памяти, а память чата
	// после потерянного стрима может не совпасть с тем, что дошло до нас.
	if !strings.Contains(prompts[1], "педагогическим опытом") {
		t.Fatalf("продолжение не получило место обрыва: %q", prompts[1])
	}
	if strings.Contains(page, "<p class=\"") {
		t.Fatalf("недописанный элемент попал в страницу: %q", page)
	}
	if err := ValidateHTMLCoversPage(coveragePage, page); err != nil {
		t.Fatalf("собранная страница всё ещё оборвана: %v", err)
	}
}

// Целый ответ продолжений не требует: лишнее сообщение стоит денег и времени.
func TestBuildHTMLPageDoesNotContinueWhenAnswerIsWhole(t *testing.T) {
	whole := "<p>Курс даёт практикующему врачу системные знания для морфологической диагностики материала.</p>" +
		"<p>Для уточнения деталей программы, дат практики и условий зачисления оставьте заявку на сайте.</p>"
	continued := false
	if _, err := BuildHTMLPage(context.Background(), HTMLPageRequest{
		Page:   coveragePage,
		Prompt: "промпт html",
		Send:   func(context.Context, string) (string, error) { return whole, nil },
		Continue: func(context.Context, string) (string, error) {
			continued = true
			return "", nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if continued {
		t.Fatal("у целого ответа запрошено продолжение")
	}
}

// Продолжения не бесконечны: модель, которая не дописывает страницу, обязана уронить стадию
// с понятной причиной, а не отдать половину страницы в блог.
func TestBuildHTMLPageFailsAfterContinuations(t *testing.T) {
	cut := "<p>Курс даёт практикующему врачу системные знания для морфологической диагностики материала.</p>"
	messages := 0
	_, err := BuildHTMLPage(context.Background(), HTMLPageRequest{
		Page:   coveragePage,
		Prompt: "промпт html",
		Send: func(context.Context, string) (string, error) {
			messages++
			return cut, nil
		},
		Continue: func(context.Context, string) (string, error) {
			messages++
			return "<h2>Кто проводит обучение</h2>", nil
		},
	})
	if !errors.Is(err, ErrHTMLIncomplete) {
		t.Fatalf("оборванная страница сохранена: %v", err)
	}
	if messages != htmlContinuations+1 {
		t.Fatalf("сообщений в чате %d, ожидалось %d", messages, htmlContinuations+1)
	}
	if len(HTMLChatStages("html")) != messages {
		t.Fatalf("чат разметки открыт на %d сообщений, а поток шлёт %d", len(HTMLChatStages("html")), messages)
	}
}
