package wordpress

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Статусы записи, которые умеет ставить пакет. Больше их не будет: команда либо публикует,
// либо готовит черновик для проверки, а редактирование чужих записей запрещено целиком.
const (
	PostStatusPublish = "publish"
	PostStatusDraft   = "draft"
)

const (
	// defaultPostType — обычная запись блога. Подставляется, когда тип не назван.
	defaultPostType = "post"
	// defaultCategoryTaxonomy — встроенная таксономия рубрик. Подставляется, когда своя
	// таксономия не названа.
	defaultCategoryTaxonomy = "category"
	// tagTaxonomy — встроенная таксономия меток. Своей у неё не бывает: метки либо есть у
	// типа записи, либо их не отправляют вовсе.
	tagTaxonomy = "post_tag"
)

// CustomField — одна пара postmeta.
//
// Значение всегда строка, даже когда по смыслу это число: репитер ACF хранит и счётчик
// строк, и сами подполя текстом, и WordPress отдаёт их обратно тоже текстом. Число здесь
// завело бы обратную сверку в сравнение int со строкой.
type CustomField struct {
	Key   string
	Value string
}

// PostPayload — всё, что уходит в WordPress одним вызовом wp.newPost.
//
// Собирается целиком до запроса: наполовину собранной публикации не существует, а дособрать
// её вторым запросом нельзя — редактирование записей пакету запрещено.
type PostPayload struct {
	// Title — заголовок записи.
	Title string
	// ContentHTML — тело записи. Уходит как есть, без обработки: у пользователя есть право
	// unfiltered_html, и WordPress сохраняет разметку побайтово.
	ContentHTML string
	// Status — PostStatusPublish или PostStatusDraft.
	Status string
	// PostType — тип записи. Пустое значение означает обычную запись блога.
	//
	// Поле есть потому, что задачи публикуются в разные сущности одной площадки: статья
	// блога — это post, коммерческая страница услуги живёт своим типом. Тип выбирает
	// вызывающий, пакет за него не решает.
	PostType string
	// CategoryTaxonomy — таксономия рубрики. Пустая означает встроенную category.
	//
	// Рубрика у записи одна при любой таксономии: это её место в каталоге. Своя таксономия
	// нужна тем типам записей, к которым встроенная category не привязана вовсе, — там имя
	// «category» WordPress просто отбросил бы, и запись легла бы без рубрики.
	CategoryTaxonomy string
	// CategoryID — рубрика, уже разрешённая в идентификатор.
	CategoryID int64
	// TagIDs — метки, уже разрешённые в идентификаторы.
	//
	// Именно идентификаторы, а не имена: XML-RPC принимает и terms_names, но тот молча
	// заводит отсутствующие термины. Заводить рубрики и метки нам запрещено, поэтому
	// несуществующее имя обязано отбиться раньше, при разрешении.
	//
	// Пустой список — законное состояние: метки есть не у каждого типа записи, и требовать
	// их от страницы услуги значило бы не опубликовать её никогда. Требование «метки
	// обязательны» принадлежит задаче и проверяется до сборки нагрузки.
	TagIDs []int64
	// ThumbnailID — вложение, которое станет изображением записи.
	//
	// Идентификатор уже загруженного файла: загрузка идёт отдельным вызовом до создания
	// записи, потому что wp.newPost принимает у обложки только идентификатор. Ноль означает
	// «записи обложка не назначается» — решает это вызывающий, пакет обложку не требует.
	ThumbnailID int64
	// Fields — ACF и Yoast одним списком. Порядок значим только для читаемости логов.
	Fields []CustomField
}

