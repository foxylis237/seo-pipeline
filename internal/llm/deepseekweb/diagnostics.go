package deepseekweb

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// diagnosticsDir — куда складывать состояние страницы при неудаче. Тот же корень, что у
// браузерных интеграций Keys.so и Arsenkin.
const diagnosticsDir = "output/task1/debug/deepseek"

// emailRE вычищает адреса из сохранённой разметки: на странице чата виден адрес аккаунта,
// а дампы лежат на диске без ротации.
var emailRE = regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`)

// saveDiagnostics сохраняет скриншот и разметку страницы.
//
// Без этого неудача ожидания ответа неотличима от любой другой: в логе видно только, что
// новых ответов не появилось, а что при этом было на странице — неизвестно. Ошибки записи
// намеренно не поднимаются наверх: диагностика не должна подменять исходную ошибку.
func (c *Client) saveDiagnostics(page playwright.Page, stage string, articleID int64) {
	directory := filepath.Join(diagnosticsDir, fmt.Sprintf("article-%d", articleID))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		c.logger.Warn("DeepSeek diagnostics directory was not created", "error", err)
		return
	}
	base := filepath.Join(directory, time.Now().UTC().Format("20060102T150405Z")+"-"+stage)

	if _, err := page.Screenshot(playwright.PageScreenshotOptions{
		Path: playwright.String(base + ".png"), FullPage: playwright.Bool(true),
	}); err != nil {
		c.logger.Warn("DeepSeek screenshot was not saved", "error", err)
	}
	content, err := page.Content()
	if err != nil {
		c.logger.Warn("DeepSeek page markup was not read", "error", err)
	} else if err := os.WriteFile(base+".html", []byte(redactDiagnosticHTML(content)), 0o600); err != nil {
		c.logger.Warn("DeepSeek page markup was not saved", "error", err)
	}

	state := responseState(page, answerMark{key: -1})
	c.logger.Warn("DeepSeek page diagnostics saved",
		"path", base, "stage", stage, "article_id", articleID, "page_state", state, "url", page.URL())
}

func redactDiagnosticHTML(content string) string {
	return emailRE.ReplaceAllString(content, "[redacted-email]")
}
