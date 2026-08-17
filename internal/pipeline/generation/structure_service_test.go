package generation

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
)

type fakeStructureRepository struct {
	savedID   int64
	savedPath string
	err       error
	trace     article.Trace
}

func (r *fakeStructureRepository) SaveStructurePath(_ context.Context, articleID int64, path string) error {
	r.savedID, r.savedPath = articleID, path
	return r.err
}

func (r *fakeStructureRepository) GetArticleTrace(_ context.Context, articleID int64) (article.Trace, error) {
	if r.trace.ArticleID != 0 {
		return r.trace, nil
	}
	return article.Trace{
		ArticleID: articleID, ExternalID: "37", Title: "Тема",
		Keyword: "ключ", ReferenceURL: "https://example.test/reference",
	}, nil
}

func TestStructureServiceDatabaseErrorKeepsPreviousFiles(t *testing.T) {
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	paths, err := writer.SaveStructure("37", "tema", "old prompt", "old structure")
	if err != nil {
		t.Fatal(err)
	}
	databaseErr := errors.New("save structure state failed")
	repository := &fakeStructureRepository{err: databaseErr}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := NewStructureService(repository, testStructureRouter(llm.Response{Text: "new structure"}, logger), writer, logger)
	input := article.GenerationInput{Article: article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema"}}

	if _, err := service.Generate(context.Background(), input); !errors.Is(err, databaseErr) {
		t.Fatalf("Generate() error = %v, want database error", err)
	}
	assertGeneratedFile(t, filepath.Join(root, filepath.FromSlash(paths.StructurePromptPath)), "old prompt")
	assertGeneratedFile(t, filepath.Join(root, filepath.FromSlash(paths.StructurePath)), "old structure")
}

// TestStructureServiceRejectsEmptyStructure фиксирует контракт ответа стадии: пустая
// структура не должна ни публиковаться, ни сохраняться. Иначе она проходит как успех, а
// статья запирается — следующий прогон видит непустой structure_path и падает на стадии
// article, указывая не на ту стадию.
func TestStructureServiceRejectsEmptyStructure(t *testing.T) {
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	repository := &fakeStructureRepository{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := NewStructureService(repository, testStructureRouter(llm.Response{Text: "  \n\t "}, logger), writer, logger)
	input := article.GenerationInput{Article: article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema"}}

	if _, err := service.Generate(context.Background(), input); !errors.Is(err, ErrEmptyStructure) {
		t.Fatalf("Generate() error = %v, want ErrEmptyStructure", err)
	}
	if repository.savedPath != "" {
		t.Fatalf("путь структуры сохранён при пустом ответе: %q", repository.savedPath)
	}
	if _, err := os.Stat(filepath.Join(root, "37-tema", "generated", "structure.txt")); !os.IsNotExist(err) {
		t.Fatalf("пустая структура опубликована на диск: %v", err)
	}
}

func TestStructureServiceGenerate(t *testing.T) {
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	input := article.GenerationInput{
		Article:             article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema"},
		CompetitorStructure: "H1 - Тема\nH2 - Раздел",
	}
	repository := &fakeStructureRepository{}
	result := llm.Response{Text: "H1 - Новая тема\nH2 - Новый раздел"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := NewStructureService(repository, testStructureRouter(result, logger), writer, logger)

	output, err := service.Generate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if output.Structure != result.Text {
		t.Fatalf("Structure = %q", output.Structure)
	}
	if repository.savedID != input.Article.ID || repository.savedPath != "37-tema/generated/structure.txt" {
		t.Fatalf("saved result = id %d path %q", repository.savedID, repository.savedPath)
	}
	assertGeneratedFile(t, filepath.Join(root, "37-tema", "generated", "structure.txt"), result.Text)
	assertGeneratedFile(t, filepath.Join(root, "37-tema", "prompts", "structure_prompt.txt"), "H1 - Тема")
	if _, err := writer.SaveArticle("37", "tema", "article prompt", "existing article", "fake"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Generate(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	assertGeneratedFile(t, filepath.Join(root, "37-tema", "generated", "article.txt"), "existing article")
}

func assertGeneratedFile(t *testing.T, path, contains string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), contains) {
		t.Fatalf("file %s does not contain %q", path, contains)
	}
}
