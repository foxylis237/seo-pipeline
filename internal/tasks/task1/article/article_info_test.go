package article

import (
	"strings"
	"testing"
)

func TestParseArticleInfo(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    ArticleInfo
		errText string
	}{
		{
			name: "valid format",
			text: "Метки: Логопед, Переподготовка, Как стать\n\nTLDR:\nПункт.\n\nFAQ:\nВопрос: Как?\nОтвет: Так.",
			want: ArticleInfo{Tags: "Логопед, Переподготовка, Как стать", TLDR: "Пункт.", FAQ: "Вопрос: Как?\nОтвет: Так."},
		},
		{
			name: "CRLF",
			text: "Метки: Три метки\r\nTLDR:\r\nИтог.\r\nFAQ:\r\nВопрос: Что?\r\nОтвет: Это.",
			want: ArticleInfo{Tags: "Три метки", TLDR: "Итог.", FAQ: "Вопрос: Что?\nОтвет: Это."},
		},
		{
			name: "multiline TLDR",
			text: "Метки: Метки\nTLDR:\nПервый.\nВторой.\nТретий.\nFAQ:\nВопрос: Что?\nОтвет: Это.",
			want: ArticleInfo{Tags: "Метки", TLDR: "Первый.\nВторой.\nТретий.", FAQ: "Вопрос: Что?\nОтвет: Это."},
		},
		{
			name: "multiline FAQ",
			text: "Метки: Метки\nTLDR:\nИтог.\nFAQ:\nВопрос: Первый?\nОтвет: Первый.\n\nВопрос: Второй?\nОтвет: Второй.",
			want: ArticleInfo{Tags: "Метки", TLDR: "Итог.", FAQ: "Вопрос: Первый?\nОтвет: Первый.\n\nВопрос: Второй?\nОтвет: Второй."},
		},
		{
			name: "variable FAQ count and blank lines",
			text: "\nМетки:\n\nПрофессия, Вид обучения, Тема\n\n\nTLDR:\n\nКраткий итог.\n\nFAQ:\n\nВопрос: Первый?\nОтвет: Первый.\n\n\nВопрос: Второй?\nОтвет: Ответ в две\nстроки.\n\nВопрос: Третий?\nОтвет: Третий.\n",
			want: ArticleInfo{Tags: "Профессия, Вид обучения, Тема", TLDR: "Краткий итог.", FAQ: "Вопрос: Первый?\nОтвет: Первый.\n\n\nВопрос: Второй?\nОтвет: Ответ в две\nстроки.\n\nВопрос: Третий?\nОтвет: Третий."},
		},
		{
			name: "FAQ-like line inside FAQ answer",
			text: "Метки: Метки\nTLDR: Итог.\nFAQ:\nВопрос: Что означает FAQ?\nОтвет: Термин.\nFAQ: эта строка является частью ответа.",
			want: ArticleInfo{Tags: "Метки", TLDR: "Итог.", FAQ: "Вопрос: Что означает FAQ?\nОтвет: Термин.\nFAQ: эта строка является частью ответа."},
		},
		{name: "missing section", text: "Метки: Метки\nTLDR:\nИтог.", errText: "must contain the sections"},
		{name: "empty section", text: "Метки: \nTLDR:\nИтог.\nFAQ:\nВопрос: Что?", errText: "Метки is empty"},
		{name: "wrong order", text: "TLDR:\nИтог.\nМетки: Метки\nFAQ:\nВопрос: Что?", errText: "in this order"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseArticleInfo(test.text)
			if test.errText != "" {
				if err == nil || !strings.Contains(err.Error(), test.errText) {
					t.Fatalf("error = %v, want containing %q", err, test.errText)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("ParseArticleInfo() = %#v, want %#v", got, test.want)
			}
		})
	}
}
