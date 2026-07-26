// Package config отвечает за загрузку и проверку настроек приложения.
package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

// Config содержит настройки приложения.
type Config struct {
	DatabaseURL string

	KeysSOEmail    string
	KeysSOPassword string
}

// Load загружает настройки из .env и переменных окружения.
//
// Переменные окружения имеют приоритет над значениями из файла .env.
func Load() (Config, error) {
	// Загружаем .env для локальной разработки.
	// Если файла нет, продолжаем работу с системными переменными окружения.
	_ = godotenv.Load()

	cfg := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),

		KeysSOEmail:    os.Getenv("KEYS_SO_EMAIL"),
		KeysSOPassword: os.Getenv("KEYS_SO_PASSWORD"),
	}

	// Без строки подключения приложение не сможет работать с PostgreSQL.
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}

	if cfg.KeysSOEmail == "" {
		return Config{}, fmt.Errorf("KEYS_SO_EMAIL is required")
	}

	if cfg.KeysSOPassword == "" {
		return Config{}, fmt.Errorf("KEYS_SO_PASSWORD is required")
	}

	return cfg, nil
}
