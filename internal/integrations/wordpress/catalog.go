package wordpress

import (
	"context"
	"fmt"
	"html"
	"strings"
)

// catalogScanPages ограничивает обход одного типа записей.
//
// Восемь сотен услуг обучения — самый крупный тип площадки, то есть восемь страниц по сотне.
// Двадцать страниц дают двукратный запас на рост каталога и при этом не дают циклу уйти в
// бесконечность, если площадка начнёт отдавать одну и ту же страницу.
const catalogScanPages = 20

// CatalogPost — запись площадки, как её отдаёт перебор каталога.
//
// Это не StoredPost: тому нужны поля, термины и обложка одной записи, а здесь читаются
// полторы тысячи записей подряд, и всё лишнее в ответе стоит времени.
type CatalogPost struct {
	ID    int64
	Slug  string
	Title string
	Link  string
	Terms []CatalogTerm
}

// CatalogTerm — термин записи: рубрика площадки или служебная метка вроде prof-type.
// Какой из них рубрика, решает читающий: имя таксономии складывается из типа записи.
type CatalogTerm struct {
	TermID   int64
	Taxonomy string
	Slug     string
	Name     string
}

// ListCatalogPosts читает все опубликованные записи одного типа.
//
// Читается по XML-RPC, а не по REST, и это вынужденно: у типов услуг show_in_rest = false,
// и wp-json отдаёт по ним 404. Тот же перебор страницами, что и у поиска по слагу, — других
// способов увидеть эти записи снаружи у площадки нет.
//
// Термины запрашиваются вместе с записями: рубрика приходит в том же ответе, и второго
// запроса на каждую услугу не нужно.
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
		// Заголовки WordPress отдаёт с сущностями: «Сантехник &#8212; обучение». Разворот
		// делается здесь, у входа, чтобы дальше по коду ходил обычный текст.
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
