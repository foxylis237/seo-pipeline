package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// newPrettyLogger собирает логгер поверх буфера с фиксированной шириной и без цвета: тест
// проверяет раскладку, а не escape-последовательности.
func newPrettyLogger(out *bytes.Buffer, width int) *slog.Logger {
	return slog.New(newPrettyHandler(out, slog.LevelInfo, width, false))
}

// Повторяет вывод make task-1 import 2: шапка вместо повторяющихся task и operation,
// по строке на статью, время работы в подвале.
func TestPrettyHandlerRendersImportRun(t *testing.T) {
	var out bytes.Buffer
	logger := newPrettyLogger(&out, 76).With("task", "task_1", "operation", "import")

	logger.Info("task started", "stage", "start")
	logger.Info("новая статья импортирована", "article_id", 1, "external_id", "37",
		"title", "Как стать логопедом: обучение, обязанности, зарплата и карьерные перспективы")
	logger.Info("импорт статей завершён", "viewed_count", 2, "imported_count", 2)
	logger.Info("task completed", "stage", "complete", "duration_ms", 1)

	report := out.String()
	if !strings.Contains(report, "task_1 · import") {
		t.Fatalf("нет шапки запуска:\n%s", report)
	}
	// task и operation постоянны весь запуск, поэтому в строках событий их быть не должно.
	if strings.Contains(report, "task=task_1") || strings.Contains(report, "operation=import") {
		t.Fatalf("постоянные поля повторяются в строках:\n%s", report)
	}
	if strings.Contains(report, "task started") || strings.Contains(report, "task completed") {
		t.Fatalf("служебные записи должны стать рамкой, а не строками:\n%s", report)
	}
	if !strings.Contains(report, "[37]") {
		t.Fatalf("нет номера статьи:\n%s", report)
	}
	if !strings.Contains(report, "готово · 1 мс") {
		t.Fatalf("нет подвала с временем работы:\n%s", report)
	}
}

// Ни одна строка не должна вылезать за ширину окна: ради этого всё и затевалось.
func TestPrettyHandlerKeepsEveryLineWithinWidth(t *testing.T) {
	const width = 60
	var out bytes.Buffer
	logger := newPrettyLogger(&out, width).With("task", "task_1", "operation", "import")

	logger.Info("новая статья импортирована", "article_id", 2, "external_id", "38",
		"title", "Как стать инструктором по физической культуре в дошкольном образовательном учреждении: обучение и карьера")
	logger.Info("импорт статей завершён", "viewed_count", 2, "imported_count", 2,
		"existing_count", 0, "invalid_count", 0, "empty_count", 0, "limit_reached", true,
		"report_path", "output/task1/import-reports/import-20260815T085845.579268000Z.json")

	for line := range strings.SplitSeq(strings.TrimRight(out.String(), "\n"), "\n") {
		if length := len([]rune(line)); length > width {
			t.Fatalf("строка шириной %d при пределе %d: %q", length, width, line)
		}
	}
}

// Длина считается в рунах, а не байтах: иначе кириллица обрезалась бы вдвое раньше и половина
// заголовка исчезала бы без причины.
func TestPrettyHandlerCountsRunesNotBytes(t *testing.T) {
	var out bytes.Buffer
	logger := newPrettyLogger(&out, 60)

	logger.Info(strings.Repeat("я", 200))

	line := strings.TrimRight(out.String(), "\n")
	if length := len([]rune(line)); length != 60 {
		t.Fatalf("ожидалась строка ровно в 60 рун, получено %d", length)
	}
	if !strings.HasSuffix(line, "…") {
		t.Fatalf("обрезанная строка должна оканчиваться многоточием: %q", line)
	}
}

