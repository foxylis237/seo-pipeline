package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/integrations/arsenkin"
	"github.com/foxylis237/seo-pipeline/internal/integrations/keysso"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/diagnostics"
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
	manualKeywords     []string
	manualKeywordsErr  error
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

func (r *fakePrepareRepository) GetManualKeywords(context.Context, int64) ([]string, error) {
	if r.manualKeywordsErr != nil {
		return nil, r.manualKeywordsErr
	}
	return r.manualKeywords, nil
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
	calls  *int
	// cleanResult и cleanErr отвечают на очистку запросов, пришедших не из Keys.so.
	cleanResult keysso.CollectResult
	cleanErr    error
	// cleaned принимает исходный список, отданный на очистку: по нему видно, что резервный
	// источник дошёл до формы delete-double, а не в обход неё.
	cleaned *[]string
}

func (c fakeKeysSOCollector) CollectCleanKeywords(context.Context, string) (keysso.CollectResult, error) {
	if c.calls != nil {
		(*c.calls)++
	}
	return c.result, c.err
}

func (c fakeKeysSOCollector) CleanKeywords(_ context.Context, queries []string) (keysso.CollectResult, error) {
	if c.cleaned != nil {
		*c.cleaned = append([]string(nil), queries...)
	}
	return c.cleanResult, c.cleanErr
}

type fakeArsenkinCollector struct {
	result arsenkin.Result
	err    error
	calls  *int
	// submitted принимает запросы, ушедшие в форму Wordstat: именно они, а не сохранённый
	// research, показывают, что доехало до интеграции.
	submitted *[]string
}

func (c fakeArsenkinCollector) CollectResearch(_ context.Context, queries []string) (arsenkin.Result, error) {
	if c.calls != nil {
		(*c.calls)++
	}
	if c.submitted != nil {
		*c.submitted = append([]string(nil), queries...)
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
	err := prepareArticleWithCollectors(context.Background(), repository, config.Config{}, testPrepareLogger(), artifacts, testPreparedArticle(), keyssoService, arsenkinService, nil)
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
		fakeKeysSOCollector{err: wantErr}, fakeArsenkinCollector{calls: &arsenkinCalls}, nil,
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
		fakeArsenkinCollector{err: wantErr}, nil,
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
		}, nil,
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
		}}, nil,
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
		fakeArsenkinCollector{}, nil,
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
		}}, nil,
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
		}}, nil,
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
		}}, nil,
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
		}}, nil,
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
		fakeKeysSOCollector{err: wantErr}, fakeArsenkinCollector{}, nil,
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

// TestPrepareArticleReplacesOnlyFirstKeysSOStageWithManualKeywords проверяет главный смысл
// ручного заполнения: вставленные запросы заменяют ТОЛЬКО сбор запросов у конкурента.
// Браузерная автоматизация первого этапа не запускается, а чистка от дублей остаётся за
// Keys.so, и в Arsenkin уходит её результат, а не вставленный список.
func TestPrepareArticleReplacesOnlyFirstKeysSOStageWithManualKeywords(t *testing.T) {
	repository := oldPrepareRepositoryState()
	repository.manualKeywords = []string{"ручной запрос", "ручной запрос повтор", "второй ручной запрос"}
	collectCalls := 0
	var sentToCleaning, submitted []string
	arsenkinService := fakeArsenkinCollector{submitted: &submitted, result: arsenkin.Result{
		WordstatKeywords: []arsenkin.KeywordFrequency{
			{Query: "ручной запрос", Frequency: 100},
			{Query: "второй ручной запрос", Frequency: 50},
		},
		LSIWords: []string{"новый lsi"}, CompetitorStructure: "новая структура",
	}}
	artifacts := newFakePrepareArtifacts()

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), artifacts, testPreparedArticle(),
		fakeKeysSOCollector{
			calls:       &collectCalls,
			err:         errors.New("сбор запросов у конкурента не должен вызываться"),
			cleaned:     &sentToCleaning,
			cleanResult: keysso.CollectResult{CollectedCount: 3, CleanedKeywords: []string{"ручной запрос", "второй ручной запрос"}},
		},
		arsenkinService, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if collectCalls != 0 {
		t.Fatalf("сбор запросов у конкурента вызван %d раз при заполненных вручную запросах", collectCalls)
	}
	// Вставленный список уходит в чистку от дублей целиком и без правок.
	if !reflect.DeepEqual(sentToCleaning, repository.manualKeywords) {
		t.Fatalf("в очистку Keys.so ушло %v, want %v", sentToCleaning, repository.manualKeywords)
	}
	// В форму Wordstat уходит результат очистки, а не исходная вставка.
	want := fakeResearch{
		cleaned: []string{"ручной запрос", "второй ручной запрос"},
		wordstat: []article.KeywordFrequency{
			{Query: "ручной запрос", Frequency: 100},
			{Query: "второй ручной запрос", Frequency: 50},
		},
		lsi: []string{"новый lsi"}, structure: "новая структура",
	}
	if !reflect.DeepEqual(submitted, want.cleaned) {
		t.Fatalf("в Arsenkin ушло %v, want %v", submitted, want.cleaned)
	}
	if repository.savePreparedCalls != 1 || !reflect.DeepEqual(repository.research, want) {
		t.Fatalf("saved research = %+v, calls = %d", repository.research, repository.savePreparedCalls)
	}
	snapshot, ok := artifacts.saved[diagnostics.KeysSOFile].(diagnostics.KeysSOSnapshot)
	if !ok {
		t.Fatalf("keysso.json = %T, want diagnostics.KeysSOSnapshot", artifacts.saved[diagnostics.KeysSOFile])
	}
	if snapshot.Source != diagnostics.KeywordSourceManual {
		t.Fatalf("source в keysso.json = %q, want %q", snapshot.Source, diagnostics.KeywordSourceManual)
	}
	if !reflect.DeepEqual(snapshot.CleanedKeywords, want.cleaned) {
		t.Fatalf("cleaned_keywords в keysso.json = %v", snapshot.CleanedKeywords)
	}
	check := prepareCheck(t, artifacts.report(t), "manual_keywords")
	if check.Status != diagnostics.StatusPassed || check.Details["raw_count"] != len(repository.manualKeywords) {
		t.Fatalf("проверка manual_keywords = %+v", check)
	}
}

