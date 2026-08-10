package llm

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/config"
)

// Стадия keywords рендерится теми же полями, что передаёт резервный источник запросов.
// Несовпадение имён даёт «<no value>» в проде, а не ошибку сборки, поэтому проверяется
// реальный файл промпта, а не его копия в тесте.
func TestKeywordsPromptRendersArticleName(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("GEMINI_API_KEY", "test-gemini")
	t.Setenv("GEMINI_MODEL", "gemini-2.5-flash")

	for name, path := range map[string]func() (config.LLMConfig, error){
		"схема Gemini": func() (config.LLMConfig, error) {
			return config.LoadLLMConfig("config/config.yaml")
		},
		"схема DeepSeek-only": func() (config.LLMConfig, error) {
			return config.LoadLLMConfigWithOverlay("config/config.yaml", "config/config.deepseek.yaml", true)
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := path()
			if err != nil {
				t.Fatal(err)
			}
			router := NewRouter(cfg, map[string]Client{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

			const articleName = "Смена профессии в 40 лет: пошаговый план"
			prepared, err := router.Prepare(Call{Stage: "keywords", Data: struct {
				ArticleName string
			}{ArticleName: articleName}})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(prepared.Prompt, articleName) {
				t.Fatalf("промпт стадии keywords не содержит название статьи:\n%s", prepared.Prompt)
			}
			if strings.Contains(prepared.Prompt, "<no value>") {
				t.Fatalf("промпт стадии keywords содержит незаполненные поля:\n%s", prepared.Prompt)
			}
			// Формат ответа — договор между промптом и разбором: одна фраза в строке, только
			// буквы, цифры и пробелы. Если требование пропадёт из текста, разбор начнёт
			// молча отбраковывать строки.
			for _, requirement := range []string{
				"Один запрос — одна строка.",
				"только буквы, цифры и пробелы",
			} {
				if !strings.Contains(prepared.Prompt, requirement) {
					t.Fatalf("промпт стадии keywords не задаёт формат ответа (%q):\n%s", requirement, prepared.Prompt)
				}
			}
			// Частотность из резервного подбора убрана: её единственный источник — Wordstat.
			if strings.Contains(prepared.Prompt, "частотност") {
				t.Fatalf("промпт стадии keywords снова просит частотность:\n%s", prepared.Prompt)
			}
		})
	}
}
