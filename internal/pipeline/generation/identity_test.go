package generation

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
)

func TestAssertLoadedArticleRejectsAnotherArticle(t *testing.T) {
	loaded := article.Article{ID: 7, ExternalID: "37", Title: "Бариста"}
	if err := assertLoadedArticle("37", loaded); err != nil {
		t.Fatalf("matching article reported as foreign: %v", err)
	}
	if err := assertLoadedArticle("38", loaded); err == nil {
		t.Fatal("foreign article was accepted")
	}
}

func TestAssertArtifactOwnerRejectsForeignDirectory(t *testing.T) {
	if err := assertArtifactOwner("37", "structure", "37-barista/generated/structure.txt"); err != nil {
		t.Fatalf("own artifact rejected: %v", err)
	}
	if err := assertArtifactOwner("37", "structure", "38-okna/generated/structure.txt"); err == nil {
		t.Fatal("artifact of another article was accepted")
	}
	if err := assertArtifactOwner("3", "structure", "37-barista/generated/structure.txt"); err == nil {
		t.Fatal("external_id prefix must not match a longer external_id")
	}
}

func TestRunArticleStopsWhenStoredIdentityDoesNotMatch(t *testing.T) {
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	paths, err := writer.SaveStructure("37", "tema", "structure prompt", "Сохранённая структура")
	if err != nil {
		t.Fatal(err)
	}
	input := pipelineTestInput()
	repository := &fakePipelineRepository{input: input, savedInput: savedPipelineInput(paths)}
	// PostgreSQL знает статью под другим названием: значит, в памяти чужая статья.
	repository.trace = article.Trace{
		ArticleID: input.Article.ID, ExternalID: input.Article.ExternalID,
		Title: "Совсем другая статья", Keyword: "окна", ReferenceURL: "https://okna.test/montazh",
	}
	chatFactory := successfulChatFactory()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipeline := NewPipeline(repository, testGenerationRouter(successfulPipelineClient(), logger), chatFactory, writer, logger)

	_, err = pipeline.RunArticleByExternalID(context.Background(), "37")
	if err == nil || !strings.Contains(err.Error(), "идентичность статьи изменилась") {
		t.Fatalf("error = %v, want an identity mismatch failure", err)
	}
	if repository.articlePath != "" || repository.articleInfo != "" {
		t.Fatalf("generation results were saved for a mismatched article: %q / %q", repository.articlePath, repository.articleInfo)
	}
	if repository.savedError == nil {
		t.Fatal("identity mismatch was not persisted as an article error")
	}
}
