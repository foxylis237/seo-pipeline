package google

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// defaultDiagnosticsDir используется, когда каталог не задан вызывающим. Имени задачи здесь
// нет намеренно: интеграция не знает, что задач больше одной, — корень ей передают.
const defaultDiagnosticsDir = "output/debug/google"

// diagnosticsDir возвращает каталог диагностики этого прогона.
func (s *playwrightSession) diagnosticsDir() string {
	if root := strings.TrimSpace(s.cfg.DiagnosticsDir); root != "" {
		return root
	}
	return defaultDiagnosticsDir
}

// emailRE вычищает адреса из сохранённой разметки: в интерфейсе Google виден адрес аккаунта,
// а дампы лежат на диске без ротации.
var emailRE = regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`)

func isDarwin() bool { return runtime.GOOS == "darwin" }

// saveDiagnostics сохраняет состояние страницы при неудаче.
//
// Без этого отказ на чужой вёрстке неотличим от любого другого: в логе видно только, что
// ожидание не дождалось, а что было на странице — неизвестно. Селекторы Google не проверены
// на живом аккаунте, поэтому диагностика здесь особенно нужна. Ошибки записи наверх не
// поднимаются: диагностика не должна подменять исходную ошибку.
func (s *playwrightSession) saveDiagnostics(ctx context.Context, stage string) {
	directory := filepath.Join(s.diagnosticsDir(), fmt.Sprintf("article-%d", s.articleID))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		s.logger.Warn("каталог диагностики Google не создан", "error", err)
		return
	}
	base := filepath.Join(directory, time.Now().UTC().Format("20060102T150405Z")+"-"+stage)

	if _, err := s.session.page.Screenshot(playwright.PageScreenshotOptions{
		Path: playwright.String(base + ".png"), FullPage: playwright.Bool(true),
	}); err != nil {
		s.logger.Warn("скриншот Google не сохранён", "error", err)
	}
	if content, err := s.session.page.Content(); err != nil {
		s.logger.Warn("разметка страницы Google не прочитана", "error", err)
	} else if err := os.WriteFile(base+".html", []byte(redactDiagnosticHTML(content)), 0o600); err != nil {
		s.logger.Warn("разметка страницы Google не сохранена", "error", err)
	}

	// info.json намеренно без cookies и заголовков авторизации: в каталоге debug им не место.
	info := struct {
		Stage     string `json:"stage"`
		ArticleID int64  `json:"article_id"`
		URL       string `json:"url"`
		Canceled  bool   `json:"canceled"`
		SavedAt   string `json:"saved_at"`
	}{
		Stage: stage, ArticleID: s.articleID, URL: s.session.page.URL(),
		Canceled: ctx.Err() != nil, SavedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if encoded, err := json.MarshalIndent(info, "", "  "); err != nil {
		s.logger.Warn("сведения о странице Google не закодированы", "error", err)
	} else if err := os.WriteFile(base+".json", encoded, 0o600); err != nil {
		s.logger.Warn("сведения о странице Google не сохранены", "error", err)
	}

	s.logger.Warn("диагностика страницы Google сохранена",
		"path", base, "stage", stage, "article_id", s.articleID, "url", s.session.page.URL())
}

func redactDiagnosticHTML(content string) string {
	return emailRE.ReplaceAllString(content, "[redacted-email]")
}
