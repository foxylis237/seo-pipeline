package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/integrations/arsenkin"
	"github.com/foxylis237/seo-pipeline/internal/integrations/keysso"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/diagnostics"
)

type fakePrepareRepository struct {
	prepared           bool
	research           fakeResearch
	metadataPresent    bool
	outputPresent      bool
	savePreparedCalls  int
	savePreparedErr    error
	savedProcessingErr error
	trace              article.Trace
}

type fakeResearch struct {
	cleaned   []string
	wordstat  []article.KeywordFrequency
	lsi       []string
	structure string
}

func (r *fakePrepareRepository) PrepareArticleForRun(context.Context, int64) error {
	r.prepared = true
	return nil
}

func (r *fakePrepareRepository) SavePreparedResearch(_ context.Context, _ int64, cleaned []string, wordstat []article.KeywordFrequency, lsi []string, structure string) error {
	r.savePreparedCalls++
	if r.savePreparedErr != nil {
		return r.savePreparedErr
	}
	r.research = fakeResearch{
		cleaned: append([]string(nil), cleaned...), wordstat: append([]article.KeywordFrequency(nil), wordstat...),
		lsi: append([]string(nil), lsi...), structure: structure,
	}
	r.metadataPresent = false
	r.outputPresent = false
	return nil
}

func (r *fakePrepareRepository) SaveError(_ context.Context, _ int64, err error) error {
	r.savedProcessingErr = err
	return nil
}

func (r *fakePrepareRepository) GetArticleInput(_ context.Context, _ int64) (article.Input, error) {
	return article.Input{
		ExcelID: 37, Title: "Как стать бариста", ImageSlug: "kak-stat-barista",
		Keyword: "бариста", ReferenceURL: "https://example.test/barista",
	}, nil
}

func (r *fakePrepareRepository) GetArticleTrace(_ context.Context, articleID int64) (article.Trace, error) {
	trace := r.trace
	if trace.ArticleID == 0 {
		selected := testPreparedArticle()
		trace = article.Trace{
			ArticleID: articleID, ExternalID: selected.ExternalID,
			Title: selected.Title, ReferenceURL: selected.ReferenceURL,
		}
	}
	return trace, nil
}

// fakePrepareArtifacts records the diagnostics files prepare tried to save.
type fakePrepareArtifacts struct {
	saved  map[string]any
	resets int
	err    error
}

func newFakePrepareArtifacts() *fakePrepareArtifacts {
	return &fakePrepareArtifacts{saved: map[string]any{}}
}

func (a *fakePrepareArtifacts) ResetDiagnostics(externalID, slug, subdirectory string) error {
	if a.err != nil {
		return a.err
	}
	a.resets++
	a.saved = map[string]any{}
	return nil
}

func (a *fakePrepareArtifacts) SaveDiagnostics(externalID, slug, subdirectory, name string, payload any) (string, error) {
	if a.err != nil {
		return "", a.err
	}
	a.saved[name] = payload
	return externalID + "-" + slug + "/" + subdirectory + "/" + name, nil
}

func (a *fakePrepareArtifacts) report(t *testing.T) *diagnostics.PrepareReport {
	t.Helper()
	saved, found := a.saved[diagnostics.PrepareReportFile]
	if !found {
		t.Fatalf("prepare-report.json не сохранён, сохранено: %v", a.saved)
	}
	report, ok := saved.(*diagnostics.PrepareReport)
	if !ok {
		t.Fatalf("prepare-report.json = %T, want *diagnostics.PrepareReport", saved)
	}
	return report
}

type fakeKeysSOCollector struct {
	result keysso.CollectResult
	err    error
}

func (c fakeKeysSOCollector) CollectCleanKeywords(context.Context, string) (keysso.CollectResult, error) {
	return c.result, c.err
}

type fakeArsenkinCollector struct {
	result arsenkin.Result
	err    error
	calls  *int
}

func (c fakeArsenkinCollector) CollectResearch(context.Context, []string) (arsenkin.Result, error) {
	if c.calls != nil {
		(*c.calls)++
	}
	return c.result, c.err
}

