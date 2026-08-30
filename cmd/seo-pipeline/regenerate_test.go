package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

// fakeRegenerateRepository повторяет поведение репозитория: сброс состояния и переход
// статьи в completed после успешного прогона.
type fakeRegenerateRepository struct {
	selected   article.Article
	result     article.ResultInput
	resetCalls int
	resetIDs   []int64
}

func (r *fakeRegenerateRepository) GetArticleByExternalID(_ context.Context, externalID string) (article.Article, error) {
	if r.selected.ExternalID != externalID {
		return article.Article{}, errors.New("статья не найдена")
	}
	return r.selected, nil
}

func (r *fakeRegenerateRepository) ResetGenerationState(_ context.Context, articleID int64) error {
	r.resetCalls++
	r.resetIDs = append(r.resetIDs, articleID)
	r.selected.Status = "pending"
	r.selected.CurrentStep = nil
	r.selected.ErrorMessage = nil
	r.result = article.ResultInput{Article: r.selected}
	return nil
}

func (r *fakeRegenerateRepository) GetResultInput(_ context.Context, _ string) (article.ResultInput, error) {
	return r.result, nil
}

// complete имитирует успешный полный прогон.
func (r *fakeRegenerateRepository) complete() {
	r.selected.Status = "completed"
	r.selected.CurrentStep = nil
	r.result = article.ResultInput{
		Article:     r.selected,
		ArticlePath: "38-tema/generated/article.txt",
		HTMLPath:    "38-tema/article.html",
	}
}

type fakeRegenerateArtifacts struct {
	files      map[string]bool
	resetCalls int
	removed    []string
	err        error
}

func newFakeRegenerateArtifacts(paths ...string) *fakeRegenerateArtifacts {
	files := make(map[string]bool, len(paths))
	for _, path := range paths {
		files[path] = true
	}
	return &fakeRegenerateArtifacts{files: files}
}

func (a *fakeRegenerateArtifacts) ResetGeneratedArtifacts(externalID, _ string) ([]string, error) {
	if a.err != nil {
		return nil, a.err
	}
	a.resetCalls++
	for path := range a.files {
		if strings.HasPrefix(path, externalID+"-") {
			a.removed = append(a.removed, path)
			delete(a.files, path)
		}
	}
	return a.removed, nil
}

func (a *fakeRegenerateArtifacts) Exists(relativePath string) bool { return a.files[relativePath] }

func regenerateTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func completedTestArticle() article.Article {
	return article.Article{ID: 2, ExternalID: "38", Title: "Как стать инструктором", Status: "completed"}
}

func TestRegenerateRebuildsCompletedArticle(t *testing.T) {
	repository := &fakeRegenerateRepository{selected: completedTestArticle()}
	artifacts := newFakeRegenerateArtifacts("38-tema/generated/article.txt", "38-tema/result.md")
	ran := ""
	run := func(_ context.Context, externalID string) error {
		ran = externalID
		if repository.selected.Status != "pending" {
			t.Fatalf("пайплайн запущен со статусом %s, ожидался pending", repository.selected.Status)
		}
		repository.complete()
		artifacts.files["38-tema/result.md"] = true
		return nil
	}

	if err := runRegenerate(context.Background(), repository, artifacts, run, regenerateTestLogger(), "38"); err != nil {
		t.Fatal(err)
	}
	if ran != "38" || repository.resetCalls != 1 || artifacts.resetCalls != 1 {
		t.Fatalf("прогон=%q сбросов состояния=%d сбросов файлов=%d", ran, repository.resetCalls, artifacts.resetCalls)
	}
	if repository.selected.Status != "completed" {
		t.Fatalf("итоговый статус = %s", repository.selected.Status)
	}
}

func TestRegenerateRestartsFailedArticle(t *testing.T) {
	message := "перед HTML обнаружен поясняющий текст"
	step := "html_generation"
	failed := completedTestArticle()
	failed.Status = "failed"
	failed.CurrentStep = &step
	failed.ErrorMessage = &message
	repository := &fakeRegenerateRepository{selected: failed}
	artifacts := newFakeRegenerateArtifacts("38-tema/generated/article.txt")
	run := func(_ context.Context, _ string) error {
		if repository.selected.ErrorMessage != nil {
			t.Fatal("ошибка не очищена перед прогоном")
		}
		if repository.selected.CurrentStep != nil {
			t.Fatalf("этап не сброшен: %s", *repository.selected.CurrentStep)
		}
		repository.complete()
		artifacts.files["38-tema/result.md"] = true
		return nil
	}

	if err := runRegenerate(context.Background(), repository, artifacts, run, regenerateTestLogger(), "38"); err != nil {
		t.Fatal(err)
	}
	if repository.resetIDs[0] != 2 {
		t.Fatalf("сброшена статья %d", repository.resetIDs[0])
	}
}

