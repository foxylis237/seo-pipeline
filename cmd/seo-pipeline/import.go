package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/foxylis237/seo-pipeline/internal/importer"
	"github.com/foxylis237/seo-pipeline/internal/repository"
)

const inputFilePath = "input/input.xlsx"

func runImport(
	ctx context.Context,
	articleRepository *repository.ArticleRepository,
	logger *slog.Logger,
) error {
	articles, err := importer.ReadArticles(inputFilePath)
	if err != nil {
		return fmt.Errorf("прочитать Excel: %w", err)
	}

	logger.Info(
		"Excel успешно прочитан",
		"articles_count", len(articles),
	)

	for _, input := range articles {
		createdArticle, err := articleRepository.Create(ctx, input)
		if err != nil {
			return fmt.Errorf(
				"сохранить статью из Excel с ID %d и названием %q: %w",
				input.ExcelID,
				input.Title,
				err,
			)
		}

		logger.Info(
			"статья успешно сохранена",
			"article_id", createdArticle.ID,
			"excel_id", input.ExcelID,
			"title", input.Title,
		)
	}

	logger.Info(
		"импорт статей завершён",
		"articles_count", len(articles),
	)

	return nil
}