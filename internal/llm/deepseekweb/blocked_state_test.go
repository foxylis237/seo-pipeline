package deepseekweb

import (
	"strings"
	"testing"
)

// Наши же слова не должны приниматься за состояние страницы.
//
// Статья 10 pprof_1 («Младший медперсонал») содержит фразу «Нарушение правил утилизации
// отходов класса „Б“ грозит штрафами». Это дословный маркер terms_violation, и на страницу
// он приезжал в нашем промпте стадии html: при дописывании оборванной разметки беседа уже
// содержала отправленный текст статьи. Дважды подряд это объявлялось блокировкой аккаунта —
// провайдер выключался на час, хотя аккаунт был исправен.
func TestSentTextIsCutFromPageState(t *testing.T) {
	if !strings.Contains(noticeTextJS, "options.sentTexts") {
		t.Fatal("noticeText не вырезает отправленное: маркер из промпта снова станет блокировкой")
	}
	passed := blockedStateOptions([]string{"пример"})
	if _, ok := passed["sentTexts"]; !ok {
		t.Fatal("blockedStateOptions не передаёт sentTexts в скрипт")
	}
}

// Отправленное живёт ровно столько, сколько беседа: на новой странице прежних сообщений нет,
// и вырезать их значило бы искать в тексте то, чего там не отрисовано.
func TestSentTextResetsWithChat(t *testing.T) {
	client := &Client{}
	client.rememberSentText("первое сообщение")
	if len(client.sentTexts()) != 1 {
		t.Fatalf("отправленное не запомнилось: %v", client.sentTexts())
	}
	client.markChatOpened(42)
	if got := client.sentTexts(); len(got) != 0 {
		t.Fatalf("новая беседа обязана обнулить отправленное, осталось %v", got)
	}
}

// Короткие строки промпта не вырезаются: «H2 - Обязанности» совпало бы со случайным местом
// страницы, а плашка площадки состоит из длинных фраз.
func TestSentTextCutKeepsShortLinesOut(t *testing.T) {
	if !strings.Contains(noticeTextJS, "piece.length >= 40") {
		t.Fatal("порог длины строки пропал: вырезание станет непредсказуемым")
	}
}

// Короткая строка промпта снимается только целой строкой страницы: запрос Wordstat
// «нарушение правил …» статьи 39 obuch_1 объявил terms_violation на собственной беседе.
func TestSentTextCutRemovesShortLinesAsWholeLines(t *testing.T) {
	if !strings.Contains(noticeTextJS, "shortLines.has(line)") {
		t.Fatal("короткие строки отправленного не снимаются: запрос-маркер снова станет блокировкой")
	}
}

// История беседы в проверку не входит: 27.09.2026 terms_violation второй раз сработал на
// беседе статьи 39 obuch_1, где фраза-маркер была только в наших сообщениях и в ответах.
func TestNoticeTextSkipsConversationHistory(t *testing.T) {
	if !strings.Contains(noticeTextJS, "threadItems.length - 1") {
		t.Fatal("история беседы снова читается как состояние страницы")
	}
	if _, ok := blockedStateOptions(nil)["itemSelector"]; !ok {
		t.Fatal("blockedStateOptions не передаёт itemSelector")
	}
}

// Панель навигации DeepSeek повторяет наши сообщения одной строкой вне треда: вырезать их
// обязано и целиком, со схлопнутыми пробелами (снимок статьи 39 obuch_1, 27.09.2026).
func TestNoticeTextCutsSentMessagesAsSingleLine(t *testing.T) {
	if !strings.Contains(noticeTextJS, "page = squash(page)") || !strings.Contains(noticeTextJS, "wholes") {
		t.Fatal("сообщение, отрисованное одной строкой, снова станет плашкой блокировки")
	}
}

// Каждая строка отправленного вырезается где угодно: 28.09.2026 наш промпт на беседе
// статьи 39 obuch_1 оказался последним смонтированным сообщением и остался в проверке.
func TestNoticeTextCutsEverySentLineAnywhere(t *testing.T) {
	if !strings.Contains(noticeTextJS, "piece.length >= 8") {
		t.Fatal("строки отправленного снова не вырезаются вне своего сообщения")
	}
}
