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
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/diagnostics"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/tasks/task1/output"
)

type StructureRepository interface {
	SaveStructurePath(ctx context.Context, articleID int64, structurePath string) error
	GetArticleTrace(ctx context.Context, articleID int64) (article.Trace, error)
}

type StructureWriter interface {
	StageStructure(externalID, slug, prompt, structure string) (*articleoutput.PendingArtifact, error)
}

type StructureOutput struct {
	Structure string
	Result    llm.Response
	Paths     articleoutput.ArticlePaths
}

// ErrEmptyStructure reports a model answer with no structure in it.
//
// Проверка обязана стоять до записи: сохранённая пустая структура публикуется как готовый
// артефакт, статья генерируется из пустоты без единой ошибки, а дальше каждый прогон видит
// непустой structure_path, уходит на стадию article и падает там на чужом имени стадии.
var ErrEmptyStructure = errors.New("structure returned an empty response")

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
	logger.Info("генерация структуры запущена", "stage", "structure_generation",
		"title", input.Article.Title,
		"competitor_structure_fingerprint", diagnostics.Fingerprint(input.CompetitorStructure),
		"competitor_structure_length", len([]rune(input.CompetitorStructure)),
	)
	trace, err := articleTrace(ctx, logger, s.repository, input)
	if err != nil {
		logger.Error("идентичность статьи не подтверждена", "stage", "verify_article_identity", "error", err)
		return StructureOutput{}, fmt.Errorf("подтвердить идентичность статьи external_id %q: %w", input.Article.ExternalID, err)
	}
	diagnostics.LogStep(s.logger, "llm_structure", "before", trace,
		"competitor_structure_fingerprint", diagnostics.Fingerprint(input.CompetitorStructure),
	)

	result, err := s.router.Generate(ctx, llm.Call{Stage: "structure", ArticleID: input.Article.ID, Data: struct {
		Title     string
		Structure string
	}{input.Article.Title, input.CompetitorStructure}})
	if err != nil {
		logger.Error("ошибка LLM при генерации структуры", "stage", "generate_structure", "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return StructureOutput{}, fmt.Errorf("сгенерировать структуру для external_id %q: %w", input.Article.ExternalID, err)
	}
	if strings.TrimSpace(result.Text) == "" {
		logger.Error("модель вернула пустую структуру", "stage", "generate_structure", "provider", result.Provider, "model", result.Model, "duration_ms", time.Since(started).Milliseconds())
		return StructureOutput{}, fmt.Errorf("сгенерировать структуру для external_id %q: %w", input.Article.ExternalID, ErrEmptyStructure)
	}
	promptSize := len([]rune(result.Prompt))
	pending, err := s.writer.StageStructure(input.Article.ExternalID, input.Article.Slug, result.Prompt, result.Text)
	if err != nil {
		logger.Error("ошибка сохранения структуры", "stage", "save_structure", "prompt_size", promptSize, "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return StructureOutput{}, fmt.Errorf("сохранить структуру для external_id %q: %w", input.Article.ExternalID, err)
	}
	defer pending.Abort()
	paths := pending.Paths
	if err := articleoutput.Commit(func() error {
		return s.repository.SaveStructurePath(ctx, input.Article.ID, paths.StructurePath)
	}, pending); err != nil {
		logger.Error("ошибка обновления PostgreSQL", "stage", "save_structure_path", "prompt_size", promptSize, "duration_ms", time.Since(started).Milliseconds(), "error", err)
		return StructureOutput{}, fmt.Errorf("сохранить результат структуры для external_id %q: %w", input.Article.ExternalID, err)
	}
	logger.Info("генерация структуры успешно завершена", "stage", "structure_generation_complete", "provider", result.Provider, "model", result.Model, "prompt_size", promptSize, "duration_ms", time.Since(started).Milliseconds(), "input_tokens", result.InputTokens, "output_tokens", result.OutputTokens, "structure_path", paths.StructurePath)
	diagnostics.LogStep(s.logger, "llm_structure", "after", trace,
		"prompt_fingerprint", diagnostics.Fingerprint(result.Prompt),
		"response_fingerprint", diagnostics.Fingerprint(result.Text),
		"result_path", paths.StructurePath,
	)
	return StructureOutput{Structure: result.Text, Result: result.Response, Paths: paths}, nil
}
