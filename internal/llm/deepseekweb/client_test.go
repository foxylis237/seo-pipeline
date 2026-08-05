package deepseekweb

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

func TestNewClientValidatesConfig(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := NewClient(Config{ChatURL: "https://chat.deepseek.com/", LoginURL: "https://chat.deepseek.com/sign_in"}, logger)
	if err == nil || !strings.Contains(err.Error(), "profile directory") {
		t.Fatalf("error = %v", err)
	}
}

func TestSessionExpiredErrorIsUnauthorized(t *testing.T) {
	err := sessionExpiredError()
	var statusErr *llm.StatusError
	if !errors.As(err, &statusErr) || statusErr.Type != llm.ErrorTypeUnauthorized || statusErr.Code != 401 {
		t.Fatalf("error = %#v", err)
	}
	if err.Error() != "DeepSeek session expired. Run deepseek-login." {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestTemporaryErrorIsProviderFailure(t *testing.T) {
	cause := errors.New("browser closed")
	err := temporaryError("open DeepSeek Chat", cause)
	var statusErr *llm.StatusError
	if !errors.As(err, &statusErr) || statusErr.Code != 503 || !errors.Is(err, cause) {
		t.Fatalf("error = %#v", err)
	}
}

func TestOperationTimeoutUsesContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	timeout := time.Duration(operationTimeout(ctx, 30*time.Second)) * time.Millisecond
	if timeout <= 0 || timeout > 2*time.Second {
		t.Fatalf("timeout = %v", timeout)
	}
}

func TestIsLoginURL(t *testing.T) {
	for _, value := range []string{"https://chat.deepseek.com/sign_in", "https://chat.deepseek.com/login?redirect=chat"} {
		if !isLoginURL(value) {
			t.Fatalf("isLoginURL(%q) = false", value)
		}
	}
	if isLoginURL("https://chat.deepseek.com/a/chat/s/123") {
		t.Fatal("chat URL was classified as login")
	}
}

func TestOperationTimeoutFallsBackToLimitWithoutDeadline(t *testing.T) {
	// Прямой вызов Generate идёт с context.Background(): раньше ожидание ответа
	// получало 1 мс и падало мгновенно.
	timeout := time.Duration(operationTimeout(context.Background(), defaultResponseTimeout)) * time.Millisecond
	if timeout != defaultResponseTimeout {
		t.Fatalf("timeout = %v, want %v", timeout, defaultResponseTimeout)
	}
}

func TestOperationTimeoutStaysPositiveAfterDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()
	if timeout := operationTimeout(ctx, defaultResponseTimeout); timeout <= 0 {
		t.Fatalf("timeout = %v, want positive", timeout)
	}
}
