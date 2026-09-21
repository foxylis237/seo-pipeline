package articleaudit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAudited кладёт на диск артефакты уже проверенной страницы и возвращает её строку базы.
func writeAudited(t *testing.T, root, externalID, slug, answer string, fields map[string]string) Article {
	t.Helper()
	directory := filepath.Join(root, DirectoryName(externalID, slug))
	for _, folder := range []string{OriginalFolder, GeneratedFolder} {
		if err := os.MkdirAll(filepath.Join(directory, folder), 0o755); err != nil {
			t.Fatalf("подготовить каталог: %v", err)
		}
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("подготовить поля: %v", err)
	}
	fieldsPath := filepath.Join(DirectoryName(externalID, slug), OriginalFolder, OriginalFieldsFile)
	auditPath := filepath.Join(DirectoryName(externalID, slug), GeneratedFolder, AuditFile)
	if err := os.WriteFile(filepath.Join(root, fieldsPath), encoded, 0o644); err != nil {
		t.Fatalf("подготовить поля: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, auditPath), []byte(answer), 0o644); err != nil {
		t.Fatalf("подготовить ответ: %v", err)
	}
	checked := articleTime()
	score, scoreMax := 12, 20
	return Article{
		ExternalID: externalID, Slug: slug, Topic: slug, SourceURL: "https://dpoprof.ru/" + slug,
		FieldsPath: fieldsPath, AuditPath: auditPath,
		Score: &score, ScoreMax: &scoreMax, CheckedAt: &checked,
	}
}

const summaryAnswer = `Итоговая оценка: 12/20

1. Критические ошибки
Ошибка: указание зарплат 60–90 тыс. — выдуманные цифры без источника
Почему это проблема: вводит читателя в заблуждение

2. Все найденные ошибки списком
— Противоречие по объёму часов: 150 ак. ч. против 72 ак. ч.
— Абзацы превышают 5 строк, стена текста
— Блок «Как проходит обучение» дублирует содержание`

// Сводка отвечает на вопрос, которого не видно в отчёте одной страницы: что ломается у всей
// пачки. Поэтому находки в ней сведены по темам, а не по формулировкам — формулировку модель
// каждый раз строит заново.
func TestBuildSummaryGroupsIssuesByTheme(t *testing.T) {
	root := t.TempDir()
	fields := map[string]string{"prof_name": "Сварщик", "faq_loop_0_faq_question": "Сколько учиться?"}
	articles := []Article{
		writeAudited(t, root, "1", "svarshhik-2", summaryAnswer, fields),
		writeAudited(t, root, "2", "svarshhik-3", summaryAnswer, fields),
	}
	summary := BuildSummary(articles, NewArtifacts(root), Options{Required: []string{"prof_name", "docs_title"}})

	if len(summary.Pages) != 2 {
		t.Fatalf("страниц в сводке %d, ожидалось 2", len(summary.Pages))
	}
	themes := map[string]int{}
	for _, issue := range summary.CommonIssues {
		themes[issue.Issue] = len(issue.Pages)
	}
	for _, want := range []string{
		"выдуманные цифры: зарплаты, сроки, число выпускников",
		"противоречие в параметрах программы (часы, сроки, цена, документ)",
		"стена текста: длинные абзацы",
		"повтор и дублирование блоков",
	} {
		if themes[want] != 2 {
			t.Fatalf("тема %q встретилась на %d страницах, ожидалось 2. Все темы: %v", want, themes[want], themes)
		}
	}
}

// Находка на одной странице частой ошибкой не считается: она уже лежит в отчёте этой страницы.
func TestBuildSummarySkipsSingleOccurrence(t *testing.T) {
	root := t.TempDir()
	articles := []Article{writeAudited(t, root, "1", "svarshhik-2", summaryAnswer, map[string]string{})}
	summary := BuildSummary(articles, NewArtifacts(root), Options{})
	if len(summary.CommonIssues) != 0 {
		t.Fatalf("одиночные находки попали в частые: %v", summary.CommonIssues)
	}
}

// Пропущенное обязательное поле сводка пересчитывает по сохранённым полям — не по тому, что
// было на момент прогона. Так список обязательного можно менять и пересобирать сводку заново.
func TestBuildSummaryRecountsMissingFields(t *testing.T) {
	root := t.TempDir()
	articles := []Article{
		writeAudited(t, root, "1", "svarshhik-2", summaryAnswer, map[string]string{"prof_name": "Сварщик"}),
		writeAudited(t, root, "2", "svarshhik-3", summaryAnswer, map[string]string{"prof_name": ""}),
	}
	summary := BuildSummary(articles, NewArtifacts(root), Options{Required: []string{"prof_name", "image_alt"}})

	gaps := map[string][]string{}
	for _, gap := range summary.MissingFields {
		gaps[gap.Field] = gap.Pages
	}
	if len(gaps["image_alt"]) != 2 {
		t.Fatalf("подпись обложки не заполнена на %v, ожидались обе страницы", gaps["image_alt"])
	}
	if len(gaps["prof_name"]) != 1 || gaps["prof_name"][0] != "2" {
		t.Fatalf("пустое prof_name найдено на %v, ожидалась вторая страница", gaps["prof_name"])
	}
}

// «Упало» и «ещё не проверено» — разные новости, и путать их нельзя: первое требует разбора,
// второе означает, что прогон просто не дошёл.
func TestBuildSummarySeparatesFailedFromPending(t *testing.T) {
	summary := BuildSummary([]Article{
		{ExternalID: "1", Slug: "svarshhik-2", ErrorMessage: "в WordPress нет записи"},
		{ExternalID: "2", Slug: "svarshhik-3"},
	}, NewArtifacts(t.TempDir()), Options{})

	if len(summary.Failed) != 1 || summary.Failed[0].ExternalID != "1" {
		t.Fatalf("упавшие разобраны как %+v", summary.Failed)
	}
	if len(summary.Pending) != 1 || summary.Pending[0].ExternalID != "2" {
		t.Fatalf("непроверенные разобраны как %+v", summary.Pending)
	}
	text := summary.Render("pprof_audit_2")
	if !strings.Contains(text, "упало: 1") || !strings.Contains(text, "ещё не проверено: 1") {
		t.Fatalf("сводка не различает упавшие и непроверенные:\n%s", text)
	}
}

// Худшие страницы стоят первыми: сводку читают, чтобы решить, за какую браться.
func TestSummarySortsWorstFirst(t *testing.T) {
	root := t.TempDir()
	low, high := 8, 18
	first := writeAudited(t, root, "1", "svarshhik-2", summaryAnswer, map[string]string{})
	first.Score = &high
	second := writeAudited(t, root, "2", "svarshhik-3", summaryAnswer, map[string]string{})
	second.Score = &low
	third := writeAudited(t, root, "3", "svarshhik-4", summaryAnswer, map[string]string{})
	third.Score = nil

	summary := BuildSummary([]Article{first, second, third}, NewArtifacts(root), Options{})
	order := []string{summary.Pages[0].ExternalID, summary.Pages[1].ExternalID, summary.Pages[2].ExternalID}
	// Неразобранная оценка идёт первой: её надо посмотреть глазами.
	if order[0] != "3" || order[1] != "2" || order[2] != "1" {
		t.Fatalf("порядок страниц %v, ожидался 3, 2, 1", order)
	}
}
