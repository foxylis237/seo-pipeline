package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/integrations/arsenkin"
	"github.com/foxylis237/seo-pipeline/internal/integrations/keysso"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/diagnostics"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/keywords"
)

// fakeKeywordsFallback подменяет резервный источник исходных запросов. Настоящая модель тесту
// не нужна: проверяется маршрут, а не качество семантики.
type fakeKeywordsFallback struct {
	queries      []string
	err          error
	calls        int
	articleNames []string
}

func (f *fakeKeywordsFallback) RawKeywords(_ context.Context, articleName string) ([]string, error) {
	f.calls++
	f.articleNames = append(f.articleNames, articleName)
	return f.queries, f.err
}

// noRawKeywordsError воспроизводит окончательный ответ Keys.so «запросов конкурента нет»
// в том виде, в каком его отдаёт интеграция.
func noRawKeywordsError() error {
	return &keysso.StageError{
		Stage: "collect_competitor_queries", CurrentURL: "https://www.keys.so/ru/keysbypage",
		Err: fmt.Errorf("%w: таблица запросов конкурента пуста", keysso.ErrNoRawKeywords),
	}
}

// TestPrepareSkipsFallbackWhenKeysSOReturnedQueries: пока Keys.so отдаёт запросы, резервный
// источник не трогается вовсе — он платный и ходит к внешнему сервису.
func TestPrepareSkipsFallbackWhenKeysSOReturnedQueries(t *testing.T) {
	repository := oldPrepareRepositoryState()
	fallback := &fakeKeywordsFallback{queries: []string{"подобранный моделью запрос"}}
	var submitted []string

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), newFakePrepareArtifacts(), testPreparedArticle(),
		fakeKeysSOCollector{result: keysso.CollectResult{CollectedCount: 2, CleanedKeywords: []string{"собранный запрос"}}},
		fakeArsenkinCollector{submitted: &submitted, result: arsenkin.Result{
			WordstatKeywords:    []arsenkin.KeywordFrequency{{Query: "собранный запрос", Frequency: 20}},
			LSIWords:            []string{"новый lsi"},
			CompetitorStructure: "новая структура",
		}},
		fallback,
	)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.calls != 0 {
		t.Fatalf("резервный источник вызван %d раз при непустом результате Keys.so", fallback.calls)
	}
	if !reflect.DeepEqual(submitted, []string{"собранный запрос"}) {
		t.Fatalf("в Arsenkin ушло %v", submitted)
	}
}

// TestPrepareUsesFallbackAndExistingKeysSOCleaning — основной сценарий: модель заменяет только
// источник исходных запросов, очистка остаётся Keys.so-шной, а cleaned_keywords сохраняется
// обычным путём.
func TestPrepareUsesFallbackAndExistingKeysSOCleaning(t *testing.T) {
	repository := oldPrepareRepositoryState()
	repository.trace = article.Trace{
		ArticleID: 7, ExternalID: "37", Title: "Как стать бариста",
		Keyword: "бариста", ReferenceURL: "https://example.test/barista",
	}
	fallback := &fakeKeywordsFallback{queries: []string{
		"обучение бариста", "обучение бариста с нуля", "работа бариста",
	}}
	var rawSentToCleaning, submitted []string
	artifacts := newFakePrepareArtifacts()

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), artifacts, testBaristaArticle(),
		fakeKeysSOCollector{
			err:         noRawKeywordsError(),
			cleaned:     &rawSentToCleaning,
			cleanResult: keysso.CollectResult{CollectedCount: 3, CleanedKeywords: []string{"обучение бариста", "работа бариста"}},
		},
		fakeArsenkinCollector{submitted: &submitted, result: arsenkin.Result{
			WordstatKeywords: []arsenkin.KeywordFrequency{
				{Query: "обучение бариста", Frequency: 900},
				{Query: "работа бариста", Frequency: 400},
			},
			LSIWords: []string{"кофе"}, CompetitorStructure: "H1 Бариста",
		}},
		fallback,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Модель спрашивали про эту статью, ровно один раз.
	if fallback.calls != 1 || !reflect.DeepEqual(fallback.articleNames, []string{"Как стать бариста"}) {
		t.Fatalf("резервный источник вызван %d раз с названиями %v", fallback.calls, fallback.articleNames)
	}
	// Ответ модели ушёл в существующую очистку Keys.so целиком и без правок.
	if !reflect.DeepEqual(rawSentToCleaning, fallback.queries) {
		t.Fatalf("в очистку Keys.so ушло %v, want %v", rawSentToCleaning, fallback.queries)
	}
	// В Arsenkin уходит результат очистки, а не сырой ответ модели.
	if !reflect.DeepEqual(submitted, []string{"обучение бариста", "работа бариста"}) {
		t.Fatalf("в Arsenkin ушло %v", submitted)
	}
	// cleaned_keywords сохраняется обычным путём, вместе с остальным research.
	want := fakeResearch{
		cleaned: []string{"обучение бариста", "работа бариста"},
		wordstat: []article.KeywordFrequency{
			{Query: "обучение бариста", Frequency: 900},
			{Query: "работа бариста", Frequency: 400},
		},
		lsi: []string{"кофе"}, structure: "H1 Бариста",
	}
	if repository.savePreparedCalls != 1 || !reflect.DeepEqual(repository.research, want) {
		t.Fatalf("saved research = %+v, calls = %d", repository.research, repository.savePreparedCalls)
	}
	snapshot, ok := artifacts.saved[diagnostics.KeysSOFile].(diagnostics.KeysSOSnapshot)
	if !ok {
		t.Fatalf("keysso.json = %T", artifacts.saved[diagnostics.KeysSOFile])
	}
	if snapshot.Source != diagnostics.KeywordSourceFallback {
		t.Fatalf("source в keysso.json = %q, want %q", snapshot.Source, diagnostics.KeywordSourceFallback)
	}
	check := prepareCheck(t, artifacts.report(t), "keywords_fallback")
	if check.Status != diagnostics.StatusPassed || check.Details["raw_count"] != len(fallback.queries) {
		t.Fatalf("проверка keywords_fallback = %+v", check)
	}
}

