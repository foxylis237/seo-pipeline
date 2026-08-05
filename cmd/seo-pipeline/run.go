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

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/integrations/arsenkin"
	"github.com/foxylis237/seo-pipeline/internal/integrations/keysso"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/diagnostics"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/tasks/task1/output"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/repository"
)

// minWordstatMembershipRatio is the share of Wordstat phrases that must come from the list
// submitted for this very article. Arsenkin answers with the phrases it was given, so a lower
// share means the page showed the result of another article's task.
const minWordstatMembershipRatio = 0.5

type keyssoRunError struct {
	articleID      int64
	stage          string
	currentURL     string
	duration       time.Duration
	collectedCount int
	cleanedCount   int
	err            error
}

type prepareRepository interface {
	PrepareArticleForRun(context.Context, int64) error
	SavePreparedResearch(context.Context, int64, []string, []article.KeywordFrequency, []string, string) error
	SaveError(context.Context, int64, error) error
	GetArticleTrace(context.Context, int64) (article.Trace, error)
	GetArticleInput(context.Context, int64) (article.Input, error)
}

// prepareArtifactWriter saves prepare diagnostics next to the article artifacts.
type prepareArtifactWriter interface {
	SaveDiagnostics(externalID, slug, subdirectory, name string, payload any) (string, error)
	ResetDiagnostics(externalID, slug, subdirectory string) error
}

type keyssoCollector interface {
	CollectCleanKeywords(context.Context, string) (keysso.CollectResult, error)
}

type arsenkinCollector interface {
	CollectResearch(context.Context, []string) (arsenkin.Result, error)
}

func (e *keyssoRunError) Error() string { return e.err.Error() }
func (e *keyssoRunError) Unwrap() error { return e.err }

func runPrepare(
	ctx context.Context,
	articleRepository *repository.ArticleRepository,
	cfg config.Config,
	logger *slog.Logger,
	writer *articleoutput.Writer,
	logRouter *diagnostics.ArticleLogRouter,
	targetExternalID string,
) error {
	if targetExternalID == "" {
		selectedArticles, err := articleRepository.GetPendingForOperation(ctx, "prepare")
		if err != nil {
			return err
		}
		byExternalID := make(map[string]article.Article, len(selectedArticles))
		for _, selected := range selectedArticles {
			byExternalID[selected.ExternalID] = selected
		}
		return runSelectedArticles(ctx, selectedArticles, "prepare", func(ctx context.Context, externalID string) error {
			return prepareArticle(ctx, articleRepository, cfg, logger, writer, logRouter, byExternalID[externalID])
		}, logger)
	}
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
		if err := prepareArticle(ctx, articleRepository, cfg, logger, writer, logRouter, selected); err != nil {
			return err
		}
		processed++
	}
	if targetExternalID != "" && processed == 0 {
		return fmt.Errorf("статья с external_id %q не найдена", targetExternalID)
	}
	return nil
}

func prepareArticle(
	ctx context.Context,
	articleRepository *repository.ArticleRepository,
	cfg config.Config,
	logger *slog.Logger,
	writer *articleoutput.Writer,
	logRouter *diagnostics.ArticleLogRouter,
	selected article.Article,
) error {
	// Каталога статьи может ещё не быть: сообщаем роутеру slug, чтобы prepare.log
	// открылся с первой же записи.
	logRouter.Register(selected.ID, selected.ExternalID, selected.Slug)
	return prepareArticleWithCollectors(
		ctx,
		articleRepository,
		cfg,
		logger,
		writer,
		selected,
		keysso.New(keysso.Config{
			ArticleID:  selected.ID,
			ExternalID: selected.ExternalID,
			Email:      cfg.KeysSOEmail,
			Password:   cfg.KeysSOPassword,
			Headless:   true,
		}, logger),
		arsenkin.New(arsenkin.Config{
			ArticleID: selected.ID,
			Email:     cfg.ArsenkinEmail,
			Password:  cfg.ArsenkinPassword,
			Headless:  cfg.ArsenkinHeadless,
		}, logger),
	)
}

