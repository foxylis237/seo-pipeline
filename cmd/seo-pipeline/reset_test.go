package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/repository"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
	"github.com/foxylis237/seo-pipeline/internal/tasks/pprof1"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1"
)

type fakeResetRepository struct {
	counts     []repository.ResetCount
	resetCalls int
}

func (r *fakeResetRepository) ResetCounts(context.Context) ([]repository.ResetCount, error) {
	return r.counts, nil
}

func (r *fakeResetRepository) Reset(context.Context) error {
	r.resetCalls++
	return nil
}

func discardResetLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// Каталоги reset берутся из профиля task_1: тесты обязаны проверять те же пути, что получит
// команда в бою, а не их копию.
var (
	resetProfile      = mustProfile(task1.Command)
	resetOutputDir    = filepath.FromSlash(resetProfile.OutputDir)
	importReportsDir  = filepath.FromSlash(resetProfile.ImportReportsDir)
	debugArtifactsDir = filepath.FromSlash(resetProfile.DiagnosticsDir)
)

func mustProfile(name string) tasks.Profile {
	profile, err := lookupTask(name)
	if err != nil {
		panic(err)
	}
	return profile
}

// newResetProject builds a temporary project with populated output directories and makes it
// the working directory, потому что reset проверяет пути относительно корня проекта.
func newResetProject(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())

	directories := []string{
		filepath.Join(resetOutputDir, "37-kak-stat-logopedom", "generated"),
		filepath.Join(resetOutputDir, "dry-run"),
		importReportsDir,
		filepath.Join(debugArtifactsDir, "keysso"),
	}
	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("создать каталог %s: %v", directory, err)
		}
	}
	files := []string{
		filepath.Join(resetOutputDir, "37-kak-stat-logopedom", "result.md"),
		filepath.Join(resetOutputDir, "37-kak-stat-logopedom", "generated", "article.md"),
		filepath.Join(resetOutputDir, "dry-run", "report.json"),
		filepath.Join(importReportsDir, "latest.json"),
		filepath.Join(debugArtifactsDir, "keysso", "page.html"),
	}
	for _, file := range files {
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatalf("создать файл %s: %v", file, err)
		}
	}
}

func newResetOptions(out io.Writer, in io.Reader, interactive bool) resetOptions {
	return resetOptions{
		OutputDir:        resetOutputDir,
		ImportReportsDir: importReportsDir,
		DiagnosticsDir:   debugArtifactsDir,
		DatabaseURL:      "postgres://seo:sup3rsecret@localhost:5432/seo?sslmode=disable",
		Interactive:      interactive,
		In:               in,
		Out:              out,
	}
}

func assertResetDirectoriesEmpty(t *testing.T) {
	t.Helper()
	for _, directory := range []string{resetOutputDir, importReportsDir, debugArtifactsDir} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatalf("каталог %s должен остаться на месте: %v", directory, err)
		}
		if len(entries) != 0 {
			t.Fatalf("каталог %s не очищен: осталось %d элементов", directory, len(entries))
		}
	}
}

func TestRunResetClearsContentsAndKeepsDirectories(t *testing.T) {
	newResetProject(t)
	articleRepository := &fakeResetRepository{counts: []repository.ResetCount{{Table: "articles", Rows: 24}}}
	out := &strings.Builder{}

	options := newResetOptions(out, strings.NewReader("reset\n"), true)
	if err := runReset(context.Background(), articleRepository, options, discardResetLogger()); err != nil {
		t.Fatalf("reset завершился ошибкой: %v", err)
	}

	if articleRepository.resetCalls != 1 {
		t.Fatalf("Reset вызван %d раз, ожидался один", articleRepository.resetCalls)
	}
	assertResetDirectoriesEmpty(t)
}

func TestRunResetIsIdempotent(t *testing.T) {
	newResetProject(t)
	articleRepository := &fakeResetRepository{}

	for attempt := 1; attempt <= 2; attempt++ {
		options := newResetOptions(&strings.Builder{}, strings.NewReader("reset\n"), true)
		if err := runReset(context.Background(), articleRepository, options, discardResetLogger()); err != nil {
			t.Fatalf("попытка %d завершилась ошибкой: %v", attempt, err)
		}
	}
	assertResetDirectoriesEmpty(t)
}

// Отсутствующие каталоги — обычное состояние свежего клона и состояние после сбоя на
// половине корней. Ни то, ни другое не должно останавливать reset.
func TestRunResetSucceedsWhenDirectoriesAreMissing(t *testing.T) {
	t.Chdir(t.TempDir())
	articleRepository := &fakeResetRepository{}

	options := newResetOptions(&strings.Builder{}, strings.NewReader("reset\n"), true)
	if err := runReset(context.Background(), articleRepository, options, discardResetLogger()); err != nil {
		t.Fatalf("reset на пустом проекте завершился ошибкой: %v", err)
	}
	if articleRepository.resetCalls != 1 {
		t.Fatalf("Reset вызван %d раз, ожидался один", articleRepository.resetCalls)
	}
}

