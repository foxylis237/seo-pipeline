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
//
// Снимок треда обязателен: плашка отказа из беседы не исчезает, и без него собственная
// плашка предыдущего сообщения выдавалась бы за отказ на новое. Набор параметров тот же,
// что у responseState, — правило «какой ответ считать новым» в клиенте одно.
func (c *Client) detectServerBusy(page playwright.Page, mark answerMark) bool {
	value, err := page.Evaluate(serverBusyJS, responseStateOptions(mark))
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