// TestPrepareArticleUsesManualKeywordsWithoutReferenceURL фиксирует, что непустой
// reference_url — предусловие сбора запросов у конкурента, а не всего prepare: с ручными
// запросами статья проходит подготовку и без него.
func TestPrepareArticleUsesManualKeywordsWithoutReferenceURL(t *testing.T) {
	repository := oldPrepareRepositoryState()
	repository.manualKeywords = []string{"ручной запрос"}
	repository.trace = article.Trace{ArticleID: 7, ExternalID: "37", Title: "Тест"}
	selected := testPreparedArticle()
	selected.ReferenceURL = ""

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), newFakePrepareArtifacts(), selected,
		fakeKeysSOCollector{
			err:         errors.New("сбор запросов у конкурента не должен вызываться"),
			cleanResult: keysso.CollectResult{CollectedCount: 1, CleanedKeywords: []string{"ручной запрос"}},
		},
		fakeArsenkinCollector{result: arsenkin.Result{
			WordstatKeywords:    []arsenkin.KeywordFrequency{{Query: "ручной запрос", Frequency: 100}},
			LSIWords:            []string{"новый lsi"},
			CompetitorStructure: "новая структура",
		}}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if repository.savePreparedCalls != 1 {
		t.Fatalf("research сохранён %d раз, ожидался один", repository.savePreparedCalls)
	}
}

// TestPrepareArticleMarksKeysSOAsSourceWithoutManualKeywords — обратная сторона:
// без ручных запросов источник в диагностике остаётся Keys.so.
func TestPrepareArticleMarksKeysSOAsSourceWithoutManualKeywords(t *testing.T) {
	repository := oldPrepareRepositoryState()
	keyssoCalls := 0
	var submitted []string
	artifacts := newFakePrepareArtifacts()

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), artifacts, testPreparedArticle(),
		fakeKeysSOCollector{calls: &keyssoCalls, result: keysso.CollectResult{
			CollectedCount: 2, CleanedKeywords: []string{"собранный запрос"},
		}},
		fakeArsenkinCollector{submitted: &submitted, result: arsenkin.Result{
			WordstatKeywords:    []arsenkin.KeywordFrequency{{Query: "собранный запрос", Frequency: 20}},
			LSIWords:            []string{"новый lsi"},
			CompetitorStructure: "новая структура",
		}}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if keyssoCalls != 1 {
		t.Fatalf("Keys.so вызван %d раз, ожидался один", keyssoCalls)
	}
	if !reflect.DeepEqual(submitted, []string{"собранный запрос"}) {
		t.Fatalf("в Arsenkin ушло %v, want %v", submitted, []string{"собранный запрос"})
	}
	snapshot, ok := artifacts.saved[diagnostics.KeysSOFile].(diagnostics.KeysSOSnapshot)
	if !ok {
		t.Fatalf("keysso.json = %T, want diagnostics.KeysSOSnapshot", artifacts.saved[diagnostics.KeysSOFile])
	}
	if snapshot.Source != diagnostics.KeywordSourceKeysSO {
		t.Fatalf("source в keysso.json = %q, want %q", snapshot.Source, diagnostics.KeywordSourceKeysSO)
	}
}

