package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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

// requiredLLMStages — стадии схемы task_1: шесть стадий генерации плюс keywords, резервный
// источник запросов для prepare. Список остаётся значением по умолчанию для вызовов без явного
// набора; у задачи со своим потоком генерации набор свой и приходит из её профиля.
var requiredLLMStages = []string{"structure", "article", "info", "review", "fix", "html", "keywords"}

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
	// AttemptTimeoutText ограничивает одну попытку, тогда как TimeoutText остаётся общим
	// бюджетом стадии на все попытки. Без него первая же зависшая попытка съедала бюджет
	// целиком: повтор не начинался, и стадия падала с «deadline exceeded before retry», ни
	// разу не повторившись. Пустое значение сохраняет прежнее поведение — попытке доступен
	// весь остаток бюджета.
	AttemptTimeoutText string `yaml:"attempt_timeout"`
	// AttachmentsDir — каталог с документом, который уходит в модель вместе с промптом
	// стадии. Имя файла не фиксируется: значим каталог, а не то, как назвали регламент.
	// Пустое значение означает стадию без вложений — так живут все стадии task_1.
	AttachmentsDir string `yaml:"attachments_dir"`
	// Mode — подпись переключателя режима в интерфейсе провайдера («Быстрый»). Подпись, а
	// не собственное имя режима: переключатель у браузерного провайдера опознаётся по
	// тексту, и держать здесь второе имя значило бы заводить таблицу соответствий,
	// которая устареет вместе с интерфейсом.
	Mode string `yaml:"mode"`

	Timeout        time.Duration `yaml:"-"`
	AttemptTimeout time.Duration `yaml:"-"`
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
	return loadLLMConfig(path, requiredLLMStages, true)
}

// LoadLLMConfigForDryRun loads prompt templates without requiring provider credentials.
func LoadLLMConfigForDryRun(path string) (LLMConfig, error) {
	return loadLLMConfig(path, requiredLLMStages, false)
}

// LoadLLMConfigForStages проверяет схему по набору стадий самой задачи.
//
// Набор приходит снаружи, потому что он у задач разный: task_1 генерирует статью шестью
// стадиями, задача со своим потоком — своими. Пустой набор означает набор task_1.
func LoadLLMConfigForStages(path string, stages []string, requireCredentials bool) (LLMConfig, error) {
	return loadLLMConfig(path, stages, requireCredentials)
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
	return LoadLLMConfigWithOverlayForStages(basePath, overlayPath, requiredLLMStages, requireCredentials)
}

