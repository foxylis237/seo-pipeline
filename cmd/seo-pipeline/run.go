package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/article"
	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/integrations/keysso"
	"github.com/foxylis237/seo-pipeline/internal/repository"
)

type keyssoRunError struct {
	articleID      int64
	stage          string
	currentURL     string
	duration       time.Duration
	collectedCount int
	cleanedCount   int
	err            error
}

func (e *keyssoRunError) Error() string { return e.err.Error() }
func (e *keyssoRunError) Unwrap() error { return e.err }

func runPipeline(
	ctx context.Context,
	articleRepository *repository.ArticleRepository,
	cfg config.Config,
	logger *slog.Logger,
) error {
	selected, found, err := articleRepository.GetFirst(ctx)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("первая статья не найдена")
	}

	stageStarted := time.Now()
	stageLogger := logger.With("article_id", selected.ID, "integration", "keysso")
	stageLogger.Info("обработка статьи начата", keyssoLogFields("article_start", stageStarted, "", 0, 0)...)
	if strings.TrimSpace(selected.ReferenceURL) == "" {
		return savePipelineError(
			ctx,
			articleRepository,
			selected.ID,
			newKeyssoRunError(selected.ID, "validate_reference_url", stageStarted, "", 0, 0, fmt.Errorf("у первой статьи пустой reference_url")),
		)
	}
	stageLogger.Info("этап Keys.so начат", keyssoLogFields("start", stageStarted, "", 0, 0)...)

	service := keysso.New(keysso.Config{
		ArticleID: selected.ID,
		Email:     cfg.KeysSOEmail,
		Password:  cfg.KeysSOPassword,
		Headless:  true,
	}, logger)
	collectResult, err := service.CollectCleanKeywords(ctx, selected.ReferenceURL)
	if err != nil {
		stage := "collect"
		currentURL := ""
		collectedCount := 0
		cleanedCount := 0
		var integrationErr *keysso.StageError
		if errors.As(err, &integrationErr) {
			stage = integrationErr.Stage
			currentURL = integrationErr.CurrentURL
			collectedCount = integrationErr.CollectedCount
			cleanedCount = integrationErr.CleanedCount
		}
		return savePipelineError(ctx, articleRepository, selected.ID, newKeyssoRunError(
			selected.ID, stage, stageStarted, currentURL, collectedCount, cleanedCount, err,
		))
	}
	if len(collectResult.CleanedKeywords) == 0 {
		return savePipelineError(
			ctx,
			articleRepository,
			selected.ID,
			newKeyssoRunError(selected.ID, "validate_result", stageStarted, "", collectResult.CollectedCount, 0, fmt.Errorf("Keys.so вернул пустой список очищенных запросов")),
		)
	}
	if err := articleRepository.SaveCleanedKeywords(ctx, selected.ID, collectResult.CleanedKeywords); err != nil {
		return savePipelineError(
			ctx,
			articleRepository,
			selected.ID,
			newKeyssoRunError(selected.ID, "save_result", stageStarted, "", collectResult.CollectedCount, len(collectResult.CleanedKeywords), fmt.Errorf("сохранить результат в PostgreSQL: %w", err)),
		)
	}
	stageLogger.Info("результат Keys.so сохранён", keyssoLogFields("save_result", stageStarted, "", collectResult.CollectedCount, len(collectResult.CleanedKeywords))...)
	stageLogger.Info("этап Keys.so завершён", keyssoLogFields("complete", stageStarted, "", collectResult.CollectedCount, len(collectResult.CleanedKeywords))...)
	printKeysSOResult(os.Stdout, selected, collectResult)

	return nil
}

func newKeyssoRunError(articleID int64, stage string, startedAt time.Time, currentURL string, collectedCount, cleanedCount int, err error) *keyssoRunError {
	return &keyssoRunError{
		articleID: articleID, stage: stage, currentURL: currentURL,
		duration: time.Since(startedAt), collectedCount: collectedCount,
		cleanedCount: cleanedCount, err: err,
	}
}

func keyssoLogFields(stage string, startedAt time.Time, currentURL string, collectedCount, cleanedCount int) []any {
	return []any{
		"stage", stage,
		"duration_ms", time.Since(startedAt).Milliseconds(),
		"current_url", currentURL,
		"collected_count", collectedCount,
		"cleaned_count", cleanedCount,
	}
}

func printKeysSOResult(writer io.Writer, selected article.Article, result keysso.CollectResult) {
	currentStage := "<none>"
	if selected.CurrentStep != nil {
		currentStage = *selected.CurrentStep
	}

	fmt.Fprintln(writer, "=== KEYS.SO RESULT ===")
	fmt.Fprintf(writer, "Article ID: %d\n", selected.ID)
	fmt.Fprintf(writer, "Title: %s\n", selected.Title)
	fmt.Fprintf(writer, "Reference URL: %s\n", selected.ReferenceURL)
	fmt.Fprintf(writer, "Collected queries: %d\n", result.CollectedCount)
	fmt.Fprintf(writer, "Cleaned queries: %d\n", len(result.CleanedKeywords))
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Keywords:")
	for index, keyword := range result.CleanedKeywords {
		fmt.Fprintf(writer, "%d. %s\n", index+1, keyword)
	}
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Saved to PostgreSQL: yes")
	fmt.Fprintf(writer, "Current stage: %s\n", currentStage)
	fmt.Fprintln(writer, "======================")
}

func savePipelineError(
	ctx context.Context,
	articleRepository *repository.ArticleRepository,
	articleID int64,
	processingErr error,
) error {
	if err := articleRepository.SaveError(ctx, articleID, processingErr); err != nil {
		return errors.Join(processingErr, fmt.Errorf("дополнительно не удалось сохранить ошибку: %w", err))
	}
	return processingErr
}
