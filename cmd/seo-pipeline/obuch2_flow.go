package main

import (
	"fmt"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/taskflow"
	"github.com/foxylis237/seo-pipeline/internal/tasks/obuch2"
)

// newObuch2Flow собирает поток генерации obuch_2 поверх общего движка.
//
// Зависимости те же, что у pprof_2: обе задачи пишут коммерческие страницы услуг тремя
// чатами, а расходятся площадкой, вёрсткой и источником чисел программы, а не работой с
// базой и файлами.
//
// Каталога услуг и страниц сайта здесь нет, в отличие от obuch_1: перелинковки у страницы
// услуги не бывает ни одной, а у кнопки заявки нет адреса — она открывает форму. Сверять
// нечего и названий программ спрашивать не у кого.
func newObuch2Flow(deps taskFlowDeps) (taskFlow, error) {
	if deps.router == nil {
		return nil, fmt.Errorf("схема стадий obuch_2 не загружена")
	}
	return obuch2.NewFlow(deps.repository, deps.writer, taskflow.NewRouterChats(deps.router),
		deps.router, deps.logger, deps.publisher), nil
}