// LoadLLMConfigWithOverlayForStages накладывает overlay и проверяет схему по набору стадий
// задачи. Пустой набор означает набор task_1.
func LoadLLMConfigWithOverlayForStages(basePath, overlayPath string, stages []string, requireCredentials bool) (LLMConfig, error) {
	base, err := readLLMFile(basePath)
	if err != nil {
		return LLMConfig{}, err
	}
	overlay, err := readLLMFile(overlayPath)
	if err != nil {
		return LLMConfig{}, err
	}
	mergeLLMConfig(&base.LLM, overlay.LLM)
	if err := validateLLMConfig(&base.LLM, stages, requireCredentials); err != nil {
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
		if strings.TrimSpace(stage.AttemptTimeoutText) != "" {
			merged.AttemptTimeoutText = stage.AttemptTimeoutText
		}
		if strings.TrimSpace(stage.AttachmentsDir) != "" {
			merged.AttachmentsDir = stage.AttachmentsDir
		}
		if strings.TrimSpace(stage.Mode) != "" {
			merged.Mode = stage.Mode
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

func loadLLMConfig(path string, stages []string, requireCredentials bool) (LLMConfig, error) {
	fileConfig, err := readLLMFile(path)
	if err != nil {
		return LLMConfig{}, err
	}
	if err := validateLLMConfig(&fileConfig.LLM, stages, requireCredentials); err != nil {
		return LLMConfig{}, err
	}
	return fileConfig.LLM, nil
}

// resolveStageTimeouts разбирает оба бюджета стадии: общий на все попытки и на одну попытку.
//
// Второй по умолчанию равен первому — так стадия ведёт себя ровно как до его появления.
func resolveStageTimeouts(stageName string, stage *LLMStageConfig) error {
	stage.Timeout = DefaultLLMTimeout
	if text := strings.TrimSpace(stage.TimeoutText); text != "" {
		timeout, err := time.ParseDuration(text)
		if err != nil || timeout <= 0 {
			return fmt.Errorf("LLM stage %q has invalid timeout %q", stageName, stage.TimeoutText)
		}
		stage.Timeout = timeout
	}
	stage.AttemptTimeout = stage.Timeout
	text := strings.TrimSpace(stage.AttemptTimeoutText)
	if text == "" {
		return nil
	}
	attemptTimeout, err := time.ParseDuration(text)
	if err != nil || attemptTimeout <= 0 {
		return fmt.Errorf("LLM stage %q has invalid attempt_timeout %q", stageName, stage.AttemptTimeoutText)
	}
	// Попытка длиннее бюджета — не ограничение, а его молчаливая отмена: повтору снова не
	// останется времени. Такую конфигурацию честнее не принимать.
	if attemptTimeout > stage.Timeout {
		return fmt.Errorf("LLM stage %q has attempt_timeout %s longer than timeout %s",
			stageName, attemptTimeout, stage.Timeout)
	}
	stage.AttemptTimeout = attemptTimeout
	return nil
}

func validateLLMConfig(cfg *LLMConfig, stages []string, requireCredentials bool) error {
	if len(stages) == 0 {
		stages = requiredLLMStages
	}
	for name, provider := range cfg.Providers {
		normalized, err := normalizeLLMProvider(name, provider)
		if err != nil {
			return err
		}
		cfg.Providers[name] = normalized
	}
	usedProviders := make(map[string]struct{})
	for _, stageName := range stages {
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
		if err := resolveStageTimeouts(stageName, &stage); err != nil {
			return err
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

// AttachmentExtension — расширение документа, прикрепляемого к стадии. Имя файла смысла
// не несёт, значимо только оно: регламент переименовывают, а стадия обязана работать.
const AttachmentExtension = ".pdf"

// ResolveStageAttachments возвращает документы стадии из её каталога.
//
// Правило то же, что у книги импорта (importer.ResolveWorkbook): каталог задан — в нём
// ровно один подходящий файл. Пустой каталог и несколько файлов — не выбор по умолчанию,
// а вопрос к человеку, и ошибка обязана назвать, что именно поправить.
//
// Разбор конфигурации сюда не заходит намеренно: регламент — рабочий документ на диске, а
// не часть репозитория, и требовать его на каждом чтении config значило бы ломать команды
// задачи и её тесты там, где до модели дело не дойдёт. Проверить документ заранее — работа
// dry-run, который печатает разрешённую маршрутизацию перед дорогим прогоном.
func ResolveStageAttachments(stageName, directory string) ([]string, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf(
				"LLM stage %q: каталог документов %s не найден — создайте его и положите туда %s",
				stageName, directory, AttachmentExtension)
		}
		return nil, fmt.Errorf("LLM stage %q: прочитать каталог документов %s: %w", stageName, directory, err)
	}
	documents := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if !strings.EqualFold(filepath.Ext(name), AttachmentExtension) {
			continue
		}
		documents = append(documents, name)
	}
	sort.Strings(documents)
	switch len(documents) {
	case 1:
		return []string{filepath.Join(directory, documents[0])}, nil
	case 0:
		return nil, fmt.Errorf(
			"LLM stage %q: в каталоге %s нет ни одного файла %s — положите туда документ стадии",
			stageName, directory, AttachmentExtension)
	default:
		return nil, fmt.Errorf(
			"LLM stage %q: в каталоге %s несколько файлов %s (%s) — оставьте один",
			stageName, directory, AttachmentExtension, strings.Join(documents, ", "))
	}
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
