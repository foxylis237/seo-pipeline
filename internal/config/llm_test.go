package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProjectLLMStageTimeouts(t *testing.T) {
	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(projectRoot)
	t.Setenv("GEMINI_API_KEY", "test-gemini")
	t.Setenv("GEMINI_MODEL", "gemini-test")
	t.Setenv("OPENROUTER_API_KEY", "test-openrouter")
	t.Setenv("OPENROUTER_MODEL", "nvidia/test-free")
	t.Setenv("GROQ_API_KEY", "test-groq")
	cfg, err := LoadLLMConfig("config/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]time.Duration{
		"structure": 2 * time.Minute,
		"article":   3 * time.Minute,
		"info":      3 * time.Minute,
		"review":    2 * time.Minute,
		"fix":       5 * time.Minute,
		"html":      3 * time.Minute,
	}
	for stage, timeout := range want {
		if cfg.Stages[stage].Timeout != timeout {
			t.Fatalf("%s timeout = %v, want %v", stage, cfg.Stages[stage].Timeout, timeout)
		}
	}
}

func TestLoadLLMConfig(t *testing.T) {
	path := writeLLMTestConfig(t, "gemini", "TEST_GEMINI_KEY", "")
	t.Setenv("TEST_GEMINI_KEY", "test-value")
	cfg, err := LoadLLMConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	stage := cfg.Stages["structure"]
	if stage.Provider != "gemini" || stage.Model != "test-model" || stage.Timeout == 0 || stage.PromptTemplate == "" {
		t.Fatalf("structure stage = %+v", stage)
	}
	if cfg.Stages["fix"].Timeout.String() != "5m0s" {
		t.Fatalf("fix timeout = %v, want 5m", cfg.Stages["fix"].Timeout)
	}
	for _, name := range []string{"structure", "article", "info", "review", "html"} {
		if cfg.Stages[name].Timeout.String() != "2m0s" {
			t.Fatalf("%s timeout = %v, want 2m", name, cfg.Stages[name].Timeout)
		}
	}
}

