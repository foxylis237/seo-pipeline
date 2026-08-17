package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/generation"
	articleoutput "github.com/foxylis237/seo-pipeline/internal/pipeline/output"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/repository"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof1"
)

// newPProf1Flow собирает поток генерации pprof_1 поверх общего движка.
//
// Репозиторий, writer и роутер — те же, что у task_1: pprof_1 отличается порядком стадий и
// промптами, а не работой с базой и файлами.
func newPProf1Flow(
	articleRepository *repository.ArticleRepository,
	writer *articleoutput.Writer,
	router *llm.Router,
	logger *slog.Logger,
	publisher *googlePublisher,
) (*pprof1.Flow, error) {
	if router == nil {
		return nil, fmt.Errorf("схема стадий pprof_1 не загружена")
	}
	return pprof1.NewFlow(articleRepository, writer, pprof1.NewRouterChats(router), router, logger,
		pprof1PromptPublisher{publisher: publisher}), nil
}

// pprof1PromptPublisher переводит задание потока в задание общей очереди публикации.
//
// Очередь одна на процесс: за persistent-профилем Chromium держится flock, и вторая своя
// очередь у pprof_1 конфликтовала бы с публикацией из task_1 в том же профиле.
type pprof1PromptPublisher struct{ publisher *googlePublisher }

func (p pprof1PromptPublisher) PublishArticlePrompt(job pprof1.ArticlePromptJob) {
	if p.publisher == nil {
		return
	}
	p.publisher.PublishArticlePrompt(generation.ArticlePromptJob{
		ArticleID:  job.ArticleID,
		ExternalID: job.ExternalID,
		Title:      job.Title,
		Prompt:     job.Prompt,
		PromptPath: job.PromptPath,
	})
}

// pprof1StageExecutor выполняет этапы возобновляемого раннера потоком pprof_1.
//
// Этапы review и fix отдельными вызовами модели не выполняются: их артефакты рождаются в
// чате 2 вместе с текстом статьи. Раннер просит их только тогда, когда артефактов нет, — а
// единственный способ их получить — пройти чат 2 целиком, поэтому оба ведут туда же.
func pprof1StageExecutor(flow *pprof1.Flow, runPrepareStage, runResultStage func(context.Context, string) error) stageExecutor {
	return func(ctx context.Context, stage pipelineStage, externalID string) error {
		switch stage {
		case stagePrepare:
			return runPrepareStage(ctx, externalID)
		case stageStructure:
			return flow.RunStructure(ctx, externalID)
		case stageArticle, stageReview, stageFix:
			return flow.RunArticle(ctx, externalID)
		case stageHTML:
			return flow.RunHTML(ctx, externalID)
		case stageResult:
			return runResultStage(ctx, externalID)
		default:
			return fmt.Errorf("неизвестный этап пайплайна %q", stage)
		}
	}
}

// pprof1StageRunner отдаёт функцию одностадийной команды pprof_1.
func pprof1StageRunner(flow *pprof1.Flow, operation string) (func(context.Context, string) error, error) {
	switch operation {
	case "article", "info", "review", "fix":
		// Все четыре — части одного чата 2 и по отдельности не выполняются: продолжить
		// оборванную беседу браузера нечем. Команда честно прогоняет чат целиком.
		return flow.RunArticle, nil
	case "html":
		return flow.RunHTML, nil
	default:
		return nil, fmt.Errorf("операция %q не поддерживается задачей pprof_1", operation)
	}
}
