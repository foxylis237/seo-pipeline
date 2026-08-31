package main

import (
	"fmt"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/integrations/sitepage"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof1"
)

// linkNameTimeout — сколько ждать страницу сайта, с которой снимается название программы.
// Запросов на статью столько же, сколько ссылок перелинковки: четыре-пять к своему же сайту.
const linkNameTimeout = 15 * time.Second

// newPProf1Flow собирает поток генерации pprof_1 поверх общего движка.
//
// Репозиторий, writer и роутер — те же, что у task_1: pprof_1 отличается порядком стадий и
// промптами, а не работой с базой и файлами.
//
// Названия программ для перелинковки снимаются с самих страниц сайта (sitepage): в книге
// импорта у ссылки есть только адрес, а анкор обязан совпадать с названием на странице.
//
// Переходника к очереди публикации здесь нет: поток объявил задание готовым типом движка, и
// googlePublisher удовлетворяет его контракту напрямую. Очередь при этом одна на процесс — за
// persistent-профилем Chromium держится flock, — а папку Drive задаёт профиль, поэтому
// документы задач лежат врозь.
func newPProf1Flow(deps taskFlowDeps) (taskFlow, error) {
	if deps.router == nil {
		return nil, fmt.Errorf("схема стадий pprof_1 не загружена")
	}
	return pprof1.NewFlow(deps.repository, deps.writer, taskflow.NewRouterChats(deps.router),
		deps.router, deps.logger, deps.publisher, sitepage.New(linkNameTimeout)), nil
}
