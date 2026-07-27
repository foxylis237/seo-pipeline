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
	// Keys.so будет подключён после ручной проверки реальных селекторов.
	_ = cfg

	logger.Info("запуск обработки статей из PostgreSQL")
	claimed, found, err := articleRepository.ClaimNextPending(ctx)
	if err != nil {
		return err
	}
	if !found {
		logger.Info("нет статей, ожидающих обработки")
		return nil
	}

	logger.Info(
		"статья получена для обработки",
		"article_id", claimed.ID,
		"external_id", claimed.ExternalID,
		"title", claimed.Title,
	)

	return nil
}
