package pprof2

import (
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

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
