package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAICompatibleClientSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatal("missing authorization header")
		}
		var body chatCompletionRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "test-model" || len(body.Messages) != 1 || body.Messages[0].Content != "test prompt" {
			t.Fatalf("request body = %+v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"generated text"}}],"usage":{"prompt_tokens":12,"completion_tokens":34}}`))
	}))
	defer server.Close()

	client, err := NewOpenAICompatibleClient(server.URL+"/v1", "secret", "test-provider", discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Generate(context.Background(), Request{Prompt: "test prompt", Model: "test-model", Temperature: 0.7, MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "generated text" {
		t.Fatalf("text = %q", response.Text)
	}
	if response.InputTokens != 12 || response.OutputTokens != 34 {
		t.Fatalf("usage = %d/%d", response.InputTokens, response.OutputTokens)
	}
}

func TestOpenAICompatibleChatPreservesArticleForInfo(t *testing.T) {
	requestNumber := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestNumber++
		var body chatCompletionRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "free-model" {
			t.Fatalf("model = %q", body.Model)
		}
		if requestNumber == 1 && len(body.Messages) != 1 {
			t.Fatalf("first messages = %+v", body.Messages)
		}
		if requestNumber == 2 {
			if len(body.Messages) != 3 || body.Messages[1].Role != "assistant" || body.Messages[1].Content != "article text" {
				t.Fatalf("second messages = %+v", body.Messages)
			}
		}
		response := "article text"
		if requestNumber == 2 {
			response = "article info"
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + response + `"}}]}`))
	}))
	defer server.Close()
	client, err := NewOpenAICompatibleClient(server.URL, "secret", "openrouter", discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	client.ConfigureChat("free-model", 0.7, 10000)
	chat, err := client.NewChat(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Generate(context.Background(), "article prompt"); err != nil {
		t.Fatal(err)
	}
	info, err := chat.Generate(context.Background(), "info prompt")
	if err != nil || info.Text != "article info" {
		t.Fatalf("info = %+v, %v", info, err)
	}
}

func TestOpenAICompatibleClientReadsSlowResponseAndLogsPhases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"slow`))
		writer.(http.Flusher).Flush()
		time.Sleep(30 * time.Millisecond)
		_, _ = writer.Write([]byte(` response"}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`))
	}))
	defer server.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	client, err := NewOpenAICompatibleClient(server.URL, "secret", "test-provider", logger)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response, err := client.Generate(ctx, Request{Prompt: "prompt", Model: "slow-model"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != "slow response" {
		t.Fatalf("response = %+v", response)
	}
	output := logs.String()
	for _, expected := range []string{
		"HTTP request started", "response headers received", "response body reading started",
		"response body read", "response JSON decoded", `"status_code":200`,
		`"time_to_headers_ms":`, `"body_read_ms":`, `"response_size_bytes":`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("log does not contain %q: %s", expected, output)
		}
	}
	if strings.Contains(output, "slow response") || strings.Contains(output, "secret") {
		t.Fatalf("log leaked response or key: %s", output)
	}
}

func TestOpenAICompatibleClientHTTPStatusError(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusPaymentRequired, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := openAICompatibleTestClient(t, status, `{"error":{"message":"sensitive provider response"}}`)
			_, err := client.Generate(context.Background(), Request{Prompt: "prompt", Model: "model"})
			var statusErr *StatusError
			if !errors.As(err, &statusErr) || statusErr.Code != status {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), "sensitive provider response") {
				t.Fatal("provider response leaked into error")
			}
		})
	}
}

