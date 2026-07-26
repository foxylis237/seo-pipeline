package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/repository"
	"github.com/foxylis237/seo-pipeline/internal/storage"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	if len(os.Args) < 2 {
		logger.Error("не указана команда")
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
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

	switch os.Args[1] {
	case "import":
		err = runImport(ctx, articleRepository, logger)

	case "run":
		err = runPipeline(ctx, articleRepository, cfg, logger)

	default:
		logger.Error("неизвестная команда", "command", os.Args[1])
		os.Exit(1)
	}

	if err != nil {
		logger.Error("ошибка выполнения команды", "error", err)
		os.Exit(1)
	}
}
