package generation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/tasks/task1/output"
)

type PipelineRepository interface {
	StructureRepository
	GetGenerationInput(ctx context.Context, externalID string) (article.GenerationInput, error)
	GetSavedGenerationInput(ctx context.Context, externalID string) (article.SavedGenerationInput, error)
	BeginGeneration(ctx context.Context, articleID int64) error
	BeginGenerationStage(ctx context.Context, articleID int64, stage string) error
	SaveGenerationPaths(ctx context.Context, articleID int64, structurePath, articlePath string) error
	SaveArticleInfo(ctx context.Context, articleID int64, rawText string, info article.ArticleInfo) error
	SaveDemoArticleInfo(ctx context.Context, articleID int64, articlePath, rawText string, info article.ArticleInfo) error
	SaveReviewPath(ctx context.Context, articleID int64, reviewPath string) error
	SaveFixedArticlePath(ctx context.Context, articleID int64, fixedArticlePath string) error
	SaveHTMLPath(ctx context.Context, articleID int64, htmlPath string) error
	SaveError(ctx context.Context, articleID int64, processingErr error) error
	GetDemoGenerationInput(ctx context.Context, externalID string) (article.GenerationInput, error)
	CompleteGeneration(ctx context.Context, articleID int64) error
}

type ResultBuilder interface {
	Build(ctx context.Context, externalID string) (articleoutput.ArticlePaths, error)
	BuildStaged(ctx context.Context, externalID string) (*articleoutput.PendingArtifact, error)
}

type PipelineWriter interface {
	StructureWriter
	StageArticle(externalID, slug, prompt, text, model string) (*articleoutput.PendingArtifact, error)
	StageArticleInfo(externalID, slug, prompt, info string) (*articleoutput.PendingArtifact, error)
	StageReview(externalID, slug, prompt, review string) (*articleoutput.PendingArtifact, error)
	StageFixedArticle(externalID, slug, prompt, article string) (*articleoutput.PendingArtifact, error)
	StageHTML(externalID, slug, prompt, html string) (*articleoutput.PendingArtifact, error)
	Read(relativePath string) (string, error)
}

type PipelineOutput struct {
	Paths articleoutput.ArticlePaths
}

type StageError struct {
	ArticleID  int64
	ExternalID string
	Stage      string
	Err        error
}

func (e *StageError) Error() string {
	return fmt.Sprintf("article_id=%d external_id=%s stage=%s: %v", e.ArticleID, e.ExternalID, e.Stage, e.Err)
}
func (e *StageError) Unwrap() error { return e.Err }

// Pipeline orchestrates all currently implemented generation stages.
type Pipeline struct {
	repository       PipelineRepository
	writer           PipelineWriter
	structureService *StructureService
	router           *llm.Router
	chatFactory      ChatFactory
	logger           *slog.Logger
	resultBuilder    ResultBuilder
}

func NewPipeline(repository PipelineRepository, router *llm.Router, chatFactory ChatFactory, writer PipelineWriter, logger *slog.Logger, resultBuilder ...ResultBuilder) *Pipeline {
	pipeline := &Pipeline{
		repository: repository, writer: writer,
		structureService: NewStructureService(repository, router, writer, logger), router: router,
		chatFactory: chatFactory, logger: logger,
	}
	if len(resultBuilder) > 0 {
		pipeline.resultBuilder = resultBuilder[0]
	}
	return pipeline
}

func (p *Pipeline) RunByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	input, err := p.repository.GetGenerationInput(ctx, externalID)
	if err != nil {
		wrapped := &StageError{ExternalID: externalID, Stage: "load_generation_data", Err: err}
		if !isContextCancellation(ctx, err) {
			p.logger.Error("generation pipeline failed", "article_id", int64(0), "external_id", externalID, "stage", wrapped.Stage, "error", err)
		}
		return PipelineOutput{}, wrapped
	}
	return p.Run(ctx, input)
}

