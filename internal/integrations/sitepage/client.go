// Package sitepage читает публичные страницы сайта, на который ведёт перелинковка, чтобы
// назвать программу так, как на странице: по адресу модель восстанавливает название транслитом.
package sitepage

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	// bodyLimit — сколько байт страницы читать: заголовок стоит в начале документа.
	bodyLimit = 512 << 10
	// userAgent — обычный браузерный, иначе часть страниц отвечает заглушкой.
	userAgent = "Mozilla/5.0 (compatible; seo-pipeline/1.0)"
)

var (
	h1RE    = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`)
	titleRE = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	tagRE   = regexp.MustCompile(`(?is)<[^>]+>`)
	spaceRE = regexp.MustCompile(`\s+`)
	// Хвосты названия: пометка в скобках («(ФИС ФРДО)») и приписка о внесении в реестр.
	parenTailRE    = regexp.MustCompile(`\s*\([^()]*\)\s*$`)
	registryTailRE = regexp.MustCompile(`(?i)\s*[-–—,]?\s*с\s+внесением\s+в\s+(?:фис\s+)?фрдо\s*$`)
)

// Client забирает названия страниц по их адресу.
type Client struct{ http *http.Client }

// New собирает клиент с общим таймаутом на запрос; nil transport — транспорт по умолчанию.
func New(timeout time.Duration, transport http.RoundTripper) *Client {
	return &Client{http: &http.Client{Timeout: timeout, Transport: transport}}
}

// Name возвращает название программы со страницы: H1, а без него — title (на части страниц
// H1 рисует тема из поля ACF, и в разметке его нет).
func (c *Client) Name(ctx context.Context, url string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("собрать запрос страницы %s: %w", url, err)
	}
	request.Header.Set("User-Agent", userAgent)
	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("запросить страницу %s: %w", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("страница %s ответила %s", url, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, bodyLimit))
	if err != nil {
		return "", fmt.Errorf("прочитать страницу %s: %w", url, err)
	}
	name := PageName(string(body))
	if name == "" {
		return "", fmt.Errorf("на странице %s нет ни H1, ни title", url)
	}
	return name, nil
}

// PageName достаёт название из разметки страницы.
func PageName(markup string) string {
	if match := h1RE.FindStringSubmatch(markup); match != nil {
		if name := dropTails(cleanName(match[1])); name != "" {
			return name
		}
	}
	if match := titleRE.FindStringSubmatch(markup); match != nil {
		return dropTails(cleanName(match[1]))
	}
	return ""
}

// dropTails снимает служебные хвосты, сколько бы их ни оказалось подряд.
func dropTails(name string) string {
	for {
		trimmed := registryTailRE.ReplaceAllString(parenTailRE.ReplaceAllString(name, ""), "")
		trimmed = strings.TrimRight(strings.TrimSpace(trimmed), "-–—,")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == name {
			return name
		}
		name = trimmed
	}
}

// cleanName снимает вложенные теги, HTML-сущности и лишние пробелы.
func cleanName(value string) string {
	value = tagRE.ReplaceAllString(value, " ")
	value = html.UnescapeString(value)
	return strings.TrimSpace(spaceRE.ReplaceAllString(value, " "))
}
