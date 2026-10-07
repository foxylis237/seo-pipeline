package wordpress

import (
	"context"
	"fmt"
	"html"
	"strings"
)

// postsPerPage — размер страницы при поиске записи; выше WordPress отвечает заметно медленнее.
const postsPerPage = 100

// maxPostPages ограничивает обход выдачи поиска: поиск идёт подстрокой и на коротком имени
// возвращает сотни записей.
const maxPostPages = 5

// ErrPostNotFound — записи с таким заголовком в этом типе записей нет.
type ErrPostNotFound struct {
	PostType string
	Title    string
}

func (e *ErrPostNotFound) Error() string {
	return fmt.Sprintf("в WordPress нет записи типа %s с заголовком %q — заводить их автоматически запрещено",
		e.PostType, e.Title)
}

// ErrPostAmbiguous — заголовку соответствует больше одной записи; брать первую нельзя.
type ErrPostAmbiguous struct {
	PostType string
	Title    string
	IDs      []int64
}

func (e *ErrPostAmbiguous) Error() string {
	ids := make([]string, 0, len(e.IDs))
	for _, id := range e.IDs {
		ids = append(ids, fmt.Sprintf("%d", id))
	}
	return fmt.Sprintf("в WordPress несколько записей типа %s с заголовком %q: %s — выбрать за человека нельзя",
		e.PostType, e.Title, strings.Join(ids, ", "))
}

// FindPostIDByTitle ищет единственную опубликованную запись заданного типа по точному заголовку.
// Параметр s у WordPress ищет подстрокой, поэтому заголовки сверяются ещё раз.
func (c *Client) FindPostIDByTitle(ctx context.Context, postType, title string) (int64, error) {
	if strings.TrimSpace(postType) == "" {
		return 0, fmt.Errorf("тип записи пуст")
	}
	wanted := normalizePostTitle(title)
	if wanted == "" {
		return 0, fmt.Errorf("заголовок записи типа %s пуст", postType)
	}
	var matched []int64
	for page := 0; page < maxPostPages; page++ {
		var response xmlrpcResponse
		params := []any{
			xmlrpcBlogID,
			c.cfg.Username,
			c.cfg.AppPassword,
			xmlrpcStruct{
				{Name: "post_type", Value: postType},
				// Умолчание статуса у wp.getPosts зависит от версии WordPress.
				{Name: "post_status", Value: PostStatusPublish},
				{Name: "s", Value: strings.TrimSpace(title)},
				{Name: "number", Value: postsPerPage},
				{Name: "offset", Value: page * postsPerPage},
			},
			xmlrpcArray{"post_id", "post_title", "post_type"},
		}
		if err := c.call(ctx, "wp.getPosts", params, &response); err != nil {
			return 0, err
		}
		items, ok := response.Value.([]any)
		if !ok {
			return 0, &ResponseError{Endpoint: "wp.getPosts", Message: "ответ не похож на список записей"}
		}
		for _, item := range items {
			post, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id := int64(intFromValue(post["post_id"]))
			if id <= 0 || normalizePostTitle(stringFromValue(post["post_title"])) != wanted {
				continue
			}
			matched = append(matched, id)
		}
		if len(items) < postsPerPage {
			break
		}
	}
	switch len(matched) {
	case 0:
		return 0, &ErrPostNotFound{PostType: postType, Title: title}
	case 1:
		return matched[0], nil
	default:
		return 0, &ErrPostAmbiguous{PostType: postType, Title: title, IDs: matched}
	}
}

// normalizePostTitle приводит заголовок к сравнимому виду: WordPress отдаёт его с HTML-сущностями.
func normalizePostTitle(title string) string {
	return strings.Join(strings.Fields(strings.ToLower(html.UnescapeString(title))), " ")
}
