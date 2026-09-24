package pprof2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	texttemplate "text/template"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// Промпт ревью правит написанную страницу, поэтому её текст обязан в него попасть. Набор
// плейсхолдеров шаблона и набор полей проверяются на настоящем файле: расхождение даёт не
// ошибку, а «<no value>» — то есть ревью без страницы, уже после оплаченного article.
func TestReviewPromptGetsThePage(t *testing.T) {
	template, err := os.ReadFile(filepath.Join("..", "..", "..", "tasks", "pprof_2", "prompts", "review.txt"))
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := texttemplate.New("review").Option("missingkey=error").Parse(string(template))
	if err != nil {
		t.Fatalf("промпт ревью не разобран: %v", err)
	}
	var rendered strings.Builder
	page := "H1 - Обучение на стропальщика\n\nтекст страницы"
	if err := prompt.Execute(&rendered, reviewData(article.GenerationInput{}, page)); err != nil {
		t.Fatalf("промпт ревью не собрался своими полями: %v", err)
	}
	if !strings.Contains(rendered.String(), page) {
		t.Fatal("страница не попала в промпт ревью")
	}
	if strings.Contains(rendered.String(), "<no value>") {
		t.Fatalf("в промпте ревью осталось незаполненное поле:\n%s", rendered.String())
	}
}

// Промпт запрещает выдумывать факты, поэтому пустое поле не должно попадать в него вовсе:
// строка «Профессия: » — это приглашение её заполнить.
func TestServiceFactsSkipsEmptyFields(t *testing.T) {
	facts := ServiceFacts(article.ResultInput{
		Section:  "Профессиональное обучение",
		Category: "",
		Header:   "Обучение на стропальщика",
	})
	if strings.Contains(facts, "Категория") {
		t.Fatalf("пустое поле попало в промпт:\n%s", facts)
	}
	for _, want := range []string{"Раздел каталога: Профессиональное обучение", "Заголовок H1"} {
		if !strings.Contains(facts, want) {
			t.Fatalf("в промпте нет %q:\n%s", want, facts)
		}
	}
}

// Пустой раздел промпта модель читает как приглашение придумать факты, поэтому вместо него
// уходит прямой запрет.
func TestServiceFactsForbidsInventionWhenNothingIsKnown(t *testing.T) {
	facts := ServiceFacts(article.ResultInput{})
	if strings.TrimSpace(facts) == "" {
		t.Fatal("раздел фактов пуст: модель дополнит его сама")
	}
	if !strings.Contains(facts, "Не придумывай") {
		t.Fatalf("нет запрета выдумывать факты: %q", facts)
	}
}

// Порядок полей фиксирован: промпт — вход модели, и одни и те же данные обязаны давать один
// и тот же текст.
func TestServiceFactsOrderIsStable(t *testing.T) {
	input := article.ResultInput{
		Section: "раздел", Category: "категория", Profession: "профессия",
		Teachers: "преподаватели", Header: "H1", MetaDescription: "мета", Keyword: "ключ",
	}
	first := ServiceFacts(input)
	for range 5 {
		if ServiceFacts(input) != first {
			t.Fatal("порядок полей в промпте меняется от вызова к вызову")
		}
	}
	wantOrder := []string{"Раздел каталога", "Категория", "Профессия", "Преподаватели",
		"Заголовок H1", "Мета-описание", "Фокусное ключевое слово"}
	position := 0
	for _, want := range wantOrder {
		index := strings.Index(first[position:], want)
		if index < 0 {
			t.Fatalf("поле %q не на своём месте:\n%s", want, first)
		}
		position += index
	}
}

// Числа программы обязаны доехать до промпта дословно: те же пять значений человек ставит в
// поля записи руками, и пересчитанное моделью число разошлось бы с админкой молча.
//
// Проверяется настоящий файл промпта: расхождение набора полей с набором плейсхолдеров даёт
// не ошибку, а «<no value>» — страницу, написанную без объёма, срока и цены, уже после
// оплаченной стадии structure.
func TestArticlePromptGetsProgramNumbersVerbatim(t *testing.T) {
	rendered := renderArticlePrompt(t, article.GenerationInput{
		Hours:       "144 часа",
		Duration:    "от 1 недели",
		Price:       "от 5 000 ₽",
		Document:    "Удостоверение о повышении квалификации",
		Attestation: "Квалификационный экзамен",
	})
	for _, want := range []string{"144 часа", "от 1 недели", "от 5 000 ₽",
		"Удостоверение о повышении квалификации", "Квалификационный экзамен"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("значение %q не попало в промпт:\n%s", want, rendered)
		}
	}
}

// Незаполненная колонка — законное состояние и прежнее поведение задачи: колонок не было
// вовсе, и живые страницы написаны по правилу вида программы. Поэтому на её месте стоит не
// пустая строка (её модель читает как приглашение придумать число), а прямая отсылка к
// правилу.
func TestArticlePromptKeepsProgramRuleWhenBookIsEmpty(t *testing.T) {
	rendered := renderArticlePrompt(t, article.GenerationInput{})
	if strings.Contains(rendered, "Объём программы: \n") {
		t.Fatalf("незаполненная колонка оставила в промпте пустое место:\n%s", rendered)
	}
	if !strings.Contains(rendered, "работай по правилу вида программы") {
		t.Fatalf("нет отсылки к правилу вида программы:\n%s", rendered)
	}
}

func renderArticlePrompt(t *testing.T, input article.GenerationInput) string {
	t.Helper()
	template, err := os.ReadFile(filepath.Join("..", "..", "..", "tasks", "pprof_2", "prompts", "article.txt"))
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := texttemplate.New("article").Option("missingkey=error").Parse(string(template))
	if err != nil {
		t.Fatalf("основной промпт не разобран: %v", err)
	}
	var rendered strings.Builder
	if err := prompt.Execute(&rendered, articleData(input, "H2 - Программа обучения")); err != nil {
		t.Fatalf("основной промпт не собрался своими полями: %v", err)
	}
	if strings.Contains(rendered.String(), "<no value>") {
		t.Fatalf("в основном промпте осталось незаполненное поле:\n%s", rendered.String())
	}
	return rendered.String()
}
