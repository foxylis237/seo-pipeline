package wordpress

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testAppPassword = "do-not-leak-this-app-password"

// newTestClient поднимает TLS-сервер и клиента, который ему доверяет.
//
// Сервер именно TLS: конфигурация отвергает http, и подменять эту проверку ради теста нельзя —
// тогда тест перестал бы проверять то, что работает в бою.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)

	client, err := NewClient(Config{
		BaseURL:     server.URL,
		Username:    "admin",
		AppPassword: testAppPassword,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	httpClient := server.Client()
	httpClient.Timeout = client.httpClient.Timeout
	client.httpClient = httpClient
	// XML-RPC ходит на тот же сервер, но своим клиентом: у записи бюджет другой, и общий
	// экземпляр сделал бы таймаут чтения и таймаут записи одним числом.
	client.xmlrpcClient = &http.Client{Transport: httpClient.Transport, Timeout: xmlrpcTimeout}
	// У загрузки обложки бюджет свой и ещё больший: файл весит мегабайты, и его время
	// определяется каналом, а не работой WordPress.
	client.mediaClient = &http.Client{Transport: httpClient.Transport, Timeout: mediaUploadTimeout}
	// Повторы проверяются без реального ожидания: иначе тест на три попытки стоил бы три секунды.
	client.sleep = func(context.Context, time.Duration) error { return nil }
	return client, server
}

func TestCheckConnectionReadsUserAndCapability(t *testing.T) {
	var gotPath, gotQuery, gotMethod, gotAuthorization string
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":1,"username":"admin","name":"Администратор",
			"capabilities":{"publish_posts":true,"edit_posts":true}}`)
	})

	connection, err := client.CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("CheckConnection: %v", err)
	}
	if connection.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want 200", connection.StatusCode)
	}
	want := User{ID: 1, Login: "admin", Name: "Администратор", CanPublishPosts: true}
	if connection.User != want {
		t.Fatalf("User = %+v, want %+v", connection.User, want)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %s, want GET: проверка обязана быть read-only", gotMethod)
	}
	if gotPath != "/wp-json/wp/v2/users/me" {
		t.Fatalf("path = %q", gotPath)
	}
	// Без context=edit WordPress не отдаёт ни username, ни capabilities.
	if gotQuery != "context=edit" {
		t.Fatalf("query = %q, want context=edit", gotQuery)
	}
	if !strings.HasPrefix(gotAuthorization, "Basic ") {
		t.Fatalf("Authorization = %q, want Basic", gotAuthorization)
	}
}

// Право отсутствует — это не отказ: credentials рабочие, а публиковать пользователь не может.
// Различать эти два состояния и есть смысл проверки.
func TestCheckConnectionSucceedsWithoutPublishCapability(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":7,"username":"editor","name":"Редактор","capabilities":{"edit_posts":true}}`)
	})

	connection, err := client.CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("CheckConnection: %v", err)
	}
	if connection.User.CanPublishPosts {
		t.Fatal("CanPublishPosts = true, want false")
	}
}

// Плагин может положить в capabilities что угодно. Один нелогический ключ не имеет права
// уронить разбор всего ответа и превратить рабочие credentials в красный фейл.
func TestCheckConnectionToleratesNonBooleanCapabilities(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":3,"username":"admin","capabilities":{"publish_posts":"1","level_10":10}}`)
	})

	connection, err := client.CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("CheckConnection: %v", err)
	}
	if !connection.User.CanPublishPosts {
		t.Fatal("CanPublishPosts = false, want true для значения \"1\"")
	}
}

func TestCheckConnectionRejectsEmptyBodyWithStatusOK(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{}`)
	})

	if _, err := client.CheckConnection(context.Background()); err == nil {
		t.Fatal("CheckConnection() error = nil, want отказ: 200 без id и username не подтверждает доступ")
	}
}

func TestCheckConnectionDoesNotRetryUnauthorized(t *testing.T) {
	var requests int
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"code":"rest_not_logged_in","message":"Вы не вошли на сайт."}`)
	})

	_, err := client.CheckConnection(context.Background())
	if err == nil {
		t.Fatal("CheckConnection() error = nil, want 401")
	}
	if requests != 1 {
		t.Fatalf("запросов = %d, want 1: неверный пароль повтор не чинит", requests)
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("err = %v, want *StatusError", err)
	}
	if statusErr.StatusCode != http.StatusUnauthorized || statusErr.Code != "rest_not_logged_in" {
		t.Fatalf("StatusError = %+v", statusErr)
	}
	if !NeedsCredentialsCheck(err) {
		t.Fatal("NeedsCredentialsCheck = false, want true")
	}
}

func TestCheckConnectionRetriesServerError(t *testing.T) {
	var requests int
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	if _, err := client.CheckConnection(context.Background()); err == nil {
		t.Fatal("CheckConnection() error = nil, want 503")
	}
	if requests != DefaultRetryPolicy().MaxAttempts {
		t.Fatalf("запросов = %d, want %d", requests, DefaultRetryPolicy().MaxAttempts)
	}
	if NeedsCredentialsCheck(errors.New("любая другая ошибка")) {
		t.Fatal("NeedsCredentialsCheck по не-StatusError = true")
	}
}

