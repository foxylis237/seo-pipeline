package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/repository"
)

type fakeClearRepository struct {
	selected    article.Article
	getErr      error
	counts      []repository.ClearCount
	clearedIDs  []int64
	clearErr    error
	countsCalls int
}

func (r *fakeClearRepository) GetArticleByExternalID(_ context.Context, externalID string) (article.Article, error) {
	if r.getErr != nil {
		return article.Article{}, r.getErr
	}
	if r.selected.ExternalID != externalID {
		return article.Article{}, fmt.Errorf("статья external_id %q не найдена", externalID)
	}
	return r.selected, nil
}

func (r *fakeClearRepository) ClearArticleCounts(context.Context, int64) ([]repository.ClearCount, error) {
	r.countsCalls++
	return r.counts, nil
}

func (r *fakeClearRepository) ClearArticleState(_ context.Context, articleID int64) error {
	if r.clearErr != nil {
		return r.clearErr
	}
	r.clearedIDs = append(r.clearedIDs, articleID)
	return nil
}

type fakeClearWriter struct {
	directory   string
	files       int
	removed     []string
	removeErr   error
	clearCalls  int
	countCalled bool
}

func (w *fakeClearWriter) CountArticleArtifacts(string) (string, int, error) {
	w.countCalled = true
	return w.directory, w.files, nil
}

func (w *fakeClearWriter) ClearArticleArtifacts(string) ([]string, error) {
	w.clearCalls++
	return w.removed, w.removeErr
}

func newClearRepository() *fakeClearRepository {
	step := "article_review"
	return &fakeClearRepository{
		selected: article.Article{
			ID: 7, ExternalID: "23", Title: "Как выбрать фрезу",
			Status: "failed", CurrentStep: &step,
		},
		counts: []repository.ClearCount{
			{Table: "article_errors", Rows: 3},
			{Table: "article_outputs", Rows: 1},
			{Table: "article_metadata", Rows: 1},
			{Table: "article_research", Rows: 1},
		},
	}
}

func newClearWriter() *fakeClearWriter {
	return &fakeClearWriter{
		directory: "23-kak-vybrat-frezu",
		files:     6,
		removed:   []string{"23-kak-vybrat-frezu/generated", "23-kak-vybrat-frezu/result.md"},
	}
}

func newClearOptions(out *bytes.Buffer, in string, interactive bool) clearOptions {
	return clearOptions{
		Interactive: interactive,
		In:          strings.NewReader(in),
		Out:         out,
	}
}

func TestClearWipesStateAndArtifactsAfterConfirmation(t *testing.T) {
	articleRepository := newClearRepository()
	writer := newClearWriter()
	var out bytes.Buffer

	err := runClear(context.Background(), articleRepository, writer,
		newClearOptions(&out, "clear\n", true), discardResetLogger(), "23")
	if err != nil {
		t.Fatalf("runClear вернул ошибку: %v", err)
	}
	if len(articleRepository.clearedIDs) != 1 || articleRepository.clearedIDs[0] != 7 {
		t.Fatalf("ожидалась очистка articles.id=7, получено %v", articleRepository.clearedIDs)
	}
	if writer.clearCalls != 1 {
		t.Fatalf("файлы статьи должны удаляться один раз, вызовов %d", writer.clearCalls)
	}
}

// Отчёт печатает оба номера: аргумент команды — external_id из Excel, а внутренний
// articles.id виден рядом, чтобы очистку нельзя было спутать со статьёй под тем же номером.
func TestClearReportShowsBothIdentifiersBeforeConfirmation(t *testing.T) {
	articleRepository := newClearRepository()
	writer := newClearWriter()
	var out bytes.Buffer

	if err := runClear(context.Background(), articleRepository, writer,
		newClearOptions(&out, "clear\n", true), discardResetLogger(), "23"); err != nil {
		t.Fatalf("runClear вернул ошибку: %v", err)
	}

	report := out.String()
	for _, expected := range []string{
		"external_id=23", "articles.id=7", "Как выбрать фрезу",
		"article_research", "23-kak-vybrat-frezu/",
		// Отчёт обязан сказать, что логи тоже исчезнут: это единственное, что раньше
		// переживало очистку, и на этом легко ошибиться в ожиданиях.
		"включая логи",
	} {
		if !strings.Contains(report, expected) {
			t.Fatalf("в отчёте нет %q:\n%s", expected, report)
		}
	}
	if !writer.countCalled {
		t.Fatal("отчёт должен считать файлы статьи до подтверждения")
	}
}

func TestClearWithoutConfirmationChangesNothing(t *testing.T) {
	articleRepository := newClearRepository()
	writer := newClearWriter()
	var out bytes.Buffer

	err := runClear(context.Background(), articleRepository, writer,
		newClearOptions(&out, "нет\n", true), discardResetLogger(), "23")
	if err != nil {
		t.Fatalf("отказ от подтверждения не должен быть ошибкой: %v", err)
	}
	if len(articleRepository.clearedIDs) != 0 {
		t.Fatalf("состояние не должно очищаться без подтверждения: %v", articleRepository.clearedIDs)
	}
	if writer.clearCalls != 0 {
		t.Fatalf("файлы не должны удаляться без подтверждения, вызовов %d", writer.clearCalls)
	}
}

