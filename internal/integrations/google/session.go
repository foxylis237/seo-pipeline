package google

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/mxschmitt/playwright-go"
)

// playwrightSession реализует Session поверх живого браузера.
type playwrightSession struct {
	cfg      Config
	folderID string
	session  *browserSession
	logger   *slog.Logger
	// articleID нужен только диагностике, чтобы дампы ложились по каталогам статей.
	articleID int64
}

// NewSessionFactory возвращает фабрику сессий для публикатора.
//
// Профиль проверяется до запуска браузера: отсутствие входа — отдельная причина отказа,
// которую повтор не лечит, и поднимать ради неё Chromium незачем.
func NewSessionFactory(cfg Config, logger *slog.Logger, articleID int64) SessionFactory {
	return func(ctx context.Context) (Session, error) {
		if err := cfg.Validate(); err != nil {
			return nil, &StageError{Stage: "validate_config", Retryable: false, Err: err}
		}
		if !ProfileExists(cfg.ProfileDir) {
			return nil, &StageError{Stage: "open_session", Retryable: false, Err: ErrLoginRequired}
		}
		folderID, err := FolderID(cfg.FolderURL)
		if err != nil {
			return nil, &StageError{Stage: "validate_config", Retryable: false, Err: err}
		}
		session, err := launchBrowser(cfg.ProfileDir, cfg.Headless, cfg.Channel)
		if err != nil {
			return nil, &StageError{Stage: "open_session", Retryable: !NeedsManualLogin(err) && !isProfileBusy(err), Err: err}
		}
		return &playwrightSession{
			cfg: cfg, folderID: folderID, session: session,
			logger: logger, articleID: articleID,
		}, nil
	}
}

func isProfileBusy(err error) bool {
	return err != nil && strings.Contains(err.Error(), ErrProfileBusy.Error())
}

func (s *playwrightSession) Close() error { return s.session.close() }

// FindDocument ищет документ по точному имени внутри папки.
//
// Поиск идёт запросом в адресе, а не набором в поле: так не нужно ждать выпадающую подсказку
// и бороться с автодополнением. Совпадение проверяется по точному имени — Drive возвращает и
// частичные, а перезаписать чужой документ хуже, чем создать новый.
func (s *playwrightSession) FindDocument(ctx context.Context, title string) (string, bool, error) {
	address := fmt.Sprintf(searchInFolderURLTemplate, url.QueryEscape(title), s.folderID)
	if err := s.goTo(ctx, address, "find_document"); err != nil {
		return "", false, err
	}
	if err := pause(ctx); err != nil {
		return "", false, err
	}

	rows, err := s.session.page.Locator(driveSearchResultRow).All()
	if err != nil {
		return "", false, s.fail(ctx, "find_document", true, fmt.Errorf("прочитать результаты поиска: %w", err))
	}
	for _, row := range rows {
		name, nameErr := rowTitle(row)
		if nameErr != nil {
			continue
		}
		if strings.TrimSpace(name) != strings.TrimSpace(title) {
			continue
		}
		id, idErr := row.GetAttribute("data-id")
		if idErr != nil || strings.TrimSpace(id) == "" {
			continue
		}
		return "https://docs.google.com/document/d/" + id + "/edit", true, nil
	}
	return "", false, nil
}

// rowTitle достаёт имя файла из строки результата.
func rowTitle(row playwright.Locator) (string, error) {
	if value, err := row.GetAttribute("aria-label"); err == nil && strings.TrimSpace(value) != "" {
		return value, nil
	}
	name := row.Locator(driveSearchResultName).First()
	if value, err := name.GetAttribute("data-tooltip"); err == nil && strings.TrimSpace(value) != "" {
		return value, nil
	}
	return name.InnerText()
}

// CreateDocument создаёт документ сразу в нужной папке и заполняет его.
func (s *playwrightSession) CreateDocument(ctx context.Context, title, body string) (string, error) {
	address := fmt.Sprintf(createDocumentURLTemplate, s.folderID)
	if err := s.goTo(ctx, address, "create_document"); err != nil {
		return "", err
	}
	if err := s.waitFor(ctx, documentCanvas, "create_document"); err != nil {
		return "", err
	}
	if err := s.renameDocument(ctx, title); err != nil {
		return "", err
	}
	if err := s.writeBody(ctx, body, "create_document"); err != nil {
		return "", err
	}
	return s.session.page.URL(), nil
}

