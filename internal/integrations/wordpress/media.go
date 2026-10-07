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

// mediaPath — создание вложения; единственная запись по REST: у wp.uploadFile нет alt и title,
// а защищённый ключ _wp_attachment_image_alt через XML-RPC не записать.
const mediaPath = "/wp-json/wp/v2/media"

// MaxMediaBytes — потолок файла, который пакет берётся отправить (тело собирается в памяти).
// Его же проверяет сухой прогон; настоящий потолок площадки (upload_max_filesize) обычно ниже.
const MaxMediaBytes = 32 << 20

// MediaFile — файл с подписью и alt, которые уходят тем же запросом, что и содержимое.
type MediaFile struct {
	// Name — имя файла без каталогов.
	Name string
	// MIMEType — тип, который WordPress сверит со своим списком разрешённых.
	MIMEType string
	Title    string
	AltText  string
	Bits     []byte
}

// UploadedMedia — то, чем WordPress ответил на загрузку.
type UploadedMedia struct {
	AttachmentID int64
	URL          string
	Type         string
	// Title и AltText — то, что вернул сам WordPress, для сверки.
	Title   string
	AltText string
}

// UploadMedia кладёт файл в медиабиблиотеку вместе с подписью и alt и сверяет ответ.
// Повторов нет: вторая попытка после обрыва дала бы вторую копию в библиотеке.
func (c *Client) UploadMedia(ctx context.Context, file MediaFile) (UploadedMedia, error) {
	if err := file.validate(); err != nil {
		return UploadedMedia{}, err
	}
	body, contentType, err := file.multipartBody()
	if err != nil {
		return UploadedMedia{}, err
	}
	// Без context=edit WordPress отдаёт title только отрисованным.
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

// multipartBody собирает файл и подписи одной формой: иначе подписи ушли бы в строку запроса.
func (f MediaFile) multipartBody() ([]byte, string, error) {
	var buffer bytes.Buffer
	buffer.Grow(len(f.Bits) + 1024)
	form := multipart.NewWriter(&buffer)

	// Не CreateFormFile: тот ставит application/octet-stream, а WordPress сверяет тип с расширением.
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

// validate отбивает структурно непригодный файл до запроса.
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
	// Пустой title WordPress заменяет именем файла.
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
