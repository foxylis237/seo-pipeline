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
	"github.com/foxylis237/seo-pipeline/internal/llm"
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
