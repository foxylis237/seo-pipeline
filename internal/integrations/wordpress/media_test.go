package wordpress

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

// coverBits — содержимое обложки. Байты намеренно не текстовые: файл обязан доехать
// побайтово, и на «hello world» подмена кодировки была бы не видна.
var coverBits = []byte{0x52, 0x49, 0x46, 0x46, 0x00, 0xFF, 0x1A, 0x00, 0x57, 0x45, 0x42, 0x50}

const (
	coverAlt   = "Разряды сантехника: какие бывают и чем отличаются"
	coverTitle = "razryady-santekhnika"
)

func sampleMediaFile() MediaFile {
	return MediaFile{
		Name:     "razryady-santekhnika.webp",
		MIMEType: "image/webp",
		Title:    coverTitle,
		AltText:  coverAlt,
		Bits:     coverBits,
	}
}

// mediaResponse воспроизводит ответ эндпоинта в context=edit: там title приезжает и raw,
// то есть ровно той строкой, которую отправляли.
func mediaResponse(id int, alt, titleRaw string) string {
	return fmt.Sprintf(`{"id":%d,"source_url":"https://example.test/wp-content/uploads/2026/08/razryady-santekhnika.webp",
		"mime_type":"image/webp","alt_text":%q,"title":{"raw":%q,"rendered":%q}}`, id, alt, titleRaw, titleRaw)
}

// readMultipart разбирает то, что действительно ушло на сервер.
func readMultipart(t *testing.T, r *http.Request) (map[string]string, []byte, string, string) {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("Content-Type = %q: %v", r.Header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(r.Body, params["boundary"])
	fields := map[string]string{}
	var fileBits []byte
	var fileName, fileType string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("разбор формы: %v", err)
		}
		content, _ := io.ReadAll(part)
		if part.FormName() == "file" {
			fileBits, fileName, fileType = content, part.FileName(), part.Header.Get("Content-Type")
			continue
		}
		fields[part.FormName()] = string(content)
	}
	return fields, fileBits, fileName, fileType
}

func TestUploadMediaSendsFileWithTitleAndAltInOneCall(t *testing.T) {
	var requests int
	var method, path, query, authorization string
	var fields map[string]string
	var fileBits []byte
	var fileName, fileType string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		method, path, query = r.Method, r.URL.Path, r.URL.RawQuery
		authorization = r.Header.Get("Authorization")
		fields, fileBits, fileName, fileType = readMultipart(t, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, mediaResponse(21610, coverAlt, coverTitle))
	})

	media, err := client.UploadMedia(context.Background(), sampleMediaFile())
	if err != nil {
		t.Fatalf("UploadMedia: %v", err)
	}
	// Файл и обе подписи уходят одним запросом: состояния «файл есть, подписи нет» быть
	// не должно, а дописать их вторым вызовом пакет не умеет.
	if requests != 1 {
		t.Fatalf("запросов %d, загрузка обязана укладываться ровно в один", requests)
	}
	if method != http.MethodPost || path != mediaPath {
		t.Fatalf("запрос = %s %s", method, path)
	}
	// context=edit нужен ответу: без него title возвращается только отрисованным, и сверять
	// его было бы не с чем.
	if query != "context=edit" {
		t.Fatalf("query = %q", query)
	}
	if !strings.HasPrefix(authorization, "Basic ") {
		t.Fatalf("Authorization = %q", authorization)
	}
	if !bytes.Equal(fileBits, coverBits) {
		t.Fatalf("содержимое файла изменено: %v", fileBits)
	}
	if fileName != "razryady-santekhnika.webp" || fileType != "image/webp" {
		t.Fatalf("файл ушёл как %q (%s)", fileName, fileType)
	}
	if fields["alt_text"] != coverAlt {
		t.Fatalf("alt_text = %q", fields["alt_text"])
	}
	if fields["title"] != coverTitle {
		t.Fatalf("title = %q", fields["title"])
	}
	if media.AttachmentID != 21610 {
		t.Fatalf("идентификатор вложения = %d", media.AttachmentID)
	}
	if media.AltText != coverAlt || media.Title != coverTitle {
		t.Fatalf("подписи из ответа = %+v", media)
	}
	if !strings.HasSuffix(media.URL, "razryady-santekhnika.webp") {
		t.Fatalf("адрес вложения = %q", media.URL)
	}
}

