package generation

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"regexp"
	"slices"
	"strings"
)

// ErrHTMLIncomplete — разметка оборвалась и до конца страницы не дошла.
//
// Отдельный тип нужен затем, что обрыв лечится продолжением того же чата, а остальные отказы
// стадии — нет. Классификация по типу, а не по тексту сообщения, — общее правило проекта.
var ErrHTMLIncomplete = errors.New("разметка не покрывает страницу целиком")

const (
	// htmlContinuations — сколько раз просить модель дописать оборванную разметку.
	//
	// Один ответ веб-интерфейса DeepSeek упирается в предел длины около 27 000 символов:
	// замеры по сохранённым статьям показывают обрывы ровно на этой границе, а страницы
	// pprof_2 в разметке дают в полтора раза больше символов, чем в тексте. Двух продолжений
	// хватает странице любой длины из тех, что задачи пишут сегодня.
	htmlContinuations = 2

	// coverageProbeWords — сколько первых слов последнего абзаца страницы искать в разметке.
	// Восьми хватает, чтобы не совпасть случайно, и мало, чтобы пережить мелкую правку конца
	// предложения, которую промпт разметки разрешает.
	coverageProbeWords = 8

	// htmlTailRunes — сколько символов конца принятой разметки показать модели, прося её
	// продолжить. Место обрыва называется явно: модель не обязана помнить, где остановилась,
	// а склейка не имеет права ни потерять кусок страницы, ни повторить его.
	htmlTailRunes = 400

	// htmlOverlapRunes — какой длины повтор снимать на стыке частей, если модель всё же
	// начала продолжение с уже выданного хвоста.
	htmlOverlapRunes = 600
)

var (
	// htmlClosingTagRE находит закрывающий тег: по нему обрезается недописанный элемент.
	htmlClosingTagRE = regexp.MustCompile(`(?is)</[a-z][a-z0-9]*\s*>`)
	// textNoiseRE оставляет в тексте только буквы, цифры и пробелы: сверяется смысл, а не
	// пунктуация и не типографика.
	textNoiseRE = regexp.MustCompile(`[^\p{L}\p{N}\s]+`)
	textSpaceRE = regexp.MustCompile(`\s+`)
	textTagRE   = regexp.MustCompile(`(?is)<[^>]*>`)
)

// HTMLMessage — одно сообщение чата разметки: первое или продолжение.
type HTMLMessage func(ctx context.Context, prompt string) (string, error)

// HTMLPageRequest — один прогон стадии html.
//
// Поток задачи даёт текст страницы, промпт стадии и способ отправить сообщение; правило
// «разметка обязана дойти до конца страницы» живёт здесь, одно на все задачи.
type HTMLPageRequest struct {
	// Page — исходный текст страницы, тот самый, что ушёл в промпт.
	Page string
	// Prompt — отрендеренный промпт стадии html.
	Prompt string
	// Send отправляет первое сообщение чата, Continue — следующее в том же чате. Без
	// Continue оборванная разметка остаётся отказом стадии, дописывать её нечем.
	Send     HTMLMessage
	Continue HTMLMessage
	Logger   *slog.Logger
	// Complete отвечает, дошёл ли ответ до конца. nil — правило по умолчанию: последний
	// абзац исходного текста обязан найтись в разметке (ValidateHTMLCoversPage).
	//
	// Своё правило нужно задаче, у которой ответ модели не обязан повторять исходный текст
	// дословно. Так у pprof_fix_1: он не размечает написанное, а правит уже опубликованное,
	// и последний абзац редактура вправе переписать — признаком обрыва там служит структура
	// статьи, а не совпадение хвоста. Ошибка обязана оборачивать ErrHTMLIncomplete, иначе
	// продолжение чата не начнётся.
	Complete func(markup string) error
}

