package main

import (
	"fmt"

	"github.com/foxylis237/seo-pipeline/internal/integrations/sitepage"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pproftemplate1"
)

// newPProfTemplate1Flow собирает поток генерации pprof_template_1 поверх общего движка.
//
// Зависимости те же, что у pprof_1: задача отличается эталоном, промптами и границами чатов,
// а не работой с базой, файлами и сайтом. Таймаут страницы сайта тоже общий — запросов на
// статью столько же, сколько ссылок перелинковки.
func newPProfTemplate1Flow(deps taskFlowDeps) (taskFlow, error) {
	if deps.router == nil {
		return nil, fmt.Errorf("схема стадий pprof_template_1 не загружена")
	}
	return pproftemplate1.NewFlow(deps.repository, deps.writer, taskflow.NewRouterChats(deps.router),
		deps.router, deps.logger, deps.publisher, sitepage.New(linkNameTimeout)), nil
}