func (p *Pipeline) Run(ctx context.Context, input article.GenerationInput) (PipelineOutput, error) {
	started := time.Now()
	logger := p.logger.With("article_id", input.Article.ID, "external_id", input.Article.ExternalID)
	logger.Info("generation pipeline started", "stage", "generation_pipeline")
	if err := p.repository.BeginGeneration(ctx, input.Article.ID); err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "begin_generation", err)
	}
	logger.Info("article prepared for generation", "stage", "structure_generation")

	logger.Info("structure generation started", "stage", "structure_generation")
	structureOutput, err := p.structureService.Generate(ctx, input)
	if err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "structure_generation", err)
	}
	logger.Info("structure generation completed", "stage", "structure_generation", "result_path", structureOutput.Paths.StructurePath)
	if err := ctx.Err(); err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "structure_generation", err)
	}

	articleOutput, err := p.runArticleAndInfo(ctx, input, structureOutput.Structure, structureOutput.Paths.StructurePath, false)
	if err != nil {
		return PipelineOutput{}, err
	}
	articleText := articleOutput.Text
	paths := articleOutput.Paths

	reviewOutput, err := p.runReview(ctx, input, articleText)
	if err != nil {
		return PipelineOutput{}, err
	}
	fixOutput, err := p.runFix(ctx, input, articleText, reviewOutput.Text)
	if err != nil {
		return PipelineOutput{}, err
	}
	htmlOutput, err := p.runHTML(ctx, input, fixOutput.Text)
	if err != nil {
		return PipelineOutput{}, err
	}
	paths = htmlOutput.Paths
	if p.resultBuilder == nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "result_generation", fmt.Errorf("result builder is not configured"))
	}
	resultPaths, resultErr := p.resultBuilder.Build(ctx, input.Article.ExternalID)
	if resultErr != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "result_generation", resultErr)
	}
	paths.ResultPath = resultPaths.ResultPath
	if err := p.repository.CompleteGeneration(ctx, input.Article.ID); err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "complete_generation", err)
	}
	logger.Info("generation pipeline completed", "stage", "generation_pipeline", "duration_ms", time.Since(started).Milliseconds())
	return PipelineOutput{Paths: paths}, nil
}

// RunDemoByExternalID generates only article, info and result using one Gemini chat.
func (p *Pipeline) RunDemoByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	input, err := p.repository.GetDemoGenerationInput(ctx, externalID)
	if err != nil {
		return PipelineOutput{}, &StageError{ExternalID: externalID, Stage: "load_demo_data", Err: err}
	}
	logger := p.stageLogger(input)
	if err := p.repository.BeginGenerationStage(ctx, input.Article.ID, "article"); err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "begin_demo_generation", err)
	}
	output, err := p.runArticleAndInfo(ctx, input, "", "", true)
	if err != nil {
		return PipelineOutput{}, err
	}
	if p.resultBuilder == nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "result_generation", fmt.Errorf("result builder is not configured"))
	}
	resultPending, err := p.resultBuilder.BuildStaged(ctx, externalID)
	if err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "result_generation", err)
	}
	defer resultPending.Abort()
	if err := articleoutput.Commit(func() error {
		return p.repository.CompleteGeneration(ctx, input.Article.ID)
	}, resultPending); err != nil {
		return PipelineOutput{}, p.fail(ctx, logger, input, "complete_demo_generation", err)
	}
	output.Paths.ResultPath = resultPending.Paths.ResultPath
	return PipelineOutput{Paths: output.Paths}, nil
}

type stageOutput struct {
	Text  string
	Paths articleoutput.ArticlePaths
}

type articleStageOutput struct {
	stageOutput
	Info string
}

