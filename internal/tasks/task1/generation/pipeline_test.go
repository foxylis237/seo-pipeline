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

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/tasks/task1/output"
)

type fakeLLMClient struct {
	responses map[string]llm.Response
	errors    map[string]error
	calls     []string
	requests  map[string]llm.Request
}

type recordingChatFactory struct {
	results []llm.Response
	errAt   int
	prompts []string
	chats   int
	closed  int
}

func (f *recordingChatFactory) NewChat(context.Context) (llm.Chat, error) {
	f.chats++
	return &recordingChat{factory: f}, nil
}

type recordingChat struct {
	factory *recordingChatFactory
	next    int
}

type fakeResultBuilder struct {
	err    error
	calls  int
	writer *articleoutput.Writer
}

func (b *fakeResultBuilder) Build(context.Context, string) (articleoutput.ArticlePaths, error) {
	b.calls++
	return articleoutput.ArticlePaths{ResultPath: "37-tema/result.md"}, b.err
}

func (b *fakeResultBuilder) BuildStaged(context.Context, string) (*articleoutput.PendingArtifact, error) {
	b.calls++
	if b.err != nil {
		return nil, b.err
	}
	return b.writer.StageResult("37", "tema", "result")
}

func newFakeResultBuilder(t *testing.T, err error) *fakeResultBuilder {
	t.Helper()
	return &fakeResultBuilder{err: err, writer: articleoutput.NewWriter(t.TempDir())}
}

func (c *recordingChat) Generate(_ context.Context, prompt string) (llm.Response, error) {
	c.factory.prompts = append(c.factory.prompts, prompt)
	if c.factory.errAt > 0 && c.next+1 == c.factory.errAt {
		return llm.Response{}, errors.New("chat request failed")
	}
	if c.next >= len(c.factory.results) {
		return llm.Response{}, errors.New("missing chat result")
	}
	result := c.factory.results[c.next]
	c.next++
	return result, nil
}

func (c *recordingChat) Close() error {
	c.factory.closed++
	return nil
}

func successfulChatFactory() *recordingChatFactory {
	return &recordingChatFactory{results: []llm.Response{
		{Text: "Исходная статья", Model: "gemini-test", InputTokens: 10, OutputTokens: 20},
		{Text: "Метки: Профессия, Обучение, Как стать\nTLDR:\nИтог.\nFAQ:\nВопрос: Как?\nОтвет: Так.", Model: "gemini-test"},
	}}
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
			prompt = "info from current chat|{{.GeneratedStructure}}"
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
	demoCompleted      bool
	demoStateSaved     bool
	completionCalls    int
}

func (r *fakePipelineRepository) GetDemoGenerationInput(_ context.Context, _ string) (article.GenerationInput, error) {
	return r.input, nil
}

