package validator

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidateRules(t *testing.T) {
	valid := validArticle()
	tests := []struct {
		name  string
		input Input
		check string
	}{
		{"valid fixture", Input{Article: valid, ExpectedStructure: "H1 - Тема\nH2 - Основной раздел"}, ""},
		{"empty article", Input{}, "empty_text"},
		{"two H1", Input{Article: valid + "\nH1 - Второй заголовок\nСодержимое раздела достаточно длинное."}, "h1_count"},
		{"H4 before H3", Input{Article: "H1 - Тема\nТекст раздела достаточно длинный.\nH2 - Раздел\nТекст раздела достаточно длинный.\nH4 - Рано\nТекст раздела достаточно длинный."}, "heading_hierarchy"},
		{"empty section", Input{Article: "H1 - Тема\nH2 - Пустой\nH2 - Следующий\nСодержимое следующего раздела."}, "heading_content"},
		{"HTML", Input{Article: valid + "\n<div>текст</div>"}, "html"},
		{"Markdown heading", Input{Article: valid + "\n## Заголовок"}, "markdown"},
		{"external link", Input{Article: valid + "\nПодробнее: https://example.com"}, "external_link"},
		{"forbidden phrase", Input{Article: valid + "\nВажно понимать, что это запрещённая фраза."}, "forbidden_phrase"},
		{"missing expected heading", Input{Article: valid, ExpectedStructure: "H2 - Основной раздел\nH3 - Потерянный подраздел"}, "expected_structure"},
		{"wrong structure order", Input{Article: articleWithSections("Второй", "Первый"), ExpectedStructure: "H2 - Первый\nH2 - Второй"}, "structure_order"},
		{"required FAQ missing", Input{Article: valid, RequireFAQ: true}, "faq"},
		{"FAQ two questions", Input{Article: articleWithFAQ(2), RequireFAQ: true}, "faq_count"},
		{"FAQ seven questions", Input{Article: articleWithFAQ(7), RequireFAQ: true}, "faq_count"},
		{"required table missing", Input{Article: valid, RequireTable: true}, "table"},
		{"unclosed bold", Input{Article: valid + "\n**Незакрытое выделение"}, "bold_format"},
		{"word spam", Input{Article: valid + "\n" + strings.Repeat("профессия ", 30)}, "word_repetition"},
		{"more than 25 percent keywords", Input{Article: valid + "\nключодин ключдва ключтри", Keywords: []string{"ключодин", "ключдва", "ключтри", "ключчетыре", "ключпять", "ключшесть", "ключсемь", "ключвосемь"}}, "keyword_share"},
		{"keyword more than three times", Input{Article: valid + "\nточный ключ точный ключ точный ключ точный ключ", Keywords: []string{"точный ключ", "другой один", "другой два", "другой три", "другой четыре", "другой пять", "другой шесть", "другой семь", "другой восемь", "другой девять"}}, "keyword_repetition"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := Validate(test.input)
			if test.check == "" {
				if ResultStatus(report) == StatusInvalid {
					t.Fatalf("valid fixture is invalid: %+v", report.Issues)
				}
				return
			}
			if !hasIssue(report, test.check) {
				t.Fatalf("issue %q not found: %+v", test.check, report.Issues)
			}
		})
	}
}

func TestUnicodeCharactersAndCompletionMarker(t *testing.T) {
	report := Validate(Input{Article: "Привет[[ARTICLE_COMPLETE]]"})
	if report.Characters != 6 {
		t.Fatalf("Characters = %d, want 6", report.Characters)
	}
	if report.Words != 1 {
		t.Fatalf("Words = %d, want 1", report.Words)
	}
}

func TestResultStatus(t *testing.T) {
	tests := []struct {
		name   string
		report Report
		want   Status
	}{
		{"valid", Report{}, StatusValid},
		{"needs review", Report{Issues: []Issue{{Severity: SeverityWarning}}}, StatusNeedsReview},
		{"invalid", Report{Issues: []Issue{{Severity: SeverityWarning}, {Severity: SeverityError}}}, StatusInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ResultStatus(test.report); got != test.want {
				t.Fatalf("status = %s, want %s", got, test.want)
			}
		})
	}
}

func TestFormatReportDoesNotContainArticle(t *testing.T) {
	report := Report{Characters: 9000, Words: 1200, Sentences: 90}
	formatted := FormatReport(7, "38", report)
	for _, expected := range []string{"ARTICLE VALIDATION REPORT", "article_id=7 external_id=38", "Статус: VALID", "НЕ ПРОВЕРЯЕТСЯ АВТОМАТИЧЕСКИ"} {
		if !strings.Contains(formatted, expected) {
			t.Fatalf("report missing %q", expected)
		}
	}
}

func validArticle() string {
	var b strings.Builder
	b.WriteString("H1 - Тема\nВводный практический пример содержит полезное объяснение для читателя.\n\nH2 - Основной раздел\n")
	for i := 0; i < 70; i++ {
		if i%10 == 0 {
			fmt.Fprintf(&b, "Короткий пример номер%d полезен.\n\n", i)
			continue
		}
		fmt.Fprintf(&b, "Уникальный термин%d помогает читателю последовательно разобрать конкретную практическую ситуацию без лишних вводных конструкций и повторения материала.\n\n", i)
	}
	b.WriteString("- Практический пункт завершает содержательную часть материала.\n")
	return b.String()
}

func articleWithSections(titles ...string) string {
	var b strings.Builder
	b.WriteString("H1 - Тема\nСодержательное введение для проверки порядка разделов.\n")
	for _, title := range titles {
		fmt.Fprintf(&b, "H2 - %s\nСодержательное описание этого раздела с практическим примером.\n", title)
	}
	b.WriteString(strings.Repeat("Дополнительный уникальный материал раскрывает тему подробно и последовательно. ", 120))
	return b.String()
}

func articleWithFAQ(count int) string {
	base := validArticle() + "\nH2 - Частые вопросы\nВ этом разделе собраны практические ответы.\n"
	var b strings.Builder
	b.WriteString(base)
	for i := 1; i <= count; i++ {
		fmt.Fprintf(&b, "H3 - Как решить вопрос номер %d?\nЭто содержательный ответ длиной больше двадцати символов.\n", i)
	}
	return b.String()
}

func hasIssue(report Report, check string) bool {
	for _, issue := range report.Issues {
		if issue.Check == check {
			return true
		}
	}
	return false
}
