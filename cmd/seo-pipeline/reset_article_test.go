package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/repository"
)

type fakeResetArticleRepository struct {
	selected    article.Article
	counts      []repository.ClearCount
	resetIDs    []int64
	resetErr    error
	countsCalls int
}

func (r *fakeResetArticleRepository) GetArticleByExternalID(
	_ context.Context,
	externalID string,
) (article.Article, error) {
	if r.selected.ExternalID != externalID {
		return article.Article{}, fmt.Errorf("статья external_id %q не найдена", externalID)
	}
	return r.selected, nil
}

func (r *fakeResetArticleRepository) ResetArticleCounts(context.Context, int64) ([]repository.ClearCount, error) {
	r.countsCalls++
	return r.counts, nil
}

func (r *fakeResetArticleRepository) ResetArticleState(_ context.Context, articleID int64) error {
	if r.resetErr != nil {
		return r.resetErr
	}
	r.resetIDs = append(r.resetIDs, articleID)
	return nil
}

func newResetArticleRepository() *fakeResetArticleRepository {
	step := "article_review"
	return &fakeResetArticleRepository{
		selected: article.Article{
			ID: 7, ExternalID: "23", Title: "Как выбрать фрезу",
			Status: "failed", CurrentStep: &step,
		},
		counts: []repository.ClearCount{
			{Table: "article_errors", Rows: 3},
			{Table: "article_outputs", Rows: 1},
			{Table: "article_metadata", Rows: 1},
			{Table: "article_research", Rows: 1},
			{Table: "article_inputs", Rows: 1},
		},
	}
}

func newResetArticleOptions(out *bytes.Buffer, in string, interactive bool) resetArticleOptions {
	return resetArticleOptions{
		TaskCommand: "task-1",
		Interactive: interactive,
		In:          strings.NewReader(in),
		Out:         out,
	}
}

func TestResetArticleWipesStateAndArtifactsAfterConfirmation(t *testing.T) {
	articleRepository := newResetArticleRepository()
	writer := newClearWriter()
	var out bytes.Buffer

	err := runResetArticle(context.Background(), articleRepository, writer,
		newResetArticleOptions(&out, "reset\n", true), discardResetLogger(), "23")
	if err != nil {
		t.Fatalf("runResetArticle вернул ошибку: %v", err)
	}
	if len(articleRepository.resetIDs) != 1 || articleRepository.resetIDs[0] != 7 {
		t.Fatalf("ожидался сброс articles.id=7, получено %v", articleRepository.resetIDs)
	}
	if writer.clearCalls != 1 {
		t.Fatalf("файлы статьи должны удаляться один раз, вызовов %d", writer.clearCalls)
	}
}

// Отчёт печатает оба номера и обещание сохранить строку articles: смысл команды в том, что
// внутренний id переживает сброс, и увидеть это нужно до подтверждения.
func TestResetArticleReportShowsWhatSurvives(t *testing.T) {
	articleRepository := newResetArticleRepository()
	writer := newClearWriter()
	var out bytes.Buffer

	if err := runResetArticle(context.Background(), articleRepository, writer,
		newResetArticleOptions(&out, "reset\n", true), discardResetLogger(), "23"); err != nil {
		t.Fatalf("runResetArticle вернул ошибку: %v", err)
	}

	report := out.String()
	for _, expected := range []string{
		"external_id=23", "articles.id=7", "Как выбрать фрезу",
		// article_inputs — единственное отличие от clear, и в отчёте оно должно быть видно.
		"article_inputs", "article_research", "23-kak-vybrat-frezu/",
		"Сохраняется: строка articles с id=7",
		"включая логи",
		// Входные данные стёрты, поэтому следующий шаг — импорт, а не run.
		"make task-1 import",
	} {
		if !strings.Contains(report, expected) {
			t.Fatalf("в отчёте нет %q:\n%s", expected, report)
		}
	}
	if !writer.countCalled {
		t.Fatal("отчёт должен считать файлы статьи до подтверждения")
	}
}

// Слово подтверждения у сброса своё: clear на reset не срабатывает и наоборот.
func TestResetArticleRejectsClearConfirmationWord(t *testing.T) {
	articleRepository := newResetArticleRepository()
	writer := newClearWriter()
	var out bytes.Buffer

	err := runResetArticle(context.Background(), articleRepository, writer,
		newResetArticleOptions(&out, "clear\n", true), discardResetLogger(), "23")
	if err != nil {
		t.Fatalf("отказ от подтверждения не должен быть ошибкой: %v", err)
	}
	if len(articleRepository.resetIDs) != 0 || writer.clearCalls != 0 {
		t.Fatal("чужое слово подтверждения не должно ничего удалять")
	}
}

func TestResetArticleRefusesWithoutTerminalAndWithoutYes(t *testing.T) {
	articleRepository := newResetArticleRepository()
	writer := newClearWriter()
	var out bytes.Buffer

	err := runResetArticle(context.Background(), articleRepository, writer,
		newResetArticleOptions(&out, "", false), discardResetLogger(), "23")
	if err == nil {
		t.Fatal("без терминала и без --yes команда обязана отказаться")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("ошибка должна подсказывать флаг --yes, получено: %v", err)
	}
	if len(articleRepository.resetIDs) != 0 || writer.clearCalls != 0 {
		t.Fatal("отказ не должен ничего удалять")
	}
}

func TestResetArticleWithAssumeYesSkipsPrompt(t *testing.T) {
	articleRepository := newResetArticleRepository()
	writer := newClearWriter()
	var out bytes.Buffer
	options := newResetArticleOptions(&out, "", false)
	options.AssumeYes = true

	if err := runResetArticle(context.Background(), articleRepository, writer,
		options, discardResetLogger(), "23"); err != nil {
		t.Fatalf("runResetArticle с --yes вернул ошибку: %v", err)
	}
	if len(articleRepository.resetIDs) != 1 {
		t.Fatalf("состояние должно быть сброшено, получено %v", articleRepository.resetIDs)
	}
}

// Неизвестный external_id не должен доходить до удаления: команда обязана упасть на поиске
// статьи, а не сбросить чужую.
func TestResetArticleUnknownArticleDoesNotTouchAnything(t *testing.T) {
	articleRepository := newResetArticleRepository()
	writer := newClearWriter()
	var out bytes.Buffer

	err := runResetArticle(context.Background(), articleRepository, writer,
		newResetArticleOptions(&out, "reset\n", true), discardResetLogger(), "999")
	if err == nil {
		t.Fatal("сброс неизвестной статьи должен возвращать ошибку")
	}
	if articleRepository.countsCalls != 0 || writer.clearCalls != 0 {
		t.Fatal("до поиска статьи ничего считать и удалять нельзя")
	}
}

// Боевой репозиторий обязан удовлетворять контракту команды: интерфейс объявлен у потребителя,
// и разойтись с ним он может только молча.
func TestArticleRepositorySatisfiesResetArticleRepository(t *testing.T) {
	var _ resetArticleRepository = (*repository.ArticleRepository)(nil)
}
