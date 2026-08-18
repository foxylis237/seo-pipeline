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

// termsPerPage — размер страницы справочника. Сотня — потолок, который принимает WordPress.
const termsPerPage = 100

// maxTermPages ограничивает обход справочника.
//
// Нужен не рубрикам, которых два десятка, а меткам: их на площадке тысячи, и поиск с
// неудачным запросом мог бы вычерпывать их страницами до бесконечности. Дойти до предела —
// значит признать, что термин не найден, а не молча взять не тот.
const maxTermPages = 10

// ErrTermNotFound — термин с таким именем на площадке отсутствует.
//
// Отдельная ошибка, потому что решение по ней принимается разное. Рубрики приложение заводить
// не имеет права: их два десятка, они продуманы человеком, и опечатка в Excel обязана
// останавливать публикацию, а не плодить восьмую «Строительство и ремонт». Метки, наоборот,
// заводятся по требованию (EnsureTag) — их тысячи, и заранее завести их руками нельзя.
// Наружу ErrTermNotFound уходит от чистого поиска: FindTagID им отвечает сухому прогону,
// которому запрещено что-либо создавать.
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

// FindCategoryID ищет рубрику по точному имени.
//
// Справочник обходится целиком, без параметра search: рубрик два десятка, а search у
// WordPress ищет подстрокой и по имени, и по описанию — то есть на «Строительство» вернул бы
// ещё и всё, где это слово встречается в описании.
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

// FindTagID ищет метку по точному имени, ничего не создавая.
//
// Чистое чтение нужно тем, кому запрещено писать, — прежде всего сухому прогону публикации:
// он обязан показать человеку, каких меток на площадке ещё нет, и при этом не завести ни
// одной. Боевая публикация ходит через EnsureTag.
//
// Здесь search обязателен — меток тысячи, — но его результат отбирается точным сравнением.
// Сам по себе он ищет подстрокой: на «Как стать» WordPress возвращает два десятка меток
// вроде «как стать сварщиком», и взять первую значило бы повесить на статью чужую метку.
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
	// Name — имя, которым метку искали. Возвращается обратно, чтобы вызывающему не
	// приходилось помнить, какому из списка соответствует ответ.
	Name string
	// Created — метку завела эта операция, до неё её на площадке не было.
	//
	// Отдельное поле, а не догадка вызывающего по логам: заведение термина в чужом блоге
	// человек имеет право видеть, и решение о логировании принимает он, а не пакет — своего
	// логгера у пакета нет и заводить его здесь незачем.
	Created bool
}

// EnsureTag разрешает имя метки в идентификатор: сначала ищет, ненайденную заводит.
//
// Дублей не создаёт по двум причинам сразу. Первая — поиск идёт до создания, поэтому у
// заведённой ранее метки берётся её же идентификатор. Вторая — сам WordPress: одноимённая
// метка в одной таксономии существовать дважды не может, и на попытку создать существующую
// он отвечает 400 term_exists с её term_id. Этот ответ считается успехом, а не отказом:
// значит, метку успели завести между нашим поиском и нашей записью.
//
// Повторов у создания нет намеренно. Отказ здесь останавливает публикацию до загрузки
// обложки и до создания записи, то есть блог остаётся нетронутым, а следующий запуск
// повторит всю операцию с начала — и найдёт метку, если она всё же успела появиться.
func (c *Client) EnsureTag(ctx context.Context, name string) (Tag, error) {
	id, err := c.FindTagID(ctx, name)
	if err == nil {
		return Tag{ID: id, Name: name}, nil
	}
	var notFound *ErrTermNotFound
	if !errors.As(err, &notFound) {
		// Пустое имя и отказ площадки — не повод заводить метку: в первом случае заводить
		// нечего, во втором неизвестно, есть она там или нет.
		return Tag{}, err
	}
	id, existed, err := c.createTag(ctx, name)
	if err != nil {
		return Tag{}, err
	}
	return Tag{ID: id, Name: name, Created: !existed}, nil
}

// createTag заводит метку и возвращает её идентификатор.
//
// existed означает, что метку завёл не этот вызов: WordPress ответил term_exists и назвал
// term_id уже существующей. Для вызывающего это такой же успех, но в лог о создании такая
// метка попадать не должна.
func (c *Client) createTag(ctx context.Context, name string) (int64, bool, error) {
	wanted := normalizeTermName(name)
	body, err := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: strings.TrimSpace(name)})
	if err != nil {
		return 0, false, fmt.Errorf("собрать тело запроса %s: %w", tagsPath, err)
	}
	// Слаг не передаётся: WordPress составит его сам, и делать это за него — значит взять на
	// себя транслитерацию кириллицы, о которой площадка знает больше нас.
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
	// Имя сверяется с отправленным: площадка могла подрезать его фильтром или плагином, и
	// тогда на статью встала бы метка, которой человек не заказывал.
	if normalizeTermName(created.Name) != wanted {
		return 0, false, &ResponseError{
			Endpoint: tagsPath,
			Message: fmt.Sprintf("метка %d заведена под именем %q вместо %q",
				created.ID, created.Name, name),
		}
	}
	return created.ID, false, nil
}

// existingTermID достаёт идентификатор из отказа term_exists.
//
// WordPress отвечает так, когда метку с этим именем уже завели, и в data.term_id называет её.
// Идентификатор берётся только у этого кода: у остальных отказов в data лежит своё, и принять
// оттуда число значило бы повесить на статью произвольный термин.
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

// normalizeTermName приводит имя к сравнимому виду.
//
// Регистр не учитывается: в Excel метка записана как «Газосварщик», а в блоге могла быть
// заведена строчными. Сущности HTML разворачиваются, потому что WordPress отдаёт имена
// закодированными — «Финансы &amp; право» приезжает именно так и с сырым амперсандом из
// Excel не совпал бы.
func normalizeTermName(name string) string {
	return strings.ToLower(strings.TrimSpace(html.UnescapeString(name)))
}

// SplitTermNames разбирает список имён из колонки Excel.
//
// Разделитель — запятая, пустые элементы отбрасываются, порядок сохраняется. Повторы тоже
// отбрасываются: одна и та же метка дважды — это не две метки, а лишний идентификатор в
// запросе.
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
