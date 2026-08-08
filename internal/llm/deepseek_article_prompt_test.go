package llm

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/config"
)

// В DeepSeek-only режиме стадия article раньше полагалась на историю одного чата, и структура
// в промпт не попадала. Тест держит явную подстановку: реальный overlay-шаблон рендерится
// теми же полями, что передаёт пайплайн.
func TestDeepSeekArticlePromptContainsGeneratedStructure(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("GEMINI_MODEL", "gemini-2.5-flash")

	cfg, err := config.LoadLLMConfigWithOverlay("config/config.yaml", "config/config.deepseek.yaml", true)
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(cfg, map[string]Client{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	const structure = "H1 - Профессия логопеда\nH2 - Чем занимается логопед"
	prepared, err := router.Prepare(Call{Stage: "article", Data: struct {
		Title              string
		Keywords           string
		LSIWords           string
		GeneratedStructure string
	}{
		Title: "Профессия логопеда", Keywords: "логопед — 1000",
		LSIWords: "дефектолог", GeneratedStructure: structure,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prepared.Prompt, structure) {
		t.Fatalf("промпт стадии article не содержит сгенерированную структуру:\n%s", prepared.Prompt)
	}
	if strings.Contains(prepared.Prompt, "<no value>") {
		t.Fatalf("промпт стадии article содержит незаполненные поля:\n%s", prepared.Prompt)
	}
}
