package generation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/output"
	"github.com/foxylis237/seo-pipeline/internal/prompts"
)

type StructureRepository interface {
	SaveStructurePath(ctx context.Context, articleID int64, structurePath string) error
}

type StructureWriter interface {
	ResetArticle(externalID, slug string) error
	SaveStructure(externalID, slug, prompt, structure string) (articleoutput.ArticlePaths, error)
}

type StructureOutput struct {
	Structure string
	Result    GenerationResult
	Paths     articleoutput.ArticlePaths
}

// StructureService owns the reusable structure-generation business stage.
type StructureService struct {
	repository  StructureRepository
	chatFactory ChatFactory
	writer      StructureWriter
	model       string
	logger      *slog.Logger
}

func NewStructureService(repository StructureRepository, chatFactory ChatFactory, writer StructureWriter, model string, logger *slog.Logger) *StructureService {
	return &StructureService{repository: repository, chatFactory: chatFactory, writer: writer, model: model, logger: logger}
}

func (s *StructureService) Generate(ctx context.Context, input article.GenerationInput) (StructureOutput, error) {
	started := time.Now()
	logger := s.logger.With("article_id", input.Article.ID, "external_id", input.Article.ExternalID, "model", s.model)
	logger.Info("генерация структуры запущена", "stage", "structure_generation")

	prompt, err := prompts.BuildStructurePrompt(prompts.StructurePromptData{Title: input.Article.Title, Structure: input.CompetitorStructure})
	if err != nil {
		logger.Error("ошибка формирования structure prompt", "stage", "build_structure_prompt", "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return StructureOutput{}, fmt.Errorf("сформировать structure prompt для external_id %q: %w", input.Article.ExternalID, err)
	}
	promptSize := len([]rune(prompt))
	logger.Info("structure prompt сформирован", "stage", "build_structure_prompt", "prompt_size", promptSize)

	chat, err := s.chatFactory.NewChat(ctx)
	if err != nil {
		logger.Error("ошибка создания structure chat", "stage", "create_structure_chat", "prompt_size", promptSize, "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return StructureOutput{}, fmt.Errorf("создать structure chat для external_id %q: %w", input.Article.ExternalID, err)
	}
	result, err := chat.Generate(ctx, prompt)
	if err != nil {
		_ = chat.Close()
		logger.Error("ошибка Gemini при генерации структуры", "stage", "generate_structure", "prompt_size", promptSize, "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return StructureOutput{}, fmt.Errorf("сгенерировать структуру для external_id %q: %w", input.Article.ExternalID, err)
	}
	if err := s.writer.ResetArticle(input.Article.ExternalID, input.Article.Slug); err != nil {
		_ = chat.Close()
		logger.Error("ошибка очистки предыдущих файлов генерации", "stage", "reset_generation_files", "prompt_size", promptSize, "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return StructureOutput{}, fmt.Errorf("очистить файлы генерации для external_id %q: %w", input.Article.ExternalID, err)
	}
	paths, err := s.writer.SaveStructure(input.Article.ExternalID, input.Article.Slug, prompt, result.Text)
	if err != nil {
		_ = chat.Close()
		logger.Error("ошибка сохранения структуры", "stage", "save_structure", "prompt_size", promptSize, "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return StructureOutput{}, fmt.Errorf("сохранить структуру для external_id %q: %w", input.Article.ExternalID, err)
	}
	if err := chat.Close(); err != nil {
		logger.Error("ошибка закрытия structure chat", "stage", "close_structure_chat", "prompt_size", promptSize, "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return StructureOutput{}, fmt.Errorf("закрыть structure chat для external_id %q: %w", input.Article.ExternalID, err)
	}
	if err := s.repository.SaveStructurePath(ctx, input.Article.ID, paths.StructurePath); err != nil {
		logger.Error("ошибка обновления PostgreSQL", "stage", "save_structure_path", "prompt_size", promptSize, "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return StructureOutput{}, fmt.Errorf("сохранить результат структуры для external_id %q: %w", input.Article.ExternalID, err)
	}
	logger.Info("генерация структуры успешно завершена", "stage", "structure_generation_complete", "prompt_size", promptSize, "duration_ms", time.Since(started).Milliseconds(), "input_tokens", result.InputTokens, "output_tokens", result.OutputTokens, "structure_path", paths.StructurePath)
	return StructureOutput{Structure: result.Text, Result: result, Paths: paths}, nil
}