func TestClearRefusesWithoutTerminalAndWithoutYes(t *testing.T) {
	articleRepository := newClearRepository()
	writer := newClearWriter()
	var out bytes.Buffer

	err := runClear(context.Background(), articleRepository, writer,
		newClearOptions(&out, "", false), discardResetLogger(), "23")
	if err == nil {
		t.Fatal("без терминала и без --yes команда обязана отказаться")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("ошибка должна подсказывать флаг --yes, получено: %v", err)
	}
	if len(articleRepository.clearedIDs) != 0 || writer.clearCalls != 0 {
		t.Fatal("отказ не должен ничего удалять")
	}
}

func TestClearWithAssumeYesSkipsPrompt(t *testing.T) {
	articleRepository := newClearRepository()
	writer := newClearWriter()
	var out bytes.Buffer
	options := newClearOptions(&out, "", false)
	options.AssumeYes = true

	if err := runClear(context.Background(), articleRepository, writer,
		options, discardResetLogger(), "23"); err != nil {
		t.Fatalf("runClear с --yes вернул ошибку: %v", err)
	}
	if len(articleRepository.clearedIDs) != 1 {
		t.Fatalf("состояние должно быть очищено, получено %v", articleRepository.clearedIDs)
	}
}

// Неизвестный external_id не должен доходить до удаления: команда обязана упасть на поиске
// статьи, а не очистить чужую.
func TestClearUnknownArticleDoesNotTouchAnything(t *testing.T) {
	articleRepository := newClearRepository()
	writer := newClearWriter()
	var out bytes.Buffer

	err := runClear(context.Background(), articleRepository, writer,
		newClearOptions(&out, "clear\n", true), discardResetLogger(), "999")
	if err == nil {
		t.Fatal("очистка неизвестной статьи должна возвращать ошибку")
	}
	if articleRepository.countsCalls != 0 || writer.clearCalls != 0 {
		t.Fatal("до поиска статьи ничего считать и удалять нельзя")
	}
}

func TestParseClearCommand(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantError  bool
		externalID string
		assumeYes  bool
	}{
		{name: "с ID", args: []string{"seo-pipeline", "task-1", "clear", "23"}, externalID: "23"},
		{name: "с --yes", args: []string{"seo-pipeline", "task-1", "clear", "23", "--yes"}, externalID: "23", assumeYes: true},
		// ID обязателен: очистка без него означала бы «стереть все статьи», а это reset.
		{name: "без ID", args: []string{"seo-pipeline", "task-1", "clear"}, wantError: true},
		{name: "нулевой ID", args: []string{"seo-pipeline", "task-1", "clear", "0"}, wantError: true},
		{name: "отрицательный ID", args: []string{"seo-pipeline", "task-1", "clear", "-3"}, wantError: true},
		{name: "нечисловой ID", args: []string{"seo-pipeline", "task-1", "clear", "abc"}, wantError: true},
		{name: "лишний аргумент", args: []string{"seo-pipeline", "task-1", "clear", "23", "37"}, wantError: true},
		{name: "--plan не поддерживается", args: []string{"seo-pipeline", "task-1", "clear", "23", "--plan"}, wantError: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			command, err := parseCommand(testCase.args)
			if testCase.wantError {
				if err == nil {
					t.Fatalf("ожидалась ошибка, получено %+v", command)
				}
				return
			}
			if err != nil {
				t.Fatalf("разбор завершился ошибкой: %v", err)
			}
			if command.Name != "clear" {
				t.Fatalf("операция %q, ожидалась clear", command.Name)
			}
			if command.ExternalID != testCase.externalID {
				t.Fatalf("ExternalID=%q, ожидался %q", command.ExternalID, testCase.externalID)
			}
			if command.AssumeYes != testCase.assumeYes {
				t.Fatalf("AssumeYes=%v, ожидалось %v", command.AssumeYes, testCase.assumeYes)
			}
		})
	}
}

// Сбой на файлах после успешного коммита в БД обязан быть виден и подсказывать повтор:
// команда идемпотентна, а частично очищенный каталог иначе остался бы незамеченным.
func TestClearReportsFileFailureAfterDatabaseCommit(t *testing.T) {
	articleRepository := newClearRepository()
	writer := newClearWriter()
	writer.removeErr = errors.New("permission denied")
	var out bytes.Buffer

	err := runClear(context.Background(), articleRepository, writer,
		newClearOptions(&out, "clear\n", true), discardResetLogger(), "23")
	if err == nil {
		t.Fatal("ошибка удаления файлов должна возвращаться наружу")
	}
	if !strings.Contains(err.Error(), "повторите clear") {
		t.Fatalf("ошибка должна подсказывать повтор, получено: %v", err)
	}
	if len(articleRepository.clearedIDs) != 1 {
		t.Fatal("состояние в БД к этому моменту уже очищено")
	}
}
