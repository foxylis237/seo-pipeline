package articleaudit

import (
	"testing"
)

// Поля, которого нет в записи, и поля пустого — разные болезни, и в отчёте они разведены.
// Первое означает, что поле не заведено; второе — что его забыли заполнить.
func TestCheckRequiredSeparatesMissingFromEmpty(t *testing.T) {
	fields := map[string]string{
		"prof_name":  "Логопед",
		"docs_title": "",
		"price_now":  "   \t ",
		"prof_title": "Курсы [blue]логопеда[/blue]",
	}
	check := CheckRequired(Post{Fields: fields}, []string{"prof_name", "prof_title", "docs_title", "price_now", "training_program"})
	if len(check.Missing) != 1 || check.Missing[0] != "training_program" {
		t.Fatalf("незаведённые поля разобраны как %v", check.Missing)
	}
	// Пробелы — это пусто: в админке такое поле выглядит заполненным, а на странице не даёт
	// ничего.
	if len(check.Empty) != 2 || check.Empty[0] != "docs_title" || check.Empty[1] != "price_now" {
		t.Fatalf("пустые поля разобраны как %v", check.Empty)
	}
	if check.RequiredProblems() != 3 || check.OK() {
		t.Fatalf("итог проверки: %d, ok=%v", check.RequiredProblems(), check.OK())
	}
}

// Пустой список обязательных полей означает «проверка выключена» — рабочее состояние, пока
// набор не назван человеком. Придумывать его за него нельзя.
func TestCheckRequiredWithoutListFindsNothing(t *testing.T) {
	check := CheckRequired(Post{Fields: map[string]string{"prof_name": ""}}, nil)
	if !check.OK() || check.RequiredProblems() != 0 {
		t.Fatalf("выключенная проверка что-то нашла: %+v", check)
	}
}

// Вопросы в блоке считаются по заполненным вопросам, а не по счётчику faq_loop: счётчик пишет
// админка, и он переживает вычищенный вопрос — тогда в блоке пусто, а в счётчике шесть.
func TestCountFAQCountsFilledQuestions(t *testing.T) {
	fields := map[string]string{
		"faq_loop":                "6",
		"faq_loop_0_faq_question": "Какое образование нужно?",
		"faq_loop_0_faq_answer":   "Среднее профессиональное.",
		"faq_loop_1_faq_question": "Вносится ли диплом в ФИС ФРДО?",
		"faq_loop_2_faq_question": "   ",
		"faq_items":               "",
		"prof_name":               "Косметолог",
	}
	if got := CountFAQ(fields, FAQScheme{}); got.Count != 2 {
		t.Fatalf("вопросов насчитано %d, ожидалось 2", got.Count)
	}
	if got := CountFAQ(map[string]string{"faq_loop": "6"}, FAQScheme{}); got.Count != 0 {
		t.Fatalf("счётчик без вопросов дал %d", got.Count)
	}
}
