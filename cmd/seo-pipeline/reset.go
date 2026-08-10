package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/repository"
)

// confirmationWord — слово, которое нужно ввести, чтобы reset выполнился. Одной буквы для
// необратимой операции мало: y жмётся рефлекторно, слово требует прочитать отчёт.
const confirmationWord = "reset"

// importReportsDir и debugArtifactsDir лежат вне OUTPUT_DIR и захардкожены теми же
// литералами, что в runImport и в клиентах интеграций.
const (
	importReportsDir  = "output/task1/import-reports"
	debugArtifactsDir = "output/task1/debug"
)

// resetRepository — то, что reset требует от хранилища.
type resetRepository interface {
	ResetCounts(ctx context.Context) ([]repository.ResetCount, error)
	Reset(ctx context.Context) error
}

// resetOptions собирает окружение команды: что чистить, куда печатать и откуда читать
// подтверждение.
type resetOptions struct {
	OutputDir   string
	DatabaseURL string
	// AssumeYes — флаг --yes: подтверждение без вопроса, для автоматизации.
	AssumeYes bool
	// Interactive сообщает, что In — терминал. Вычисляется вызывающим, чтобы команда
	// оставалась проверяемой на обычном io.Reader.
	Interactive bool
	In          io.Reader
	Out         io.Writer
}

// resetTarget — каталог, содержимое которого очищает reset.
type resetTarget struct {
	label   string
	path    string
	entries int
}

// runReset приводит task_1 к состоянию «импорта ещё не было»: пустые таблицы, сброшенные
// счётчики идентификаторов, пустые каталоги вывода.
//
// Порядок намеренный: сначала коммит в БД, потом файлы. Сбой на файлах оставляет пустую базу
// и лишние каталоги — это чинится повторным запуском, потому что команда идемпотентна.
// Обратный порядок оставил бы записи о статьях, чьи артефакты уже удалены.
func runReset(ctx context.Context, articleRepository resetRepository, options resetOptions, logger *slog.Logger) error {
	outputDir, err := validateResetOutputDir(options.OutputDir)
	if err != nil {
		return err
	}
	targets := []resetTarget{
		{label: filepath.ToSlash(outputDir) + "/", path: outputDir},
		{label: importReportsDir + "/", path: importReportsDir},
		{label: debugArtifactsDir + "/", path: debugArtifactsDir},
	}
	for index := range targets {
		entries, countErr := countDirectoryEntries(targets[index].path)
		if countErr != nil {
			return countErr
		}
		targets[index].entries = entries
	}

	counts, err := articleRepository.ResetCounts(ctx)
	if err != nil {
		return err
	}
	printResetReport(options.Out, options.DatabaseURL, counts, targets)

	confirmed, err := confirmReset(options)
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(options.Out, "Подтверждение не получено. Ничего не удалено.")
		logger.Info("сброс отменён", "stage", "confirm")
		return nil
	}

	logger.Info("очистка базы данных начата", "stage", "database")
	if err := articleRepository.Reset(ctx); err != nil {
		return err
	}
	logger.Info("очистка базы данных завершена", "stage", "database")

	// Каждый каталог чистится независимо: сбой на одном не должен отменять остальные,
	// иначе повторный reset пришлось бы запускать ради каталога, который и так был бы пуст.
	var failures []error
	for _, target := range targets {
		if clearErr := clearDirectoryContents(target.path); clearErr != nil {
			failures = append(failures, clearErr)
			continue
		}
		logger.Info("каталог очищен", "stage", "files", "path", target.path)
	}
	if err := errors.Join(failures...); err != nil {
		return fmt.Errorf("база данных очищена, файлы удалены не полностью — повторите reset: %w", err)
	}

	fmt.Fprintln(options.Out)
	fmt.Fprintln(options.Out, "Состояние task_1 сброшено: таблицы очищены, счётчики ID сброшены, каталоги вывода пусты.")
	fmt.Fprintln(options.Out, "Дальше: make task-1 import")
	return nil
}