// BuildHTMLPage получает разметку страницы и дописывает её, если ответ модели оборвался.
//
// Обрыв ответа — не редкость, а рядовое поведение веб-интерфейса: длинный ответ упирается в
// предел длины сообщения, а прерванный стрим (уснувшая машина, потерянная сеть) оставляет на
// странице ровно то, что успело прийти. И в том, и в другом случае разметка внешне исправна —
// теги закрыты, заголовки и абзацы на месте, — поэтому обычные проверки HTML её пропускают, и
// в блог уходит половина страницы. Здесь обрыв опознаётся по исходному тексту и лечится
// продолжением того же чата.
func BuildHTMLPage(ctx context.Context, request HTMLPageRequest) (string, error) {
	logger := request.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	answer, err := request.Send(ctx, request.Prompt)
	if err != nil {
		return "", err
	}
	page, err := NormalizeHTML(answer)
	if err != nil {
		return "", err
	}
	complete := request.Complete
	if complete == nil {
		complete = func(markup string) error { return ValidateHTMLCoversPage(request.Page, markup) }
	}
	coverErr := complete(page)
	for attempt := 1; coverErr != nil && attempt <= htmlContinuations; attempt++ {
		if !errors.Is(coverErr, ErrHTMLIncomplete) || request.Continue == nil {
			break
		}
		page = trimIncompleteHTML(page)
		logger.Warn("разметка оборвалась, просим модель дописать страницу",
			"stage", "html_generation", "attempt", attempt, "html_runes", len([]rune(page)))
		part, partErr := request.Continue(ctx, continueHTMLPrompt(page))
		if partErr != nil {
			return "", partErr
		}
		cleaned, partErr := NormalizeHTMLPart(part)
		if partErr != nil {
			return "", partErr
		}
		page, partErr = joinHTMLParts(request.Page, page, cleaned)
		if partErr != nil {
			return "", partErr
		}
		coverErr = complete(page)
		if coverErr == nil {
			logger.Info("страница дописана после обрыва",
				"stage", "html_generation", "attempt", attempt, "html_runes", len([]rune(page)))
		}
	}
	if coverErr != nil {
		return "", coverErr
	}
	return page, nil
}

// HTMLChatStages перечисляет стадии чата разметки: первое сообщение и продолжения.
//
// Чат роутера принимает столько сообщений, сколько стадий ему назвали при создании, а
// продолжение идёт той же стадией html — с её провайдером, режимом и сроками.
func HTMLChatStages(stage string) []string {
	stages := make([]string, 0, htmlContinuations+1)
	for range htmlContinuations + 1 {
		stages = append(stages, stage)
	}
	return stages
}

// ValidateHTMLCoversPage проверяет, что разметка дошла до конца страницы.
//
// Признак — последний абзац исходного текста: обрыв всегда съедает конец, а промпт разметки
// запрещает выбрасывать текст. Сверяются первые слова абзаца, очищенные от тегов, пунктуации
// и регистра: вёрстка вправе разбить абзац или перенести его в блок, но не переписать.
//
// Страница без единого длинного абзаца проверку проходит молча: сверять нечего, и выдумывать
// отказ на пустом месте нельзя.
func ValidateHTMLCoversPage(page, markup string) error {
	probe := pageClosingProbe(page)
	if probe == "" {
		return nil
	}
	if strings.Contains(normalizedText(markup), probe) {
		return nil
	}
	return fmt.Errorf("%w: последнего абзаца страницы («%s…») в ней нет", ErrHTMLIncomplete, probe)
}

// NormalizeHTMLPart готовит продолжение разметки.
//
// От целой страницы отличается только требованием к содержанию: у продолжения его нет.
// Хвост страницы вправе оказаться одним закрывающим блоком, и требовать от него заголовок
// или абзац значило бы отказаться от почти собранной страницы.
func NormalizeHTMLPart(value string) (string, error) {
	part, _, err := cleanHTMLAnswer(value)
	return part, err
}

