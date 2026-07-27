// Package config отвечает за загрузку и проверку настроек приложения.
package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

// Config содержит настройки приложения.
type Config struct {
	DatabaseURL   string
	InputFilePath string

	KeysSOEmail    string
	KeysSOPassword string
}

// Load загружает настройки из .env и переменных окружения.
//
// Переменные окружения имеют приоритет над значениями из файла .env.
func Load() Config {
	// Загружаем .env для локальной разработки.
	// Если файла нет, продолжаем работу с системными переменными окружения.
	_ = godotenv.Load()

	cfg := Config{
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		InputFilePath: os.Getenv("INPUT_FILE_PATH"),

		KeysSOEmail:    os.Getenv("KEYS_SO_EMAIL"),
		KeysSOPassword: os.Getenv("KEYS_SO_PASSWORD"),
	}

	if cfg.InputFilePath == "" {
		cfg.InputFilePath = "input/input.xlsx"
	}

	return cfg
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

// ValidateRun проверяет настройки текущего этапа run.
// Keys.so пока не проверяется: интеграция ещё не подключена к команде.
func (c Config) ValidateRun() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	return nil
}
