package taskflow

import (
	"log/slog"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/generation"
)

// KeepLead returns markup with the lead of page put back when the model dropped it.
func KeepLead(logger *slog.Logger, stage, page, markup string) string {
	if generation.LeadKept(page, markup) {
		return markup
	}
	logger.Warn("вводный абзац возвращён в разметку кодом", "stage", stage)
	return generation.RestoreLead(page, markup)
}

// WarnLeftoverMarkers logs block markers that the markup kept as plain text.
func WarnLeftoverMarkers(logger *slog.Logger, stage, markup string) {
	if left := generation.LeftoverBlockMarkers(markup); len(left) > 0 {
		logger.Warn("вёрстка не узнала метки визуальных блоков, они остались в разметке текстом",
			"stage", stage, "markers", strings.Join(left, ", "))
	}
}
