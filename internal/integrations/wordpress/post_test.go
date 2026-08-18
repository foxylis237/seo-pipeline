package wordpress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// articleHTML воспроизводит то, что действительно уходит в WordPress: разметка стадии html
// с кавычками, амперсандами, угловыми скобками и кириллицей. Экранирование ломается именно
// на таком тексте, а не на «hello world».
const articleHTML = `<p class="ds-markdown-paragraph"><span class="">Разряды 2 & 6 — «сварка» <b>труб</b></span></p>
<p>Второй абзац</p>`

func samplePayload() PostPayload {
	return PostPayload{
		Title:       "Разряды газосварщиков: категории, обязанности и зарплата",
		ContentHTML: articleHTML,
		Status:      PostStatusPublish,
		CategoryID:  2575,
		TagIDs:      []int64{1801, 1251, 49416},
		ThumbnailID: 21610,
		Fields: []CustomField{
			{Key: "blog_tldr", Value: "От разряда зависит сложность работ."},
			{Key: "blog_read", Value: "9 мин"},
			{Key: "blog_faq", Value: "2"},
			{Key: "blog_faq_0_question", Value: "Сколько разрядов?"},
			{Key: "blog_faq_0_answer", Value: "Пять — со 2-го по 6-й."},
			{Key: "blog_faq_1_question", Value: "Где работает?"},
			{Key: "blog_faq_1_answer", Value: "В нефтегазовой отрасли."},
			{Key: "prof_title", Value: "Разряды газосварщиков: какие бывают"},
			{Key: "prof_blue", Value: "от 7 000 р"},
			{Key: "prof_name", Value: "газосварщик"},
			{Key: "_yoast_wpseo_focuskw", Value: "разряды газосварщиков"},
			{Key: "_yoast_wpseo_title", Value: "Разряды газосварщиков"},
			{Key: "_yoast_wpseo_metadesc", Value: "Какие категории существуют."},
		},
	}
}

func methodResponse(body string) string {
	return `<?xml version="1.0"?><methodResponse><params><param>` + body + `</param></params></methodResponse>`
}

func TestCreatePostSendsEverythingInOneCall(t *testing.T) {
	var requests int
	var body, contentType string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		contentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, methodResponse(`<value><string>21602</string></value>`))
	})

	postID, err := client.CreatePost(context.Background(), samplePayload())
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if postID != 21602 {
		t.Fatalf("идентификатор записи = %d, ожидался 21602", postID)
	}
	if requests != 1 {
		t.Fatalf("запросов %d, публикация обязана укладываться ровно в один", requests)
	}
	if !strings.HasPrefix(contentType, "text/xml") {
		t.Fatalf("Content-Type = %q, ожидался text/xml", contentType)
	}
	for _, want := range []string{
		"<methodName>wp.newPost</methodName>",
		"<name>post_type</name><value><string>post</string></value>",
		"<name>post_status</name><value><string>publish</string></value>",
		"<name>category</name><value><array><data><value><int>2575</int></value></data></array></value>",
		"<int>49416</int>",
		// Пары custom_fields — это структуры {key, value}, а не именованные поля:
		// имя ACF-поля едет значением, и репитер уходит плоскими ключами.
		"<name>key</name><value><string>blog_faq</string></value>",
		"<name>key</name><value><string>blog_faq_1_answer</string></value>",
		"<name>key</name><value><string>_yoast_wpseo_focuskw</string></value>",
		"<name>key</name><value><string>prof_name</string></value>",
		"<name>value</name><value><string>газосварщик</string></value>",
		// Обложка уходит идентификатором уже загруженного вложения: другого способа
		// назначить её у wp.newPost нет, а дописать её вторым запросом нельзя.
		"<name>post_thumbnail</name><value><int>21610</int></value>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("в теле вызова нет %q", want)
		}
	}
	// Разметка обязана уйти экранированной, а не сырой: сырой знак < оборвал бы разбор XML
	// на стороне WordPress.
	if strings.Contains(body, `<p class="ds-markdown-paragraph">`) {
		t.Error("HTML статьи ушёл неэкранированным")
	}
	if !strings.Contains(body, "&lt;p class=&#34;ds-markdown-paragraph&#34;&gt;") {
		t.Error("HTML статьи не найден в экранированном виде")
	}
}

