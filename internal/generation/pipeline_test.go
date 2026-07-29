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
	"time"

	"github.com/foxylis237/seo-pipeline/internal/article"
	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/llm"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/output"
)

type fakeLLMClient struct {
	responses map[string]llm.Response
	errors    map[string]error
	calls     []string
	requests  map[string]llm.Request
}

func (c *fakeLLMClient) Generate(_ context.Context, request llm.Request) (llm.Response, error) {
	c.calls = append(c.calls, request.Model)
	c.requests[request.Model] = request
	return c.responses[request.Model], c.errors[request.Model]
}

func testGenerationRouter(client *fakeLLMClient, logger *slog.Logger) *llm.Router {
	temperature := 0.3
	stages := map[string]config.LLMStageConfig{}
	for _, stage := range []string{"structure", "article", "info", "review", "fix", "html"} {
		prompt := stage + "|{{.Article}}"
		switch stage {
		case "structure":
			prompt = "structure|{{.Title}}|{{.Structure}}"
		case "article":
			prompt = "article|{{.Title}}|{{.Keywords}}|{{.LSIWords}}|{{.GeneratedStructure}}"
		case "fix":
			prompt = "fix|{{.Article}}|{{.Review}}|{{.Professions}}|{{.Links}}"
		case "info":
			prompt = "info|{{.Structure}}|{{.Article}}"
		}
		stages[stage] = config.LLMStageConfig{
			Provider: "fake", Model: stage, PromptTemplate: prompt,
			Temperature: &temperature, MaxTokens: 100, Timeout: time.Second,
		}
	}
	return llm.NewRouter(config.LLMConfig{
		Providers: map[string]config.LLMProviderConfig{"fake": {Type: "gemini", APIKeyEnv: "TEST"}},
		Stages:    stages,
	}, map[string]llm.Client{"fake": client}, logger)
}

func testStructureRouter(response llm.Response, logger *slog.Logger) *llm.Router {
	client := &fakeLLMClient{responses: map[string]llm.Response{"structure": response}, errors: map[string]error{}, requests: map[string]llm.Request{}}
	return testGenerationRouter(client, logger)
}

type fakePipelineRepository struct {
	input              article.GenerationInput
	savedInput         article.SavedGenerationInput
	structurePath      string
	articlePath        string
	articleInfo        string
	structureArticleID int64
	articleArticleID   int64
	reviewPath         string
	fixedArticlePath   string
	htmlPath           string
	savedError         error
	generationBegun    bool
	begunStages        []string
}

func (r *fakePipelineRepository) BeginGeneration(_ context.Context, _ int64) error {
	r.generationBegun = true
	return nil
}

func (r *fakePipelineRepository) BeginGenerationStage(_ context.Context, _ int64, stage string) error {
	r.begunStages = append(r.begunStages, stage)
	return nil
}

func (r *fakePipelineRepository) GetSavedGenerationInput(_ context.Context, _ string) (article.SavedGenerationInput, error) {
	return r.savedInput, nil
}

func (r *fakePipelineRepository) SaveReviewPath(_ context.Context, _ int64, path string) error {
	r.reviewPath = path
	return nil
}

func (r *fakePipelineRepository) SaveArticleInfo(_ context.Context, _ int64, info string) error {
	r.articleInfo = info
	return nil
}

func (r *fakePipelineRepository) SaveFixedArticlePath(_ context.Context, _ int64, path string) error {
	r.fixedArticlePath = path
	return nil
}

func (r *fakePipelineRepository) SaveHTMLPath(_ context.Context, _ int64, path string) error {
	r.htmlPath = path
	return nil
}

