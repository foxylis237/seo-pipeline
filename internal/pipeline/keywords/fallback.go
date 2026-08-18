// Package keywords supplies raw search queries when Keys.so returned none for the article.
//
// Пакет заменяет только ИСТОЧНИК исходных запросов — первый этап Keys.so. Очистка, сохранение
// cleaned_keywords и всё, что идёт после, остаются прежними: результат отсюда попадает ровно
// в ту же очистку Keys.so, что и обычный сбор с сайта конкурента.
//
// Частотности здесь нет и быть не должно. Первый этап Keys.so её тоже не даёт: единственный
// источник частотностей в пайплайне — Wordstat на этапе Arsenkin, и колонка wordstat_keywords
// заполняется только оттуда.
package keywords

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"unicode"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

// StageName — имя LLM-стадии резервного подбора запросов. Стадия настраивается там же, где
// стадии генерации (config/config.yaml), и идёт через тот же Router.
const StageName = "keywords"

// MaxKeywords — верхняя граница числа запросов, о которой просит промпт. Ответ модели ей не
// подчиняется автоматически, поэтому граница проверяется ещё раз при разборе.
const MaxKeywords = 49

// StageError передаёт вызывающему контекст отказа резервного источника, чтобы классификация
// шла по типу, а не по подстроке сообщения.
type StageError struct {
	ArticleID int64
	Stage     string
	Err       error
}

func (e *StageError) Error() string {
	return fmt.Sprintf("резервный подбор запросов stage=%s: %v", e.Stage, e.Err)
}

func (e *StageError) Unwrap() error { return e.Err }

// Generator выполняет одну настроенную LLM-стадию. Интерфейс объявлен у потребителя и держится
// узким: *llm.Router удовлетворяет ему как есть.
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

// RawKeywords возвращает исходные запросы в том же виде, в каком их отдаёт первый этап
// Keys.so, — плоским списком фраз. Ошибка возвращается вместо пустого списка: пустой список
// уехал бы в Arsenkin и превратился в бессмысленный прогон Wordstat.
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
	// ArticleID намеренно нулевой: в режиме одного диалога на статью ненулевой id открыл бы
	// беседу этой статьи и сделал подбор запросов её первым сообщением. Стадии генерации
	// опираются на историю диалога, и посторонний первый ход им не нужен.
	response, err := f.generator.Generate(ctx, llm.Call{
		Stage: StageName,
		Data:  promptData{ArticleName: articleName},
	})
	if err != nil {
		return nil, &StageError{ArticleID: f.articleID, Stage: "generate", Err: err}
	}
	queries := Parse(response.Text)
	if len(queries) == 0 {
		return nil, &StageError{ArticleID: f.articleID, Stage: "parse_answer", Err: fmt.Errorf(
			"в ответе модели нет ни одного пригодного запроса (получено %d символов)",
			len([]rune(response.Text)),
		)}
	}
	f.logger.Info("резервный подбор запросов выполнен",
		"provider", response.Provider, "model", response.Model, "keywords_count", len(queries))
	return queries, nil
}

func (f *Fallback) articleIDSafe() int64 {
	if f == nil {
		return 0
	}
	return f.articleID
}

// Parse разбирает ответ модели: один запрос — одна строка.
//
// Порядок строк сохраняется: промпт просит сортировку по убыванию популярности, и обрезка по
// MaxKeywords должна отбрасывать наименее популярные, а не случайные. Всё, что не укладывается
// в формат, отбрасывается молча: ответ модели — не протокол, и одна испорченная строка не
// повод терять остальные. Пустой результат означает, что пригодных строк в ответе нет вовсе,
// и решение об ошибке принимает вызывающий.
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

// IsPlainQuery проверяет требование промпта: в запросе только буквы, цифры и пробелы.
//
// Проверка не косметическая. Она же отсекает нумерацию, маркеры списков, Markdown-обёртку и
// пояснения — всё, что промпт запрещает. И она же закрывает известную ловушку: операторные
// символы во фразе («/», «-») роняют задачу Wordstat молча, форма принимает список, а задача
// не создаётся вовсе. Поэтому её спрашивает и ручная вставка запросов: там ответ модели ни
// при чём, а ловушка Wordstat та же.
func IsPlainQuery(query string) bool {
	for _, symbol := range query {
		if unicode.IsLetter(symbol) || unicode.IsDigit(symbol) || symbol == ' ' {
			continue
		}
		return false
	}
	return true
}
