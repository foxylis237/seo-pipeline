package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/output"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/repository"
)

// clearConfirmationWord — слово подтверждения очистки одной статьи. Своё, а не общее с reset:
// перепутать команды на необратимой операции дороже, чем продублировать константу.
const clearConfirmationWord = "clear"

// clearRepository — то, что очистка требует от хранилища. Узко и намеренно без методов
// пайплайна: команда не двигает статью по этапам, а только возвращает её к импорту.
type clearRepository interface {
	GetArticleByExternalID(ctx context.Context, externalID string) (article.Article, error)
	ClearArticleCounts(ctx context.Context, articleID int64) ([]repository.ClearCount, error)
	ClearArticleState(ctx context.Context, articleID int64) error
}

// clearArtifactWriter удаляет файлы одной статьи и считает их для отчёта.
type clearArtifactWriter interface {
	CountArticleArtifacts(externalID string) (string, int, error)
	ClearArticleArtifacts(externalID string) ([]string, error)
}

// clearOptions собирает окружение команды: куда печатать и откуда читать подтверждение.
type clearOptions struct {
	// AssumeYes — флаг --yes: подтверждение без вопроса, для автоматизации.
	AssumeYes bool
	// Interactive сообщает, что In — терминал. Вычисляется вызывающим, чтобы команда
	// оставалась проверяемой на обычном io.Reader.
	Interactive bool
	In          io.Reader
	Out         io.Writer
}

// runClear возвращает одну статью к состоянию сразу после импорта.
//
// external_id — это ID из Excel, а не articles.id: команда сама находит статью и печатает оба
// номера, чтобы очистка не ушла в соседнюю строку из-за путаницы между ними.
//
// Сохраняются строка articles с её id и external_id, article_inputs и logs/ статьи. Удаляются
// research, metadata, outputs, история ошибок и все файлы статьи, кроме логов.
//
// Порядок намеренный, тот же, что у reset: сначала коммит в БД, потом файлы. Сбой на файлах
// оставляет чистую запись и лишние файлы — это чинится повторным запуском, потому что команда
// идемпотентна. Обратный порядок оставил бы запись о research, чьи артефакты уже удалены.
func runClear(
	ctx context.Context,
	articleRepository clearRepository,
	artifacts clearArtifactWriter,
	options clearOptions,
	logger *slog.Logger,
	externalID string,
) error {
	selected, err := articleRepository.GetArticleByExternalID(ctx, externalID)
	if err != nil {
		return err
	}

	counts, err := articleRepository.ClearArticleCounts(ctx, selected.ID)
	if err != nil {
		return err
	}
	directory, files, err := artifacts.CountArticleArtifacts(externalID)
	if err != nil {
		return err
	}

	printClearReport(options.Out, selected, counts, directory, files)

	confirmed, err := confirmClear(options)
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(options.Out, "Подтверждение не получено. Ничего не удалено.")
		logger.Info("очистка статьи отменена", "stage", "confirm",
			"article_id", selected.ID, "external_id", selected.ExternalID)
		return nil
	}

	logger.Info("очистка статьи начата", "stage", "database",
		"article_id", selected.ID, "external_id", selected.ExternalID,
		"old_status", selected.Status, "old_current_step", optionalText(selected.CurrentStep))
	if err := articleRepository.ClearArticleState(ctx, selected.ID); err != nil {
		return err
	}
	logger.Info("состояние статьи очищено", "stage", "database", "article_id", selected.ID)

	removed, err := artifacts.ClearArticleArtifacts(externalID)
	logger.Info("файлы статьи удалены", "stage", "files",
		"article_id", selected.ID, "removed_count", len(removed), "removed", removed)
	if err != nil {
		return fmt.Errorf(
			"состояние статьи external_id=%s очищено, файлы удалены не полностью — повторите clear: %w",
			externalID, err)
	}

	fmt.Fprintln(options.Out)
	fmt.Fprintf(options.Out, "Статья external_id=%s очищена: состояние pending, импорт сохранён.\n", externalID)
	fmt.Fprintf(options.Out, "Дальше: make task-1 run %s\n", externalID)
	return nil
}

// printClearReport показывает фактический масштаб удаления до подтверждения. Оба номера
// печатаются рядом: аргумент команды — external_id из Excel, а не внутренний articles.id.
func printClearReport(
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
	kept := output.ClearKeepsDescription()
	if kept == "" {
		fmt.Fprintf(out, "Сохраняется: строка articles с id=%d и external_id=%s, article_inputs.\n",
			selected.ID, selected.ExternalID)
		fmt.Fprintln(out, "На диске от статьи не останется ничего, включая логи прошлых прогонов.")
	} else {
		fmt.Fprintf(out, "Сохраняется: строка articles с id=%d и external_id=%s, article_inputs, %s статьи.\n",
			selected.ID, selected.ExternalID, kept)
	}
	fmt.Fprintln(out)
}

// confirmClear получает подтверждение. Без терминала и без --yes команда отказывается
// работать: иначе она молча читала бы пустой поток и выглядела бы зависшей.
func confirmClear(options clearOptions) (bool, error) {
	if options.AssumeYes {
		fmt.Fprintln(options.Out, "Подтверждено флагом --yes.")
		return true, nil
	}
	if !options.Interactive {
		return false, fmt.Errorf("stdin не терминал: запустите clear с флагом --yes")
	}
	fmt.Fprintf(options.Out, "Введите %s для подтверждения: ", clearConfirmationWord)
	answer, err := bufio.NewReader(options.In).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("прочитать подтверждение: %w", err)
	}
	answer = strings.TrimSpace(answer)
	// Пустой ответ на сразу закрытом потоке — это не отказ пользователя, а отсутствие того,
	// кто мог бы ответить.
	if answer == "" && errors.Is(err, io.EOF) {
		return false, fmt.Errorf("подтверждение не прочитано: запустите clear с флагом --yes")
	}
	return answer == clearConfirmationWord, nil
}
