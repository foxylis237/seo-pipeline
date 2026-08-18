package wordpress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
)

// mediaPath — создание вложения в медиабиблиотеке.
//
// Единственная запись, которая идёт по REST, а не через XML-RPC, и на то есть причина:
// alt и title — это собственные поля эндпоинта (alt_text, title), а не postmeta. У
// wp.uploadFile их нет вовсе, и проставлять их пришлось бы вторым, редактирующим вызовом
// по защищённому ключу _wp_attachment_image_alt — то есть тем самым способом, который у
// этого сайта и не работает (ради него запись и ушла в XML-RPC).
const mediaPath = "/wp-json/wp/v2/media"

// MaxMediaBytes — потолок файла, который пакет вообще берётся отправить.
//
// Ограничение своё, а не «пусть решит сервер»: тело запроса собирается в памяти целиком, и
// узнавать о непосильном файле по отказу, потратив на передачу весь бюджет, незачем.
//
// Значение публично: тот же потолок обязан проверять и вызывающий, который держит файл на
// диске, — иначе сухой прогон объявит статью готовой, а публикация отвалится на размере.
//
// Настоящий потолок всё равно у площадки (upload_max_filesize и post_max_size в PHP), и он
// обычно ниже этого. Наш смысл в другом: отбить заведомо неподъёмное до запроса.
const MaxMediaBytes = 32 << 20

// MediaFile — файл вместе со всем, что о нём должно быть известно библиотеке.
//
// Title и AltText приходят готовыми значениями из данных статьи и уходят тем же запросом,
// что и содержимое. Ни вычислять их после загрузки, ни дописывать вторым вызовом пакет не
// станет: у эндпоинта они первоклассные поля, и разносить создание вложения на два шага
// значило бы допустить состояние «файл есть, подписи нет».
type MediaFile struct {
	// Name — имя файла в библиотеке. Именно имя, без каталогов: WordPress раскладывает
	// загруженное по своим папкам года и месяца сам.
	Name string
	// MIMEType — тип, который WordPress сверит со своим списком разрешённых.
	MIMEType string
	// Title — подпись вложения (поле title).
	Title string
	// AltText — альтернативный текст (поле alt_text). Именно он читается вслух и попадает
	// в выдачу по картинкам, поэтому пустым он тут быть не может.
	AltText string
	// Bits — содержимое файла как есть.
	Bits []byte
}

// UploadedMedia — то, чем WordPress ответил на загрузку.
//
// AttachmentID дальше нужен ровно одному вызову — созданию записи, где он уходит обложкой.
// Ни в PostgreSQL, ни в артефактах статьи он не сохраняется: вложение без записи ценности
// не имеет, а связь «статья ⇄ запись» уже хранится в articles.wordpress_post_id.
type UploadedMedia struct {
	AttachmentID int64
	URL          string
	Type         string
	// Title и AltText — то, что вернул сам WordPress. Нужны сверке: молча отброшенное поле
	// иначе обнаружилось бы картинкой без подписи в уже опубликованной статье.
	Title   string
	AltText string
}

// UploadMedia кладёт файл в медиабиблиотеку вместе с подписью и alt.
//
// Повторов нет, по той же причине, что и у CreatePost: при обрыве после отправки неизвестно,
// дошёл ли файл, а удалять вложения пакету запрещено. Вторая попытка — это вторая копия в
// библиотеке, и решение о ней принимает человек.
//
// Ответ сверяется сразу: context=edit возвращает title и alt_text ровно теми строками,
// которые были отправлены, и расхождение здесь означает, что площадка поле не приняла.
func (c *Client) UploadMedia(ctx context.Context, file MediaFile) (UploadedMedia, error) {
	if err := file.validate(); err != nil {
		return UploadedMedia{}, err
	}
	body, contentType, err := file.multipartBody()
	if err != nil {
		return UploadedMedia{}, err
	}
	// context=edit нужен ответу, а не запросу: без него WordPress отдаёт title только
	// отрисованным, а сверять надо с тем, что отправляли.
	endpoint := c.cfg.BaseURL + mediaPath + "?context=edit"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return UploadedMedia{}, fmt.Errorf("собрать запрос %s: %w", mediaPath, err)
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/json")
	request.SetBasicAuth(c.cfg.Username, c.cfg.AppPassword)

	response, err := c.mediaClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return UploadedMedia{}, fmt.Errorf("загрузка файла %s прервана: %w", file.Name, ctxErr)
		}
		return UploadedMedia{}, &transportError{Endpoint: mediaPath, Err: err}
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return UploadedMedia{}, fmt.Errorf("прочитать ответ %s: %w", mediaPath, ctxErr)
		}
		return UploadedMedia{}, &transportError{Endpoint: mediaPath, Err: fmt.Errorf("прочитать ответ: %w", err)}
	}
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return UploadedMedia{}, newStatusError(mediaPath, response, raw, c.cfg.AppPassword)
	}
	var payload mediaPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return UploadedMedia{}, fmt.Errorf("разобрать ответ %s: %w", mediaPath, err)
	}
	media := payload.media()
	if media.AttachmentID <= 0 {
		return UploadedMedia{}, &ResponseError{
			Endpoint: mediaPath,
			Message:  "ответ без идентификатора вложения — неизвестно, загружен ли файл",
		}
	}
	if mismatches := file.verify(media); len(mismatches) > 0 {
		var report strings.Builder
		for _, mismatch := range mismatches {
			fmt.Fprintf(&report, "\n  - %s", mismatch)
		}
		return UploadedMedia{}, &ResponseError{
			Endpoint: mediaPath,
			Message: fmt.Sprintf("вложение %d создано, но данные картинки сохранились не полностью:%s",
				media.AttachmentID, report.String()),
		}
	}
	return media, nil
}

