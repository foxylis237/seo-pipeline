package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof1"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		args     []string
		want     taskCommand
		errorHas string
	}{
		{[]string{"seo-pipeline", "task-1", "import"}, taskCommand{Name: "import"}, ""},
		{[]string{"seo-pipeline", "task-1", "errors"}, taskCommand{Name: "errors"}, ""},
		{[]string{"seo-pipeline", "task-1", "errors", "124"}, taskCommand{Name: "errors", ExternalID: "124"}, ""},
		{[]string{"seo-pipeline", "task-1", "retry", "57"}, taskCommand{Name: "retry", ExternalID: "57"}, ""},
		{[]string{"seo-pipeline", "task-1", "retry"}, taskCommand{Name: "retry"}, ""},
		{[]string{"seo-pipeline", "task-1", "errors", "0"}, taskCommand{}, "positive integer"},
		{[]string{"seo-pipeline", "task_1", "import", "10"}, taskCommand{Name: "import", ImportLimit: 10}, ""},
		{[]string{"seo-pipeline", "task-1", "import", "0"}, taskCommand{}, "positive integer"},
		{[]string{"seo-pipeline", "task-1", "import", "-1"}, taskCommand{}, "positive integer"},
		{[]string{"seo-pipeline", "task-1", "import", "wrong"}, taskCommand{}, "positive integer"},
		{[]string{"seo-pipeline", "task-1", "prepare", "37"}, taskCommand{Name: "prepare", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "task-1", "prepare"}, taskCommand{Name: "prepare"}, ""},
		{[]string{"seo-pipeline", "task-1", "generate", "37"}, taskCommand{Name: "generate", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "task-1", "generate"}, taskCommand{Name: "generate"}, ""},
		{[]string{"seo-pipeline", "task-1", "deepseek-login"}, taskCommand{Name: "deepseek-login"}, ""},
		{[]string{"seo-pipeline", "task-1", "deepseek-login", "37"}, taskCommand{}, "usage: seo-pipeline task-1 deepseek-login"},
		{[]string{"seo-pipeline", "task-1", "article", "37"}, taskCommand{Name: "article", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "task-1", "article"}, taskCommand{Name: "article"}, ""},
		{[]string{"seo-pipeline", "task-1", "info", "37"}, taskCommand{Name: "info", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "task-1", "info"}, taskCommand{Name: "info"}, ""},
		{[]string{"seo-pipeline", "task-1", "run", "37"}, taskCommand{Name: "run", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "task-1", "run"}, taskCommand{Name: "run"}, ""},
		{[]string{"seo-pipeline", "task-1", "run", "--dry-run"}, taskCommand{Name: "run", DryRun: true}, ""},
		{[]string{"seo-pipeline", "--dry-run", "task_1", "run"}, taskCommand{Name: "run", DryRun: true}, ""},
		{[]string{"seo-pipeline", "task_1", "review", "37"}, taskCommand{Name: "review", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "task_1", "review"}, taskCommand{Name: "review"}, ""},
		{[]string{"seo-pipeline", "task_1", "fix", "37"}, taskCommand{Name: "fix", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "task_1", "fix"}, taskCommand{Name: "fix"}, ""},
		{[]string{"seo-pipeline", "task_1", "html", "37"}, taskCommand{Name: "html", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "task_1", "html"}, taskCommand{Name: "html"}, ""},
		{[]string{"seo-pipeline", "task_1", "result"}, taskCommand{Name: "result"}, ""},
		{[]string{"seo-pipeline", "task-1", "demo-generate"}, taskCommand{Name: "demo-generate"}, ""},
		{[]string{"seo-pipeline", "task_1"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "task_1", "unknown"}, taskCommand{}, `unknown task-1 operation "unknown"`},
		{[]string{"seo-pipeline", "task_1", "generate", "not-a-number"}, taskCommand{}, "positive integer"},
		{[]string{"seo-pipeline", "article", "37"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "info", "37"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "review", "37"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "fix", "37"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "html", "37"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "result", "37"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "demo-generate", "37"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "task-1", "result", "0"}, taskCommand{}, "positive integer"},
		{[]string{"seo-pipeline", "generate", "37"}, taskCommand{}, "available task-1 operations"},
		{[]string{"seo-pipeline", "task-1", "generate", "37", "--dry-run"}, taskCommand{}, "supported only"},
		{[]string{"seo-pipeline", "task-1", "run", "37", "--dry-run"}, taskCommand{}, "supported only"},
		{[]string{"seo-pipeline", "task-1", "run", "--dry-run", "--dry-run"}, taskCommand{}, "only once"},

		// pprof_1 — те же операции в своём пространстве имён.
		{[]string{"seo-pipeline", "pprof-1", "import"}, taskCommand{Profile: mustProfile(pprof1.Command), Name: "import"}, ""},
		{[]string{"seo-pipeline", "pprof-1", "run", "37"}, taskCommand{Profile: mustProfile(pprof1.Command), Name: "run", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "pprof_1", "prepare"}, taskCommand{Profile: mustProfile(pprof1.Command), Name: "prepare"}, ""},
		{[]string{"seo-pipeline", "pprof-1", "clear", "37"}, taskCommand{Profile: mustProfile(pprof1.Command), Name: "clear", ExternalID: "37"}, ""},
		{[]string{"seo-pipeline", "pprof-1", "reset"}, taskCommand{Profile: mustProfile(pprof1.Command), Name: "reset"}, ""},
		{[]string{"seo-pipeline", "pprof-1", "unknown"}, taskCommand{}, `unknown pprof-1 operation "unknown"`},
		{[]string{"seo-pipeline", "pprof-1", "clear"}, taskCommand{}, "usage: seo-pipeline pprof-1 clear <external_id>"},
		{[]string{"seo-pipeline", "pprof-1"}, taskCommand{}, "available pprof-1 operations"},
		{[]string{"seo-pipeline", "unknown-task", "run"}, taskCommand{}, `unknown task "unknown-task"`},

		// Глобальный вход: вне пространства задач, профиль у команды пуст.
		{[]string{"seo-pipeline", "login", "deepseek"}, taskCommand{Name: "login", Service: "deepseek"}, ""},
		{[]string{"seo-pipeline", "login", "google"}, taskCommand{Name: "login", Service: "google"}, ""},
		{[]string{"seo-pipeline", "login", "keysso"}, taskCommand{}, "входит автоматически"},
		{[]string{"seo-pipeline", "login", "arsenkin"}, taskCommand{}, "входит автоматически"},
		{[]string{"seo-pipeline", "login", "unknown"}, taskCommand{}, "unknown login service"},
		{[]string{"seo-pipeline", "login"}, taskCommand{}, "usage: seo-pipeline login"},
	}
	for _, test := range tests {
		got, err := parseCommand(test.args)
		if test.errorHas != "" {
			if err == nil || !strings.Contains(err.Error(), test.errorHas) {
				t.Fatalf("parseCommand(%v) error = %v, want containing %q", test.args, err, test.errorHas)
			}
			continue
		}
		// Профиль task_1 подставляется в ожидание, а не переписывается в каждую строку:
		// таблица про разбор аргументов, а не про содержимое профилей.
		want := test.want
		if want.Name != loginCommandName && want.Profile.Name == "" {
			want.Profile = mustProfile(task1.Command)
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("parseCommand(%v) = %+v, %v; want %+v", test.args, got, err, want)
		}
	}
}
