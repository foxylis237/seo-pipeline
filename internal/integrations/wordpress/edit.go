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
// Меняется названное и только оно: заголовок, тело и перечисленные поля postmeta. Слаг,
// дата, рубрики, метки и обложка не называются вовсе — не отправленное поле WordPress не
// трогает, и адрес статьи вместе с накопленными позициями не меняется.
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
// ID приходит из чтения записи (StoredPost.FieldIDs): без него wp.editPost не обновляет
// поле, а добавляет второе с тем же ключом, и какое из двух достанется get_field(), решал бы
// порядок в базе.
type FieldUpdate struct {
	ID    string
	Key   string
	Value string
	// IDs — значение-список идентификаторов записей, каким его хранит связь ACF.
	//
	// Отдельным полем по той же причине, что и у CustomField: готовую сериализованную
	// строку WordPress пропускает через maybe_serialize второй раз, и связь перестаёт
	// читаться. Непустой список вытесняет Value.
	IDs []int64
	// Create разрешает завести поле, которого у записи ещё нет.
	//
	// Без него пустой ID — ошибка, и это главная защита правки от второго поля с тем же
	// ключом. Но у записи, созданной раньше самого поля, его нет вовсе: так связь
	// related_courses отсутствует у статей, опубликованных до появления блока курсов, — и
	// завести её правкой единственный способ. Признак ставит тот, кто прочитал запись и
	// потому знает, что ключа в ней нет.
	Create bool
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
			if strings.TrimSpace(field.Key) == "" {
				return fmt.Errorf("поле записи названо пустым ключом")
			}
			id := strings.TrimSpace(field.ID)
			if id == "" && !field.Create {
				return fmt.Errorf("поле %q без идентификатора: правка завела бы второе поле с тем же ключом", field.Key)
			}
			// Поле без id WordPress заводит впервые, с id — обновляет. Пустой id поэтому не
			// отправляется вовсе: он означал бы «postmeta номер ноль», а не «нового поля».
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

// value — то, что уходит в custom_fields этого поля. Правило одно с созданием записи.
func (f FieldUpdate) value() any { return customFieldValue(f.Value, f.IDs) }

// VerifyFields сверяет поля, ушедшие правкой, с тем, что вернуло чтение записи.
//
// Причина та же, что и у PostPayload.Verify: ключ бывает отброшен молча — так уходят все
// защищённые ключи с ведущим подчёркиванием, — и ответ «200 OK» о содержимом postmeta не
// говорит ничего. Связь сверяется составом идентификаторов, а не строкой: WordPress
// возвращает её сериализованным массивом PHP, и это единственное место, где вообще видно,
// легло ли поле массивом или строкой.
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
