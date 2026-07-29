// Package config отвечает за загрузку и проверку настроек приложения.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/joho/godotenv"
)

// Config содержит настройки приложения.
type Config struct {
	DatabaseURL   string
	InputFilePath string
	OutputDir     string
	LogLevel      string
	LogFormat     string
	GeminiAPIKey  string
	GeminiModel   string

	KeysSOEmail      string
	KeysSOPassword   string
	ArsenkinEmail    string
	ArsenkinPassword string
	ArsenkinHeadless bool
}

// Load загружает настройки из .env и переменных окружения.
//
// Переменные окружения имеют приоритет над значениями из файла .env.
func Load() (Config, error) {
	envPath, err := envFilePath()
	if err != nil {
		return Config{}, err
	}
	if err := godotenv.Load(envPath); err != nil {
		// Не включаем ошибку парсера: она может содержать строку из .env с секретом.
		return Config{}, fmt.Errorf("failed to load .env\n\nsearched:\n%s", envPath)
	}

	arsenkinHeadless := true
	if value := os.Getenv("ARSENKIN_HEADLESS"); value != "" {
		arsenkinHeadless, err = strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("ARSENKIN_HEADLESS must be true or false: %w", err)
		}
	}

	cfg := Config{
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		InputFilePath: os.Getenv("INPUT_FILE_PATH"),
		OutputDir:     os.Getenv("OUTPUT_DIR"),
		LogLevel:      os.Getenv("LOG_LEVEL"),
		LogFormat:     os.Getenv("LOG_FORMAT"),
		GeminiAPIKey:  os.Getenv("GEMINI_API_KEY"),
		GeminiModel:   os.Getenv("GEMINI_MODEL"),

		KeysSOEmail:      os.Getenv("KEYS_SO_EMAIL"),
		KeysSOPassword:   os.Getenv("KEYS_SO_PASSWORD"),
		ArsenkinEmail:    os.Getenv("ARSENKIN_EMAIL"),
		ArsenkinPassword: os.Getenv("ARSENKIN_PASSWORD"),
		ArsenkinHeadless: arsenkinHeadless,
	}

	if cfg.InputFilePath == "" {
		cfg.InputFilePath = "tasks/task_1/input/input.xlsx"
	}
	if cfg.OutputDir == "" {
		cfg.OutputDir = "tasks/task_1/output"
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	if cfg.LogFormat == "" {
		cfg.LogFormat = "text"
	}

	return cfg, nil
}

func envFilePath() (string, error) {
	if configuredPath, found := os.LookupEnv("ENV_FILE"); found && configuredPath != "" {
		return absoluteEnvPath("ENV_FILE", configuredPath)
	}

	// Сохраняем совместимость с прежним именем переменной.
	if configuredPath, found := os.LookupEnv("SEO_PIPELINE_ENV"); found && configuredPath != "" {
		return absoluteEnvPath("SEO_PIPELINE_ENV", configuredPath)
	}

	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("determine working directory for .env lookup: %w", err)
	}
	projectDirectory := workingDirectory
	for {
		if _, err := os.Stat(filepath.Join(projectDirectory, "go.mod")); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect project directory %q: %w", projectDirectory, err)
		}
		parent := filepath.Dir(projectDirectory)
		if parent == projectDirectory {
			projectDirectory = workingDirectory
			break
		}
		projectDirectory = parent
	}

	return filepath.Join(filepath.Dir(projectDirectory), ".env"), nil
}

func absoluteEnvPath(variable, configuredPath string) (string, error) {
	absolutePath, err := filepath.Abs(configuredPath)
	if err != nil {
		return "", fmt.Errorf("resolve %s path %q: %w", variable, configuredPath, err)
	}
	return filepath.Clean(absolutePath), nil
}

// ValidateImport проверяет настройки, необходимые команде import.
func (c Config) ValidateImport() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if c.InputFilePath == "" {
		return fmt.Errorf("INPUT_FILE_PATH is required")
	}
	return nil
}

// ValidateReset проверяет настройки, необходимые команде reset.
func (c Config) ValidateReset() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	return nil
}

// ValidateGenerate checks settings required only by local LLM stages.
func (c Config) ValidateGenerate() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if c.GeminiModel == "" {
		return fmt.Errorf("GEMINI_MODEL is required")
	}
	return nil
}

// ValidatePrepare checks settings required by external research collection.
func (c Config) ValidatePrepare() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if c.KeysSOEmail == "" {
		return fmt.Errorf("KEYS_SO_EMAIL is required")
	}
	if c.KeysSOPassword == "" {
		return fmt.Errorf("KEYS_SO_PASSWORD is required")
	}
	if c.ArsenkinEmail == "" {
		return fmt.Errorf("ARSENKIN_EMAIL is required")
	}
	if c.ArsenkinPassword == "" {
		return fmt.Errorf("ARSENKIN_PASSWORD is required")
	}
	return nil
}
