package generation

import "testing"

func TestNormalizeAndValidateHTMLAcceptsChatWrapping(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		want        string
		wantCleanup string
	}{
		{
			name:  "markdown-обёртка целиком",
			value: "```html\n<h1>Тема</h1>\n<p>Текст</p>\n```",
			want:  "<h1>Тема</h1>\n<p>Текст</p>",
		},
		{
			name:  "пояснение перед разметкой",
			value: "Вот готовая HTML-версия статьи:\n\n<h1>Тема</h1>\n<p>Текст</p>",
			want:  "<h1>Тема</h1>\n<p>Текст</p>",
		},
		{
			name:  "пояснение и Markdown-блок вместе",
			value: "Вот готовая HTML-версия:\n```html\n<h1>Тема</h1>\n<p>Текст</p>\n```\nГотово!",
			want:  "<h1>Тема</h1>\n<p>Текст</p>",
		},
		{
			name:  "блок без указания языка",
			value: "```\n<h1>Тема</h1>\n<p>Текст</p>\n```",
			want:  "<h1>Тема</h1>\n<p>Текст</p>",
		},
		{
			name:  "чистая разметка не меняется",
			value: "<h1>Тема</h1>\n<p>Текст</p>",
			want:  "<h1>Тема</h1>\n<p>Текст</p>",
		},
		{
			name:        "незакрытая Markdown-обёртка",
			value:       "```html\n<h1>Тема</h1>\n<p>Текст</p>",
			want:        "<h1>Тема</h1>\n<p>Текст</p>",
			wantCleanup: htmlCleanupStrippedMarkers,
		},
		{
			name:        "чужой язык обёртки",
			value:       "```python\n<h1>Тема</h1>\n<p>Текст</p>\n```",
			want:        "<h1>Тема</h1>\n<p>Текст</p>",
			wantCleanup: htmlCleanupForeignLanguage,
		},
		{
			name:        "лишний маркер внутри разметки",
			value:       "```html\n<h1>Тема</h1>\n```\n<p>Текст</p>",
			want:        "<h1>Тема</h1>\n<p>Текст</p>",
			wantCleanup: htmlCleanupStrippedMarkers,
		},
		{
			name:        "одинокий хвостовой маркер",
			value:       "<h1>Тема</h1>\n<p>Текст</p>\n```",
			want:        "<h1>Тема</h1>\n<p>Текст</p>",
			wantCleanup: htmlCleanupStrippedMarkers,
		},
		{
			name:  "закрывающий маркер в конце строки разметки",
			value: "```html\n<h1>Тема</h1>\n<p>Текст</p>```",
			want:  "<h1>Тема</h1>\n<p>Текст</p>",
		},
		{
			name:        "разметка после закрывающего маркера сохраняется",
			value:       "```html\n<h1>Тема</h1>\n```\n<p>Хвост</p>\n<p>Ещё</p>",
			want:        "<h1>Тема</h1>\n<p>Хвост</p>\n<p>Ещё</p>",
			wantCleanup: htmlCleanupStrippedMarkers,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, cleanup, err := normalizeAndValidateHTML(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("HTML = %q, want %q", got, test.want)
			}
			if cleanup.Kind != test.wantCleanup {
				t.Fatalf("вид очистки = %q, want %q", cleanup.Kind, test.wantCleanup)
			}
			if cleanup.Applied() && cleanup.SizeAfter > cleanup.SizeBefore {
				t.Fatalf("после очистки размер вырос: %d -> %d", cleanup.SizeBefore, cleanup.SizeAfter)
			}
		})
	}
}

func TestNormalizeAndValidateHTMLKeepsRealFailures(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "пустой ответ", value: ""},
		{name: "только пробелы", value: "   \n  "},
		{name: "отказ модели без разметки", value: "Не могу выполнить этот запрос."},
		{name: "текст со знаком меньше, но без тегов", value: "Результат: 5 < 10, разметки нет"},
		{name: "нет заголовка и абзаца", value: "<div>текст</div>"},
		{name: "отказ модели в незакрытой обёртке", value: "```html\nНе могу выполнить этот запрос."},
		{name: "пустая Markdown-обёртка", value: "```html\n```"},
		{name: "пояснение без разметки после очистки", value: "Вот HTML-версия статьи: скоро пришлю"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, _, err := normalizeAndValidateHTML(test.value)
			if err == nil {
				t.Fatalf("ошибка не возвращена, HTML = %q", got)
			}
		})
	}
}
