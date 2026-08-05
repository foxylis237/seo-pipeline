package llm

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/config"
)

type fakeClient struct {
	responses []Response
	errors    []error
	calls     int
	request   Request
	deadlines []time.Time
	wait      bool
	delay     time.Duration
}

func (c *fakeClient) Generate(ctx context.Context, request Request) (Response, error) {
	c.request = request
	if deadline, found := ctx.Deadline(); found {
		c.deadlines = append(c.deadlines, deadline)
	}
	index := c.calls
	c.calls++
	if c.wait {
		<-ctx.Done()
		return Response{}, ctx.Err()
	}
	if c.delay > 0 {
		timer := time.NewTimer(c.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-timer.C:
		}
	}
	if index < len(c.errors) && c.errors[index] != nil {
		return Response{}, c.errors[index]
	}
	if index < len(c.responses) {
		return c.responses[index], nil
	}
	return Response{Text: "ok"}, nil
}

func TestRouterHeartbeatStopsAfterCompletion(t *testing.T) {
	tests := []struct {
		name   string
		client *fakeClient
		cancel bool
	}{
		{name: "success", client: &fakeClient{delay: 12 * time.Millisecond}},
		{name: "error", client: &fakeClient{delay: 12 * time.Millisecond, errors: []error{errors.New("failed")}}},
		{name: "context cancellation", client: &fakeClient{wait: true}, cancel: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			router := testRouter(test.client)
			router.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			router.heartbeatInterval = 2 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			if test.cancel {
				timer := time.AfterFunc(12*time.Millisecond, cancel)
				defer timer.Stop()
			} else {
				defer cancel()
			}
			_, _ = router.Generate(ctx, Call{Stage: "structure", ArticleID: 7, Data: struct{ Title string }{"Тема"}})
			before := strings.Count(logs.String(), "LLM request still running")
			if before == 0 {
				t.Fatalf("heartbeat was not logged: %s", logs.String())
			}
			time.Sleep(8 * time.Millisecond)
			after := strings.Count(logs.String(), "LLM request still running")
			if after != before {
				t.Fatalf("heartbeat continued after completion: before=%d after=%d", before, after)
			}
		})
	}
}

func TestRouterUsesOneTimeoutForAllAttempts(t *testing.T) {
	client := &fakeClient{errors: []error{NewStatusError(503, "temporary"), NewStatusError(503, "temporary"), nil}}
	router := testRouter(client)
	if _, err := router.Generate(context.Background(), Call{Stage: "structure", Data: struct{ Title string }{"Тема"}}); err != nil {
		t.Fatal(err)
	}
	if len(client.deadlines) != 3 {
		t.Fatalf("deadlines = %d", len(client.deadlines))
	}
	for _, deadline := range client.deadlines[1:] {
		if !deadline.Equal(client.deadlines[0]) {
			t.Fatalf("retry received a new deadline: %v", client.deadlines)
		}
	}
}

func TestRouterDoesNotRetryAfterOverallTimeout(t *testing.T) {
	client := &fakeClient{errors: []error{NewStatusError(429, "rate limit exceeded")}}
	router := testRouter(client)
	stage := router.config.Stages["structure"]
	stage.Timeout = 20 * time.Millisecond
	router.config.Stages["structure"] = stage
	router.baseDelay = 100 * time.Millisecond
	router.sleep = sleepContext

	started := time.Now()
	_, err := router.Generate(context.Background(), Call{Stage: "structure", Data: struct{ Title string }{"Тема"}})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("calls = %d, want 1", client.calls)
	}
	if elapsed := time.Since(started); elapsed > 80*time.Millisecond {
		t.Fatalf("overall timeout took %v", elapsed)
	}
}

