package articleaudit

import (
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"
)

// FixesFile — имя таблицы правок в корне артефактов задачи.
const FixesFile = "fixes.xlsx"

// Таблица правок — та же пачка, но в виде списка задач для человека.
//
// Сводка отвечает на вопрос «за какую страницу браться», отчёт страницы — «что с ней не так»;
// таблица отвечает на третий: «что именно пойти и сделать». Поэтому в ней одна широкая колонка
// с задачами, а рядом ровно то, по чему страницу находят: оценка, индекс, название и адрес.
//
// Книга, а не Markdown: правки разбирают по строкам, помечают сделанное и делят между людьми —
// это работа таблицы. Пересобирается она сколько угодно, ни сети, ни модели ей не нужно.
const (
	fixesSheet     = "правки"
	fixesGapsTitle = "ДОЗАПОЛНИТЬ:"
	fixesFixTitle  = "ПРАВКИ:"
	// adviceLimit — предел длины совета. Ячейку читают глазами, а не разбирают программой:
	// три полных абзаца в ней нечитаемы, и обрезка идёт по границе слова.
	adviceLimit = 130
)

// WriteFixes собирает таблицу правок по сводке.
//
// Источник — та же Summary, что и у Markdown-сводки: два разных взгляда на одни данные
// расходиться не должны. Страницы идут худшими вперёд, как и в сводке.
func (s Summary) WriteFixes(path string) error {
	book := excelize.NewFile()
	defer func() { _ = book.Close() }()
	if err := book.SetSheetName(book.GetSheetName(0), fixesSheet); err != nil {
		return fmt.Errorf("лист таблицы правок: %w", err)
	}

	header := []any{"Что править", "Оценка", "ID", "Страница", "Ссылка"}
	if err := book.SetSheetRow(fixesSheet, "A1", &header); err != nil {
		return fmt.Errorf("шапка таблицы правок: %w", err)
	}
	line := 1
	for _, page := range s.Pages {
		line++
		cell, err := excelize.CoordinatesToCellName(1, line)
		if err != nil {
			return err
		}
		row := []any{fixesCell(page), scoreText(page), page.ExternalID, page.Title, page.URL}
		if err := book.SetSheetRow(fixesSheet, cell, &row); err != nil {
			return fmt.Errorf("строка %d таблицы правок: %w", line, err)
		}
	}
	if err := decorateFixes(book, line); err != nil {
		return err
	}
	if err := book.SaveAs(path); err != nil {
		return fmt.Errorf("сохранить таблицу правок %q: %w", path, err)
	}
	return nil
}

// decorateFixes делает книгу читаемой: перенос строк, ширина колонок, закреплённая шапка.
func decorateFixes(book *excelize.File, lastLine int) error {
	wrap, err := book.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{WrapText: true, Vertical: "top"},
	})
	if err != nil {
		return err
	}
	bold, err := book.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return err
	}
	if lastLine > 1 {
		last, err := excelize.CoordinatesToCellName(5, lastLine)
		if err != nil {
			return err
		}
		if err := book.SetCellStyle(fixesSheet, "A2", last, wrap); err != nil {
			return err
		}
	}
	if err := book.SetCellStyle(fixesSheet, "A1", "E1", bold); err != nil {
		return err
	}
	for _, column := range []struct {
		name  string
		width float64
	}{{"A", 85}, {"B", 8}, {"C", 6}, {"D", 45}, {"E", 60}} {
		if err := book.SetColWidth(fixesSheet, column.name, column.name, column.width); err != nil {
			return err
		}
	}
	return book.SetPanes(fixesSheet, &excelize.Panes{
		Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft",
	})
}

// fixesCell — содержимое главной колонки: что дозаполнить и что переписать.
//
// Два блока, а не один список: дозаполнить — работа в админке по готовому перечню граф,
// переписать — работа с текстом. Их делают в разное время и часто разные люди.
func fixesCell(page SummaryPage) string {
	var out strings.Builder
	if gaps := fixesGaps(page); len(gaps) > 0 {
		out.WriteString(fixesGapsTitle + "\n")
		for _, gap := range gaps {
			fmt.Fprintf(&out, "• %s\n", gap)
		}
	}
	if len(page.Advice) > 0 {
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString(fixesFixTitle + "\n")
		for _, advice := range page.Advice {
			fmt.Fprintf(&out, "• %s\n", shortenAdvice(advice))
		}
	}
	if out.Len() == 0 {
		return fixesNothing(page)
	}
	return strings.TrimRight(out.String(), "\n")
}

// fixesGaps — что дозаполнить: пустые графы записи и недобор по счёту.
//
// Имена полей переводятся в то, как их называет человек: «поле author_link» заставляет
// вспоминать, что это и где оно в админке.
func fixesGaps(page SummaryPage) []string {
	var gaps []string
	if !page.FAQ.Enough() {
		gaps = append(gaps, fmt.Sprintf("FAQ: %d из %d — дописать вопросы",
			page.FAQ.Count, page.FAQ.Min))
	}
	if !page.Links.Enough() {
		gaps = append(gaps, fmt.Sprintf("перелинковка: %d из %d — добавить ссылки",
			page.Links.Count(), page.Links.Min))
	}
	for _, field := range page.Missing {
		gaps = append(gaps, Label(field))
	}
	if page.RecordGaps > 0 {
		gaps = append(gaps, fmt.Sprintf(
			"ещё %d в самой записи — рубрика, метки, название или обложка", page.RecordGaps))
	}
	return gaps
}

// fixesNothing — что написать, когда делать нечего. Пустая ячейка выглядит как потерянные
// данные, поэтому состояние называется словами.
func fixesNothing(page SummaryPage) string {
	if strings.TrimSpace(page.Error) != "" {
		return "страница не проверена: " + page.Error
	}
	if page.Score == nil {
		return "отчёт не разобран — посмотреть generated/audit.txt"
	}
	return "правок нет"
}

// shortenAdvice режет длинный совет по границе слова.
func shortenAdvice(text string) string {
	runes := []rune(strings.TrimRight(strings.TrimSpace(text), " ."))
	if len(runes) <= adviceLimit {
		return string(runes)
	}
	cut := string(runes[:adviceLimit])
	if space := strings.LastIndex(cut, " "); space > adviceLimit/2 {
		cut = cut[:space]
	}
	return cut + "…"
}
