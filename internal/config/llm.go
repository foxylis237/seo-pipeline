package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultLLMTemperature = 0.3
	DefaultLLMMaxTokens   = 4096
	DefaultLLMTimeout     = 2 * time.Minute
)

var requiredLLMStages = []string{"structure", "article", "info", "review", "fix", "html"}

type LLMFileConfig struct {
	LLM LLMConfig `yaml:"llm"`
}

type LLMConfig struct {
	Providers map[string]LLMProviderConfig `yaml:"providers"`
	Stages    map[string]LLMStageConfig    `yaml:"stages"`
}

type LLMProviderConfig struct {
	Type      string `yaml:"type"`
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
}

type LLMStageConfig struct {
	Provider    string   `yaml:"provider"`
	Model       string   `yaml:"model"`
	Prompt      string   `yaml:"prompt"`
	Temperature *float64 `yaml:"temperature"`
	MaxTokens   int      `yaml:"max_tokens"`
	TimeoutText string   `yaml:"timeout"`

	Timeout        time.Duration `yaml:"-"`
	PromptTemplate string        `yaml:"-"`
}

func LoadLLMConfig(path string) (LLMConfig, error) {
	return loadLLMConfig(path, true)
}

// LoadLLMConfigForDryRun loads prompt templates without requiring provider credentials.
func LoadLLMConfigForDryRun(path string) (LLMConfig, error) {
	return loadLLMConfig(path, false)
}

func loadLLMConfig(path string, requireCredentials bool) (LLMConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LLMConfig{}, fmt.Errorf("прочитать LLM config %q: %w", path, err)
	}
	var fileConfig LLMFileConfig
	if err := yaml.Unmarshal(data, &fileConfig); err != nil {
		return LLMConfig{}, fmt.Errorf("разобрать LLM config %q: %w", path, err)
	}
	if err := validateLLMConfig(&fileConfig.LLM, requireCredentials); err != nil {
		return LLMConfig{}, err
	}
	return fileConfig.LLM, nil
}

func validateLLMConfig(cfg *LLMConfig, requireCredentials bool) error {
	for name, provider := range cfg.Providers {
		provider.Type = strings.TrimSpace(provider.Type)
		provider.APIKeyEnv = strings.TrimSpace(provider.APIKeyEnv)
		switch provider.Type {
		case "gemini":
		case "openai_compatible":
			parsed, err := url.ParseRequestURI(provider.BaseURL)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
				return fmt.Errorf("provider %q has invalid base_url", name)
			}
		default:
			return fmt.Errorf("provider %q has unsupported type %q", name, provider.Type)
		}
		if provider.APIKeyEnv == "" {
			return fmt.Errorf("provider %q has empty api_key_env", name)
		}
		if requireCredentials && strings.TrimSpace(os.Getenv(provider.APIKeyEnv)) == "" {
			return fmt.Errorf("provider %q requires environment variable %s", name, provider.APIKeyEnv)
		}
		cfg.Providers[name] = provider
	}
	for _, stageName := range requiredLLMStages {
		stage, found := cfg.Stages[stageName]
		if !found {
			return fmt.Errorf("LLM stage %q is missing", stageName)
		}
		if _, found := cfg.Providers[stage.Provider]; !found {
			return fmt.Errorf("LLM stage %q references unknown provider %q", stageName, stage.Provider)
		}
		if strings.TrimSpace(stage.Model) == "" {
			return fmt.Errorf("LLM stage %q has empty model", stageName)
		}
		if strings.TrimSpace(stage.Prompt) == "" {
			return fmt.Errorf("LLM stage %q has empty prompt path", stageName)
		}
		prompt, err := os.ReadFile(stage.Prompt)
		if err != nil {
			return fmt.Errorf("LLM stage %q prompt %q: %w", stageName, stage.Prompt, err)
		}
		stage.PromptTemplate = string(prompt)
		if stage.Temperature == nil {
			value := DefaultLLMTemperature
			stage.Temperature = &value
		}
		if stage.MaxTokens == 0 {
			stage.MaxTokens = DefaultLLMMaxTokens
		}
		if stage.MaxTokens < 0 {
			return fmt.Errorf("LLM stage %q has invalid max_tokens", stageName)
		}
		if strings.TrimSpace(stage.TimeoutText) == "" {
			stage.Timeout = DefaultLLMTimeout
		} else {
			stage.Timeout, err = time.ParseDuration(stage.TimeoutText)
			if err != nil || stage.Timeout <= 0 {
				return fmt.Errorf("LLM stage %q has invalid timeout %q", stageName, stage.TimeoutText)
			}
		}
		cfg.Stages[stageName] = stage
	}
	return nil
}
