// Package config отвечает за загрузку и проверку настроек приложения.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config содержит настройки приложения.
type Config struct {
	AppEnv      string
	DatabaseURL string
	// InputFilePath — явно заданный INPUT_FILE_PATH; пустой — искать книгу в InputDir.
	// Догадку сюда не подставлять: выбор человека должен отличаться от умолчания.
	InputFilePath string
	// InputDir — каталог импорта задачи; книгу в нём выбирает importer.ResolveWorkbook.
	InputDir     string
	OutputDir    string
	LogLevel     string
	LogFormat    string
	GeminiAPIKey string
	GeminiModel  string
	// KeysSODisabled — Keys.so выключен (pipeline.keysso: false); нулевое значение — сбор идёт.
	KeysSODisabled bool

	KeysSOEmail      string
	KeysSOPassword   string
	ArsenkinEmail    string
	ArsenkinPassword string
	ArsenkinHeadless bool

	// WordPress — площадка публикации этой задачи.
	WordPress WordPressConfig

	// envPrefix — префикс переменных задачи; нужен сообщениям об ошибках.
	envPrefix string
}

// WordPressConfig — доступ к площадке одной задачи; три значения всегда идут вместе.
type WordPressConfig struct {
	// BaseURL — корень сайта из <префикс>WORDPRESS_URL.
	BaseURL string
	// Username — логин из <префикс>WORDPRESS_USERNAME.
	Username string
	// AppPassword — Application Password из <префикс>WORDPRESS_APP_PASSWORD, не пароль администратора.
	AppPassword string
}

// EnvName возвращает имя переменной окружения с префиксом задачи.
func (c Config) EnvName(name string) string {
	return c.envPrefix + name
}

// TaskDefaults — то, чем задача подменяет общие настройки.
// Пустой EnvPrefix — имена без префикса, и они действуют только на задачу без префикса.
type TaskDefaults struct {
	InputDir  string
	OutputDir string
	EnvPrefix string
}

// Load загружает настройки из .env и переменных окружения; окружение важнее файла.
func Load(defaults TaskDefaults) (Config, error) {
	return load(true, defaults)
}

// LoadDryRun loads local settings without requiring an environment file or paid-service credentials.
func LoadDryRun(defaults TaskDefaults) (Config, error) {
	return load(false, defaults)
}

// taskEnv читает переменную задачи. Имя без префикса читается только задачей без префикса,
// чтобы оно не протекало в другие задачи.
func (d TaskDefaults) taskEnv(name string) string {
	if d.EnvPrefix != "" {
		return os.Getenv(d.EnvPrefix + name)
	}
	return os.Getenv(name)
}

