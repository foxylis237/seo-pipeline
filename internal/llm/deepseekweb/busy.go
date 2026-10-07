package deepseekweb

import (
	"github.com/mxschmitt/playwright-go"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

// detectServerBusy сообщает, отказался ли DeepSeek обслуживать отправленное сообщение.
// Это не блокировка: без cooldown и сброса сессии — беседа нужна следующим стадиям.
// Метка треда нужна, потому что плашка отказа на прошлое сообщение из беседы не исчезает.
func (c *Client) detectServerBusy(page playwright.Page, mark answerMark) bool {
	value, err := page.Evaluate(serverBusyJS, responseStateOptions(mark))
	if err != nil {
		return false
	}
	busy, ok := value.(bool)
	return ok && busy
}

// serverBusyError — временный отказ; Overloaded откладывает повтор на минуты.
func serverBusyError() error {
	return &llm.StatusError{
		Code:    503,
		Type:    llm.ErrorTypeOverloaded,
		Message: "deepseek_server_busy: DeepSeek не принял запрос, сервер перегружен",
	}
}
