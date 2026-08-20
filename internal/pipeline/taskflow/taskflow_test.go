package taskflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
)

type fakeRepository struct {
	saved    article.SavedGenerationInput
	savedErr error
	failures []error
}

func (r *fakeRepository) GetSavedGenerationInput(context.Context, string) (article.SavedGenerationInput, error) {
	return r.saved, r.savedErr
}

func (r *fakeRepository) SaveError(_ context.Context, _ int64, processingErr error) error {
	r.failures = append(r.failures, processingErr)
	return nil
}

type fakeWriter struct{ files map[string]string }

func (w fakeWriter) Read(relativePath string) (string, error) { return w.files[relativePath], nil }

type fakeRenderer struct{ prompt string }

func (r fakeRenderer) Prepare(llm.Call) (llm.PreparedCall, error) {
	return llm.PreparedCall{Prompt: r.prompt}, nil
}

func newBase(repository *fakeRepository, writer fakeWriter, renderer fakeRenderer) *Base {
	return NewBase(repository, writer, nil, renderer, nil, nil)
}

// Маркер конца ответа в артефакт не попадает: его просит промпт, и статье он не принадлежит.
func TestAnswerTrimsCompletionMarker(t *testing.T) {
	base := newBase(&fakeRepository{}, fakeWriter{}, fakeRenderer{})
	send := func(context.Context, string) (string, error) {
		return "текст страницы [[ARTICLE_COMPLETE]]", nil
	}

	answer, err := base.Answer(context.Background(), send, "промпт", "article")
	if err != nil {
		t.Fatalf("Answer() = %v", err)
	}
	if answer != "текст страницы" {
		t.Fatalf("Answer() = %q, want %q", answer, "текст страницы")
	}
}

// Пустой ответ — отказ стадии, а не пустой артефакт: сохранить его значит показать человеку
// файл, которого модель не писала.
func TestAnswerRejectsEmptyResponse(t *testing.T) {
	base := newBase(&fakeRepository{}, fakeWriter{}, fakeRenderer{})
	send := func(context.Context, string) (string, error) { return "  [[ARTICLE_COMPLETE]] ", nil }

	if _, err := base.Answer(context.Background(), send, "промпт", "article"); err == nil {
		t.Fatal("Answer() = nil, want ошибку пустого ответа")
	}
}

// Пустой промпт до модели не доходит: расхождение полей шаблона и данных даёт не ошибку, а
// пустой текст, и заметить его можно только здесь.
func TestRenderRejectsEmptyPrompt(t *testing.T) {
	base := newBase(&fakeRepository{}, fakeWriter{}, fakeRenderer{prompt: "   "})

	if _, err := base.Render("structure", nil); err == nil {
		t.Fatal("Render() = nil, want ошибку пустого промпта")
	}
}

// Ошибка стадии уходит в состояние статьи с контекстом: по нему видно, на чём остановились.
func TestFailSavesStageError(t *testing.T) {
	repository := &fakeRepository{}
	base := newBase(repository, fakeWriter{}, fakeRenderer{})
	selected := article.Article{ID: 7, ExternalID: "7"}

	err := base.Fail(context.Background(), base.ArticleLogger(selected), selected, "html_generation", errors.New("отказ провайдера"))

	var stageErr *StageError
	if !errors.As(err, &stageErr) {
		t.Fatalf("Fail() = %v, want *StageError", err)
	}
	if stageErr.Stage != "html_generation" || stageErr.ArticleID != 7 {
		t.Fatalf("Fail() = %+v", stageErr)
	}
	if len(repository.failures) != 1 {
		t.Fatalf("сохранённые ошибки = %v, want одну", repository.failures)
	}
}

// Отменённый контекст в состояние статьи не пишется: это остановка процесса, а не отказ
// статьи, и на следующем запуске человек увидел бы «прогон прерван» вместо причины.
func TestFailKeepsStateOnCancelledContext(t *testing.T) {
	repository := &fakeRepository{}
	base := newBase(repository, fakeWriter{}, fakeRenderer{})
	selected := article.Article{ID: 7, ExternalID: "7"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := base.Fail(ctx, base.ArticleLogger(selected), selected, "html_generation", context.Canceled); err == nil {
		t.Fatal("Fail() = nil, want ошибку стадии")
	}
	if len(repository.failures) != 0 {
		t.Fatalf("сохранённые ошибки = %v, want ни одной", repository.failures)
	}
}

// Без структуры следующая стадия не начинается: промпт с пустым разделом ушёл бы в модель
// молча.
func TestSavedStructureRequiresStructure(t *testing.T) {
	base := newBase(&fakeRepository{}, fakeWriter{}, fakeRenderer{})

	_, err := base.SavedStructure(context.Background(), "7")
	if err == nil || !strings.Contains(err.Error(), "структура") {
		t.Fatalf("SavedStructure() = %v, want ошибку об отсутствии структуры", err)
	}
}

// Сохранённая структура читается с диска по пути из состояния статьи.
func TestSavedStructureReadsSavedFile(t *testing.T) {
	repository := &fakeRepository{saved: article.SavedGenerationInput{StructurePath: "7-slug/generated/structure.txt"}}
	writer := fakeWriter{files: map[string]string{"7-slug/generated/structure.txt": "H2 - Программа"}}
	base := newBase(repository, writer, fakeRenderer{})

	structure, err := base.SavedStructure(context.Background(), "7")
	if err != nil {
		t.Fatalf("SavedStructure() = %v", err)
	}
	if structure != "H2 - Программа" {
		t.Fatalf("SavedStructure() = %q", structure)
	}
}
