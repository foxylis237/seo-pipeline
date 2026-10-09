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

func TestGenerateRecordsCarryArticleAndStage(t *testing.T) {
	profileDir := t.TempDir()
	if err := writeBlockedUntil(profileDir, time.Now().Add(time.Hour), "account_blocked"); err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	client, err := NewClient(Config{
		ChatURL: "https://chat.deepseek.com/", LoginURL: "https://chat.deepseek.com/sign_in", ProfileDir: profileDir,
	}, slog.New(slog.NewTextHandler(&logs, nil)).With("provider", "deepseek_web"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Generate(context.Background(), llm.Request{Prompt: "prompt", ArticleID: 9, Stage: "expert"}); err == nil {
		t.Fatal("cooldown did not reject the request")
	}
	output := logs.String()
	if !strings.Contains(output, "отклонён") {
		t.Fatalf("cooldown was not logged: %s", output)
	}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if !strings.Contains(line, "article_id=9") || !strings.Contains(line, "stage=expert") {
			t.Errorf("record does not reach the article log: %s", line)
		}
		if strings.Contains(line, "provider_type") {
			t.Errorf("provider_type duplicates provider: %s", line)
		}
	}
	logs.Reset()
	client.step("open_page")
	if !strings.Contains(logs.String(), `msg="шаг DeepSeek" provider=deepseek_web step=open_page`) {
		t.Errorf("step is not written as step: %s", logs.String())
	}
}
