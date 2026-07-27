package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/repository"
	"github.com/foxylis237/seo-pipeline/internal/storage"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	command, err := parseCommand(os.Args)
	if err != nil {
		logger.Error(err.Error(), "available_commands", "import, run")
		os.Exit(1)
	}

	cfg := config.Load()
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
		logger.Error("ошибка выполнения команды", "error", err)
		os.Exit(1)
	}
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