// ReplaceDocument полностью заменяет содержимое документа.
func (s *playwrightSession) ReplaceDocument(ctx context.Context, documentURL, body string) error {
	if err := s.goTo(ctx, documentURL, "replace_document"); err != nil {
		return err
	}
	if err := s.waitFor(ctx, documentCanvas, "replace_document"); err != nil {
		return err
	}
	return s.writeBody(ctx, body, "replace_document")
}

// renameDocument задаёт имя документа.
func (s *playwrightSession) renameDocument(ctx context.Context, title string) error {
	field := s.session.page.Locator(documentTitleInput).First()
	if err := field.Fill(title, playwright.LocatorFillOptions{
		Timeout: playwright.Float(operationTimeout(ctx, s.cfg.OperationTimeout)),
	}); err != nil {
		return s.fail(ctx, "rename_document", true, fmt.Errorf("задать имя документа: %w", err))
	}
	if err := s.session.page.Keyboard().Press("Enter"); err != nil {
		return s.fail(ctx, "rename_document", true, fmt.Errorf("подтвердить имя документа: %w", err))
	}
	return pause(ctx)
}

// writeBody заменяет весь текст документа промптом.
//
// Текст вставляется из буфера обмена, а не набирается: промпт статьи — это десятки тысяч
// символов, набор занял бы минуты и попал бы под автозамену Docs. Select-all перед вставкой
// и даёт полную перезапись без версий и копий.
func (s *playwrightSession) writeBody(ctx context.Context, body, stage string) error {
	if _, err := s.session.page.Evaluate(writeClipboardJS, body); err != nil {
		return s.fail(ctx, stage, true, fmt.Errorf("положить промпт в буфер обмена: %w", err))
	}
	canvas := s.session.page.Locator(documentCanvas).First()
	if err := canvas.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(operationTimeout(ctx, s.cfg.OperationTimeout)),
	}); err != nil {
		return s.fail(ctx, stage, true, fmt.Errorf("поставить курсор в документ: %w", err))
	}
	modifier := "Control"
	// Docs слушает системное сочетание, а на macOS это Meta.
	if isDarwin() {
		modifier = "Meta"
	}
	if err := s.session.page.Keyboard().Press(modifier + "+a"); err != nil {
		return s.fail(ctx, stage, true, fmt.Errorf("выделить прежнее содержимое: %w", err))
	}
	if err := s.session.page.Keyboard().Press(modifier + "+v"); err != nil {
		return s.fail(ctx, stage, true, fmt.Errorf("вставить промпт: %w", err))
	}
	if err := pause(ctx); err != nil {
		return err
	}
	// Docs сохраняет сам, но выходить из браузера до появления отметки нельзя: закрытая
	// вкладка обрывает автосохранение и оставляет документ пустым.
	if err := s.waitFor(ctx, documentSavedMarker, stage); err != nil {
		return err
	}
	return nil
}

// goTo переходит по адресу и сразу проверяет, не увёл ли Google на вход или проверку.
func (s *playwrightSession) goTo(ctx context.Context, address, stage string) error {
	if _, err := s.session.page.Goto(address, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(operationTimeout(ctx, s.cfg.OperationTimeout)),
	}); err != nil {
		return s.fail(ctx, stage, true, fmt.Errorf("открыть %s: %w", address, err))
	}
	if err := classifyPage(s.session.page.URL()); err != nil {
		return s.fail(ctx, stage, false, err)
	}
	return nil
}

// waitFor ждёт наблюдаемое состояние страницы. Никаких sleep: условие проверяется на DOM.
func (s *playwrightSession) waitFor(ctx context.Context, selector, stage string) error {
	if _, err := s.session.page.WaitForFunction(visibleElementJS, selector, playwright.PageWaitForFunctionOptions{
		Polling: "raf",
		Timeout: playwright.Float(operationTimeout(ctx, s.cfg.OperationTimeout)),
	}); err != nil {
		// Уход на страницу входа посреди ожидания выглядит как таймаут, но лечится не повтором.
		if pageErr := classifyPage(s.session.page.URL()); pageErr != nil {
			return s.fail(ctx, stage, false, pageErr)
		}
		return s.fail(ctx, stage, true, fmt.Errorf("дождаться %s: %w", selector, err))
	}
	return nil
}

// fail сохраняет диагностику и возвращает классифицированную ошибку.
func (s *playwrightSession) fail(ctx context.Context, stage string, retryable bool, err error) error {
	s.saveDiagnostics(ctx, stage)
	return &StageError{ArticleID: s.articleID, Stage: stage, Retryable: retryable, Err: err}
}
