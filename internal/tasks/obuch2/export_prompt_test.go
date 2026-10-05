package obuch2

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// Выгружаемый промпт — шаблон владельца с данными страницы: структура одними заголовками,
// факты только из заполненных колонок книги.
func TestRenderExportPromptFillsOwnerTemplate(t *testing.T) {
	input := article.GenerationInput{
		Article:  article.Article{Title: "Сварщик 3 разряда"},
		LSIWords: []string{"электрод", "шов"},
		Hours:    "72 часа",
		Price:    "от 5 000 ₽",
	}
	structure := "H1: Сварщик 3 разряда\nЛИД: Сварщик — это…\nH2: Кому подойдёт\nСТРОКА: Новичкам"
	prompt, err := RenderExportPrompt(filepath.Join(projectRoot, filepath.FromSlash(ExportPromptPath)), input, structure)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"Тема страницы:\nСварщик 3 разряда\n", "электрод\nшов",
		"Структура от человека:\nH1 - Сварщик 3 разряда\nH2 - Кому подойдёт\nФакты",
		"Объём программы: 72 часа\nСтоимость: от 5 000 ₽\n"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{"ЛИД:", "СТРОКА:", "Срок обучения:", "[ТЕМА]", "<no value>"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt contains %q", unwanted)
		}
	}
}
