package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

	cfg, err := Load()
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

	_, err := Load()
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

	cfg, err := LoadDryRun()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://seo:seo@localhost:5433/seo_dry_run?sslmode=disable" {
		t.Fatalf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.InputFilePath != "input/task_1/input.xlsx" {
		t.Fatalf("InputFilePath = %q", cfg.InputFilePath)
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

	cfg, err := Load()
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

	_, err := Load()
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

	cfg, err := Load()
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