func (p *Pipeline) runArticleAndInfo(ctx context.Context, input article.GenerationInput, structure, structurePath string, atomicDemo bool) (output articleStageOutput, returnErr error) {
	started := time.Now()
	logger := p.stageLogger(input)
	if err := ctx.Err(); err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "article_generation", err)
	}
	logger.Info("article generation started", "stage", "article_generation")
	articleCall, err := p.router.Prepare(llm.Call{Stage: "article", ArticleID: input.Article.ID, Data: struct {
		Title              string
		Keywords           string
		LSIWords           string
		GeneratedStructure string
	}{
		Title: input.Article.Title, Keywords: formatKeywords(input.WordstatKeywords),
		LSIWords: strings.Join(input.LSIWords, "\n"), GeneratedStructure: structure,
	}})
	if err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "article_generation", err)
	}
	chat, err := p.chatFactory.NewChat(ctx)
	if err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "article_generation", err)
	}
	defer func() {
		if err := chat.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close article Gemini chat: %w", err))
		}
	}()
	logger.Info("Gemini chat created", "stage", "article_generation", "model", articleCall.Model)
	articleCtx, cancelArticle := context.WithTimeout(ctx, articleCall.Timeout)
	articleResult, err := chat.Generate(articleCtx, articleCall.Prompt)
	cancelArticle()
	if err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "article_generation", err)
	}
	text := strings.TrimSpace(strings.ReplaceAll(articleResult.Text, "[[ARTICLE_COMPLETE]]", ""))
	if text == "" {
		return articleStageOutput{}, p.fail(ctx, logger, input, "article_generation", fmt.Errorf("article returned an empty response"))
	}
	if err := ctx.Err(); err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "article_generation", err)
	}
	logger.Info("article generated", "stage", "article_generation", "model", articleResult.Model, "prompt_size", len([]rune(articleCall.Prompt)), "input_tokens", articleResult.InputTokens, "output_tokens", articleResult.OutputTokens, "duration_ms", time.Since(started).Milliseconds())
	articlePending, err := p.writer.StageArticle(input.Article.ExternalID, input.Article.Slug, articleCall.Prompt, text, articleResult.Model)
	if err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "save_article", err)
	}
	defer articlePending.Abort()
	paths := articlePending.Paths
	if !atomicDemo {
		if err := articleoutput.Commit(func() error {
			return p.repository.SaveGenerationPaths(ctx, input.Article.ID, structurePath, paths.ArticlePath)
		}, articlePending); err != nil {
			return articleStageOutput{}, p.fail(ctx, logger, input, "save_article_path", err)
		}
	}
	logger.Info("article saved", "stage", "article_generation", "model", articleResult.Model, "result_path", paths.ArticlePath)

	infoStarted := time.Now()
	if err := ctx.Err(); err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "metadata_generation", err)
	}
	logger.Info("article info generation started", "stage", "info")
	infoCall, err := p.router.Prepare(llm.Call{Stage: "info", ArticleID: input.Article.ID, Data: struct {
		GeneratedStructure string
	}{GeneratedStructure: structure}})
	if err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "metadata_generation", err)
	}
	infoCtx, cancelInfo := context.WithTimeout(ctx, infoCall.Timeout)
	infoResult, err := chat.Generate(infoCtx, infoCall.Prompt)
	cancelInfo()
	if err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "metadata_generation", err)
	}
	articleInfo := infoResult.Text
	if strings.TrimSpace(articleInfo) == "" {
		err := fmt.Errorf("article info returned an empty response")
		return articleStageOutput{}, p.fail(ctx, logger, input, "metadata_generation", err)
	}
	if err := ctx.Err(); err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "metadata_generation", err)
	}
	logger.Info("article info generated", "stage", "info", "model", infoResult.Model, "prompt_size", len([]rune(infoCall.Prompt)), "input_tokens", infoResult.InputTokens, "output_tokens", infoResult.OutputTokens, "duration_ms", time.Since(infoStarted).Milliseconds())
	logger.Info("article info parsing started", "stage", "info")
	parsedInfo, err := article.ParseArticleInfo(articleInfo)
	if err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "metadata_parsing", err)
	}
	logger.Info("article info parsed", "stage", "info")
	if atomicDemo {
		infoPending, stageErr := p.writer.StageArticleInfo(input.Article.ExternalID, input.Article.Slug, infoCall.Prompt, articleInfo)
		err = stageErr
		if err != nil {
			return articleStageOutput{}, p.fail(ctx, logger, input, "save_article_info_files", err)
		}
		defer infoPending.Abort()
		paths = infoPending.Paths
		err = articleoutput.Commit(func() error {
			return p.repository.SaveDemoArticleInfo(ctx, input.Article.ID, paths.ArticlePath, articleInfo, parsedInfo)
		}, articlePending, infoPending)
	} else {
		err = p.repository.SaveArticleInfo(ctx, input.Article.ID, articleInfo, parsedInfo)
	}
	if err != nil {
		return articleStageOutput{}, p.fail(ctx, logger, input, "save_article_info_state", err)
	}
	logger.Info("article info saved", "stage", "info")
	logger.Info("article stage completed", "stage", "article_generation", "duration_ms", time.Since(started).Milliseconds(), "result_path", paths.ArticlePath)
	return articleStageOutput{stageOutput: stageOutput{Text: text, Paths: paths}, Info: articleInfo}, nil
}

