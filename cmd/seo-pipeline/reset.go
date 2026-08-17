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

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/repository"
)

// confirmationWord — слово, которое нужно ввести, чтобы reset выполнился. Одной буквы для
// необратимой операции мало: y жмётся рефлекторно, слово требует прочитать отчёт.
const confirmationWord = "reset"

// Отчёты импорта и диагностика лежат вне OUTPUT_DIR, поэтому их каталоги приходят в
// resetOptions отдельно. Оба — из профиля задачи: reset обязан чистить только свою задачу.

// resetRepository — то, что reset требует от хранилища.
type resetRepository interface {
	ResetCounts(ctx context.Context) ([]repository.ResetCount, error)
	Reset(ctx context.Context) error
}

// resetOptions собирает окружение команды: что чистить, куда печатать и откуда читать
// подтверждение.
type resetOptions struct {
	// TaskName и TaskCommand попадают в отчёт: команда обязана называть ту задачу, которую
	// сбросила, иначе pprof-1 reset отчитается о сбросе task_1.
	TaskName    string
	TaskCommand string
	OutputDir   string
	// ImportReportsDir и DiagnosticsDir принадлежат этой же задаче. Общих каталогов здесь
	// быть не должно: reset одной задачи не имеет права стирать данные другой.
	ImportReportsDir string
	DiagnosticsDir   string
	DatabaseURL      string
	// AssumeYes — флаг --yes: подтверждение без вопроса, для автоматизации.
	AssumeYes bool
	// Interactive сообщает, что In — терминал. Вычисляется вызывающим, чтобы команда
	// оставалась проверяемой на обычном io.Reader.
	Interactive bool
	In          io.Reader
	Out         io.Writer
}

// resetArticleRepository — то, что reset одной статьи требует от хранилища. Узко и намеренно
// без методов пайплайна: команда не двигает статью по этапам, а возвращает её к импорту.
type resetArticleRepository interface {
	GetArticleByExternalID(ctx context.Context, externalID string) (article.Article, error)
	ResetArticleCounts(ctx context.Context, articleID int64) ([]repository.ClearCount, error)
	ResetArticleState(ctx context.Context, articleID int64) error
}

// resetArticleOptions собирает окружение команды: куда печатать и откуда читать подтверждение.
type resetArticleOptions struct {
	// TaskCommand подставляется в подсказку следующего шага: у pprof-1 она обязана называть
	// pprof-1, а не task-1.
	TaskCommand string
	// AssumeYes — флаг --yes: подтверждение без вопроса, для автоматизации.
	AssumeYes bool
	// Interactive сообщает, что In — терминал. Вычисляется вызывающим, чтобы команда
	// оставалась проверяемой на обычном io.Reader.
	Interactive bool
	In          io.Reader
	Out         io.Writer
}

// runResetArticle возвращает одну статью к состоянию «импорта ещё не было», сохраняя её место
// в базе: строка articles остаётся, articles.id и external_id не меняются.
//
// external_id — это ID из Excel, а не articles.id: команда сама находит статью и печатает оба
// номера, чтобы сброс не ушёл в соседнюю строку из-за путаницы между ними.
//
// От clear отличается ровно тем, что стирает ещё и article_inputs: после clear статью можно
// сразу запускать, после reset ей нужен повторный `import`.
//
// Порядок намеренный, тот же, что у сброса задачи: сначала коммит в БД, потом файлы. Сбой на
// файлах оставляет чистую запись и лишние файлы — это чинится повторным запуском, потому что
// команда идемпотентна. Обратный порядок оставил бы запись о research, чьи артефакты удалены.
func runResetArticle(
	ctx context.Context,
	articleRepository resetArticleRepository,
	artifacts clearArtifactWriter,
	options resetArticleOptions,
	logger *slog.Logger,
	externalID string,
) error {
	selected, err := articleRepository.GetArticleByExternalID(ctx, externalID)
	if err != nil {
		return err
	}

	counts, err := articleRepository.ResetArticleCounts(ctx, selected.ID)
	if err != nil {
		return err
	}
	directory, files, err := artifacts.CountArticleArtifacts(externalID)
	if err != nil {
		return err
	}

	printResetArticleReport(options.Out, selected, counts, directory, files)

	confirmed, err := confirmDestructive(
		confirmationWord, options.AssumeYes, options.Interactive, options.In, options.Out)
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(options.Out, "Подтверждение не получено. Ничего не удалено.")
		logger.Info("сброс статьи отменён", "stage", "confirm",
			"article_id", selected.ID, "external_id", selected.ExternalID)
		return nil
	}

	logger.Info("сброс статьи начат", "stage", "database",
		"article_id", selected.ID, "external_id", selected.ExternalID,
		"old_status", selected.Status, "old_current_step", optionalText(selected.CurrentStep))
	if err := articleRepository.ResetArticleState(ctx, selected.ID); err != nil {
		return err
	}
	logger.Info("состояние статьи сброшено", "stage", "database", "article_id", selected.ID)

	removed, err := artifacts.ClearArticleArtifacts(externalID)
	logger.Info("файлы статьи удалены", "stage", "files",
		"article_id", selected.ID, "removed_count", len(removed), "removed", removed)
	if err != nil {
		return fmt.Errorf(
			"состояние статьи external_id=%s сброшено, файлы удалены не полностью — повторите reset: %w",
			externalID, err)
	}

	fmt.Fprintln(options.Out)
	fmt.Fprintf(options.Out, "Статья external_id=%s сброшена: состояние pending, входные данные удалены.\n", externalID)
	fmt.Fprintf(options.Out, "Дальше: make %s import\n", options.TaskCommand)
	return nil
}

