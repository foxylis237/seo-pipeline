// Package keywords supplies raw search queries when Keys.so returned none for the article.
//
// Пакет заменяет только первый этап Keys.so: результат идёт в ту же очистку. Частотностей
// здесь нет — их даёт только Wordstat на этапе Arsenkin.
package keywords

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"unicode"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

// StageName — имя LLM-стадии резервного подбора запросов.
const StageName = "keywords"

// MaxKeywords — верхняя граница числа запросов; промпт просит её, разбор проверяет ещё раз.
const MaxKeywords = 49

// StageError передаёт вызывающему контекст отказа резервного источника.
type StageError struct {
	ArticleID int64
	Stage     string
	Err       error
}

func (e *StageError) Error() string {
	return fmt.Sprintf("резервный подбор запросов stage=%s: %v", e.Stage, e.Err)
}

func (e *StageError) Unwrap() error { return e.Err }

// Generator выполняет одну настроенную LLM-стадию; *llm.Router удовлетворяет ему как есть.
type Generator interface {
	Generate(ctx context.Context, call llm.Call) (llm.RoutedResponse, error)
}

// Fallback подбирает запросы через настроенную LLM-стадию.
type Fallback struct {
	generator Generator
	articleID int64
	logger    *slog.Logger
}

// NewFallback создаёт резервный источник запросов одной статьи.
func NewFallback(generator Generator, articleID int64, logger *slog.Logger) *Fallback {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Fallback{
		generator: generator,
		articleID: articleID,
		logger:    logger.With("article_id", articleID, "stage", StageName),
	}
}

// promptData — поля шаблона стадии keywords (prompts/keywords.txt каталога задачи).
type promptData struct {
	ArticleName string
}

// RawKeywords возвращает исходные запросы плоским списком фраз, как первый этап Keys.so;
// вместо пустого списка — ошибка.
func (f *Fallback) RawKeywords(ctx context.Context, articleName string) ([]string, error) {
	if f == nil || f.generator == nil {
		return nil, &StageError{ArticleID: f.articleIDSafe(), Stage: "configure",
			Err: fmt.Errorf("резервный источник запросов не настроен")}
	}
	if strings.TrimSpace(articleName) == "" {
		return nil, &StageError{ArticleID: f.articleID, Stage: "validate_article_name",
			Err: fmt.Errorf("у статьи пустое название")}
	}
	f.logger.Info("Keys.so не дал исходных запросов, запускается резервный подбор",
		"article_name", articleName)
	// ArticleID нулевой: иначе в режиме одного диалога на статью подбор стал бы первым
	// сообщением её беседы. Непригодный ответ спрашивается ещё раз.
	var lastErr error
	for attempt := 1; attempt <= parseAttempts; attempt++ {
		response, err := f.generator.Generate(ctx, llm.Call{
			Stage: StageName,
			Data:  promptData{ArticleName: articleName},
		})
		if err != nil {
			return nil, &StageError{ArticleID: f.articleID, Stage: "generate", Err: err}
		}
		queries := Parse(response.Text)
		if len(queries) > 0 {
			f.logger.Info("резервный подбор запросов выполнен", "provider", response.Provider,
				"model", response.Model, "keywords_count", len(queries), "attempt", attempt)
			return queries, nil
		}
		lastErr = &StageError{ArticleID: f.articleID, Stage: "parse_answer", Err: fmt.Errorf(
			"в ответе модели нет ни одного пригодного запроса (получено %d символов, попыток: %d, начало ответа: %q)",
			len([]rune(response.Text)), attempt, answerSample(response.Text),
		)}
		f.logger.Warn("резервный подбор: в ответе нет пригодных запросов",
			"attempt", attempt, "max_attempts", parseAttempts, "error", lastErr)
	}
	return nil, lastErr
}

// parseAttempts — сколько раз спрашивать модель, пока ответ не даст ни одного запроса.
const parseAttempts = 2

// answerSampleRunes ограничивает выдержку из непригодного ответа в тексте ошибки.
const answerSampleRunes = 200

func answerSample(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > answerSampleRunes {
		return string(runes[:answerSampleRunes]) + "…"
	}
	return string(runes)
}

func (f *Fallback) articleIDSafe() int64 {
	if f == nil {
		return 0
	}
	return f.articleID
}

// Parse разбирает ответ модели: один запрос — одна строка. Порядок сохраняется (промпт
// сортирует по популярности, обрезка по MaxKeywords режет хвост), непригодные строки отбрасываются.
func Parse(answer string) []string {
	lines := strings.Split(strings.ReplaceAll(answer, "\r\n", "\n"), "\n")
	queries := make([]string, 0, MaxKeywords)
	for _, line := range lines {
		query := strings.TrimSpace(line)
		if query == "" || !IsPlainQuery(query) {
			continue
		}
		queries = append(queries, query)
		if len(queries) == MaxKeywords {
			break
		}
	}
	return queries
}

// IsPlainQuery проверяет, что в запросе только буквы, цифры и пробелы: так отсекаются
// нумерация, маркеры и пояснения, а заодно операторные символы, на которых молчит Wordstat.
func IsPlainQuery(query string) bool {
	for _, symbol := range query {
		if unicode.IsLetter(symbol) || unicode.IsDigit(symbol) || symbol == ' ' {
			continue
		}
		return false
	}
	return true
}
