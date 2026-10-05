package generation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ErrTextIncomplete — текст статьи оборвался и до конца плана не дошёл.
//
// Отдельный тип нужен затем же, зачем ErrHTMLIncomplete: обрыв лечится продолжением того же
// чата, а остальные отказы стадии — нет.
var ErrTextIncomplete = errors.New("текст статьи не дописан до конца")

const (
	// textContinuations — сколько раз просить модель дописать оборванный текст.
	//
	// Один ответ веб-интерфейса DeepSeek упирается в предел длины около 27 000 символов:
	// статья 2 obuch_1 оборвалась на 24 256 знаках посреди предложения, потеряв два
	// последних раздела — блок вопросов и призыв. Двух продолжений хватает статье любой
	// длины из тех, что задачи пишут сегодня.
	textContinuations = 2

	// textTailRunes — сколько символов конца принятого текста показать модели, прося её
	// продолжить. Место обрыва называется явно: модель не обязана помнить, где остановилась.
	textTailRunes = 400

	// textOverlapRunes — какой длины повтор снимать на стыке частей, если модель начала
	// продолжение с уже выданного хвоста.
	textOverlapRunes = 600

	// textDonePhrase — чем модель отвечает, если текст уже дописан до конца.
	//
	// Нужно против ложного доспроса: признак обрыва по разделам срабатывает и тогда, когда
	// модель свела два раздела плана в один. Дешёвый ответ «ГОТОВО» закрывает такой случай
	// без хвоста, дописанного ради проверки.
	textDonePhrase = "ГОТОВО"
)

var (
	// sectionLineRE — строка раздела в каноническом виде «H2 - Название». Считаются только
	// разделы: подзаголовки модель вправе делить и сводить, а разделы задаёт структура.
	sectionLineRE = regexp.MustCompile(`(?i)^\s*h2\s*[-:\x{2013}\x{2014}]\s*\S`)

	// sentenceEndRE — конец предложения: точка и её родня, возможно под кавычкой или скобкой.
	// По нему отличается законченный текст от оборванного на полуслове.
	sentenceEndRE = regexp.MustCompile(`[.!?…:;]["»)\]*_]*$`)
)

// TextMessage — одно сообщение чата, который пишет текст статьи.
type TextMessage func(ctx context.Context, prompt string) (string, error)

// ArticleTextRequest — один прогон стадии, возвращающей текст статьи целиком.
//
// Поток задачи даёт план статьи, промпт стадии и способ отправить сообщение; правило «текст
// обязан дойти до конца плана» живёт здесь, одно на все задачи.
type ArticleTextRequest struct {
	// Structure — план статьи: по числу его разделов видно, сколько их ждать в тексте.
	// Пустой план проверку по разделам выключает, обрыв на полуслове ловится всё равно.
	Structure string
	// Prompt — отрендеренный промпт стадии.
	Prompt string
	// Send отправляет первое сообщение стадии: у первой стадии чата это Chat.Send, у
	// следующей — Chat.Continue. Continue шлёт продолжения; без него оборванный текст
	// остаётся отказом стадии, дописывать его нечем.
	Send     TextMessage
	Continue TextMessage
	Logger   *slog.Logger
	// Stage — имя этапа для лога: «article_generation» у эксперта, «article_review» у
	// редактуры. Пустое означает «article_generation».
	Stage string
}

// BuildArticleText получает текст статьи и дописывает его, если ответ модели оборвался.
//
// Обрыв ответа — рядовое поведение веб-интерфейса: длинный ответ упирается в предел длины
// сообщения. Для разметки это лечит BuildHTMLPage, а текст статьи до сих пор принимался как
// есть — и половина статьи проходила все следующие стадии молча: разметка ложилась на
// обрубок, код дописывал карточку призыва, статья получала completed без блока вопросов и
// без раздела призыва.
//
// Не дописанный и после продолжений текст — отказ стадии: статья без последних разделов
// дороже повторного прогона, потому что её незаметно опубликуют.
func BuildArticleText(ctx context.Context, request ArticleTextRequest) (string, error) {
	logger := request.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	stage := request.Stage
	if stage == "" {
		stage = "article_generation"
	}
	answer, err := request.Send(ctx, request.Prompt)
	if err != nil {
		return "", err
	}
	text := NormalizeHeadings(answer)
	completeErr := ValidateArticleTextComplete(request.Structure, text)
	for attempt := 1; completeErr != nil && attempt <= textContinuations; attempt++ {
		if !errors.Is(completeErr, ErrTextIncomplete) || request.Continue == nil {
			break
		}
		text = trimIncompleteText(text)
		logger.Warn("текст статьи оборвался, просим модель дописать",
			"stage", stage, "attempt", attempt, "text_runes", len([]rune(text)),
			"error", completeErr)
		part, partErr := request.Continue(ctx, continueArticleTextPrompt(text))
		if partErr != nil {
			return "", partErr
		}
		if isTextDone(part) {
			logger.Info("модель считает текст законченным, продолжение не требуется",
				"stage", stage, "attempt", attempt)
			return text, nil
		}
		text = joinTextParts(text, NormalizeHeadings(part))
		completeErr = ValidateArticleTextComplete(request.Structure, text)
		if completeErr == nil {
			logger.Info("текст статьи дописан после обрыва",
				"stage", stage, "attempt", attempt, "text_runes", len([]rune(text)))
		}
	}
	if completeErr != nil {
		return "", completeErr
	}
	return text, nil
}

