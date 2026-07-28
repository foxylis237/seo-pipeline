package validator

import (
	"fmt"
	"strings"
)

// FormatReport returns one complete terminal-ready report without article text.
func FormatReport(articleID int64, externalID string, report Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "========================================\nARTICLE VALIDATION REPORT\narticle_id=%d external_id=%s\n========================================\n\n", articleID, externalID)
	fmt.Fprintf(&b, "ОБЪЁМ\nСимволов: %d\nСлов: %d\nПредложений: %d\n\n", report.Characters, report.Words, report.Sentences)
	fmt.Fprintf(&b, "СТРУКТУРА\nH1: %d\nH2: %d\nH3: %d\nH4: %d\n", report.Headings.H1, report.Headings.H2, report.Headings.H3, report.Headings.H4)
	if report.StructureSkipped {
		b.WriteString("Проверка ожидаемой структуры: ПРОПУЩЕНА — структура не передана\n")
	}
	b.WriteString("\nНАЙДЕННЫЕ ПРОБЛЕМЫ\n")
	if len(report.Issues) == 0 {
		b.WriteString("PASS: ошибок и предупреждений не найдено\n")
	}
	for _, issue := range report.Issues {
		prefix := "WARN"
		if issue.Severity == SeverityError {
			prefix = "ERROR"
		}
		fmt.Fprintf(&b, "%s [%s] %s", prefix, issue.Check, issue.Message)
		if issue.Line > 0 {
			fmt.Fprintf(&b, " — строка %d", issue.Line)
		}
		b.WriteByte('\n')
		if issue.Fragment != "" {
			fmt.Fprintf(&b, "   %s\n", issue.Fragment)
		}
	}
	b.WriteString("\nСТАТИСТИКА РАСПРЕДЕЛЕНИЯ СЛОВ\n")
	for _, stat := range report.TopWords {
		fmt.Fprintf(&b, "- %s: %d\n", stat.Word, stat.Count)
	}
	b.WriteString("Отслеживаемые слова: ")
	for i, stat := range report.TrackedWords {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s=%d", stat.Word, stat.Count)
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "Вариативность длины предложений: средняя %.1f, минимум %.0f, максимум %.0f, коротких %d, длинных %d\n", report.SentenceLengths.Average, report.SentenceLengths.Minimum, report.SentenceLengths.Maximum, report.SentenceLengths.Short, report.SentenceLengths.Long)
	if report.KeywordsSkipped {
		b.WriteString("Ключевые запросы: проверка пропущена — список не передан\n")
	} else {
		fmt.Fprintf(&b, "Использовано уникальных ключей: %d\n", len(report.UsedKeywords))
		for _, stat := range report.UsedKeywords {
			fmt.Fprintf(&b, "- %s: %d\n", stat.Phrase, stat.Count)
		}
	}
	if report.LSISkipped {
		b.WriteString("LSI: проверка пропущена — список не передан\n")
	} else {
		fmt.Fprintf(&b, "Использовано LSI: %d\n", len(report.FrequentLSI))
		for _, stat := range report.FrequentLSI {
			fmt.Fprintf(&b, "- %s: %d\n", stat.Phrase, stat.Count)
		}
	}
	b.WriteString("\nНЕ ПРОВЕРЯЕТСЯ АВТОМАТИЧЕСКИ\n- фактическая достоверность\n- актуальность зарплат\n- юридическая корректность требований к образованию\n- E-E-A-T и YMYL\n- смысловые повторы\n- естественность и заметность ИИ\n- качество примеров\n- соответствие поисковому интенту\n")
	errorsCount, warningsCount := 0, 0
	for _, issue := range report.Issues {
		if issue.Severity == SeverityError {
			errorsCount++
		} else if issue.Severity == SeverityWarning {
			warningsCount++
		}
	}
	fmt.Fprintf(&b, "\nИТОГ\nПроверок с ошибками: %d\nПредупреждений: %d\nСтатус: %s\n", errorsCount, warningsCount, ResultStatus(report))
	return b.String()
}