func (r *fakePipelineRepository) SaveError(_ context.Context, _ int64, err error) error {
	r.savedError = err
	return nil
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

func TestPipelineRunsRoutedStagesInOrderWithMinimalData(t *testing.T) {
	root := t.TempDir()
	input := article.GenerationInput{
		Article:             article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema", Status: "completed"},
		CompetitorStructure: "H1 - Тема\nH2 - Раздел",
		WordstatKeywords:    []article.KeywordFrequency{{Query: "ключ", Frequency: 100}},
		LSIWords:            []string{"слово"},
		Professions:         "Профессия логопеда",
		Links:               "/professions/logoped",
	}
	repository := &fakePipelineRepository{input: input}
	writer := articleoutput.NewWriter(root)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := successfulPipelineClient()
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), writer, logger)

	output, err := pipeline.RunByExternalID(context.Background(), "37")
	if err != nil {
		t.Fatal(err)
	}
	if repository.structureArticleID != input.Article.ID || repository.articleArticleID != input.Article.ID {
		t.Fatalf("saved IDs: structure=%d article=%d", repository.structureArticleID, repository.articleArticleID)
	}
	if !repository.generationBegun {
		t.Fatal("generation was not initialized before stages")
	}
	if output.Paths.StructurePath != "37-tema/generated/structure.txt" || output.Paths.ArticlePath != "37-tema/generated/article.txt" {
		t.Fatalf("paths = %+v", output.Paths)
	}
	for _, relativePath := range []string{
		"37-tema/prompts/structure_prompt.txt",
		"37-tema/generated/structure.txt",
		"37-tema/prompts/article_prompt.txt",
		"37-tema/generated/article.txt",
		"37-tema/generated/generation_context.json",
		"37-tema/prompts/article_review_prompt.txt",
		"37-tema/generated/review.txt",
		"37-tema/prompts/fix_article_prompt.txt",
		"37-tema/generated/fixed_article.txt",
		"37-tema/prompts/article_html_prompt.txt",
		"37-tema/article.html",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relativePath))); err != nil {
			t.Fatalf("expected %s: %v", relativePath, err)
		}
	}
	if repository.reviewPath != "37-tema/generated/review.txt" || repository.fixedArticlePath != "37-tema/generated/fixed_article.txt" || repository.htmlPath != "37-tema/article.html" {
		t.Fatalf("review/fix/html paths = %q, %q, %q", repository.reviewPath, repository.fixedArticlePath, repository.htmlPath)
	}
	wantOrder := []string{"structure", "article", "review", "fix", "html"}
	if strings.Join(client.calls, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("stage order = %v", client.calls)
	}
	if got := client.requests["review"].Prompt; !strings.Contains(got, "Исходная статья") || strings.Contains(got, "ключ") {
		t.Fatalf("review prompt = %q", got)
	}
	fixPrompt := client.requests["fix"].Prompt
	for _, required := range []string{"Исходная статья", "Замечания review", input.Professions, input.Links} {
		if !strings.Contains(fixPrompt, required) {
			t.Fatalf("fix prompt does not contain %q: %q", required, fixPrompt)
		}
	}
	htmlPrompt := client.requests["html"].Prompt
	if !strings.Contains(htmlPrompt, "Исправленная статья") || strings.Contains(htmlPrompt, "Исходная статья") {
		t.Fatalf("html prompt = %q", htmlPrompt)
	}
	if _, err := pipeline.RunByExternalID(context.Background(), "37"); err != nil {
		t.Fatalf("second generate: %v", err)
	}
}

func TestPipelineStopsAfterStageError(t *testing.T) {
	root := t.TempDir()
	repository := &fakePipelineRepository{input: pipelineTestInput()}
	client := successfulPipelineClient()
	client.errors["fix"] = errors.New("fix failed")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), articleoutput.NewWriter(root), logger)

	if _, err := pipeline.RunByExternalID(context.Background(), "37"); err == nil {
		t.Fatal("pipeline error = nil")
	}
	if strings.Join(client.calls, ",") != "structure,article,review,fix" {
		t.Fatalf("calls after error = %v", client.calls)
	}
	if repository.fixedArticlePath != "" || repository.htmlPath != "" || repository.savedError == nil {
		t.Fatalf("saved after error: fixed=%q html=%q error=%v", repository.fixedArticlePath, repository.htmlPath, repository.savedError)
	}
}