func load(requireEnvFile bool, defaults TaskDefaults) (Config, error) {
	envPath, err := envFilePath()
	if err != nil {
		return Config{}, err
	}
	configuredEnvFile := os.Getenv("ENV_FILE") != "" || os.Getenv("SEO_PIPELINE_ENV") != ""
	if !requireEnvFile && !configuredEnvFile {
		err = nil
	} else if _, statErr := os.Stat(envPath); statErr == nil {
		err = godotenv.Load(envPath)
	} else {
		err = statErr
	}
	if err != nil {
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

	// Сервер PostgreSQL у задач общий; префиксная переменная лишь уводит задачу на другой.
	databaseURL := os.Getenv("DATABASE_URL")
	if prefixed := defaults.taskEnv("DATABASE_URL"); prefixed != "" {
		databaseURL = prefixed
	}

	cfg := Config{
		AppEnv:        os.Getenv("APP_ENV"),
		DatabaseURL:   databaseURL,
		InputFilePath: defaults.taskEnv("INPUT_FILE_PATH"),
		InputDir:      defaults.InputDir,
		OutputDir:     defaults.taskEnv("OUTPUT_DIR"),
		LogLevel:      os.Getenv("LOG_LEVEL"),
		LogFormat:     os.Getenv("LOG_FORMAT"),
		GeminiAPIKey:  os.Getenv("GEMINI_API_KEY"),
		GeminiModel:   os.Getenv("GEMINI_MODEL"),

		KeysSOEmail:      os.Getenv("KEYS_SO_EMAIL"),
		KeysSOPassword:   os.Getenv("KEYS_SO_PASSWORD"),
		ArsenkinEmail:    os.Getenv("ARSENKIN_EMAIL"),
		ArsenkinPassword: os.Getenv("ARSENKIN_PASSWORD"),
		ArsenkinHeadless: arsenkinHeadless,

		WordPress: WordPressConfig{
			BaseURL:     defaults.taskEnv("WORDPRESS_URL"),
			Username:    defaults.taskEnv("WORDPRESS_USERNAME"),
			AppPassword: defaults.taskEnv("WORDPRESS_APP_PASSWORD"),
		},

		envPrefix: defaults.EnvPrefix,
	}

	if cfg.OutputDir == "" {
		cfg.OutputDir = defaults.OutputDir
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	// auto, а не text: явный LOG_FORMAT=text должен остаться text и в терминале.
	if cfg.LogFormat == "" {
		cfg.LogFormat = "auto"
	}
	if !requireEnvFile {
		cfg.DatabaseURL = os.Getenv("DRY_RUN_DATABASE_URL")
		if prefixed := defaults.taskEnv("DRY_RUN_DATABASE_URL"); prefixed != "" {
			cfg.DatabaseURL = prefixed
		}
		if cfg.DatabaseURL == "" {
			cfg.DatabaseURL = "postgres://seo:seo@localhost:5433/seo_dry_run?sslmode=disable"
		}
		cfg.OutputDir = filepath.Join(cfg.OutputDir, "dry-run")
	}

	return cfg, nil
}

// LoadEnvFile loads the project .env into the process environment without building a Config.
func LoadEnvFile() error {
	envPath, err := envFilePath()
	if err != nil {
		return err
	}
	if err := godotenv.Load(envPath); err != nil {
		// Не включаем ошибку парсера: она может содержать строку из .env с секретом.
		return fmt.Errorf("failed to load .env\n\nsearched:\n%s", envPath)
	}
	return nil
}

func envFilePath() (string, error) {
	if configuredPath, found := os.LookupEnv("ENV_FILE"); found && configuredPath != "" {
		return absoluteEnvPath("ENV_FILE", configuredPath)
	}

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
	return c.validateImportSource()
}

// validateImportSource требует явный путь книги или каталог задачи; наличие книги проверяет
// importer.ResolveWorkbook.
func (c Config) validateImportSource() error {
	if c.InputFilePath == "" && c.InputDir == "" {
		return fmt.Errorf("INPUT_FILE_PATH or a task input directory is required")
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

// ValidateWordPress проверяет настройки площадки задачи; DATABASE_URL не требуется.
func (c Config) ValidateWordPress() error {
	if c.WordPress.BaseURL == "" {
		return fmt.Errorf("%s is required", c.EnvName("WORDPRESS_URL"))
	}
	if c.WordPress.Username == "" {
		return fmt.Errorf("%s is required", c.EnvName("WORDPRESS_USERNAME"))
	}
	if c.WordPress.AppPassword == "" {
		return fmt.Errorf("%s is required", c.EnvName("WORDPRESS_APP_PASSWORD"))
	}
	return nil
}

// ValidateGenerate checks settings required only by local LLM stages.
func (c Config) ValidateGenerate() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
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

// ValidateDryRun checks only local resources used by the offline pipeline.
func (c Config) ValidateDryRun() error {
	appEnv := strings.ToLower(strings.TrimSpace(c.AppEnv))
	if appEnv != "local" && appEnv != "test" {
		return fmt.Errorf("dry-run requires APP_ENV=local or APP_ENV=test")
	}
	if c.DatabaseURL == "" {
		return fmt.Errorf("DRY_RUN_DATABASE_URL is required")
	}
	if err := c.validateImportSource(); err != nil {
		return err
	}
	parsed, err := url.Parse(c.DatabaseURL)
	if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return fmt.Errorf("DRY_RUN_DATABASE_URL must be a valid PostgreSQL URL")
	}
	databaseName := strings.ToLower(strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if decoded, decodeErr := url.PathUnescape(databaseName); decodeErr == nil {
		databaseName = decoded
	}
	if !strings.Contains(databaseName, "test") && !strings.Contains(databaseName, "dry_run") && !strings.Contains(databaseName, "dry-run") {
		return fmt.Errorf("dry-run database name %q must contain test, dry_run, or dry-run", databaseName)
	}
	if filepath.Base(filepath.Clean(c.OutputDir)) != "dry-run" {
		return fmt.Errorf("dry-run OUTPUT_DIR must end with dry-run")
	}
	return nil
}