func TestRunResetReportShowsTopLevelScaleWithoutCredentials(t *testing.T) {
	newResetProject(t)
	articleRepository := &fakeResetRepository{counts: []repository.ResetCount{
		{Table: "articles", Rows: 24},
		{Table: "article_errors", Rows: 7},
	}}
	out := &strings.Builder{}

	options := newResetOptions(out, strings.NewReader("нет\n"), true)
	if err := runReset(context.Background(), articleRepository, options, discardResetLogger()); err != nil {
		t.Fatalf("reset завершился ошибкой: %v", err)
	}

	report := out.String()
	for _, fragment := range []string{"articles", "24", "article_errors", "7", "seo (localhost:5432)"} {
		if !strings.Contains(report, fragment) {
			t.Fatalf("отчёт не содержит %q:\n%s", fragment, report)
		}
	}
	// Каталог статей содержит два элемента верхнего уровня и четыре файла в глубине:
	// в отчёте должно быть 2, иначе счёт стал рекурсивным.
	if !strings.Contains(report, "2 элементов") {
		t.Fatalf("отчёт считает не элементы верхнего уровня:\n%s", report)
	}
	if strings.Contains(report, "sup3rsecret") {
		t.Fatalf("отчёт раскрывает пароль из DATABASE_URL:\n%s", report)
	}
}

func TestRunResetWithoutConfirmationChangesNothing(t *testing.T) {
	newResetProject(t)
	articleRepository := &fakeResetRepository{}
	out := &strings.Builder{}

	options := newResetOptions(out, strings.NewReader("да\n"), true)
	if err := runReset(context.Background(), articleRepository, options, discardResetLogger()); err != nil {
		t.Fatalf("отказ не должен быть ошибкой: %v", err)
	}

	if articleRepository.resetCalls != 0 {
		t.Fatalf("база очищена без подтверждения: %d вызовов", articleRepository.resetCalls)
	}
	if _, err := os.Stat(filepath.Join(importReportsDir, "latest.json")); err != nil {
		t.Fatalf("файлы удалены без подтверждения: %v", err)
	}
	if !strings.Contains(out.String(), "Ничего не удалено") {
		t.Fatalf("отказ не назван явно:\n%s", out.String())
	}
}

func TestRunResetRequiresYesFlagWithoutTerminal(t *testing.T) {
	newResetProject(t)
	articleRepository := &fakeResetRepository{}

	options := newResetOptions(&strings.Builder{}, strings.NewReader("reset\n"), false)
	err := runReset(context.Background(), articleRepository, options, discardResetLogger())
	if err == nil {
		t.Fatal("reset без терминала и без --yes должен отказать")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("ошибка не подсказывает флаг: %v", err)
	}
	if articleRepository.resetCalls != 0 {
		t.Fatalf("база очищена вопреки отказу: %d вызовов", articleRepository.resetCalls)
	}
}

// `< /dev/null` выглядит символьным устройством, поэтому доходит до чтения ответа. Пустой
// поток должен подсказать флаг, а не выглядеть отказом пользователя.
func TestRunResetRequiresYesFlagOnEmptyStdin(t *testing.T) {
	newResetProject(t)
	articleRepository := &fakeResetRepository{}

	options := newResetOptions(&strings.Builder{}, strings.NewReader(""), true)
	err := runReset(context.Background(), articleRepository, options, discardResetLogger())
	if err == nil {
		t.Fatal("пустой stdin должен приводить к отказу с подсказкой")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("ошибка не подсказывает флаг: %v", err)
	}
	if articleRepository.resetCalls != 0 {
		t.Fatalf("база очищена при пустом stdin: %d вызовов", articleRepository.resetCalls)
	}
}

func TestRunResetAcceptsYesFlagWithoutTerminal(t *testing.T) {
	newResetProject(t)
	articleRepository := &fakeResetRepository{}

	options := newResetOptions(&strings.Builder{}, strings.NewReader(""), false)
	options.AssumeYes = true
	if err := runReset(context.Background(), articleRepository, options, discardResetLogger()); err != nil {
		t.Fatalf("reset с --yes завершился ошибкой: %v", err)
	}
	if articleRepository.resetCalls != 1 {
		t.Fatalf("Reset вызван %d раз, ожидался один", articleRepository.resetCalls)
	}
	assertResetDirectoriesEmpty(t)
}

func TestRunResetRejectsUnsafeOutputDir(t *testing.T) {
	cases := map[string]string{
		"пустой":          "",
		"корень проекта":  ".",
		"вне проекта":     filepath.Join("..", "elsewhere"),
		"корень файловой": string(filepath.Separator),
	}
	for name, outputDir := range cases {
		t.Run(name, func(t *testing.T) {
			newResetProject(t)
			articleRepository := &fakeResetRepository{}

			options := newResetOptions(&strings.Builder{}, strings.NewReader("reset\n"), true)
			options.OutputDir = outputDir
			if err := runReset(context.Background(), articleRepository, options, discardResetLogger()); err == nil {
				t.Fatalf("OUTPUT_DIR %q принят", outputDir)
			}
			if articleRepository.resetCalls != 0 {
				t.Fatalf("база очищена при отвергнутом OUTPUT_DIR: %d вызовов", articleRepository.resetCalls)
			}
			if _, err := os.Stat(filepath.Join(importReportsDir, "latest.json")); err != nil {
				t.Fatalf("файлы удалены при отвергнутом OUTPUT_DIR: %v", err)
			}
		})
	}
}

