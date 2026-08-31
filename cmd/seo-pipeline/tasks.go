package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/tasks"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof1"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof2"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pproffix1"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pproffix2"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pproffix3"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pproffix4"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pproffix5"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1"
)

// taskRegistry — единственное место, где перечислены задачи проекта.
//
// Реестр живёт в composition root, а не в internal/tasks: иначе пакет с типом профиля
// импортировал бы пакеты задач, а те — его, и получился бы цикл. Следующая задача добавляется
// одной строкой здесь плюс своим пакетом конфигурации и каталогами на диске.
func taskRegistry() []tasks.Profile {
	return []tasks.Profile{
		task1.Profile(), pprof1.Profile(), pprof2.Profile(),
		pproffix1.Profile(), pproffix2.Profile(), pproffix3.Profile(), pproffix4.Profile(),
		pproffix5.Profile(),
	}
}

// lookupTask находит профиль по имени задачи в любой из двух форм.
func lookupTask(name string) (tasks.Profile, error) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	for _, profile := range taskRegistry() {
		if normalized == profile.Command || normalized == profile.Name {
			return profile, nil
		}
	}
	return tasks.Profile{}, fmt.Errorf("unknown task %q; available tasks: %s", name, strings.Join(taskCommands(), ", "))
}

// taskCommands перечисляет дефисные имена задач для сообщений об ошибках.
func taskCommands() []string {
	commands := make([]string, 0, len(taskRegistry()))
	for _, profile := range taskRegistry() {
		commands = append(commands, profile.Command)
	}
	sort.Strings(commands)
	return commands
}
