// Package taskflow даёт общий каркас потока генерации задачи: открыть диалог, отрендерить
// промпт, проверить ответ на пустоту, записать ошибку статьи, отдать промпт в публикацию.
// Порядок чатов, стадии и слоты артефактов держит поток самой задачи.
package taskflow

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/generation"
)

// completionMarker — маркер конца ответа, который модель ставит по просьбе промпта; из ответа вырезается.
const completionMarker = "[[ARTICLE_COMPLETE]]"

// Repository — то, что каркас требует от хранилища.
type Repository interface {
	GetSavedGenerationInput(ctx context.Context, externalID string) (article.SavedGenerationInput, error)
	SaveError(ctx context.Context, articleID int64, processingErr error) error
}

// Writer читает сохранённый артефакт.
type Writer interface {
	Read(relativePath string) (string, error)
}

// PromptRenderer рендерит промпт стадии из её шаблона и данных.
type PromptRenderer interface {
	Prepare(call llm.Call) (llm.PreparedCall, error)
}

// PromptPublisher выгружает основной промпт наружу; возвращает управление сразу и ошибок не поднимает.
type PromptPublisher interface {
	PublishArticlePrompt(job generation.ArticlePromptJob)
}

// StageError передаёт наверх контекст отказа стадии.
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

// Base — общая часть потока задачи; поток встраивает его.
type Base struct {
	repository Repository
	writer     Writer
	chats      ChatFactory
	prompts    PromptRenderer
	logger     *slog.Logger
	publisher  PromptPublisher
}

// NewBase собирает общую часть потока. publisher необязателен и может быть nil.
func NewBase(repository Repository, writer Writer, chats ChatFactory, prompts PromptRenderer,
	logger *slog.Logger, publisher PromptPublisher) *Base {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Base{repository: repository, writer: writer, chats: chats, prompts: prompts,
		logger: logger, publisher: publisher}
}

// NewChat открывает новый диалог по списку стадий.
func (b *Base) NewChat(ctx context.Context, articleID int64, stages ...string) (Chat, error) {
	return b.chats.NewChat(ctx, articleID, stages...)
}

// Message рендерит промпт стадии и отправляет его сообщением чата.
func (b *Base) Message(ctx context.Context, send Send, stage string, data any) (prompt, answer string, err error) {
	prompt, err = b.Render(stage, data)
	if err != nil {
		return "", "", err
	}
	answer, err = b.Answer(ctx, send, prompt, stage)
	if err != nil {
		return "", "", err
	}
	return prompt, answer, nil
}

// Answer выполняет одно сообщение чата и проверяет ответ на пустоту до сохранения.
func (b *Base) Answer(ctx context.Context, send Send, prompt, stage string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	text, err := send(ctx, prompt)
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, completionMarker, ""))
	if text == "" {
		return "", fmt.Errorf("LLM stage %q returned an empty response", stage)
	}
	return text, ctx.Err()
}

// Render собирает промпт стадии; пустой промпт — ошибка.
func (b *Base) Render(stage string, data any) (string, error) {
	prepared, err := b.prompts.Prepare(llm.Call{Stage: stage, Data: data})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(prepared.Prompt) == "" {
		return "", fmt.Errorf("LLM stage %q rendered an empty prompt", stage)
	}
	return prepared.Prompt, nil
}

// PublishPrompt отдаёт основной промпт в очередь публикации и сразу возвращает управление.
func (b *Base) PublishPrompt(job generation.ArticlePromptJob) {
	if b.publisher == nil {
		return
	}
	b.publisher.PublishArticlePrompt(job)
}

// CloseChat завершает диалог; отказ закрыть только пишется в лог.
func (b *Base) CloseChat(chat Chat, logger *slog.Logger, stage string) {
	if err := chat.Close(); err != nil {
		logger.Warn("не удалось закрыть чат", "stage", stage, "error", err)
	}
}

// Logger возвращает логгер потока.
func (b *Base) Logger() *slog.Logger { return b.logger }

// ArticleLogger добавляет к логгеру идентичность статьи.
func (b *Base) ArticleLogger(selected article.Article) *slog.Logger {
	return b.logger.With("article_id", selected.ID, "external_id", selected.ExternalID)
}

// SavedStructure читает структуру, сохранённую первым чатом.
func (b *Base) SavedStructure(ctx context.Context, externalID string) (string, error) {
	path, err := b.SavedStructurePath(ctx, externalID)
	if err != nil {
		return "", err
	}
	return b.writer.Read(path)
}

// SavedStructurePath возвращает путь структуры; без сохранённой структуры — ошибка.
func (b *Base) SavedStructurePath(ctx context.Context, externalID string) (string, error) {
	saved, err := b.repository.GetSavedGenerationInput(ctx, externalID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(saved.StructurePath) == "" {
		return "", fmt.Errorf("структура не сохранена: сначала выполните этап structure")
	}
	return saved.StructurePath, nil
}

// Fail сохраняет ошибку в состоянии статьи и возвращает её с контекстом стадии.
// При отменённом контексте ошибка не сохраняется: это остановка процесса, а не отказ статьи.
func (b *Base) Fail(ctx context.Context, logger *slog.Logger, selected article.Article, stage string, err error) error {
	wrapped := &StageError{ArticleID: selected.ID, ExternalID: selected.ExternalID, Stage: stage, Err: err}
	if ctx.Err() == nil {
		logger.Error("generation failed", "stage", stage, "error", err)
		if saveErr := b.repository.SaveError(ctx, selected.ID, wrapped); saveErr != nil {
			logger.Error("не удалось сохранить ошибку статьи", "stage", stage, "error", saveErr)
		}
	}
	return wrapped
}

// StageFailure собирает ошибку стадии до того, как статья прочитана.
func StageFailure(externalID, stage string, err error) error {
	return &StageError{ExternalID: externalID, Stage: stage, Err: err}
}