func TestParseCommandReset(t *testing.T) {
	cases := []struct {
		name           string
		args           []string
		assumeYes      bool
		wantExternalID string
		wantError      bool
	}{
		{name: "без флага", args: []string{"seo-pipeline", "task-1", "reset"}},
		{name: "с флагом", args: []string{"seo-pipeline", "task-1", "reset", "--yes"}, assumeYes: true},
		{
			name: "позиционный аргумент", args: []string{"seo-pipeline", "task-1", "reset", "37"},
			wantExternalID: "37",
		},
		{
			name: "аргумент с флагом", args: []string{"seo-pipeline", "task-1", "reset", "37", "--yes"},
			assumeYes: true, wantExternalID: "37",
		},
		{name: "нечисловой аргумент", args: []string{"seo-pipeline", "task-1", "reset", "abc"}, wantError: true},
		{name: "два аргумента", args: []string{"seo-pipeline", "task-1", "reset", "37", "38"}, wantError: true},
		{name: "флаг дважды", args: []string{"seo-pipeline", "task-1", "reset", "--yes", "--yes"}, wantError: true},
		{name: "флаг у чужой операции", args: []string{"seo-pipeline", "task-1", "run", "--yes"}, wantError: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			command, err := parseCommand(testCase.args)
			if testCase.wantError {
				if err == nil {
					t.Fatalf("ожидалась ошибка, получено %+v", command)
				}
				return
			}
			if err != nil {
				t.Fatalf("разбор завершился ошибкой: %v", err)
			}
			if command.Name != "reset" {
				t.Fatalf("операция %q, ожидалась reset", command.Name)
			}
			if command.AssumeYes != testCase.assumeYes {
				t.Fatalf("AssumeYes=%v, ожидалось %v", command.AssumeYes, testCase.assumeYes)
			}
			if command.ExternalID != testCase.wantExternalID {
				t.Fatalf("ExternalID=%q, ожидалось %q", command.ExternalID, testCase.wantExternalID)
			}
		})
	}
}

// Reset одной задачи не имеет права трогать данные другой: каталоги приходят из профиля, и
// эта проверка ловит возврат к общим захардкоженным путям.
func TestRunResetTouchesOnlyItsOwnTask(t *testing.T) {
	tests := []struct {
		name  string
		reset string
		other string
	}{
		{name: "task-1 не трогает pprof_1", reset: task1.Command, other: pprof1.Command},
		{name: "pprof-1 не трогает task_1", reset: pprof1.Command, other: task1.Command},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			resetProfile := mustProfile(test.reset)
			otherProfile := mustProfile(test.other)

			otherFiles := []string{
				filepath.Join(filepath.FromSlash(otherProfile.OutputDir), "37-tema", "result.md"),
				filepath.Join(filepath.FromSlash(otherProfile.ImportReportsDir), "latest.json"),
				filepath.Join(filepath.FromSlash(otherProfile.DiagnosticsDir), "keysso", "page.html"),
			}
			resetFiles := []string{
				filepath.Join(filepath.FromSlash(resetProfile.OutputDir), "37-tema", "result.md"),
				filepath.Join(filepath.FromSlash(resetProfile.ImportReportsDir), "latest.json"),
				filepath.Join(filepath.FromSlash(resetProfile.DiagnosticsDir), "keysso", "page.html"),
			}
			for _, file := range append(append([]string{}, otherFiles...), resetFiles...) {
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			options := resetOptions{
				OutputDir:        filepath.FromSlash(resetProfile.OutputDir),
				ImportReportsDir: filepath.FromSlash(resetProfile.ImportReportsDir),
				DiagnosticsDir:   filepath.FromSlash(resetProfile.DiagnosticsDir),
				DatabaseURL:      "postgres://seo:sup3rsecret@localhost:5432/seo?sslmode=disable",
				AssumeYes:        true,
				Out:              &strings.Builder{},
			}
			if err := runReset(context.Background(), &fakeResetRepository{}, options, discardResetLogger()); err != nil {
				t.Fatalf("reset %s завершился ошибкой: %v", test.reset, err)
			}

			for _, file := range resetFiles {
				if _, err := os.Stat(file); !os.IsNotExist(err) {
					t.Fatalf("reset %s не удалил свой файл %s: %v", test.reset, file, err)
				}
			}
			for _, file := range otherFiles {
				if _, err := os.Stat(file); err != nil {
					t.Fatalf("reset %s удалил чужой файл %s: %v", test.reset, file, err)
				}
			}
		})
	}
}
