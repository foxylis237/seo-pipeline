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
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/diagnostics"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/repository"
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
	GetManualKeywords(context.Context, int64) ([]string, error)
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
	// CleanKeywords очищает запросы, полученные не от Keys.so. Метод нужен резервному
	// источнику: правила очистки остаются Keys.so-шными, меняется только происхождение
	// исходного списка.
	CleanKeywords(context.Context, []string) (keysso.CollectResult, error)
}

// keywordsFallback подбирает исходные запросы, когда Keys.so не нашёл у конкурента ни одного.
// nil означает «резервного источника в этом прогоне нет» — тогда пустой результат Keys.so
// остаётся ошибкой этапа, как и раньше.
type keywordsFallback interface {
	RawKeywords(ctx context.Context, articleName string) ([]string, error)
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
	newFallback keywordsFallbackFactory,
	debugDirs diagnosticsDirs,
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
			return prepareArticle(ctx, articleRepository, cfg, logger, writer, logRouter, newFallback, debugDirs, byExternalID[externalID])
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
		if err := prepareArticle(ctx, articleRepository, cfg, logger, writer, logRouter, newFallback, debugDirs, selected); err != nil {
			return err
		}
		processed++
	}
	if targetExternalID != "" && processed == 0 {
		return fmt.Errorf("статья с external_id %q не найдена", targetExternalID)
	}
	return nil
}

// keywordsFallbackFactory создаёт резервный источник исходных запросов для одной статьи.
// nil-фабрика (или nil-результат) означает прогон без резерва: пустой результат Keys.so
// останется ошибкой этапа.
type keywordsFallbackFactory func(article.Article) keywordsFallback

