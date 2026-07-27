package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/repository"
	"github.com/foxylis237/seo-pipeline/internal/storage"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	command, err := parseCommand(os.Args)
	if err != nil {
		logger.Error(err.Error(), "available_commands", "import, run")
		os.Exit(1)
	}

	cfg := config.Load()
	logger, err = newLogger(cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
		logger.Error("не удалось настроить логирование", "error", err)
		os.Exit(1)
	}
	if err := validateConfig(command, cfg); err != nil {
		logger.Error("не удалось загрузить конфигурацию", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	pool, err := storage.NewPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("не удалось подключиться к PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	logger.Info("подключение к PostgreSQL успешно установлено")

	articleRepository := repository.NewArticleRepository(pool)

	switch command {
	case "import":
		err = runImport(ctx, articleRepository, cfg.InputFilePath, logger)

	case "run":
		err = runPipeline(ctx, articleRepository, cfg, logger)

	}

	if err != nil {
		var stageErr *keyssoRunError
		if errors.As(err, &stageErr) {
			logger.Error(
				"этап Keys.so завершён с ошибкой",
				"article_id", stageErr.articleID,
				"integration", "keysso",
				"stage", stageErr.stage,
				"duration_ms", stageErr.duration.Milliseconds(),
				"current_url", stageErr.currentURL,
				"collected_count", stageErr.collectedCount,
				"cleaned_count", stageErr.cleanedCount,
				"error", stageErr.err,
			)
		} else {
			logger.Error("ошибка выполнения команды", "error", err)
		}
		os.Exit(1)
	}
}

func newLogger(levelValue, formatValue string) (*slog.Logger, error) {
	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(levelValue)) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, fmt.Errorf("неподдерживаемый LOG_LEVEL %q", levelValue)
	}

	options := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(formatValue)) {
	case "text":
		handler = slog.NewTextHandler(os.Stdout, options)
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, options)
	default:
		return nil, fmt.Errorf("неподдерживаемый LOG_FORMAT %q", formatValue)
	}
	return slog.New(handler), nil
}

func parseCommand(args []string) (string, error) {
	if len(args) < 2 {
		return "", fmt.Errorf("не указана команда")
	}

	switch args[1] {
	case "import", "run":
		return args[1], nil
	default:
		return "", fmt.Errorf("неизвестная команда %q", args[1])
	}
}

func validateConfig(command string, cfg config.Config) error {
	if command == "import" {
		return cfg.ValidateImport()
	}
	return cfg.ValidateRun()
}