func (p *Pipeline) runReview(ctx context.Context, input article.GenerationInput, articleText string) (stageOutput, error) {
	started := time.Now()
	logger := p.logger.With("article_id", input.Article.ID, "external_id", input.Article.ExternalID)
	if err := ctx.Err(); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "article_review", err)
	}
	logger.Info("article review started", "stage", "article_review")
	result, err := p.router.Generate(ctx, llm.Call{Stage: "review", ArticleID: input.Article.ID, Data: struct{ Article string }{articleText}})
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "article_review", err)
	}
	text := strings.TrimSpace(result.Text)
	if text == "" {
		return stageOutput{}, p.fail(ctx, logger, input, "article_review", fmt.Errorf("review returned an empty response"))
	}
	if err := ctx.Err(); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "article_review", err)
	}
	pending, err := p.writer.StageReview(input.Article.ExternalID, input.Article.Slug, result.Prompt, text)
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_article_review", err)
	}
	defer pending.Abort()
	paths := pending.Paths
	if err := articleoutput.Commit(func() error {
		return p.repository.SaveReviewPath(ctx, input.Article.ID, paths.ReviewPath)
	}, pending); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_article_review_path", err)
	}
	logger.Info("article review completed", "stage", "article_review", "prompt_size", len([]rune(result.Prompt)), "input_tokens", result.InputTokens, "output_tokens", result.OutputTokens, "duration_ms", time.Since(started).Milliseconds(), "result_path", paths.ReviewPath)
	return stageOutput{Text: text, Paths: paths}, nil
}

func (p *Pipeline) runFix(ctx context.Context, input article.GenerationInput, articleText, reviewText string) (stageOutput, error) {
	started := time.Now()
	logger := p.logger.With("article_id", input.Article.ID, "external_id", input.Article.ExternalID)
	if err := ctx.Err(); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "article_fix", err)
	}
	logger.Info("article fix started", "stage", "article_fix")
	result, err := p.router.Generate(ctx, llm.Call{Stage: "fix", ArticleID: input.Article.ID, Data: struct {
		Article, Review, Professions, Links string
	}{articleText, reviewText, input.Professions, input.Links}})
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "article_fix", err)
	}
	text := strings.TrimSpace(strings.ReplaceAll(result.Text, "[[ARTICLE_COMPLETE]]", ""))
	if text == "" {
		return stageOutput{}, p.fail(ctx, logger, input, "article_fix", fmt.Errorf("fix returned an empty response"))
	}
	if err := ctx.Err(); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "article_fix", err)
	}
	pending, err := p.writer.StageFixedArticle(input.Article.ExternalID, input.Article.Slug, result.Prompt, text)
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_fixed_article", err)
	}
	defer pending.Abort()
	paths := pending.Paths
	if err := articleoutput.Commit(func() error {
		return p.repository.SaveFixedArticlePath(ctx, input.Article.ID, paths.FixedArticlePath)
	}, pending); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_fixed_article_path", err)
	}
	logger.Info("article fix completed", "stage", "article_fix", "prompt_size", len([]rune(result.Prompt)), "input_tokens", result.InputTokens, "output_tokens", result.OutputTokens, "duration_ms", time.Since(started).Milliseconds(), "result_path", paths.FixedArticlePath)
	return stageOutput{Text: text, Paths: paths}, nil
}