// prepareArticleWithCollectors runs prepare for one article and always leaves a report of
// what happened in <external_id>-<slug>/prepare/, успешным был прогон или нет.
func prepareArticleWithCollectors(
	ctx context.Context,
	articleRepository prepareRepository,
	cfg config.Config,
	logger *slog.Logger,
	artifacts prepareArtifactWriter,
	selected article.Article,
	keyssoService keyssoCollector,
	arsenkinService arsenkinCollector,
) error {
	report := diagnostics.NewPrepareReport(selected)
	resetPrepareDiagnostics(logger, artifacts, selected)
	stage, err := collectPreparedResearch(ctx, articleRepository, cfg, logger, artifacts, selected, keyssoService, arsenkinService, report)
	report.Finish(stage, err)
	savePrepareDiagnostics(logger, artifacts, selected, diagnostics.PrepareReportFile, report)
	return err
}

// collectPreparedResearch runs the external stages and returns the stage that failed.
func collectPreparedResearch(
	ctx context.Context,
	articleRepository prepareRepository,
	cfg config.Config,
	logger *slog.Logger,
	artifacts prepareArtifactWriter,
	selected article.Article,
	keyssoService keyssoCollector,
	arsenkinService arsenkinCollector,
	report *diagnostics.PrepareReport,
) (string, error) {
	articleStarted := time.Now()
	articleLogger := logger.With(
		"article_id", selected.ID,
		"external_id", selected.ExternalID,
		"title", selected.Title,
		"model", cfg.GeminiModel,
	)
	articleLogger.Info("обработка статьи начата", "stage", "article_start")
	if err := articleRepository.PrepareArticleForRun(ctx, selected.ID); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled) {
			return "reset", err
		}
		articleLogger.Error("ошибка сброса данных статьи", "stage", "reset", "error", err)
		return "reset", err
	}
	trace, err := articleRepository.GetArticleTrace(ctx, selected.ID)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled) {
			return "identity_trace", err
		}
		articleLogger.Error("не удалось прочитать идентичность статьи", "stage", "identity_trace", "error", err)
		return "identity_trace", err
	}
	report.UseTrace(trace)
	if mismatchErr := diagnostics.TraceMismatch(article.Trace{
		ArticleID: selected.ID, ExternalID: selected.ExternalID, Title: selected.Title,
		Keyword: trace.Keyword, ReferenceURL: selected.ReferenceURL,
	}, trace); mismatchErr != nil {
		report.Fail("identity_trace", mismatchErr.Error(), nil)
		return "identity_trace", savePipelineError(ctx, articleRepository, selected.ID, mismatchErr)
	}
	report.Pass("identity_trace", nil)
	saveArticleInputDiagnostics(ctx, articleRepository, logger, artifacts, selected)
	stageStarted := time.Now()
	stageLogger := logger.With("article_id", selected.ID, "integration", "keysso")
	stageLogger.Info("обработка статьи начата", keyssoLogFields("article_start", stageStarted, "", 0, 0)...)
	if strings.TrimSpace(selected.ReferenceURL) == "" {
		report.Fail("reference_url", "у статьи пустой reference_url", nil)
		return "validate_reference_url", savePipelineError(
			ctx,
			articleRepository,
			selected.ID,
			newKeyssoRunError(selected.ID, "validate_reference_url", stageStarted, "", 0, 0, fmt.Errorf("у статьи пустой reference_url")),
		)
	}
	report.Pass("reference_url", map[string]any{"reference_url": selected.ReferenceURL})
	stageLogger.Info("этап Keys.so начат", keyssoLogFields("start", stageStarted, "", 0, 0)...)
	diagnostics.LogStep(logger, "keysso", "before", trace)

	collectResult, err := keyssoService.CollectCleanKeywords(ctx, selected.ReferenceURL)
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
		report.Fail("keysso_collect", err.Error(), map[string]any{
			"stage": stage, "current_url": currentURL,
			"collected_count": collectedCount, "cleaned_count": cleanedCount,
		})
		return "keysso_collect", savePipelineError(ctx, articleRepository, selected.ID, newKeyssoRunError(
			selected.ID, stage, stageStarted, currentURL, collectedCount, cleanedCount, err,
		))
	}
	saveKeysSODiagnostics(logger, artifacts, selected, trace, collectResult, time.Since(stageStarted))
	if len(collectResult.CleanedKeywords) == 0 {
		report.Fail("keysso_collect", "Keys.so вернул пустой список очищенных запросов", map[string]any{
			"collected_count": collectResult.CollectedCount, "cleaned_count": 0,
		})
		return "keysso_collect", savePipelineError(
			ctx,
			articleRepository,
			selected.ID,
			newKeyssoRunError(selected.ID, "validate_result", stageStarted, "", collectResult.CollectedCount, 0, fmt.Errorf("Keys.so вернул пустой список очищенных запросов")),
		)
	}
	report.Pass("keysso_collect", map[string]any{
		"collected_count": collectResult.CollectedCount,
		"cleaned_count":   len(collectResult.CleanedKeywords),
		"fingerprint":     diagnostics.Fingerprint(strings.Join(collectResult.CleanedKeywords, "\n")),
	})
	stageLogger.Info("результат Keys.so собран", keyssoLogFields("collect_result", stageStarted, "", collectResult.CollectedCount, len(collectResult.CleanedKeywords))...)
	stageLogger.Info("этап Keys.so завершён", keyssoLogFields("complete", stageStarted, "", collectResult.CollectedCount, len(collectResult.CleanedKeywords))...)
	diagnostics.LogStep(logger, "keysso", "after", trace,
		"collected_count", collectResult.CollectedCount,
		"cleaned_count", len(collectResult.CleanedKeywords),
		"keywords_fingerprint", diagnostics.Fingerprint(strings.Join(collectResult.CleanedKeywords, "\n")),
		"keywords_sample", diagnostics.Sample(collectResult.CleanedKeywords, 5),
	)
	if relevanceErr := checkKeywordRelevance(articleLogger, trace, collectResult, stageStarted, report); relevanceErr != nil {
		return "keyword_relevance", savePipelineError(ctx, articleRepository, selected.ID, relevanceErr)
	}

	arsenkinStarted := time.Now()
	arsenkinLogger := logger.With("article_id", selected.ID, "integration", "arsenkin")
	diagnostics.LogStep(logger, "arsenkin", "before", trace,
		"submitted_count", len(collectResult.CleanedKeywords),
		"submitted_fingerprint", diagnostics.Fingerprint(strings.Join(collectResult.CleanedKeywords, "\n")),
	)
	arsenkinResult, err := arsenkinService.CollectResearch(ctx, collectResult.CleanedKeywords)
	if err != nil {
		report.Fail("arsenkin_collect", err.Error(), map[string]any{"submitted_count": len(collectResult.CleanedKeywords)})
		return "arsenkin_collect", savePipelineError(ctx, articleRepository, selected.ID, err)
	}
	wordstatKeywords := make([]article.KeywordFrequency, len(arsenkinResult.WordstatKeywords))
	returnedQueries := make([]string, len(arsenkinResult.WordstatKeywords))
	for index, keyword := range arsenkinResult.WordstatKeywords {
		wordstatKeywords[index] = article.KeywordFrequency{Query: keyword.Query, Frequency: keyword.Frequency}
		returnedQueries[index] = keyword.Query
	}
	diagnostics.LogStep(logger, "arsenkin", "after", trace,
		"wordstat_count", len(wordstatKeywords),
		"lsi_count", len(arsenkinResult.LSIWords),
		"competitor_structure_length", len([]rune(arsenkinResult.CompetitorStructure)),
		"competitor_structure_fingerprint", diagnostics.Fingerprint(arsenkinResult.CompetitorStructure),
		"lsi_fingerprint", diagnostics.Fingerprint(strings.Join(arsenkinResult.LSIWords, "\n")),
		"wordstat_sample", diagnostics.Sample(returnedQueries, 5),
	)
	saveArsenkinDiagnostics(logger, artifacts, selected, trace, collectResult.CleanedKeywords, diagnostics.ArsenkinResult{
		WordstatKeywords: wordstatKeywords, CopywriterQueries: arsenkinResult.CopywriterQueries,
		LSIWords: arsenkinResult.LSIWords, CompetitorStructure: arsenkinResult.CompetitorStructure,
	}, time.Since(arsenkinStarted))
	report.Pass("arsenkin_collect", map[string]any{
		"wordstat_count":                   len(wordstatKeywords),
		"lsi_count":                        len(arsenkinResult.LSIWords),
		"competitor_structure_length":      len([]rune(arsenkinResult.CompetitorStructure)),
		"competitor_structure_fingerprint": diagnostics.Fingerprint(arsenkinResult.CompetitorStructure),
	})
	if membershipErr := checkWordstatMembership(articleLogger, trace, collectResult.CleanedKeywords, returnedQueries, arsenkinStarted, report); membershipErr != nil {
		return "wordstat_membership", savePipelineError(ctx, articleRepository, selected.ID, membershipErr)
	}
	if err := articleRepository.SavePreparedResearch(
		ctx,
		selected.ID,
		collectResult.CleanedKeywords,
		wordstatKeywords,
		arsenkinResult.LSIWords,
		arsenkinResult.CompetitorStructure,
	); err != nil {
		report.Fail("save_research", err.Error(), nil)
		return "save_research", savePipelineError(ctx, articleRepository, selected.ID, &arsenkin.StageError{
			ArticleID:  selected.ID,
			Stage:      "save_result",
			CurrentURL: "https://arsenkin.ru/tools/copyrighters/",
			Duration:   time.Since(arsenkinStarted),
			Err:        fmt.Errorf("сохранить результаты Arsenkin в PostgreSQL: %w", err),
		})
	}
	savedTrace, traceErr := articleRepository.GetArticleTrace(ctx, selected.ID)
	if traceErr != nil {
		report.Fail("save_research", traceErr.Error(), nil)
		return "save_research", savePipelineError(ctx, articleRepository, selected.ID, traceErr)
	}
	if mismatchErr := diagnostics.TraceMismatch(trace, savedTrace); mismatchErr != nil {
		report.Fail("save_research", mismatchErr.Error(), nil)
		return "save_research", savePipelineError(ctx, articleRepository, selected.ID, mismatchErr)
	}
	report.Pass("save_research", nil)
	diagnostics.LogStep(logger, "save_research", "after", savedTrace,
		"competitor_structure_fingerprint", diagnostics.Fingerprint(arsenkinResult.CompetitorStructure),
	)
	stageLogger.Info("результат Keys.so сохранён", keyssoLogFields("save_result", stageStarted, "", collectResult.CollectedCount, len(collectResult.CleanedKeywords))...)
	arsenkinLogger.Info("данные сохранены", "stage", "save_result", "duration_ms", time.Since(arsenkinStarted).Milliseconds(), "current_url", "https://arsenkin.ru/tools/copyrighters/", "wordstat_count", len(arsenkinResult.WordstatKeywords), "lsi_count", len(arsenkinResult.LSIWords), "competitor_structure_length", len(arsenkinResult.CompetitorStructure))
	arsenkinLogger.Info("этап завершён", "stage", "complete", "duration_ms", time.Since(arsenkinStarted).Milliseconds(), "current_url", "https://arsenkin.ru/tools/copyrighters/", "wordstat_count", len(arsenkinResult.WordstatKeywords), "lsi_count", len(arsenkinResult.LSIWords), "competitor_structure_length", len(arsenkinResult.CompetitorStructure))
	printKeysSOResult(os.Stdout, selected, collectResult)
	printArsenkinResult(os.Stdout, selected, arsenkinResult)

	articleLogger.Info("подготовка статьи завершена", "stage", "complete", "duration_ms", time.Since(articleStarted).Milliseconds())

	return "", nil
}

