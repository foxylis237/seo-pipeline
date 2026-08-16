package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// prettyHandler печатает лог так, как его читает человек за терминалом: время, значок уровня,
// сообщение и короткий хвост из полей. Он существует только для консоли — логи статей в
// logs/<операция>.log пишутся прежним text или json, иначе диагностика прогонов перестанет
// разбираться инструментами.
//
// Почему не slog.NewTextHandler с ReplaceAttr: убрать нужно не отдельные поля, а повторяющийся
// каркас. task и operation постоянны весь запуск, level у 95% записей INFO, дата совпадает с
// сегодняшней. ReplaceAttr это не выражает, а формат строки — выражает.
type prettyHandler struct {
	mutex *sync.Mutex
	out   io.Writer
	// wrote общий у всех клонов, как и mutex: WithAttrs копирует структуру, а «печатали ли мы
	// уже что-нибудь» — свойство потока вывода, а не отдельного обработчика.
	wrote  *bool
	level  slog.Level
	width  int
	color  bool
	attrs  []slog.Attr
	groups []string
	// skip перечисляет поля, которые не печатаются: они либо постоянны весь запуск, либо уже
	// показаны в другом виде.
	skip map[string]struct{}
}

// prettyIndent — ширина левого поля «время + значок». Продолжения выравниваются по ней.
const prettyIndent = 13

// defaultPrettyWidth используется, когда ширину терминала узнать не удалось: 80 — безопасный
// минимум, при котором ничего не разъезжается.
const defaultPrettyWidth = 80

// newPrettyHandler собирает обработчик для консоли.
func newPrettyHandler(out io.Writer, level slog.Level, width int, color bool) slog.Handler {
	if width <= 0 {
		width = defaultPrettyWidth
	}
	wrote := false
	return &prettyHandler{
		mutex: &sync.Mutex{},
		out:   out,
		wrote: &wrote,
		level: level,
		width: width,
		color: color,
		skip: map[string]struct{}{
			// Постоянны весь запуск и печатаются в шапке.
			"task": {}, "operation": {},
			// Служебные метки этапа: значок уровня и текст сообщения уже говорят то же самое.
			"stage": {},
			// Внутренний articles.id: в строке уже стоит external_id, которым пользуются в
			// командах. Для сверки с базой он остаётся в logs/<операция>.log и в json.
			"article_id": {},
		},
	}
}

func (h *prettyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *prettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}

func (h *prettyHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groups = append(append([]string{}, h.groups...), name)
	return &clone
}

// Handle печатает одну запись. Ошибка вывода наружу не возвращается как есть: slog не умеет
// её показать, а падать из-за сломанного stdout смысла нет.
func (h *prettyHandler) Handle(_ context.Context, record slog.Record) error {
	fields := map[string]string{}
	for _, attr := range h.attrs {
		collectPrettyAttr(fields, attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		collectPrettyAttr(fields, attr)
		return true
	})

	// Начало и конец запуска — это рамка, а не событие. Распознаются по структурному полю
	// stage, а не по тексту сообщения: текст меняют, не задумываясь о том, кто его читает.
	if record.Level < slog.LevelWarn {
		switch fields["stage"] {
		case "start":
			return h.write(h.header(fields))
		case "complete":
			return h.write(h.footer(fields))
		}
	}

	var lines []string
	head := record.Time.Format("15:04:05") + "  " + h.mark(record.Level) + "  " + h.headline(record, fields)
	lines = append(lines, h.trim(head, record.Level))
	lines = append(lines, h.tail(fields)...)
	return h.write(lines)
}

func (h *prettyHandler) write(lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	h.mutex.Lock()
	defer h.mutex.Unlock()
	// Пустая первая строка съедается: отступ отделяет блоки друг от друга, а не вывод от
	// приглашения командной строки.
	if !*h.wrote && lines[0] == "" {
		lines = lines[1:]
		if len(lines) == 0 {
			return nil
		}
	}
	*h.wrote = true
	_, _ = io.WriteString(h.out, strings.Join(lines, "\n")+"\n")
	return nil
}

// header печатает шапку запуска. Она заменяет собой task и operation, которые иначе повторялись
// бы в каждой строке, ничего не добавляя.
func (h *prettyHandler) header(fields map[string]string) []string {
	title := fields["task"]
	if operation := fields["operation"]; operation != "" {
		if title != "" {
			title += " · "
		}
		title += operation
	}
	if title == "" {
		return nil
	}
	return []string{"", h.paint(title, colorDim), ""}
}

// footer закрывает запуск временем работы.
func (h *prettyHandler) footer(fields map[string]string) []string {
	line := "готово"
	if elapsed, ok := fields["duration_ms"]; ok {
		line += " · " + elapsed
	}
	return []string{"", h.paint(line, colorDim)}
}

// headline собирает главную строку: сообщение и, если запись относится к конкретной статье,
// её номер впереди — по нему запись и ищут.
func (h *prettyHandler) headline(record slog.Record, fields map[string]string) string {
	message := record.Message
	if externalID, ok := fields["external_id"]; ok {
		message = "[" + externalID + "] " + message
	}
	if title, ok := fields["title"]; ok {
		message += " · " + title
	}
	return message
}