func TestRouterSelectsProviderAndModel(t *testing.T) {
	client := &fakeClient{responses: []Response{{Text: "result", InputTokens: 3, OutputTokens: 4}}}
	router := testRouter(client)
	result, err := router.Generate(context.Background(), Call{Stage: "structure", ArticleID: 7, Data: struct{ Title string }{"Тема"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "selected" || result.Model != "configured-model" || client.request.Model != "configured-model" {
		t.Fatalf("routing result=%+v request=%+v", result, client.request)
	}
	if client.request.Prompt != "Title: Тема" {
		t.Fatalf("prompt = %q", client.request.Prompt)
	}
}

func TestRouterSelectsModelsIndependentlyPerStage(t *testing.T) {
	structureClient := &fakeClient{}
	reviewClient := &fakeClient{}
	temperature := 0.3
	cfg := config.LLMConfig{Stages: map[string]config.LLMStageConfig{
		"structure": {Provider: "gemini", Model: "gemini-model", PromptTemplate: "{{.Title}}", Temperature: &temperature, MaxTokens: 100, Timeout: time.Second},
		"review":    {Provider: "openrouter", Model: "free-model", PromptTemplate: "{{.Title}}", Temperature: &temperature, MaxTokens: 200, Timeout: time.Second},
	}}
	router := NewRouter(cfg, map[string]Client{"gemini": structureClient, "openrouter": reviewClient}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := router.Generate(context.Background(), Call{Stage: "structure", Data: struct{ Title string }{"Structure"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Generate(context.Background(), Call{Stage: "review", Data: struct{ Title string }{"Review"}}); err != nil {
		t.Fatal(err)
	}
	if structureClient.request.Model != "gemini-model" || reviewClient.request.Model != "free-model" {
		t.Fatalf("models: structure=%q review=%q", structureClient.request.Model, reviewClient.request.Model)
	}
}

func TestRouterFallsBackForEligibleErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{"rate limit", NewStatusError(429, "rate limit exceeded")},
		{"resource exhausted", NewStatusError(429, "resource exhausted")},
		{"service unavailable", NewStatusError(503, "temporary")},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := &fakeClient{errors: []error{test.err, test.err, test.err}}
			second := &fakeClient{}
			router := fallbackTestRouter(first, second)
			result, err := router.Generate(context.Background(), Call{Stage: "structure", Data: struct{ Title string }{"T"}})
			if err != nil || result.Provider != "second" || second.calls != 1 {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, second.calls)
			}
		})
	}
}

func TestRouterDoesNotFallbackForPermanentError(t *testing.T) {
	first := &fakeClient{errors: []error{NewStatusError(400, "bad request")}}
	second := &fakeClient{}
	_, err := fallbackTestRouter(first, second).Generate(context.Background(), Call{Stage: "structure", Data: struct{ Title string }{"T"}})
	if err == nil || second.calls != 0 {
		t.Fatalf("err=%v second.calls=%d", err, second.calls)
	}
}

func TestRouterAggregatesAllTargetErrorsAndChatCarriesHistory(t *testing.T) {
	temporary := NewStatusError(503, "temporary")
	first := &fakeClient{errors: []error{temporary, temporary, temporary, temporary, temporary, temporary}}
	second := &fakeClient{}
	router := fallbackTestRouter(first, second)
	chat, _ := router.NewStageChatFactory("structure", "structure").NewChat(context.Background(), 7)
	if _, err := chat.Generate(context.Background(), "first prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Generate(context.Background(), "second prompt"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.request.Prompt, "assistant:\nok") || !strings.Contains(second.request.Prompt, "first prompt") {
		t.Fatalf("history missing: %q", second.request.Prompt)
	}

	failedRouter := fallbackTestRouter(&fakeClient{errors: []error{temporary, temporary, temporary}}, &fakeClient{errors: []error{temporary, temporary, temporary}})
	_, err := failedRouter.Generate(context.Background(), Call{Stage: "structure", Data: struct{ Title string }{"T"}})
	if err == nil || !strings.Contains(err.Error(), "provider=first") || !strings.Contains(err.Error(), "provider=second") {
		t.Fatalf("aggregate error=%v", err)
	}
}

func fallbackTestRouter(first, second Client) *Router {
	temperature := 0.3
	stage := config.LLMStageConfig{Targets: []config.LLMTargetConfig{{Provider: "first", Model: "one"}, {Provider: "second", Model: "two"}}, PromptTemplate: "{{.Title}}", Temperature: &temperature, MaxTokens: 100, Timeout: time.Second}
	router := NewRouter(config.LLMConfig{Stages: map[string]config.LLMStageConfig{"structure": stage}}, map[string]Client{"first": first, "second": second}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router.sleep = func(context.Context, time.Duration) error { return nil }
	return router
}

func TestRouterRetriesTemporaryErrors(t *testing.T) {
	for _, code := range []int{429, 500, 502, 503, 504} {
		client := &fakeClient{errors: []error{NewStatusError(code, "temporary rate limit"), nil}}
		router := testRouter(client)
		if _, err := router.Generate(context.Background(), Call{Stage: "structure", Data: struct{ Title string }{"Тема"}}); err != nil {
			t.Fatalf("status %d: %v", code, err)
		}
		if client.calls != 2 {
			t.Fatalf("status %d calls = %d, want 2", code, client.calls)
		}
	}
}

func TestRouterDoesNotRetryExhaustedQuotaOrCredits(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		message string
	}{
		{name: "quota", err: NewStatusError(429, "insufficient_quota"), message: "LLM quota exhausted"},
		{name: "credits", err: NewStatusError(402, "insufficient credits"), message: "LLM credits exhausted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeClient{errors: []error{test.err}}
			router := testRouter(client)
			_, err := router.Generate(context.Background(), Call{Stage: "structure", ArticleID: 7, Data: struct{ Title string }{"Тема"}})
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v", err)
			}
			if client.calls != 1 {
				t.Fatalf("calls = %d, want 1", client.calls)
			}
		})
	}
}

