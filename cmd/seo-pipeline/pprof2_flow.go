package main

import (
	"fmt"

	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof2"
)

// newPProf2Flow собирает поток генерации pprof_2 поверх общего движка.
//
// Репозиторий, writer и роутер — те же, что у остальных задач: pprof_2 отличается порядком
// стадий, промптами и своими колонками article_inputs, а не работой с базой и файлами.
//
// Переходника к очереди публикации здесь нет: поток объявил задание готовым типом движка, и
// googlePublisher удовлетворяет его контракту напрямую. Очередь при этом одна на процесс — за
// persistent-профилем Chromium держится flock, — а папку Drive задаёт профиль, поэтому
// документы задач лежат врозь.
func newPProf2Flow(deps taskFlowDeps) (taskFlow, error) {
	if deps.router == nil {
		return nil, fmt.Errorf("схема стадий pprof_2 не загружена")
	}
	return pprof2.NewFlow(deps.repository, deps.writer, pprof2.NewRouterChats(deps.router),
		deps.router, deps.logger, deps.publisher), nil
}
