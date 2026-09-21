package articleaudit

import (
	"strings"
	"testing"
)

// Имена полей блока вопросов у площадок разные. Ошибка здесь не видна глазами: проверка
// объявит блок пустым там, где он заполнен, — поэтому схема приходит из профиля.
func TestCountFAQReadsBlogFields(t *testing.T) {
	fields := map[string]string{
		"blog_faq":            "6",
		"blog_faq_0_question": "Кто такой оператор КОС?",
		"blog_faq_0_answer":   "Специалист очистных сооружений.",
		"blog_faq_1_question": "Где учиться?",
		"blog_faq_2_question": "   ",
	}
	blog := FAQScheme{Question: "blog_faq_%d_question", Answer: "blog_faq_%d_answer", Min: 6}

	check := CountFAQ(fields, blog)
	if check.Count != 2 {
		t.Fatalf("вопросов насчитано %d, ожидалось 2", check.Count)
	}
	if check.Enough() {
		t.Fatal("два вопроса при минимуме шесть — это нехватка")
	}
	// Схема услуг тех же полей не знает и не должна их находить.
	if got := CountFAQ(fields, FAQScheme{}); got.Count != 0 {
		t.Fatalf("схема услуг нашла в блоговых полях %d вопросов", got.Count)
	}
	if text := FormatFAQ(fields, blog); !strings.Contains(text, "Специалист очистных сооружений.") {
		t.Fatalf("ответ не попал в блок для промпта:\n%s", text)
	}
}

// Без нормы число вопросов остаётся числом: сколько их должно быть, у такой задачи никто не
// решал. Так живут услуги.
func TestFAQWithoutMinimumIsNeverAFinding(t *testing.T) {
	check := CountFAQ(map[string]string{}, FAQScheme{})

	if check.Enabled() || !check.Enough() {
		t.Fatalf("пустой блок без нормы объявлен находкой: %+v", check)
	}
	report := BuildReport(Article{}, Post{}, FieldCheck{FAQ: check}, Answer{}, nil)
	if report.FAQ != "блока нет" {
		t.Fatalf("шапка отчёта без нормы: %q", report.FAQ)
	}
}

// Нехватка вопросов — находка кода, и человек ищет её там же, где незаполненные поля.
func TestReportListsMissingFAQ(t *testing.T) {
	check := FieldCheck{FAQ: FAQCheck{Count: 4, Min: 6}}

	report := BuildReport(Article{}, Post{}, check, Answer{}, nil)

	if !strings.Contains(report.Issues, "вопросов в блоке FAQ 4 при минимуме 6") {
		t.Fatalf("нехватки вопросов нет в списке ошибок:\n%s", report.Issues)
	}
	if !strings.Contains(report.FAQ, "не хватает 2") {
		t.Fatalf("шапка отчёта молчит о нехватке: %q", report.FAQ)
	}
}

// Рубрика и метки — разные графы, и пустая метка не должна прятаться за заполненной рубрикой.
// Имя таксономии рубрики у каждого типа записи своё, поэтому ею считается любой термин,
// кроме меток.
func TestCheckRequiredSeparatesCategoryAndTags(t *testing.T) {
	post := Post{TermIDs: map[string][]int64{"category": {2575}}}

	check := CheckRequired(post, []string{RecordCategory, RecordTags})

	if len(check.Empty) != 1 || check.Empty[0] != RecordTags {
		t.Fatalf("пустыми названы %v, ожидались только метки", check.Empty)
	}
	// Услуга лежит в своей таксономии — рубрика у неё тоже заполнена.
	service := Post{TermIDs: map[string][]int64{"cat_rabprof": {18}, "post_tag": {7}}}
	if got := CheckRequired(service, []string{RecordCategory, RecordTags}); len(got.Empty) != 0 {
		t.Fatalf("у услуги пустыми названы %v", got.Empty)
	}
	// Одни метки без рубрики — наоборот.
	tagsOnly := Post{TermIDs: map[string][]int64{"post_tag": {7}}}
	if got := CheckRequired(tagsOnly, []string{RecordCategory}); len(got.Empty) != 1 {
		t.Fatalf("запись без рубрики прошла проверку: %+v", got)
	}
}

// Ярлык называет графу так, как её видит человек в админке: «поле author_link» заставляет
// вспоминать, что это и где оно.
func TestLabelsNameNewFields(t *testing.T) {
	for field, want := range map[string]string{
		RecordTags:        "метки записи",
		"blog_tldr":       "краткое содержание",
		"blog_read":       "время чтения",
		"author_link":     "автор статьи",
		"related_courses": "связанные курсы",
	} {
		if got := Label(field); !strings.HasPrefix(got, want) {
			t.Fatalf("%s назван %q, ожидалось начало %q", field, got, want)
		}
	}
}

// Графы самой записи сводка пересчитать не может: в артефактах лежат поля, а рубрика, метки,
// название и обложка живут в записи. Названные пустыми, они попали бы в сводку находкой у
// каждой страницы — как это и случилось с метками.
func TestSummarySkipsRecordGraphs(t *testing.T) {
	required := []string{RecordCategory, RecordTags, RecordTitle, RecordThumbnail, "blog_read"}

	got := fieldNames(required)

	if len(got) != 1 || got[0] != "blog_read" {
		t.Fatalf("сводка пересчитывает %v, ожидалось только поле записи", got)
	}
}
