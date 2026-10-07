package wordpress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	categoriesPath = "/wp-json/wp/v2/categories"
	tagsPath       = "/wp-json/wp/v2/tags"
)

// termsPerPage — размер страницы справочника; сотня — потолок WordPress.
const termsPerPage = 100

// maxTermPages ограничивает обход справочника: меток на площадке тысячи.
const maxTermPages = 10

// ErrTermNotFound — термин с таким именем на площадке отсутствует.
type ErrTermNotFound struct {
	Taxonomy string
	Name     string
}

func (e *ErrTermNotFound) Error() string {
	return fmt.Sprintf("в WordPress нет %s с именем %q — заводить их автоматически запрещено", e.Taxonomy, e.Name)
}

type termPayload struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// FindCategoryID ищет рубрику по точному имени, обходя справочник целиком: search у WordPress
// ищет подстрокой и по имени, и по описанию.
func (c *Client) FindCategoryID(ctx context.Context, name string) (int64, error) {
	wanted := normalizeTermName(name)
	if wanted == "" {
		return 0, fmt.Errorf("имя рубрики пусто")
	}
	for page := 1; page <= maxTermPages; page++ {
		var terms []termPayload
		query := fmt.Sprintf("?per_page=%d&page=%d&_fields=id,name&orderby=id&order=asc", termsPerPage, page)
		if err := c.get(ctx, categoriesPath+query, &terms); err != nil {
			return 0, err
		}
		if id, found := matchTerm(terms, wanted); found {
			return id, nil
		}
		if len(terms) < termsPerPage {
			break
		}
	}
	return 0, &ErrTermNotFound{Taxonomy: "рубрики", Name: name}
}

// FindTermIDInTaxonomy ищет термин произвольной таксономии по точному имени.
// Через XML-RPC: маршрут REST у таксономии темы есть только при show_in_rest и зовётся по rest_base.
func (c *Client) FindTermIDInTaxonomy(ctx context.Context, taxonomy, name string) (int64, error) {
	if strings.TrimSpace(taxonomy) == "" {
		return 0, fmt.Errorf("имя таксономии пусто")
	}
	wanted := normalizeTermName(name)
	if wanted == "" {
		return 0, fmt.Errorf("имя термина таксономии %s пусто", taxonomy)
	}
	for page := 0; page < maxTermPages; page++ {
		var response xmlrpcResponse
		params := []any{
			xmlrpcBlogID,
			c.cfg.Username,
			c.cfg.AppPassword,
			taxonomy,
			xmlrpcStruct{
				{Name: "search", Value: strings.TrimSpace(name)},
				{Name: "number", Value: termsPerPage},
				{Name: "offset", Value: page * termsPerPage},
				{Name: "hide_empty", Value: false},
			},
		}
		if err := c.call(ctx, "wp.getTerms", params, &response); err != nil {
			return 0, err
		}
		items, ok := response.Value.([]any)
		if !ok {
			return 0, &ResponseError{Endpoint: "wp.getTerms", Message: "ответ не похож на список терминов"}
		}
		for _, item := range items {
			term, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id := int64(intFromValue(term["term_id"]))
			if id > 0 && normalizeTermName(stringFromValue(term["name"])) == wanted {
				return id, nil
			}
		}
		if len(items) < termsPerPage {
			break
		}
	}
	return 0, &ErrTermNotFound{Taxonomy: taxonomy, Name: name}
}

// FindTagID ищет метку по точному имени, ничего не создавая (для сухого прогона).
// Результат search отбирается точным сравнением: search ищет подстрокой.
func (c *Client) FindTagID(ctx context.Context, name string) (int64, error) {
	wanted := normalizeTermName(name)
	if wanted == "" {
		return 0, fmt.Errorf("имя метки пусто")
	}
	for page := 1; page <= maxTermPages; page++ {
		var terms []termPayload
		query := fmt.Sprintf("?search=%s&per_page=%d&page=%d&_fields=id,name&orderby=id&order=asc",
			url.QueryEscape(name), termsPerPage, page)
		if err := c.get(ctx, tagsPath+query, &terms); err != nil {
			return 0, err
		}
		if id, found := matchTerm(terms, wanted); found {
			return id, nil
		}
		if len(terms) < termsPerPage {
			break
		}
	}
	return 0, &ErrTermNotFound{Taxonomy: "метки", Name: name}
}

