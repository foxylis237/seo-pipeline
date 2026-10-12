package obuch2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

func TestProfile22PathsExist(t *testing.T) {
	profile := Profile22()
	for _, path := range []string{
		profile.InputDir, profile.PromptsDir, profile.TemplatePath, profile.LLMConfigPath, ExportPrompt22Path,
	} {
		if _, err := os.Stat(filepath.Join(projectRoot, filepath.FromSlash(path))); err != nil {
			t.Fatalf("путь профиля %s недоступен: %v", path, err)
		}
	}
	if profile.DBSchema != Name22 || profile.EnvPrefix != Profile().EnvPrefix {
		t.Fatalf("схема %q, префикс %q", profile.DBSchema, profile.EnvPrefix)
	}
}

func TestProfile22PromptsHaveNoArsenkinData(t *testing.T) {
	for _, path := range []string{
		"tasks/obuch_22/prompts/structure.txt", "tasks/obuch_22/prompts/article.txt", ExportPrompt22Path,
	} {
		raw, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"{{.LSIWords}}", "LSI", "частотност", "конкурент"} {
			if strings.Contains(string(raw), banned) {
				t.Fatalf("%s содержит %q", path, banned)
			}
		}
	}
}

func TestFormatKeywordsWithoutFrequencies(t *testing.T) {
	got := formatKeywords([]article.KeywordFrequency{{Query: "сварщик обучение"}, {Query: "курсы сварщика"}})
	if got != "сварщик обучение\nкурсы сварщика" {
		t.Fatalf("got %q", got)
	}
	got = formatKeywords([]article.KeywordFrequency{{Query: "a", Frequency: 5}, {Query: "b"}})
	if got != "a\t5\nb\t0" {
		t.Fatalf("got %q", got)
	}
}