// StoredPost — то, чем WordPress ответил на чтение записи.
type StoredPost struct {
	ID     int64
	Title  string
	Status string
	// PostType — фактический тип записи, каким его хранит WordPress.
	//
	// Сверкой не проверяется намеренно: у записи, созданной с пустым типом, здесь стоит
	// подставленное площадкой «post», и сравнение с пустым ожиданием было бы ложной
	// тревогой. Поле нужно тому, кто читает чужую запись, — например, чтобы узнать, каким
	// типом заведены уже существующие страницы.
	PostType    string
	ContentHTML string
	Link        string
	// TermIDs — термины записи по таксономиям, как их вернул WordPress.
	//
	// Карта, а не пара полей под category и post_tag: таксономия рубрики у разных типов
	// записей своя, и знать её имена заранее пакет не может. Сверка спрашивает ровно те
	// таксономии, которые были в отправленной нагрузке.
	TermIDs map[string][]int64
	// ThumbnailID — вложение, назначенное записи изображением. Ноль означает, что обложки
	// у записи нет.
	ThumbnailID int64
	Fields      map[string]string
	// FieldIDs — идентификаторы postmeta тех же полей.
	//
	// Нужны правке: wp.editPost обновляет поле только по его id, а без id заводит вторую
	// запись postmeta с тем же ключом — и какая из двух достанется get_field(), решал бы
	// порядок в базе. Создание записи их не спрашивает: у новой записи полей ещё нет.
	FieldIDs map[string]string
}

// Mismatch — одно поле, которое не сошлось при обратной сверке.
type Mismatch struct {
	Field    string
	Expected string
	Actual   string
}

func (m Mismatch) String() string {
	return fmt.Sprintf("%s: ожидалось %q, в WordPress %q", m.Field, truncate(m.Expected, 80), truncate(m.Actual, 80))
}

// CreatePost создаёт запись одним вызовом и возвращает её идентификатор.
//
// Повторов нет ни при каком отказе, и это не упущение, а требование. При обрыве после
// отправки неизвестно, дошёл ли запрос: вторая попытка — это второй пост в блоге, а удалять
// записи пакету запрещено. Решение «повторять или нет» остаётся человеку, который может
// посмотреть в админку.
func (c *Client) CreatePost(ctx context.Context, payload PostPayload) (int64, error) {
	if err := payload.validate(); err != nil {
		return 0, err
	}
	var response xmlrpcResponse
	params := []any{
		xmlrpcBlogID,
		c.cfg.Username,
		c.cfg.AppPassword,
		payload.content(),
	}
	if err := c.call(ctx, "wp.newPost", params, &response); err != nil {
		return 0, err
	}
	// Идентификатор новой записи WordPress отдаёт строкой, а не числом.
	postID := intFromValue(response.Value)
	if postID <= 0 {
		return 0, &ResponseError{
			Endpoint: "wp.newPost",
			Message:  "ответ без идентификатора записи — неизвестно, создана ли она",
		}
	}
	return int64(postID), nil
}

// GetPost читает запись обратно. Нужен обязательной сверке после создания.
func (c *Client) GetPost(ctx context.Context, postID int64) (StoredPost, error) {
	var response xmlrpcResponse
	params := []any{
		xmlrpcBlogID,
		c.cfg.Username,
		c.cfg.AppPassword,
		postID,
		xmlrpcArray{"post_id", "post_title", "post_status", "post_type", "post_content", "link",
			"terms", "custom_fields", "post_thumbnail"},
	}
	if err := c.call(ctx, "wp.getPost", params, &response); err != nil {
		return StoredPost{}, err
	}
	members, ok := response.Value.(map[string]any)
	if !ok {
		return StoredPost{}, &ResponseError{Endpoint: "wp.getPost", Message: "ответ не похож на запись"}
	}
	return storedPostFromMembers(members), nil
}