// TestPrepareArticleReportsOnlyActuallySubmittedQueries: форма Wordstat принимает
// ограниченный список, и клиент Arsenkin его обрезает. Диагностика обязана называть
// отправленным именно этот набор — иначе отчёт приписывает Arsenkin фразы, которых он не
// получал, а проверка происхождения сверяет ответ с непосланными запросами.
func TestPrepareArticleReportsOnlyActuallySubmittedQueries(t *testing.T) {
	const cleanedCount = 60
	cleaned := make([]string, cleanedCount)
	for index := range cleaned {
		cleaned[index] = fmt.Sprintf("бариста запрос %02d", index)
	}
	wantSubmitted := arsenkin.SubmittedQueries(cleaned)
	if len(wantSubmitted) >= cleanedCount {
		t.Fatalf("клиент Arsenkin не обрезал список: %d запросов", len(wantSubmitted))
	}

	repository := oldPrepareRepositoryState()
	repository.trace = article.Trace{
		ArticleID: 7, ExternalID: "37", Title: "Как стать бариста",
		Keyword: "бариста", ReferenceURL: "https://example.test/barista",
	}
	var submitted []string
	artifacts := newFakePrepareArtifacts()

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), artifacts, testBaristaArticle(),
		fakeKeysSOCollector{result: keysso.CollectResult{CollectedCount: cleanedCount, CleanedKeywords: cleaned}},
		fakeArsenkinCollector{submitted: &submitted, result: arsenkin.Result{
			WordstatKeywords: []arsenkin.KeywordFrequency{
				{Query: wantSubmitted[0], Frequency: 500},
				{Query: wantSubmitted[1], Frequency: 400},
			},
			LSIWords: []string{"кофе"}, CompetitorStructure: "H1 Бариста",
		}}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	// В Arsenkin ушли ровно первые запросы в исходном порядке, а не весь cleaned.
	if !reflect.DeepEqual(submitted, wantSubmitted) {
		t.Fatalf("в Arsenkin ушло %d запросов, want %d", len(submitted), len(wantSubmitted))
	}
	if !reflect.DeepEqual(submitted, cleaned[:len(wantSubmitted)]) {
		t.Fatalf("порядок отправленных запросов изменился: %v", submitted[:3])
	}

	snapshot, ok := artifacts.saved[diagnostics.ArsenkinFile].(diagnostics.ArsenkinSnapshot)
	if !ok {
		t.Fatalf("arsenkin.json = %T, want diagnostics.ArsenkinSnapshot", artifacts.saved[diagnostics.ArsenkinFile])
	}
	if snapshot.SubmittedCount != len(wantSubmitted) || !reflect.DeepEqual(snapshot.SubmittedQueries, wantSubmitted) {
		t.Fatalf("arsenkin.json: submitted_count = %d, queries = %d", snapshot.SubmittedCount, len(snapshot.SubmittedQueries))
	}

	report := artifacts.report(t)
	if report.Status != diagnostics.StatusPassed {
		t.Fatalf("status = %q, failed_stage = %q", report.Status, report.FailedStage)
	}
	collect := prepareCheck(t, report, "arsenkin_collect")
	if got := collect.Details["submitted_count"]; got != len(wantSubmitted) {
		t.Fatalf("submitted_count в отчёте = %v, want %d", got, len(wantSubmitted))
	}
	// Проверка происхождения идёт по отправленному набору: обе вернувшиеся фразы — свои.
	membership := prepareCheck(t, report, "wordstat_membership")
	if membership.Details["returned_count"] != 2 || membership.Details["matched_count"] != 2 {
		t.Fatalf("membership считался не по отправленному набору: %+v", membership.Details)
	}
	// Исходное количество нигде не выдаётся за отправленное.
	for _, check := range report.Checks {
		if got, found := check.Details["submitted_count"]; found && got == cleanedCount {
			t.Fatalf("проверка %q называет отправленными все %d запросов", check.Name, cleanedCount)
		}
	}
}

func prepareCheck(t *testing.T, report *diagnostics.PrepareReport, name string) diagnostics.CheckResult {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("проверка %q отсутствует в отчёте", name)
	return diagnostics.CheckResult{}
}