// printResetArticleReport показывает фактический масштаб удаления до подтверждения. Оба номера
// печатаются рядом: аргумент команды — external_id из Excel, а не внутренний articles.id.
func printResetArticleReport(
	out io.Writer,
	selected article.Article,
	counts []repository.ClearCount,
	directory string,
	files int,
) {
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Статья external_id=%s (articles.id=%d) %q\n", selected.ExternalID, selected.ID, selected.Title)
	fmt.Fprintf(out, "  статус %s, этап %s\n", selected.Status, optionalText(selected.CurrentStep))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Будет удалено безвозвратно:")
	for _, count := range counts {
		fmt.Fprintf(out, "    %-28s %6d\n", count.Table, count.Rows)
	}
	if directory == "" {
		fmt.Fprintf(out, "    %-28s %6s\n", "каталог статьи", "нет")
	} else {
		fmt.Fprintf(out, "    %-28s %6d элементов\n", directory+"/", files)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Сохраняется: строка articles с id=%d и external_id=%s.\n",
		selected.ID, selected.ExternalID)
	fmt.Fprintln(out, "На диске от статьи не останется ничего, включая логи прошлых прогонов.")
	fmt.Fprintln(out)
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
		{label: options.ImportReportsDir + "/", path: options.ImportReportsDir},
		{label: options.DiagnosticsDir + "/", path: options.DiagnosticsDir},
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
	fmt.Fprintf(options.Out, "Состояние %s сброшено: таблицы очищены, счётчики ID сброшены, каталоги вывода пусты.\n", options.TaskName)
	fmt.Fprintf(options.Out, "Дальше: make %s import\n", options.TaskCommand)
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

// confirmReset получает подтверждение сброса задачи.
func confirmReset(options resetOptions) (bool, error) {
	return confirmDestructive(confirmationWord, options.AssumeYes, options.Interactive, options.In, options.Out)
}

// confirmDestructive получает подтверждение необратимой команды. Слово подтверждения совпадает
// с именем команды, поэтому оно же называет команду в подсказках.
//
// Без терминала и без --yes команда отказывается работать: иначе она молча читала бы пустой
// поток и выглядела бы зависшей.
func confirmDestructive(word string, assumeYes, interactive bool, in io.Reader, out io.Writer) (bool, error) {
	if assumeYes {
		fmt.Fprintln(out, "Подтверждено флагом --yes.")
		return true, nil
	}
	if !interactive {
		return false, fmt.Errorf("stdin не терминал: запустите %s с флагом --yes", word)
	}
	fmt.Fprintf(out, "Введите %s для подтверждения: ", word)
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("прочитать подтверждение: %w", err)
	}
	answer = strings.TrimSpace(answer)
	// Пустой ответ на сразу закрытом потоке — это не отказ пользователя, а отсутствие того,
	// кто мог бы ответить: `< /dev/null` проходит проверку на символьное устройство, но
	// терминалом не является. Подсказываем флаг вместо молчаливого выхода.
	if answer == "" && errors.Is(err, io.EOF) {
		return false, fmt.Errorf("подтверждение не прочитано: запустите %s с флагом --yes", word)
	}
	return answer == word, nil
}