// Verify сверяет отправленное с тем, что действительно легло в WordPress.
//
// Проверяются все записываемые поля, а не выборка: смысл сверки в том, чтобы поймать молча
// отброшенный ключ, а какой именно ключ отбросят, заранее неизвестно. Пустой результат
// означает, что публикация состоялась полностью.
func (p PostPayload) Verify(stored StoredPost) []Mismatch {
	var mismatches []Mismatch
	add := func(field, expected, actual string) {
		if expected != actual {
			mismatches = append(mismatches, Mismatch{Field: field, Expected: expected, Actual: actual})
		}
	}
	add("post_title", p.Title, stored.Title)
	add("post_status", p.Status, stored.Status)
	add("post_content", p.ContentHTML, stored.ContentHTML)
	category := p.categoryTaxonomy()
	add("terms."+category, formatIDs([]int64{p.CategoryID}), formatIDs(stored.TermIDs[category]))
	// Метки сверяются и тогда, когда их не отправляли: пустое ожидание против непустого
	// ответа — это метка, повешенная площадкой помимо нас, и знать об этом человек обязан.
	add("terms."+tagTaxonomy, formatIDs(p.TagIDs), formatIDs(stored.TermIDs[tagTaxonomy]))
	// Обложка сверяется наравне с остальным: назначить её вторым запросом нельзя —
	// редактирование записей пакету запрещено, — а запись без картинки в блоге видна сразу.
	add("post_thumbnail", formatIDs([]int64{p.ThumbnailID}), formatIDs([]int64{stored.ThumbnailID}))
	for _, field := range p.Fields {
		add(field.Key, field.Value, stored.Fields[field.Key])
	}
	return mismatches
}

// validate отбивает заведомо непригодную нагрузку до запроса.
//
// Проверка структурная, а не деловая: готовность статьи выясняется раньше и по данным в
// PostgreSQL. Здесь ловится только то, из чего WordPress собрал бы покалеченную запись,
// которую мы потом не смогли бы ни исправить, ни удалить.
func (p PostPayload) validate() error {
	if strings.TrimSpace(p.Title) == "" {
		return errors.New("WordPress: заголовок записи пуст")
	}
	if strings.TrimSpace(p.ContentHTML) == "" {
		return errors.New("WordPress: тело записи пусто")
	}
	if p.Status != PostStatusPublish && p.Status != PostStatusDraft {
		return fmt.Errorf("WordPress: недопустимый статус записи %q", p.Status)
	}
	if p.CategoryID <= 0 {
		return errors.New("WordPress: рубрика не разрешена в идентификатор")
	}
	// Метки не требуются: их наличие — правило задачи, а не структуры записи. Задача, у
	// которой меток нет вовсе, проверяет это до сборки нагрузки и сюда доходит с пустым
	// списком намеренно.
	for _, id := range p.TagIDs {
		if id <= 0 {
			return fmt.Errorf("WordPress: недопустимый идентификатор метки %d", id)
		}
	}
	if p.ThumbnailID < 0 {
		return fmt.Errorf("WordPress: недопустимый идентификатор обложки %d", p.ThumbnailID)
	}
	seen := make(map[string]struct{}, len(p.Fields))
	for _, field := range p.Fields {
		if strings.TrimSpace(field.Key) == "" {
			return errors.New("WordPress: пустое имя поля в custom_fields")
		}
		if _, duplicate := seen[field.Key]; duplicate {
			// Дубликат ключа WordPress разложил бы в две записи postmeta, и какая из них
			// достанется get_field(), решал бы порядок в базе.
			return fmt.Errorf("WordPress: поле %q встречается в custom_fields дважды", field.Key)
		}
		seen[field.Key] = struct{}{}
	}
	return nil
}

// postType возвращает тип записи, подставляя обычную запись блога.
func (p PostPayload) postType() string {
	if strings.TrimSpace(p.PostType) == "" {
		return defaultPostType
	}
	return p.PostType
}

// categoryTaxonomy возвращает таксономию рубрики, подставляя встроенную.
func (p PostPayload) categoryTaxonomy() string {
	if strings.TrimSpace(p.CategoryTaxonomy) == "" {
		return defaultCategoryTaxonomy
	}
	return p.CategoryTaxonomy
}

