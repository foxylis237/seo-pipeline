package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

type fakeRetryRepository struct {
	articles    []article.ArticleError
	saved       article.Article
	cleared     []int64
	runsAtClear []int
	savedErrors []int64
}

func (r *fakeRetryRepository) SaveError(_ context.Context, id int64, _ error) error {
	r.savedErrors = append(r.savedErrors, id)
	return nil
}

func (r *fakeRetryRepository) ListArticlesWithErrors(context.Context) ([]article.ArticleError, error) {
	return r.articles, nil
}
func (r *fakeRetryRepository) GetArticleByExternalID(context.Context, string) (article.Article, error) {
	return r.saved, nil
}
func (r *fakeRetryRepository) ClearArticleErrorForRetry(_ context.Context, id int64) (bool, error) {
	r.cleared = append(r.cleared, id)
	return true, nil
}

func TestRunRetryClearsSequentiallyAndContinuesAfterFailure(t *testing.T) {
	message := "old error"
	repository := &fakeRetryRepository{articles: []article.ArticleError{
		{Article: article.Article{ID: 1, ExternalID: "51", Status: "failed", ErrorMessage: &message}},
		{Article: article.Article{ID: 2, ExternalID: "52", Status: "failed", ErrorMessage: &message}},
	}}
	var runs []string
	err := runRetry(context.Background(), repository, "", func(_ context.Context, externalID string) error {
		runs = append(runs, externalID)
		repository.runsAtClear = append(repository.runsAtClear, len(repository.cleared))
		if externalID == "51" {
			return errors.New("new failure")
		}
		return nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !reflect.DeepEqual(repository.cleared, []int64{1, 2}) || !reflect.DeepEqual(runs, []string{"51", "52"}) {
		t.Fatalf("err=%v cleared=%v runs=%v", err, repository.cleared, runs)
	}
	if !reflect.DeepEqual(repository.runsAtClear, []int{1, 2}) {
		t.Fatalf("runsAtClear=%v", repository.runsAtClear)
	}
}

func TestRunRetrySingleUsesExternalIDAndSkipsWithoutError(t *testing.T) {
	repository := &fakeRetryRepository{saved: article.Article{ID: 9, ExternalID: "57", Status: "failed"}}
	called := false
	if err := runRetry(context.Background(), repository, "57", func(context.Context, string) error { called = true; return nil }, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if called || len(repository.cleared) != 0 {
		t.Fatalf("pipeline called=%v cleared=%v", called, repository.cleared)
	}
}

// TestRunRetryDrivesFullPipeline фиксирует F1: retry обязан вести статью полным маршрутом
// prepare → structure → article/info → review → fix → html → result и завершать её только
// после html. Раньше он шёл demo-путём и ставил completed сразу после article/info.
func TestRunRetryDrivesFullPipeline(t *testing.T) {
	message := "prepare failed"
	retryRepo := &fakeRetryRepository{saved: article.Article{ID: 7, ExternalID: "37", Status: "failed", ErrorMessage: &message}}
	runRepo := &fakeRunRepository{state: pipelineState{Status: "failed", ErrorMessage: message}}

	var executed []pipelineStage
	completedAfter := make([]pipelineStage, 0, 1)
	runOne := func(ctx context.Context, externalID string) error {
		return runFullPipeline(ctx, runRepo, func(_ context.Context, stage pipelineStage, _ string) error {
			executed = append(executed, stage)
			runRepo.advance(stage)
			if runRepo.state.Status == "completed" {
				completedAfter = append(completedAfter, stage)
			}
			return nil
		}, runPipelineTestLogger(), externalID, false)
	}

	if err := runRetry(context.Background(), retryRepo, "37", runOne, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	want := []pipelineStage{stagePrepare, stageStructure, stageArticle, stageReview, stageFix, stageHTML, stageResult}
	if !reflect.DeepEqual(executed, want) {
		t.Fatalf("retry выполнил этапы %v, ожидались %v", executed, want)
	}
	if !reflect.DeepEqual(completedAfter, []pipelineStage{stageResult}) {
		t.Fatalf("статья завершена на этапах %v, ожидался только %q", completedAfter, stageResult)
	}
	if !reflect.DeepEqual(retryRepo.cleared, []int64{7}) {
		t.Fatalf("сохранённая ошибка не снята: cleared=%v", retryRepo.cleared)
	}
}

// TestRunRetryDoesNotCompleteArticleWithoutHTML — этап html, не сохранивший артефакт,
// обязан остановить retry, а не пропустить статью в result и completed.
func TestRunRetryDoesNotCompleteArticleWithoutHTML(t *testing.T) {
	message := "html failed"
	retryRepo := &fakeRetryRepository{saved: article.Article{ID: 7, ExternalID: "37", Status: "failed", ErrorMessage: &message}}
	runRepo := &fakeRunRepository{state: readyThrough(stageFix)}

	var executed []pipelineStage
	runOne := func(ctx context.Context, externalID string) error {
		return runFullPipeline(ctx, runRepo, func(_ context.Context, stage pipelineStage, _ string) error {
			executed = append(executed, stage)
			if stage == stageHTML {
				return nil // этап отработал без ошибки, но html_path не появился
			}
			runRepo.advance(stage)
			return nil
		}, runPipelineTestLogger(), externalID, false)
	}

	err := runRetry(context.Background(), retryRepo, "37", runOne, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("retry завершился успешно, хотя html не сохранён")
	}
	if !reflect.DeepEqual(executed, []pipelineStage{stageHTML}) {
		t.Fatalf("после незавершённого html выполнены этапы %v", executed)
	}
	if runRepo.state.Status == "completed" || runRepo.state.HTMLPath != "" {
		t.Fatalf("статья завершена без html: status=%q html_path=%q", runRepo.state.Status, runRepo.state.HTMLPath)
	}
}

// TestRetryIsWiredToFullPipelineRunner проверяет саму проводку в main: retry и run обязаны
// получать один и тот же раннер. Именно расхождение здесь и было причиной F1, а поймать его
// иначе нельзя — обе команды собираются внутри main() и наружу не выведены.
func TestRetryIsWiredToFullPipelineRunner(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	runners := map[string]string{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name, ok := call.Fun.(*ast.Ident)
		if !ok || len(call.Args) < 4 {
			return true
		}
		switch name.Name {
		case "runRetry", "runRegenerate":
			if argument, isIdent := call.Args[3].(*ast.Ident); isIdent {
				runners[name.Name] = argument.Name
			}
		}
		return true
	})
	if runners["runRetry"] == "" || runners["runRetry"] != runners["runRegenerate"] {
		t.Fatalf("retry и regenerate получают разные раннеры: %v", runners)
	}
}

func TestRunRetrySkipsCompletedArticleWithError(t *testing.T) {
	message := "inconsistent error"
	repository := &fakeRetryRepository{saved: article.Article{ID: 9, ExternalID: "57", Status: "completed", ErrorMessage: &message}}
	called := false
	if err := runRetry(context.Background(), repository, "57", func(context.Context, string) error { called = true; return nil }, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if called || len(repository.cleared) != 0 {
		t.Fatalf("pipeline called=%v cleared=%v", called, repository.cleared)
	}
}