// Пустой post_thumbnail WordPress понимает как «снять обложку». У новой записи снимать
// нечего, и поля в запросе быть не должно вовсе.
func TestCreatePostOmitsThumbnailWhenNotSet(t *testing.T) {
	var body string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		fmt.Fprint(w, methodResponse(`<value><string>21602</string></value>`))
	})

	payload := samplePayload()
	payload.ThumbnailID = 0
	if _, err := client.CreatePost(context.Background(), payload); err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	if strings.Contains(body, "post_thumbnail") {
		t.Error("в вызове есть post_thumbnail, хотя обложка не назначена")
	}
}

func TestCreatePostNeverRetries(t *testing.T) {
	// Отказ намеренно повторяемый по меркам REST: 500 и 429 там уходят на вторую попытку.
	// У записи второй попытки нет ни при каком коде — иначе в блоге появится второй пост,
	// удалить который пакет не умеет.
	for _, status := range []int{http.StatusInternalServerError, http.StatusTooManyRequests, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests int
			client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(status)
				fmt.Fprint(w, `{"code":"internal","message":"сервер прилёг"}`)
			})
			if _, err := client.CreatePost(context.Background(), samplePayload()); err == nil {
				t.Fatal("ожидалась ошибка")
			}
			if requests != 1 {
				t.Fatalf("запросов %d, повтор записи запрещён", requests)
			}
		})
	}
}

func TestCreatePostReturnsFaultAsTypedError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><methodResponse><fault><value><struct>`+
			`<member><name>faultCode</name><value><int>401</int></value></member>`+
			`<member><name>faultString</name><value><string>Неверное имя пользователя или пароль.</string></value></member>`+
			`</struct></value></fault></methodResponse>`)
	})

	_, err := client.CreatePost(context.Background(), samplePayload())
	var faultErr *FaultError
	if !errors.As(err, &faultErr) {
		t.Fatalf("ошибка %v не разбирается как *FaultError", err)
	}
	if faultErr.Code != 401 {
		t.Fatalf("faultCode = %d, ожидался 401", faultErr.Code)
	}
}

func TestCreatePostKeepsPasswordOutOfFaultMessage(t *testing.T) {
	// Чужой сервер волен вернуть в сообщении что угодно, включая присланный пароль.
	// В нашу ошибку и наш лог он попасть не имеет права.
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><methodResponse><fault><value><struct>`+
			`<member><name>faultCode</name><value><int>403</int></value></member>`+
			`<member><name>faultString</name><value><string>Отказано для `+testAppPassword+`</string></value></member>`+
			`</struct></value></fault></methodResponse>`)
	})

	_, err := client.CreatePost(context.Background(), samplePayload())
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if strings.Contains(err.Error(), testAppPassword) {
		t.Fatalf("пароль утёк в текст ошибки: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Fatalf("секрет не заменён плейсхолдером: %v", err)
	}
}

func TestCreatePostRejectsBrokenPayloadWithoutRequest(t *testing.T) {
	cases := map[string]func(*PostPayload){
		"пустой заголовок":        func(p *PostPayload) { p.Title = "  " },
		"пустое тело":             func(p *PostPayload) { p.ContentHTML = "" },
		"недопустимый статус":     func(p *PostPayload) { p.Status = "future" },
		"рубрика не разрешена":    func(p *PostPayload) { p.CategoryID = 0 },
		"метки не разрешены":      func(p *PostPayload) { p.TagIDs = nil },
		"нулевой идентификатор":   func(p *PostPayload) { p.TagIDs = []int64{1801, 0} },
		"пустое имя поля":         func(p *PostPayload) { p.Fields[0].Key = "" },
		"повторяющийся ключ поля": func(p *PostPayload) { p.Fields[1].Key = p.Fields[0].Key },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var requests int
			client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
			})
			payload := samplePayload()
			mutate(&payload)
			if _, err := client.CreatePost(context.Background(), payload); err == nil {
				t.Fatal("ожидалась ошибка проверки")
			}
			if requests != 0 {
				t.Fatalf("запросов %d, непригодная нагрузка не должна уходить в сеть", requests)
			}
		})
	}
}

