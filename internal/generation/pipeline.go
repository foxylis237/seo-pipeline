package generation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/output"
	"github.com/foxylis237/seo-pipeline/internal/prompts"
	"github.com/foxylis237/seo-pipeline/internal/validator"
)

type PipelineRepository interface {
	StructureRepository
	GetGenerationInput(ctx context.Context, externalID string) (article.GenerationInput, error)
	SaveGenerationPaths(ctx context.Context, articleID int64, structurePath, articlePath string) error
}

type PipelineWriter interface {
	StructureWriter
	SaveArticle(externalID, slug, prompt, text string) (articleoutput.ArticlePaths, error)
	Read(relativePath string) (string, error)
}

type PipelineOutput struct {
	Paths      articleoutput.ArticlePaths
	Validation validator.Report
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
	chatFactory      ChatFactory
	writer           PipelineWriter
	structureService *StructureService
	model            string
	logger           *slog.Logger
}

func NewPipeline(repository PipelineRepository, chatFactory ChatFactory, writer PipelineWriter, model string, logger *slog.Logger) *Pipeline {
	return &Pipeline{
		repository: repository, chatFactory: chatFactory, writer: writer,
		structureService: NewStructureService(repository, chatFactory, writer, model, logger),
		model:            model, logger: logger,
	}
}

func (p *Pipeline) RunByExternalID(ctx context.Context, externalID string) (PipelineOutput, error) {
	input, err := p.repository.GetGenerationInput(ctx, externalID)
	if err != nil {
		wrapped := &StageError{ExternalID: externalID, Stage: "load_generation_data", Err: err}
		p.logger.Error("generation pipeline failed", "article_id", int64(0), "external_id", externalID, "model", p.model, "stage", wrapped.Stage, "error", err)
		return PipelineOutput{}, wrapped
	}
	return p.Run(ctx, input)
}

func (p *Pipeline) Run(ctx context.Context, input article.GenerationInput) (PipelineOutput, error) {
	started := time.Now()
	logger := p.logger.With("article_id", input.Article.ID, "external_id", input.Article.ExternalID, "model", p.model)
	logger.Info("generation pipeline started", "stage", "generation_pipeline")

	logger.Info("structure generation started", "stage", "structure_generation")
	structureOutput, err := p.structureService.Generate(ctx, input)
	if err != nil {
		return PipelineOutput{}, p.fail(logger, input, "structure_generation", err)
	}
	logger.Info("structure generation completed", "stage", "structure_generation", "result_path", structureOutput.Paths.StructurePath)

	articleStarted := time.Now()
	logger.Info("article generation started", "stage", "article_generation")
	articlePrompt, err := prompts.BuildArticlePrompt(prompts.ArticlePromptData{
		Title: input.Article.Title, Keywords: formatKeywords(input.WordstatKeywords),
		LSIWords: strings.Join(input.LSIWords, "\n"), GeneratedStructure: structureOutput.Structure,
	})
	if err != nil {
		return PipelineOutput{}, p.fail(logger, input, "build_article_prompt", err)
	}
	promptSize := len([]rune(articlePrompt))
	chat, err := p.chatFactory.NewChat(ctx)
	if err != nil {
		return PipelineOutput{}, p.fail(logger, input, "create_article_chat", err)
	}
	articleResult, err := chat.Generate(ctx, articlePrompt)
	if err != nil {
		_ = chat.Close()
		return PipelineOutput{}, p.fail(logger, input, "article_generation", err)
	}
	articleText := strings.TrimSpace(strings.ReplaceAll(articleResult.Text, "[[ARTICLE_COMPLETE]]", ""))
	paths, err := p.writer.SaveArticle(input.Article.ExternalID, input.Article.Slug, articlePrompt, articleText)
	if err != nil {
		_ = chat.Close()
		return PipelineOutput{}, p.fail(logger, input, "save_article", err)
	}
	if err := chat.Close(); err != nil {
		return PipelineOutput{}, p.fail(logger, input, "close_article_chat", err)
	}
	if err := p.repository.SaveGenerationPaths(ctx, input.Article.ID, paths.StructurePath, paths.ArticlePath); err != nil {
		return PipelineOutput{}, p.fail(logger, input, "save_article_path", err)
	}
	logger.Info("article generation completed", "stage", "article_generation", "prompt_size", promptSize, "input_tokens", articleResult.InputTokens, "output_tokens", articleResult.OutputTokens, "duration_ms", time.Since(articleStarted).Milliseconds(), "result_path", paths.ArticlePath)

	validationStarted := time.Now()
	logger.Info("article validation started", "stage", "article_validation")
	savedArticle, err := p.writer.Read(paths.ArticlePath)
	if err != nil {
		return PipelineOutput{}, p.fail(logger, input, "article_validation", err)
	}
	keywords := make([]string, 0, len(input.WordstatKeywords))
	for _, keyword := range input.WordstatKeywords {
		keywords = append(keywords, keyword.Query)
	}
	requireFAQ, requireTable := validationRequirements(structureOutput.Structure)
	report := validator.Validate(validator.Input{
		Article: savedArticle, ExpectedStructure: structureOutput.Structure,
		Keywords: keywords, LSIWords: input.LSIWords, RequireFAQ: requireFAQ, RequireTable: requireTable,
	})
	fmt.Print(validator.FormatReport(input.Article.ID, input.Article.ExternalID, report))
	logger.Info("article validation completed", "stage", "article_validation", "duration_ms", time.Since(validationStarted).Milliseconds(), "validation_status", validator.ResultStatus(report))

	errorCount := 0
	for _, issue := range report.Issues {
		if issue.Severity == validator.SeverityError {
			errorCount++
		}
	}
	if errorCount == 0 {
		logger.Info("article correction skipped", "stage", "article_correction", "reason", "validation_has_no_errors")
	} else {
		logger.Warn("article correction skipped", "stage", "article_correction", "reason", "correction_stage_not_implemented", "validation_errors", errorCount)
	}
	logger.Warn("HTML generation skipped", "stage", "html_generation", "reason", "html_stage_not_implemented")
	logger.Info("generation pipeline completed", "stage", "generation_pipeline", "duration_ms", time.Since(started).Milliseconds(), "html_generated", false)
	return PipelineOutput{Paths: paths, Validation: report}, nil
}

func (p *Pipeline) fail(logger *slog.Logger, input article.GenerationInput, stage string, err error) error {
	wrapped := &StageError{ArticleID: input.Article.ID, ExternalID: input.Article.ExternalID, Stage: stage, Err: err}
	logger.Error("generation pipeline failed", "stage", stage, "error", err)
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

func validationRequirements(structure string) (requireFAQ, requireTable bool) {
	normalized := strings.ToLower(structure)
	requireFAQ = strings.Contains(normalized, "faq") || strings.Contains(normalized, "частые вопросы") || strings.Contains(normalized, "вопросы и ответы")
	requireTable = strings.Contains(normalized, "таблиц")
	return requireFAQ, requireTable
}
