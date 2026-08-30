package wordpress

import (
	"context"
	"fmt"
	"strings"
)

// slugScanPages ограничивает обход при поиске записи по слагу.
//
// Слаг в фильтре wp.getPosts не поддерживается — фильтр принимает тип, статус, порядок и
// поиск подстрокой, но не post_name, — поэтому записи перебираются страницами и слаг
// сверяется у каждой. Тридцати страниц по сотне хватает типу записи любого размера из тех,
// что есть на площадке; дойти до предела означает, что записи с таким слагом нет.
const slugScanPages = 30

// PostUpdate — то, что меняется у уже опубликованной записи.
//
// Полей ровно два, и это объявление, а не заготовка: задача pprof_fix_1 правит заголовок и
// тело, всё остальное у записи остаётся прежним. Слаг, дата, рубрики, метки, обложка и поля
// ACF не называются вовсе — не отправленное поле WordPress не трогает, и адрес статьи
// вместе с накопленными позициями не меняется.
type PostUpdate struct {
	PostID      int64
	Title       string
	ContentHTML string
	// Fields — поля postmeta, которые меняются вместе с текстом.
	//
	// Нужны потому, что видимый заголовок страницы услуги живёт не в post_title, а в поле
	// ACF (prof_title): тема рисует H1 из него. Правка, сменившая только post_title, меняет
	// название в админке и не меняет ничего на самой странице.
	Fields []FieldUpdate
}

// FieldUpdate — одно поле postmeta существующей записи.
//
// ID обязателен: без него wp.editPost не обновляет поле, а добавляет второе с тем же
// ключом. Идентификатор приходит из чтения записи (StoredPost.FieldIDs), то есть поле
// сначала должно существовать — заводить новые поля правка не умеет и не должна.
type FieldUpdate struct {
	ID    string
	Key   string
	Value string
}

// EditPost переписывает заголовок и тело существующей записи.
//
// Единственный метод пакета, который меняет чужую запись, и живёт он отдельным файлом
// намеренно: создание (CreatePost) повторять при обрыве нельзя, потому что вторая попытка
// даёт вторую запись в блоге, — а правка идемпотентна, второй такой же вызов приводит
// запись в то же состояние. Повторов здесь всё равно нет: решение «повторять или нет»
// принимает вызывающий, который знает, сохранён ли у него оригинал.
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
			if strings.TrimSpace(field.ID) == "" {
				return fmt.Errorf("поле %q без идентификатора: правка завела бы второе поле с тем же ключом", field.Key)
			}
			fields = append(fields, xmlrpcStruct{
				{Name: "id", Value: field.ID},
				{Name: "key", Value: field.Key},
				{Name: "value", Value: field.Value},
			})
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

// FoundPost — запись, найденная по адресу.
type FoundPost struct {
	ID   int64
	Slug string
	Link string
	// PostType — тип записи, в котором она нашлась. Нужен логам: типов на площадке несколько,
	// и знать, где именно нашлась статья, важнее, чем кажется — правка уйдёт именно туда.
	PostType string
}

// FindPostBySlug находит опубликованную запись по слагу из её адреса.
//
// Слаг, а не заголовок: заголовок задача как раз и меняет, и после первого же прогона поиск
// по нему перестал бы находить статью.
//
// Типы записей перебираются в заданном порядке, и берётся первое совпадение. Список нужен
// потому, что статьи площадки живут не только в post: страницы услуг заведены своим типом,
// и в REST он не выставлен вовсе — оттого поиск и идёт по XML-RPC, который видит все типы.
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

// findInPostType перебирает записи одного типа и возвращает ту, у которой совпал слаг.
// Нулевой идентификатор без ошибки означает «в этом типе такой записи нет».
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