// validateResetOutputDir отвергает OUTPUT_DIR, очистка которого не была бы тем, чего ждёт
// пользователь. Путь приходит из окружения, поэтому опечатка в нём иначе стоила бы чужого
// дерева файлов.
func validateResetOutputDir(outputDir string) (string, error) {
	cleaned := strings.TrimSpace(outputDir)
	if cleaned == "" {
		return "", fmt.Errorf("OUTPUT_DIR пуст: reset не знает, какой каталог очищать")
	}
	cleaned = filepath.Clean(cleaned)

	absolute, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("определить абсолютный путь OUTPUT_DIR %q: %w", outputDir, err)
	}
	if absolute == filepath.Dir(absolute) {
		return "", fmt.Errorf("OUTPUT_DIR указывает на корень файловой системы: %q", outputDir)
	}

	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("определить рабочий каталог: %w", err)
	}
	projectRoot, err := filepath.Abs(workingDirectory)
	if err != nil {
		return "", fmt.Errorf("определить корень проекта: %w", err)
	}
	if absolute == projectRoot {
		return "", fmt.Errorf("OUTPUT_DIR совпадает с корнем проекта: %q", outputDir)
	}
	relative, err := filepath.Rel(projectRoot, absolute)
	if err != nil {
		return "", fmt.Errorf("сопоставить OUTPUT_DIR %q с корнем проекта: %w", outputDir, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("OUTPUT_DIR вне корня проекта: %q", outputDir)
	}
	return cleaned, nil
}

// countDirectoryEntries считает элементы верхнего уровня каталога. Рекурсия не нужна:
// отчёт показывает масштаб, а не инвентаризацию.
func countDirectoryEntries(path string) (int, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("прочитать каталог %s: %w", path, err)
	}
	return len(entries), nil
}

// clearDirectoryContents удаляет содержимое каталога, оставляя сам каталог. Отсутствующий
// каталог — не ошибка: reset идемпотентен и доделывает то, что не закончил предыдущий запуск.
func clearDirectoryContents(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("прочитать каталог %s: %w", path, err)
	}
	var failures []error
	for _, entry := range entries {
		target := filepath.Join(path, entry.Name())
		if removeErr := os.RemoveAll(target); removeErr != nil {
			failures = append(failures, fmt.Errorf("удалить %s: %w", target, removeErr))
		}
	}
	return errors.Join(failures...)
}

// printResetReport показывает фактический масштаб удаления до подтверждения: главная ошибка
// здесь — запустить reset, не зная, что в базе лежат готовые статьи.
func printResetReport(out io.Writer, databaseURL string, counts []repository.ResetCount, targets []resetTarget) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Будет удалено безвозвратно:")
	fmt.Fprintf(out, "  %s\n", describeDatabase(databaseURL))
	for _, count := range counts {
		fmt.Fprintf(out, "    %-28s %6d\n", count.Table, count.Rows)
	}
	for _, target := range targets {
		fmt.Fprintf(out, "  %-30s %6d элементов\n", target.label, target.entries)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Сохраняется: входной Excel, промпты, конфигурация, миграции, схема БД, data/.")
	fmt.Fprintln(out)
}

// describeDatabase называет базу, не показывая учётные данные из DATABASE_URL.
func describeDatabase(databaseURL string) string {
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Host == "" {
		return "база данных из DATABASE_URL"
	}
	name := strings.TrimPrefix(parsed.Path, "/")
	if name == "" {
		return fmt.Sprintf("база данных на %s", parsed.Host)
	}
	return fmt.Sprintf("база данных %s (%s)", name, parsed.Host)
}

// confirmReset получает подтверждение. Без терминала и без --yes команда отказывается
// работать: иначе она молча читала бы пустой поток и выглядела бы зависшей.
func confirmReset(options resetOptions) (bool, error) {
	if options.AssumeYes {
		fmt.Fprintln(options.Out, "Подтверждено флагом --yes.")
		return true, nil
	}
	if !options.Interactive {
		return false, fmt.Errorf("stdin не терминал: запустите reset с флагом --yes")
	}
	fmt.Fprintf(options.Out, "Введите %s для подтверждения: ", confirmationWord)
	answer, err := bufio.NewReader(options.In).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("прочитать подтверждение: %w", err)
	}
	answer = strings.TrimSpace(answer)
	// Пустой ответ на сразу закрытом потоке — это не отказ пользователя, а отсутствие того,
	// кто мог бы ответить: `< /dev/null` проходит проверку на символьное устройство, но
	// терминалом не является. Подсказываем флаг вместо молчаливого выхода.
	if answer == "" && errors.Is(err, io.EOF) {
		return false, fmt.Errorf("подтверждение не прочитано: запустите reset с флагом --yes")
	}
	return answer == confirmationWord, nil
}