func TestPipelineDoesNotSaveEmptyReview(t *testing.T) {
	root := t.TempDir()
	repository := &fakePipelineRepository{input: pipelineTestInput()}
	client := successfulPipelineClient()
	client.responses["review"] = llm.Response{Text: " \n"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), articleoutput.NewWriter(root), logger)

	if _, err := pipeline.RunByExternalID(context.Background(), "37"); err == nil {
		t.Fatal("pipeline error = nil")
	}
	if repository.reviewPath != "" || repository.fixedArticlePath != "" || repository.htmlPath != "" {
		t.Fatalf("empty result was saved: review=%q fixed=%q html=%q", repository.reviewPath, repository.fixedArticlePath, repository.htmlPath)
	}
	if strings.Join(client.calls, ",") != "structure,article,review" {
		t.Fatalf("calls after empty result = %v", client.calls)
	}
}

func TestRunFixByExternalIDCallsOnlyFixAndSavesResult(t *testing.T) {
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	paths, err := writer.SaveStructure("37", "tema", "structure prompt", "structure")
	if err != nil {
		t.Fatal(err)
	}
	paths, err = writer.SaveArticle("37", "tema", "article prompt", "Сохранённая статья", "model")
	if err != nil {
		t.Fatal(err)
	}
	paths, err = writer.SaveReview("37", "tema", "review prompt", "Сохранённое review")
	if err != nil {
		t.Fatal(err)
	}
	repository := &fakePipelineRepository{savedInput: savedPipelineInput(paths)}
	client := successfulPipelineClient()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), writer, logger)

	output, err := pipeline.RunFixByExternalID(context.Background(), "37")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(client.calls, ",") != "fix" || strings.Join(repository.begunStages, ",") != "fix" {
		t.Fatalf("calls=%v begun=%v", client.calls, repository.begunStages)
	}
	if output.Paths.FixedArticlePath == "" || repository.fixedArticlePath != output.Paths.FixedArticlePath {
		t.Fatalf("fixed result paths = %+v, %q", output.Paths, repository.fixedArticlePath)
	}
	assertGeneratedFile(t, filepath.Join(root, filepath.FromSlash(output.Paths.FixedArticlePath)), "Исправленная статья")
	if _, err := pipeline.RunFixByExternalID(context.Background(), "37"); err != nil {
		t.Fatalf("second fix: %v", err)
	}
}

func TestRunReviewByExternalIDCallsOnlyReviewAndSavesResult(t *testing.T) {
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	paths, err := writer.SaveArticle("37", "tema", "article prompt", "Сохранённая статья", "model")
	if err != nil {
		t.Fatal(err)
	}
	repository := &fakePipelineRepository{savedInput: savedPipelineInput(paths)}
	client := successfulPipelineClient()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), writer, logger)

	output, err := pipeline.RunReviewByExternalID(context.Background(), "37")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(client.calls, ",") != "review" || strings.Join(repository.begunStages, ",") != "review" {
		t.Fatalf("calls=%v begun=%v", client.calls, repository.begunStages)
	}
	if output.Paths.ReviewPath == "" || repository.reviewPath != output.Paths.ReviewPath || repository.fixedArticlePath != "" {
		t.Fatalf("review result paths = %+v, review=%q fixed=%q", output.Paths, repository.reviewPath, repository.fixedArticlePath)
	}
	if _, err := pipeline.RunReviewByExternalID(context.Background(), "37"); err != nil {
		t.Fatalf("second review: %v", err)
	}
}

