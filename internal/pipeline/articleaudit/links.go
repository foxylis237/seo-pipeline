package articleaudit

import (
	"net/url"
	"regexp"
	"strings"
)

// LinkCheck — перелинковка в теле страницы, посчитанная кодом: модель адреса за анкором не видит.
type LinkCheck struct {
	// Min — минимум внутренних ссылок из профиля задачи; ноль выключает проверку.
	Min int
	// URLs — уникальные адреса внутренних ссылок в порядке появления в тексте.
	URLs []string
}

func (c LinkCheck) Enabled() bool { return c.Min > 0 }

func (c LinkCheck) Count() int { return len(c.URLs) }

// Enough отвечает, набралось ли ссылок до минимума; у выключенной проверки — всегда да.
func (c LinkCheck) Enough() bool { return !c.Enabled() || c.Count() >= c.Min }

var anchorHrefRE = regexp.MustCompile(`(?i)<a\b[^>]*\bhref\s*=\s*["']([^"']*)["']`)

// CollectInternalLinks собирает уникальные ссылки тела на ту же площадку (относительные или с хостом страницы),
// кроме ссылки страницы на саму себя.
func CollectInternalLinks(markup, pageURL string, minimum int) LinkCheck {
	check := LinkCheck{Min: minimum}
	host, self := siteOf(pageURL)
	seen := make(map[string]struct{})
	for _, match := range anchorHrefRE.FindAllStringSubmatch(markup, -1) {
		address, ok := internalAddress(match[1], host)
		if !ok || address == self {
			continue
		}
		if _, repeat := seen[address]; repeat {
			continue
		}
		seen[address] = struct{}{}
		check.URLs = append(check.URLs, address)
	}
	return check
}

// siteOf разбирает адрес страницы на хост и путь; при неразобранном адресе внутренними считаются только относительные ссылки.
func siteOf(pageURL string) (host, self string) {
	parsed, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil || parsed.Host == "" {
		return "", ""
	}
	return normalizeHost(parsed.Host), normalizePath(parsed.Path)
}

func internalAddress(href, host string) (string, bool) {
	value := strings.TrimSpace(href)
	if value == "" || strings.HasPrefix(value, "#") {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", false
	}
	switch {
	case parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https":
		return "", false
	case parsed.Host == "":
		// Относительная ссылка — всегда своя площадка.
	case host == "" || normalizeHost(parsed.Host) != host:
		return "", false
	}
	return normalizePath(parsed.Path), true
}

func normalizeHost(host string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "www.")
}

func normalizePath(path string) string {
	trimmed := strings.ToLower(strings.TrimSpace(path))
	if trimmed == "" {
		return "/"
	}
	if len(trimmed) > 1 {
		trimmed = strings.TrimSuffix(trimmed, "/")
	}
	return trimmed
}
