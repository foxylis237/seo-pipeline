// Package demo assembles a DEMO folder next to the production artifacts of one article.
//
// Сборка не пишет в PostgreSQL и не трогает боевые артефакты; заново выполняется только то,
// чего ещё нет.
package demo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
)

const (
	// FolderName — каталог демо-сборки внутри каталога статьи.
	FolderName = articleoutput.DemoFolder
	// FixLinksHTMLPromptFile — объединённый промпт ручного чата относительно каталога общих промптов.
	FixLinksHTMLPromptFile = "demo/fix_links_html.txt"
)

// Подпапки DEMO повторяют раскладку боевого каталога статьи.
const (
	promptsFolder   = articleoutput.PromptsFolder
	generatedFolder = "generated"
	prepareFolder   = "prepare"
)

// Имена файлов внутри DEMO. Промпт статьи лежит в prompts/, как в боевом каталоге:
// публикация в Google Docs ищет его по одному правилу.
const (
	resultFile             = "result.md"
	fixLinksHTMLPromptFile = "fix_links_html_prompt.txt"

	articlePromptFile     = promptsFolder + "/" + articleoutput.ArticlePromptFile
	structurePromptFile   = promptsFolder + "/structure_prompt.txt"
	articleInfoPromptFile = promptsFolder + "/article_info_prompt.txt"

	structureFile   = generatedFolder + "/structure.txt"
	articleFile     = generatedFolder + "/article.txt"
	articleInfoFile = generatedFolder + "/article_info.txt"
)

// infoStage — стадия метаданных публикации; есть не у каждой задачи.
const infoStage = "info"

// Repository читает сохранённое состояние статьи; методов записи нет — demo не двигает статью.
type Repository interface {
	GetResultInput(ctx context.Context, externalID string) (article.ResultInput, error)
	GetGenerationInput(ctx context.Context, externalID string) (article.GenerationInput, error)
	GetDemoGenerationInput(ctx context.Context, externalID string) (article.GenerationInput, error)
	GetSavedGenerationInput(ctx context.Context, externalID string) (article.SavedGenerationInput, error)
}

// Generator рендерит и выполняет настроенные стадии LLM (реализуется *llm.Router).
type Generator interface {
	Prepare(call llm.Call) (llm.PreparedCall, error)
	Generate(ctx context.Context, call llm.Call) (llm.RoutedResponse, error)
	// HasStage отвечает, есть ли стадия в схеме задачи; спрашивается до обращения к модели.
	HasStage(stage string) bool
}

// PromptData собирает данные промптов тех стадий, которые DEMO выполняет сам; набор полей
// задаёт задача. nil означает общий набор: Title, ключи, LSI и структура.
type PromptData interface {
	StructureData(input article.GenerationInput) any
	ArticleData(input article.GenerationInput, structure string) any
}

// ResultRenderer собирает result.md, не записывая его; metadata == nil — TL;DR и FAQ из PostgreSQL.
type ResultRenderer interface {
	RenderForDemo(ctx context.Context, externalID, articleText string, metadata *article.ArticleInfo) (string, error)
}

// Artifacts читает боевые артефакты по путям относительно OUTPUT_DIR.
type Artifacts interface {
	Read(relativePath string) (string, error)
	Exists(relativePath string) bool
}

// Preparer собирает research статьи, когда его ещё нет (реализуется командой prepare).
type Preparer interface {
	Prepare(ctx context.Context, externalID string) error
}

// PrepareFunc адаптирует функцию под Preparer.
type PrepareFunc func(ctx context.Context, externalID string) error

func (f PrepareFunc) Prepare(ctx context.Context, externalID string) error { return f(ctx, externalID) }

// Builder собирает DEMO одной статьи.
type Builder struct {
	root             string
	repository       Repository
	artifacts        Artifacts
	result           ResultRenderer
	generator        Generator
	promptData       PromptData
	preparer         Preparer
	mergedPromptPath string
	logger           *slog.Logger
}

// NewBuilder собирает сборщик DEMO; mergedPromptPath — путь к объединённому промпту ручного чата.
func NewBuilder(root, mergedPromptPath string, repository Repository, artifacts Artifacts, result ResultRenderer, generator Generator, promptData PromptData, preparer Preparer, logger *slog.Logger) *Builder {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Builder{
		root: root, repository: repository, artifacts: artifacts, result: result,
		generator: generator, promptData: promptData, preparer: preparer,
		mergedPromptPath: mergedPromptPath, logger: logger,
	}
}

// Build пересобирает DEMO статьи целиком; ошибка стадии не отменяет сборку, а возвращается.
func (b *Builder) Build(ctx context.Context, externalID string) error {
	state, err := b.load(ctx, externalID)
	if err != nil {
		return err
	}
	articleDirectory := filepath.Join(b.root, state.directory)
	if err := os.MkdirAll(articleDirectory, 0o755); err != nil {
		return fmt.Errorf("создать каталог статьи %s: %w", state.directory, err)
	}
	staging, err := os.MkdirTemp(articleDirectory, ".DEMO-")
	if err != nil {
		return fmt.Errorf("подготовить временный каталог DEMO для external_id %s: %w", externalID, err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	stageErr := b.assemble(ctx, state, staging)
	if err := publish(staging, filepath.Join(articleDirectory, FolderName)); err != nil {
		return errors.Join(stageErr, err)
	}
	b.logger.Info("сборка DEMO завершена", "external_id", externalID, "step", "build",
		"path", filepath.ToSlash(filepath.Join(state.directory, FolderName)))
	return stageErr
}

func (b *Builder) assemble(ctx context.Context, state articleState, staging string) error {
	b.copyPrepare(state, staging)
	structure, structureErr := b.structure(ctx, state, staging)
	articleText, articleErr := b.article(ctx, state, staging, structure)
	metadata, infoErr := b.articleInfo(ctx, state, staging, structure, articleText)
	promptErr := b.mergedPrompt(state, staging)
	resultErr := b.resultMarkdown(ctx, state, staging, articleText, metadata)
	// Без research сборка не прерывается: result.md и промпты всё равно должны появиться.
	return errors.Join(state.researchErr, structureErr, articleErr, infoErr, promptErr, resultErr)
}

// publish заменяет предыдущую папку DEMO собранной; прошлая удаляется только после замены.
func publish(staging, final string) error {
	previous := final + ".previous"
	if err := os.RemoveAll(previous); err != nil {
		return fmt.Errorf("удалить остаток предыдущей папки DEMO: %w", err)
	}
	if err := os.Rename(final, previous); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("отложить предыдущую папку DEMO: %w", err)
	}
	if err := os.Rename(staging, final); err != nil {
		if restoreErr := os.Rename(previous, final); restoreErr != nil && !errors.Is(restoreErr, os.ErrNotExist) {
			return errors.Join(fmt.Errorf("опубликовать папку DEMO: %w", err), restoreErr)
		}
		return fmt.Errorf("опубликовать папку DEMO: %w", err)
	}
	if err := os.RemoveAll(previous); err != nil {
		return fmt.Errorf("удалить предыдущую папку DEMO: %w", err)
	}
	return nil
}