func TestRunHTMLByExternalIDCallsOnlyHTMLAndSavesResult(t *testing.T) {
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	paths, err := writer.SaveFixedArticle("37", "tema", "fix prompt", "Сохранённая исправленная статья")
	if err != nil {
		t.Fatal(err)
	}
	repository := &fakePipelineRepository{savedInput: savedPipelineInput(paths)}
	client := successfulPipelineClient()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), writer, logger)

	output, err := pipeline.RunHTMLByExternalID(context.Background(), "37")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(client.calls, ",") != "html" || strings.Join(repository.begunStages, ",") != "html" {
		t.Fatalf("calls=%v begun=%v", client.calls, repository.begunStages)
	}
	if output.Paths.HTMLPath == "" || repository.htmlPath != output.Paths.HTMLPath {
		t.Fatalf("HTML result paths = %+v, %q", output.Paths, repository.htmlPath)
	}
	if _, err := pipeline.RunHTMLByExternalID(context.Background(), "37"); err != nil {
		t.Fatalf("second html: %v", err)
	}
}

func TestRunArticleByExternalIDCallsOnlyArticleAndUsesSavedStructure(t *testing.T) {
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	paths, err := writer.SaveStructure("37", "tema", "structure prompt", "Сохранённая структура")
	if err != nil {
		t.Fatal(err)
	}
	input := pipelineTestInput()
	repository := &fakePipelineRepository{input: input, savedInput: savedPipelineInput(paths)}
	client := successfulPipelineClient()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), writer, logger)

	output, err := pipeline.RunArticleByExternalID(context.Background(), "37")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(client.calls, ",") != "article" || strings.Join(repository.begunStages, ",") != "article" {
		t.Fatalf("calls=%v begun=%v", client.calls, repository.begunStages)
	}
	if !strings.Contains(client.requests["article"].Prompt, "Сохранённая структура") {
		t.Fatalf("article prompt = %q", client.requests["article"].Prompt)
	}
	if output.Paths.ArticlePath == "" || repository.articlePath != output.Paths.ArticlePath {
		t.Fatalf("article result paths = %+v, %q", output.Paths, repository.articlePath)
	}
}

func TestRunInfoByExternalIDCallsOnlyInfoAndSavesPromptAndResult(t *testing.T) {
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	paths, err := writer.SaveStructure("37", "tema", "structure prompt", "H1: Сохранённая структура")
	if err != nil {
		t.Fatal(err)
	}
	paths, err = writer.SaveArticle("37", "tema", "article prompt", "Сохранённая статья", "model")
	if err != nil {
		t.Fatal(err)
	}
	repository := &fakePipelineRepository{savedInput: savedPipelineInput(paths)}
	client := successfulPipelineClient()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), writer, logger)

	output, err := pipeline.RunInfoByExternalID(context.Background(), "37")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(client.calls, ",") != "info" || strings.Join(repository.begunStages, ",") != "info" {
		t.Fatalf("calls=%v begun=%v", client.calls, repository.begunStages)
	}
	prompt := client.requests["info"].Prompt
	if !strings.Contains(prompt, "H1: Сохранённая структура") || !strings.Contains(prompt, "Сохранённая статья") {
		t.Fatalf("info prompt = %q", prompt)
	}
	if repository.articleInfo == "" || output.Paths.ArticleInfoPath == "" {
		t.Fatalf("saved info=%q paths=%+v", repository.articleInfo, output.Paths)
	}
	assertGeneratedFile(t, filepath.Join(root, filepath.FromSlash(output.Paths.ArticleInfoPromptPath)), prompt)
	assertGeneratedFile(t, filepath.Join(root, filepath.FromSlash(output.Paths.ArticleInfoPath)), repository.articleInfo)
	if _, err := pipeline.RunInfoByExternalID(context.Background(), "37"); err != nil {
		t.Fatalf("second info: %v", err)
	}
}

