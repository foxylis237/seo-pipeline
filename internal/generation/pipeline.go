package generation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/article"
	"github.com/foxylis237/seo-pipeline/internal/llm"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/output"
)

type PipelineRepository interface {
	StructureRepository
	GetGenerationInput(ctx context.Context, externalID string) (article.GenerationInput, error)
	GetSavedGenerationInput(ctx context.Context, externalID string) (article.SavedGenerationInput, error)
	BeginGeneration(ctx context.Context, articleID int64) error
	BeginGenerationStage(ctx context.Context, articleID int64, stage string) error
	SaveGenerationPaths(ctx context.Context, articleID int64, structurePath, articlePath string) error
	SaveArticleInfo(ctx context.Context, articleID int64, info string) error
	SaveReviewPath(ctx context.Context, articleID int64, reviewPath string) error
	SaveFixedArticlePath(ctx context.Context, articleID int64, fixedArticlePath string) error
	SaveHTMLPath(ctx context.Context, articleID int64, htmlPath string) error
	SaveError(ctx context.Context, articleID int64, processingErr error) error
}

type PipelineWriter interface {
	StructureWriter
	SaveArticle(externalID, slug, prompt, text, model string) (articleoutput.ArticlePaths, error)
	SaveArticleInfo(externalID, slug, prompt, info string) (articleoutput.ArticlePaths, error)
	SaveReview(externalID, slug, prompt, review string) (articleoutput.ArticlePaths, error)
	SaveFixedArticle(externalID, slug, prompt, article string) (articleoutput.ArticlePaths, error)
	SaveHTML(externalID, slug, prompt, html string) (articleoutput.ArticlePaths, error)
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
	logger           *slog.Logger
}

func NewPipeline(repository PipelineRepository, router *llm.Router, writer PipelineWriter, logger *slog.Logger) *Pipeline {
	return &Pipeline{
		repository: repository, writer: writer,
		structureService: NewStructureService(repository, router, writer, logger), router: router,
		logger: logger,
	}
}

func (p *Pipeline) RunByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	input, err := p.repository.GetGenerationInput(ctx, externalID)
	if err != nil {
		wrapped := &StageError{ExternalID: externalID, Stage: "load_generation_data", Err: err}
		p.logger.Error("generation pipeline failed", "article_id", int64(0), "external_id", externalID, "stage", wrapped.Stage, "error", err)
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

	articleOutput, err := p.runArticle(ctx, input, structureOutput.Structure, structureOutput.Paths.StructurePath)
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
	logger.Info("generation pipeline completed", "stage", "generation_pipeline", "duration_ms", time.Since(started).Milliseconds())
	return PipelineOutput{Paths: paths}, nil
}

type stageOutput struct {
	Text  string
	Paths articleoutput.ArticlePaths
}

func (p *Pipeline) runArticle(ctx context.Context, input article.GenerationInput, structure, structurePath string) (stageOutput, error) {
	started := time.Now()
	logger := p.stageLogger(input)
	logger.Info("article generation started", "stage", "article_generation")
	result, err := p.router.Generate(ctx, llm.Call{Stage: "article", ArticleID: input.Article.ID, Data: struct {
		Title              string
		Keywords           string
		LSIWords           string
		GeneratedStructure string
	}{
		Title: input.Article.Title, Keywords: formatKeywords(input.WordstatKeywords),
		LSIWords: strings.Join(input.LSIWords, "\n"), GeneratedStructure: structure,
	}})
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "article_generation", err)
	}
	text := strings.TrimSpace(strings.ReplaceAll(result.Text, "[[ARTICLE_COMPLETE]]", ""))
	if text == "" {
		return stageOutput{}, p.fail(ctx, logger, input, "article_generation", fmt.Errorf("article returned an empty response"))
	}
	paths, err := p.writer.SaveArticle(input.Article.ExternalID, input.Article.Slug, result.Prompt, text, result.Model)
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_article", err)
	}
	if err := p.repository.SaveGenerationPaths(ctx, input.Article.ID, structurePath, paths.ArticlePath); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_article_path", err)
	}
	logger.Info("article generation completed", "stage", "article_generation", "prompt_size", len([]rune(result.Prompt)), "input_tokens", result.InputTokens, "output_tokens", result.OutputTokens, "duration_ms", time.Since(started).Milliseconds(), "result_path", paths.ArticlePath)
	return stageOutput{Text: text, Paths: paths}, nil
}

