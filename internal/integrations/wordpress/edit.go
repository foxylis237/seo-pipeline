package wordpress

import (
	"context"
	"fmt"
	"strings"
)

// slugScanPages ограничивает обход при поиске записи по слагу: фильтр wp.getPosts не
// принимает post_name, поэтому записи перебираются страницами.
const slugScanPages = 30

// PostUpdate — заголовок, тело и поля postmeta, которые меняются у опубликованной записи.
// Не отправленное поле WordPress не трогает.
type PostUpdate struct {
	PostID      int64
	Title       string
	ContentHTML string
	// Fields — поля postmeta, которые меняются вместе с текстом (видимый H1 живёт в prof_title).
	Fields []FieldUpdate
}

// FieldUpdate — одно поле postmeta существующей записи.
// ID берётся из StoredPost.FieldIDs: без него wp.editPost заводит второе поле с тем же ключом.
type FieldUpdate struct {
	ID    string
	Key   string
	Value string
	// IDs — список идентификаторов для связи ACF (см. CustomField.IDs); непустой вытесняет Value.
	IDs []int64
	// Create разрешает завести поле, которого у записи нет; без него пустой ID — ошибка.
	// Ставит тот, кто прочитал запись и знает, что ключа в ней нет.
	Create bool
}

// EditPost переписывает заголовок, тело и названные поля существующей записи.
// Повторов внутри нет: решает вызывающий.
func (c *Client) EditPost(ctx context.Context, update PostUpdate) error {
	if update.PostID <= 0 {
		return fmt.Errorf("идентификатор записи не задан")
	}
	if strings.TrimSpace(update.Title) == "" {
		return fmt.Errorf("заголовок записи пуст")
	}
	if strings.TrimSpace(update.ContentHTML) == "" {
		return fmt.Errorf("тело записи пусто")
	}
	content := xmlrpcStruct{
		{Name: "post_title", Value: update.Title},
		{Name: "post_content", Value: update.ContentHTML},
	}
	if len(update.Fields) > 0 {
		fields := make(xmlrpcArray, 0, len(update.Fields))
		for _, field := range update.Fields {
			if strings.TrimSpace(field.Key) == "" {
				return fmt.Errorf("поле записи названо пустым ключом")
			}
			id := strings.TrimSpace(field.ID)
			if id == "" && !field.Create {
				return fmt.Errorf("поле %q без идентификатора: правка завела бы второе поле с тем же ключом", field.Key)
			}
			// Пустой id не отправляется: поле без id WordPress заводит, с id — обновляет.
			member := make(xmlrpcStruct, 0, 3)
			if id != "" {
				member = append(member, xmlrpcMember{Name: "id", Value: id})
			}
			member = append(member,
				xmlrpcMember{Name: "key", Value: field.Key},
				xmlrpcMember{Name: "value", Value: field.value()},
			)
			fields = append(fields, member)
		}
		content = append(content, xmlrpcMember{Name: "custom_fields", Value: fields})
	}
	var response xmlrpcResponse
	params := []any{
		xmlrpcBlogID,
		c.cfg.Username,
		c.cfg.AppPassword,
		update.PostID,
		content,
	}
	if err := c.call(ctx, "wp.editPost", params, &response); err != nil {
		return err
	}
	return nil
}

func (f FieldUpdate) value() any { return customFieldValue(f.Value, f.IDs, nil) }

// VerifyFields сверяет поля, ушедшие правкой, с тем, что вернуло чтение записи.
// XML-RPC молча отбрасывает ключи с ведущим подчёркиванием; связь сверяется составом
// идентификаторов, потому что WordPress возвращает её сериализованным массивом PHP.
func VerifyFields(updates []FieldUpdate, stored StoredPost) []Mismatch {
	var mismatches []Mismatch
	for _, field := range updates {
		expected, actual := field.Value, stored.Fields[field.Key]
		if len(field.IDs) > 0 {
			expected, actual = formatIDs(field.IDs), formatIDs(serializedIDs(actual))
		}
		if expected != actual {
			mismatches = append(mismatches, Mismatch{Field: field.Key, Expected: expected, Actual: actual})
		}
	}
	return mismatches
}

// FoundPost — запись, найденная по адресу.
type FoundPost struct {
	ID   int64
	Slug string
	Link string
	// PostType — тип записи, в котором она нашлась.
	PostType string
}

// FindPostBySlug находит опубликованную запись по слагу, перебирая типы записей по порядку.
// Через XML-RPC: типы услуг в REST не выставлены.
func (c *Client) FindPostBySlug(ctx context.Context, postTypes []string, slug string) (FoundPost, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return FoundPost{}, fmt.Errorf("слаг записи пуст")
	}
	if len(postTypes) == 0 {
		return FoundPost{}, fmt.Errorf("не названо ни одного типа записи для поиска")
	}
	for _, postType := range postTypes {
		found, err := c.findInPostType(ctx, postType, slug)
		if err != nil {
			return FoundPost{}, err
		}
		if found.ID > 0 {
			return found, nil
		}
	}
	return FoundPost{}, &ErrPostNotFound{PostType: strings.Join(postTypes, ", "), Title: slug}
}

// findInPostType возвращает запись типа с совпавшим слагом; нулевой ID без ошибки — не нашлась.
func (c *Client) findInPostType(ctx context.Context, postType, slug string) (FoundPost, error) {
	for page := 0; page < slugScanPages; page++ {
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
			xmlrpcArray{"post_id", "post_name", "post_title", "post_type", "link"},
		}
		if err := c.call(ctx, "wp.getPosts", params, &response); err != nil {
			return FoundPost{}, err
		}
		items, ok := response.Value.([]any)
		if !ok {
			return FoundPost{}, &ResponseError{Endpoint: "wp.getPosts", Message: "ответ не похож на список записей"}
		}
		for _, item := range items {
			post, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if !strings.EqualFold(stringFromValue(post["post_name"]), slug) {
				continue
			}
			id := int64(intFromValue(post["post_id"]))
			if id <= 0 {
				continue
			}
			return FoundPost{
				ID:       id,
				Slug:     stringFromValue(post["post_name"]),
				Link:     stringFromValue(post["link"]),
				PostType: stringFromValue(post["post_type"]),
			}, nil
		}
		if len(items) < postsPerPage {
			break
		}
	}
	return FoundPost{}, nil
}
