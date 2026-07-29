package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/integrations/arsenkin"
	"github.com/foxylis237/seo-pipeline/internal/integrations/keysso"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
)

type fakePrepareRepository struct {
	prepared           bool
	research           fakeResearch
	metadataPresent    bool
	outputPresent      bool
	savePreparedCalls  int
	savedProcessingErr error
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
	arsenkinService := fakeArsenkinCollector{result: arsenkin.Result{
		WordstatKeywords: []arsenkin.KeywordFrequency{{Query: "новый wordstat", Frequency: 20}},
		LSIWords:         []string{"новый lsi"}, CompetitorStructure: "новая структура",
	}}

	err := prepareArticleWithCollectors(context.Background(), repository, config.Config{}, testPrepareLogger(), testPreparedArticle(), keyssoService, arsenkinService)
	if err != nil {
		t.Fatal(err)
	}
	want := fakeResearch{
		cleaned:  []string{"новый cleaned"},
		wordstat: []article.KeywordFrequency{{Query: "новый wordstat", Frequency: 20}},
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
		context.Background(), repository, config.Config{}, testPrepareLogger(), testPreparedArticle(),
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
		context.Background(), repository, config.Config{}, testPrepareLogger(), testPreparedArticle(),
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
		context.Background(), repository, config.Config{}, testPrepareLogger(), testPreparedArticle(),
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

func testPrepareLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