func (p *Pipeline) runInfo(ctx context.Context, input article.GenerationInput, structure, articleText string) (stageOutput, error) {
	started := time.Now()
	logger := p.stageLogger(input)
	logger.Info("article info generation started", "stage", "info")
	logger.Info("waiting for Gemini response", "stage", "info")
	result, err := p.router.Generate(ctx, llm.Call{Stage: "info", ArticleID: input.Article.ID, Data: struct {
		Structure string
		Article   string
	}{Structure: structure, Article: articleText}})
	if err != nil {
		logger.Error("article info generation failed", "stage", "info", "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return stageOutput{}, p.fail(ctx, logger, input, "metadata_generation", err)
	}
	text := strings.TrimSpace(result.Text)
	if text == "" {
		err := fmt.Errorf("article info returned an empty response")
		logger.Error("article info generation failed", "stage", "info", "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return stageOutput{}, p.fail(ctx, logger, input, "metadata_generation", err)
	}
	paths, err := p.writer.SaveArticleInfo(input.Article.ExternalID, input.Article.Slug, result.Prompt, text)
	if err != nil {
		logger.Error("article info generation failed", "stage", "info", "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return stageOutput{}, p.fail(ctx, logger, input, "save_article_info", err)
	}
	if err := p.repository.SaveArticleInfo(ctx, input.Article.ID, text); err != nil {
		logger.Error("article info generation failed", "stage", "info", "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return stageOutput{}, p.fail(ctx, logger, input, "save_article_info_state", err)
	}
	logger.Info("article info saved", "stage", "info", "duration_ms", time.Since(started).Milliseconds(), "result_path", paths.ArticleInfoPath)
	return stageOutput{Text: text, Paths: paths}, nil
}

func (p *Pipeline) runReview(ctx context.Context, input article.GenerationInput, articleText string) (stageOutput, error) {
	started := time.Now()
	logger := p.logger.With("article_id", input.Article.ID, "external_id", input.Article.ExternalID)
	logger.Info("article review started", "stage", "article_review")
	result, err := p.router.Generate(ctx, llm.Call{Stage: "review", ArticleID: input.Article.ID, Data: struct{ Article string }{articleText}})
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "article_review", err)
	}
	text := strings.TrimSpace(result.Text)
	if text == "" {
		return stageOutput{}, p.fail(ctx, logger, input, "article_review", fmt.Errorf("review returned an empty response"))
	}
	paths, err := p.writer.SaveReview(input.Article.ExternalID, input.Article.Slug, result.Prompt, text)
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_article_review", err)
	}
	if err := p.repository.SaveReviewPath(ctx, input.Article.ID, paths.ReviewPath); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_article_review_path", err)
	}
	logger.Info("article review completed", "stage", "article_review", "prompt_size", len([]rune(result.Prompt)), "input_tokens", result.InputTokens, "output_tokens", result.OutputTokens, "duration_ms", time.Since(started).Milliseconds(), "result_path", paths.ReviewPath)
	return stageOutput{Text: text, Paths: paths}, nil
}

func (p *Pipeline) runFix(ctx context.Context, input article.GenerationInput, articleText, reviewText string) (stageOutput, error) {
	started := time.Now()
	logger := p.logger.With("article_id", input.Article.ID, "external_id", input.Article.ExternalID)
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
	paths, err := p.writer.SaveFixedArticle(input.Article.ExternalID, input.Article.Slug, result.Prompt, text)
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_fixed_article", err)
	}
	if err := p.repository.SaveFixedArticlePath(ctx, input.Article.ID, paths.FixedArticlePath); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_fixed_article_path", err)
	}
	logger.Info("article fix completed", "stage", "article_fix", "prompt_size", len([]rune(result.Prompt)), "input_tokens", result.InputTokens, "output_tokens", result.OutputTokens, "duration_ms", time.Since(started).Milliseconds(), "result_path", paths.FixedArticlePath)
	return stageOutput{Text: text, Paths: paths}, nil
}

