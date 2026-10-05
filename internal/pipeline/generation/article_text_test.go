package generation

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const testStructure = "H2: Первый раздел\nH2: Второй раздел\nH2: Часто задаваемые вопросы"

// Оборванный ответ дописывается продолжением того же чата, а недописанное предложение в
// склейку не попадает: иначе фраза собралась бы из половин двух попыток.
func TestBuildArticleTextCompletesCutAnswer(t *testing.T) {
	cut := "H2 - Первый раздел\n\nПервый абзац закончен точкой. Второй обрывается на полусло"
	tail := "H2 - Второй раздел\n\nВторой раздел на месте.\n\nH2 - Часто задаваемые вопросы\n\nВопрос и ответ."
	var continues int

	text, err := BuildArticleText(context.Background(), ArticleTextRequest{
		Structure: testStructure,
		Prompt:    "промпт",
		Send:      func(context.Context, string) (string, error) { return cut, nil },
		Continue: func(_ context.Context, prompt string) (string, error) {
			continues++
			if !strings.Contains(prompt, "закончен точкой") {
				t.Fatalf("продолжение не назвало место обрыва:\n%s", prompt)
			}
			return tail, nil
		},
	})
	if err != nil {
		t.Fatalf("текст не собран: %v", err)
	}
	if continues != 1 {
		t.Fatalf("продолжений запрошено %d, ожидалось одно", continues)
	}
	if strings.Contains(text, "полусло") {
		t.Fatalf("в склейке осталось оборванное предложение:\n%s", text)
	}
	if got := CountSections(text); got != 3 {
		t.Fatalf("разделов в склейке %d, ожидалось 3:\n%s", got, text)
	}
}

// Целый ответ продолжений не требует: лишнее сообщение стоит денег и времени.
func TestBuildArticleTextDoesNotContinueWhenAnswerIsWhole(t *testing.T) {
	whole := "H2 - Первый раздел\n\nТекст.\n\nH2 - Второй раздел\n\nТекст.\n\nH2 - Часто задаваемые вопросы\n\nТекст."

	text, err := BuildArticleText(context.Background(), ArticleTextRequest{
		Structure: testStructure,
		Prompt:    "промпт",
		Send:      func(context.Context, string) (string, error) { return whole, nil },
		Continue: func(context.Context, string) (string, error) {
			t.Fatal("целый текст отправили на дописывание")
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("целый текст объявлен оборванным: %v", err)
	}
	if text != whole {
		t.Fatalf("текст изменился:\n%s", text)
	}
}

// Модель вправе свести два раздела плана в один — тогда счёт разделов ловит обрыв там, где
// его нет. Ответ «ГОТОВО» закрывает такой случай, не дописывая хвост ради проверки.
func TestBuildArticleTextAcceptsDoneAnswer(t *testing.T) {
	short := "H2 - Первый раздел\n\nТекст.\n\nH2 - Второй раздел и вопросы\n\nТекст."

	text, err := BuildArticleText(context.Background(), ArticleTextRequest{
		Structure: testStructure,
		Prompt:    "промпт",
		Send:      func(context.Context, string) (string, error) { return short, nil },
		Continue:  func(context.Context, string) (string, error) { return "ГОТОВО", nil },
	})
	if err != nil {
		t.Fatalf("ответ «ГОТОВО» не принят: %v", err)
	}
	if text != short {
		t.Fatalf("текст изменился после ответа «ГОТОВО»:\n%s", text)
	}
}

// A line without a final period is not a cut sentence once the model says it is done.
func TestBuildArticleTextDoneKeepsUntrimmedTail(t *testing.T) {
	short := "H2 - Первый раздел\n\nТекст.\n\nH2 - Второй раздел и вопросы\n\nТекст.\n\nИсточник: https://hh.ru"

	text, err := BuildArticleText(context.Background(), ArticleTextRequest{
		Structure: testStructure,
		Prompt:    "промпт",
		Send:      func(context.Context, string) (string, error) { return short, nil },
		Continue:  func(context.Context, string) (string, error) { return "ГОТОВО", nil },
	})
	if err != nil {
		t.Fatalf("ответ «ГОТОВО» не принят: %v", err)
	}
	if text != short {
		t.Fatalf("после ответа «ГОТОВО» текст обрезан:\n%s", text)
	}
}

// Не дописанный и после продолжений текст — отказ стадии: половина статьи пройдёт разметку
// и публикацию молча, а стоит это дороже повторного прогона.
func TestBuildArticleTextFailsAfterContinuations(t *testing.T) {
	cut := "H2 - Первый раздел\n\nТекст закончен. Дальше обрыв на полусло"

	_, err := BuildArticleText(context.Background(), ArticleTextRequest{
		Structure: testStructure,
		Prompt:    "промпт",
		Send:      func(context.Context, string) (string, error) { return cut, nil },
		Continue: func(context.Context, string) (string, error) {
			return "и снова обрыв на полусло", nil
		},
	})
	if !errors.Is(err, ErrTextIncomplete) {
		t.Fatalf("обрыв не признан обрывом: %v", err)
	}
}

// Без плана статьи проверка по разделам выключается, а обрыв на полуслове ловится всё равно:
// план есть не у каждой стадии, а предел длины ответа — у всех.
func TestValidateArticleTextCompleteWithoutStructure(t *testing.T) {
	if err := ValidateArticleTextComplete("", "H2 - Раздел\n\nТекст закончен."); err != nil {
		t.Fatalf("законченный текст объявлен оборванным: %v", err)
	}
	if err := ValidateArticleTextComplete("", "H2 - Раздел\n\nТекст обрывается на полусло"); !errors.Is(err, ErrTextIncomplete) {
		t.Fatalf("обрыв без плана не пойман: %v", err)
	}
}

// Слоты чата резервируются под продолжения заранее: роутер раздаёт стадии сообщениям по
// порядку, и взять слот по ходу неоткуда.
func TestTextChatStagesReservesContinuations(t *testing.T) {
	if got := TextChatStages("expert"); len(got) != textContinuations+1 {
		t.Fatalf("слотов %d, ожидалось %d: %v", len(got), textContinuations+1, got)
	}
}
