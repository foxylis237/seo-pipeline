package generation

import "testing"

func TestNormalizeAndValidateHTMLAcceptsChatWrapping(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeAndValidateHTML(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("HTML = %q, want %q", got, test.want)
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
		{name: "незакрытая Markdown-обёртка", value: "```html\n<p>текст</p>"},
		{name: "чужой язык обёртки", value: "```python\n<p>текст</p>\n```"},
		{name: "пояснение без разметки после очистки", value: "Вот HTML-версия статьи: скоро пришлю"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeAndValidateHTML(test.value)
			if err == nil {
				t.Fatalf("ошибка не возвращена, HTML = %q", got)
			}
		})
	}
}
