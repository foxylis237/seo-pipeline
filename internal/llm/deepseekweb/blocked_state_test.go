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