// Молча отброшенная подпись — это картинка без альтернативного текста в уже опубликованной
// статье, а исправить её пакет не сможет. Значит, расхождение обязано быть отказом.
func TestUploadMediaFailsWhenSiteDroppedSignatures(t *testing.T) {
	cases := map[string]struct {
		alt, title, want string
	}{
		"alt потерян":    {"", coverTitle, "alt_text"},
		"title подменён": {coverAlt, "razryady-santekhnika.webp", "title"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, mediaResponse(21610, testCase.alt, testCase.title))
			})

			_, err := client.UploadMedia(context.Background(), sampleMediaFile())
			if err == nil {
				t.Fatal("потерянная подпись принята за успех")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("ошибка не называет поле %s: %v", testCase.want, err)
			}
			// Вложение уже создано, и человеку надо знать его номер: удалять лишнее пакету
			// запрещено, разбираться придётся в админке.
			if !strings.Contains(err.Error(), "21610") {
				t.Fatalf("ошибка не называет вложение: %v", err)
			}
		})
	}
}

func TestUploadMediaDoesNotRetry(t *testing.T) {
	var requests int
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"code":"internal","message":"сервер прилёг"}`)
	})

	if _, err := client.UploadMedia(context.Background(), sampleMediaFile()); err == nil {
		t.Fatal("ожидался отказ")
	}
	// Повтор — это вторая копия в библиотеке, а удалять вложения пакету запрещено.
	if requests != 1 {
		t.Fatalf("попыток %d, повтор загрузки запрещён", requests)
	}
}

func TestUploadMediaReportsRefusalWithCode(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		fmt.Fprint(w, `{"code":"rest_upload_file_too_big","message":"Файл превышает допустимый размер."}`)
	})

	_, err := client.UploadMedia(context.Background(), sampleMediaFile())
	if err == nil {
		t.Fatal("ожидался отказ")
	}
	if !strings.Contains(err.Error(), "rest_upload_file_too_big") {
		t.Fatalf("ошибка не доносит код площадки: %v", err)
	}
}

func TestUploadMediaRejectsUnusableFileBeforeRequest(t *testing.T) {
	cases := map[string]func(*MediaFile){
		"без имени":       func(f *MediaFile) { f.Name = " " },
		"имя с каталогом": func(f *MediaFile) { f.Name = "images/cover.webp" },
		"без типа":        func(f *MediaFile) { f.MIMEType = "" },
		"пустой title":    func(f *MediaFile) { f.Title = "  " },
		"пустой alt":      func(f *MediaFile) { f.AltText = "" },
		"пустой файл":     func(f *MediaFile) { f.Bits = nil },
		"слишком тяжёлый": func(f *MediaFile) { f.Bits = bytes.Repeat([]byte{1}, MaxMediaBytes+1) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var requests int
			client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, mediaResponse(21610, coverAlt, coverTitle))
			})
			file := sampleMediaFile()
			mutate(&file)

			if _, err := client.UploadMedia(context.Background(), file); err == nil {
				t.Fatal("непригодный файл принят")
			}
			if requests != 0 {
				t.Fatalf("запросов %d, непригодный файл обязан отбиваться до отправки", requests)
			}
		})
	}
}

func TestUploadMediaRejectsResponseWithoutID(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"source_url":"https://example.test/cover.webp"}`)
	})

	if _, err := client.UploadMedia(context.Background(), sampleMediaFile()); err == nil {
		t.Fatal("ответ без идентификатора вложения принят за успех")
	}
}
