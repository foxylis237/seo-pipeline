package generation

import (
	"strings"
	"testing"
)

func TestPlacedInternalLinksCountsUniqueAddresses(t *testing.T) {
	links := "https://example.test/a\nhttps://example.test/a/\nhttps://example.test/b"
	markup := `<p><a href="https://example.test/a">a</a></p>`

	if got := PlacedInternalLinks(markup, links); got != 1 {
		t.Fatalf("в тексте %d ссылок, ожидалась одна", got)
	}
}

// Промпт короткий: называет недостающие ссылки с названиями и свободные разделы, страницу не
// повторяет.
func TestRepairLinksPromptListsMissingLinksAndFreeSections(t *testing.T) {
	links := "Обучение на логопеда — https://example.test/logoped\nhttps://example.test/a"
	markup := `<h2>Занят</h2><p><a href="https://example.test/a">a</a></p><h2>Свободен</h2><p>текст</p>`

	prompt := RepairLinksPrompt(markup, links, []string{"https://example.test/logoped"})

	for _, want := range []string{"- Обучение на логопеда — https://example.test/logoped", "- Свободен", ";;"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("в промпте нет %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "- Занят") || strings.Contains(prompt, "<p>текст</p>") {
		t.Fatalf("промпт называет занятый раздел или повторяет страницу:\n%s", prompt)
	}
}

func TestParseLinkInsertsKeepsOnlyAskedLinks(t *testing.T) {
	links := "Обучение на логопеда — https://example.test/logoped\nПереподготовка дефектолога — https://example.test/defect\nhttps://example.test/other"
	missing := []string{"https://example.test/logoped", "https://example.test/defect", "https://example.test/bare"}
	answer := "Вот строки:\n```\n" +
		"1. `https://example.test/logoped/` ;; H2 - «Где учиться» ;; <p>Азы даёт <a href=\"https://example.test/logoped\">Обучение на логопеда</a>.</p>\n" +
		"https://example.test/logoped ;; Второй ;; повтор той же ссылки <a href=\"https://example.test/logoped\">x</a>.\n" +
		"https://example.test/defect ;; Карьера ;; Дальше помогает Переподготовка дефектолога.\n" +
		"https://example.test/bare ;; Карьера ;; Без анкора и без названия.\n" +
		"https://example.test/other ;; Карьера ;; <a href=\"https://example.test/other\">чужая</a>.\n```"

	inserts := ParseLinkInserts(answer, links, missing)

	if len(inserts) != 2 {
		t.Fatalf("разобрано %d вставок, ожидалось две: %+v", len(inserts), inserts)
	}
	if inserts[0].URL != "https://example.test/logoped" || inserts[0].Heading != "Где учиться" ||
		inserts[0].Sentence != `Азы даёт <a href="https://example.test/logoped">Обучение на логопеда</a>.` {
		t.Fatalf("первая вставка разобрана неверно: %+v", inserts[0])
	}
	if want := `Дальше помогает <a href="https://example.test/defect">Переподготовка дефектолога</a>.`; inserts[1].Sentence != want {
		t.Fatalf("название без тега не обёрнуто в ссылку: %q", inserts[1].Sentence)
	}
}

func TestInsertLinkSentencesAppendsToFirstParagraph(t *testing.T) {
	markup := "<p>Лид.</p>\n<h2>Где <em>учиться</em></h2>\n<p>Первый абзац.</p>\n<p>Второй.</p>\n" +
		"<h2>Документы для работы</h2>\n<p>Понадобятся:</p>\n<ul><li>диплом</li></ul>"
	inserts := []LinkInsert{
		{URL: "https://example.test/a", Heading: "где учиться", Sentence: `Про <a href="https://example.test/a">a</a>.`},
		{URL: "https://example.test/b", Heading: "Документы", Sentence: `Про <a href="https://example.test/b">b</a>.`},
		{URL: "https://example.test/c", Heading: "Нет такого", Sentence: `Про <a href="https://example.test/c">c</a>.`},
	}

	got, skipped := InsertLinkSentences(markup, inserts)

	if !strings.Contains(got, `<p>Первый абзац. Про <a href="https://example.test/a">a</a>.</p>`) {
		t.Fatalf("предложение не вписано в конец первого абзаца раздела:\n%s", got)
	}
	if !strings.Contains(got, "<h2>Документы для работы</h2>\n<p>Про <a href=\"https://example.test/b\">b</a>.</p>\n<p>Понадобятся:</p>") {
		t.Fatalf("абзац перед списком разорван или раздел не найден по вхождению:\n%s", got)
	}
	if len(skipped) != 1 || skipped[0].URL != "https://example.test/c" {
		t.Fatalf("ненайденный раздел не вернулся: %+v", skipped)
	}
}

// Задача, которая доспрашивает ссылки у модели, шаблонной строки от кода не получает, а
// сгрудившиеся ссылки код по-прежнему разводит.
func TestSpreadCrowdedLinksDoesNotWriteMissingLinks(t *testing.T) {
	links := "https://example.test/a\nhttps://example.test/b\nОбучение на логопеда — https://example.test/logoped"
	markup := `<h2>Один</h2><p>Про <a href="https://example.test/a">a</a>. Про <a href="https://example.test/b">b</a>.</p>` +
		`<h2>Два</h2><p>текст</p>`

	got, moved := SpreadCrowdedLinks(markup, links)

	if moved != 1 {
		t.Fatalf("перенесено %d ссылок, ожидалась одна:\n%s", moved, got)
	}
	if strings.Contains(got, "Подробнее о программе") || strings.Contains(got, "https://example.test/logoped") {
		t.Fatalf("код дописал недостающую ссылку:\n%s", got)
	}
}
