package main

import (
	"context"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof1"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof2"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1"
)

// Реестр — единственное место, где перечислены задачи. Обе формы имени обязаны находиться:
// человек пишет дефисную, логи и схема PostgreSQL используют подчёркнутую.
func TestRegistryResolvesPProf2(t *testing.T) {
	for _, name := range []string{pprof2.Command, pprof2.Name} {
		profile, err := lookupTask(name)
		if err != nil {
			t.Fatalf("задача %q не найдена: %v", name, err)
		}
		if profile.Name != pprof2.Name {
			t.Fatalf("задача %q разрешилась в %q", name, profile.Name)
		}
	}
	if _, err := lookupTask("pprof-3"); err == nil {
		t.Fatal("несуществующая задача найдена")
	}
}

// Схемы PostgreSQL у задач обязаны быть разными: изоляция данных держится только на них.
func TestTaskSchemasDoNotCollide(t *testing.T) {
	seen := make(map[string]string)
	for _, profile := range taskRegistry() {
		if previous, found := seen[profile.DBSchema]; found {
			t.Fatalf("задачи %s и %s делят схему %q", previous, profile.Name, profile.DBSchema)
		}
		seen[profile.DBSchema] = profile.Name
	}
	if len(seen) != 3 {
		t.Fatalf("схем %d при трёх задачах: %v", len(seen), seen)
	}
}

// Каталоги артефактов, отчётов и диагностики тоже не делятся: reset одной задачи не должен
// сносить файлы другой.
func TestTaskDirectoriesDoNotCollide(t *testing.T) {
	seen := make(map[string]string)
	for _, profile := range taskRegistry() {
		for _, dir := range []string{profile.InputDir, profile.OutputDir, profile.PromptsDir,
			profile.ImportReportsDir, profile.DiagnosticsDir, profile.TemplatePath} {
			if previous, found := seen[dir]; found {
				t.Fatalf("задачи %s и %s делят путь %q", previous, profile.Name, dir)
			}
			seen[dir] = profile.Name
		}
	}
}

// Набор операций у задач один и тот же: pprof_2 отличается путями, схемой стадий и своими
// колонками, а не составом команд.
func TestPProf2AcceptsTheSameOperations(t *testing.T) {
	for _, operation := range []string{"import", "prepare", "generate", "run", "retry",
		"article", "info", "review", "fix", "html", "result", "errors"} {
		if _, err := parseCommand([]string{"seo-pipeline", pprof2.Command, operation}); err != nil {
			t.Fatalf("операция %q не принята pprof-2: %v", operation, err)
		}
	}
	for _, operation := range []string{"regenerate", "clear", "keywords"} {
		if _, err := parseCommand([]string{"seo-pipeline", pprof2.Command, operation, "5"}); err != nil {
			t.Fatalf("операция %q с ID не принята pprof-2: %v", operation, err)
		}
	}
	if _, err := parseCommand([]string{"seo-pipeline", pprof2.Command, "unknown"}); err == nil {
		t.Fatal("несуществующая операция принята")
	}
}

// stubFlow фиксирует, какой вызов потока сделал раннер. Ничего другого от него здесь не нужно.
type stubFlow struct{ calls []string }

func (f *stubFlow) RunStructure(context.Context, string) error {
	f.calls = append(f.calls, "structure")
	return nil
}
func (f *stubFlow) RunArticle(context.Context, string) error {
	f.calls = append(f.calls, "article")
	return nil
}
func (f *stubFlow) RunHTML(context.Context, string) error {
	f.calls = append(f.calls, "html")
	return nil
}

// Этапы article, review и fix ведут в один чат 2: по отдельности они не выполняются, потому
// что продолжить оборванную браузерную беседу нечем.
func TestTaskFlowStageExecutorRoutesChatTwoStagesTogether(t *testing.T) {
	flow := &stubFlow{}
	var prepared, resulted int
	execute := taskFlowStageExecutor(flow,
		func(context.Context, string) error { prepared++; return nil },
		func(context.Context, string) error { resulted++; return nil })
	ctx := context.Background()
	for _, stage := range []pipelineStage{stagePrepare, stageStructure, stageArticle,
		stageReview, stageFix, stageHTML, stageResult} {
		if err := execute(ctx, stage, "5"); err != nil {
			t.Fatalf("этап %s: %v", stage, err)
		}
	}
	want := []string{"structure", "article", "article", "article", "html"}
	if strings.Join(flow.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("поток вызван как %v, ожидалось %v", flow.calls, want)
	}
	if prepared != 1 || resulted != 1 {
		t.Fatalf("общие этапы выполнены %d и %d раз", prepared, resulted)
	}
	if err := execute(ctx, pipelineStage("нет такого"), "5"); err == nil {
		t.Fatal("неизвестный этап выполнен молча")
	}
}

func TestTaskFlowStageRunnerRejectsUnknownOperation(t *testing.T) {
	flow := &stubFlow{}
	for _, operation := range []string{"article", "info", "review", "fix"} {
		run, err := taskFlowStageRunner(flow, operation)
		if err != nil {
			t.Fatalf("операция %q: %v", operation, err)
		}
		if err := run(context.Background(), "5"); err != nil {
			t.Fatal(err)
		}
	}
	if len(flow.calls) != 4 {
		t.Fatalf("чат 2 запущен %d раз вместо четырёх", len(flow.calls))
	}
	if _, err := taskFlowStageRunner(flow, "structure"); err == nil {
		t.Fatal("structure принята одностадийной командой, хотя она часть полного прогона")
	}
}

// Свой поток есть не у всякой задачи: task_1 идёт общим конвейером, и nil здесь — законный
// ответ, а не сбой.
func TestNewTaskFlowHasNoFlowForTask1(t *testing.T) {
	flow, err := newTaskFlow(task1.Profile(), taskFlowDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if flow != nil {
		t.Fatal("у task_1 появился собственный поток")
	}
	// У задач со своим потоком без роутера собрать его нечем — это ошибка, а не тихий nil.
	for _, profile := range []string{pprof1.Name, pprof2.Name} {
		resolved, lookupErr := lookupTask(profile)
		if lookupErr != nil {
			t.Fatal(lookupErr)
		}
		if _, err := newTaskFlow(resolved, taskFlowDeps{}); err == nil {
			t.Fatalf("поток %s собран без схемы стадий", profile)
		}
	}
}