func TestPrepareArticleReplacesResearchOnlyAfterAllCollectorsSucceed(t *testing.T) {
	repository := oldPrepareRepositoryState()
	keyssoService := fakeKeysSOCollector{result: keysso.CollectResult{CollectedCount: 2, CleanedKeywords: []string{"новый cleaned"}}}
	// Wordstat отвечает теми же фразами, которые ему отправили: иначе срабатывает
	// проверка происхождения запросов.
	arsenkinService := fakeArsenkinCollector{result: arsenkin.Result{
		WordstatKeywords: []arsenkin.KeywordFrequency{{Query: "новый cleaned", Frequency: 20}},
		LSIWords:         []string{"новый lsi"}, CompetitorStructure: "новая структура",
	}}

	artifacts := newFakePrepareArtifacts()
	err := prepareArticleWithCollectors(context.Background(), repository, config.Config{}, testPrepareLogger(), artifacts, testPreparedArticle(), keyssoService, arsenkinService)
	if err != nil {
		t.Fatal(err)
	}
	want := fakeResearch{
		cleaned:  []string{"новый cleaned"},
		wordstat: []article.KeywordFrequency{{Query: "новый cleaned", Frequency: 20}},
		lsi:      []string{"новый lsi"}, structure: "новая структура",
	}
	if repository.savePreparedCalls != 1 || !reflect.DeepEqual(repository.research, want) {
		t.Fatalf("saved research = %+v, calls = %d", repository.research, repository.savePreparedCalls)
	}
	if repository.metadataPresent || repository.outputPresent {
		t.Fatal("stale metadata/output were not cleared after successful research replacement")
	}
}

func TestPrepareArticleKeepsPreviousResultsOnKeysSOError(t *testing.T) {
	repository := oldPrepareRepositoryState()
	want := repository.research
	arsenkinCalls := 0
	wantErr := errors.New("keys.so unavailable")

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), newFakePrepareArtifacts(), testPreparedArticle(),
		fakeKeysSOCollector{err: wantErr}, fakeArsenkinCollector{calls: &arsenkinCalls},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	assertOldPrepareResultsPreserved(t, repository, want)
	if arsenkinCalls != 0 {
		t.Fatalf("Arsenkin calls = %d, want 0", arsenkinCalls)
	}
}

func TestPrepareArticleKeepsPreviousResultsOnArsenkinError(t *testing.T) {
	repository := oldPrepareRepositoryState()
	want := repository.research
	wantErr := errors.New("arsenkin unavailable")

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), newFakePrepareArtifacts(), testPreparedArticle(),
		fakeKeysSOCollector{result: keysso.CollectResult{CollectedCount: 1, CleanedKeywords: []string{"частично новый cleaned"}}},
		fakeArsenkinCollector{err: wantErr},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	assertOldPrepareResultsPreserved(t, repository, want)
}

func TestPrepareArticleDoesNotSavePartiallyCollectedArsenkinResult(t *testing.T) {
	repository := oldPrepareRepositoryState()
	want := repository.research
	wantErr := errors.New("copywriters failed")

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), newFakePrepareArtifacts(), testPreparedArticle(),
		fakeKeysSOCollector{result: keysso.CollectResult{CollectedCount: 1, CleanedKeywords: []string{"новый cleaned"}}},
		fakeArsenkinCollector{
			result: arsenkin.Result{WordstatKeywords: []arsenkin.KeywordFrequency{{Query: "частичный wordstat", Frequency: 99}}},
			err:    wantErr,
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	assertOldPrepareResultsPreserved(t, repository, want)
}

