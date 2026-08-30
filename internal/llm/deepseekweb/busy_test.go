package deepseekweb

import (
	"errors"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

// Отказ «Server is busy» — не блокировка: аккаунт цел, беседа открыта, повторять имеет
// смысл. Но не через секунды, поэтому у него отдельный тип, по которому маршрутизатор
// выбирает длинную паузу.
func TestServerBusyErrorIsTemporaryOverload(t *testing.T) {
	err := serverBusyError()
	var statusErr *llm.StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error = %#v", err)
	}
	if statusErr.Type != llm.ErrorTypeOverloaded {
		t.Fatalf("type = %s, want %s", statusErr.Type, llm.ErrorTypeOverloaded)
	}
	if statusErr.Code != 503 {
		t.Fatalf("code = %d, want 503: иначе отказ перестанет быть временным", statusErr.Code)
	}
	if !strings.Contains(err.Error(), "deepseek_server_busy") {
		t.Fatalf("message = %q", err.Error())
	}
}

// Перегрузка не трогает ни профиль, ни открытую беседу: сброс сессии закрыл бы браузер, а
// вместе с ним чат, на историю которого опираются следующие стадии.
func TestServerBusyHandlingKeepsSessionAndProfile(t *testing.T) {
	source := readSourceFile(t, "busy.go")
	for _, forbidden := range []string{"resetSession", "writeBlockedUntil", "blockAccount", "RemoveAll"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("busy.go содержит %q: перегрузка не должна ронять сессию и аккаунт", forbidden)
		}
	}
}

// Отказ появляется под последним сообщением — в конце длинной беседы, длина которой зависит
// от виртуального списка. Пока текст страницы читался окном, распознать его было нечем.
func TestServerBusyScriptReadsWholePage(t *testing.T) {
	if !strings.Contains(serverBusyJS, `"server is busy"`) {
		t.Fatal("скрипт не знает формулировки DeepSeek")
	}
	if !strings.Contains(serverBusyJS, "noticeText()") || !strings.Contains(blockedStateJS, "noticeText()") {
		t.Fatal("текст страницы читается разными правилами")
	}
	if strings.Contains(noticeTextJS, "slice(") {
		t.Fatal("текст страницы урезан: отказ под последним сообщением выпадет из окна поиска")
	}
	if !strings.Contains(noticeTextJS, "options.answerSelector") {
		t.Fatal("содержимое ответов не вырезается: слова модели будут приняты за состояние страницы")
	}
}

// Ожидание ответа обязано спрашивать о перегрузке само: иначе оно висит до конца бюджета
// стадии и не оставляет времени ни на один повтор.
func TestWaitForAnswerAsksAboutServerBusy(t *testing.T) {
	source := readSourceFile(t, "client.go")
	start := strings.Index(source, "func (c *Client) waitForAnswer(")
	if start < 0 {
		t.Fatal("waitForAnswer не найден")
	}
	end := strings.Index(source[start+1:], "\nfunc ")
	if end < 0 {
		t.Fatal("конец waitForAnswer не найден")
	}
	if !strings.Contains(source[start:start+1+end], "detectServerBusy") {
		t.Fatal("ожидание ответа не проверяет перегрузку")
	}
}

// Плашка отказа остаётся в треде навсегда, а беседа у задачи одна на несколько сообщений.
// Пока проверка читала страницу целиком, чужая плашка от предыдущего сообщения роняла
// каждый следующий ответ на первом же хартбите — при этом ответ в этот момент писался.
func TestServerBusyScriptIgnoresNoticeFromPreviousMessage(t *testing.T) {
	if !strings.Contains(serverBusyJS, "findFreshAnswer() !== null") {
		t.Fatal("скрипт не спрашивает, не пишется ли ответ на наше сообщение")
	}
	if !strings.Contains(serverBusyJS, "options.stopSelector") {
		t.Fatal("скрипт не смотрит на кнопку остановки: генерация до первого узла ответа выпадет")
	}
	if !strings.Contains(serverBusyJS, "items[items.length - 1]") {
		t.Fatal("плашка ищется не в последнем сообщении треда: старая снова будет считаться отказом")
	}
	if !strings.Contains(serverBusyJS, "noticeText()") {
		t.Fatal("нет запасного правила на случай пропажи разметки списка")
	}
}

// Снимок треда обязателен: без него скрипту нечем отличить свою плашку от чужой, а правило
// «какой ответ считать новым» в клиенте должно остаться одно.
func TestServerBusyDetectionUsesThreadMark(t *testing.T) {
	source := readSourceFile(t, "busy.go")
	if !strings.Contains(source, "detectServerBusy(page playwright.Page, mark answerMark)") {
		t.Fatal("detectServerBusy не принимает снимок треда")
	}
	if !strings.Contains(source, "responseStateOptions(mark)") {
		t.Fatal("параметры скрипта собираются не тем же правилом, что у responseState")
	}
}