// mediaPayload — та часть ответа, которая нам нужна.
type mediaPayload struct {
	ID        int64  `json:"id"`
	SourceURL string `json:"source_url"`
	MIMEType  string `json:"mime_type"`
	AltText   string `json:"alt_text"`
	Title     struct {
		Raw      string `json:"raw"`
		Rendered string `json:"rendered"`
	} `json:"title"`
}

func (p mediaPayload) media() UploadedMedia {
	title := p.Title.Raw
	if title == "" {
		title = p.Title.Rendered
	}
	return UploadedMedia{
		AttachmentID: p.ID,
		URL:          p.SourceURL,
		Type:         p.MIMEType,
		Title:        title,
		AltText:      p.AltText,
	}
}

// verify сверяет подписи с тем, что вернула площадка.
//
// Проверяются только те поля, которые мы задавали. Смысл тот же, что у сверки записи:
// поймать молча отброшенное значение до того, как картинка окажется в опубликованной
// статье без подписи, — исправить её потом пакет не сможет.
func (f MediaFile) verify(media UploadedMedia) []Mismatch {
	var mismatches []Mismatch
	if f.AltText != media.AltText {
		mismatches = append(mismatches, Mismatch{Field: "alt_text", Expected: f.AltText, Actual: media.AltText})
	}
	if f.Title != media.Title {
		mismatches = append(mismatches, Mismatch{Field: "title", Expected: f.Title, Actual: media.Title})
	}
	return mismatches
}

// multipartBody собирает тело запроса: файл и подписи одной формой.
//
// Форма, а не сырое тело с Content-Disposition: с сырым телом title и alt_text пришлось бы
// класть в строку запроса, где они видны в логах веб-сервера и упираются в его лимит длины.
func (f MediaFile) multipartBody() ([]byte, string, error) {
	var buffer bytes.Buffer
	// Ёмкость под файл резервируется заранее: без этого буфер переедет с десяток раз,
	// каждый раз копируя мегабайты.
	buffer.Grow(len(f.Bits) + 1024)
	form := multipart.NewWriter(&buffer)

	// Заголовки части с файлом задаются вручную: CreateFormFile проставил бы
	// application/octet-stream, а WordPress сверяет присланный тип с расширением.
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="file"; filename=%q`, f.Name))
	header.Set("Content-Type", f.MIMEType)
	part, err := form.CreatePart(header)
	if err != nil {
		return nil, "", fmt.Errorf("собрать форму загрузки файла %s: %w", f.Name, err)
	}
	if _, err := part.Write(f.Bits); err != nil {
		return nil, "", fmt.Errorf("собрать форму загрузки файла %s: %w", f.Name, err)
	}
	for _, field := range []struct{ name, value string }{
		{"title", f.Title},
		{"alt_text", f.AltText},
	} {
		if err := form.WriteField(field.name, field.value); err != nil {
			return nil, "", fmt.Errorf("собрать форму загрузки файла %s: %w", f.Name, err)
		}
	}
	if err := form.Close(); err != nil {
		return nil, "", fmt.Errorf("собрать форму загрузки файла %s: %w", f.Name, err)
	}
	return buffer.Bytes(), form.FormDataContentType(), nil
}

// validate отбивает непригодный файл до запроса.
//
// Проверка структурная: годится ли эта картинка статье, решает вызывающий. Здесь ловится
// только то, из чего WordPress сделал бы вложение, которое мы не сможем ни исправить, ни
// удалить, — и то, что заведомо не дойдёт.
func (f MediaFile) validate() error {
	name := strings.TrimSpace(f.Name)
	if name == "" {
		return errors.New("WordPress: имя файла обложки пусто")
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("WordPress: имя файла обложки содержит каталог: %q", name)
	}
	if strings.TrimSpace(f.MIMEType) == "" {
		return fmt.Errorf("WordPress: не задан тип файла обложки %q", name)
	}
	// Пустые подписи не «просто пустые»: WordPress подставит вместо title имя файла, alt
	// останется пустым, и картинка уедет в статью без альтернативного текста. Молча
	// допускать это нельзя — ради этих двух полей загрузка и идёт по REST.
	if strings.TrimSpace(f.Title) == "" {
		return fmt.Errorf("WordPress: у обложки %q пустой title", name)
	}
	if strings.TrimSpace(f.AltText) == "" {
		return fmt.Errorf("WordPress: у обложки %q пустой alt", name)
	}
	if len(f.Bits) == 0 {
		return fmt.Errorf("WordPress: файл обложки %q пуст", name)
	}
	if len(f.Bits) > MaxMediaBytes {
		return fmt.Errorf("WordPress: файл обложки %q весит %.1f МБ при потолке %d МБ — сожмите картинку",
			name, float64(len(f.Bits))/(1<<20), MaxMediaBytes>>20)
	}
	return nil
}