func TestRegenerateStartsPipelineFromFirstStage(t *testing.T) {
	// После сброса состояние статьи такое же, как у только что импортированной: раннер
	// обязан выбрать первый незавершённый этап, а не продолжить с прежнего.
	repository := &fakeRegenerateRepository{selected: completedTestArticle()}
	artifacts := newFakeRegenerateArtifacts("38-tema/result.md")
	var stageAtStart pipelineStage
	run := func(_ context.Context, _ string) error {
		state := pipelineState{
			Status:      repository.selected.Status,
			CurrentStep: optionalText(repository.selected.CurrentStep),
		}
		stageAtStart = nextStage(state)
		repository.complete()
		artifacts.files["38-tema/result.md"] = true
		return nil
	}

	if err := runRegenerate(context.Background(), repository, artifacts, run, regenerateTestLogger(), "38"); err != nil {
		t.Fatal(err)
	}
	if stageAtStart != stagePrepare {
		t.Fatalf("пайплайн начал с %q, ожидался первый этап", stageAtStart)
	}
}

func TestRegenerateRemovesGeneratedFiles(t *testing.T) {
	repository := &fakeRegenerateRepository{selected: completedTestArticle()}
	artifacts := newFakeRegenerateArtifacts(
		"38-tema/generated/article.txt", "38-tema/generated/structure.txt",
		"38-tema/article.html", "38-tema/result.md",
	)
	run := func(_ context.Context, _ string) error {
		if len(artifacts.files) != 0 {
			t.Fatalf("к началу прогона остались файлы: %v", artifacts.files)
		}
		repository.complete()
		artifacts.files["38-tema/result.md"] = true
		return nil
	}

	if err := runRegenerate(context.Background(), repository, artifacts, run, regenerateTestLogger(), "38"); err != nil {
		t.Fatal(err)
	}
	if len(artifacts.removed) != 4 {
		t.Fatalf("удалено %d файлов: %v", len(artifacts.removed), artifacts.removed)
	}
}

func TestRegenerateFailsWhenPipelineLeftArticleUnfinished(t *testing.T) {
	repository := &fakeRegenerateRepository{selected: completedTestArticle()}
	artifacts := newFakeRegenerateArtifacts()
	// Прогон завершился без ошибки, но статью не довёл: проверка обязана это поймать.
	run := func(_ context.Context, _ string) error { return nil }

	err := runRegenerate(context.Background(), repository, artifacts, run, regenerateTestLogger(), "38")
	if err == nil {
		t.Fatal("незавершённая статья принята как пересозданная")
	}
	for _, want := range []string{"статус pending", "html_path пуст", "result.md не найден"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в ошибке нет %q: %v", want, err)
		}
	}
}

func TestRegenerateStopsWhenPipelineFails(t *testing.T) {
	repository := &fakeRegenerateRepository{selected: completedTestArticle()}
	artifacts := newFakeRegenerateArtifacts("38-tema/generated/article.txt")
	wantErr := errors.New("LLM недоступен")

	err := runRegenerate(context.Background(), repository, artifacts,
		func(context.Context, string) error { return wantErr }, regenerateTestLogger(), "38")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if repository.resetCalls != 1 || artifacts.resetCalls != 1 {
		t.Fatal("сброс должен произойти до запуска пайплайна")
	}
}

// Пересоздание выкладывается тем же публикатором и тем же выключателем, что и полный
// прогон, но обёртка стоит поверх всего runRegenerate: публиковать статью раньше, чем
// verifyRegenerated ответит, достроена ли она, незачем.
func TestRegenerateWrappedByPublisherPublishesVerifiedArticle(t *testing.T) {
	repository := &fakeRegenerateRepository{selected: completedTestArticle()}
	artifacts := newFakeRegenerateArtifacts("38-tema/generated/article.txt", "38-tema/result.md")
	run := func(context.Context, string) error {
		repository.complete()
		artifacts.files["38-tema/result.md"] = true
		return nil
	}
	var published []string
	publisher := newRunPublisher(func(_ context.Context, externalID string) error {
		if repository.selected.Status != "completed" {
			t.Fatalf("публикация начата со статусом %s", repository.selected.Status)
		}
		published = append(published, externalID)
		return nil
	}, regenerateTestLogger())

	regenerateOne := func(ctx context.Context, externalID string) error {
		return runRegenerate(ctx, repository, artifacts, run, regenerateTestLogger(), externalID)
	}
	if err := publisher.wrap(regenerateOne)(context.Background(), "38"); err != nil {
		t.Fatal(err)
	}
	if len(published) != 1 || published[0] != "38" {
		t.Fatalf("опубликовано %v, ожидалась одна статья 38", published)
	}
}

func TestRegenerateWrappedByPublisherKeepsUnfinishedArticleOutOfBlog(t *testing.T) {
	repository := &fakeRegenerateRepository{selected: completedTestArticle()}
	artifacts := newFakeRegenerateArtifacts()
	// Прогон завершился без ошибки, но статью не довёл: проверка это поймает, а в блог
	// уходить нечему.
	run := func(context.Context, string) error { return nil }
	publisher := newRunPublisher(func(context.Context, string) error {
		t.Fatal("незавершённая статья ушла в блог")
		return nil
	}, regenerateTestLogger())

	regenerateOne := func(ctx context.Context, externalID string) error {
		return runRegenerate(ctx, repository, artifacts, run, regenerateTestLogger(), externalID)
	}
	if err := publisher.wrap(regenerateOne)(context.Background(), "38"); err == nil {
		t.Fatal("незавершённая статья принята как пересозданная")
	}
}
