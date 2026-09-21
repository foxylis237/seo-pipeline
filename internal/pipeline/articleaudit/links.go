package articleaudit

import (
	"net/url"
	"regexp"
	"strings"
)

// LinkCheck — перелинковка в теле страницы, посчитанная кодом.
//
// Считает код, а не модель: ссылка либо стоит в тексте, либо нет — это факт, а не суждение.
// Спрошенная о числе ссылок модель отвечает наугад: анкор она видит, адрес за ним — нет, и
// упоминание программы словами засчитывает за ссылку на неё.
//
// Минимум приходит из профиля задачи, а не из движка: у статьи блога перелинковка обязательна,
// у страницы услуги её может не быть вовсе. Ноль означает «проверка выключена» — это рабочее
// состояние, а не недоделка.
type LinkCheck struct {
	// Min — сколько внутренних ссылок задача считает минимумом.
	Min int
	// URLs — уникальные адреса внутренних ссылок в порядке появления в тексте.
	URLs []string
}

// Enabled отвечает, проверяется ли перелинковка у этой задачи.
func (c LinkCheck) Enabled() bool { return c.Min > 0 }

// Count — сколько внутренних ссылок нашлось.
func (c LinkCheck) Count() int { return len(c.URLs) }

// Enough отвечает, набралось ли ссылок до минимума. У выключенной проверки — всегда да:
// ненайденная ссылка у задачи, которая её не требует, ошибкой не является.
func (c LinkCheck) Enough() bool { return !c.Enabled() || c.Count() >= c.Min }

// anchorHrefRE — адрес ссылки в разметке. Кавычки бывают и двойные, и одинарные: разметку
// пишет модель, и приводить её к одному виду здесь нечем — тело прочитано из блога как есть.
var anchorHrefRE = regexp.MustCompile(`(?i)<a\b[^>]*\bhref\s*=\s*["']([^"']*)["']`)

// CollectInternalLinks собирает внутренние ссылки тела страницы.
//
// Внутренней считается ссылка на ту же площадку: относительная либо абсолютная с тем же
// хостом, что у самой страницы. Источник под таблицей заработка перелинковкой не является —
// его промпт как раз требует, и засчитывать внешние ссылки значило бы объявлять перелинковку
// там, где её нет.
//
// Ссылка страницы на саму себя не считается: читателя она никуда не ведёт. Повторы одного
// адреса считаются один раз — три ссылки на одну программу это одна перелинковка.
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

// siteOf разбирает адрес страницы на хост и её собственный путь.
//
// Пустой или неразобранный адрес оставляет хост пустым: тогда внутренними считаются только
// относительные ссылки. Это осторожная сторона ошибки — чужую ссылку за свою мы не примем.
func siteOf(pageURL string) (host, self string) {
	parsed, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil || parsed.Host == "" {
		return "", ""
	}
	return normalizeHost(parsed.Host), normalizePath(parsed.Path)
}

// internalAddress приводит href к сравнимому виду и отвечает, ведёт ли он на ту же площадку.
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
	// mailto:, tel:, javascript: — не переходы по сайту.
	case parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https":
		return "", false
	case parsed.Host == "":
		// Относительная ссылка — всегда своя площадка.
	case host == "" || normalizeHost(parsed.Host) != host:
		return "", false
	}
	return normalizePath(parsed.Path), true
}

// normalizeHost снимает с хоста регистр и «www.»: для перелинковки это один и тот же сайт.
func normalizeHost(host string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "www.")
}

// normalizePath приводит путь к сравнимому виду: без регистра и без хвостовой косой черты.
// Адрес с ней и без неё — одна и та же страница, и считать их двумя ссылками нельзя.
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
