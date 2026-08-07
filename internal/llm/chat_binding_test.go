package llm

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/config"
)

// reviewFixRouter повторяет целевую раскладку: у review два target, fix продолжает тот же чат.
func reviewFixRouter(t *testing.T, primary, reserve *fakeClient) *Router {
	t.Helper()
	temperature := 0.3
	stage := config.LLMStageConfig{
		PromptTemplate: "{{.Text}}", Temperature: &temperature, MaxTokens: 100, Timeout: 5 * time.Second,
		Targets: []config.LLMTargetConfig{
			{Provider: "primary", Model: "gemini-2.5-flash"},
			{Provider: "reserve", Model: "deepseek-web"},
		},
	}
	router := NewRouter(config.LLMConfig{
		Providers: map[string]config.LLMProviderConfig{
			"primary": {Type: "gemini"},
			"reserve": {Type: "deepseek_web"},
		},
		Stages: map[string]config.LLMStageConfig{"review": stage, "fix": stage},
	}, map[string]Client{"primary": primary, "reserve": reserve}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router.sleep = func(context.Context, time.Duration) error { return nil }
	return router
}

func TestChatKeepsProviderChosenByFirstStage(t *testing.T) {
	// primary отвечает отказом авторизации: повторов у него не будет, сработает fallback.
	primary := &fakeClient{errors: []error{NewStatusError(403, "")}}
	reserve := &fakeClient{}
	router := reviewFixRouter(t, primary, reserve)

	chat, err := router.NewStageChatFactory("review", "fix").NewChat(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Generate(context.Background(), "review prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Generate(context.Background(), "fix prompt"); err != nil {
		t.Fatal(err)
	}

	if primary.calls != 1 {
		t.Fatalf("primary.calls = %d, want 1: fix не должен заново выбирать target", primary.calls)
	}
	if reserve.calls != 2 {
		t.Fatalf("reserve.calls = %d, want 2: fix должен идти к провайдеру, ответившему на review", reserve.calls)
	}
	if reserve.request.Model != "deepseek-web" {
		t.Fatalf("model = %q, want deepseek-web", reserve.request.Model)
	}
}

func TestChatKeepsFirstProviderWhenItSucceeds(t *testing.T) {
	primary := &fakeClient{}
	reserve := &fakeClient{}
	router := reviewFixRouter(t, primary, reserve)

	chat, err := router.NewStageChatFactory("review", "fix").NewChat(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"review prompt", "fix prompt"} {
		if _, err := chat.Generate(context.Background(), prompt); err != nil {
			t.Fatal(err)
		}
	}
	if primary.calls != 2 {
		t.Fatalf("primary.calls = %d, want 2", primary.calls)
	}
	if reserve.calls != 0 {
		t.Fatalf("reserve.calls = %d, want 0: резерв не нужен, пока основной провайдер отвечает", reserve.calls)
	}
}

func TestBoundChatDoesNotSwitchProviderOnLaterFailure(t *testing.T) {
	primary := &fakeClient{}
	// Первый ответ успешен, дальше три отказа: повторы у того же провайдера, но не переход.
	reserve := &fakeClient{}
	router := reviewFixRouter(t, primary, reserve)
	primary.errors = []error{nil, NewStatusError(503, ""), NewStatusError(503, ""), NewStatusError(503, "")}

	chat, err := router.NewStageChatFactory("review", "fix").NewChat(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Generate(context.Background(), "review prompt"); err != nil {
		t.Fatal(err)
	}
	_, err = chat.Generate(context.Background(), "fix prompt")
	if err == nil {
		t.Fatal("fix завершился успешно, хотя привязанный провайдер отвечал 503")
	}
	if reserve.calls != 0 {
		t.Fatalf("reserve.calls = %d, want 0: привязанный чат не должен уходить к другому провайдеру", reserve.calls)
	}
	if primary.calls != 4 {
		t.Fatalf("primary.calls = %d, want 4 (review + три попытки fix)", primary.calls)
	}
}

func TestChatHistoryReachesBoundProvider(t *testing.T) {
	primary := &fakeClient{errors: []error{NewStatusError(403, "")}}
	reserve := &fakeClient{responses: []Response{{Text: "результат ревью"}}}
	router := reviewFixRouter(t, primary, reserve)

	chat, err := router.NewStageChatFactory("review", "fix").NewChat(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Generate(context.Background(), "review prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Generate(context.Background(), "fix prompt"); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"review prompt", "результат ревью", "fix prompt"} {
		if !strings.Contains(reserve.request.Prompt, expected) {
			t.Fatalf("транскрипт не содержит %q: %q", expected, reserve.request.Prompt)
		}
	}
}

func TestBrowserProviderUsesLongerRetryDelays(t *testing.T) {
	router := reviewFixRouter(t, &fakeClient{}, &fakeClient{})
	want := []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second}
	for attempt, expected := range want {
		if got := router.retryDelay("reserve", attempt+1); got != expected {
			t.Fatalf("retryDelay(deepseek_web, %d) = %v, want %v", attempt+1, got, expected)
		}
	}
	if got := router.retryDelay("reserve", 9); got != 30*time.Second {
		t.Fatalf("retryDelay за пределами таблицы = %v, want 30s", got)
	}
}

func TestNonBrowserProviderKeepsShortExponentialBackoff(t *testing.T) {
	router := reviewFixRouter(t, &fakeClient{}, &fakeClient{})
	for attempt, expected := range []time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond} {
		if got := router.retryDelay("primary", attempt+1); got != expected {
			t.Fatalf("retryDelay(gemini, %d) = %v, want %v", attempt+1, got, expected)
		}
	}
	if got := router.retryDelay("unknown", 1); got != 200*time.Millisecond {
		t.Fatalf("retryDelay для незарегистрированного провайдера = %v, want 200ms", got)
	}
}

// singleChatRouter повторяет DeepSeek-only режим: один провайдер, который сам держит контекст.
func singleChatRouter(t *testing.T, client *fakeClient) *Router {
	t.Helper()
	temperature := 0.3
	stage := config.LLMStageConfig{
		PromptTemplate: "{{.Text}}", Temperature: &temperature, MaxTokens: 100, Timeout: 5 * time.Second,
		Targets: []config.LLMTargetConfig{{Provider: "deepseek", Model: "deepseek-web"}},
	}
	stages := map[string]config.LLMStageConfig{"review": stage, "fix": stage}
	return NewRouter(config.LLMConfig{
		Providers: map[string]config.LLMProviderConfig{
			"deepseek": {Type: "deepseek_web", SingleChatPerArticle: true},
		},
		Stages: stages,
	}, map[string]Client{"deepseek": client}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestSingleChatProviderReceivesOnlyNewPrompt(t *testing.T) {
	client := &fakeClient{responses: []Response{{Text: "результат ревью"}}}
	router := singleChatRouter(t, client)

	chat, err := router.NewStageChatFactory("review", "fix").NewChat(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Generate(context.Background(), "review prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Generate(context.Background(), "fix prompt"); err != nil {
		t.Fatal(err)
	}
	// Контекст держит сам провайдер, поэтому склеенный транскрипт отправлять нельзя.
	if client.request.Prompt != "fix prompt" {
		t.Fatalf("prompt = %q, want ровно \"fix prompt\" без истории", client.request.Prompt)
	}
	for _, unwanted := range []string{"review prompt", "результат ревью", "assistant:"} {
		if strings.Contains(client.request.Prompt, unwanted) {
			t.Fatalf("история попала в промпт повторно: %q", client.request.Prompt)
		}
	}
}

func TestStatelessProviderStillReceivesTranscript(t *testing.T) {
	client := &fakeClient{responses: []Response{{Text: "результат ревью"}}}
	router := singleChatRouter(t, client)
	// Тот же чат, но провайдер контекст не держит.
	providers := router.config.Providers["deepseek"]
	providers.SingleChatPerArticle = false
	router.config.Providers["deepseek"] = providers

	chat, err := router.NewStageChatFactory("review", "fix").NewChat(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"review prompt", "fix prompt"} {
		if _, err := chat.Generate(context.Background(), prompt); err != nil {
			t.Fatal(err)
		}
	}
	for _, expected := range []string{"review prompt", "результат ревью", "fix prompt"} {
		if !strings.Contains(client.request.Prompt, expected) {
			t.Fatalf("транскрипт не содержит %q: %q", expected, client.request.Prompt)
		}
	}
}

func TestArticleIDReachesProvider(t *testing.T) {
	client := &fakeClient{}
	router := singleChatRouter(t, client)
	if _, err := router.generatePrompt(context.Background(), Call{Stage: "review", ArticleID: 99}, "prompt"); err != nil {
		t.Fatal(err)
	}
	if client.request.ArticleID != 99 {
		t.Fatalf("request.ArticleID = %d, want 99: провайдер не сможет держать чат на статью", client.request.ArticleID)
	}
}

// pacedClient повторяет провайдера с самоограничением: он ждёт перед запросом.
type pacedClient struct {
	fakeClient
	pause          time.Duration
	paceCalls      int
	pacedUnderTerm bool
}

func (c *pacedClient) WaitBeforeRequest(ctx context.Context) error {
	c.paceCalls++
	if _, found := ctx.Deadline(); found {
		c.pacedUnderTerm = true
	}
	timer := time.NewTimer(c.pause)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func TestProviderPauseDoesNotConsumeStageTimeout(t *testing.T) {
	client := &pacedClient{pause: 80 * time.Millisecond}
	temperature := 0.3
	router := NewRouter(config.LLMConfig{
		Providers: map[string]config.LLMProviderConfig{"deepseek": {Type: "deepseek_web"}},
		Stages: map[string]config.LLMStageConfig{"structure": {
			PromptTemplate: "{{.Text}}", Temperature: &temperature, MaxTokens: 100,
			Timeout: 100 * time.Millisecond,
			Targets: []config.LLMTargetConfig{{Provider: "deepseek", Model: "deepseek-web"}},
		}},
	}, map[string]Client{"deepseek": client}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	started := time.Now()
	if _, err := router.generatePrompt(context.Background(), Call{Stage: "structure", ArticleID: 1}, "prompt"); err != nil {
		t.Fatalf("пауза съела бюджет стадии: %v", err)
	}
	// Пауза 80ms при бюджете стадии 100ms: если бы она вычиталась из бюджета, на генерацию
	// осталось бы 20ms и запрос не успел бы. Общее время обязано превысить сам бюджет.
	if elapsed := time.Since(started); elapsed < 80*time.Millisecond {
		t.Fatalf("пауза не выдержана: %v", elapsed)
	}
	if client.paceCalls != 1 {
		t.Fatalf("WaitBeforeRequest вызван %d раз, want 1", client.paceCalls)
	}
	if client.pacedUnderTerm {
		t.Fatal("пауза выдерживалась под дедлайном стадии")
	}
	if len(client.deadlines) != 1 {
		t.Fatalf("Generate вызван %d раз, want 1", len(client.deadlines))
	}
	if remaining := time.Until(client.deadlines[0]); remaining <= 0 {
		t.Fatalf("на генерацию не осталось бюджета: %v", remaining)
	}
}
