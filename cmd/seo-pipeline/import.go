package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/importer"
)

type articleImporter interface {
	Import(context.Context, article.Input) (article.Article, bool, error)
}

func runImport(
	ctx context.Context,
	articleRepository articleImporter,
	inputFilePath string,
	logger *slog.Logger,
) error {
	articles, err := importer.ReadArticles(inputFilePath)
	if err != nil {
		return fmt.Errorf("прочитать Excel: %w", err)
	}

	found := len(articles)
	added := 0
	skipped := 0
	logger.Info("Excel успешно прочитан", "found_count", found)

	for _, input := range articles {
		createdArticle, created, err := articleRepository.Import(ctx, input)
		if err != nil {
			return fmt.Errorf(
				"сохранить статью из Excel с ID %d и названием %q: %w",
				input.ExcelID,
				input.Title,
				err,
			)
		}

		if !created {
			skipped++
			logger.Info(
				"ранее импортированная статья пропущена",
				"article_id", createdArticle.ID,
				"external_id", createdArticle.ExternalID,
				"title", createdArticle.Title,
			)
			continue
		}
		added++
		logger.Info(
			"новая статья импортирована",
			"article_id", createdArticle.ID,
			"external_id", createdArticle.ExternalID,
			"title", input.Title,
		)
	}

	logger.Info(
		"импорт статей завершён",
		"found_count", found,
		"added_count", added,
		"skipped_count", skipped,
	)

	return nil
}