func (r *fakePipelineRepository) CompleteGeneration(_ context.Context, _ int64) error {
	r.demoCompleted = true
	r.completionCalls++
	return nil
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

func (r *fakePipelineRepository) SaveArticleInfo(_ context.Context, _ int64, rawText string, _ article.ArticleInfo) error {
	r.articleInfo = rawText
	return nil
}

func (r *fakePipelineRepository) SaveDemoArticleInfo(_ context.Context, _ int64, articlePath, rawText string, _ article.ArticleInfo) error {
	r.articlePath = articlePath
	r.articleInfo = rawText
	r.demoStateSaved = true
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

func TestPipelineDoesNotSaveContextCancellationAsArticleError(t *testing.T) {
	repository := &fakePipelineRepository{}
	pipeline := &Pipeline{
		repository: repository,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	input := article.GenerationInput{Article: article.Article{ID: 7, ExternalID: "37"}}

	err := pipeline.fail(ctx, pipeline.stageLogger(input), input, "article_generation", context.Canceled)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("fail() error = %v, want context.Canceled", err)
	}
	if repository.savedError != nil {
		t.Fatalf("saved error = %v, want no persisted article error", repository.savedError)
	}
}

func TestDemoGenerateUsesOneChatSkipsHTMLAndKeepsMetadataStage(t *testing.T) {
	input := article.GenerationInput{Article: article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema"}}
	repository := &fakePipelineRepository{input: input}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := successfulPipelineClient()
	chatFactory := successfulChatFactory()
	builder := newFakeResultBuilder(t, nil)
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), chatFactory, articleoutput.NewWriter(t.TempDir()), logger, builder)

	output, err := pipeline.RunDemoByExternalID(context.Background(), "37")
	if err != nil {
		t.Fatal(err)
	}
	if chatFactory.chats != 1 || len(chatFactory.prompts) != 2 {
		t.Fatalf("chats=%d prompts=%d, want one chat and two prompts", chatFactory.chats, len(chatFactory.prompts))
	}
	if len(client.calls) != 0 || repository.htmlPath != "" {
		t.Fatalf("demo invoked routed/HTML stages: calls=%v html=%q", client.calls, repository.htmlPath)
	}
	if !repository.demoCompleted || repository.completionCalls != 1 || !repository.demoStateSaved || builder.calls != 1 || output.Paths.ResultPath == "" {
		t.Fatalf("demo completion: completed=%t completion_calls=%d result_calls=%d paths=%+v", repository.demoCompleted, repository.completionCalls, builder.calls, output.Paths)
	}
	if len(repository.begunStages) != 1 || repository.begunStages[0] != "article" {
		t.Fatalf("demo begun stages = %v", repository.begunStages)
	}
}

func TestDemoResultErrorDoesNotCompleteFlow(t *testing.T) {
	input := article.GenerationInput{Article: article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema"}}
	repository := &fakePipelineRepository{input: input}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	builder := newFakeResultBuilder(t, errors.New("result write failed"))
	pipeline := NewPipeline(repository, testGenerationRouter(successfulPipelineClient(), logger), successfulChatFactory(), articleoutput.NewWriter(t.TempDir()), logger, builder)

	_, err := pipeline.RunDemoByExternalID(context.Background(), "37")
	if err == nil || repository.demoCompleted || repository.savedError == nil {
		t.Fatalf("err=%v completed=%t saved_error=%v", err, repository.demoCompleted, repository.savedError)
	}
}

func TestDemoInfoFailureDoesNotPersistAtomicState(t *testing.T) {
	input := article.GenerationInput{Article: article.Article{ID: 7, ExternalID: "37", Title: "Тема", Slug: "tema"}}
	repository := &fakePipelineRepository{input: input}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	chatFactory := &recordingChatFactory{results: []llm.Response{{Text: "Статья", Model: "test"}, {Text: "неверный info", Model: "test"}}}
	pipeline := NewPipeline(repository, testGenerationRouter(successfulPipelineClient(), logger), chatFactory, articleoutput.NewWriter(t.TempDir()), logger, newFakeResultBuilder(t, nil))

	_, err := pipeline.RunDemoByExternalID(context.Background(), "37")
	if err == nil || repository.demoStateSaved || repository.demoCompleted || repository.savedError == nil {
		t.Fatalf("err=%v state_saved=%t completed=%t saved_error=%v", err, repository.demoStateSaved, repository.demoCompleted, repository.savedError)
	}
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
	chatFactory := successfulChatFactory()
	resultBuilder := newFakeResultBuilder(t, nil)
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), chatFactory, writer, logger, resultBuilder)

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
	wantOrder := []string{"structure", "review", "fix", "html"}
	if strings.Join(client.calls, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("stage order = %v", client.calls)
	}
	if chatFactory.chats != 1 || chatFactory.closed != 1 || len(chatFactory.prompts) != 2 {
		t.Fatalf("article chat: chats=%d closed=%d prompts=%d", chatFactory.chats, chatFactory.closed, len(chatFactory.prompts))
	}
	if !strings.Contains(chatFactory.prompts[0], "H1: Тема") || strings.Contains(chatFactory.prompts[1], "Исходная статья") {
		t.Fatalf("shared chat prompts = %#v", chatFactory.prompts)
	}
	if repository.articleInfo == "" {
		t.Fatal("article info was not saved to PostgreSQL repository")
	}
	if repository.completionCalls != 1 || resultBuilder.calls != 1 {
		t.Fatalf("completion calls = %d, result calls = %d; want 1 and 1", repository.completionCalls, resultBuilder.calls)
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
	if repository.completionCalls != 2 {
		t.Fatalf("completion calls after second generate = %d, want 2", repository.completionCalls)
	}
}

func TestFullPipelineResultBuildErrorDoesNotCompleteArticle(t *testing.T) {
	assertFullPipelineResultFailure(t, errors.New("result template build failed"))
}

func TestFullPipelineResultPublicationErrorDoesNotCompleteArticle(t *testing.T) {
	assertFullPipelineResultFailure(t, errors.New("result publication failed"))
}

func assertFullPipelineResultFailure(t *testing.T, resultErr error) {
	t.Helper()
	repository := &fakePipelineRepository{input: pipelineTestInput()}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := successfulPipelineClient()
	builder := newFakeResultBuilder(t, resultErr)
	pipeline := NewPipeline(
		repository,
		testGenerationRouter(client, logger),
		successfulChatFactory(),
		articleoutput.NewWriter(t.TempDir()),
		logger,
		builder,
	)

	if _, err := pipeline.RunByExternalID(context.Background(), "37"); !errors.Is(err, resultErr) {
		t.Fatalf("pipeline error = %v, want %v", err, resultErr)
	}
	if repository.htmlPath == "" {
		t.Fatal("HTML path was lost after result failure")
	}
	if repository.completionCalls != 0 || repository.demoCompleted {
		t.Fatalf("completion calls = %d, completed = %v; want no completion", repository.completionCalls, repository.demoCompleted)
	}
	if repository.savedError == nil || builder.calls != 1 {
		t.Fatalf("saved error = %v, result calls = %d", repository.savedError, builder.calls)
	}
	if strings.Join(client.calls, ",") != "structure,review,fix,html" {
		t.Fatalf("stages after result error = %v", client.calls)
	}
}

func TestPipelineStopsAfterStageError(t *testing.T) {
	root := t.TempDir()
	repository := &fakePipelineRepository{input: pipelineTestInput()}
	client := successfulPipelineClient()
	client.errors["fix"] = errors.New("fix failed")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), successfulChatFactory(), articleoutput.NewWriter(root), logger)

	if _, err := pipeline.RunByExternalID(context.Background(), "37"); err == nil {
		t.Fatal("pipeline error = nil")
	}
	if strings.Join(client.calls, ",") != "structure,review,fix" {
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
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), successfulChatFactory(), articleoutput.NewWriter(root), logger)

	if _, err := pipeline.RunByExternalID(context.Background(), "37"); err == nil {
		t.Fatal("pipeline error = nil")
	}
	if repository.reviewPath != "" || repository.fixedArticlePath != "" || repository.htmlPath != "" {
		t.Fatalf("empty result was saved: review=%q fixed=%q html=%q", repository.reviewPath, repository.fixedArticlePath, repository.htmlPath)
	}
	if strings.Join(client.calls, ",") != "structure,review" {
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
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), successfulChatFactory(), writer, logger)

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
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), successfulChatFactory(), writer, logger)

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
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), successfulChatFactory(), writer, logger)

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

