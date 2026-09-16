package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/pagebatch"
	"github.com/foxylis237/seo-pipeline/internal/tasks"
)

// Общая обвязка задач, которые работают по списку уже опубликованных страниц: правки
// (internal/pipeline/articlefix) и аудита (internal/pipeline/articleaudit).
//
// Потоки у них разные и сводить их нельзя — одна пишет в живой блог, другая не пишет туда
// ничего, — но сброс задачи и прогон пачки с предохранителем у них совпадают до строчки.
// Различаются только слова, которыми это называют человеку, и они вынесены в pageBatchWords.

// pageBatchWords — чем задача называет свою единицу работы.
//
// Слова, а не форматные строки целиком: сообщения обязаны остаться теми же, что были у каждой
// задачи, иначе общий код молча переписал бы её вывод.
type pageBatchWords struct {
	// ItemsGenitive — «статей» / «страниц»: столько-то в схеме, столько-то не тронуто.
	ItemsGenitive string
	// ItemGenitive — «статьи» / «страницы»: состояний у неё два.
	ItemGenitive string
	// States — «переписана или нет» / «проверена или нет».
	States string
	// Done — «переписана» / «проверена»: строка об успехе одной единицы.
	Done string
	// DoneMany и FailedMany — «переписано» / «не переписано» в итоговом счёте.
	DoneMany   string
	FailedMany string
	// FailedOne — «статья не переписана» / «страница не проверена»: сообщение в лог.
	FailedOne string
	// DoneCounter — как счётчик успехов называется в логе предохранителя.
	DoneCounter string
	// ResetConsequence — чем сброс обернётся; печатается перед подтверждением.
	ResetConsequence []string
}

// pageBatchDeps — общая часть зависимостей обеих задач.
type pageBatchDeps struct {
	profile   tasks.Profile
	command   taskCommand
	outputDir string
	logger    *slog.Logger
	output    io.Writer
	words     pageBatchWords
}

// pageBatchStore — то, что общий сброс требует от таблицы задачи.
//
// Два метода: масштаб удаления человек должен увидеть до подтверждения, а не после.
type pageBatchStore interface {
	Count(ctx context.Context) (int, error)
	Reset(ctx context.Context) error
}

// runPageBatchReset возвращает задачу к нулю: пустая таблица и пустой каталог артефактов.
//
// Только без ID. Сброс одной страницы не поддерживается намеренно: у таких задач нет ни
// промежуточных состояний, ни этапов, которые имело бы смысл переигрывать поодиночке.
//
// Отказ по ID приходит до обращения к базе — на этом держатся тесты, которым PostgreSQL не
// нужен.
func runPageBatchReset(ctx context.Context, store pageBatchStore, deps pageBatchDeps) error {
	if strings.TrimSpace(deps.command.ExternalID) != "" {
		return fmt.Errorf("%s reset сбрасывает всю задачу и ID не принимает: "+
			"состояний у %s два — %s", deps.profile.Command, deps.words.ItemGenitive, deps.words.States)
	}
	articles, err := store.Count(ctx)
	if err != nil {
		return err
	}
	folders, err := countDirectoryEntries(deps.outputDir)
	if err != nil {
		return err
	}
	fmt.Fprintln(deps.output)
	fmt.Fprintln(deps.output, "Будет удалено безвозвратно:")
	fmt.Fprintf(deps.output, "  %s в схеме %s: %d (счётчик идентификаторов обнулится)\n",
		deps.words.ItemsGenitive, deps.profile.Name, articles)
	fmt.Fprintf(deps.output, "  каталогов артефактов в %s: %d\n", deps.outputDir, folders)
	fmt.Fprintln(deps.output)
	for _, line := range deps.words.ResetConsequence {
		fmt.Fprintln(deps.output, line)
	}
	fmt.Fprintln(deps.output)
	confirmed, err := confirmDestructive(confirmationWord, deps.command.AssumeYes,
		isCharDevice(os.Stdin), os.Stdin, deps.output)
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(deps.output, "Отменено.")
		return nil
	}
	if err := store.Reset(ctx); err != nil {
		return err
	}
	if err := clearDirectoryContents(deps.outputDir); err != nil {
		return err
	}
	deps.logger.Info("задача сброшена", "articles", articles, "folders", folders, "output_dir", deps.outputDir)
	fmt.Fprintf(deps.output, "Задача %s сброшена: таблица пуста, счётчик идентификаторов с единицы, "+
		"артефакты удалены. Следующий шаг — %s import.\n", deps.profile.Name, deps.profile.Command)
	return nil
}

// runPageBatch проводит пачку через поток задачи под предохранителем.
//
// Предохранитель считает подряд идущие отказы: единичные не останавливают пачку, а сплошные —
// останавливают. Без него прогон, у которого слёг провайдер, честно перебирает все оставшиеся
// страницы по четверти часа на каждую.
//
// Оставшиеся после остановки страницы не тронуты: их не пробовали, отметки у них нет, и
// следующий run возьмёт их сам. Сказать об этом надо здесь — по коду возврата отличить
// брошенную пачку от пройденной нельзя.
func runPageBatch(ctx context.Context, externalIDs []string,
	run func(ctx context.Context, externalID string) error, deps pageBatchDeps) error {
	guard := pagebatch.NewFailureGuard()
	var failed, done int
	for index, externalID := range externalIDs {
		if err := run(ctx, externalID); err != nil {
			if ctx.Err() != nil {
				return err
			}
			failed++
			deps.logger.Error(deps.words.FailedOne, "external_id", externalID, "error", err)
			fmt.Fprintf(deps.output, "%s — ошибка: %v\n", externalID, err)
			if stop := guard.Failed(err); stop != nil {
				untouched := len(externalIDs) - index - 1
				deps.logger.Error("прогон остановлен предохранителем",
					"error", stop, deps.words.DoneCounter, done, "failed", failed, "untouched", untouched)
				fmt.Fprintf(deps.output, "\n%v.\nОстальные %d %s не тронуты — их возьмёт следующий %s run.\n",
					stop, untouched, deps.words.ItemsGenitive, deps.profile.Command)
				return fmt.Errorf("%w (%s %d, %s %d, не тронуто %d)",
					stop, deps.words.DoneMany, done, deps.words.FailedMany, failed, untouched)
			}
			continue
		}
		guard.Passed()
		done++
		fmt.Fprintf(deps.output, "%s — %s\n", externalID, deps.words.Done)
	}
	if failed > 0 {
		return fmt.Errorf("%s %s: %d из %d", deps.words.FailedMany, deps.words.ItemsGenitive,
			failed, len(externalIDs))
	}
	return nil
}
