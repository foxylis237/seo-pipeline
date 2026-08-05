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
