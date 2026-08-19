package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/keywords"
)

// keywordsRepository — то, что вставка запросов требует от хранилища. Узко и намеренно без
// методов пайплайна: команда ничего не запускает и никуда наружу не ходит, она только кладёт
// в article_research то, что обычно приносит первый этап Keys.so.
type keywordsRepository interface {
	GetArticleByExternalID(ctx context.Context, externalID string) (article.Article, error)
	SaveManualKeywords(ctx context.Context, articleID int64, keywords []string) error
}

// keywordsOptions собирает окружение команды: откуда читать колонку и куда печатать отчёт.
type keywordsOptions struct {
	// TaskCommand подставляется в подсказку следующего шага: у pprof-1 она обязана называть
	// pprof-1, а не task-1.
	TaskCommand string
	// Interactive сообщает, что In — терминал. От этого зависит только признак конца ввода:
	// с терминала колонка заканчивается пустой строкой, из файла — концом файла.
	Interactive bool
	In          io.Reader
	Out         io.Writer
}

// keywordsInput — разобранная колонка: что сохраняем и о чём предупреждаем.
type keywordsInput struct {
	Keywords []string
	// Duplicates — сколько строк отброшено как повтор уже принятого запроса.
	Duplicates int
	// Suspicious — запросы с символами кроме букв, цифр и пробелов. Не отбрасываются:
	// решение за пользователем, но молчать о них нельзя (см. warnKeywordsWordstatTrap).
	Suspicious []string
}

// runKeywords кладёт вставленную руками колонку запросов вместо первого этапа Keys.so.
//
// Команда заменяет ровно один шаг — «откуда взялся исходный список запросов». Дальше статья
// идёт общим путём: prepare прогонит эти запросы через чистку от дублей Keys.so, отдаст в
// Arsenkin и сохранит research как обычно. Ни LLM, ни браузер здесь не поднимаются.
//
// Признак ручного заполнения — непустые cleaned_keywords при пустом competitor_structure;
// его создаёт SaveManualKeywords, обнуляя остальной research той же транзакцией. Перезапись
// безусловная: и запросы, и статус статьи всегда уступают вставке, в каком бы состоянии она
// ни была.
func runKeywords(
	ctx context.Context,
	articleRepository keywordsRepository,
	options keywordsOptions,
	logger *slog.Logger,
	externalID string,
) error {
	selected, err := articleRepository.GetArticleByExternalID(ctx, externalID)
	if err != nil {
		return err
	}

	printKeywordsPrompt(options.Out, selected, options.Interactive)
	parsed, err := readKeywordsColumn(options.In, options.Interactive)
	if err != nil {
		return err
	}
	if len(parsed.Keywords) == 0 {
		return fmt.Errorf("не получено ни одного запроса: статья external_id=%s не изменена", externalID)
	}

	if err := articleRepository.SaveManualKeywords(ctx, selected.ID, parsed.Keywords); err != nil {
		return err
	}
	logger.Info("ручные запросы сохранены", "stage", "manual_keywords",
		"article_id", selected.ID, "external_id", selected.ExternalID,
		"keywords_count", len(parsed.Keywords), "duplicates_dropped", parsed.Duplicates)

	printKeywordsReport(options.Out, options.TaskCommand, selected, parsed)
	return nil
}

// printKeywordsPrompt объясняет, что вставлять и как закончить. Оба номера печатаются рядом:
// аргумент команды — external_id из Excel, а не внутренний articles.id.
func printKeywordsPrompt(out io.Writer, selected article.Article, interactive bool) {
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Статья external_id=%s (articles.id=%d) %q\n", selected.ExternalID, selected.ID, selected.Title)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Введите или вставьте ключевые запросы — по одному в строке.")
	if interactive {
		fmt.Fprintln(out, "Конец ввода: пустая строка или Ctrl-D.")
	}
	fmt.Fprintln(out)
}

