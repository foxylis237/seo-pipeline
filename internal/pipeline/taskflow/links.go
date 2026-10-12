package taskflow

import (
	"context"
	"log/slog"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/generation"
)

// enoughInternalLinks is the number of placed links after which missing ones are not asked for.
const enoughInternalLinks = 3

// LinkNames returns the program name shown on the page at url.
type LinkNames interface {
	Name(ctx context.Context, url string) (string, error)
}

// NamedLinks prefixes each link with the program name from its page.
// A failed lookup keeps the bare URL: the stage must not fail over a name.
func NamedLinks(ctx context.Context, logger *slog.Logger, names LinkNames, stage, links string) string {
	urls := generation.LinkURLs(links)
	if names == nil || len(urls) == 0 {
		return links
	}
	lines := make([]string, 0, len(urls))
	for _, url := range urls {
		name, err := names.Name(ctx, url)
		if err != nil || strings.TrimSpace(name) == "" {
			logger.Warn("название программы не получено, ссылка уходит без него",
				"stage", stage, "url", url, "error", err)
			lines = append(lines, url)
			continue
		}
		lines = append(lines, strings.TrimSpace(name)+" — "+url)
	}
	return strings.Join(lines, "\n")
}

// CompleteLinks asks the model for missing links when fewer than enoughInternalLinks are placed,
// then spreads crowded links across sections. Code never writes link sentences of its own.
func (b *Base) CompleteLinks(ctx context.Context, logger *slog.Logger, chat Chat, stage, links, markup string) string {
	if missing := generation.MissingInternalLinks(markup, links); len(missing) > 0 {
		if placed := generation.PlacedInternalLinks(markup, links); placed >= enoughInternalLinks {
			logger.Info("ссылок перелинковки в тексте достаточно, недостающие не доспрашиваются",
				"stage", stage, "placed_links", placed, "missing_links", len(missing))
		} else {
			markup = b.repairLinks(ctx, logger, chat, stage, links, markup, missing)
		}
	}
	spread, moved := generation.SpreadCrowdedLinks(markup, links)
	if moved > 0 {
		logger.Info("перелинковка расставлена кодом",
			"stage", stage, "links_moved", moved)
		markup = spread
	}
	if left := generation.MissingInternalLinks(markup, links); len(left) > 0 {
		logger.Warn("часть ссылок перелинковки в разметку так и не попала",
			"stage", stage, "missing_links", len(left))
	}
	if crowded := generation.CrowdedLinks(markup, links); len(crowded) > 0 {
		logger.Warn("часть ссылок осталась в одном разделе: свободных разделов не хватило",
			"stage", stage, "crowded_links", len(crowded))
	}
	return markup
}

// repairLinks asks the model for one sentence per missing link; a failed request keeps markup as is.
func (b *Base) repairLinks(ctx context.Context, logger *slog.Logger, chat Chat, stage, links, markup string, missing []string) string {
	logger.Info("ссылок перелинковки в тексте мало, недостающие доспрашиваются у модели",
		"stage", stage, "missing_links", len(missing))
	answer, err := b.Answer(ctx, chat.Continue, generation.RepairLinksPrompt(markup, links, missing), stage)
	if err != nil {
		logger.Warn("доспрос перелинковки не удался", "stage", stage, "error", err)
		return markup
	}
	inserts := generation.ParseLinkInserts(answer, links, missing)
	repaired, skipped := generation.InsertLinkSentences(markup, inserts)
	for _, insert := range skipped {
		logger.Warn("раздел, названный моделью для ссылки, в разметке не найден",
			"stage", stage, "url", insert.URL, "heading", insert.Heading)
	}
	logger.Info("перелинковка дописана моделью", "stage", stage,
		"asked_links", len(missing), "inserted_links", len(inserts)-len(skipped))
	return repaired
}
