package deepseekweb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

// blockedStateFileName лежит рядом с профилем, а не в БД: провайдер отказывает до подключения к PostgreSQL.
const blockedStateFileName = ".seo-pipeline.blocked"

// accountUnavailableError — терминальный отказ провайдера; Unauthorized не повторяется.
func accountUnavailableError(reason string) error {
	return &llm.StatusError{
		Code:    403,
		Type:    llm.ErrorTypeUnauthorized,
		Message: "deepseek_account_unavailable: " + reason,
	}
}

// BlockedUntil сообщает, действует ли cooldown аккаунта, не запуская браузер.
func BlockedUntil(profileDir string, now time.Time) (until time.Time, reason string, blocked bool) {
	return readBlockedUntil(profileDir, now)
}

func blockedStatePath(profileDir string) string {
	return filepath.Join(profileDir, blockedStateFileName)
}

// readBlockedUntil: нечитаемый или битый файл — нет блокировки, иначе маркер выключил бы провайдера навсегда.
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

// blockAccount фиксирует cooldown и возвращает терминальную ошибку; сессию не сбрасывает —
// перезапуск Chromium против заблокированного аккаунта только добавляет нагрузки.
func (c *Client) blockAccount(reason string) error {
	until := c.pace.now().Add(blockCooldown)
	if err := writeBlockedUntil(c.cfg.ProfileDir, until, reason); err != nil {
		c.log().Warn("отметка блокировки DeepSeek не сохранена", "error", err)
	}
	c.log().Error("аккаунт DeepSeek недоступен",
		"reason", reason, "blocked_until", until.UTC().Format(time.RFC3339), "cooldown", blockCooldown.String())
	return accountUnavailableError(reason)
}

// blockedStateOptions: ключи совпадают с options.* в blockedStateJS.
func blockedStateOptions(sentTexts []string) map[string]any {
	return map[string]any{
		"blockedSelector": blockedSelector,
		"answerSelector":  answerSelector,
		"itemSelector":    itemSelector,
		// Свои отправленные тексты: фраза-маркер в промпте не блокировка (см. noticeTextJS).
		"sentTexts": sentTexts,
	}
}

// detectBlocked ищет на открытой странице признаки блокировки, проверки Cloudflare или капчи.
func (c *Client) detectBlocked(page playwright.Page) (string, bool) {
	value, err := page.Evaluate(blockedStateJS, blockedStateOptions(c.sentTexts()))
	if err != nil {
		return "", false
	}
	reason, ok := value.(string)
	if !ok || strings.TrimSpace(reason) == "" {
		return "", false
	}
	// Снимок: без него ложную блокировку (провайдер выключен на час) не отличить от настоящей.
	c.mu.Lock()
	articleID := c.openArticleID
	c.mu.Unlock()
	c.saveDiagnostics(page, "blocked_"+reason, articleID)
	return reason, true
}
