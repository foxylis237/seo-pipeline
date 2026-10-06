package pprof1

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// Стадия html расставляет внутреннюю перелинковку, поэтому в промпт обязаны уйти Links —
// адреса из входных данных. Professions на их месте означали бы задание «расставь ссылки»
// со списком слов-меток вместо ссылок. Проверяется реальный файл промпта, а не его копия
// в тесте: расхождение полей шаблона и структуры не ловится сборкой.
func TestHTMLPromptCarriesInternalLinks(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	profile := Profile()
	cfg, err := config.LoadLLMConfigForStages(profile.LLMConfigPath, profile.LLMStages, false)
	if err != nil {
		t.Fatal(err)
	}
	router := llm.NewRouter(cfg, map[string]llm.Client{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	const (
		link        = "https://dpoprof.ru/obuchenie/distancionnoe-obuchenie-santehnik/"
		professions = "сантехник, слесарь, монтажник"
		finalText   = "Готовый текст статьи."
	)
	input := article.GenerationInput{Professions: professions, Links: link}

	prepared, err := router.Prepare(llm.Call{Stage: StageHTML, Data: htmlData(input, finalText, input.Links)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prepared.Prompt, link) {
		t.Fatalf("промпт стадии html не содержит ссылок перелинковки:\n%s", prepared.Prompt)
	}
	if !strings.Contains(prepared.Prompt, finalText) {
		t.Fatalf("промпт стадии html не содержит текста статьи:\n%s", prepared.Prompt)
	}
	if strings.Contains(prepared.Prompt, professions) {
		t.Fatalf("промпт стадии html содержит слова-метки вместо ссылок:\n%s", prepared.Prompt)
	}
	if strings.Contains(prepared.Prompt, "<no value>") {
		t.Fatalf("промпт стадии html содержит незаполненные поля:\n%s", prepared.Prompt)
	}
}

// Объём черновика считает код и называет редактуре, на сколько сократить.
func TestVolumeTaskAsksToCutOverLimit(t *testing.T) {
	if got := articleVolume("ab c\nd"); got != 4 {
		t.Fatalf("articleVolume = %d, want 4", got)
	}
	task := volumeTask(22000)
	if !strings.Contains(task, "22000") || !strings.Contains(task, "Подрежь её примерно на 5500") {
		t.Fatalf("задание на сокращение: %s", task)
	}
	if !strings.Contains(volumeTask(12000), "Допиши примерно 3000") {
		t.Fatal("короткой статье не задано дописать")
	}
	if !strings.Contains(volumeTask(16000), "в норме") {
		t.Fatal("статья в норме названа ненормальной")
	}
}
