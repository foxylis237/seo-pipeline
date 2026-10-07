package wordpress

import (
	"context"
	"fmt"
	"html"
	"strings"
)

// catalogScanPages ограничивает обход одного типа записей: запас к крупнейшему типу
// и защита от площадки, отдающей одну и ту же страницу.
const catalogScanPages = 20

// CatalogPost — запись площадки, как её отдаёт перебор каталога; облегчённая против StoredPost.
type CatalogPost struct {
	ID    int64
	Slug  string
	Title string
	Link  string
	Terms []CatalogTerm
}

// CatalogTerm — термин записи: рубрика площадки или служебная метка вроде prof-type.
type CatalogTerm struct {
	TermID   int64
	Taxonomy string
	Slug     string
	Name     string
}

// ListCatalogPosts читает все опубликованные записи одного типа вместе с терминами.
// Через XML-RPC: у типов услуг show_in_rest = false, и wp-json отдаёт по ним 404.
func (c *Client) ListCatalogPosts(ctx context.Context, postType string) ([]CatalogPost, error) {
	if strings.TrimSpace(postType) == "" {
		return nil, fmt.Errorf("тип записи пуст")
	}
	var posts []CatalogPost
	for page := 0; page < catalogScanPages; page++ {
		var response xmlrpcResponse
		params := []any{
			xmlrpcBlogID,
			c.cfg.Username,
			c.cfg.AppPassword,
			xmlrpcStruct{
				{Name: "post_type", Value: postType},
				{Name: "post_status", Value: PostStatusPublish},
				{Name: "number", Value: postsPerPage},
				{Name: "offset", Value: page * postsPerPage},
			},
			xmlrpcArray{"post_id", "post_name", "post_title", "link", "terms"},
		}
		if err := c.call(ctx, "wp.getPosts", params, &response); err != nil {
			return nil, err
		}
		items, ok := response.Value.([]any)
		if !ok {
			return nil, &ResponseError{Endpoint: "wp.getPosts", Message: "ответ не похож на список записей"}
		}
		for _, item := range items {
			member, ok := item.(map[string]any)
			if !ok {
				continue
			}
			post := catalogPostFromMembers(member)
			if post.ID <= 0 {
				continue
			}
			posts = append(posts, post)
		}
		if len(items) < postsPerPage {
			return posts, nil
		}
	}
	return posts, nil
}

func catalogPostFromMembers(members map[string]any) CatalogPost {
	post := CatalogPost{
		ID: int64(intFromValue(members["post_id"])),
		// WordPress отдаёт заголовки с HTML-сущностями (&#8212;).
		Title: html.UnescapeString(stringFromValue(members["post_title"])),
		Slug:  stringFromValue(members["post_name"]),
		Link:  stringFromValue(members["link"]),
	}
	terms, ok := members["terms"].([]any)
	if !ok {
		return post
	}
	for _, item := range terms {
		member, ok := item.(map[string]any)
		if !ok {
			continue
		}
		term := CatalogTerm{
			TermID:   int64(intFromValue(member["term_id"])),
			Taxonomy: stringFromValue(member["taxonomy"]),
			Slug:     stringFromValue(member["slug"]),
			Name:     html.UnescapeString(stringFromValue(member["name"])),
		}
		if term.TermID <= 0 || term.Taxonomy == "" {
			continue
		}
		post.Terms = append(post.Terms, term)
	}
	return post
}