// Tag — метка, разрешённая в идентификатор.
type Tag struct {
	ID int64
	// Name — имя, которым метку искали.
	Name string
	// Created — метку завела эта операция.
	Created bool
}

// EnsureTag разрешает имя метки в идентификатор: сначала ищет, ненайденную заводит.
// На существующую метку WordPress отвечает 400 term_exists с её term_id — это успех.
func (c *Client) EnsureTag(ctx context.Context, name string) (Tag, error) {
	id, err := c.FindTagID(ctx, name)
	if err == nil {
		return Tag{ID: id, Name: name}, nil
	}
	var notFound *ErrTermNotFound
	if !errors.As(err, &notFound) {
		// При отказе площадки неизвестно, есть ли метка.
		return Tag{}, err
	}
	id, existed, err := c.createTag(ctx, name)
	if err != nil {
		return Tag{}, err
	}
	return Tag{ID: id, Name: name, Created: !existed}, nil
}

// createTag заводит метку; existed — WordPress ответил term_exists и назвал существующую.
func (c *Client) createTag(ctx context.Context, name string) (int64, bool, error) {
	wanted := normalizeTermName(name)
	body, err := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: strings.TrimSpace(name)})
	if err != nil {
		return 0, false, fmt.Errorf("собрать тело запроса %s: %w", tagsPath, err)
	}
	// Слаг не передаётся: транслитерацию кириллицы делает WordPress.
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+tagsPath, bytes.NewReader(body))
	if err != nil {
		return 0, false, fmt.Errorf("собрать запрос %s: %w", tagsPath, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.SetBasicAuth(c.cfg.Username, c.cfg.AppPassword)

	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, false, fmt.Errorf("создание метки %q прервано: %w", name, ctxErr)
		}
		return 0, false, &transportError{Endpoint: tagsPath, Err: err}
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, false, fmt.Errorf("прочитать ответ %s: %w", tagsPath, ctxErr)
		}
		return 0, false, &transportError{Endpoint: tagsPath, Err: fmt.Errorf("прочитать ответ: %w", err)}
	}
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		if id := existingTermID(raw); id > 0 {
			return id, true, nil
		}
		return 0, false, newStatusError(tagsPath, response, raw, c.cfg.AppPassword)
	}
	var created termPayload
	if err := json.Unmarshal(raw, &created); err != nil {
		return 0, false, fmt.Errorf("разобрать ответ %s: %w", tagsPath, err)
	}
	if created.ID <= 0 {
		return 0, false, &ResponseError{
			Endpoint: tagsPath,
			Message:  fmt.Sprintf("ответ без идентификатора метки — неизвестно, заведена ли %q", name),
		}
	}
	// Площадка может подрезать имя фильтром или плагином.
	if normalizeTermName(created.Name) != wanted {
		return 0, false, &ResponseError{
			Endpoint: tagsPath,
			Message: fmt.Sprintf("метка %d заведена под именем %q вместо %q",
				created.ID, created.Name, name),
		}
	}
	return created.ID, false, nil
}

// existingTermID достаёт data.term_id из отказа term_exists и только из него.
func existingTermID(body []byte) int64 {
	var payload struct {
		Code string `json:"code"`
		Data struct {
			TermID int64 `json:"term_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0
	}
	if payload.Code != "term_exists" {
		return 0
	}
	return payload.Data.TermID
}

func matchTerm(terms []termPayload, wanted string) (int64, bool) {
	for _, term := range terms {
		if normalizeTermName(term.Name) == wanted && term.ID > 0 {
			return term.ID, true
		}
	}
	return 0, false
}

// normalizeTermName приводит имя к сравнимому виду: без регистра, с развёрнутыми
// HTML-сущностями — WordPress отдаёт имена закодированными.
func normalizeTermName(name string) string {
	return strings.ToLower(strings.TrimSpace(html.UnescapeString(name)))
}

// SplitTermNames разбирает список имён через запятую, отбрасывая пустые и повторы с сохранением порядка.
func SplitTermNames(raw string) []string {
	parts := strings.Split(raw, ",")
	names := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		key := normalizeTermName(name)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, name)
	}
	return names
}
