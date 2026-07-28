package generation

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/output"
)

type fakePipelineRepository struct {
	input              article.GenerationInput
	structurePath      string
	articlePath        string
	structureArticleID int64
	articleArticleID   int64
}

func (r *fakePipelineRepository) GetGenerationInput(_ context.Context, _ string) (article.GenerationInput, error) {
	return r.input, nil
}

func (r *fakePipelineRepository) SaveStructurePath(_ context.Context, articleID int64, path string) error {
	r.structureArticleID = articleID
	r.structurePath = path
	return nil
}

func (r *fakePipelineRepository) SaveGenerationPaths(_ context.Context, articleID int64, structurePath, articlePath string) error {
	r.articleArticleID = articleID
	r.structurePath = structurePath
	r.articlePath = articlePath
	return nil
}

func TestPipelineRunByExternalIDRunsStructureArticleAndValidation(t *testing.T) {
	root := t.TempDir()
	input := article.GenerationInput{
		Article:             article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema"},
		CompetitorStructure: "H1 - Тема\nH2 - Раздел",
		WordstatKeywords:    []article.KeywordFrequency{{Query: "ключ", Frequency: 100}},
		LSIWords:            []string{"слово"},
	}
	repository := &fakePipelineRepository{input: input}
	factory := FakeGenerator{Result: GenerationResult{Text: "H1 - Тема\nH2 - Раздел\nТекст раздела", Model: "fake", InputTokens: 10, OutputTokens: 20}}
	writer := articleoutput.NewWriter(root)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipeline := NewPipeline(repository, factory, writer, "fake", logger)

	output, err := pipeline.RunByExternalID(context.Background(), "37")
	if err != nil {
		t.Fatal(err)
	}
	if repository.structureArticleID != input.Article.ID || repository.articleArticleID != input.Article.ID {
		t.Fatalf("saved IDs: structure=%d article=%d", repository.structureArticleID, repository.articleArticleID)
	}
	if output.Paths.StructurePath != "37-tema/generated/structure.txt" || output.Paths.ArticlePath != "37-tema/generated/article.txt" {
		t.Fatalf("paths = %+v", output.Paths)
	}
	for _, relativePath := range []string{
		"37-tema/prompts/structure_prompt.txt",
		"37-tema/generated/structure.txt",
		"37-tema/prompts/article_prompt.txt",
		"37-tema/generated/article.txt",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relativePath))); err != nil {
			t.Fatalf("expected %s: %v", relativePath, err)
		}
	}
}
