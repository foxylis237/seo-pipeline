package wordpress

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Статусы записи, которые умеет ставить пакет.
const (
	PostStatusPublish = "publish"
	PostStatusDraft   = "draft"
)

const (
	defaultPostType         = "post"
	defaultCategoryTaxonomy = "category"
	tagTaxonomy             = "post_tag"
)

// CustomField — одна пара postmeta; числа тоже строкой — WordPress хранит и отдаёт их текстом.
type CustomField struct {
	Key   string
	Value string
	// IDs — связь ACF на несколько записей; уходит XML-RPC-массивом, потому что готовую
	// сериализованную строку WordPress пропускает через maybe_serialize второй раз.
	// Связи на одну запись хватает скаляра: ACF разворачивает его сама (acf_get_array).
	IDs []int64
	// Values — набор флажков ACF (prog_format); уходит XML-RPC-массивом по той же причине, что IDs.
	Values []string
}

// PostPayload — всё, что уходит в WordPress одним вызовом wp.newPost.
type PostPayload struct {
	Title string
	// Slug — адрес записи (post_name); пустой — WordPress выводит его из заголовка.
	Slug string
	// ContentHTML — тело записи как есть: с правом unfiltered_html WordPress сохраняет его побайтово.
	ContentHTML string
	// Status — PostStatusPublish или PostStatusDraft.
	Status string
	// PostType — тип записи; пустой — post.
	PostType string
	// CategoryTaxonomy — таксономия рубрики; пустая — category. Чужую для типа таксономию
	// WordPress молча отбрасывает.
	CategoryTaxonomy string
	CategoryID       int64
	// TagIDs — идентификаторы меток: terms_names молча заводит отсутствующие термины.
	// Пустой список допустим — метки есть не у каждого типа записи.
	TagIDs []int64
	// ThumbnailID — уже загруженное вложение-обложка (wp.newPost принимает только id); ноль — без обложки.
	ThumbnailID int64
	// Fields — ACF и Yoast одним списком.
	Fields []CustomField
}

// StoredPost — то, чем WordPress ответил на чтение записи.
type StoredPost struct {
	ID     int64
	Title  string
	Status string
	// PostType — фактический тип записи; сверкой не проверяется: пустой тип WordPress заменяет на post.
	PostType string
	// Slug — итоговый адрес: занятый слаг WordPress дополняет числом.
	Slug        string
	ContentHTML string
	Link        string
	// TermIDs — термины записи по таксономиям.
	TermIDs map[string][]int64
	// ThumbnailID — обложка записи; ноль — обложки нет.
	ThumbnailID int64
	Fields      map[string]string
	// FieldIDs — идентификаторы postmeta тех же полей: wp.editPost обновляет поле только по id.
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
// Повторов нет: после обрыва вторая попытка может дать второй пост в блоге.
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

// GetPost читает запись по идентификатору.
func (c *Client) GetPost(ctx context.Context, postID int64) (StoredPost, error) {
	var response xmlrpcResponse
	params := []any{
		xmlrpcBlogID,
		c.cfg.Username,
		c.cfg.AppPassword,
		postID,
		xmlrpcArray{"post_id", "post_title", "post_name", "post_status", "post_type", "post_content",
			"link", "terms", "custom_fields", "post_thumbnail"},
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

// Verify сверяет все отправленные поля с тем, что легло в WordPress: какой ключ будет молча
// отброшен, заранее неизвестно.
func (p PostPayload) Verify(stored StoredPost) []Mismatch {
	var mismatches []Mismatch
	add := func(field, expected, actual string) {
		if expected != actual {
			mismatches = append(mismatches, Mismatch{Field: field, Expected: expected, Actual: actual})
		}
	}
	add("post_title", p.Title, stored.Title)
	// Слаг сверяется началом: занятый адрес WordPress дополняет числом («-2»).
	if slug := strings.TrimSpace(p.Slug); slug != "" && !strings.HasPrefix(stored.Slug, slug) {
		mismatches = append(mismatches, Mismatch{Field: "post_name", Expected: slug, Actual: stored.Slug})
	}
	add("post_status", p.Status, stored.Status)
	add("post_content", p.ContentHTML, stored.ContentHTML)
	category := p.categoryTaxonomy()
	add("terms."+category, formatIDs([]int64{p.CategoryID}), formatIDs(stored.TermIDs[category]))
	// Метки сверяются и без отправленных: площадка может повесить свои.
	add("terms."+tagTaxonomy, formatIDs(p.TagIDs), formatIDs(stored.TermIDs[tagTaxonomy]))
	add("post_thumbnail", formatIDs([]int64{p.ThumbnailID}), formatIDs([]int64{stored.ThumbnailID}))
	for _, field := range p.Fields {
		// Связь возвращается сериализованным массивом PHP: сверяется состав, а не строка.
		if len(field.IDs) > 0 {
			add(field.Key, formatIDs(field.IDs), formatIDs(serializedIDs(stored.Fields[field.Key])))
			continue
		}
		// Набор флажков тоже возвращается сериализованным массивом; Value у него пуст.
		if len(field.Values) > 0 {
			add(field.Key, formatValues(field.Values), formatValues(serializedStrings(stored.Fields[field.Key])))
			continue
		}
		add(field.Key, field.Value, stored.Fields[field.Key])
	}
	return mismatches
}

// validate отбивает структурно непригодную нагрузку до запроса.
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
	// Метки не требуются: это правило задачи, а не структуры записи.
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
			// Дубликат ключа WordPress разложил бы в две записи postmeta.
			return fmt.Errorf("WordPress: поле %q встречается в custom_fields дважды", field.Key)
		}
		seen[field.Key] = struct{}{}
	}
	return nil
}

