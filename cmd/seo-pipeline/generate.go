package main

import (
	"context"
	"log/slog"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/generation"
)

func runGenerate(ctx context.Context, pipeline *generation.Pipeline, externalID string) error {
	_, err := pipeline.RunByExternalID(ctx, externalID)
	return err
}

func runDemoGenerate(ctx context.Context, pipeline *generation.Pipeline, externalID string) error {
	_, err := pipeline.RunDemoByExternalID(ctx, externalID)
	return err
}

type incompleteArticleRepository interface {
	GetNextIncomplete(context.Context) (article.Article, bool, error)
}

func runAllDemo(ctx context.Context, articleRepository incompleteArticleRepository, runArticle func(context.Context, string) error, logger *slog.Logger) error {
	completed := 0
	for {
		selected, found, err := articleRepository.GetNextIncomplete(ctx)
		if err != nil {
			logger.Error("не удалось выбрать следующую статью", "stage", "select_next_article", "completed_count", completed, "error", err)
			return err
		}
		if !found {
			logger.Info("все доступные статьи обработаны", "stage", "complete", "completed_count", completed)
			return nil
		}
		stage := "pending"
		if selected.CurrentStep != nil {
			stage = *selected.CurrentStep
		}
		articleLogger := logger.With(
			"article_id", selected.ID,
			"external_id", selected.ExternalID,
			"title", selected.Title,
			"current_stage", stage,
		)
		articleLogger.Info("обработка статьи начата", "stage", "article_start")
		articleLogger.Info("атомарный demo-этап начат", "stage", "article_generation")
		if err := runArticle(ctx, selected.ExternalID); err != nil {
			articleLogger.Error("обработка статьи остановлена с ошибкой", "stage", "article_generation", "completed_count", completed, "error", err)
			return err
		}
		completed++
		articleLogger.Info("атомарный demo-этап завершён", "stage", "article_generation")
		articleLogger.Info("обработка статьи завершена", "stage", "article_complete", "completed_count", completed)
	}
}

func runArticle(ctx context.Context, pipeline *generation.Pipeline, externalID string) error {
	_, err := pipeline.RunArticleByExternalID(ctx, externalID)
	return err
}

func runReview(ctx context.Context, pipeline *generation.Pipeline, externalID string) error {
	_, err := pipeline.RunReviewByExternalID(ctx, externalID)
	return err
}

func runFix(ctx context.Context, pipeline *generation.Pipeline, externalID string) error {
	_, err := pipeline.RunFixByExternalID(ctx, externalID)
	return err
}

func runHTML(ctx context.Context, pipeline *generation.Pipeline, externalID string) error {
	_, err := pipeline.RunHTMLByExternalID(ctx, externalID)
	return err
}