// continueHTMLPrompt просит дописать страницу с места обрыва и называет это место.
//
// Промпт технический: он не описывает вёрстку и не спорит с промптом стадии, а сообщает, что
// ответ оборвался, и где именно. Поэтому он живёт рядом со склейкой, а не в каталоге задачи:
// у всех задач он один и тот же, и расходиться ему незачем.
func continueHTMLPrompt(accepted string) string {
	tail := []rune(strings.TrimSpace(accepted))
	if len(tail) > htmlTailRunes {
		tail = tail[len(tail)-htmlTailRunes:]
	}
	return fmt.Sprintf(`Твой ответ оборвался: страница размечена не до конца.

Вот чем заканчивается уже принятая разметка:

%s

Продолжи ровно с этого места — выведи следующий элемент страницы и всё, что идёт за ним, по тому же регламенту. Не повторяй выданное, не начинай страницу заново, не пиши вступлений и комментариев: в ответе только HTML продолжения.`, string(tail))
}

// trimIncompleteHTML отбрасывает недописанный последний элемент.
//
// Обрыв приходится на середину тега или текста, и склеивать продолжение с такой серединой
// нельзя: элемент останется без содержимого или без закрывающего тега. Отброшенное модель
// выдаст заново — место обрыва она получает явно.
func trimIncompleteHTML(markup string) string {
	closings := htmlClosingTagRE.FindAllStringIndex(markup, -1)
	if len(closings) == 0 {
		return strings.TrimSpace(markup)
	}
	return strings.TrimSpace(markup[:closings[len(closings)-1][1]])
}

// joinHTMLParts приклеивает продолжение к принятой разметке.
//
// Повтор на стыке снимается: модель нередко начинает продолжение с последних выданных строк.
// Продолжение, начатое с начала страницы, — отказ: склеить его значит выдать страницу дважды,
// и проверка покрытия такую склейку пропустит.
func joinHTMLParts(page, accepted, part string) (string, error) {
	if opening := pageOpeningProbe(page); opening != "" && strings.Contains(normalizedText(part), opening) {
		return "", fmt.Errorf("%w: продолжение начало страницу заново, а не с места обрыва", ErrHTMLIncomplete)
	}
	return strings.TrimSpace(accepted) + "\n" + strings.TrimSpace(dropOverlap(accepted, part)), nil
}

// dropOverlap снимает с начала продолжения то, чем уже заканчивается принятая разметка.
func dropOverlap(accepted, part string) string {
	tail := []rune(strings.TrimSpace(accepted))
	head := []rune(strings.TrimSpace(part))
	limit := min(len(tail), len(head), htmlOverlapRunes)
	for size := limit; size > 0; size-- {
		if string(tail[len(tail)-size:]) == string(head[:size]) {
			return string(head[size:])
		}
	}
	return part
}

// pageClosingProbe возвращает начало последнего содержательного абзаца страницы.
func pageClosingProbe(page string) string {
	for _, line := range slices.Backward(strings.Split(page, "\n")) {
		if probe := lineProbe(line); probe != "" {
			return probe
		}
	}
	return ""
}

// pageOpeningProbe возвращает начало первого содержательного абзаца страницы.
func pageOpeningProbe(page string) string {
	for line := range strings.SplitSeq(page, "\n") {
		if probe := lineProbe(line); probe != "" {
			return probe
		}
	}
	return ""
}

// lineProbe отдаёт первые слова строки, если она достаточно длинная, чтобы быть абзацем.
// Короткие строки — заголовки, подписи и разделители — приметой конца страницы служить не
// могут: заголовок вёрстка вправе унести в карточку и оставить без своего тега.
func lineProbe(line string) string {
	words := strings.Fields(normalizedText(line))
	if len(words) < coverageProbeWords {
		return ""
	}
	return strings.Join(words[:coverageProbeWords], " ")
}

// normalizedText сводит текст и разметку к одному виду: без тегов, без пунктуации, без
// регистра и без разницы между «ё» и «е».
func normalizedText(value string) string {
	text := html.UnescapeString(textTagRE.ReplaceAllString(value, " "))
	text = strings.NewReplacer("ё", "е", "Ё", "Е").Replace(text)
	text = textNoiseRE.ReplaceAllString(text, " ")
	return strings.ToLower(strings.TrimSpace(textSpaceRE.ReplaceAllString(text, " ")))
}