// resetPrepareDiagnostics drops the diagnostics of the previous run so that a new report
// never stands next to files another run left behind. Логи статьи и результаты генерации
// остаются на месте: очищается только каталог prepare.
func resetPrepareDiagnostics(logger *slog.Logger, artifacts prepareArtifactWriter, selected article.Article) {
	if artifacts == nil {
		return
	}
	if err := artifacts.ResetDiagnostics(selected.ExternalID, selected.Slug, articleoutput.PrepareSubdirectory); err != nil {
		logger.Warn("не удалось очистить диагностику предыдущего прогона",
			"stage", "prepare_diagnostics", "article_id", selected.ID, "external_id", selected.ExternalID, "error", err)
	}
}

// savePrepareDiagnostics writes one diagnostics file of the prepare run. A diagnostics
// failure never fails the article: losing a report must not discard a paid external run.
func savePrepareDiagnostics(logger *slog.Logger, artifacts prepareArtifactWriter, selected article.Article, name string, payload any) {
	if artifacts == nil {
		return
	}
	path, err := artifacts.SaveDiagnostics(selected.ExternalID, selected.Slug, articleoutput.PrepareSubdirectory, name, payload)
	if err != nil {
		logger.Warn("не удалось сохранить диагностику prepare",
			"stage", "prepare_diagnostics", "article_id", selected.ID, "external_id", selected.ExternalID,
			"file", name, "error", err)
		return
	}
	logger.Info("диагностика prepare сохранена",
		"stage", "prepare_diagnostics", "article_id", selected.ID, "external_id", selected.ExternalID,
		"file", name, "path", path)
}