// prepareArticle собирает research одной статьи. Репозиторий принимается интерфейсом, потому
// что demo-сборке нужен тот же сбор, но без переходов состояния статьи.
func prepareArticle(
	ctx context.Context,
	articleRepository prepareRepository,
	cfg config.Config,
	logger *slog.Logger,
	writer *articleoutput.Writer,
	logRouter *diagnostics.ArticleLogRouter,
	newFallback keywordsFallbackFactory,
	debugDirs diagnosticsDirs,
	selected article.Article,
) error {
	// Каталога статьи может ещё не быть: сообщаем роутеру slug, чтобы prepare.log
	// открылся с первой же записи.
	logRouter.Register(selected.ID, selected.ExternalID, selected.Slug)
	var fallback keywordsFallback
	if newFallback != nil {
		fallback = newFallback(selected)
	}
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
			DebugDir:   debugDirs.keysso,
		}, logger),
		arsenkin.New(arsenkin.Config{
			ArticleID: selected.ID,
			Email:     cfg.ArsenkinEmail,
			Password:  cfg.ArsenkinPassword,
			Headless:  cfg.ArsenkinHeadless,
			DebugDir:  debugDirs.arsenkin,
		}, logger),
		fallback,
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
	fallback keywordsFallback,
) error {
	report := diagnostics.NewPrepareReport(selected)
	resetPrepareDiagnostics(logger, artifacts, selected)
	stage, err := collectPreparedResearch(ctx, articleRepository, cfg, logger, artifacts, selected, keyssoService, arsenkinService, fallback, report)
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
	fallback keywordsFallback,
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

	collectResult, source, failedStage, err := collectCleanedKeywords(
		ctx, articleRepository, logger, stageLogger, artifacts, selected, trace, keyssoService, fallback, report, stageStarted,
	)
	if err != nil {
		return failedStage, err
	}
	stageLogger.Info("результат Keys.so собран", append(
		keyssoLogFields("collect_result", stageStarted, "", collectResult.CollectedCount, len(collectResult.CleanedKeywords)),
		"source", source,
	)...)
	stageLogger.Info("этап Keys.so завершён", keyssoLogFields("complete", stageStarted, "", collectResult.CollectedCount, len(collectResult.CleanedKeywords))...)
	diagnostics.LogStep(logger, "keysso", "after", trace,
		"source", source,
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
	// Что именно уйдёт в форму Wordstat, решает клиент Arsenkin: он нормализует список и
	// обрезает его до лимита формы. Диагностика спрашивает набор у него, а не пересчитывает
	// сама, иначе лимит пришлось бы держать в двух местах и они разошлись бы.
	submittedQueries := arsenkin.SubmittedQueries(collectResult.CleanedKeywords)
	diagnostics.LogStep(logger, "arsenkin", "before", trace,
		"cleaned_count", len(collectResult.CleanedKeywords),
		"submitted_count", len(submittedQueries),
		"submitted_fingerprint", diagnostics.Fingerprint(strings.Join(submittedQueries, "\n")),
	)
	arsenkinResult, err := arsenkinService.CollectResearch(ctx, submittedQueries)
	if err != nil {
		report.Fail("arsenkin_collect", err.Error(), map[string]any{"submitted_count": len(submittedQueries)})
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
	saveArsenkinDiagnostics(logger, artifacts, selected, trace, submittedQueries, diagnostics.ArsenkinResult{
		WordstatKeywords: wordstatKeywords, CopywriterQueries: arsenkinResult.CopywriterQueries,
		LSIWords: arsenkinResult.LSIWords, CompetitorStructure: arsenkinResult.CompetitorStructure,
	}, time.Since(arsenkinStarted))
	report.Pass("arsenkin_collect", map[string]any{
		"submitted_count":                  len(submittedQueries),
		"wordstat_count":                   len(wordstatKeywords),
		"lsi_count":                        len(arsenkinResult.LSIWords),
		"competitor_structure_length":      len([]rune(arsenkinResult.CompetitorStructure)),
		"competitor_structure_fingerprint": diagnostics.Fingerprint(arsenkinResult.CompetitorStructure),
	})
	if membershipErr := checkWordstatMembership(articleLogger, trace, submittedQueries, returnedQueries, arsenkinStarted, report); membershipErr != nil {
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
		// Отказ PostgreSQL остаётся отказом PostgreSQL. Раньше он заворачивался в
		// arsenkin.StageError с адресом страницы Copywriters, и недоступность базы
		// приезжала пользователю как «этап Arsenkin завершён с ошибкой» с URL, по
		// которому в этот момент никто не ходил.
		return "save_research", savePipelineError(ctx, articleRepository, selected.ID,
			fmt.Errorf("сохранить research статьи external_id=%s в PostgreSQL: %w", selected.ExternalID, err))
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
	printKeysSOResult(os.Stdout, selected, source, collectResult)
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

// collectCleanedKeywords returns the queries to submit to Arsenkin and where they came from.
//
// Запросы, заполненные руками в article_research, имеют приоритет над Keys.so: их наличие —
// явное указание не ходить в Keys.so за этой статьёй. Всё остальное — диагностика, проверка
// релевантности, сохранение research — идёт дальше общим путём, независимо от источника.
//
// Третий источник — резервный подбор моделью. Он включается ровно в одном случае: Keys.so
// дошёл до результата и не нашёл у конкурента ни одного запроса. Ни ручное заполнение, ни
// технический отказ Keys.so сюда не ведут.
func collectCleanedKeywords(
	ctx context.Context,
	articleRepository prepareRepository,
	logger *slog.Logger,
	stageLogger *slog.Logger,
	artifacts prepareArtifactWriter,
	selected article.Article,
	trace article.Trace,
	keyssoService keyssoCollector,
	fallback keywordsFallback,
	report *diagnostics.PrepareReport,
	stageStarted time.Time,
) (keysso.CollectResult, string, string, error) {
	manualKeywords, err := articleRepository.GetManualKeywords(ctx, selected.ID)
	if err != nil {
		report.Fail("manual_keywords", err.Error(), nil)
		return keysso.CollectResult{}, diagnostics.KeywordSourceKeysSO, "manual_keywords",
			savePipelineError(ctx, articleRepository, selected.ID, err)
	}
	if len(manualKeywords) > 0 {
		collected := keysso.CollectResult{
			CollectedCount:  len(manualKeywords),
			CleanedKeywords: manualKeywords,
		}
		stageLogger.Info("этап Keys.so пропущен: запросы заполнены вручную",
			append(keyssoLogFields("skip", stageStarted, "", collected.CollectedCount, len(manualKeywords)),
				"source", diagnostics.KeywordSourceManual)...)
		diagnostics.LogStep(logger, "keysso", "before", trace, "source", diagnostics.KeywordSourceManual)
		saveKeysSODiagnostics(logger, artifacts, selected, trace, diagnostics.KeywordSourceManual, collected, time.Since(stageStarted))
		report.Pass("keysso_collect", map[string]any{
			"source":          diagnostics.KeywordSourceManual,
			"collected_count": collected.CollectedCount,
			"cleaned_count":   len(manualKeywords),
			"fingerprint":     diagnostics.Fingerprint(strings.Join(manualKeywords, "\n")),
		})
		return collected, diagnostics.KeywordSourceManual, "", nil
	}

	if strings.TrimSpace(selected.ReferenceURL) == "" {
		report.Fail("reference_url", "у статьи пустой reference_url", nil)
		return keysso.CollectResult{}, diagnostics.KeywordSourceKeysSO, "validate_reference_url", savePipelineError(
			ctx,
			articleRepository,
			selected.ID,
			newKeyssoRunError(selected.ID, "validate_reference_url", stageStarted, "", 0, 0, fmt.Errorf("у статьи пустой reference_url")),
		)
	}
	report.Pass("reference_url", map[string]any{"reference_url": selected.ReferenceURL})
	stageLogger.Info("этап Keys.so начат", keyssoLogFields("start", stageStarted, "", 0, 0)...)
	diagnostics.LogStep(logger, "keysso", "before", trace, "source", diagnostics.KeywordSourceKeysSO)

	collected, err := keyssoService.CollectCleanKeywords(ctx, selected.ReferenceURL)
	if err != nil {
		// Отсутствие исходных запросов — единственный отказ, который лечится сменой
		// источника. Таймауты, отказ авторизации и сломанная навигация остаются ошибками
		// этапа: их повтор осмыслен, а подмена источника скрыла бы поломку интеграции.
		if fallback != nil && keysso.NoRawKeywords(err) {
			return collectFallbackKeywords(ctx, articleRepository, logger, stageLogger, artifacts,
				selected, trace, keyssoService, fallback, report, stageStarted, err)
		}
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
		return keysso.CollectResult{}, diagnostics.KeywordSourceKeysSO, "keysso_collect",
			savePipelineError(ctx, articleRepository, selected.ID, newKeyssoRunError(
				selected.ID, stage, stageStarted, currentURL, collectedCount, cleanedCount, err,
			))
	}
	saveKeysSODiagnostics(logger, artifacts, selected, trace, diagnostics.KeywordSourceKeysSO, collected, time.Since(stageStarted))
	if len(collected.CleanedKeywords) == 0 {
		report.Fail("keysso_collect", "Keys.so вернул пустой список очищенных запросов", map[string]any{
			"collected_count": collected.CollectedCount, "cleaned_count": 0,
		})
		return keysso.CollectResult{}, diagnostics.KeywordSourceKeysSO, "keysso_collect", savePipelineError(
			ctx,
			articleRepository,
			selected.ID,
			newKeyssoRunError(selected.ID, "validate_result", stageStarted, "", collected.CollectedCount, 0, fmt.Errorf("Keys.so вернул пустой список очищенных запросов")),
		)
	}
	report.Pass("keysso_collect", map[string]any{
		"source":          diagnostics.KeywordSourceKeysSO,
		"collected_count": collected.CollectedCount,
		"cleaned_count":   len(collected.CleanedKeywords),
		"fingerprint":     diagnostics.Fingerprint(strings.Join(collected.CleanedKeywords, "\n")),
	})
	return collected, diagnostics.KeywordSourceKeysSO, "", nil
}

// collectFallbackKeywords подбирает исходные запросы моделью и прогоняет их через ту же
// очистку Keys.so, что и обычный сбор.
//
// Модель заменяет только источник исходного списка. Правила очистки в новом коде не
// повторяются: за ними идём в ту же форму delete-double, поэтому cleaned_keywords остаётся
// результатом Keys.so независимо от того, откуда пришли исходные запросы.
func collectFallbackKeywords(
	ctx context.Context,
	articleRepository prepareRepository,
	logger *slog.Logger,
	stageLogger *slog.Logger,
	artifacts prepareArtifactWriter,
	selected article.Article,
	trace article.Trace,
	keyssoService keyssoCollector,
	fallback keywordsFallback,
	report *diagnostics.PrepareReport,
	stageStarted time.Time,
	collectErr error,
) (keysso.CollectResult, string, string, error) {
	stageLogger.Warn("Keys.so не нашёл исходных запросов, запускается резервный подбор",
		append(keyssoLogFields("fallback_start", stageStarted, "", 0, 0),
			"source", diagnostics.KeywordSourceFallback, "reason", collectErr.Error())...)
	diagnostics.LogStep(logger, "keysso", "before", trace, "source", diagnostics.KeywordSourceFallback)

	rawKeywords, err := fallback.RawKeywords(ctx, selected.Title)
	if err != nil {
		report.Fail("keywords_fallback", err.Error(), map[string]any{
			"keysso_reason": collectErr.Error(),
		})
		return keysso.CollectResult{}, diagnostics.KeywordSourceFallback, "keywords_fallback",
			savePipelineError(ctx, articleRepository, selected.ID, fmt.Errorf(
				"резервный подбор запросов для статьи external_id=%s (Keys.so: %w): %w",
				selected.ExternalID, collectErr, err,
			))
	}
	report.Pass("keywords_fallback", map[string]any{
		"keysso_reason": collectErr.Error(),
		"raw_count":     len(rawKeywords),
		"fingerprint":   diagnostics.Fingerprint(strings.Join(rawKeywords, "\n")),
	})

	collected, err := keyssoService.CleanKeywords(ctx, rawKeywords)
	if err != nil {
		report.Fail("keysso_collect", err.Error(), map[string]any{
			"source": diagnostics.KeywordSourceFallback, "collected_count": len(rawKeywords),
		})
		return keysso.CollectResult{}, diagnostics.KeywordSourceFallback, "keysso_collect",
			savePipelineError(ctx, articleRepository, selected.ID, newKeyssoRunError(
				selected.ID, "clean_duplicates", stageStarted, "", len(rawKeywords), 0, err,
			))
	}
	saveKeysSODiagnostics(logger, artifacts, selected, trace, diagnostics.KeywordSourceFallback, collected, time.Since(stageStarted))
	if len(collected.CleanedKeywords) == 0 {
		report.Fail("keysso_collect", "очистка Keys.so вернула пустой список запросов", map[string]any{
			"source": diagnostics.KeywordSourceFallback, "collected_count": collected.CollectedCount, "cleaned_count": 0,
		})
		return keysso.CollectResult{}, diagnostics.KeywordSourceFallback, "keysso_collect", savePipelineError(
			ctx,
			articleRepository,
			selected.ID,
			newKeyssoRunError(selected.ID, "validate_result", stageStarted, "", collected.CollectedCount, 0,
				fmt.Errorf("очистка Keys.so вернула пустой список запросов")),
		)
	}
	report.Pass("keysso_collect", map[string]any{
		"source":          diagnostics.KeywordSourceFallback,
		"collected_count": collected.CollectedCount,
		"cleaned_count":   len(collected.CleanedKeywords),
		"fingerprint":     diagnostics.Fingerprint(strings.Join(collected.CleanedKeywords, "\n")),
	})
	return collected, diagnostics.KeywordSourceFallback, "", nil
}

func saveKeysSODiagnostics(
	logger *slog.Logger,
	artifacts prepareArtifactWriter,
	selected article.Article,
	trace article.Trace,
	source string,
	collected keysso.CollectResult,
	duration time.Duration,
) {
	savePrepareDiagnostics(logger, artifacts, selected, diagnostics.KeysSOFile,
		diagnostics.NewKeysSOSnapshot(trace, source, collected.CollectedCount, collected.CleanedKeywords, duration))
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

func printKeysSOResult(writer io.Writer, selected article.Article, source string, result keysso.CollectResult) {
	currentStage := "<none>"
	if selected.CurrentStep != nil {
		currentStage = *selected.CurrentStep
	}

	fmt.Fprintln(writer, "=== KEYS.SO RESULT ===")
	fmt.Fprintf(writer, "Article ID: %d\n", selected.ID)
	fmt.Fprintf(writer, "Title: %s\n", selected.Title)
	fmt.Fprintf(writer, "Reference URL: %s\n", selected.ReferenceURL)
	fmt.Fprintf(writer, "Source: %s\n", source)
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
