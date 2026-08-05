// Package deepseekweb implements the shared LLM client boundary through the
// public DeepSeek Chat web interface and a persistent Playwright profile.
package deepseekweb

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	defaultOperationTimeout = 30 * time.Second
	defaultLoginTimeout     = 30 * time.Minute
	responseStableFor       = 4 * time.Second
	// defaultResponseTimeout ограничивает ожидание ответа, когда у контекста нет своего
	// дедлайна: прямой вызов Generate с context.Background() иначе получал 1 мс.
	defaultResponseTimeout = 5 * time.Minute
	// responseHeartbeat — как часто писать в лог, что ответ ещё генерируется.
	responseHeartbeat = 30 * time.Second
	// clipboardMarker кладётся в буфер перед нажатием «Копировать», чтобы прежнее значение
	// нельзя было принять за новый ответ.
	clipboardMarker       = "__seo_pipeline_clipboard__"
	clipboardTimeout      = 5 * time.Second
	clipboardPollInterval = 200 * time.Millisecond
)

type Config struct {
	ChatURL    string
	LoginURL   string
	ProfileDir string
	Headless   bool
}

func validateConfig(cfg Config, logger *slog.Logger) error {
	if strings.TrimSpace(cfg.ChatURL) == "" {
		return fmt.Errorf("DeepSeek chat URL is empty")
	}
	if strings.TrimSpace(cfg.LoginURL) == "" {
		return fmt.Errorf("DeepSeek login URL is empty")
	}
	if strings.TrimSpace(cfg.ProfileDir) == "" {
		return fmt.Errorf("DeepSeek browser profile directory is empty")
	}
	if logger == nil {
		return fmt.Errorf("DeepSeek logger is nil")
	}
	return nil
}
