package deepseekweb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/mxschmitt/playwright-go"
)

// blockedStateFileName лежит рядом с профилем, а не в БД: провайдер должен уметь отказать
// до подключения к чему-либо ещё, включая PostgreSQL.
const blockedStateFileName = ".seo-pipeline.blocked"

// accountUnavailableError — терминальный отказ провайдера.
//
// Тип Unauthorized выбран намеренно: isTemporary для него возвращает false, поэтому повторов
// внутри таргета не будет без правки общей политики повторов.
func accountUnavailableError(reason string) error {
	return &llm.StatusError{
		Code:    403,
		Type:    llm.ErrorTypeUnauthorized,
		Message: "deepseek_account_unavailable: " + reason,
	}
}

func blockedStatePath(profileDir string) string {
	return filepath.Join(profileDir, blockedStateFileName)
}

// readBlockedUntil сообщает, действует ли ещё cooldown. Нечитаемый или битый файл трактуется
// как отсутствие блокировки: сломанный маркер не должен навсегда выключить провайдера.
func readBlockedUntil(profileDir string, now time.Time) (time.Time, string, bool) {
	raw, err := os.ReadFile(blockedStatePath(profileDir))
	if err != nil {
		return time.Time{}, "", false
	}
	lines := strings.SplitN(strings.TrimSpace(string(raw)), "\n", 2)
	until, err := time.Parse(time.RFC3339, strings.TrimSpace(lines[0]))
	if err != nil {
		return time.Time{}, "", false
	}
	if !now.Before(until) {
		return until, "", false
	}
	reason := ""
	if len(lines) > 1 {
		reason = strings.TrimSpace(lines[1])
	}
	return until, reason, true
}

func writeBlockedUntil(profileDir string, until time.Time, reason string) error {
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return fmt.Errorf("create DeepSeek profile directory for block marker: %w", err)
	}
	content := until.UTC().Format(time.RFC3339) + "\n" + reason + "\n"
	if err := os.WriteFile(blockedStatePath(profileDir), []byte(content), 0o600); err != nil {
		return fmt.Errorf("save DeepSeek block marker: %w", err)
	}
	return nil
}

// blockAccount фиксирует cooldown и возвращает терминальную ошибку.
//
// Сессия намеренно не сбрасывается: перезапускать Chromium против заблокированного аккаунта
// бессмысленно и увеличивает нагрузку, за которую блокировка и выдана.
func (c *Client) blockAccount(reason string) error {
	until := c.pace.now().Add(blockCooldown)
	if err := writeBlockedUntil(c.cfg.ProfileDir, until, reason); err != nil {
		c.logger.Warn("DeepSeek block marker was not saved", "error", err)
	}
	c.logger.Error("DeepSeek account is unavailable",
		"reason", reason, "blocked_until", until.UTC().Format(time.RFC3339), "cooldown", blockCooldown.String())
	return accountUnavailableError(reason)
}

// detectBlocked ищет на открытой странице признаки блокировки, проверки Cloudflare или капчи.
// Это только распознавание состояния: ничего не обходит и не подменяет.
func (c *Client) detectBlocked(page playwright.Page) (string, bool) {
	value, err := page.Evaluate(blockedStateJS, map[string]any{
		"blockedSelector":  blockedSelector,
		"composerSelector": composerSelector,
	})
	if err != nil {
		return "", false
	}
	reason, ok := value.(string)
	if !ok || strings.TrimSpace(reason) == "" {
		return "", false
	}
	return reason, true
}