// TestPrepareArticleReportsDatabaseFailureAsItself фиксирует, что недоступность PostgreSQL
// не выдаётся за отказ Arsenkin. Раньше она заворачивалась в arsenkin.StageError с адресом
// страницы Copywriters, и main печатал «этап Arsenkin завершён с ошибкой» с этим URL —
// пользователь шёл чинить интеграцию вместо базы.
func TestPrepareArticleReportsDatabaseFailureAsItself(t *testing.T) {
	databaseErr := errors.New("connection refused")
	repository := &fakePrepareRepository{savePreparedErr: databaseErr}

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), newFakePrepareArtifacts(), testPreparedArticle(),
		fakeKeysSOCollector{result: keysso.CollectResult{CollectedCount: 1, CleanedKeywords: []string{"бариста обучение"}}},
		fakeArsenkinCollector{result: arsenkin.Result{
			WordstatKeywords:    []arsenkin.KeywordFrequency{{Query: "бариста обучение", Frequency: 100}},
			LSIWords:            []string{"кофе"},
			CompetitorStructure: "H1 - Бариста",
		}},
	)
	if !errors.Is(err, databaseErr) {
		t.Fatalf("error = %v, want database error", err)
	}
	var stageErr *arsenkin.StageError
	if errors.As(err, &stageErr) {
		t.Fatalf("отказ PostgreSQL выдан за отказ Arsenkin: stage=%s current_url=%s", stageErr.Stage, stageErr.CurrentURL)
	}
	if strings.Contains(err.Error(), "arsenkin.ru") {
		t.Fatalf("в ошибке базы остался адрес Arsenkin: %v", err)
	}
	if !errors.Is(repository.savedProcessingErr, databaseErr) {
		t.Fatalf("сохранённая ошибка = %v, want database error", repository.savedProcessingErr)
	}
}

func oldPrepareRepositoryState() *fakePrepareRepository {
	return &fakePrepareRepository{
		research: fakeResearch{
			cleaned:  []string{"старый cleaned"},
			wordstat: []article.KeywordFrequency{{Query: "старый wordstat", Frequency: 10}},
			lsi:      []string{"старый lsi"}, structure: "старая структура",
		},
		metadataPresent: true,
		outputPresent:   true,
	}
}

func assertOldPrepareResultsPreserved(t *testing.T, repository *fakePrepareRepository, want fakeResearch) {
	t.Helper()
	if !repository.prepared {
		t.Fatal("article was not prepared for the run")
	}
	if repository.savePreparedCalls != 0 || !reflect.DeepEqual(repository.research, want) {
		t.Fatalf("research changed before successful preparation: %+v", repository.research)
	}
	if !repository.metadataPresent || !repository.outputPresent {
		t.Fatal("metadata/output disappeared before successful preparation")
	}
	if repository.savedProcessingErr == nil {
		t.Fatal("existing error flow did not persist the integration error")
	}
}

func testPreparedArticle() article.Article {
	return article.Article{ID: 7, ExternalID: "37", Title: "Тест", ReferenceURL: "https://example.test/reference"}
}

// testBaristaArticle matches the trace used by the cross-article guard tests.
func testBaristaArticle() article.Article {
	return article.Article{ID: 7, ExternalID: "37", Title: "Как стать бариста", ReferenceURL: "https://example.test/barista"}
}

func testPrepareLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPrepareArticleRejectsKeysSOResultOfAnotherArticle(t *testing.T) {
	repository := oldPrepareRepositoryState()
	want := repository.research
	repository.trace = article.Trace{
		ArticleID: 7, ExternalID: "37", Title: "Как стать бариста",
		Keyword: "бариста", ReferenceURL: "https://example.test/barista",
	}
	artifacts := newFakePrepareArtifacts()

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), artifacts, testBaristaArticle(),
		fakeKeysSOCollector{result: keysso.CollectResult{
			CollectedCount: 2, CleanedKeywords: []string{"монтаж пластиковых окон", "замена стеклопакета"},
		}},
		fakeArsenkinCollector{},
	)
	if err == nil || !strings.Contains(err.Error(), "не связанные") {
		t.Fatalf("error = %v, want a keyword relevance failure", err)
	}
	assertOldPrepareResultsPreserved(t, repository, want)
}