func saveArticleInputDiagnostics(
	ctx context.Context,
	articleRepository prepareRepository,
	logger *slog.Logger,
	artifacts prepareArtifactWriter,
	selected article.Article,
) {
	input, err := articleRepository.GetArticleInput(ctx, selected.ID)
	if err != nil {
		logger.Warn("не удалось прочитать входные данные статьи для диагностики",
			"stage", "prepare_diagnostics", "article_id", selected.ID, "external_id", selected.ExternalID, "error", err)
		return
	}
	savePrepareDiagnostics(logger, artifacts, selected, diagnostics.InputFile, diagnostics.NewInputSnapshot(selected, input))
}

func saveKeysSODiagnostics(
	logger *slog.Logger,
	artifacts prepareArtifactWriter,
	selected article.Article,
	trace article.Trace,
	collected keysso.CollectResult,
	duration time.Duration,
) {
	savePrepareDiagnostics(logger, artifacts, selected, diagnostics.KeysSOFile,
		diagnostics.NewKeysSOSnapshot(trace, collected.CollectedCount, collected.CleanedKeywords, duration))
}

func saveArsenkinDiagnostics(
	logger *slog.Logger,
	artifacts prepareArtifactWriter,
	selected article.Article,
	trace article.Trace,
	submitted []string,
	result diagnostics.ArsenkinResult,
	duration time.Duration,
) {
	savePrepareDiagnostics(logger, artifacts, selected, diagnostics.ArsenkinFile,
		diagnostics.NewArsenkinSnapshot(trace, submitted, result, duration))
}

