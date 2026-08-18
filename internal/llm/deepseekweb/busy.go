package deepseekweb

import (
	"github.com/mxschmitt/playwright-go"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

// detectServerBusy сообщает, отказался ли DeepSeek обслуживать отправленное сообщение.
//
// Это не блокировка аккаунта: профиль, авторизация и открытая беседа целы, отказ снимается
// сам. Поэтому здесь нет ни cooldown, ни сброса сессии — сброс закрыл бы браузер вместе с
// беседой, а на её историю опираются следующие стадии чата.
func (c *Client) detectServerBusy(page playwright.Page) bool {
	value, err := page.Evaluate(serverBusyJS, map[string]any{"answerSelector": answerSelector})
	if err != nil {
		return false
	}
	busy, ok := value.(bool)
	return ok && busy
}

// serverBusyError — временный отказ провайдера с собственной паузой перед повтором.
// Тип Overloaded выбран, чтобы повтор ушёл на минуты: за секунды перегрузка не проходит.
func serverBusyError() error {
	return &llm.StatusError{
		Code:    503,
		Type:    llm.ErrorTypeOverloaded,
		Message: "deepseek_server_busy: DeepSeek не принял запрос, сервер перегружен",
	}
}
