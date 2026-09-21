package articleaudit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pageURL = "https://dpoprof.ru/blog/professiya-svarshhik/"

// Внешний источник под таблицей заработка перелинковкой не является: его требует сам промпт,
// и засчитывать его значило бы объявлять ссылки там, где их нет.
func TestCollectInternalLinksSkipsExternal(t *testing.T) {
	markup := `<p>Учат на <a href="https://dpoprof.ru/obuchenie/svarshhik/">сварщика</a>.</p>` +
		`<p>Источник: <a href="https://hh.ru/" rel="nofollow">hh.ru</a></p>`

	check := CollectInternalLinks(markup, pageURL, 3)

	if check.Count() != 1 {
		t.Fatalf("внутренних ссылок %d, ожидалась одна: %v", check.Count(), check.URLs)
	}
	if check.Enough() {
		t.Fatal("одна ссылка при минимуме три обязана считаться нехваткой")
	}
}

// Адрес с хвостовой косой чертой и без неё — одна и та же страница, и считать их двумя
// ссылками нельзя. Относительная ссылка — тоже своя площадка.
func TestCollectInternalLinksCountsUniquePages(t *testing.T) {
	markup := `<a href="https://dpoprof.ru/obuchenie/svarshhik/">раз</a>` +
		`<a href="https://www.dpoprof.ru/obuchenie/svarshhik">два</a>` +
		`<a href="/perepodgotovka/gazosvarshhik/">три</a>` +
		`<a href='/attestacziya/naks/'>четыре</a>`

	check := CollectInternalLinks(markup, pageURL, 3)

	if check.Count() != 3 {
		t.Fatalf("уникальных ссылок %d, ожидалось три: %v", check.Count(), check.URLs)
	}
	if !check.Enough() {
		t.Fatal("три ссылки при минимуме три — норма набрана")
	}
	if check.URLs[0] != "/obuchenie/svarshhik" {
		t.Fatalf("первый адрес %q, ожидался нормализованный путь", check.URLs[0])
	}
}

// Ссылка страницы на саму себя, якорь и mailto читателя никуда не ведут.
func TestCollectInternalLinksSkipsSelfAndNonPages(t *testing.T) {
	markup := `<a href="https://dpoprof.ru/blog/professiya-svarshhik/">сюда же</a>` +
		`<a href="#faq">к вопросам</a><a href="mailto:info@dpoprof.ru">почта</a>` +
		`<a href="tel:+70000000000">телефон</a>`

	check := CollectInternalLinks(markup, pageURL, 3)

	if check.Count() != 0 {
		t.Fatalf("ссылок %d, ожидалось ноль: %v", check.Count(), check.URLs)
	}
}

// Нулевой минимум — прежнее поведение: задача перелинковку не требует, и нехватки у неё не
// бывает. Так живут услуги.
func TestCollectInternalLinksDisabled(t *testing.T) {
	check := CollectInternalLinks(`<p>текст без ссылок</p>`, pageURL, 0)

	if check.Enabled() {
		t.Fatal("нулевой минимум обязан означать выключенную проверку")
	}
	if !check.Enough() {
		t.Fatal("выключенная проверка не может быть нехваткой")
	}
}

// Нехватка ссылок — такая же ошибка страницы, как незаполненное поле, и человек ищет её в
// том же списке.
func TestReportListsMissingLinks(t *testing.T) {
	check := FieldCheck{
		Links: CollectInternalLinks(`<p>без ссылок</p>`, pageURL, 3),
	}
	report := BuildReport(
		Article{ExternalID: "7", SourceURL: pageURL},
		Post{ID: 21, Title: "Профессия сварщик", Link: pageURL},
		check, Answer{}, nil)

	if !strings.Contains(report.Issues, "внутренних ссылок в тексте 0 при минимуме 3") {
		t.Fatalf("нехватки ссылок нет в списке ошибок:\n%s", report.Issues)
	}
	if !strings.Contains(report.InternalLinks, "не хватает 3") {
		t.Fatalf("шапка отчёта молчит о ссылках: %q", report.InternalLinks)
	}
}

// Перелинковку поток считает по адресу самой страницы: ссылка на свою площадку — находка,
// внешний источник — нет. Нехватка ложится в отчёт наравне с незаполненными полями.
func TestFlowCountsInternalLinks(t *testing.T) {
	post := testPost()
	post.ContentHTML = `<h2>О профессии</h2><p>Текст страницы.</p>` +
		`<p>Учиться: <a href="https://dpoprof.ru/obuchenie/logoped/">курс логопеда</a>,` +
		` <a href="/perepodgotovka/defektolog/">дефектолога</a>.</p>` +
		`<p>Источник: <a href="https://hh.ru/">hh.ru</a></p>`
	articles := &fakeArticles{article: testArticle()}
	blog := &fakeBlog{post: post}
	chats := &fakeChats{chat: &fakeChat{answers: []string{fullAnswer}}}
	flow, root := newTestFlow(t, articles, blog, chats, nil, 3)

	if err := flow.Run(context.Background(), "2"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	report, err := os.ReadFile(filepath.Join(root, "2-logoped", ResultFile))
	if err != nil {
		t.Fatalf("result.md не собран: %v", err)
	}
	text := string(report)
	if !strings.Contains(text, "Внутренних ссылок: 2 при минимуме 3") {
		t.Fatalf("шапка отчёта считает ссылки иначе:\n%s", text)
	}
	if !strings.Contains(text, "внутренних ссылок в тексте 2 при минимуме 3") {
		t.Fatalf("нехватки ссылок нет в списке ошибок:\n%s", text)
	}
}
