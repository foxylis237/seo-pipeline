package keysso

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestWaitKeywordsResultsRetriesAtMostThreeTimes(t *testing.T) {
	tests := []struct {
		name          string
		failures      int
		wantAttempts  int
		wantRefreshes int
	}{
		{name: "first attempt", failures: 0, wantAttempts: 1},
		{name: "one retry", failures: 1, wantAttempts: 2, wantRefreshes: 1},
		{name: "two retries", failures: 2, wantAttempts: 3, wantRefreshes: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			service := New(Config{ArticleID: 7}, slog.New(slog.NewTextHandler(&logs, nil)))
			attempts := 0
			refreshes := 0
			captures := 0
			service.waitKeywordsResultsHook = func(context.Context) error {
				attempts++
				if attempts <= test.failures {
					return errors.New("playwright table timeout")
				}
				return nil
			}
			service.refreshKeywordsResultsHook = func(context.Context) error {
				refreshes++
				return nil
			}
			service.saveDebugArtifactsHook = func(string, int, int, error) { captures++ }

			if err := service.waitKeywordsResults(context.Background()); err != nil {
				t.Fatal(err)
			}
			if attempts != test.wantAttempts || refreshes != test.wantRefreshes {
				t.Fatalf("attempts=%d refreshes=%d, want %d/%d", attempts, refreshes, test.wantAttempts, test.wantRefreshes)
			}
			if captures != test.failures {
				t.Fatalf("debug captures=%d, want %d", captures, test.failures)
			}
			if !strings.Contains(logs.String(), "attempt="+strconv.Itoa(test.wantAttempts)) || !strings.Contains(logs.String(), "keywords table loaded") {
				t.Fatalf("attempt/success log is missing:\n%s", logs.String())
			}
		})
	}
}

func TestWaitKeywordsResultsReturnsOriginalErrorAfterThreeAttempts(t *testing.T) {
	var logs bytes.Buffer
	service := New(Config{ArticleID: 9}, slog.New(slog.NewTextHandler(&logs, nil)))
	original := errors.New("playwright: timeout: Timeout 60000ms exceeded")
	attempts := 0
	refreshes := 0
	captures := 0
	service.waitKeywordsResultsHook = func(context.Context) error {
		attempts++
		return original
	}
	service.refreshKeywordsResultsHook = func(context.Context) error {
		refreshes++
		return nil
	}
	service.saveDebugArtifactsHook = func(stage string, attempt, maxAttempts int, err error) {
		captures++
		if stage != "wait_search_results" || attempt != captures || maxAttempts != 3 || !errors.Is(err, original) {
			t.Fatalf("capture %d: stage=%q attempt=%d max=%d err=%v", captures, stage, attempt, maxAttempts, err)
		}
	}

	err := service.waitKeywordsResults(context.Background())
	if !errors.Is(err, original) {
		t.Fatalf("error %v does not preserve original %v", err, original)
	}
	var waitErr *keywordsTableWaitError
	if !errors.As(err, &waitErr) {
		t.Fatalf("error type = %T, want *keywordsTableWaitError", err)
	}
	if attempts != 3 || refreshes != 2 || captures != 3 || waitErr.Attempts != 3 || len(waitErr.AttemptDurations) != 3 {
		t.Fatalf("attempts=%d refreshes=%d error=%+v", attempts, refreshes, waitErr)
	}
	for _, expected := range []string{"after 3 attempts", keywordsTableSelector, "attempt_durations", "failed after 3 attempts", "attempt=3"} {
		if !strings.Contains(err.Error()+logs.String(), expected) {
			t.Fatalf("missing %q in error/logs:\nerror=%v\nlogs=%s", expected, err, logs.String())
		}
	}
}