// checkKeywordRelevance verifies that collected queries are about the article at hand.
// Keys.so returns the queries of the competitor page named in reference_url, so with no
// overlap at all the browser almost certainly showed the results of another search.
func checkKeywordRelevance(logger *slog.Logger, trace article.Trace, collected keysso.CollectResult, startedAt time.Time, report *diagnostics.PrepareReport) error {
	relevance := diagnostics.CheckKeywordRelevance(trace.Keyword, trace.Title, collected.CleanedKeywords)
	logger.Info(
		"проверка соответствия запросов ключевому слову",
		append([]any{"stage", "identity_trace", "integration", "keysso"}, relevance.Fields()...)...,
	)
	blocked := relevance.KeywordBased && relevance.Matched == 0
	report.AddKeywordRelevance(relevance, blocked)
	if !blocked {
		return nil
	}
	return newKeyssoRunError(
		trace.ArticleID, "validate_relevance", startedAt, "", collected.CollectedCount, len(collected.CleanedKeywords),
		fmt.Errorf(
			"Keys.so вернул запросы, не связанные ни с ключевым словом %q, ни с названием %q статьи external_id=%s (проверено %d запросов, пример: %v)",
			trace.Keyword, trace.Title, trace.ExternalID, relevance.Checked, relevance.Unmatched,
		),
	)
}

// checkWordstatMembership verifies that Wordstat answered with the phrases submitted for
// this very article. Anything else means the page carried the result of another task.
func checkWordstatMembership(logger *slog.Logger, trace article.Trace, submitted, returned []string, startedAt time.Time, report *diagnostics.PrepareReport) error {
	membership := diagnostics.CheckQueryMembership(submitted, returned)
	logger.Info(
		"проверка происхождения запросов Wordstat",
		append([]any{"stage", "identity_trace", "integration", "arsenkin"}, membership.Fields()...)...,
	)
	blocked := membership.Returned > 0 && membership.Ratio() < minWordstatMembershipRatio
	report.AddQueryMembership(membership, blocked)
	if !blocked {
		return nil
	}
	return &arsenkin.StageError{
		ArticleID:  trace.ArticleID,
		Stage:      "validate_wordstat_membership",
		CurrentURL: "https://arsenkin.ru/tools/wordstat/",
		Duration:   time.Since(startedAt),
		Err: fmt.Errorf(
			"Wordstat вернул запросы, которые не отправлялись для статьи external_id=%s: совпало %d из %d (пример: %v)",
			trace.ExternalID, membership.Matched, membership.Returned, membership.Unmatched,
		),
	}
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
	articleRepository interface {
		SaveError(context.Context, int64, error) error
	},
	articleID int64,
	processingErr error,
) error {
	if errors.Is(ctx.Err(), context.Canceled) && errors.Is(processingErr, context.Canceled) {
		return processingErr
	}
	if err := articleRepository.SaveError(ctx, articleID, processingErr); err != nil {
		return errors.Join(processingErr, fmt.Errorf("дополнительно не удалось сохранить ошибку: %w", err))
	}
	return processingErr
}