// TestPrepareFallbackKeepsWordstatAsOnlyFrequencySource: резерв подменяет только первый этап
// Keys.so, где частотности нет ни у одного источника. Единственный поставщик частотностей —
// Wordstat, и research резервного пути обязан совпасть с research обычного.
func TestPrepareFallbackKeepsWordstatAsOnlyFrequencySource(t *testing.T) {
	// Ответ модели — плоский список фраз, ровно как первый этап Keys.so.
	rawQueries := keywords.Parse("обучение бариста\nработа бариста\n")

	run := func(t *testing.T, keyssoService fakeKeysSOCollector, fallback keywordsFallback) fakeResearch {
		t.Helper()
		repository := oldPrepareRepositoryState()
		repository.trace = article.Trace{
			ArticleID: 7, ExternalID: "37", Title: "Как стать бариста",
			Keyword: "бариста", ReferenceURL: "https://example.test/barista",
		}
		err := prepareArticleWithCollectors(
			context.Background(), repository, config.Config{}, testPrepareLogger(), newFakePrepareArtifacts(), testBaristaArticle(),
			keyssoService,
			fakeArsenkinCollector{result: arsenkin.Result{
				WordstatKeywords: []arsenkin.KeywordFrequency{
					{Query: "обучение бариста", Frequency: 14500},
					{Query: "работа бариста", Frequency: 3200},
				},
				LSIWords: []string{"кофе"}, CompetitorStructure: "H1 Бариста",
			}},
			fallback,
		)
		if err != nil {
			t.Fatal(err)
		}
		return repository.research
	}

	cleaned := keysso.CollectResult{CollectedCount: 2, CleanedKeywords: rawQueries}
	viaKeysSO := run(t, fakeKeysSOCollector{result: cleaned}, nil)
	viaFallback := run(t,
		fakeKeysSOCollector{err: noRawKeywordsError(), cleanResult: cleaned},
		&fakeKeywordsFallback{queries: rawQueries},
	)

	if !reflect.DeepEqual(viaKeysSO, viaFallback) {
		t.Fatalf("research резервного пути отличается от обычного:\n keysso   = %+v\n fallback = %+v", viaKeysSO, viaFallback)
	}
	wantWordstat := []article.KeywordFrequency{
		{Query: "обучение бариста", Frequency: 14500},
		{Query: "работа бариста", Frequency: 3200},
	}
	if !reflect.DeepEqual(viaFallback.wordstat, wantWordstat) {
		t.Fatalf("частотности в research = %+v, want Wordstat-частотности %+v", viaFallback.wordstat, wantWordstat)
	}
	// Блок «2. КЛЮЧЕВЫЕ ЗАПРОСЫ С ЧАСТОТНОСТЬЮ» промпта статьи рендерится из этих же
	// wordstat_keywords — другого источника частотности в пайплайне нет.
	wantPromptBlock := "обучение бариста\t14500\nработа бариста\t3200"
	if got := article.FormatKeywords(viaFallback.wordstat); got != wantPromptBlock {
		t.Fatalf("блок ключей промпта статьи = %q, want %q", got, wantPromptBlock)
	}
	// cleaned_keywords остаётся плоским списком фраз: частотности в нём нет ни при каком
	// источнике исходных запросов.
	if !reflect.DeepEqual(viaFallback.cleaned, rawQueries) {
		t.Fatalf("cleaned_keywords = %v, want %v", viaFallback.cleaned, rawQueries)
	}
}