func TestLoadLLMConfigRejectsUnknownProvider(t *testing.T) {
	path := writeLLMTestConfig(t, "missing", "TEST_GEMINI_KEY", "")
	t.Setenv("TEST_GEMINI_KEY", "test-value")
	_, err := LoadLLMConfig(path)
	if err == nil || !strings.Contains(err.Error(), `stage "structure" target 0 references unknown provider "missing"`) || !strings.Contains(err.Error(), "available providers: gemini") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateLLMConfigSupportsConfiguredProviderTypes(t *testing.T) {
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(prompt, []byte("{{.Title}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, providerType, baseURL, wantType string
	}{
		{name: "gemini", providerType: "gemini", wantType: "gemini"},
		{name: "standard OpenAI", providerType: "openai", baseURL: "https://api.openai.com/v1", wantType: "openai_compatible"},
		{name: "custom compatible", providerType: "openai-compatible", baseURL: "https://openrouter.ai/api/v1", wantType: "openai_compatible"},
		{name: "DeepSeek Web", providerType: "deepseek-web", wantType: "deepseek_web"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := testLLMConfig(prompt, "selected", "model-a", "model-b")
			provider := LLMProviderConfig{Type: test.providerType, BaseURL: test.baseURL, APIKeyEnv: "TEST_KEY"}
			if test.wantType == "deepseek_web" {
				provider.APIKeyEnv = ""
			}
			cfg.Providers = map[string]LLMProviderConfig{"selected": provider}
			if err := validateLLMConfig(&cfg, false); err != nil {
				t.Fatal(err)
			}
			if cfg.Providers["selected"].Type != test.wantType {
				t.Fatalf("type = %q", cfg.Providers["selected"].Type)
			}
			if test.wantType == "deepseek_web" && cfg.Providers["selected"].ProfileDir != "data/browser/deepseek" {
				t.Fatalf("profile_dir = %q", cfg.Providers["selected"].ProfileDir)
			}
			if cfg.Stages["structure"].Model != "model-a" || cfg.Stages["review"].Model != "model-b" {
				t.Fatalf("stage models were not independent: structure=%q review=%q", cfg.Stages["structure"].Model, cfg.Stages["review"].Model)
			}
		})
	}
}

func TestValidateLLMConfigRejectsUnknownProviderTypeWithAllowedTypes(t *testing.T) {
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(prompt, []byte("prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testLLMConfig(prompt, "selected", "model-a", "model-b")
	cfg.Providers = map[string]LLMProviderConfig{"selected": {Type: "openrouter", APIKeyEnv: "KEY"}}
	err := validateLLMConfig(&cfg, false)
	if err == nil || !strings.Contains(err.Error(), "supported types: gemini, openai_compatible, deepseek_web") {
		t.Fatalf("error = %v", err)
	}
}

func testLLMConfig(prompt, provider, structureModel, otherModel string) LLMConfig {
	temperature := 0.3
	stages := make(map[string]LLMStageConfig, len(requiredLLMStages))
	for _, name := range requiredLLMStages {
		model := otherModel
		if name == "structure" {
			model = structureModel
		}
		stages[name] = LLMStageConfig{Provider: provider, Model: model, Prompt: prompt, Temperature: &temperature, MaxTokens: 100, TimeoutText: "1m"}
	}
	return LLMConfig{Stages: stages}
}

func TestLoadLLMConfigRejectsMissingEnvironment(t *testing.T) {
	path := writeLLMTestConfig(t, "gemini", "MISSING_LLM_TEST_KEY", "")
	_ = os.Unsetenv("MISSING_LLM_TEST_KEY")
	_, err := LoadLLMConfig(path)
	if err == nil || !strings.Contains(err.Error(), "MISSING_LLM_TEST_KEY") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadLLMConfigForDryRunDoesNotRequireProviderCredentials(t *testing.T) {
	path := writeLLMTestConfig(t, "gemini", "MISSING_DRY_RUN_KEY", "")
	_ = os.Unsetenv("MISSING_DRY_RUN_KEY")
	cfg, err := LoadLLMConfigForDryRun(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Stages["structure"].PromptTemplate == "" {
		t.Fatal("dry-run prompt template is empty")
	}
}

func TestLoadLLMProviderConfigDoesNotValidatePipeline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `llm:
  providers:
    deepseek_web:
      type: deepseek_web
      profile_dir: data/test-deepseek
  stages:
    structure:
      provider: gemini
      model: "${MISSING_LOGIN_TEST_MODEL}"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Unsetenv("MISSING_LOGIN_TEST_MODEL")
	provider, err := LoadLLMProviderConfig(path, "deepseek_web")
	if err != nil {
		t.Fatal(err)
	}
	if provider.Type != "deepseek_web" || provider.ProfileDir != "data/test-deepseek" || provider.ChatURL == "" {
		t.Fatalf("provider = %+v", provider)
	}
}

func TestDeepSeekWebDoesNotRequireAPICredentials(t *testing.T) {
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(prompt, []byte("prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testLLMConfig(prompt, "deepseek", "deepseek-chat", "deepseek-chat")
	cfg.Providers = map[string]LLMProviderConfig{"deepseek": {Type: "deepseek_web"}}
	if err := validateLLMConfig(&cfg, true); err != nil {
		t.Fatal(err)
	}
	provider := cfg.Providers["deepseek"]
	if provider.ChatURL == "" || provider.LoginURL == "" || provider.ProfileDir == "" {
		t.Fatalf("DeepSeek defaults were not applied: %+v", provider)
	}
}

func TestLoadLLMConfigRejectsMissingPrompt(t *testing.T) {
	path := writeLLMTestConfig(t, "gemini", "TEST_GEMINI_KEY", filepath.Join(t.TempDir(), "missing.txt"))
	t.Setenv("TEST_GEMINI_KEY", "test-value")
	_, err := LoadLLMConfig(path)
	if err == nil || !strings.Contains(err.Error(), `stage "structure" prompt`) {
		t.Fatalf("error = %v", err)
	}
}

func writeLLMTestConfig(t *testing.T, structureProvider, keyEnv, promptOverride string) string {
	t.Helper()
	directory := t.TempDir()
	promptPath := filepath.Join(directory, "prompt.txt")
	if promptOverride != "" {
		promptPath = promptOverride
	} else if err := os.WriteFile(promptPath, []byte("Title: {{.Title}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "config.yaml")
	content := "llm:\n  providers:\n    gemini:\n      type: gemini\n      api_key_env: " + keyEnv + "\n  stages:\n"
	for _, stage := range requiredLLMStages {
		provider := "gemini"
		if stage == "structure" {
			provider = structureProvider
		}
		timeout := "2m"
		if stage == "fix" {
			timeout = "5m"
		}
		content += "    " + stage + ":\n      provider: " + provider + "\n      model: test-model\n      prompt: " + promptPath + "\n      timeout: " + timeout + "\n"
	}
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestTask1StageProvidersMatchConfiguredScheme(t *testing.T) {
	projectRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(projectRoot)
	t.Setenv("GEMINI_API_KEY", "test-gemini")
	t.Setenv("GEMINI_MODEL", "gemini-2.5-flash")
	t.Setenv("OPENROUTER_API_KEY", "test-openrouter")
	t.Setenv("OPENROUTER_MODEL", "openrouter-model")
	config, err := LoadLLMConfig("config/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		stage     string
		providers []string
	}{
		{stage: "structure", providers: []string{"deepseek_web"}},
		{stage: "article", providers: []string{"gemini", "deepseek_web"}},
		{stage: "info", providers: []string{"deepseek_web"}},
		{stage: "review", providers: []string{"gemini", "deepseek_web"}},
		// fix продолжает чат review и идёт к выбранному там провайдеру; список targets
		// существует только ради обязательной валидации стадии.
		{stage: "fix", providers: []string{"gemini", "deepseek_web"}},
		{stage: "html", providers: []string{"deepseek_web"}},
	}
	for _, test := range tests {
		t.Run(test.stage, func(t *testing.T) {
			stage, found := config.Stages[test.stage]
			if !found {
				t.Fatalf("стадия %q не настроена", test.stage)
			}
			var providers []string
			for _, target := range stage.Targets {
				providers = append(providers, target.Provider)
				if strings.TrimSpace(target.Model) == "" {
					t.Errorf("у провайдера %s не задана модель", target.Provider)
				}
			}
			if !reflect.DeepEqual(providers, test.providers) {
				t.Fatalf("провайдеры = %v, ожидались %v", providers, test.providers)
			}
		})
	}
}