func TestOpenAICompatibleClientClassifiesLimitErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantType  ErrorType
		retryable bool
	}{
		{name: "temporary rate limit", status: 429, body: `{"error":{"message":"rate limit exceeded; retry shortly"}}`, wantType: ErrorTypeRateLimit, retryable: true},
		{name: "quota exhausted", status: 429, body: `{"error":{"code":"insufficient_quota","message":"free quota exceeded"}}`, wantType: ErrorTypeQuotaExhausted},
		{name: "credits exhausted", status: 402, body: `{"error":{"message":"insufficient credits"}}`, wantType: ErrorTypeCreditsExhausted},
		{name: "unauthorized", status: 401, body: `{"error":{"message":"invalid key"}}`, wantType: ErrorTypeUnauthorized},
		{name: "forbidden", status: 403, body: `{"error":{"message":"forbidden"}}`, wantType: ErrorTypeUnauthorized},
		{name: "server error", status: 503, body: `{"error":{"message":"temporarily unavailable"}}`, wantType: ErrorTypeProvider, retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := openAICompatibleTestClient(t, test.status, test.body)
			_, err := client.Generate(context.Background(), Request{Prompt: "prompt", Model: "model"})
			var statusErr *StatusError
			if !errors.As(err, &statusErr) {
				t.Fatalf("error = %v", err)
			}
			if statusErr.Type != test.wantType || isTemporary(err) != test.retryable {
				t.Fatalf("classification = %q retryable=%v", statusErr.Type, isTemporary(err))
			}
		})
	}
}

func TestOpenAICompatibleClientDoesNotExposeErrorBodySecrets(t *testing.T) {
	const secret = "sk-secret-value"
	const bodyTail = "full-response-body-tail"
	client := openAICompatibleTestClient(t, 429, `{"error":{"code":"insufficient_quota","message":"quota exceeded bearer `+secret+` `+bodyTail+`"},"debug":"`+bodyTail+`"}`)
	_, err := client.Generate(context.Background(), Request{Prompt: "prompt", Model: "model"})
	if err == nil {
		t.Fatal("error = nil")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), bodyTail) {
		t.Fatalf("error leaked response data: %v", err)
	}
}

func TestOpenAICompatibleClientEmptyChoices(t *testing.T) {
	client := openAICompatibleTestClient(t, http.StatusOK, `{"choices":[],"usage":{}}`)
	_, err := client.Generate(context.Background(), Request{Prompt: "prompt", Model: "model"})
	if err == nil || !strings.Contains(err.Error(), "returned no choices") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAICompatibleClientInvalidJSON(t *testing.T) {
	client := openAICompatibleTestClient(t, http.StatusOK, `{not-json`)
	_, err := client.Generate(context.Background(), Request{Prompt: "prompt", Model: "model"})
	if err == nil || !strings.Contains(err.Error(), "decode test-provider response") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAICompatibleClientReportsDeadlineBeforeRequestCreation(t *testing.T) {
	client, err := NewOpenAICompatibleClient("https://example.test", "secret", "test-provider", discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Generate(ctx, Request{Prompt: "prompt", Model: "model"})
	if err == nil || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "create test-provider HTTP request") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAICompatibleClientReportsDeadlineDuringDo(t *testing.T) {
	client, err := NewOpenAICompatibleClient("https://example.test", "secret", "test-provider", discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = client.Generate(ctx, Request{Prompt: "prompt", Model: "model"})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "during Do") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAICompatibleClientReportsDeadlineWhileReadingBody(t *testing.T) {
	client, err := NewOpenAICompatibleClient("https://example.test", "secret", "test-provider", discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	client.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: &cancelingBody{ctx: ctx, cancel: cancel}, Header: make(http.Header)}, nil
	})}
	_, err = client.Generate(ctx, Request{Prompt: "prompt", Model: "model"})
	if err == nil || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "read test-provider HTTP response body") {
		t.Fatalf("error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type cancelingBody struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (b *cancelingBody) Read([]byte) (int, error) {
	b.cancel()
	return 0, b.ctx.Err()
}

func (*cancelingBody) Close() error { return nil }

func openAICompatibleTestClient(t *testing.T, status int, body string) *OpenAICompatibleClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	client, err := NewOpenAICompatibleClient(server.URL, "secret", "test-provider", discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
