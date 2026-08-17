// Package demo assembles a DEMO folder next to the production artifacts of one article.
//
// Демо-сборка — отдельная операция подготовки файлов, а не продолжение боевого пайплайна:
// она ничего не пишет в PostgreSQL, не меняет статус, current_step и error_message статьи и
// не трогает боевые артефакты. Всё, что уже сделано, переиспользуется как есть; заново
// выполняется только то, чего действительно нет.
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
)

const (
	// FolderName — каталог демо-сборки внутри каталога статьи.
	FolderName = "DEMO"
	// FixLinksHTMLPromptFile — объединённый промпт второго сообщения ручного чата. Он
	// существует только для DEMO и боевые промпты не подменяет. Путь к нему собирается от
	// каталога промптов задачи и приходит в NewBuilder: имени задачи пакет не знает.
	FixLinksHTMLPromptFile = "demo/fix_links_html.txt"
)

// Подпапки DEMO повторяют раскладку боевого каталога статьи: то же имя папки — тот же вид
// содержимого, отдельную схему запоминать не нужно.
const (
	promptsFolder   = "prompts"
	generatedFolder = "generated"
	prepareFolder   = "prepare"
)

// Имена файлов внутри DEMO. В корне лежит ровно то, что открывают руками при ручном прогоне:
// готовый результат и два промпта ручного чата. Всё остальное — по подпапкам.
const (
	resultFile             = "result.md"
	articlePromptFile      = "article_prompt.txt"
	fixLinksHTMLPromptFile = "fix_links_html_prompt.txt"

	structurePromptFile   = promptsFolder + "/structure_prompt.txt"
	articleInfoPromptFile = promptsFolder + "/article_info_prompt.txt"

	structureFile   = generatedFolder + "/structure.txt"
	articleFile     = generatedFolder + "/article.txt"
	articleInfoFile = generatedFolder + "/article_info.txt"
)

// Repository читает сохранённое состояние статьи. Методов записи здесь нет намеренно: demo
// не имеет права двигать статью по пайплайну.
type Repository interface {
	GetResultInput(ctx context.Context, externalID string) (article.ResultInput, error)
	GetGenerationInput(ctx context.Context, externalID string) (article.GenerationInput, error)
	GetDemoGenerationInput(ctx context.Context, externalID string) (article.GenerationInput, error)
	GetSavedGenerationInput(ctx context.Context, externalID string) (article.SavedGenerationInput, error)
}

// Generator рендерит и выполняет настроенные стадии LLM. Реализуется *llm.Router, поэтому
// demo идёт теми же промптами и той же маршрутизацией, что и боевой прогон.
type Generator interface {
	Prepare(call llm.Call) (llm.PreparedCall, error)
	Generate(ctx context.Context, call llm.Call) (llm.RoutedResponse, error)
}

// ResultRenderer собирает result.md из сохранённых данных, не записывая его. metadata == nil
// означает «взять Метки, TL;DR и FAQ из PostgreSQL», иначе они берутся из переданного набора
// целиком.
type ResultRenderer interface {
	RenderForDemo(ctx context.Context, externalID, articleText string, metadata *article.ArticleInfo) (string, error)
}

// Artifacts читает боевые артефакты по путям относительно OUTPUT_DIR.
type Artifacts interface {
	Read(relativePath string) (string, error)
	Exists(relativePath string) bool
}

// Preparer собирает research статьи, когда его ещё нет. Реализуется существующей командой
// prepare: собственных интеграций с Keys.so и Arsenkin у demo-сборки нет.
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
	preparer         Preparer
	mergedPromptPath string
	logger           *slog.Logger
}

// NewBuilder собирает сборщик DEMO. mergedPromptPath — путь к объединённому промпту ручного
// чата; он свой у каждой задачи, поэтому приходит снаружи, а не берётся из константы.
func NewBuilder(root, mergedPromptPath string, repository Repository, artifacts Artifacts, result ResultRenderer, generator Generator, preparer Preparer, logger *slog.Logger) *Builder {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Builder{
		root: root, repository: repository, artifacts: artifacts, result: result,
		generator: generator, preparer: preparer, mergedPromptPath: mergedPromptPath, logger: logger,
	}
}

// Build пересобирает DEMO статьи целиком, независимо от её статуса и сохранённой ошибки.
//
// Ошибка отдельной стадии не отменяет сборку: папка публикуется с тем, что удалось собрать,
// а сама ошибка возвращается вызывающему — иначе неполный DEMO выглядел бы успешным.
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
	b.logger.Info("DEMO собран", "external_id", externalID, "stage", "demo_build",
		"demo_path", filepath.ToSlash(filepath.Join(state.directory, FolderName)))
	return stageErr
}

func (b *Builder) assemble(ctx context.Context, state articleState, staging string) error {
	b.copyPrepare(state, staging)
	structure, structureErr := b.structure(ctx, state, staging)
	articleText, articleErr := b.article(ctx, state, staging, structure)
	metadata, infoErr := b.articleInfo(ctx, state, staging, structure, articleText)
	promptErr := b.mergedPrompt(state, staging)
	resultErr := b.resultMarkdown(ctx, state, staging, articleText, metadata)
	// researchErr идёт в общий итог, а не прерывает сборку: без research папка выходит
	// неполной, но result.md и промпты в ней всё равно должны появиться.
	return errors.Join(state.researchErr, structureErr, articleErr, infoErr, promptErr, resultErr)
}

// publish заменяет предыдущую папку DEMO собранной. Прошлая версия удаляется только после
// того, как новая полностью собрана: прерванный прогон не должен оставить без материалов
// предыдущего.
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