// tail печатает поля, которые не влезли в главную строку, с отступом под неё. Ошибка идёт
// отдельной строкой и никогда не обрезается: усечённый текст ошибки бесполезен.
func (h *prettyHandler) tail(fields map[string]string) []string {
	indent := strings.Repeat(" ", prettyIndent)
	var lines []string

	if failure, ok := fields["error"]; ok {
		lines = append(lines, indent+h.paint(failure, colorRed))
	}

	names := make([]string, 0, len(fields))
	for name := range fields {
		switch name {
		case "external_id", "title", "error":
			continue
		}
		if _, skipped := h.skip[name]; skipped {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return lines
	}
	sort.Strings(names)

	pairs := make([]string, 0, len(names))
	for _, name := range names {
		pairs = append(pairs, name+"="+fields[name])
	}
	// Поля переносятся по ширине окна, а не обрезаются: в отличие от заголовка статьи каждое
	// из них короткое, и потерять половину списка хуже, чем занять вторую строку.
	for _, line := range wrapPretty(pairs, h.width-prettyIndent) {
		lines = append(lines, indent+h.paint(line, colorDim))
	}
	return lines
}

// trim обрезает строку по ширине окна. Ошибки и предупреждения не трогаются: их читают
// целиком, и обрезанная диагностика уводит в неверную сторону.
func (h *prettyHandler) trim(line string, level slog.Level) string {
	if level >= slog.LevelWarn {
		return line
	}
	runes := []rune(line)
	if len(runes) <= h.width {
		return line
	}
	if h.width <= 1 {
		return string(runes[:h.width])
	}
	return string(runes[:h.width-1]) + "…"
}

const (
	colorDim    = "\x1b[2m"
	colorRed    = "\x1b[31m"
	colorYellow = "\x1b[33m"
	colorGreen  = "\x1b[32m"
	colorReset  = "\x1b[0m"
)

func (h *prettyHandler) paint(text, color string) string {
	if !h.color || text == "" {
		return text
	}
	return color + text + colorReset
}

// mark — значок уровня. Одним символом видно, что случилось, без слова level в каждой строке.
func (h *prettyHandler) mark(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return h.paint("✗", colorRed)
	case level >= slog.LevelWarn:
		return h.paint("!", colorYellow)
	case level < slog.LevelInfo:
		return h.paint("·", colorDim)
	default:
		return h.paint("✓", colorGreen)
	}
}

// collectPrettyAttr раскладывает атрибут в плоскую карту, разворачивая группы через точку.
func collectPrettyAttr(fields map[string]string, attr slog.Attr) {
	value := attr.Value.Resolve()
	if value.Kind() == slog.KindGroup {
		for _, nested := range value.Group() {
			collectPrettyAttr(fields, slog.Attr{Key: attr.Key + "." + nested.Key, Value: nested.Value})
		}
		return
	}
	if attr.Key == "" {
		return
	}
	// duration_ms приходит целым числом миллисекунд, а не slog.Duration: 25400 читается плохо,
	// «25,4 с» — хорошо. Единственное поле проекта, где единица зашита в имя.
	if attr.Key == "duration_ms" && value.Kind() == slog.KindInt64 {
		fields[attr.Key] = formatPrettyDuration(time.Duration(value.Int64()) * time.Millisecond)
		return
	}
	fields[attr.Key] = formatPrettyValue(value)
}

// formatPrettyValue приводит значение к короткому виду. Длительность в миллисекундах читается
// плохо: 25400 ничего не говорит, 25,4 с говорит.
func formatPrettyValue(value slog.Value) string {
	switch value.Kind() {
	case slog.KindDuration:
		return formatPrettyDuration(value.Duration())
	case slog.KindTime:
		return value.Time().Format("15:04:05")
	case slog.KindFloat64:
		return strconv.FormatFloat(value.Float64(), 'f', -1, 64)
	default:
		return strings.TrimSpace(value.String())
	}
}

func formatPrettyDuration(duration time.Duration) string {
	switch {
	case duration >= time.Minute:
		return fmt.Sprintf("%d мин %d с", int(duration.Minutes()), int(duration.Seconds())%60)
	case duration >= time.Second:
		return strings.Replace(fmt.Sprintf("%.1f с", duration.Seconds()), ".", ",", 1)
	default:
		return fmt.Sprintf("%d мс", duration.Milliseconds())
	}
}

// shortenPretty укорачивает значение до нужной ширины, вырезая середину. Обрезка с конца
// съела бы имя файла у report_path, а именно оно там и нужно; начало пути тоже говорит о
// многом, поэтому сохраняются оба края.
func shortenPretty(text string, width int) string {
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width <= 1 {
		if width <= 0 {
			return ""
		}
		return "…"
	}
	head := (width - 1) / 2
	tail := width - 1 - head
	return string(runes[:head]) + "…" + string(runes[len(runes)-tail:])
}

// wrapPretty раскладывает готовые куски по строкам заданной ширины, не разрывая сам кусок.
// Длина считается в рунах: кириллица иначе переносилась бы вдвое раньше нужного.
func wrapPretty(parts []string, width int) []string {
	if width < 8 {
		width = 8
	}
	var lines []string
	current := ""
	for _, part := range parts {
		// Кусок длиннее строки не переносится сам по себе: пара имя=значение неразрывна.
		// Такое значение усекается, иначе оно вылезло бы за край окна целиком.
		part = shortenPretty(part, width)
		candidate := part
		if current != "" {
			candidate = current + "  " + part
		}
		if len([]rune(candidate)) > width && current != "" {
			lines = append(lines, current)
			current = part
			continue
		}
		current = candidate
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

// terminalWidth возвращает ширину окна. Переменная COLUMNS — единственный источник, который
// читается без cgo и без сторонних зависимостей; когда её нет, берётся безопасный минимум.
func terminalWidth() int {
	width, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS")))
	if err != nil || width <= 0 {
		return defaultPrettyWidth
	}
	return width
}