func (p *Pipeline) runHTML(ctx context.Context, input article.GenerationInput, fixedArticle string) (stageOutput, error) {
	started := time.Now()
	logger := p.logger.With("article_id", input.Article.ID, "external_id", input.Article.ExternalID)
	logger.Info("HTML generation started", "stage", "html_generation")
	result, err := p.router.Generate(ctx, llm.Call{Stage: "html", ArticleID: input.Article.ID, Data: struct{ Article string }{fixedArticle}})
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "html_generation", err)
	}
	html, err := normalizeAndValidateHTML(result.Text)
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "validate_html", err)
	}
	paths, err := p.writer.SaveHTML(input.Article.ExternalID, input.Article.Slug, result.Prompt, html)
	if err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_html", err)
	}
	if err := p.repository.SaveHTMLPath(ctx, input.Article.ID, paths.HTMLPath); err != nil {
		return stageOutput{}, p.fail(ctx, logger, input, "save_html_path", err)
	}
	logger.Info("HTML generation completed", "stage", "html_generation", "prompt_size", len([]rune(result.Prompt)), "input_tokens", result.InputTokens, "output_tokens", result.OutputTokens, "duration_ms", time.Since(started).Milliseconds(), "result_path", paths.HTMLPath)
	return stageOutput{Text: html, Paths: paths}, nil
}

func (p *Pipeline) RunArticleByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	input, err := p.repository.GetGenerationInput(ctx, externalID)
	if err != nil {
		wrapped := &StageError{ExternalID: externalID, Stage: "load_article_data", Err: err}
		p.logger.Error("generation stage failed", "article_id", int64(0), "external_id", externalID, "stage", wrapped.Stage, "error", err)
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
	output, err := p.runArticle(ctx, input, structure, saved.StructurePath)
	return PipelineOutput{Paths: output.Paths}, err
}

func (p *Pipeline) RunInfoByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	saved, input, err := p.loadSavedInput(ctx, externalID, "info")
	if err != nil {
		return PipelineOutput{}, err
	}
	articleText, err := p.readRequiredArtifact(ctx, input, "info", "article", saved.ArticlePath)
	if err != nil {
		return PipelineOutput{}, err
	}
	structure, err := p.readRequiredArtifact(ctx, input, "info", "structure", saved.StructurePath)
	if err != nil {
		return PipelineOutput{}, err
	}
	if err := p.repository.BeginGenerationStage(ctx, input.Article.ID, "info"); err != nil {
		return PipelineOutput{}, p.fail(ctx, p.stageLogger(input), input, "begin_info", err)
	}
	output, err := p.runInfo(ctx, input, structure, articleText)
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
		p.logger.Error("generation stage failed", "article_id", int64(0), "external_id", externalID, "stage", wrapped.Stage, "error", err)
		return article.SavedGenerationInput{}, article.GenerationInput{}, wrapped
	}
	input := article.GenerationInput{Article: saved.Article, Professions: saved.Professions, Links: saved.Links}
	return saved, input, nil
}

func (p *Pipeline) readRequiredArtifact(ctx context.Context, input article.GenerationInput, stage, name, path string) (string, error) {
	logger := p.stageLogger(input)
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
	logger.Error("generation pipeline failed", "stage", stage, "error", err)
	if saveErr := p.repository.SaveError(ctx, input.Article.ID, wrapped); saveErr != nil {
		return errors.Join(wrapped, fmt.Errorf("сохранить ошибку статьи: %w", saveErr))
	}
	return wrapped
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