// content собирает структуру аргумента wp.newPost.
func (p PostPayload) content() xmlrpcStruct {
	terms := xmlrpcStruct{
		{Name: p.categoryTaxonomy(), Value: xmlrpcArray{p.CategoryID}},
	}
	// Пустой post_tag не отправляется вовсе: у типа записи, к которому метки не привязаны,
	// WordPress отвечает на такую таксономию отказом, а не молчаливым пропуском.
	if len(p.TagIDs) > 0 {
		tags := make(xmlrpcArray, 0, len(p.TagIDs))
		for _, id := range p.TagIDs {
			tags = append(tags, id)
		}
		terms = append(terms, xmlrpcMember{Name: tagTaxonomy, Value: tags})
	}
	fields := make(xmlrpcArray, 0, len(p.Fields))
	for _, field := range p.Fields {
		fields = append(fields, xmlrpcStruct{
			{Name: "key", Value: field.Key},
			{Name: "value", Value: field.Value},
		})
	}
	content := xmlrpcStruct{
		{Name: "post_type", Value: p.postType()},
		{Name: "post_status", Value: p.Status},
		{Name: "post_title", Value: p.Title},
		{Name: "post_content", Value: p.ContentHTML},
		{Name: "terms", Value: terms},
		{Name: "custom_fields", Value: fields},
	}
	// Ноль не отправляется вовсе: пустой post_thumbnail WordPress понимает как «снять
	// обложку», и у новой записи это лишнее поле в запросе с несуществующим смыслом.
	if p.ThumbnailID > 0 {
		content = append(content, xmlrpcMember{Name: "post_thumbnail", Value: p.ThumbnailID})
	}
	return content
}

func storedPostFromMembers(members map[string]any) StoredPost {
	post := StoredPost{
		ID:          int64(intFromValue(members["post_id"])),
		Title:       stringFromValue(members["post_title"]),
		Status:      stringFromValue(members["post_status"]),
		PostType:    stringFromValue(members["post_type"]),
		ContentHTML: stringFromValue(members["post_content"]),
		Link:        stringFromValue(members["link"]),
		TermIDs:     make(map[string][]int64),
		Fields:      make(map[string]string),
		FieldIDs:    make(map[string]string),
	}
	// Термины раскладываются по всем таксономиям, какие вернул WordPress, а не по двум
	// известным: какая из них рубрика этой записи, знает отправленная нагрузка, а не ответ.
	if terms, ok := members["terms"].([]any); ok {
		for _, item := range terms {
			term, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id := int64(intFromValue(term["term_id"]))
			taxonomy := stringFromValue(term["taxonomy"])
			if id <= 0 || taxonomy == "" {
				continue
			}
			post.TermIDs[taxonomy] = append(post.TermIDs[taxonomy], id)
		}
	}
	// post_thumbnail приезжает целым описанием вложения, а не числом; у записи без обложки
	// на его месте пустой массив.
	if thumbnail, ok := members["post_thumbnail"].(map[string]any); ok {
		post.ThumbnailID = int64(intFromValue(thumbnail["attachment_id"]))
		if post.ThumbnailID < 0 {
			post.ThumbnailID = 0
		}
	}
	if fields, ok := members["custom_fields"].([]any); ok {
		for _, item := range fields {
			field, ok := item.(map[string]any)
			if !ok {
				continue
			}
			key := stringFromValue(field["key"])
			if key == "" {
				continue
			}
			post.Fields[key] = stringFromValue(field["value"])
			if id := stringFromValue(field["id"]); id != "" {
				post.FieldIDs[key] = id
			}
		}
	}
	return post
}

// formatIDs приводит набор идентификаторов к сравнимому виду. Порядок терминов WordPress не
// сохраняет, и сравнивать их как последовательность было бы ложной тревогой.
func formatIDs(ids []int64) string {
	sorted := make([]int64, len(ids))
	copy(sorted, ids)
	slices.Sort(sorted)
	parts := make([]string, 0, len(sorted))
	for _, id := range sorted {
		parts = append(parts, fmt.Sprintf("%d", id))
	}
	return strings.Join(parts, ",")
}