func (p PostPayload) postType() string {
	if strings.TrimSpace(p.PostType) == "" {
		return defaultPostType
	}
	return p.PostType
}

func (p PostPayload) categoryTaxonomy() string {
	if strings.TrimSpace(p.CategoryTaxonomy) == "" {
		return defaultCategoryTaxonomy
	}
	return p.CategoryTaxonomy
}

func (p PostPayload) content() xmlrpcStruct {
	terms := xmlrpcStruct{
		{Name: p.categoryTaxonomy(), Value: xmlrpcArray{p.CategoryID}},
	}
	// Пустой post_tag не отправляется: у типа без меток WordPress отвечает на него отказом.
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
			{Name: "value", Value: field.value()},
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
	if slug := strings.TrimSpace(p.Slug); slug != "" {
		content = append(content, xmlrpcMember{Name: "post_name", Value: slug})
	}
	// Пустой post_thumbnail WordPress понимает как «снять обложку».
	if p.ThumbnailID > 0 {
		content = append(content, xmlrpcMember{Name: "post_thumbnail", Value: p.ThumbnailID})
	}
	return content
}

func storedPostFromMembers(members map[string]any) StoredPost {
	post := StoredPost{
		ID:          int64(intFromValue(members["post_id"])),
		Title:       stringFromValue(members["post_title"]),
		Slug:        stringFromValue(members["post_name"]),
		Status:      stringFromValue(members["post_status"]),
		PostType:    stringFromValue(members["post_type"]),
		ContentHTML: stringFromValue(members["post_content"]),
		Link:        stringFromValue(members["link"]),
		TermIDs:     make(map[string][]int64),
		Fields:      make(map[string]string),
		FieldIDs:    make(map[string]string),
	}
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
	// post_thumbnail приходит описанием вложения, у записи без обложки — пустым массивом.
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

func (f CustomField) value() any { return customFieldValue(f.Value, f.IDs, f.Values) }

// customFieldValue готовит значение поля postmeta; общий у создания записи и у её правки.
func customFieldValue(value string, ids []int64, values []string) any {
	if len(values) > 0 {
		list := make(xmlrpcArray, 0, len(values))
		for _, item := range values {
			list = append(list, item)
		}
		return list
	}
	if len(ids) == 0 {
		return value
	}
	// Строками: так ACF хранит их внутри сериализованного массива (s:3:"507").
	list := make(xmlrpcArray, 0, len(ids))
	for _, id := range ids {
		list = append(list, strconv.FormatInt(id, 10))
	}
	return list
}

// serializedIDs вынимает идентификаторы из сериализованного массива PHP вида
// a:N:{i:0;s:3:"507";…}; строка другого вида даёт пустой набор.
func serializedIDs(value string) []int64 {
	var ids []int64
	for _, match := range serializedStringValue.FindAllStringSubmatch(value, -1) {
		id, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// serializedStrings вынимает строковые элементы сериализованного массива PHP (a:1:{i:0;s:4:"dist";} → [dist]).
func serializedStrings(value string) []string {
	var values []string
	for _, match := range serializedAnyString.FindAllStringSubmatch(value, -1) {
		values = append(values, match[1])
	}
	return values
}

// formatValues приводит набор строк к сравнимому виду: порядок флажков не значим.
func formatValues(values []string) string {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

var serializedAnyString = regexp.MustCompile(`s:\d+:"([^"]*)"`)

var serializedStringValue = regexp.MustCompile(`s:\d+:"(\d+)"`)

// formatIDs приводит набор идентификаторов к сравнимому виду: порядок WordPress не сохраняет.
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
