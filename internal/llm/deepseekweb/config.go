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
	// responseSettledFor — сколько текст должен не меняться, когда под ответом уже
	// отрисована панель действий. Панель появляется только после конца генерации, поэтому
	// длинного ожидания здесь не нужно.
	responseSettledFor = 2 * time.Second
	// responseStableFor — запасной признак на случай, если панель действий не опознана.
	// Прежние 4 s принимали за конец ответа обычную паузу стрима: статья сохранялась
	// обрезанной на полуслове, а следующий промпт уходил в поле во время генерации.
	responseStableFor = 25 * time.Second
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

	// minRequestInterval и requestJitter задают паузу между двумя запросами к веб-интерфейсу:
	// 20 s плюс случайные 0–40 s. Случайность здесь не маскирует автоматизацию, а убирает
	// ровный машинный ритм, по которому нагрузка выглядит агрессивной.
	minRequestInterval = 20 * time.Second
	requestJitter      = 40 * time.Second

	// blockCooldown — на сколько клиент перестаёт открывать браузер после того, как увидел
	// страницу блокировки. Реальные блокировки длятся дольше, но ложное срабатывание на
	// проверке Cloudflare не должно выключать провайдера на сутки.
	blockCooldown = time.Hour
)

type Config struct {
	ChatURL    string
	LoginURL   string
	ProfileDir string
	Headless   bool
	// DiagnosticsDir — корень диагностики этого провайдера. Задаётся вызывающим, чтобы дампы
	// разных пайплайнов не смешивались; пустое значение включает общий каталог по умолчанию.
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