func TestPrepareArticleRejectsWordstatResultOfAnotherArticle(t *testing.T) {
	repository := oldPrepareRepositoryState()
	want := repository.research
	repository.trace = article.Trace{
		ArticleID: 7, ExternalID: "37", Title: "Как стать бариста",
		Keyword: "бариста", ReferenceURL: "https://example.test/barista",
	}
	artifacts := newFakePrepareArtifacts()

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), artifacts, testBaristaArticle(),
		fakeKeysSOCollector{result: keysso.CollectResult{CollectedCount: 1, CleanedKeywords: []string{"работа бариста"}}},
		fakeArsenkinCollector{result: arsenkin.Result{
			WordstatKeywords:    []arsenkin.KeywordFrequency{{Query: "монтаж пластиковых окон", Frequency: 500}},
			LSIWords:            []string{"стеклопакет"},
			CompetitorStructure: "структура другой статьи",
		}},
	)
	if err == nil || !strings.Contains(err.Error(), "не отправлялись") {
		t.Fatalf("error = %v, want a Wordstat membership failure", err)
	}
	assertOldPrepareResultsPreserved(t, repository, want)
}

func TestPrepareSavesDiagnosticsForSuccessfulRun(t *testing.T) {
	repository := oldPrepareRepositoryState()
	repository.trace = article.Trace{
		ArticleID: 7, ExternalID: "37", Title: "Как стать бариста",
		Keyword: "бариста", ReferenceURL: "https://example.test/barista",
	}
	artifacts := newFakePrepareArtifacts()

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), artifacts, testBaristaArticle(),
		fakeKeysSOCollector{result: keysso.CollectResult{CollectedCount: 3, CleanedKeywords: []string{"работа бариста"}}},
		fakeArsenkinCollector{result: arsenkin.Result{
			WordstatKeywords:    []arsenkin.KeywordFrequency{{Query: "работа бариста", Frequency: 500}},
			LSIWords:            []string{"кофе"},
			CompetitorStructure: "H1 Бариста",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		diagnostics.InputFile, diagnostics.KeysSOFile,
		diagnostics.ArsenkinFile, diagnostics.PrepareReportFile,
	} {
		if _, found := artifacts.saved[name]; !found {
			t.Errorf("%s не сохранён", name)
		}
	}
	report := artifacts.report(t)
	if report.Status != diagnostics.StatusPassed || report.FailedStage != "" {
		t.Fatalf("status = %q, failed_stage = %q", report.Status, report.FailedStage)
	}
	if report.ArticleID != 7 || report.ExternalID != "37" || report.Keyword != "бариста" ||
		report.ReferenceURL != "https://example.test/barista" || report.Title != "Как стать бариста" {
		t.Fatalf("идентичность в отчёте: %+v", report)
	}
	wantChecks := []string{
		"identity_trace", "reference_url", "keysso_collect",
		"keyword_relevance", "arsenkin_collect", "wordstat_membership", "save_research",
	}
	if len(report.Checks) != len(wantChecks) {
		t.Fatalf("проверок в отчёте %d, ожидалось %d: %+v", len(report.Checks), len(wantChecks), report.Checks)
	}
	for index, want := range wantChecks {
		check := report.Checks[index]
		if check.Name != want || check.Status != diagnostics.StatusPassed {
			t.Errorf("проверка %d = %s/%s, ожидалось %s/passed", index, check.Name, check.Status, want)
		}
	}
}

