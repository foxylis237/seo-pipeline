package main

import (
	"fmt"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/generation"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof1"
)

// newPProf1Flow собирает поток генерации pprof_1 поверх общего движка.
//
// Репозиторий, writer и роутер — те же, что у task_1: pprof_1 отличается порядком стадий и
// промптами, а не работой с базой и файлами.
func newPProf1Flow(deps taskFlowDeps) (taskFlow, error) {
	if deps.router == nil {
		return nil, fmt.Errorf("схема стадий pprof_1 не загружена")
	}
	return pprof1.NewFlow(deps.repository, deps.writer, pprof1.NewRouterChats(deps.router), deps.router,
		deps.logger, pprof1PromptPublisher{publisher: deps.publisher}), nil
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
