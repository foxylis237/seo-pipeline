package generation

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/output"
)

type fakeStructureRepository struct {
	savedID   int64
	savedPath string
}

func (r *fakeStructureRepository) SaveStructurePath(_ context.Context, articleID int64, path string) error {
	r.savedID, r.savedPath = articleID, path
	return nil
}

func TestStructureServiceGenerate(t *testing.T) {
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	input := article.GenerationInput{
		Article:             article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema"},
		CompetitorStructure: "H1 - Тема\nH2 - Раздел",
	}
	repository := &fakeStructureRepository{}
	factory := FakeGenerator{Result: GenerationResult{Text: "H1 - Новая тема\nH2 - Новый раздел", Model: "fake"}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := NewStructureService(repository, factory, writer, "fake", logger)

	output, err := service.Generate(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if output.Structure != factory.Result.Text {
		t.Fatalf("Structure = %q", output.Structure)
	}
	if repository.savedID != input.Article.ID || repository.savedPath != "37-tema/generated/structure.txt" {
		t.Fatalf("saved result = id %d path %q", repository.savedID, repository.savedPath)
	}
	assertGeneratedFile(t, filepath.Join(root, "37-tema", "generated", "structure.txt"), factory.Result.Text)
	assertGeneratedFile(t, filepath.Join(root, "37-tema", "prompts", "structure_prompt.txt"), "Структура конкурентов")
	if _, err := writer.SaveArticle("37", "tema", "article prompt", "existing article"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Generate(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "37-tema", "generated", "article.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale article was not removed: %v", err)
	}
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