// Текст ошибки не обрезается никогда: усечённая диагностика уводит в неверную сторону, а
// именно её и читают, когда что-то сломалось.
func TestPrettyHandlerNeverTruncatesErrors(t *testing.T) {
	var out bytes.Buffer
	logger := newPrettyLogger(&out, 40)
	failure := errors.New("stage=article_review: обрыв генерации на 3 попытке, провайдер вернул пустой ответ")

	logger.Error("этап завершён с ошибкой", "external_id", "45", "error", failure)

	report := out.String()
	if !strings.Contains(report, failure.Error()) {
		t.Fatalf("текст ошибки потерян:\n%s", report)
	}
	if strings.Contains(report, "…") {
		t.Fatalf("строки уровня error не должны обрезаться:\n%s", report)
	}
}

func TestPrettyHandlerFormatsDurations(t *testing.T) {
	cases := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{name: "миллисекунды", duration: 940 * time.Millisecond, want: "940 мс"},
		{name: "секунды", duration: 25*time.Second + 400*time.Millisecond, want: "25,4 с"},
		{name: "минуты", duration: 3*time.Minute + 7*time.Second, want: "3 мин 7 с"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := formatPrettyDuration(testCase.duration); got != testCase.want {
				t.Fatalf("получено %q, ожидалось %q", got, testCase.want)
			}
		})
	}
}

// duration_ms приходит целым числом миллисекунд, а не slog.Duration.
func TestPrettyHandlerHumanisesDurationMilliseconds(t *testing.T) {
	var out bytes.Buffer
	logger := newPrettyLogger(&out, 76)

	logger.Info("этап завершён", "duration_ms", 25400)

	if report := out.String(); !strings.Contains(report, "25,4 с") {
		t.Fatalf("duration_ms не переведён в человеческий вид:\n%s", report)
	}
}

// WithAttrs клонирует обработчик, но поток вывода у клонов общий: иначе каждый клон заново
// решал бы, что он печатает первым, и вывод рвался бы пустыми строками.
func TestPrettyHandlerSharesOutputStateAcrossClones(t *testing.T) {
	var out bytes.Buffer
	logger := newPrettyLogger(&out, 76)

	logger.With("external_id", "37").Info("первая")
	logger.With("external_id", "38").Info("вторая")

	if strings.HasPrefix(out.String(), "\n") {
		t.Fatalf("вывод начинается с пустой строки:\n%q", out.String())
	}
	if lines := strings.Count(strings.TrimRight(out.String(), "\n"), "\n"); lines != 1 {
		t.Fatalf("ожидались две строки, получено %d переводов строки:\n%s", lines, out.String())
	}
}

func TestResolveConsoleFormat(t *testing.T) {
	cases := []struct {
		name        string
		format      string
		interactive bool
		want        string
	}{
		{name: "auto в терминале", format: "auto", interactive: true, want: "pretty"},
		{name: "auto в пайпе", format: "auto", interactive: false, want: "text"},
		// Явный text обязан остаться text и в терминале: его выставляют, когда вывод парсят.
		{name: "явный text в терминале", format: "text", interactive: true, want: "text"},
		{name: "json в терминале", format: "json", interactive: true, want: "json"},
		{name: "явный pretty в пайпе", format: "pretty", interactive: false, want: "pretty"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := resolveConsoleFormat(testCase.format, testCase.interactive); got != testCase.want {
				t.Fatalf("получено %q, ожидалось %q", got, testCase.want)
			}
		})
	}
}

// Логи статей читают инструментами, поэтому фабрика файловых обработчиков никогда не отдаёт
// pretty — ни для auto, ни для явного pretty.
func TestArticleLogFactoryNeverUsesPretty(t *testing.T) {
	for _, format := range []string{"auto", "pretty", "text"} {
		newHandler, err := newHandlerFactory("info", format)
		if err != nil {
			t.Fatalf("формат %q: %v", format, err)
		}
		var out bytes.Buffer
		slog.New(newHandler(&out)).With("task", "task_1").Info("статья", "external_id", "37")

		report := out.String()
		if !strings.Contains(report, "task=task_1") || !strings.Contains(report, "external_id=37") {
			t.Fatalf("формат %q: файловый лог потерял поля:\n%s", format, report)
		}
		if !strings.Contains(report, "level=INFO") {
			t.Fatalf("формат %q: файловый лог должен оставаться разбираемым:\n%s", format, report)
		}
	}
}