func TestRunInfoByExternalIDRequiresArticleAndStructure(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	articlePaths, err := writer.SaveArticle("37", "tema", "prompt", "article", "model")
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name    string
		saved   article.SavedGenerationInput
		missing string
	}{
		{name: "article", saved: savedPipelineInput(articleoutput.ArticlePaths{StructurePath: "unused"}), missing: "article"},
		{name: "structure", saved: savedPipelineInput(articlePaths), missing: "structure"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repository := &fakePipelineRepository{savedInput: testCase.saved}
			client := successfulPipelineClient()
			pipeline := NewPipeline(repository, testGenerationRouter(client, logger), writer, logger)
			_, err := pipeline.RunInfoByExternalID(context.Background(), "37")
			if err == nil || !strings.Contains(err.Error(), testCase.missing) {
				t.Fatalf("error = %v", err)
			}
			if len(client.calls) != 0 || len(repository.begunStages) != 0 {
				t.Fatalf("calls=%v begun=%v", client.calls, repository.begunStages)
			}
		})
	}
}

func TestRunFixByExternalIDRequiresReviewBeforeLLMCall(t *testing.T) {
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	paths, err := writer.SaveArticle("37", "tema", "article prompt", "Сохранённая статья", "model")
	if err != nil {
		t.Fatal(err)
	}
	saved := savedPipelineInput(paths)
	saved.ReviewPath = ""
	repository := &fakePipelineRepository{savedInput: saved}
	client := successfulPipelineClient()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), writer, logger)

	_, err = pipeline.RunFixByExternalID(context.Background(), "37")
	if err == nil || !strings.Contains(err.Error(), "missing saved review result") {
		t.Fatalf("error = %v", err)
	}
	if len(client.calls) != 0 || len(repository.begunStages) != 0 {
		t.Fatalf("calls=%v begun=%v", client.calls, repository.begunStages)
	}
}

func TestRunHTMLByExternalIDRequiresFixedArticle(t *testing.T) {
	repository := &fakePipelineRepository{savedInput: savedPipelineInput(articleoutput.ArticlePaths{})}
	client := successfulPipelineClient()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), articleoutput.NewWriter(t.TempDir()), logger)

	_, err := pipeline.RunHTMLByExternalID(context.Background(), "37")
	if err == nil || !strings.Contains(err.Error(), "missing saved fixed article result") {
		t.Fatalf("error = %v", err)
	}
	if len(client.calls) != 0 || len(repository.begunStages) != 0 {
		t.Fatalf("calls=%v begun=%v", client.calls, repository.begunStages)
	}
}

func savedPipelineInput(paths articleoutput.ArticlePaths) article.SavedGenerationInput {
	return article.SavedGenerationInput{
		Article:     article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema"},
		Professions: "Профессия логопеда", Links: "/professions/logoped",
		StructurePath: paths.StructurePath, ArticlePath: paths.ArticlePath,
		ReviewPath: paths.ReviewPath, FixedArticlePath: paths.FixedArticlePath,
	}
}

func successfulPipelineClient() *fakeLLMClient {
	return &fakeLLMClient{
		responses: map[string]llm.Response{
			"structure": {Text: "H1: Тема\nH2: Раздел"},
			"article":   {Text: "Исходная статья", InputTokens: 10, OutputTokens: 20},
			"info":      {Text: "**Название**\n\nТема\n\n**Метки**\n\nПрофессия, Обучение, Как стать"},
			"review":    {Text: "Замечания review"},
			"fix":       {Text: "Исправленная статья"},
			"html":      {Text: "```html\n<h2>Исправленная статья</h2>\n```"},
		},
		errors:   map[string]error{},
		requests: map[string]llm.Request{},
	}
}

func pipelineTestInput() article.GenerationInput {
	return article.GenerationInput{
		Article:             article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema"},
		CompetitorStructure: "H1 - Тема", WordstatKeywords: []article.KeywordFrequency{{Query: "ключ", Frequency: 100}},
		LSIWords: []string{"слово"}, Professions: "Профессия логопеда", Links: "/professions/logoped",
	}
}