func TestRouterLogsSafeLimitDiagnostics(t *testing.T) {
	const secret = "sk-secret-value"
	const bodyTail = "full-response-body-tail"
	client := &fakeClient{errors: []error{NewStatusError(429, "insufficient_quota bearer "+secret+" "+bodyTail)}}
	var logs bytes.Buffer
	temperature := 0.3
	router := NewRouter(config.LLMConfig{
		Providers: map[string]config.LLMProviderConfig{"selected": {Type: "openai_compatible", APIKeyEnv: "TEST"}},
		Stages: map[string]config.LLMStageConfig{"structure": {
			Provider: "selected", Model: "configured-model", PromptTemplate: "Title: {{.Title}}",
			Temperature: &temperature, MaxTokens: 100, Timeout: time.Second,
		}},
	}, map[string]Client{"selected": client}, slog.New(slog.NewJSONHandler(&logs, nil)))
	router.sleep = func(context.Context, time.Duration) error { return nil }

	_, _ = router.Generate(context.Background(), Call{Stage: "structure", ArticleID: 77, Data: struct{ Title string }{"Тема"}})
	output := logs.String()
	for _, expected := range []string{`"article_id":77`, `"stage":"structure"`, `"provider":"selected"`, `"model":"configured-model"`, `"remaining_ms":`, `"status_code":429`, `"error_type":"quota_exhausted"`, `"provider_message":"quota exceeded"`, `"retryable":false`} {
		if !strings.Contains(output, expected) {
			t.Fatalf("log does not contain %s: %s", expected, output)
		}
	}
	if strings.Contains(output, secret) || strings.Contains(output, bodyTail) || strings.Contains(output, "Title: Тема") {
		t.Fatalf("log leaked sensitive data: %s", output)
	}
}

func TestRouterDoesNotRetryPermanentHTTPError(t *testing.T) {
	for _, code := range []int{400, 401, 403} {
		client := &fakeClient{errors: []error{&StatusError{Code: code, Err: errors.New("permanent")}}}
		router := testRouter(client)
		if _, err := router.Generate(context.Background(), Call{Stage: "structure", Data: struct{ Title string }{"Тема"}}); err == nil {
			t.Fatalf("status %d error = nil", code)
		}
		if client.calls != 1 {
			t.Fatalf("status %d calls = %d, want 1", code, client.calls)
		}
	}
}

func testRouter(client Client) *Router {
	temperature := 0.3
	router := NewRouter(config.LLMConfig{
		Providers: map[string]config.LLMProviderConfig{"selected": {Type: "gemini", APIKeyEnv: "TEST"}},
		Stages: map[string]config.LLMStageConfig{"structure": {
			Provider: "selected", Model: "configured-model", PromptTemplate: "Title: {{.Title}}",
			Temperature: &temperature, MaxTokens: 100, Timeout: time.Second,
		}},
	}, map[string]Client{"selected": client}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router.sleep = func(context.Context, time.Duration) error { return nil }
	return router
}

func TestStageChatLogsArticleIDForEveryStage(t *testing.T) {
	client := &fakeClient{}
	var logs bytes.Buffer
	temperature := 0.3
	router := NewRouter(config.LLMConfig{
		Providers: map[string]config.LLMProviderConfig{"selected": {Type: "openai_compatible", APIKeyEnv: "TEST"}},
		Stages: map[string]config.LLMStageConfig{
			"article": {Provider: "selected", Model: "configured-model", PromptTemplate: "{{.Title}}",
				Temperature: &temperature, MaxTokens: 100, Timeout: time.Second},
			"info": {Provider: "selected", Model: "configured-model", PromptTemplate: "{{.Title}}",
				Temperature: &temperature, MaxTokens: 100, Timeout: time.Second},
		},
	}, map[string]Client{"selected": client}, slog.New(slog.NewJSONHandler(&logs, nil)))

	chat, err := router.NewStageChatFactory("article", "info").NewChat(context.Background(), 77)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Generate(context.Background(), "article prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Generate(context.Background(), "info prompt"); err != nil {
		t.Fatal(err)
	}
	output := logs.String()
	for _, expected := range []string{`"article_id":77,"stage":"article"`, `"article_id":77,"stage":"info"`} {
		if !strings.Contains(output, expected) {
			t.Fatalf("log does not contain %s: %s", expected, output)
		}
	}
	if strings.Contains(output, `"article_id":0`) {
		t.Fatalf("chat stage was logged without an article: %s", output)
	}
}
