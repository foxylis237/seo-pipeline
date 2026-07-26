package main

import (
	"context"
	"log/slog"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/repository"
)

func runPipeline(
	ctx context.Context,
	articleRepository *repository.ArticleRepository,
	cfg config.Config,
	logger *slog.Logger,
) error {
	// Пока параметры понадобятся на следующем этапе,
	// когда подключим получение статьи из БД и Keys.so.
	_ = ctx
	_ = articleRepository
	_ = cfg

	logger.Info("запуск обработки статей из PostgreSQL")

	return nil
}
