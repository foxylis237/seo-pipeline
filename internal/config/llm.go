package config

import (
	"fmt"
	"net/url"
	"os"
	"sort"
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
	Type       string `yaml:"type"`
	BaseURL    string `yaml:"base_url"`
	APIKeyEnv  string `yaml:"api_key_env"`
	ChatURL    string `yaml:"chat_url"`
	LoginURL   string `yaml:"login_url"`
	ProfileDir string `yaml:"profile_dir"`
	Headless   *bool  `yaml:"headless"`
}

type LLMStageConfig struct {
	Provider    string            `yaml:"provider"`
	Model       string            `yaml:"model"`
	Targets     []LLMTargetConfig `yaml:"targets"`
	Prompt      string            `yaml:"prompt"`
	Temperature *float64          `yaml:"temperature"`
	MaxTokens   int               `yaml:"max_tokens"`
	TimeoutText string            `yaml:"timeout"`

	Timeout        time.Duration `yaml:"-"`
	PromptTemplate string        `yaml:"-"`
}

type LLMTargetConfig struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

func LoadLLMConfig(path string) (LLMConfig, error) {
	return loadLLMConfig(path, true)
}

// LoadLLMConfigForDryRun loads prompt templates without requiring provider credentials.
func LoadLLMConfigForDryRun(path string) (LLMConfig, error) {
	return loadLLMConfig(path, false)
}

// LoadLLMProviderConfig loads one provider without validating stages, prompt
// templates, model environment variables, or credentials of other providers.
// It is intended for provider-specific maintenance commands such as browser login.
func LoadLLMProviderConfig(path, name string) (LLMProviderConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LLMProviderConfig{}, fmt.Errorf("прочитать LLM config %q: %w", path, err)
	}
	var fileConfig LLMFileConfig
	if err := yaml.Unmarshal(data, &fileConfig); err != nil {
		return LLMProviderConfig{}, fmt.Errorf("разобрать LLM config %q: %w", path, err)
	}
	provider, found := fileConfig.LLM.Providers[name]
	if !found {
		return LLMProviderConfig{}, fmt.Errorf("LLM provider %q is not configured", name)
	}
	return normalizeLLMProvider(name, provider)
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
		normalized, err := normalizeLLMProvider(name, provider)
		if err != nil {
			return err
		}
		cfg.Providers[name] = normalized
	}
	usedProviders := make(map[string]struct{})
	for _, stageName := range requiredLLMStages {
		stage, found := cfg.Stages[stageName]
		if !found {
			return fmt.Errorf("LLM stage %q is missing", stageName)
		}
		if len(stage.Targets) == 0 {
			stage.Targets = []LLMTargetConfig{{Provider: stage.Provider, Model: stage.Model}}
		}
		for index := range stage.Targets {
			target := &stage.Targets[index]
			target.Provider = strings.TrimSpace(target.Provider)
			target.Model = os.ExpandEnv(target.Model)
			if _, found := cfg.Providers[target.Provider]; !found {
				return fmt.Errorf("LLM stage %q target %d references unknown provider %q; available providers: %s", stageName, index, target.Provider, strings.Join(providerNames(cfg.Providers), ", "))
			}
			if strings.TrimSpace(target.Model) == "" {
				return fmt.Errorf("LLM stage %q target %d has empty model", stageName, index)
			}
			usedProviders[target.Provider] = struct{}{}
		}
		stage.Provider, stage.Model = stage.Targets[0].Provider, stage.Targets[0].Model
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
	if requireCredentials {
		for name := range usedProviders {
			provider := cfg.Providers[name]
			if provider.Type == "deepseek_web" {
				continue
			}
			if strings.TrimSpace(os.Getenv(provider.APIKeyEnv)) == "" {
				return fmt.Errorf("provider %q requires environment variable %s", name, provider.APIKeyEnv)
			}
		}
	}
	return nil
}

func normalizeLLMProvider(name string, provider LLMProviderConfig) (LLMProviderConfig, error) {
	provider.Type = strings.TrimSpace(provider.Type)
	provider.APIKeyEnv = strings.TrimSpace(provider.APIKeyEnv)
	provider.BaseURL = os.ExpandEnv(strings.TrimSpace(provider.BaseURL))
	provider.ChatURL = os.ExpandEnv(strings.TrimSpace(provider.ChatURL))
	provider.LoginURL = os.ExpandEnv(strings.TrimSpace(provider.LoginURL))
	provider.ProfileDir = os.ExpandEnv(strings.TrimSpace(provider.ProfileDir))
	switch provider.Type {
	case "gemini":
	case "openai", "openai-compatible", "openai_compatible":
		provider.Type = "openai_compatible"
		parsed, err := url.ParseRequestURI(provider.BaseURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return LLMProviderConfig{}, fmt.Errorf("provider %q has invalid base_url", name)
		}
	case "deepseek-web", "deepseek_web":
		provider.Type = "deepseek_web"
		if provider.ChatURL == "" {
			provider.ChatURL = "https://chat.deepseek.com/"
		}
		if provider.LoginURL == "" {
			provider.LoginURL = "https://chat.deepseek.com/sign_in"
		}
		if provider.ProfileDir == "" {
			provider.ProfileDir = "data/browser/deepseek"
		}
		for field, value := range map[string]string{"chat_url": provider.ChatURL, "login_url": provider.LoginURL} {
			parsed, err := url.ParseRequestURI(value)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
				return LLMProviderConfig{}, fmt.Errorf("provider %q has invalid %s", name, field)
			}
		}
	default:
		return LLMProviderConfig{}, fmt.Errorf("provider %q has unsupported type %q; supported types: gemini, openai_compatible, deepseek_web", name, provider.Type)
	}
	if provider.Type != "deepseek_web" && provider.APIKeyEnv == "" {
		return LLMProviderConfig{}, fmt.Errorf("provider %q has empty api_key_env", name)
	}
	return provider, nil
}

func providerNames(providers map[string]LLMProviderConfig) []string {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