// TextChatStages перечисляет слоты чата под одну стадию текста: первое сообщение и
// продолжения. Роутер раздаёт стадии сообщениям по порядку, поэтому слоты под продолжения
// резервируются заранее, а неиспользованные снимает Chat.SkipStage.
func TextChatStages(stage string) []string {
	stages := make([]string, 0, textContinuations+1)
	for range textContinuations + 1 {
		stages = append(stages, stage)
	}
	return stages
}

// ValidateArticleTextComplete проверяет, что текст дошёл до конца статьи.
//
// Признака два, и они независимы. Первый — обрыв на полуслове: ответ, упёршийся в предел
// длины, рвётся посреди предложения, и законченный текст так не кончается. Второй — разделы:
// в тексте их не может быть меньше, чем в плане, по которому его писали. Одного первого мало:
// обрыв изредка приходится ровно на точку.
func ValidateArticleTextComplete(structure, text string) error {
	if tail := unfinishedTail(text); tail != "" {
		return fmt.Errorf("%w: последнее предложение не закончено («…%s»)", ErrTextIncomplete, tail)
	}
	want := CountSections(NormalizeHeadings(structure))
	if want == 0 {
		return nil
	}
	if got := CountSections(text); got < want {
		return fmt.Errorf("%w: разделов %d из %d", ErrTextIncomplete, got, want)
	}
	return nil
}

// CountSections считает разделы статьи — строки вида «H2 - Название».
//
// Считать имеет смысл после NormalizeHeadings: модель свободно переходит с «H2 - » на «H2:»
// и на Markdown, и до приведения к канону счёт разошёлся бы у двух версий одного текста.
func CountSections(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if sectionLineRE.MatchString(strings.TrimSuffix(line, "\r")) {
			count++
		}
	}
	return count
}

// unfinishedTail возвращает конец текста, если он оборван на полуслове, и пустую строку,
// если текст закончен.
func unfinishedTail(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			last = line
			break
		}
	}
	if last == "" {
		return ""
	}
	// Заголовок точкой не заканчивается и обрывом не считается: статья вправе кончиться
	// заголовком только по ошибке плана, а не по обрыву ответа.
	if sectionLineRE.MatchString(last) || headingLineRE.MatchString(last) {
		return ""
	}
	if sentenceEndRE.MatchString(last) {
		return ""
	}
	runes := []rune(last)
	if len(runes) > 40 {
		runes = runes[len(runes)-40:]
	}
	return string(runes)
}

// trimIncompleteText отбрасывает недописанное последнее предложение.
//
// Склеивать продолжение с серединой фразы нельзя: получится предложение из двух половин
// разных попыток. Отброшенное модель выдаст заново — место обрыва она получает явно.
func trimIncompleteText(text string) string {
	trimmed := strings.TrimSpace(text)
	if unfinishedTail(trimmed) == "" {
		return trimmed
	}
	cut := strings.LastIndexAny(trimmed, ".!?…")
	if cut < 0 {
		return trimmed
	}
	// LastIndexAny отдаёт байтовый индекс начала знака, а «…» занимает три байта: резать
	// нужно за ним целиком, иначе в тексте останется половина руны.
	_, size := utf8.DecodeRuneInString(trimmed[cut:])
	return strings.TrimSpace(trimmed[:cut+size])
}

// continueArticleTextPrompt просит дописать статью с места обрыва и называет это место.
//
// Промпт технический: он не описывает статью и не спорит с промптом стадии, а сообщает, что
// ответ оборвался, и где именно. Поэтому он живёт рядом со склейкой, а не в каталоге задачи.
func continueArticleTextPrompt(accepted string) string {
	tail := []rune(strings.TrimSpace(accepted))
	if len(tail) > textTailRunes {
		tail = tail[len(tail)-textTailRunes:]
	}
	return fmt.Sprintf(`Твой ответ оборвался: статья написана не до конца.

Вот чем заканчивается уже принятый текст:

%s

Продолжи ровно с этого места — допиши оставшиеся разделы по тому же плану и в той же форме записи заголовков («H2 - Название», «H3 - Название»). Не повторяй выданное, не начинай статью заново, не пиши вступлений и комментариев.

Если статья на самом деле уже дописана до конца, ответь одним словом: %s`, string(tail), textDonePhrase)
}

// isTextDone узнаёт ответ «статья уже закончена».
func isTextDone(part string) bool {
	cleaned := strings.TrimSpace(strings.Trim(strings.TrimSpace(part), ".!*_"))
	return strings.EqualFold(cleaned, textDonePhrase)
}

// joinTextParts приклеивает продолжение к принятому тексту, сняв повтор на стыке.
func joinTextParts(accepted, part string) string {
	return strings.TrimSpace(accepted) + "\n\n" + strings.TrimSpace(dropTextOverlap(accepted, part))
}

// dropTextOverlap снимает с начала продолжения то, чем уже заканчивается принятый текст.
func dropTextOverlap(accepted, part string) string {
	tail := []rune(strings.TrimSpace(accepted))
	head := []rune(strings.TrimSpace(part))
	limit := min(len(tail), len(head), textOverlapRunes)
	for size := limit; size > 0; size-- {
		if string(tail[len(tail)-size:]) == string(head[:size]) {
			return string(head[size:])
		}
	}
	return part
}