func TestPrepareReportRecordsFailedCheck(t *testing.T) {
	repository := oldPrepareRepositoryState()
	repository.trace = article.Trace{
		ArticleID: 7, ExternalID: "37", Title: "Как стать бариста",
		Keyword: "бариста", ReferenceURL: "https://example.test/barista",
	}
	artifacts := newFakePrepareArtifacts()

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), artifacts, testBaristaArticle(),
		fakeKeysSOCollector{result: keysso.CollectResult{CollectedCount: 1, CleanedKeywords: []string{"работа бариста"}}},
		fakeArsenkinCollector{result: arsenkin.Result{
			WordstatKeywords:    []arsenkin.KeywordFrequency{{Query: "монтаж пластиковых окон", Frequency: 500}},
			LSIWords:            []string{"стеклопакет"},
			CompetitorStructure: "структура другой статьи",
		}},
	)
	if err == nil {
		t.Fatal("прогон с чужим результатом Wordstat не остановлен")
	}
	report := artifacts.report(t)
	if report.Status != diagnostics.StatusFailed || report.FailedStage != "wordstat_membership" {
		t.Fatalf("status = %q, failed_stage = %q", report.Status, report.FailedStage)
	}
	if report.Error == "" {
		t.Fatal("текст ошибки не попал в отчёт")
	}
	failed := report.Checks[len(report.Checks)-1]
	if failed.Name != "wordstat_membership" || failed.Status != diagnostics.StatusFailed {
		t.Fatalf("последняя проверка = %+v", failed)
	}
	if failed.Details["matched_count"] != 0 {
		t.Fatalf("детали проверки: %+v", failed.Details)
	}
	// Данные Keys.so и Arsenkin сохранены даже у неуспешного прогона: именно они нужны для разбора.
	if _, found := artifacts.saved[diagnostics.KeysSOFile]; !found {
		t.Error("keysso.json не сохранён для неуспешного прогона")
	}
	if _, found := artifacts.saved[diagnostics.ArsenkinFile]; !found {
		t.Error("arsenkin.json не сохранён для неуспешного прогона")
	}
}

func TestPrepareSurvivesDiagnosticsWriteFailure(t *testing.T) {
	repository := oldPrepareRepositoryState()
	artifacts := newFakePrepareArtifacts()
	artifacts.err = errors.New("диск переполнен")

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), artifacts, testPreparedArticle(),
		fakeKeysSOCollector{result: keysso.CollectResult{CollectedCount: 1, CleanedKeywords: []string{"новый cleaned"}}},
		fakeArsenkinCollector{result: arsenkin.Result{
			WordstatKeywords: []arsenkin.KeywordFrequency{{Query: "новый cleaned", Frequency: 20}},
			LSIWords:         []string{"новый lsi"}, CompetitorStructure: "новая структура",
		}},
	)
	if err != nil {
		t.Fatalf("сбой записи диагностики остановил успешный prepare: %v", err)
	}
	if repository.savePreparedCalls != 1 {
		t.Fatalf("research не сохранён: calls = %d", repository.savePreparedCalls)
	}
}

func TestPrepareClearsStaleDiagnosticsBeforeRun(t *testing.T) {
	repository := oldPrepareRepositoryState()
	artifacts := newFakePrepareArtifacts()
	// Файлы предыдущего прогона: их не должно остаться рядом с новым отчётом.
	artifacts.saved[diagnostics.KeysSOFile] = "прошлый keysso"
	artifacts.saved[diagnostics.ArsenkinFile] = "прошлый arsenkin"
	artifacts.saved[diagnostics.PrepareReportFile] = "прошлый отчёт"
	wantErr := errors.New("keys.so unavailable")

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), artifacts, testPreparedArticle(),
		fakeKeysSOCollector{err: wantErr}, fakeArsenkinCollector{},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if artifacts.resets != 1 {
		t.Fatalf("очистка выполнена %d раз, ожидался один", artifacts.resets)
	}
	if stale, found := artifacts.saved[diagnostics.ArsenkinFile]; found {
		t.Fatalf("arsenkin.json предыдущего прогона остался: %v", stale)
	}
	if stale, found := artifacts.saved[diagnostics.KeysSOFile]; found {
		t.Fatalf("keysso.json предыдущего прогона остался: %v", stale)
	}
	report := artifacts.report(t)
	if report.Status != diagnostics.StatusFailed || report.FailedStage != "keysso_collect" {
		t.Fatalf("отчёт нового прогона = %s / %s", report.Status, report.FailedStage)
	}
	if report.StartedAt.IsZero() || report.FinishedAt.Before(report.StartedAt) {
		t.Fatalf("время запуска в отчёте: started=%v finished=%v", report.StartedAt, report.FinishedAt)
	}
}