// Успех после временного отказа: повтор существует ради этого случая, а не ради статистики.
func TestCheckConnectionSucceedsAfterTemporaryFailure(t *testing.T) {
	var requests int
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"id":1,"username":"admin"}`)
	})

	connection, err := client.CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("CheckConnection: %v", err)
	}
	if connection.User.Login != "admin" {
		t.Fatalf("Login = %q", connection.User.Login)
	}
}

func TestCheckConnectionHonorsRetryAfter(t *testing.T) {
	var waits []time.Duration
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	client.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}

	if _, err := client.CheckConnection(context.Background()); err == nil {
		t.Fatal("CheckConnection() error = nil, want 429")
	}
	for _, wait := range waits {
		if wait != 2*time.Second {
			t.Fatalf("паузы = %v, want 2s из Retry-After", waits)
		}
	}
	if len(waits) == 0 {
		t.Fatal("повторов не было, а 429 временный")
	}
}

// Без Retry-After работает свой backoff: 1 с, затем 2 с.
func TestRetryDelayFallsBackToBackoff(t *testing.T) {
	var waits []time.Duration
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	client.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}

	if _, err := client.CheckConnection(context.Background()); err == nil {
		t.Fatal("CheckConnection() error = nil, want 500")
	}
	want := []time.Duration{time.Second, 2 * time.Second}
	if len(waits) != len(want) {
		t.Fatalf("паузы = %v, want %v", waits, want)
	}
	for i, wait := range waits {
		if wait != want[i] {
			t.Fatalf("паузы = %v, want %v", waits, want)
		}
	}
}

// Общий дедлайн обрывает и паузу между попытками. Исходная ошибка при этом не теряется:
// без неё человек увидит только «context deadline exceeded» и не узнает, чем ответил сайт.
func TestCheckConnectionStopsOnDeadlineDuringBackoff(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	client.sleep = func(context.Context, time.Duration) error { return context.DeadlineExceeded }

	_, err := client.CheckConnection(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("err = %v, ответ сайта потерян", err)
	}
}

func TestNewClientRejectsUnsafeBaseURL(t *testing.T) {
	for name, baseURL := range map[string]string{
		"http":          "http://dpoprof.ru",
		"без схемы":     "dpoprof.ru",
		"пустой":        "   ",
		"с credentials": "https://admin:secret@dpoprof.ru",
		"без хоста":     "https:///wp-json",
		"чужая схема":   "ftp://dpoprof.ru",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewClient(Config{BaseURL: baseURL, Username: "admin", AppPassword: testAppPassword}); err == nil {
				t.Fatalf("NewClient(%q) error = nil, want отказ", baseURL)
			}
		})
	}
}

func TestNewClientRequiresCredentials(t *testing.T) {
	if _, err := NewClient(Config{BaseURL: "https://dpoprof.ru", AppPassword: testAppPassword}); err == nil {
		t.Fatal("NewClient без username error = nil")
	}
	if _, err := NewClient(Config{BaseURL: "https://dpoprof.ru", Username: "admin"}); err == nil {
		t.Fatal("NewClient без пароля error = nil")
	}
}

// Хвостовой слэш срезается, подкаталог остаётся: WordPress часто живёт в /blog.
func TestBaseURLKeepsSubdirectoryAndDropsTrailingSlash(t *testing.T) {
	var gotPath string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `{"id":1,"username":"admin"}`)
	}))
	defer server.Close()

	client, err := NewClient(Config{
		BaseURL: server.URL + "/blog/", Username: "admin", AppPassword: testAppPassword,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.httpClient = server.Client()

	if _, err := client.CheckConnection(context.Background()); err != nil {
		t.Fatalf("CheckConnection: %v", err)
	}
	if gotPath != "/blog/wp-json/wp/v2/users/me" {
		t.Fatalf("path = %q, want /blog/wp-json/wp/v2/users/me", gotPath)
	}
}

// Главный тест этого пакета: пароль не должен появляться нигде, кроме заголовка запроса.
func TestErrorsNeverExposeAppPassword(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		// Сервер эхом возвращает пароль — так делают некоторые плагины безопасности,
		// логирующие запрос. В нашу ошибку он всё равно попасть не должен.
		fmt.Fprintf(w, `{"code":"rest_forbidden","message":"denied for %s"}`, testAppPassword)
	})

	_, err := client.CheckConnection(context.Background())
	if err == nil {
		t.Fatal("CheckConnection() error = nil, want 403")
	}
	if strings.Contains(err.Error(), testAppPassword) {
		t.Fatalf("ошибка раскрывает пароль: %q", err)
	}
	// Отказ конфигурации — второй путь, которым секрет мог бы утечь наружу.
	_, configErr := NewClient(Config{BaseURL: "http://dpoprof.ru", Username: "admin", AppPassword: testAppPassword})
	if configErr == nil || strings.Contains(configErr.Error(), testAppPassword) {
		t.Fatalf("ошибка конфигурации раскрывает пароль: %v", configErr)
	}
}
