package pprof1

import (
	"strings"
	"testing"
)

func TestDropFAQSection(t *testing.T) {
	markup := "<h2>Обязанности</h2>\n<p>Текст.</p>\n" +
		"<h2><strong>Часто задаваемые вопросы о профессии</strong></h2>\n<p><strong>Сколько учиться?</strong> Месяц.</p>\n" +
		"<h2>Запишитесь на обучение в ДПО ПРОФ</h2>\n<p>Итог.</p>"
	got, dropped := dropFAQSection(markup)
	if !dropped || strings.Contains(got, "Сколько учиться") || strings.Contains(got, "Часто задаваемые") {
		t.Fatalf("FAQ не вырезан: %s", got)
	}
	for _, keep := range []string{"Обязанности", "Текст.", "Запишитесь на обучение", "Итог."} {
		if !strings.Contains(got, keep) {
			t.Fatalf("вырезано лишнее, нет %q: %s", keep, got)
		}
	}
}

func TestDropFAQSectionKeepsOrdinaryQuestions(t *testing.T) {
	markup := "<h2>Вопросы на собеседовании монтажника</h2>\n<p>Текст.</p>"
	if _, dropped := dropFAQSection(markup); dropped {
		t.Fatal("обычный раздел про вопросы принят за FAQ")
	}
}