func TestNoDataStopsWithoutRetry(t *testing.T) {
	service := New(Config{ArticleID: 29}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	attempts := 0
	refreshes := 0
	service.waitKeywordsResultsHook = func(context.Context) error {
		attempts++
		return &resultError{Kind: resultNoData, Retryable: false, Err: errors.New("Keys.so returned no data")}
	}
	service.refreshKeywordsResultsHook = func(context.Context) error { refreshes++; return nil }
	service.saveDebugArtifactsHook = func(string, int, int, error) {}
	err := service.waitKeywordsResults(context.Background())
	var classified *resultError
	if !errors.As(err, &classified) || classified.Kind != resultNoData || classified.Retryable || attempts != 1 || refreshes != 0 {
		t.Fatalf("err=%v attempts=%d refreshes=%d", err, attempts, refreshes)
	}
}

func TestMaintenanceRemainsRetryable(t *testing.T) {
	service := New(Config{ArticleID: 29}, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	attempts := 0
	refreshes := 0
	service.waitKeywordsResultsHook = func(context.Context) error {
		attempts++
		return &resultError{Kind: resultMaintenance, Retryable: true, Err: errors.New("Keys.so is unavailable: maintenance page detected")}
	}
	service.refreshKeywordsResultsHook = func(context.Context) error { refreshes++; return nil }
	service.saveDebugArtifactsHook = func(string, int, int, error) {}
	err := service.waitKeywordsResults(context.Background())
	var classified *resultError
	if !errors.As(err, &classified) || classified.Kind != resultMaintenance || !classified.Retryable || attempts != 3 || refreshes != 2 {
		t.Fatalf("err=%v attempts=%d refreshes=%d", err, attempts, refreshes)
	}
}

func TestNavigationErrorPreservesChromiumCause(t *testing.T) {
	original := errors.New("playwright: net::ERR_CONNECTION_RESET")
	err := &resultError{Kind: resultNavigationError, Retryable: true, Err: fmt.Errorf("Keys.so navigation failed: %w", original)}
	if !errors.Is(err, original) || !strings.Contains(err.Error(), "net::ERR_CONNECTION_RESET") {
		t.Fatalf("navigation error lost original cause: %v", err)
	}
}

func TestExpectedResultsPageIgnoresFragmentAndRejectsChromeError(t *testing.T) {
	if !isExpectedKeysSOPage("https://www.keys.so/ru/keysbypage?domain=test.ru#page=1", "/ru/keysbypage") {
		t.Fatal("valid Keys.so results page was rejected")
	}
	if isExpectedKeysSOPage("chrome-error://chromewebdata/", "/ru/keysbypage") {
		t.Fatal("chrome error page must never be reloaded as Keys.so")
	}
	if isExpectedKeysSOPage("https://www.keys.so/ru/login", "/ru/keysbypage") {
		t.Fatal("unexpected Keys.so page was accepted")
	}
}

func TestParseQueriesPreservesOrderAndRemovesEmptyLines(t *testing.T) {
	got := parseQueries("  первый запрос\r\n\r\nвторой запрос  \n третий запрос ")
	want := []string{"первый запрос", "второй запрос", "третий запрос"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseQueries() = %#v, want %#v", got, want)
	}
}

func TestNormalizeQueriesDoesNotSortOrDeduplicate(t *testing.T) {
	got := normalizeQueries([]string{"второй", "первый", "второй"})
	want := []string{"второй", "первый", "второй"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeQueries() = %#v, want %#v", got, want)
	}
}

func TestSamePageIgnoresTrailingSlashAndQuery(t *testing.T) {
	if !samePage(
		"https://www.keys.so/ru/?source=profile",
		"https://www.keys.so/ru/",
	) {
		t.Fatal("expected URLs to identify the same Keys.so page")
	}
}

func TestSamePageRejectsDifferentPath(t *testing.T) {
	if samePage(
		"https://www.keys.so/ru/login",
		"https://www.keys.so/ru/",
	) {
		t.Fatal("different Keys.so paths must not be treated as the same page")
	}
}

func TestDiagnosticDataRedactsConfiguredCredentials(t *testing.T) {
	html := `<input value="user@example.test"><input value="secret-password">`
	redacted := redactDiagnosticHTML(html, "user@example.test", "secret-password")
	if strings.Contains(redacted, "user@example.test") || strings.Contains(redacted, "secret-password") {
		t.Fatalf("credentials were not redacted: %s", redacted)
	}
	err := errors.New("Authorization: Bearer secret-token")
	if got := safeDiagnosticError(err); got != "sensitive error details redacted" {
		t.Fatalf("safeDiagnosticError() = %q", got)
	}
}

// TestNoRawKeywordsDistinguishesEmptyResultFromFailure: по этому признаку вызывающий решает,
// искать ли запросы в другом источнике. Ошибка приезжает к нему завёрнутой в StageError,
// поэтому проверяется вся цепочка, а не голый sentinel.
func TestNoRawKeywordsDistinguishesEmptyResultFromFailure(t *testing.T) {
	empty := &StageError{
		Stage: "collect_competitor_queries",
		Err: &resultError{Kind: resultNoData, Retryable: false,
			Err: fmt.Errorf("%w: таблица запросов конкурента пуста", ErrNoRawKeywords)},
	}
	if !NoRawKeywords(empty) {
		t.Fatal("пустой результат Keys.so не опознан как отсутствие исходных запросов")
	}

	for name, err := range map[string]error{
		"таймаут": &StageError{Stage: "wait_search_results",
			Err: &resultError{Kind: resultTimeout, Retryable: true, Err: errors.New("timeout")}},
		"технические работы": &StageError{Stage: "wait_search_results",
			Err: &resultError{Kind: resultMaintenance, Retryable: true, Err: errors.New("maintenance")}},
		"пустая очистка": &StageError{Stage: "clean_duplicates",
			Err: errors.New("duplicate cleanup result is empty")},
	} {
		if NoRawKeywords(err) {
			t.Errorf("%s опознан как отсутствие исходных запросов", name)
		}
	}
}

// Пустой ответ Keys.so рисует двумя разметками. Пока проверка знала одну, окончательный
// ответ сервиса приходил к нам таймаутом — а по таймауту резервный подбор запросов моделью
// намеренно не включается, и статья вставала на этапе целиком.
func TestResultStateScriptKnowsBothEmptyMarkups(t *testing.T) {
	if !strings.Contains(keywordsResultStateJS, "selectors.empty)") {
		t.Fatal("скрипт забыл строку tr.p-datatable-emptymessage")
	}
	if !strings.Contains(keywordsResultStateJS, "selectors.emptyInfo") {
		t.Fatal("скрипт не знает подпись пагинации vuetable: «данных нет» снова станет таймаутом")
	}
	if !strings.Contains(keywordsEmptyInfoSelector, "vuetable-pagination-info") {
		t.Fatalf("селектор подписи пагинации: %q", keywordsEmptyInfoSelector)
	}
}

// Подпись пагинации живёт на странице и до прихода данных, а индикатора загрузки у этой
// разметки нет. Поспешный вывод молча подменил бы источник запросов моделью там, где
// Keys.so просто отвечал медленно, поэтому пустоте дают отстояться.
func TestResultStateScriptWaitsBeforeCallingPageEmpty(t *testing.T) {
	if !strings.Contains(keywordsResultStateJS, "selectors.emptySettle") {
		t.Fatal("пустая страница признаётся окончательной сразу")
	}
	if keywordsEmptySettleMilliseconds <= 0 {
		t.Fatalf("выдержка %d мс", keywordsEmptySettleMilliseconds)
	}
	if keywordsEmptySettleMilliseconds >= longOperationTimeoutMilliseconds {
		t.Fatalf("выдержка %d мс не оставляет времени самому ожиданию", keywordsEmptySettleMilliseconds)
	}
}
