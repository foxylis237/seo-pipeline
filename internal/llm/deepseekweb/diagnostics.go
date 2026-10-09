package deepseekweb

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// defaultDiagnosticsDir — каталог снимков страницы, если вызывающий его не задал.
const defaultDiagnosticsDir = "output/debug/deepseek"

func (c *Client) diagnosticsDir() string {
	if root := strings.TrimSpace(c.cfg.DiagnosticsDir); root != "" {
		return root
	}
	return defaultDiagnosticsDir
}

// emailRE вычищает из дампов адрес аккаунта, видимый на странице чата.
var emailRE = regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`)

// saveDiagnostics сохраняет скриншот и разметку страницы; ошибки записи не поднимаются,
// чтобы не подменить исходную.
func (c *Client) saveDiagnostics(page playwright.Page, stage string, articleID int64) {
	directory := filepath.Join(c.diagnosticsDir(), fmt.Sprintf("article-%d", articleID))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		c.log().Warn("каталог диагностики DeepSeek не создан", "error", err)
		return
	}
	base := filepath.Join(directory, time.Now().UTC().Format("20060102T150405Z")+"-"+stage)

	if _, err := page.Screenshot(playwright.PageScreenshotOptions{
		Path: playwright.String(base + ".png"), FullPage: playwright.Bool(true),
	}); err != nil {
		c.log().Warn("снимок страницы DeepSeek не сохранён", "error", err)
	}
	content, err := page.Content()
	if err != nil {
		c.log().Warn("разметка страницы DeepSeek не прочитана", "error", err)
	} else if err := os.WriteFile(base+".html", []byte(redactDiagnosticHTML(content)), 0o600); err != nil {
		c.log().Warn("разметка страницы DeepSeek не сохранена", "error", err)
	}

	state := responseState(page, answerMark{key: -1})
	c.log().Warn("диагностика страницы DeepSeek сохранена",
		"debug_path", base, "step", stage, "page_state", state, "url", page.URL())
}

func redactDiagnosticHTML(content string) string {
	return emailRE.ReplaceAllString(content, "[redacted-email]")
}