// printKeywordsReport показывает, что именно сохранено и что будет дальше.
func printKeywordsReport(out io.Writer, taskCommand string, selected article.Article, parsed keywordsInput) {
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Сохранено запросов: %d", len(parsed.Keywords))
	if parsed.Duplicates > 0 {
		fmt.Fprintf(out, " (повторов отброшено: %d)", parsed.Duplicates)
	}
	fmt.Fprintln(out)
	warnKeywordsWordstatTrap(out, parsed.Suspicious)
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Статья возвращена к сбору research: статус pending, этап arsenkin_collection (было: %s, %s).\n",
		selected.Status, optionalText(selected.CurrentStep))
	fmt.Fprintln(out, "Прежние запросы и прежний research перезаписаны, ошибка снята.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Первый этап Keys.so для статьи считается выполненным: сбор запросов у")
	fmt.Fprintln(out, "конкурента пропускается, а чистка от дублей, Arsenkin и всё остальное")
	fmt.Fprintln(out, "идут обычным путём при следующем run.")
	fmt.Fprintf(out, "Дальше: make %s run %s\n", taskCommand, selected.ExternalID)
	fmt.Fprintln(out, "Готовые файлы генерации остаются на месте: run продолжит с того этапа,")
	fmt.Fprintf(out, "где их ещё нет. Пересобрать текст под новые запросы — make %s regenerate %s\n",
		taskCommand, selected.ExternalID)
}

// warnKeywordsWordstatTrap предупреждает о символах, которых форма Wordstat не принимает.
// Сама отправка их вычистит (arsenkin.sanitizeWordstatQuery), поэтому предупреждение говорит
// не об отказе, а о том, что фраза уедет изменённой. Запросы сохраняются как есть: чистка
// нужна Wordstat, а не тексту статьи, и подменять вставленное молча здесь нечем.
func warnKeywordsWordstatTrap(out io.Writer, suspicious []string) {
	if len(suspicious) == 0 {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Внимание: в %d запросах есть символы кроме букв, цифр и пробелов.\n", len(suspicious))
	fmt.Fprintln(out, "Перед отправкой в Wordstat они заменяются пробелами — проверьте, что фраза")
	fmt.Fprintln(out, "от этого не теряет смысл:")
	for _, query := range suspicious {
		fmt.Fprintf(out, "    %s\n", query)
	}
}

// readKeywordsColumn разбирает вставленную колонку.
//
// Признак конца ввода зависит от источника: с терминала колонку закрывает пустая строка,
// из файла или конвейера — конец файла. Пустые строки внутри файла при этом не обрывают
// чтение: там пустая строка — это пустая ячейка Excel, а не «я закончил».
//
// Из строки берётся первый столбец до табуляции: Excel копирует колонку с частотностями
// вместе с частотностями, а частотности сюда не относятся — их даёт только Wordstat.
func readKeywordsColumn(in io.Reader, interactive bool) (keywordsInput, error) {
	parsed := keywordsInput{}
	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		query := normalizeManualKeyword(scanner.Text())
		if query == "" {
			if interactive && len(parsed.Keywords) > 0 {
				break
			}
			continue
		}
		key := strings.ToLower(query)
		if _, found := seen[key]; found {
			parsed.Duplicates++
			continue
		}
		seen[key] = struct{}{}
		parsed.Keywords = append(parsed.Keywords, query)
		if !keywords.IsPlainQuery(query) {
			parsed.Suspicious = append(parsed.Suspicious, query)
		}
	}
	if err := scanner.Err(); err != nil {
		return keywordsInput{}, fmt.Errorf("прочитать колонку запросов: %w", err)
	}
	return parsed, nil
}

// normalizeManualKeyword приводит строку колонки к виду запроса: один столбец, без кавычек
// Excel, с одиночными пробелами между словами.
func normalizeManualKeyword(line string) string {
	query, _, _ := strings.Cut(line, "\t")
	query = strings.TrimSpace(query)
	query = strings.Trim(query, `"`)
	return strings.Join(strings.Fields(query), " ")
}