func TestGetPostReadsTermsAndCustomFields(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), "<methodName>wp.getPost</methodName>") {
			t.Errorf("вызван не wp.getPost: %s", truncate(string(raw), 120))
		}
		fmt.Fprint(w, methodResponse(`<value><struct>`+
			`<member><name>post_id</name><value><string>21602</string></value></member>`+
			`<member><name>post_title</name><value><string>Заголовок</string></value></member>`+
			`<member><name>post_status</name><value><string>draft</string></value></member>`+
			`<member><name>post_content</name><value><string>&lt;p&gt;тело&lt;/p&gt;</string></value></member>`+
			`<member><name>link</name><value><string>https://example.test/blog/post/</string></value></member>`+
			`<member><name>terms</name><value><array><data>`+
			`<value><struct><member><name>term_id</name><value><string>2575</string></value></member>`+
			`<member><name>taxonomy</name><value><string>category</string></value></member></struct></value>`+
			`<value><struct><member><name>term_id</name><value><string>1801</string></value></member>`+
			`<member><name>taxonomy</name><value><string>post_tag</string></value></member></struct></value>`+
			`</data></array></value></member>`+
			`<member><name>post_thumbnail</name><value><struct>`+
			`<member><name>attachment_id</name><value><string>21610</string></value></member>`+
			`<member><name>link</name><value><string>https://example.test/cover.webp</string></value></member>`+
			`</struct></value></member>`+
			`<member><name>custom_fields</name><value><array><data>`+
			`<value><struct><member><name>key</name><value><string>blog_read</string></value></member>`+
			`<member><name>value</name><value><string>9 мин</string></value></member></struct></value>`+
			`</data></array></value></member>`+
			`</struct></value>`))
	})

	post, err := client.GetPost(context.Background(), 21602)
	if err != nil {
		t.Fatalf("GetPost: %v", err)
	}
	if post.ID != 21602 || post.Status != "draft" {
		t.Fatalf("разобрано %+v", post)
	}
	if post.ContentHTML != "<p>тело</p>" {
		t.Fatalf("тело записи = %q", post.ContentHTML)
	}
	if post.Link != "https://example.test/blog/post/" {
		t.Fatalf("адрес записи = %q", post.Link)
	}
	if len(post.CategoryIDs) != 1 || post.CategoryIDs[0] != 2575 {
		t.Fatalf("рубрики = %v", post.CategoryIDs)
	}
	if len(post.TagIDs) != 1 || post.TagIDs[0] != 1801 {
		t.Fatalf("метки = %v", post.TagIDs)
	}
	if post.Fields["blog_read"] != "9 мин" {
		t.Fatalf("custom_fields = %v", post.Fields)
	}
	// Обложка приезжает целым описанием вложения, а сверять её надо идентификатором.
	if post.ThumbnailID != 21610 {
		t.Fatalf("обложка = %d, ожидалось 21610", post.ThumbnailID)
	}
}

// Запись без обложки: на месте post_thumbnail пустой массив, и это не повод считать
// идентификатор мусорным.
func TestGetPostReadsMissingThumbnailAsZero(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, methodResponse(`<value><struct>`+
			`<member><name>post_id</name><value><string>21602</string></value></member>`+
			`<member><name>post_thumbnail</name><value><array><data></data></array></value></member>`+
			`</struct></value>`))
	})

	post, err := client.GetPost(context.Background(), 21602)
	if err != nil {
		t.Fatalf("GetPost: %v", err)
	}
	if post.ThumbnailID != 0 {
		t.Fatalf("обложка = %d, ожидался ноль", post.ThumbnailID)
	}
}