// TestPrepareDoesNotRunArsenkinWhenFallbackAnswerIsUnusable: пустой или мусорный ответ модели
// не превращается в пустой список запросов для Wordstat.
func TestPrepareDoesNotRunArsenkinWhenFallbackAnswerIsUnusable(t *testing.T) {
	repository := oldPrepareRepositoryState()
	want := repository.research
	arsenkinCalls := 0
	fallbackErr := &keywords.StageError{ArticleID: 7, Stage: "parse_answer",
		Err: errors.New("в ответе модели нет ни одной строки вида «запрос<TAB>частотность»")}
	var rawSentToCleaning []string
	artifacts := newFakePrepareArtifacts()

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), artifacts, testPreparedArticle(),
		fakeKeysSOCollector{err: noRawKeywordsError(), cleaned: &rawSentToCleaning},
		fakeArsenkinCollector{calls: &arsenkinCalls},
		&fakeKeywordsFallback{err: fallbackErr},
	)
	if !errors.Is(err, fallbackErr) {
		t.Fatalf("error = %v, want %v", err, fallbackErr)
	}
	if arsenkinCalls != 0 {
		t.Fatalf("Arsenkin вызван %d раз после негодного ответа модели", arsenkinCalls)
	}
	if rawSentToCleaning != nil {
		t.Fatalf("в очистку Keys.so ушёл список %v после негодного ответа модели", rawSentToCleaning)
	}
	assertOldPrepareResultsPreserved(t, repository, want)
	check := prepareCheck(t, artifacts.report(t), "keywords_fallback")
	if check.Status != diagnostics.StatusFailed {
		t.Fatalf("проверка keywords_fallback = %+v", check)
	}
}

// TestPrepareKeepsTechnicalKeysSOFailureAsFailure: подмена источника лечит только отсутствие
// данных. Таймаут остаётся отказом этапа, иначе поломка интеграции пряталась бы за моделью.
func TestPrepareKeepsTechnicalKeysSOFailureAsFailure(t *testing.T) {
	repository := oldPrepareRepositoryState()
	want := repository.research
	fallback := &fakeKeywordsFallback{queries: []string{"подобранный моделью запрос"}}
	wantErr := &keysso.StageError{Stage: "wait_search_results", Err: errors.New("Keys.so timed out")}

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), newFakePrepareArtifacts(), testPreparedArticle(),
		fakeKeysSOCollector{err: wantErr}, fakeArsenkinCollector{}, fallback,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if fallback.calls != 0 {
		t.Fatalf("резервный источник вызван %d раз на техническом отказе Keys.so", fallback.calls)
	}
	assertOldPrepareResultsPreserved(t, repository, want)
}

// TestPrepareSkipsBothKeysSOAndFallbackForManualKeywords: ручное заполнение — явное указание
// не ходить наружу за этой статьёй. Резервный источник тоже наружный.
func TestPrepareSkipsBothKeysSOAndFallbackForManualKeywords(t *testing.T) {
	repository := oldPrepareRepositoryState()
	repository.manualKeywords = []string{"ручной запрос", "второй ручной запрос"}
	keyssoCalls := 0
	fallback := &fakeKeywordsFallback{queries: []string{"подобранный моделью запрос"}}
	var submitted []string

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), newFakePrepareArtifacts(), testPreparedArticle(),
		fakeKeysSOCollector{calls: &keyssoCalls, err: errors.New("Keys.so не должен вызываться")},
		fakeArsenkinCollector{submitted: &submitted, result: arsenkin.Result{
			WordstatKeywords: []arsenkin.KeywordFrequency{
				{Query: "ручной запрос", Frequency: 100},
				{Query: "второй ручной запрос", Frequency: 50},
			},
			LSIWords: []string{"новый lsi"}, CompetitorStructure: "новая структура",
		}},
		fallback,
	)
	if err != nil {
		t.Fatal(err)
	}
	if keyssoCalls != 0 {
		t.Fatalf("Keys.so вызван %d раз при ручных запросах", keyssoCalls)
	}
	if fallback.calls != 0 {
		t.Fatalf("резервный источник вызван %d раз при ручных запросах", fallback.calls)
	}
	if !reflect.DeepEqual(submitted, repository.manualKeywords) {
		t.Fatalf("в Arsenkin ушло %v, want %v", submitted, repository.manualKeywords)
	}
}

// TestPrepareWithoutFallbackKeepsEmptyKeysSOResultAsFailure фиксирует поведение прогона без
// настроенного резерва: оно должно остаться прежним.
func TestPrepareWithoutFallbackKeepsEmptyKeysSOResultAsFailure(t *testing.T) {
	repository := oldPrepareRepositoryState()
	want := repository.research
	arsenkinCalls := 0

	err := prepareArticleWithCollectors(
		context.Background(), repository, config.Config{}, testPrepareLogger(), newFakePrepareArtifacts(), testPreparedArticle(),
		fakeKeysSOCollector{err: noRawKeywordsError()}, fakeArsenkinCollector{calls: &arsenkinCalls}, nil,
	)
	if !keysso.NoRawKeywords(err) {
		t.Fatalf("error = %v, want отсутствие исходных запросов", err)
	}
	if arsenkinCalls != 0 {
		t.Fatalf("Arsenkin вызван %d раз без запросов", arsenkinCalls)
	}
	assertOldPrepareResultsPreserved(t, repository, want)
}
