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
			text: "TLDR:\nПункт.\n\nFAQ:\nВопрос: Как?\nОтвет: Так.",
			want: ArticleInfo{TLDR: "Пункт.", FAQ: "Вопрос: Как?\nОтвет: Так."},
		},
		{
			name: "CRLF",
			text: "TLDR:\r\nИтог.\r\nFAQ:\r\nВопрос: Что?\r\nОтвет: Это.",
			want: ArticleInfo{TLDR: "Итог.", FAQ: "Вопрос: Что?\nОтвет: Это."},
		},
		{
			name: "multiline TLDR",
			text: "TLDR:\nПервый.\nВторой.\nТретий.\nFAQ:\nВопрос: Что?\nОтвет: Это.",
			want: ArticleInfo{TLDR: "Первый.\nВторой.\nТретий.", FAQ: "Вопрос: Что?\nОтвет: Это."},
		},
		{
			name: "multiline FAQ",
			text: "TLDR:\nИтог.\nFAQ:\nВопрос: Первый?\nОтвет: Первый.\n\nВопрос: Второй?\nОтвет: Второй.",
			want: ArticleInfo{TLDR: "Итог.", FAQ: "Вопрос: Первый?\nОтвет: Первый.\n\nВопрос: Второй?\nОтвет: Второй."},
		},
		{
			name: "variable FAQ count and blank lines",
			text: "\nTLDR:\n\nКраткий итог.\n\nFAQ:\n\nВопрос: Первый?\nОтвет: Первый.\n\n\nВопрос: Второй?\nОтвет: Ответ в две\nстроки.\n\nВопрос: Третий?\nОтвет: Третий.\n",
			want: ArticleInfo{TLDR: "Краткий итог.", FAQ: "Вопрос: Первый?\nОтвет: Первый.\n\n\nВопрос: Второй?\nОтвет: Ответ в две\nстроки.\n\nВопрос: Третий?\nОтвет: Третий."},
		},
		{
			name: "FAQ-like line inside FAQ answer",
			text: "TLDR: Итог.\nFAQ:\nВопрос: Что означает FAQ?\nОтвет: Термин.\nFAQ: эта строка является частью ответа.",
			want: ArticleInfo{TLDR: "Итог.", FAQ: "Вопрос: Что означает FAQ?\nОтвет: Термин.\nFAQ: эта строка является частью ответа."},
		},
		{
			name: "TL semicolon DR",
			text: "TL;DR:\nИтог.\nFAQ:\nВопрос: Что?\nОтвет: Это.",
			want: ArticleInfo{TLDR: "Итог.", FAQ: "Вопрос: Что?\nОтвет: Это.", FallbackUsed: true},
		},
		{
			name: "different order",
			text: "FAQ:\nВопрос: Что?\nОтвет: Это.\nTLDR:\nИтог.",
			want: ArticleInfo{TLDR: "Итог.", FAQ: "Вопрос: Что?\nОтвет: Это.", FallbackUsed: true},
		},
		{
			name: "markdown headings and case",
			text: "### faq:\nВопрос: Что?\nОтвет: Это.\n# tl;dr:\nИтог.",
			want: ArticleInfo{TLDR: "Итог.", FAQ: "Вопрос: Что?\nОтвет: Это.", FallbackUsed: true},
		},
		{
			name: "unrecognized intro text",
			text: "Вступление.\nTLDR:\nИтог.\nFAQ:\nВопрос: Что?\nОтвет: Это.",
			want: ArticleInfo{TLDR: "Итог.", FAQ: "Вопрос: Что?\nОтвет: Это.", AdditionalInfo: "Вступление.", FallbackUsed: true},
		},
		{
			name: "missing FAQ",
			text: "TLDR:\nИтог.",
			want: ArticleInfo{TLDR: "Итог.", FallbackUsed: true},
		},
		{
			name: "missing TLDR",
			text: "FAQ:\nВопрос: Что?\nОтвет: Это.",
			want: ArticleInfo{FAQ: "Вопрос: Что?\nОтвет: Это.", FallbackUsed: true},
		},
		{
			name: "one known section",
			text: "Пояснение.\n## FAQ\nВопрос: Что?\nОтвет: Это.",
			want: ArticleInfo{FAQ: "Вопрос: Что?\nОтвет: Это.", AdditionalInfo: "Пояснение.", FallbackUsed: true},
		},
		{
			name: "nothing recognized",
			text: "Модель вернула полезный текст.\nЗдесь есть рекомендации.",
			want: ArticleInfo{AdditionalInfo: "Модель вернула полезный текст.\nЗдесь есть рекомендации.", FallbackUsed: true},
		},
		{
			name: "additional markdown section",
			text: "TLDR:\nИтог.\n## Рекомендации\nСохранить этот текст.\nFAQ:\nВопрос: Что?\nОтвет: Это.",
			want: ArticleInfo{TLDR: "Итог.", FAQ: "Вопрос: Что?\nОтвет: Это.", AdditionalInfo: "## Рекомендации\nСохранить этот текст.", FallbackUsed: true},
		},
		{name: "empty", text: " \n\t", errText: "empty response"},
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

// Метки больше не часть контракта стадии info. Если модель всё-таки их вернёт, они не
// должны молча стать метаданными: заголовок не распознаётся и уходит в AdditionalInfo.
func TestParseArticleInfoDoesNotRecognizeTags(t *testing.T) {
	info, err := ParseArticleInfo("Метки: Логопед, Переподготовка\nTLDR:\nИтог.\nFAQ:\nВопрос: Что?\nОтвет: Это.")
	if err != nil {
		t.Fatal(err)
	}
	if info.TLDR != "Итог." {
		t.Fatalf("TLDR = %q", info.TLDR)
	}
	if !strings.Contains(info.AdditionalInfo, "Метки") {
		t.Fatalf("блок меток не попал в AdditionalInfo: %+v", info)
	}
}
