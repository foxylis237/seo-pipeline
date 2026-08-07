package deepseekweb

import (
	"strings"
	"testing"
)

func TestDiagnosticHTMLRedactsEmails(t *testing.T) {
	content := `<div class="account">foxylis237@gmail.com</div><span>user.name+tag@sub.example.co.uk</span>`
	redacted := redactDiagnosticHTML(content)
	for _, secret := range []string{"foxylis237@gmail.com", "user.name+tag@sub.example.co.uk"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("адрес %q попал в дамп: %s", secret, redacted)
		}
	}
	if strings.Count(redacted, "[redacted-email]") != 2 {
		t.Fatalf("подменены не все адреса: %s", redacted)
	}
	if !strings.Contains(redacted, `<div class="account">`) {
		t.Fatalf("разметка повреждена: %s", redacted)
	}
}

func TestDiagnosticHTMLKeepsMarkupWithoutEmails(t *testing.T) {
	content := `<div class="ds-markdown"><p>Текст ответа без адресов</p></div>`
	if got := redactDiagnosticHTML(content); got != content {
		t.Fatalf("разметка изменена без причины: %s", got)
	}
}
