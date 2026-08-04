package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
)

type retryRepository interface {
	ListArticlesWithErrors(context.Context) ([]article.ArticleError, error)
	GetArticleByExternalID(context.Context, string) (article.Article, error)
	ClearArticleErrorForRetry(context.Context, int64) (bool, error)
	SaveError(context.Context, int64, error) error
}

func runRetry(ctx context.Context, repository retryRepository, externalID string, runDemo func(context.Context, string) error, logger *slog.Logger) error {
	var selected []article.ArticleError
	if externalID == "" {
		var err error
		selected, err = repository.ListArticlesWithErrors(ctx)
		if err != nil {
			return err
		}
	} else {
		selectedArticle, err := repository.GetArticleByExternalID(ctx, externalID)
		if err != nil {
			return err
		}
		selected = []article.ArticleError{{Article: selectedArticle}}
	}

	completed, failed, skipped := 0, 0, 0
	var batchErr error
	for _, item := range selected {
		articleLogger := logger.With("article_id", item.ID, "external_id", item.ExternalID)
		if item.Status == "completed" {
			skipped++
			articleLogger.Warn("article retry skipped", "reason", "already_completed")
			continue
		}
		if item.ErrorMessage == nil || strings.TrimSpace(*item.ErrorMessage) == "" {
			skipped++
			articleLogger.Info("article retry skipped", "reason", "no_recorded_error")
			continue
		}
		articleLogger.Info("article retry started", "status", item.Status, "current_step", optionalText(item.CurrentStep))
		cleared, err := repository.ClearArticleErrorForRetry(ctx, item.ID)
		if err != nil {
			failed++
			batchErr = errors.Join(batchErr, err)
			articleLogger.Error("article retry failed", "error", err)
			continue
		}
		if !cleared {
			skipped++
			articleLogger.Info("article retry skipped", "reason", "no_recorded_error")
			continue
		}
		if err := runDemo(ctx, item.ExternalID); err != nil {
			if saveErr := saveRetryErrorIfNeeded(ctx, repository, item.ExternalID, item.ID, err); saveErr != nil {
				err = errors.Join(err, saveErr)
			}
			failed++
			batchErr = errors.Join(batchErr, fmt.Errorf("retry article_id=%d external_id=%s: %w", item.ID, item.ExternalID, err))
			articleLogger.Error("article retry failed", "error", err)
			if isGracefulCancellation(ctx, err) {
				break
			}
			continue
		}
		completed++
		articleLogger.Info("article retry completed")
	}
	logger.Info("retry failed articles finished", "total", len(selected), "completed", completed, "failed", failed, "skipped", skipped)
	return batchErr
}

func saveRetryErrorIfNeeded(ctx context.Context, repository retryRepository, externalID string, articleID int64, processingErr error) error {
	selected, err := repository.GetArticleByExternalID(ctx, externalID)
	if err == nil && selected.ErrorMessage != nil && strings.TrimSpace(*selected.ErrorMessage) != "" {
		return nil
	}
	if err != nil {
		return errors.Join(err, repository.SaveError(ctx, articleID, processingErr))
	}
	return repository.SaveError(ctx, articleID, processingErr)
}
