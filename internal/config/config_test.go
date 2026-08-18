package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testTaskDefaults повторяет профиль task_1: тесты проверяют исторические дефолты, и они
// обязаны остаться прежними.
func testTaskDefaults() TaskDefaults {
	return TaskDefaults{InputDir: "input/task_1", OutputDir: "tasks/task_1/output"}
}

func TestEnvFilePathUsesConfiguredPath(t *testing.T) {
	t.Setenv("ENV_FILE", filepath.Join("config", "pipeline.env"))

	got, err := envFilePath()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(filepath.Join("config", "pipeline.env"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("envFilePath() = %q, want %q", got, want)
	}
}

func TestLoadUsesExistingEnvFile(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "pipeline.env")
	if err := os.WriteFile(envPath, []byte("DATABASE_URL=postgres://from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unsetEnv(t, "DATABASE_URL")
	t.Setenv("ENV_FILE", envPath)

	cfg, err := Load(testTaskDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://from-file" {
		t.Fatalf("DatabaseURL = %q, want value from ENV_FILE", cfg.DatabaseURL)
	}
}

func TestEnvFilePathFindsEnvBesideProject(t *testing.T) {
	t.Setenv("ENV_FILE", "")
	t.Setenv("SEO_PIPELINE_ENV", "")
	parent := t.TempDir()
	project := filepath.Join(parent, "seo-pipeline")
	nested := filepath.Join(project, "cmd", "seo-pipeline")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "go.mod"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	previousDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDirectory) })

	got, err := envFilePath()
	if err != nil {
		t.Fatal(err)
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(resolvedParent, ".env")
	if got != want {
		t.Fatalf("envFilePath() = %q, want %q", got, want)
	}
}

func TestLoadReportsSearchedPathWhenEnvIsMissing(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing.env")
	t.Setenv("ENV_FILE", missingPath)

	_, err := Load(testTaskDefaults())
	if err == nil {
		t.Fatal("Load() error = nil, want missing .env error")
	}
	if !strings.Contains(err.Error(), "failed to load .env") ||
		!strings.Contains(err.Error(), "searched:\n"+missingPath) {
		t.Fatalf("Load() error = %q, want searched path", err)
	}
}

