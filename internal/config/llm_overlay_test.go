package config

import (
	"path/filepath"
	"testing"
	"time"
)

func projectRootDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDeepSeekOverlaySwitchesEveryStageToOneProvider(t *testing.T) {
	t.Chdir(projectRootDir(t))
	// Ключ Gemini намеренно не задаётся: в DeepSeek-only режиме Gemini не используется
	// ни одной стадией, поэтому и требовать его учётные данные нельзя.
	t.Setenv("GEMINI_MODEL", "gemini-2.5-flash")

	cfg, err := LoadLLMConfigWithOverlay("config/config.yaml", "config/config.deepseek.yaml", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, stageName := range requiredLLMStages {
		stage, found := cfg.Stages[stageName]
		if !found {
			t.Fatalf("стадия %q отсутствует", stageName)
		}
		if len(stage.Targets) != 1 || stage.Targets[0].Provider != "deepseek_web" {
			t.Fatalf("стадия %q: targets = %+v, ожидался единственный deepseek_web", stageName, stage.Targets)
		}
		if stage.PromptTemplate == "" {
			t.Fatalf("стадия %q: шаблон промпта не загружен", stageName)
		}
		// keywords не участвует в диалоге статьи: она выполняется в prepare, отдельным
		// запросом и до первой стадии генерации. Своей DeepSeek-версии промпта у неё
		// поэтому нет и быть не должно — второй копии одного текста не заводим.
		if stageName == "keywords" {
			if stage.Prompt != "tasks/task_1/prompts/keywords.txt" {
				t.Fatalf("стадия keywords использует промпт %q вместо общего", stage.Prompt)
			}
			continue
		}
		if want := "tasks/task_1/prompts/deepseek/"; len(stage.Prompt) < len(want) || stage.Prompt[:len(want)] != want {
			t.Fatalf("стадия %q использует промпт %q вместо DeepSeek-версии", stageName, stage.Prompt)
		}
	}
	if !cfg.Providers["deepseek_web"].SingleChatPerArticle {
		t.Fatal("overlay не включил single_chat_per_article")
	}
}

func TestDeepSeekOverlayKeepsBaseTimeoutsAndLimits(t *testing.T) {
	t.Chdir(projectRootDir(t))
	t.Setenv("GEMINI_API_KEY", "test-gemini")
	t.Setenv("GEMINI_MODEL", "gemini-2.5-flash")

	base, err := LoadLLMConfig("config/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	overlaid, err := LoadLLMConfigWithOverlay("config/config.yaml", "config/config.deepseek.yaml", true)
	if err != nil {
		t.Fatal(err)
	}
	// Overlay задаёт провайдера, промпт и собственные таймауты: в одном чате каждая
	// следующая стадия читает растущий диалог и работает дольше. Всё остальное обязано
	// прийти из базы, иначе появятся две копии конфигурации, которые разъедутся.
	for _, stageName := range requiredLLMStages {
		if overlaid.Stages[stageName].Timeout <= base.Stages[stageName].Timeout &&
			overlaid.Stages[stageName].Timeout < 3*time.Minute {
			t.Fatalf("стадия %q: timeout = %v, для одного чата он должен быть увеличен",
				stageName, overlaid.Stages[stageName].Timeout)
		}
		if got, want := overlaid.Stages[stageName].MaxTokens, base.Stages[stageName].MaxTokens; got != want {
			t.Fatalf("стадия %q: max_tokens = %d, want %d из базового файла", stageName, got, want)
		}
		if got, want := *overlaid.Stages[stageName].Temperature, *base.Stages[stageName].Temperature; got != want {
			t.Fatalf("стадия %q: temperature = %v, want %v из базового файла", stageName, got, want)
		}
	}
}

func TestBaseConfigKeepsSingleChatDisabled(t *testing.T) {
	t.Chdir(projectRootDir(t))
	t.Setenv("GEMINI_API_KEY", "test-gemini")
	t.Setenv("GEMINI_MODEL", "gemini-2.5-flash")

	cfg, err := LoadLLMConfig("config/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["deepseek_web"].SingleChatPerArticle {
		t.Fatal("режим Gemini не должен включать один чат на статью")
	}
}

func TestMergeKeepsBaseFieldsWhenOverlayIsEmpty(t *testing.T) {
	temperature := 0.7
	base := LLMConfig{
		Providers: map[string]LLMProviderConfig{"deepseek_web": {Type: "deepseek_web", ProfileDir: "data/browser/deepseek"}},
		Stages: map[string]LLMStageConfig{
			"structure": {
				Targets:     []LLMTargetConfig{{Provider: "gemini", Model: "base-model"}},
				Prompt:      "base.txt",
				Temperature: &temperature,
				MaxTokens:   777,
				TimeoutText: "42s",
			},
		},
	}
	mergeLLMConfig(&base, LLMConfig{
		Providers: map[string]LLMProviderConfig{"deepseek_web": {SingleChatPerArticle: true}},
		Stages:    map[string]LLMStageConfig{"structure": {Prompt: "overlay.txt"}},
	})

	stage := base.Stages["structure"]
	if stage.Prompt != "overlay.txt" {
		t.Fatalf("prompt = %q, want overlay.txt", stage.Prompt)
	}
	if len(stage.Targets) != 1 || stage.Targets[0].Model != "base-model" {
		t.Fatalf("targets перезаписаны пустым overlay: %+v", stage.Targets)
	}
	if stage.MaxTokens != 777 || stage.TimeoutText != "42s" || *stage.Temperature != 0.7 {
		t.Fatalf("stage потерял базовые значения: %+v", stage)
	}
	provider := base.Providers["deepseek_web"]
	if !provider.SingleChatPerArticle {
		t.Fatal("флаг overlay не применился")
	}
	if provider.ProfileDir != "data/browser/deepseek" || provider.Type != "deepseek_web" {
		t.Fatalf("provider потерял базовые поля: %+v", provider)
	}
}

func TestMergeAddsStageMissingInBase(t *testing.T) {
	base := LLMConfig{Providers: map[string]LLMProviderConfig{}, Stages: map[string]LLMStageConfig{}}
	mergeLLMConfig(&base, LLMConfig{
		Providers: map[string]LLMProviderConfig{"new": {Type: "gemini"}},
		Stages:    map[string]LLMStageConfig{"new": {Prompt: "new.txt", TimeoutText: "9s"}},
	})
	if base.Stages["new"].Prompt != "new.txt" || base.Stages["new"].TimeoutText != "9s" {
		t.Fatalf("новая стадия не добавлена: %+v", base.Stages)
	}
	if base.Providers["new"].Type != "gemini" {
		t.Fatalf("новый провайдер не добавлен: %+v", base.Providers)
	}
}

func TestOverlayFailsOnMissingFile(t *testing.T) {
	t.Chdir(projectRootDir(t))
	if _, err := LoadLLMConfigWithOverlay("config/config.yaml", "config/нет-такого.yaml", true); err == nil {
		t.Fatal("отсутствующий overlay не вызвал ошибку")
	}
}

func TestBaseTimeoutsAreRealDurations(t *testing.T) {
	t.Chdir(projectRootDir(t))
	t.Setenv("GEMINI_API_KEY", "test-gemini")
	t.Setenv("GEMINI_MODEL", "gemini-2.5-flash")
	cfg, err := LoadLLMConfig("config/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, stageName := range requiredLLMStages {
		if cfg.Stages[stageName].Timeout < time.Second {
			t.Fatalf("стадия %q: подозрительный timeout %v", stageName, cfg.Stages[stageName].Timeout)
		}
	}
}
