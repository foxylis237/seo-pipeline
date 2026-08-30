package pproffix1

import (
	"errors"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/generation"
)

const originalArticle = `<p>Вводный абзац статьи про обучение медсестёр и их работу.</p>
<h2>Кому подходит программа</h2><p>Специалистам со средним медицинским образованием.</p>
<h2>Как проходит обучение</h2><p>Дистанционно, в удобном темпе, с итоговой аттестацией.</p>
<h2>Какой документ выдаётся</h2><p>Удостоверение о повышении квалификации и сведения в ФИС ФРДО.</p>`

// Правка вправе переписать хвост статьи: общая проверка покрытия здесь дала бы ложный обрыв,
// поэтому признак свой — структура.
func TestValidateRewriteCoversAcceptsRewrittenTail(t *testing.T) {
	rewritten := strings.Replace(originalArticle,
		"Удостоверение о повышении квалификации и сведения в ФИС ФРДО.",
		"Диплом об обучении. Сведения передаются в ФИС ФРДО в установленном порядке.", 1)
	if err := ValidateRewriteCovers(originalArticle, rewritten); err != nil {
		t.Fatalf("ValidateRewriteCovers вернул обрыв на переписанном хвосте: %v", err)
	}
}

// Обрыв ответа съедает конец статьи вместе с последним заголовком — это и ловится.
func TestValidateRewriteCoversDetectsTruncation(t *testing.T) {
	truncated := originalArticle[:strings.Index(originalArticle, "<h2>Какой документ")]
	err := ValidateRewriteCovers(originalArticle, truncated)
	if err == nil {
		t.Fatal("обрыв не замечен")
	}
	if !errors.Is(err, generation.ErrHTMLIncomplete) {
		t.Fatalf("ошибка %v не оборачивает ErrHTMLIncomplete — продолжение чата не начнётся", err)
	}
}

// Статья без заголовков проверяется по объёму: обрыв в самом начале ответа виден и так.
func TestValidateRewriteCoversDetectsShortAnswerWithoutHeadings(t *testing.T) {
	original := "<p>" + strings.Repeat("длинный текст статьи ", 50) + "</p>"
	err := ValidateRewriteCovers(original, "<p>длинный текст статьи</p>")
	if err == nil || !errors.Is(err, generation.ErrHTMLIncomplete) {
		t.Fatalf("короткий ответ не опознан как обрыв: %v", err)
	}
}
