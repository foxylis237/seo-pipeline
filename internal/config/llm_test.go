package config

import (
	"os"
	"path/filepath"
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
	t.Setenv("OPENROUTER_API_KEY", "test-openrouter")
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
	if err == nil || !strings.Contains(err.Error(), `stage "structure" references unknown provider "missing"`) {
		t.Fatalf("error = %v", err)
	}
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