func (p *Pipeline) runHTML(ctx context.Context, input article.GenerationInput, fixedArticle string) (stageOutput, error) {
	started := time.Now()
	logger := p.logger.With("article_id", input.Article.ID, "external_id", input.Article.ExternalID)
	if err := ctx.Err(); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "html_generation", err)
	}
	logger.Info("HTML generation started", "stage", "html_generation")
	result, err := p.router.Generate(ctx, llm.Call{Stage: "html", ArticleID: input.Article.ID, Data: struct{ Article string }{fixedArticle}})
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "html_generation", err)
	}
	html, err := normalizeAndValidateHTML(result.Text)
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "validate_html", err)
	}
	if err := ctx.Err(); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "html_generation", err)
	}
	pending, err := p.writer.StageHTML(input.Article.ExternalID, input.Article.Slug, result.Prompt, html)
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_html", err)
	}
	defer pending.Abort()
	paths := pending.Paths
	if err := articleoutput.Commit(func() error {
		return p.repository.SaveHTMLPath(ctx, input.Article.ID, paths.HTMLPath)
	}, pending); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_html_path", err)
	}
	logger.Info("HTML generation completed", "stage", "html_generation", "prompt_size", len([]rune(result.Prompt)), "input_tokens", result.InputTokens, "output_tokens", result.OutputTokens, "duration_ms", time.Since(started).Milliseconds(), "result_path", paths.HTMLPath)
	return stageOutput{Text: html, Paths: paths}, nil
}

func (p *Pipeline) RunArticleByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	input, err := p.repository.GetGenerationInput(ctx, externalID)
	if err != nil {
		wrapped := &StageError{ExternalID: externalID, Stage: "load_article_data", Err: err}
		if !isContextCancellation(ctx, err) {
			p.logger.Error("generation stage failed", "article_id", int64(0), "external_id", externalID, "stage", wrapped.Stage, "error", err)
		}
		return PipelineOutput{}, wrapped
	}
	saved, err := p.repository.GetSavedGenerationInput(ctx, externalID)
	if err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "load_article_artifacts", err)
	}
	structure, err := p.readRequiredArtifact(ctx, input, "article", "structure", saved.StructurePath)
	if err != nil {
		return PipelineOutput{}, err
	}
	if err := p.repository.BeginGenerationStage(ctx, input.Article.ID, "article"); err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "begin_article", err)
	}
	output, err := p.runArticleAndInfo(ctx, input, structure, saved.StructurePath, false)
	return PipelineOutput{Paths: output.Paths}, err
}

func (p *Pipeline) RunReviewByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	saved, input, err := p.loadSavedInput(ctx, externalID, "review")
	if err != nil {
		return PipelineOutput{}, err
	}
	articleText, err := p.readRequiredArtifact(ctx, input, "review", "article", saved.ArticlePath)
	if err != nil {
		return PipelineOutput{}, err
	}
	if err := p.repository.BeginGenerationStage(ctx, input.Article.ID, "review"); err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "begin_review", err)
	}
	output, err := p.runReview(ctx, input, articleText)
	return PipelineOutput{Paths: output.Paths}, err
}

func (p *Pipeline) RunFixByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	saved, input, err := p.loadSavedInput(ctx, externalID, "fix")
	if err != nil {
		return PipelineOutput{}, err
	}
	articleText, err := p.readRequiredArtifact(ctx, input, "fix", "article", saved.ArticlePath)
	if err != nil {
		return PipelineOutput{}, err
	}
	reviewText, err := p.readRequiredArtifact(ctx, input, "fix", "review", saved.ReviewPath)
	if err != nil {
		return PipelineOutput{}, err
	}
	if err := p.repository.BeginGenerationStage(ctx, input.Article.ID, "fix"); err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "begin_fix", err)
	}
	output, err := p.runFix(ctx, input, articleText, reviewText)
	return PipelineOutput{Paths: output.Paths}, err
}

