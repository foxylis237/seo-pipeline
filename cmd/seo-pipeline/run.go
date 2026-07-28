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
	"github.com/foxylis237/seo-pipeline/internal/generation"
	"github.com/foxylis237/seo-pipeline/internal/integrations/arsenkin"
	"github.com/foxylis237/seo-pipeline/internal/integrations/keysso"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/output"
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
	generationPipeline *generation.Pipeline,
	writer *articleoutput.Writer,
	targetExternalID string,
) error {
	selectedArticles, err := articleRepository.GetAll(ctx)
	if err != nil {
		return err
	}
	if len(selectedArticles) == 0 {
		return fmt.Errorf("статьи не найдены")
	}
	processed := 0
	for _, selected := range selectedArticles {
		if targetExternalID != "" && selected.ExternalID != targetExternalID {
			continue
		}
		if err := runArticlePipeline(ctx, articleRepository, cfg, logger, generationPipeline, writer, selected); err != nil {
			return err
		}
		processed++
	}
	if targetExternalID != "" && processed == 0 {
		return fmt.Errorf("статья с external_id %q не найдена", targetExternalID)
	}
	return nil
}

func runArticlePipeline(
	ctx context.Context,
	articleRepository *repository.ArticleRepository,
	cfg config.Config,
	logger *slog.Logger,
	generationPipeline *generation.Pipeline,
	writer *articleoutput.Writer,
	selected article.Article,
) error {
	articleStarted := time.Now()
	articleLogger := logger.With(
		"article_id", selected.ID,
		"external_id", selected.ExternalID,
		"title", selected.Title,
		"model", cfg.GeminiModel,
	)
	articleLogger.Info("обработка статьи начата", "stage", "article_start")
	if err := articleRepository.ResetArticleForRun(ctx, selected.ID); err != nil {
		articleLogger.Error("ошибка сброса данных статьи", "stage", "reset", "error", err)
		return err
	}
	if err := writer.ResetArticle(selected.ExternalID, selected.Slug); err != nil {
		articleLogger.Error("ошибка сброса файлов статьи", "stage", "reset", "error", err)
		return savePipelineError(ctx, articleRepository, selected.ID, fmt.Errorf("reset output for article %d: %w", selected.ID, err))
	}

	stageStarted := time.Now()
	stageLogger := logger.With("article_id", selected.ID, "integration", "keysso")
	stageLogger.Info("обработка статьи начата", keyssoLogFields("article_start", stageStarted, "", 0, 0)...)
	if strings.TrimSpace(selected.ReferenceURL) == "" {
		return savePipelineError(
			ctx,
			articleRepository,
			selected.ID,
			newKeyssoRunError(selected.ID, "validate_reference_url", stageStarted, "", 0, 0, fmt.Errorf("у статьи пустой reference_url")),
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

	arsenkinStarted := time.Now()
	arsenkinLogger := logger.With("article_id", selected.ID, "integration", "arsenkin")
	arsenkinService := arsenkin.New(arsenkin.Config{
		ArticleID: selected.ID,
		Email:     cfg.ArsenkinEmail,
		Password:  cfg.ArsenkinPassword,
		Headless:  cfg.ArsenkinHeadless,
	}, logger)
	arsenkinResult, err := arsenkinService.CollectResearch(ctx, collectResult.CleanedKeywords)
	if err != nil {
		return savePipelineError(ctx, articleRepository, selected.ID, err)
	}
	if err := articleRepository.SaveArsenkinResearch(
		ctx,
		selected.ID,
		arsenkinResult.WordstatKeywords,
		arsenkinResult.LSIWords,
		arsenkinResult.CompetitorStructure,
	); err != nil {
		return savePipelineError(ctx, articleRepository, selected.ID, &arsenkin.StageError{
			ArticleID:  selected.ID,
			Stage:      "save_result",
			CurrentURL: "https://arsenkin.ru/tools/copyrighters/",
			Duration:   time.Since(arsenkinStarted),
			Err:        fmt.Errorf("сохранить результаты Arsenkin в PostgreSQL: %w", err),
		})
	}
	arsenkinLogger.Info("данные сохранены", "stage", "save_result", "duration_ms", time.Since(arsenkinStarted).Milliseconds(), "current_url", "https://arsenkin.ru/tools/copyrighters/", "wordstat_count", len(arsenkinResult.WordstatKeywords), "lsi_count", len(arsenkinResult.LSIWords), "competitor_structure_length", len(arsenkinResult.CompetitorStructure))
	arsenkinLogger.Info("этап завершён", "stage", "complete", "duration_ms", time.Since(arsenkinStarted).Milliseconds(), "current_url", "https://arsenkin.ru/tools/copyrighters/", "wordstat_count", len(arsenkinResult.WordstatKeywords), "lsi_count", len(arsenkinResult.LSIWords), "competitor_structure_length", len(arsenkinResult.CompetitorStructure))
	printArsenkinResult(os.Stdout, selected, arsenkinResult)

	_, err = generationPipeline.Run(ctx, article.GenerationInput{
		Article:             selected,
		CompetitorStructure: arsenkinResult.CompetitorStructure,
		WordstatKeywords:    arsenkinResult.WordstatKeywords,
		LSIWords:            arsenkinResult.LSIWords,
	})
	if err != nil {
		return savePipelineError(ctx, articleRepository, selected.ID, err)
	}
	articleLogger.Info("обработка статьи завершена", "stage", "complete", "duration_ms", time.Since(articleStarted).Milliseconds())

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

func printArsenkinResult(writer io.Writer, selected article.Article, result arsenkin.Result) {
	fmt.Fprintln(writer, "==================================================")
	fmt.Fprintln(writer, "                ARSENKIN RESULT")
	fmt.Fprintln(writer, "==================================================")
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "Article ID: %d\n", selected.ID)
	fmt.Fprintf(writer, "Title: %s\n", selected.Title)
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "--------------- WORDSTAT TOP ----------------")
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "%-42s %s\n", "Запрос", "Частотность")
	fmt.Fprintln(writer)
	for _, keyword := range result.WordstatKeywords {
		fmt.Fprintf(writer, "%-42s %d\n", keyword.Query, keyword.Frequency)
	}
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "--------------- LSI ------------------------")
	fmt.Fprintln(writer)
	for _, word := range result.LSIWords {
		fmt.Fprintln(writer, word)
	}
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "----------- COMPETITOR STRUCTURE -----------")
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, result.CompetitorStructure)
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "==================================================")
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "Wordstat keywords: %d\n", len(result.WordstatKeywords))
	fmt.Fprintf(writer, "LSI words: %d\n", len(result.LSIWords))
	fmt.Fprintf(writer, "Structure length: %d chars\n", len([]rune(result.CompetitorStructure)))
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "==================================================")
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
