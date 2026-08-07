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
	// SingleChatPerArticle держит один диалог провайдера на статью вместо новой беседы на
	// каждую стадию. Поддерживается только браузерными провайдерами.
	SingleChatPerArticle bool `yaml:"single_chat_per_article"`
}

// LLMStageConfig описывает одну стадию. Маршрутизация существует здесь ровно в одном виде —
// Targets. Исторические ключи provider/model на уровне стадии по-прежнему разбираются, но
// полями структуры не становятся: пока они ими были, их значение расходилось с Targets и код
// читал то одно, то другое.
type LLMStageConfig struct {
	Targets     []LLMTargetConfig `yaml:"targets"`
	Prompt      string            `yaml:"prompt"`
	Temperature *float64          `yaml:"temperature"`
	MaxTokens   int               `yaml:"max_tokens"`
	TimeoutText string            `yaml:"timeout"`

	Timeout        time.Duration `yaml:"-"`
	PromptTemplate string        `yaml:"-"`
}

// UnmarshalYAML принимает обе формы записи стадии и схлопывает историческую в Targets прямо
// при разборе. Дальше по коду вторая форма уже не существует — в том числе в mergeLLMConfig,
// который иначе молча пропускал бы overlay, написанный одиночной формой.
func (s *LLMStageConfig) UnmarshalYAML(node *yaml.Node) error {
	// stageFields не наследует этот метод, поэтому рекурсии при разборе не возникает.
	type stageFields LLMStageConfig
	var raw struct {
		stageFields `yaml:",inline"`
		Provider    string `yaml:"provider"`
		Model       string `yaml:"model"`
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*s = LLMStageConfig(raw.stageFields)
	if len(s.Targets) == 0 && (strings.TrimSpace(raw.Provider) != "" || strings.TrimSpace(raw.Model) != "") {
		s.Targets = []LLMTargetConfig{{Provider: raw.Provider, Model: raw.Model}}
	}
	return nil
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

// LoadLLMConfigWithOverlay накладывает файл отличий на базовую конфигурацию.
//
// Overlay перечисляет только то, что меняется: путь к промпту, targets, флаг провайдера.
// Всё остальное — таймауты, температуры, max_tokens — остаётся в одном месте, в базовом
// файле, и не расходится между режимами.
func LoadLLMConfigWithOverlay(basePath, overlayPath string, requireCredentials bool) (LLMConfig, error) {
	base, err := readLLMFile(basePath)
	if err != nil {
		return LLMConfig{}, err
	}
	overlay, err := readLLMFile(overlayPath)
	if err != nil {
		return LLMConfig{}, err
	}
	mergeLLMConfig(&base.LLM, overlay.LLM)
	if err := validateLLMConfig(&base.LLM, requireCredentials); err != nil {
		return LLMConfig{}, fmt.Errorf("LLM config %q с наложенным %q: %w", basePath, overlayPath, err)
	}
	return base.LLM, nil
}

// mergeLLMConfig переносит заполненные поля overlay в базовую конфигурацию.
// Пустое поле overlay означает «оставить как в базе», а не «очистить».
func mergeLLMConfig(base *LLMConfig, overlay LLMConfig) {
	for name, provider := range overlay.Providers {
		merged, found := base.Providers[name]
		if !found {
			base.Providers[name] = provider
			continue
		}
		if provider.SingleChatPerArticle {
			merged.SingleChatPerArticle = true
		}
		if provider.Headless != nil {
			merged.Headless = provider.Headless
		}
		base.Providers[name] = merged
	}
	for name, stage := range overlay.Stages {
		merged, found := base.Stages[name]
		if !found {
			base.Stages[name] = stage
			continue
		}
		if len(stage.Targets) > 0 {
			merged.Targets = stage.Targets
		}
		if strings.TrimSpace(stage.Prompt) != "" {
			merged.Prompt = stage.Prompt
		}
		if stage.Temperature != nil {
			merged.Temperature = stage.Temperature
		}
		if stage.MaxTokens != 0 {
			merged.MaxTokens = stage.MaxTokens
		}
		if strings.TrimSpace(stage.TimeoutText) != "" {
			merged.TimeoutText = stage.TimeoutText
		}
		base.Stages[name] = merged
	}
}

func readLLMFile(path string) (LLMFileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LLMFileConfig{}, fmt.Errorf("прочитать LLM config %q: %w", path, err)
	}
	var fileConfig LLMFileConfig
	if err := yaml.Unmarshal(data, &fileConfig); err != nil {
		return LLMFileConfig{}, fmt.Errorf("разобрать LLM config %q: %w", path, err)
	}
	return fileConfig, nil
}

func loadLLMConfig(path string, requireCredentials bool) (LLMConfig, error) {
	fileConfig, err := readLLMFile(path)
	if err != nil {
		return LLMConfig{}, err
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
		if err := validateStageTargets(cfg, stageName, stage.Targets, usedProviders, requireCredentials); err != nil {
			return err
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

// validateStageTargets приводит targets стадии к рабочему виду и отмечает задействованных
// провайдеров. Пустой список — ошибка: после разбора YAML маршрут обязан существовать.
func validateStageTargets(cfg *LLMConfig, stageName string, targets []LLMTargetConfig, usedProviders map[string]struct{}, requireCredentials bool) error {
	if len(targets) == 0 {
		return fmt.Errorf("LLM stage %q has no targets", stageName)
	}
	for index := range targets {
		target := &targets[index]
		target.Provider = strings.TrimSpace(target.Provider)
		if _, found := cfg.Providers[target.Provider]; !found {
			return fmt.Errorf("LLM stage %q target %d references unknown provider %q; available providers: %s",
				stageName, index, target.Provider, strings.Join(providerNames(cfg.Providers), ", "))
		}
		// Модель из переменной окружения — такое же требование окружения, как API-ключ,
		// поэтому пустое значение после подстановки проверяется тем же флагом. Иначе
		// dry-run падал бы на незаданном GEMINI_MODEL, так и не дойдя до вывода о том,
		// что Gemini в этом прогоне вообще не нужен.
		raw := strings.TrimSpace(target.Model)
		if raw == "" {
			return fmt.Errorf("LLM stage %q target %d has empty model", stageName, index)
		}
		target.Model = strings.TrimSpace(os.ExpandEnv(raw))
		if target.Model == "" && requireCredentials {
			return fmt.Errorf("LLM stage %q target %d model %q expands to an empty value", stageName, index, raw)
		}
		usedProviders[target.Provider] = struct{}{}
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
