package generation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/article"
	"github.com/foxylis237/seo-pipeline/internal/llm"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/output"
)

type StructureRepository interface {
	SaveStructurePath(ctx context.Context, articleID int64, structurePath string) error
}

type StructureWriter interface {
	SaveStructure(externalID, slug, prompt, structure string) (articleoutput.ArticlePaths, error)
}

type StructureOutput struct {
	Structure string
	Result    llm.Response
	Paths     articleoutput.ArticlePaths
}

// StructureService owns the reusable structure-generation business stage.
type StructureService struct {
	repository StructureRepository
	router     *llm.Router
	writer     StructureWriter
	logger     *slog.Logger
}

func NewStructureService(repository StructureRepository, router *llm.Router, writer StructureWriter, logger *slog.Logger) *StructureService {
	return &StructureService{repository: repository, router: router, writer: writer, logger: logger}
}

func (s *StructureService) Generate(ctx context.Context, input article.GenerationInput) (StructureOutput, error) {
	started := time.Now()
	logger := s.logger.With("article_id", input.Article.ID, "external_id", input.Article.ExternalID)
	logger.Info("генерация структуры запущена", "stage", "structure_generation")

	result, err := s.router.Generate(ctx, llm.Call{Stage: "structure", ArticleID: input.Article.ID, Data: struct {
		Title     string
		Structure string
	}{input.Article.Title, input.CompetitorStructure}})
	if err != nil {
		logger.Error("ошибка LLM при генерации структуры", "stage", "generate_structure", "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return StructureOutput{}, fmt.Errorf("сгенерировать структуру для external_id %q: %w", input.Article.ExternalID, err)
	}
	promptSize := len([]rune(result.Prompt))
	paths, err := s.writer.SaveStructure(input.Article.ExternalID, input.Article.Slug, result.Prompt, result.Text)
	if err != nil {
		logger.Error("ошибка сохранения структуры", "stage", "save_structure", "prompt_size", promptSize, "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return StructureOutput{}, fmt.Errorf("сохранить структуру для external_id %q: %w", input.Article.ExternalID, err)
	}
	if err := s.repository.SaveStructurePath(ctx, input.Article.ID, paths.StructurePath); err != nil {
		logger.Error("ошибка обновления PostgreSQL", "stage", "save_structure_path", "prompt_size", promptSize, "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return StructureOutput{}, fmt.Errorf("сохранить результат структуры для external_id %q: %w", input.Article.ExternalID, err)
	}
	logger.Info("генерация структуры успешно завершена", "stage", "structure_generation_complete", "provider", result.Provider, "model", result.Model, "prompt_size", promptSize, "duration_ms", time.Since(started).Milliseconds(), "input_tokens", result.InputTokens, "output_tokens", result.OutputTokens, "structure_path", paths.StructurePath)
	return StructureOutput{Structure: result.Text, Result: result.Response, Paths: paths}, nil
}
