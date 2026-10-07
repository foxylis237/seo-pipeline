package deepseekweb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

const (
	// attachmentUploadTimeout — ожидание карточки документа над полем ввода; загрузка PDF дольше обычного действия.
	attachmentUploadTimeout = 2 * time.Minute
	// attachmentMarkerRunes — сколько первых символов имени файла искать на странице:
	// длинное имя интерфейс обрезает многоточием.
	attachmentMarkerRunes = 8
)

// SupportsAttachments сообщает роутеру, что провайдер умеет отправить документ вместе с промптом.
func (c *Client) SupportsAttachments() bool { return true }

// applyMode переключает интерфейс в режим стадии; переключатель есть только на экране нового чата.
// Ненайденный переключатель стадию не роняет — предупреждение и снимок страницы.
// Переключение двустороннее: профиль браузера хранит DeepThink с прошлой стадии.
func (c *Client) applyMode(page playwright.Page, request llm.Request, newChat bool) {
	mode := strings.TrimSpace(request.Mode)
	if mode == "" || !newChat {
		return
	}
	value, err := page.Evaluate(selectModeJS, map[string]any{
		"mode":            mode,
		"modeSelector":    modeSelector,
		"toggleSelector":  searchToggleSelector,
		"reasoningLabels": reasoningLabels,
	})
	if err != nil {
		c.logger.Warn("DeepSeek mode was not switched", "mode", mode, "error", err)
		return
	}
	result, _ := value.(string)
	switch result {
	case "clicked", "already":
		c.stage("select_mode", "mode", mode, "result", result)
	default:
		c.saveDiagnostics(page, "select_mode", request.ArticleID)
		c.logger.Warn("DeepSeek mode switch was not found on the page", "mode", mode, "result", result)
	}
}

// applySearch включает поиск в интернете на новом чате, если стадия его просит;
// ненайденный тумблер стадию не роняет.
func (c *Client) applySearch(page playwright.Page, request llm.Request, newChat bool) {
	if !request.Search || !newChat {
		return
	}
	value, err := page.Evaluate(toggleSearchJS, map[string]any{
		"selector": searchToggleSelector,
		"labels":   searchLabels,
	})
	if err != nil {
		c.logger.Warn("DeepSeek search was not switched on", "error", err)
		return
	}
	result, _ := value.(string)
	switch result {
	case "clicked", "already":
		c.stage("enable_search", "result", result)
	default:
		c.saveDiagnostics(page, "enable_search", request.ArticleID)
		c.logger.Warn("DeepSeek search toggle was not found on the page", "result", result)
	}
}

// attachDocuments прикрепляет документы стадии к отправляемому сообщению; недоступный документ — отказ.
func (c *Client) attachDocuments(ctx context.Context, page playwright.Page, request llm.Request) error {
	if len(request.Attachments) == 0 {
		return nil
	}
	paths, markers, err := attachmentPaths(request.Attachments)
	if err != nil {
		return missingAttachmentError(err)
	}
	timeout := operationTimeout(ctx, defaultOperationTimeout)
	input := page.Locator(fileInputSelector).First()
	// Поле загрузки скрыто, поэтому ждём появления в DOM, а не видимости.
	if err := input.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateAttached, Timeout: playwright.Float(timeout),
	}); err != nil {
		c.saveDiagnostics(page, "attach_document", request.ArticleID)
		return c.browserError(ctx, "find DeepSeek attachment input", err)
	}
	if err := input.SetInputFiles(paths, playwright.LocatorSetInputFilesOptions{
		Timeout: playwright.Float(timeout),
	}); err != nil {
		c.saveDiagnostics(page, "attach_document", request.ArticleID)
		return c.browserError(ctx, "attach DeepSeek document", err)
	}
	c.stage("attach_document", "documents", len(paths), "names", strings.Join(documentNames(paths), ", "))
	return c.waitForAttachments(ctx, page, request, markers)
}

// waitForAttachments ждёт карточки документов над полем ввода: во время загрузки DeepSeek
// держит отправку заблокированной.
func (c *Client) waitForAttachments(ctx context.Context, page playwright.Page, request llm.Request, markers []string) error {
	started := time.Now()
	_, err := page.WaitForFunction(attachmentsReadyJS, map[string]any{"markers": markers},
		playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(operationTimeout(ctx, attachmentUploadTimeout))})
	if err != nil {
		c.saveDiagnostics(page, "attachment_upload", request.ArticleID)
		return c.browserError(ctx, "wait for DeepSeek attachment upload", err)
	}
	c.stage("attachment_uploaded", "duration_ms", time.Since(started).Milliseconds(), "documents", len(markers))
	return nil
}

// attachmentPaths приводит пути к абсолютным и готовит признаки документов на странице.
func attachmentPaths(attachments []string) (paths, markers []string, err error) {
	paths = make([]string, 0, len(attachments))
	markers = make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		absolute, err := filepath.Abs(strings.TrimSpace(attachment))
		if err != nil {
			return nil, nil, fmt.Errorf("путь документа %q: %w", attachment, err)
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return nil, nil, fmt.Errorf("документ %q: %w", absolute, err)
		}
		if info.IsDir() {
			return nil, nil, fmt.Errorf("документ %q: это каталог", absolute)
		}
		paths = append(paths, absolute)
		markers = append(markers, attachmentMarker(filepath.Base(absolute)))
	}
	return paths, markers, nil
}

// attachmentMarker — начало имени файла в том виде, в каком его показывает страница.
func attachmentMarker(name string) string {
	name = strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
	name = strings.Join(strings.Fields(name), " ")
	if runes := []rune(name); len(runes) > attachmentMarkerRunes {
		return string(runes[:attachmentMarkerRunes])
	}
	return name
}

// missingAttachmentError — окончательная ошибка: повтор и смена провайдера не помогут.
func missingAttachmentError(err error) error {
	return &llm.StatusError{
		Code: 400, Type: llm.ErrorTypeProvider,
		Message: "deepseek_attachment_unavailable", Err: err,
	}
}

func documentNames(paths []string) []string {
	names := make([]string, 0, len(paths))
	for _, path := range paths {
		names = append(names, filepath.Base(path))
	}
	return names
}
