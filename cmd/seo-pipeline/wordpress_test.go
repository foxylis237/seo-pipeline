package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/foxylis237/seo-pipeline/internal/config"
	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
)

// stubWordPressChecker подменяет живой сайт. Команду можно проверить целиком, не поднимая ни
// TLS-сервера, ни настоящего WordPress.
type stubWordPressChecker struct {
	connection wordpress.Connection
	err        error
	calls      int
}

func (s *stubWordPressChecker) CheckConnection(context.Context) (wordpress.Connection, error) {
	s.calls++
	return s.connection, s.err
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestRunWordPressCheckReportsUser(t *testing.T) {
	checker := &stubWordPressChecker{connection: wordpress.Connection{
		StatusCode: http.StatusOK,
		User:       wordpress.User{ID: 1, Login: "admin", Name: "Администратор", CanPublishPosts: true},
	}}
	var out bytes.Buffer

	if err := runWordPressCheck(context.Background(), checker, "PPROF_1_", discardLogger(), &out); err != nil {
		t.Fatalf("runWordPressCheck: %v", err)
	}
	report := out.String()
	for _, want := range []string{"HTTP 200", "admin", "id=1", "Право publish_posts: есть"} {
		if !strings.Contains(report, want) {
			t.Fatalf("вывод не содержит %q:\n%s", want, report)
		}
	}
	if checker.calls != 1 {
		t.Fatalf("вызовов = %d, want 1: проверка делает ровно один запрос", checker.calls)
	}
}

// Пользователь без права публикации — не отказ: credentials рабочие. Но предупредить надо
// сейчас, а не на первом POST, когда публикация уже начата.
func TestRunWordPressCheckWarnsWithoutPublishCapability(t *testing.T) {
	checker := &stubWordPressChecker{connection: wordpress.Connection{
		StatusCode: http.StatusOK,
		User:       wordpress.User{ID: 7, Login: "editor"},
	}}
	var out bytes.Buffer

	if err := runWordPressCheck(context.Background(), checker, "PPROF_1_", discardLogger(), &out); err != nil {
		t.Fatalf("runWordPressCheck: %v", err)
	}
	if !strings.Contains(out.String(), "Право publish_posts: НЕТ") {
		t.Fatalf("вывод не предупреждает об отсутствии права:\n%s", out.String())
	}
}

// Подсказка обязана называть ту переменную, которую человек действительно правит: у pprof_1
// это PPROF_1_WORDPRESS_APP_PASSWORD, и отправить его чинить WORDPRESS_APP_PASSWORD — значит
// отправить не в ту строку .env.
func TestRunWordPressCheckNamesTaskScopedVariablesOnUnauthorized(t *testing.T) {
	checker := &stubWordPressChecker{err: &wordpress.StatusError{
		StatusCode: http.StatusUnauthorized, Endpoint: "/wp-json/wp/v2/users/me", Code: "rest_not_logged_in",
	}}

	err := runWordPressCheck(context.Background(), checker, "PPROF_1_", discardLogger(), &bytes.Buffer{})
	if err == nil {
		t.Fatal("runWordPressCheck() error = nil, want 401")
	}
	if !strings.Contains(err.Error(), "PPROF_1_WORDPRESS_APP_PASSWORD") {
		t.Fatalf("подсказка не называет переменную задачи: %v", err)
	}
}

// Временный отказ подсказки про пароль не получает: credentials тут ни при чём, и посылать
// человека их проверять — ложный след.
func TestRunWordPressCheckDoesNotBlameCredentialsOnServerError(t *testing.T) {
	checker := &stubWordPressChecker{err: &wordpress.StatusError{
		StatusCode: http.StatusServiceUnavailable, Endpoint: "/wp-json/wp/v2/users/me", Retryable: true,
	}}

	err := runWordPressCheck(context.Background(), checker, "PPROF_1_", discardLogger(), &bytes.Buffer{})
	if err == nil {
		t.Fatal("runWordPressCheck() error = nil, want 503")
	}
	if strings.Contains(err.Error(), "WORDPRESS_APP_PASSWORD") {
		t.Fatalf("временный отказ отправляет проверять пароль: %v", err)
	}
}

func TestNewWordPressClientRejectsInsecureURL(t *testing.T) {
	_, err := newWordPressClient(config.WordPressConfig{
		BaseURL: "http://dpoprof.ru", Username: "admin", AppPassword: "secret",
	})
	if err == nil {
		t.Fatal("newWordPressClient(http) error = nil, want отказ")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("ошибка раскрывает пароль: %v", err)
	}
}

// wordpress-check не должна требовать базу: она и нужна чаще всего тогда, когда докер потушен.
func TestValidateConfigForWordPressCheckIgnoresDatabase(t *testing.T) {
	cfg := config.Config{WordPress: config.WordPressConfig{
		BaseURL: "https://dpoprof.ru", Username: "admin", AppPassword: "secret",
	}}
	if err := validateConfig(wordPressCheckOperation, cfg); err != nil {
		t.Fatalf("validateConfig(%s) = %v, want nil без DATABASE_URL", wordPressCheckOperation, err)
	}
	if err := validateConfig(wordPressCheckOperation, config.Config{}); err == nil {
		t.Fatal("validateConfig без настроек WordPress = nil")
	}
}

// Операция разбирается для обеих задач и не принимает external_id: площадка проверяется
// целиком, у отдельной статьи своего доступа не бывает.
func TestParseCommandAcceptsWordPressCheckWithoutExternalID(t *testing.T) {
	for _, task := range []string{"task-1", "pprof-1"} {
		command, err := parseCommand([]string{"seo-pipeline", task, wordPressCheckOperation})
		if err != nil {
			t.Fatalf("parseCommand(%s %s): %v", task, wordPressCheckOperation, err)
		}
		if command.Name != wordPressCheckOperation {
			t.Fatalf("Name = %q, want %q", command.Name, wordPressCheckOperation)
		}
		if command.ExternalID != "" {
			t.Fatalf("ExternalID = %q, want пустой", command.ExternalID)
		}
	}
	if _, err := parseCommand([]string{"seo-pipeline", "pprof-1", wordPressCheckOperation, "37"}); err == nil {
		t.Fatal("parseCommand с external_id = nil, want отказ")
	}
}

// Ошибка не-StatusError проходит наружу как есть — вызывающий не должен додумывать причину.
func TestRunWordPressCheckPassesThroughUnknownError(t *testing.T) {
	sentinel := errors.New("сеть недоступна")
	checker := &stubWordPressChecker{err: sentinel}

	err := runWordPressCheck(context.Background(), checker, "", discardLogger(), &bytes.Buffer{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
}
