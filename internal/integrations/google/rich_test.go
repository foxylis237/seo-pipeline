package google

import "testing"

// Размеченный промпт получает HTML с заголовками, списками и разделителем; неразмеченный
// уходит текстом, как у остальных задач.
func TestPromptHTML(t *testing.T) {
	got, ok := promptHTML("# РОЛЬ\nТы — редактор.\n* живо;\n* конкретно.\n1. Раз.\n________________\n## ПЕРВАЯ КОМАНДА\nключ\t12")
	want := "<h1>РОЛЬ</h1><p>Ты — редактор.</p><ul><li>живо;</li><li>конкретно.</li></ul><ol><li>Раз.</li></ol><hr><h2>ПЕРВАЯ КОМАНДА</h2><p>ключ — 12</p>"
	if !ok || got != want {
		t.Fatalf("promptHTML = %q, %v", got, ok)
	}
	if _, ok := promptHTML("РОЛЬ\n* пункт"); ok {
		t.Fatal("unmarked prompt got HTML")
	}
}