func TestVerifyPassesWhenWordPressKeptEverything(t *testing.T) {
	payload := samplePayload()
	stored := StoredPost{
		Title:       payload.Title,
		Status:      payload.Status,
		ContentHTML: payload.ContentHTML,
		CategoryIDs: []int64{payload.CategoryID},
		// Порядок меток WordPress не сохраняет — сверка обязана это переживать.
		TagIDs:      []int64{49416, 1801, 1251},
		ThumbnailID: payload.ThumbnailID,
		Fields:      map[string]string{},
	}
	for _, field := range payload.Fields {
		stored.Fields[field.Key] = field.Value
	}
	if mismatches := payload.Verify(stored); len(mismatches) != 0 {
		t.Fatalf("сверка нашла расхождения там, где их нет: %v", mismatches)
	}
}

func TestVerifyCatchesSilentlyDroppedField(t *testing.T) {
	payload := samplePayload()
	stored := StoredPost{
		Title:       payload.Title,
		Status:      payload.Status,
		ContentHTML: payload.ContentHTML,
		CategoryIDs: []int64{payload.CategoryID},
		TagIDs:      payload.TagIDs,
		ThumbnailID: payload.ThumbnailID,
		Fields:      map[string]string{},
	}
	for _, field := range payload.Fields {
		stored.Fields[field.Key] = field.Value
	}
	// Ровно тот отказ, ради которого сверка и существует: WordPress молча не принял ключ,
	// ответив при этом успехом.
	delete(stored.Fields, "_yoast_wpseo_focuskw")
	stored.Fields["blog_tldr"] = "не то, что отправляли"

	mismatches := payload.Verify(stored)
	if len(mismatches) != 2 {
		t.Fatalf("расхождений %d, ожидалось 2: %v", len(mismatches), mismatches)
	}
	found := map[string]bool{}
	for _, mismatch := range mismatches {
		found[mismatch.Field] = true
	}
	if !found["_yoast_wpseo_focuskw"] || !found["blog_tldr"] {
		t.Fatalf("не те поля: %v", mismatches)
	}
}

// Запись без обложки в блоге видна сразу, а дописать картинку вторым запросом нельзя:
// редактирование записей запрещено. Значит, потерянная обложка обязана быть расхождением.
func TestVerifyCatchesLostThumbnail(t *testing.T) {
	payload := samplePayload()
	stored := StoredPost{
		Title:       payload.Title,
		Status:      payload.Status,
		ContentHTML: payload.ContentHTML,
		CategoryIDs: []int64{payload.CategoryID},
		TagIDs:      payload.TagIDs,
		Fields:      map[string]string{},
	}
	for _, field := range payload.Fields {
		stored.Fields[field.Key] = field.Value
	}
	mismatches := payload.Verify(stored)
	if len(mismatches) != 1 || mismatches[0].Field != "post_thumbnail" {
		t.Fatalf("расхождения = %v, ожидалось одно по обложке", mismatches)
	}
}

func TestVerifyCatchesChangedTermsAndStatus(t *testing.T) {
	payload := samplePayload()
	stored := StoredPost{
		Title:       payload.Title,
		Status:      PostStatusDraft,
		ContentHTML: payload.ContentHTML,
		CategoryIDs: []int64{506},
		TagIDs:      []int64{1801},
		ThumbnailID: payload.ThumbnailID,
		Fields:      map[string]string{},
	}
	for _, field := range payload.Fields {
		stored.Fields[field.Key] = field.Value
	}
	mismatches := payload.Verify(stored)
	if len(mismatches) != 3 {
		t.Fatalf("расхождений %d, ожидалось 3 (status, category, post_tag): %v", len(mismatches), mismatches)
	}
}