func TestRunArticleByExternalIDGeneratesArticleAndInfoInOneChat(t *testing.T) {
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	paths, err := writer.SaveStructure("37", "tema", "structure prompt", "Сохранённая структура")
	if err != nil {
		t.Fatal(err)
	}
	input := pipelineTestInput()
	repository := &fakePipelineRepository{input: input, savedInput: savedPipelineInput(paths)}
	client := successfulPipelineClient()
	chatFactory := successfulChatFactory()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), chatFactory, writer, logger)

	output, err := pipeline.RunArticleByExternalID(context.Background(), "37")
	if err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 0 || strings.Join(repository.begunStages, ",") != "article" {
		t.Fatalf("calls=%v begun=%v", client.calls, repository.begunStages)
	}
	if chatFactory.chats != 1 || chatFactory.closed != 1 || len(chatFactory.prompts) != 2 {
		t.Fatalf("chat: created=%d closed=%d prompts=%d", chatFactory.chats, chatFactory.closed, len(chatFactory.prompts))
	}
	if !strings.Contains(chatFactory.prompts[0], "Сохранённая структура") {
		t.Fatalf("article prompt = %q", chatFactory.prompts[0])
	}
	if strings.Contains(chatFactory.prompts[1], "Исходная статья") || !strings.Contains(chatFactory.prompts[1], "Сохранённая структура") {
		t.Fatalf("info prompt has incorrect context: %q", chatFactory.prompts[1])
	}
	if output.Paths.ArticlePath == "" || repository.articlePath != output.Paths.ArticlePath {
		t.Fatalf("article result paths = %+v, %q", output.Paths, repository.articlePath)
	}
	if repository.articleInfo == "" {
		t.Fatal("article info was not saved")
	}
	for _, relativePath := range []string{"37-tema/prompts/article_info_prompt.txt", "37-tema/generated/article_info.txt"} {
		if _, err := os.Stat(filepath.Join(root, relativePath)); !os.IsNotExist(err) {
			t.Fatalf("info file must not be created: %s", relativePath)
		}
	}
}

func TestRunArticleByExternalIDSavesErrorWhenInfoFails(t *testing.T) {
	root := t.TempDir()
	writer := articleoutput.NewWriter(root)
	paths, err := writer.SaveStructure("37", "tema", "structure prompt", "structure")
	if err != nil {
		t.Fatal(err)
	}
	repository := &fakePipelineRepository{input: pipelineTestInput(), savedInput: savedPipelineInput(paths)}
	chatFactory := successfulChatFactory()
	chatFactory.errAt = 2
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipeline := NewPipeline(repository, testGenerationRouter(successfulPipelineClient(), logger), chatFactory, writer, logger)

	_, err = pipeline.RunArticleByExternalID(context.Background(), "37")
	if err == nil || repository.savedError == nil {
		t.Fatalf("error=%v saved_error=%v", err, repository.savedError)
	}
	if repository.articlePath == "" || repository.articleInfo != "" || chatFactory.closed != 1 {
		t.Fatalf("article_path=%q info=%q closed=%d", repository.articlePath, repository.articleInfo, chatFactory.closed)
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
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), successfulChatFactory(), writer, logger)

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
	pipeline := NewPipeline(repository, testGenerationRouter(client, logger), successfulChatFactory(), articleoutput.NewWriter(t.TempDir()), logger)

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
			"info":      {Text: "Метки: Профессия, Обучение, Как стать\nTLDR:\nИтог.\nFAQ:\nВопрос: Как?\nОтвет: Так."},
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
