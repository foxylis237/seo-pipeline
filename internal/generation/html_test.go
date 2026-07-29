package generation

import "testing"

func TestNormalizeAndValidateHTML(t *testing.T) {
	got, err := normalizeAndValidateHTML("```html\n<h1>Тема</h1>\n<p>Текст</p>\n```")
	if err != nil {
		t.Fatal(err)
	}
	if got != "<h1>Тема</h1>\n<p>Текст</p>" {
		t.Fatalf("HTML = %q", got)
	}
	for _, invalid := range []string{"", "готовый HTML: <p>текст</p>", "<div>текст</div>", "```html\n<p>текст</p>"} {
		if _, err := normalizeAndValidateHTML(invalid); err == nil {
			t.Fatalf("normalizeAndValidateHTML(%q) error = nil", invalid)
		}
	}
}
