// Package deepseekweb implements the LLM client through the DeepSeek Chat web interface
// and a persistent Playwright profile.
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
	// responseSettledFor — сколько текст не меняется при уже отрисованной панели действий;
	// панель появляется только после конца генерации.
	responseSettledFor = 2 * time.Second
	// responseStableFor — запасной признак конца без панели действий; меньший срок принимает
	// за конец обычную паузу стрима.
	responseStableFor = 25 * time.Second
	// defaultResponseTimeout ограничивает ожидание ответа, когда у контекста нет дедлайна.
	defaultResponseTimeout = 5 * time.Minute
	// responseHeartbeat — как часто писать в лог, что ответ ещё генерируется.
	responseHeartbeat = 30 * time.Second
	// clipboardMarker кладётся в буфер перед «Копировать», чтобы прежнее значение не сошло за ответ.
	clipboardMarker = "__seo_pipeline_clipboard__"
	// answerActionsGrace — сколько ждать панель действий, если конец ответа определён
	// стабилизацией текста.
	answerActionsGrace    = 3 * time.Minute
	clipboardTimeout      = 5 * time.Second
	clipboardPollInterval = 200 * time.Millisecond

	// minRequestInterval и requestJitter — пауза между запросами; случайность убирает ровный машинный ритм.
	minRequestInterval = 20 * time.Second
	requestJitter      = 40 * time.Second

	// sessionBreakEvery, sessionBreakMin и sessionBreakJitter — длинный перерыв после серии
	// запросов: многочасовая работа без перерыва не похожа на человека.
	sessionBreakEvery  = 30
	sessionBreakMin    = 15 * time.Minute
	sessionBreakJitter = 15 * time.Minute

	// blockCooldown — пауза после страницы блокировки; короткая, чтобы ложное срабатывание
	// не выключало провайдера надолго.
	blockCooldown = time.Hour
)

type Config struct {
	ChatURL    string
	LoginURL   string
	ProfileDir string
	Headless   bool
	// DiagnosticsDir — корень диагностики провайдера; пустое — defaultDiagnosticsDir.
	DiagnosticsDir string
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