func (p *Pipeline) RunHTMLByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	saved, input, err := p.loadSavedInput(ctx, externalID, "html")
	if err != nil {
		return PipelineOutput{}, err
	}
	fixedArticle, err := p.readRequiredArtifact(ctx, input, "html", "fixed article", saved.FixedArticlePath)
	if err != nil {
		return PipelineOutput{}, err
	}
	if err := p.repository.BeginGenerationStage(ctx, input.Article.ID, "html"); err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "begin_html", err)
	}
	output, err := p.runHTML(ctx, input, fixedArticle)
	return PipelineOutput{Paths: output.Paths}, err
}

func (p *Pipeline) loadSavedInput(ctx context.Context, externalID, stage string) (article.SavedGenerationInput, article.GenerationInput, error) {
	saved, err := p.repository.GetSavedGenerationInput(ctx, externalID)
	if err != nil {
		wrapped := &StageError{ExternalID: externalID, Stage: "load_" + stage + "_data", Err: err}
		if !isContextCancellation(ctx, err) {
			p.logger.Error("generation stage failed", "article_id", int64(0), "external_id", externalID, "stage", wrapped.Stage, "error", err)
		}
		return article.SavedGenerationInput{}, article.GenerationInput{}, wrapped
	}
	input := article.GenerationInput{Article: saved.Article, Professions: saved.Professions, Links: saved.Links}
	return saved, input, nil
}

func (p *Pipeline) readRequiredArtifact(ctx context.Context, input article.GenerationInput, stage, name, path string) (string, error) {
	logger := p.stageLogger(input)
	if err := ctx.Err(); err != nil {
		return "", p.fail(ctx, logger, input, stage, err)
	}
	if strings.TrimSpace(path) == "" {
		return "", p.fail(ctx, logger, input, stage, fmt.Errorf("article_id=%d external_id=%s: missing saved %s result", input.Article.ID, input.Article.ExternalID, name))
	}
	text, err := p.writer.Read(path)
	if err != nil {
		return "", p.fail(ctx, logger, input, stage, fmt.Errorf("article_id=%d external_id=%s: read saved %s result: %w", input.Article.ID, input.Article.ExternalID, name, err))
	}
	if strings.TrimSpace(text) == "" {
		return "", p.fail(ctx, logger, input, stage, fmt.Errorf("article_id=%d external_id=%s: saved %s result is empty", input.Article.ID, input.Article.ExternalID, name))
	}
	return text, nil
}

func (p *Pipeline) stageLogger(input article.GenerationInput) *slog.Logger {
	return p.logger.With("article_id", input.Article.ID, "external_id", input.Article.ExternalID)
}

func (p *Pipeline) fail(ctx context.Context, logger *slog.Logger, input article.GenerationInput, stage string, err error) error {
	wrapped := &StageError{ArticleID: input.Article.ID, ExternalID: input.Article.ExternalID, Stage: stage, Err: err}
	if isContextCancellation(ctx, err) {
		return wrapped
	}
	logger.Error("generation pipeline failed", "stage", stage, "error", err)
	if saveErr := p.repository.SaveError(ctx, input.Article.ID, wrapped); saveErr != nil {
		return errors.Join(wrapped, fmt.Errorf("сохранить ошибку статьи: %w", saveErr))
	}
	return wrapped
}

func isContextCancellation(ctx context.Context, err error) bool {
	return errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled)
}

func formatKeywords(keywords []article.KeywordFrequency) string {
	var result strings.Builder
	for index, keyword := range keywords {
		if index > 0 {
			result.WriteByte('\n')
		}
		fmt.Fprintf(&result, "%s\t%d", keyword.Query, keyword.Frequency)
	}
	return result.String()
}