func TestLoadDryRunUsesLocalDefaultsWhenEnvIsMissing(t *testing.T) {
	t.Setenv("ENV_FILE", "")
	t.Setenv("SEO_PIPELINE_ENV", "")
	t.Setenv("APP_ENV", "test")
	unsetEnv(t, "DATABASE_URL")
	unsetEnv(t, "DRY_RUN_DATABASE_URL")
	unsetEnv(t, "INPUT_FILE_PATH")

	cfg, err := LoadDryRun(testTaskDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://seo:seo@localhost:5433/seo_dry_run?sslmode=disable" {
		t.Fatalf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.InputDir != "input/task_1" {
		t.Fatalf("InputDir = %q", cfg.InputDir)
	}
	if cfg.OutputDir != filepath.Join("tasks", "task_1", "output", "dry-run") {
		t.Fatalf("OutputDir = %q", cfg.OutputDir)
	}
}

func TestValidateDryRunRejectsUnsafeEnvironmentAndDatabase(t *testing.T) {
	base := Config{AppEnv: "test", DatabaseURL: "postgres://seo:seo@localhost:5433/seo_dry_run", InputFilePath: "input.xlsx", OutputDir: "output/dry-run"}
	if err := base.ValidateDryRun(); err != nil {
		t.Fatalf("safe config: %v", err)
	}

	unsafeEnv := base
	unsafeEnv.AppEnv = "production"
	if err := unsafeEnv.ValidateDryRun(); err == nil || !strings.Contains(err.Error(), "APP_ENV") {
		t.Fatalf("unsafe environment error = %v", err)
	}

	unsafeDatabase := base
	unsafeDatabase.DatabaseURL = "postgres://seo:seo@localhost:5432/seo"
	if err := unsafeDatabase.ValidateDryRun(); err == nil || !strings.Contains(err.Error(), "database name") {
		t.Fatalf("unsafe database error = %v", err)
	}

	unsafeOutput := base
	unsafeOutput.OutputDir = "tasks/task_1/output"
	if err := unsafeOutput.ValidateDryRun(); err == nil || !strings.Contains(err.Error(), "OUTPUT_DIR") {
		t.Fatalf("unsafe output error = %v", err)
	}
}

func TestLoadPreservesSystemEnvironment(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("DATABASE_URL=postgres://from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", envPath)
	t.Setenv("DATABASE_URL", "postgres://from-system")

	cfg, err := Load(testTaskDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://from-system" {
		t.Fatalf("DatabaseURL = %q, want system environment value", cfg.DatabaseURL)
	}
}

func TestLoadErrorDoesNotExposeSecrets(t *testing.T) {
	const secret = "do-not-leak-this-password"
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("KEYS_SO_PASSWORD=\""+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", envPath)

	_, err := Load(testTaskDefaults())
	if err == nil {
		t.Fatal("Load() error = nil, want malformed .env error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Load() error exposes a secret: %q", err)
	}
}

func TestLoadParsesArsenkinHeadless(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", envPath)
	t.Setenv("ARSENKIN_HEADLESS", "false")

	cfg, err := Load(testTaskDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ArsenkinHeadless {
		t.Fatal("ArsenkinHeadless = true, want false")
	}
}

func TestValidatePrepareDoesNotRequireGeminiConfiguration(t *testing.T) {
	cfg := Config{
		DatabaseURL: "postgres://database",
		KeysSOEmail: "email", KeysSOPassword: "password",
		ArsenkinEmail: "email", ArsenkinPassword: "password",
	}
	if err := cfg.ValidatePrepare(); err != nil {
		t.Fatalf("ValidatePrepare() error = %v", err)
	}
}

func TestValidateGenerateDoesNotRequireCollectorCredentials(t *testing.T) {
	cfg := Config{DatabaseURL: "postgres://database", GeminiModel: "gemini-test"}
	if err := cfg.ValidateGenerate(); err != nil {
		t.Fatalf("ValidateGenerate() error = %v", err)
	}
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	value, found := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if found {
			_ = os.Setenv(name, value)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

// Переменная без префикса принадлежит задаче без префикса. Иначе один OUTPUT_DIR в .env увёл
// бы артефакты обеих задач в общий каталог, и изоляция задач держалась бы на честном слове.
func TestTaskWithPrefixIgnoresUnprefixedPaths(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "pipeline.env")
	if err := os.WriteFile(envPath, []byte("DATABASE_URL=postgres://from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", envPath)
	t.Setenv("OUTPUT_DIR", "tasks/task_1/output")
	t.Setenv("INPUT_FILE_PATH", "input/task_1/input.xlsx")
	unsetEnv(t, "PPROF_1_OUTPUT_DIR")
	unsetEnv(t, "PPROF_1_INPUT_FILE_PATH")

	defaults := TaskDefaults{
		InputDir:  "input/pprof_1",
		OutputDir: "tasks/pprof_1/output",
		EnvPrefix: "PPROF_1_",
	}
	cfg, err := Load(defaults)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OutputDir != defaults.OutputDir {
		t.Fatalf("OutputDir = %q, want %q: переменная без префикса протекла в другую задачу", cfg.OutputDir, defaults.OutputDir)
	}
	if cfg.InputFilePath != "" {
		t.Fatalf("InputFilePath = %q, want empty: INPUT_FILE_PATH протёк в задачу с префиксом", cfg.InputFilePath)
	}
	if cfg.InputDir != defaults.InputDir {
		t.Fatalf("InputDir = %q, want %q", cfg.InputDir, defaults.InputDir)
	}
}

// Префиксованная переменная перекрывает профиль — это и есть точечное переопределение задачи.
func TestPrefixedEnvOverridesTaskDefaults(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "pipeline.env")
	if err := os.WriteFile(envPath, []byte("DATABASE_URL=postgres://from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", envPath)
	t.Setenv("PPROF_1_OUTPUT_DIR", "tasks/pprof_1/custom")
	t.Setenv("PPROF_1_DATABASE_URL", "postgres://pprof-host/seo")
	t.Setenv("DATABASE_URL", "postgres://shared-host/seo")

	cfg, err := Load(TaskDefaults{
		InputDir:  "input/pprof_1",
		OutputDir: "tasks/pprof_1/output",
		EnvPrefix: "PPROF_1_",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OutputDir != "tasks/pprof_1/custom" {
		t.Fatalf("OutputDir = %q, want tasks/pprof_1/custom", cfg.OutputDir)
	}
	if cfg.DatabaseURL != "postgres://pprof-host/seo" {
		t.Fatalf("DatabaseURL = %q, want postgres://pprof-host/seo", cfg.DatabaseURL)
	}
}

// Общий DATABASE_URL — намеренное исключение: сервер у задач один, разводит их search_path.
func TestTaskWithoutOwnDatabaseURLUsesSharedOne(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "pipeline.env")
	if err := os.WriteFile(envPath, []byte("APP_ENV=local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", envPath)
	t.Setenv("DATABASE_URL", "postgres://shared-host/seo")
	unsetEnv(t, "PPROF_1_DATABASE_URL")

	cfg, err := Load(TaskDefaults{OutputDir: "tasks/pprof_1/output", EnvPrefix: "PPROF_1_"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://shared-host/seo" {
		t.Fatalf("DatabaseURL = %q, want postgres://shared-host/seo", cfg.DatabaseURL)
	}
}

// Площадка привязана к задаче: сайтов у проекта несколько, и адрес одной задачи не имеет
// права протечь в другую. Иначе pprof_1 однажды опубликует статью на сайт task_1.
func TestWordPressCredentialsAreTaskScoped(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "pipeline.env")
	if err := os.WriteFile(envPath, []byte("DATABASE_URL=postgres://from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", envPath)
	t.Setenv("WORDPRESS_URL", "https://task-1-site.ru")
	t.Setenv("WORDPRESS_USERNAME", "task-1-user")
	t.Setenv("WORDPRESS_APP_PASSWORD", "task-1-password")
	t.Setenv("PPROF_1_WORDPRESS_URL", "https://pprof-site.ru")
	t.Setenv("PPROF_1_WORDPRESS_USERNAME", "pprof-user")
	t.Setenv("PPROF_1_WORDPRESS_APP_PASSWORD", "pprof-password")

	prefixed, err := Load(TaskDefaults{OutputDir: "tasks/pprof_1/output", EnvPrefix: "PPROF_1_"})
	if err != nil {
		t.Fatal(err)
	}
	want := WordPressConfig{
		BaseURL: "https://pprof-site.ru", Username: "pprof-user", AppPassword: "pprof-password",
	}
	if prefixed.WordPress != want {
		t.Fatalf("WordPress = %+v, want %+v: задача взяла чужую площадку", prefixed.WordPress, want)
	}

	// Задача без префикса читает исторические имена — это её собственная площадка, а не запасная.
	historical, err := Load(testTaskDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if historical.WordPress.BaseURL != "https://task-1-site.ru" {
		t.Fatalf("BaseURL = %q, want https://task-1-site.ru", historical.WordPress.BaseURL)
	}
}

// Сообщение обязано называть ту переменную, которую человек правит: у pprof_1 она с префиксом.
func TestValidateWordPressNamesTaskScopedVariables(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "pipeline.env")
	if err := os.WriteFile(envPath, []byte("DATABASE_URL=postgres://from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", envPath)
	unsetEnv(t, "PPROF_1_WORDPRESS_URL")
	unsetEnv(t, "PPROF_1_WORDPRESS_USERNAME")
	unsetEnv(t, "PPROF_1_WORDPRESS_APP_PASSWORD")

	cfg, err := Load(TaskDefaults{OutputDir: "tasks/pprof_1/output", EnvPrefix: "PPROF_1_"})
	if err != nil {
		t.Fatal(err)
	}
	validateErr := cfg.ValidateWordPress()
	if validateErr == nil || !strings.Contains(validateErr.Error(), "PPROF_1_WORDPRESS_URL") {
		t.Fatalf("ValidateWordPress() = %v, want упоминание PPROF_1_WORDPRESS_URL", validateErr)
	}
}

// Проверка подключения не трогает базу: она нужна ровно тогда, когда докер потушен.
func TestValidateWordPressDoesNotRequireDatabase(t *testing.T) {
	cfg := Config{WordPress: WordPressConfig{
		BaseURL: "https://dpoprof.ru", Username: "admin", AppPassword: "password",
	}}
	if err := cfg.ValidateWordPress(); err != nil {
		t.Fatalf("ValidateWordPress() = %v, want nil без DATABASE_URL", err)
	}
}
