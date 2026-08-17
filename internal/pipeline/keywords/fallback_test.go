package keywords

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

// fakeGenerator подменяет Router: настоящий запрос к модели тесту не нужен, нужен разбор
// того, что она ответила.
type fakeGenerator struct {
	answer string
	err    error
	calls  int
	call   llm.Call
}

func (g *fakeGenerator) Generate(_ context.Context, call llm.Call) (llm.RoutedResponse, error) {
	g.calls++
	g.call = call
	if g.err != nil {
		return llm.RoutedResponse{}, g.err
	}
	return llm.RoutedResponse{
		Response: llm.Response{Text: g.answer}, Provider: "deepseek_web", Model: "deepseek-web",
	}, nil
}

// TestParseReadsOneQueryPerLine: формат ответа — плоский список фраз, ровно тот же тип, что
// отдаёт первый этап Keys.so. Порядок сохраняется — промпт просит сортировку по убыванию
// популярности, и переставлять её разбор не должен.
func TestParseReadsOneQueryPerLine(t *testing.T) {
	answer := "смена профессии в 40 лет\nкак сменить профессию\nновая профессия после 40\n"

	queries := Parse(answer)

	want := []string{"смена профессии в 40 лет", "как сменить профессию", "новая профессия после 40"}
	if !reflect.DeepEqual(queries, want) {
		t.Fatalf("queries = %v, want %v", queries, want)
	}
}

func TestParseDropsEmptyLines(t *testing.T) {
	answer := "\n\nобучение бариста\n   \n\nработа бариста\n\n"

	queries := Parse(answer)

	want := []string{"обучение бариста", "работа бариста"}
	if !reflect.DeepEqual(queries, want) {
		t.Fatalf("queries = %v, want %v", queries, want)
	}
}

// TestParseDropsLinesWithSpecialCharacters: промпт требует только буквы, цифры и пробелы.
// Та же проверка снимает нумерацию, маркеры списков, Markdown и пояснения — и закрывает
// известную ловушку Wordstat, где фраза с «/» или «-» не создаёт задачу вовсе.
func TestParseDropsLinesWithSpecialCharacters(t *testing.T) {
	answer := strings.Join([]string{
		"```",
		"Вот подобранное семантическое ядро:",
		"1. обучение бариста",
		"- работа бариста",
		"курсы бариста / обучение",
		"повар-кондитер",
		"кофе 3 в 1",
		"обучение бариста с нуля",
		"```",
	}, "\n")

	queries := Parse(answer)

	want := []string{"кофе 3 в 1", "обучение бариста с нуля"}
	if !reflect.DeepEqual(queries, want) {
		t.Fatalf("queries = %v, want %v", queries, want)
	}
}

func TestParseKeepsAtMost49Queries(t *testing.T) {
	var answer strings.Builder
	for index := range 80 {
		fmt.Fprintf(&answer, "запрос %02d\n", index)
	}

	queries := Parse(answer.String())

	if len(queries) != MaxKeywords {
		t.Fatalf("разобрано %d запросов, want %d", len(queries), MaxKeywords)
	}
	// Обрезаются последние, а не случайные: модель сортирует по убыванию популярности,
	// и лишними должны оказаться наименее популярные.
	if queries[0] != "запрос 00" || queries[MaxKeywords-1] != "запрос 48" {
		t.Fatalf("границы среза: %q ... %q", queries[0], queries[MaxKeywords-1])
	}
}

func TestRawKeywordsRendersArticleNameAndReturnsQueries(t *testing.T) {
	generator := &fakeGenerator{answer: "обучение бариста\nработа бариста\n"}
	fallback := NewFallback(generator, 7, nil)

	queries, err := fallback.RawKeywords(context.Background(), "Как стать бариста")
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(queries, []string{"обучение бариста", "работа бариста"}) {
		t.Fatalf("queries = %v", queries)
	}
	if generator.call.Stage != StageName {
		t.Fatalf("стадия запроса = %q, want %q", generator.call.Stage, StageName)
	}
	data, ok := generator.call.Data.(promptData)
	if !ok || data.ArticleName != "Как стать бариста" {
		t.Fatalf("данные промпта = %+v", generator.call.Data)
	}
	// Нулевой article_id обязателен: в режиме одного диалога на статью ненулевой открыл бы
	// беседу этой статьи и сделал подбор запросов её первым сообщением.
	if generator.call.ArticleID != 0 {
		t.Fatalf("article_id запроса = %d, want 0", generator.call.ArticleID)
	}
}

func TestRawKeywordsFailsWhenAnswerHasNoUsableQueries(t *testing.T) {
	for name, answer := range map[string]string{
		"пустой ответ":      "",
		"только пробелы":    "\n   \n\t\n",
		"мусор без формата": "Извините, я не могу выполнить этот запрос.",
		"markdown-таблица":  "| запрос | частотность |\n|---|---|\n| бариста | 900 |",
		"нумерованный спис": "1. обучение бариста\n2. работа бариста",
	} {
		t.Run(name, func(t *testing.T) {
			fallback := NewFallback(&fakeGenerator{answer: answer}, 7, nil)

			queries, err := fallback.RawKeywords(context.Background(), "Как стать бариста")

			if err == nil {
				t.Fatalf("пустой разбор не привёл к ошибке, queries = %v", queries)
			}
			if len(queries) != 0 {
				t.Fatalf("вместе с ошибкой вернулись запросы: %v", queries)
			}
			var stageErr *StageError
			if !errors.As(err, &stageErr) || stageErr.Stage != "parse_answer" {
				t.Fatalf("ошибка = %v, want StageError со stage=parse_answer", err)
			}
		})
	}
}

func TestRawKeywordsWrapsGeneratorFailure(t *testing.T) {
	wantErr := errors.New("DeepSeek недоступен")
	fallback := NewFallback(&fakeGenerator{err: wantErr}, 7, nil)

	_, err := fallback.RawKeywords(context.Background(), "Как стать бариста")

	if !errors.Is(err, wantErr) {
		t.Fatalf("ошибка = %v, want %v", err, wantErr)
	}
	var stageErr *StageError
	if !errors.As(err, &stageErr) || stageErr.Stage != "generate" {
		t.Fatalf("ошибка = %v, want StageError со stage=generate", err)
	}
}

func TestRawKeywordsRejectsEmptyArticleName(t *testing.T) {
	generator := &fakeGenerator{answer: "обучение бариста\n"}
	fallback := NewFallback(generator, 7, nil)

	if _, err := fallback.RawKeywords(context.Background(), "  "); err == nil {
		t.Fatal("пустое название статьи не остановило запрос к модели")
	}
	if generator.calls != 0 {
		t.Fatalf("модель вызвана %d раз при пустом названии статьи", generator.calls)
	}
}
