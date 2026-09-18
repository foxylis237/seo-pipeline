package main

import (
	"fmt"

	"github.com/foxylis237/seo-pipeline/internal/integrations/sitepage"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
	"github.com/foxylis237/seo-pipeline/internal/tasks/obuch1"
)

// newObuch1Flow собирает поток генерации obuch_1 поверх общего движка.
//
// Зависимости те же, что у соседних задач: обе площадки пишут статьи блога тремя чатами, а
// расходятся вёрсткой и карточкой призыва под статьёй, а не работой с базой, файлами и сайтом.
// Таймаут страницы сайта тоже общий — запросов на статью столько же, сколько ссылок
// перелинковки.
func newObuch1Flow(deps taskFlowDeps) (taskFlow, error) {
	if deps.router == nil {
		return nil, fmt.Errorf("схема стадий obuch_1 не загружена")
	}
	return obuch1.NewFlow(deps.repository, deps.writer, taskflow.NewRouterChats(deps.router),
		deps.router, deps.logger, deps.publisher, sitepage.New(linkNameTimeout)), nil
}
