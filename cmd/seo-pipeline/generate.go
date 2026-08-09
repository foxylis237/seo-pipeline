package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/generation"
)

type pendingOperationRepository interface {
	GetPendingForOperation(context.Context, string) ([]article.Article, error)
}

func runBatchOperation(
	ctx context.Context,
	repository pendingOperationRepository,
	operation string,
	runArticle func(context.Context, string) error,
	logger *slog.Logger,
) error {
	selected, err := repository.GetPendingForOperation(ctx, operation)
	if err != nil {
		return err
	}
	return runSelectedArticles(ctx, selected, operation, runArticle, logger)
}

func runSelectedArticles(
	ctx context.Context,
	selected []article.Article,
	operation string,
	runArticle func(context.Context, string) error,
	logger *slog.Logger,
) error {
	started := time.Now()
	succeeded := 0
	failedIDs := make([]int64, 0)
	var batchErr error
	logger.Info("batch operation started", "stage", "batch_start", "found_count", len(selected))
	for _, selectedArticle := range selected {
		step := ""
		if selectedArticle.CurrentStep != nil {
			step = *selectedArticle.CurrentStep
		}
		articleLogger := logger.With(
			"article_id", selectedArticle.ID,
			"external_id", selectedArticle.ExternalID,
			"operation", operation,
			"status", selectedArticle.Status,
			"current_step", step,
		)
		articleStarted := time.Now()
		articleLogger.Info("batch article started", "stage", "article_start")
		if err := runArticle(ctx, selectedArticle.ExternalID); err != nil {
			articleLogger.Error("batch article failed", "stage", "article_failed", "duration_ms", time.Since(articleStarted).Milliseconds(), "error", err)
			if isGracefulCancellation(ctx, err) {
				return errors.Join(batchErr, err)
			}
			failedIDs = append(failedIDs, selectedArticle.ID)
			batchErr = errors.Join(batchErr, fmt.Errorf("article_id=%d external_id=%s operation=%s: %w", selectedArticle.ID, selectedArticle.ExternalID, operation, err))
			continue
		}
		succeeded++
		articleLogger.Info("batch article completed", "stage", "article_complete", "duration_ms", time.Since(articleStarted).Milliseconds())
	}
	logger.Info(
		"batch operation completed",
		"stage", "batch_complete",
		"found_count", len(selected),
		"succeeded_count", succeeded,
		"skipped_count", 0,
		"error_count", len(failedIDs),
		"failed_article_ids", failedIDs,
		"duration_ms", time.Since(started).Milliseconds(),
	)
	return batchErr
}

func runGenerate(ctx context.Context, pipeline *generation.Pipeline, externalID string) error {
	_, err := pipeline.RunByExternalID(ctx, externalID)
	return err
}

func runQuickDemoGenerate(ctx context.Context, pipeline *generation.Pipeline, externalID string) error {
	_, err := pipeline.RunQuickDemoByExternalID(ctx, externalID)
	return err
}

type incompleteArticleRepository interface {
	ClaimNextIncomplete(context.Context) (article.Article, bool, error)
}

func runAllDemo(ctx context.Context, articleRepository incompleteArticleRepository, runArticle func(context.Context, string) error, logger *slog.Logger) error {
	completed := 0
	for {
		selected, found, err := articleRepository.ClaimNextIncomplete(ctx)
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled) {
				return err
			}
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
			if errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled) {
				return err
			}
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
