package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1"
)

// wantGeminiStages — распределение стадий, когда Gemini доступен. Зафиксировано тестом,
// потому что это и есть поведение, которое переработка маршрутизации обязана сохранить.
var wantGeminiStages = map[string]string{
	"structure": "deepseek_web",
	"article":   "gemini",
	"info":      "deepseek_web",
	"review":    "gemini",
	"fix":       "gemini",
	"html":      "deepseek_web",
}

func testResolver(t *testing.T, clock time.Time, requireCredentials bool) llmResolver {
	t.Helper()
	configs, err := loadStageConfigs(mustProfile(task1.Command), modeTestLogger(), requireCredentials)
	if err != nil {
		t.Fatal(err)
	}
	return llmResolver{
		configs:      configs,
		availability: geminiAvailability{path: filepath.Join(t.TempDir(), "gemini-unavailable"), now: func() time.Time { return clock }},
		now:          func() time.Time { return clock },
	}
}

func TestResolveGivesGeminiSchemeWithItsStageDistribution(t *testing.T) {
	chdirProjectRoot(t)
	t.Setenv("GEMINI_API_KEY", "test-gemini")
	t.Setenv("GEMINI_MODEL", "gemini-2.5-flash")
	t.Setenv("LLM_MODE", "")

	routing := testResolver(t, time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC), true).Resolve()
	if routing.Scheme != schemeGemini {
		t.Fatalf("схема = %q (%s)", routing.Scheme, routing.Reason)
	}
	for stage, wantProvider := range wantGeminiStages {
		targets := routing.Config.Stages[stage].Targets
		if len(targets) == 0 {
			t.Fatalf("стадия %q без targets", stage)
		}
		if targets[0].Provider != wantProvider {
			t.Fatalf("стадия %q через %q, ожидался %q", stage, targets[0].Provider, wantProvider)
		}
	}
	if routing.Config.Providers["deepseek_web"].SingleChatPerArticle {
		t.Fatal("схема Gemini не должна держать один чат на статью")
	}
}

func TestResolveFallsBackToDeepSeekWhileGeminiIsDisabled(t *testing.T) {
	chdirProjectRoot(t)
	t.Setenv("GEMINI_API_KEY", "test-gemini")
	t.Setenv("GEMINI_MODEL", "gemini-2.5-flash")
	t.Setenv("LLM_MODE", "")

	clock := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	resolver := testResolver(t, clock, true)
	if _, err := resolver.availability.disable("quota_exhausted"); err != nil {
		t.Fatal(err)
	}

	routing := resolver.Resolve()
	if routing.Scheme != schemeDeepSeek {
		t.Fatalf("схема = %q (%s)", routing.Scheme, routing.Reason)
	}
	if !strings.Contains(routing.Reason, "quota_exhausted") {
		t.Fatalf("причина не названа: %q", routing.Reason)
	}
	for _, stage := range pipelineStageOrder {
		if got := routing.Config.Stages[stage].Targets[0].Provider; got != "deepseek_web" {
			t.Fatalf("DeepSeek-only: стадия %q через %q", stage, got)
		}
	}
	if !routing.Config.Providers["deepseek_web"].SingleChatPerArticle {
		t.Fatal("DeepSeek-only обязан держать один чат на статью")
	}
	if status, found := findProviderStatus(routing.Providers, "gemini"); !found || status.Available {
		t.Fatalf("gemini отмечен доступным: %+v", status)
	}
}

// Схема Gemini разбирается, но без ключа режим всё равно DeepSeek-only: проверка перед дорогим
// прогоном обязана назвать причину, а не молча считать Gemini рабочим.
func TestResolveReportsMissingGeminiCredentialsWithoutFailing(t *testing.T) {
	chdirProjectRoot(t)
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GEMINI_MODEL", "gemini-2.5-flash")
	t.Setenv("LLM_MODE", "")

	routing := testResolver(t, time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC), false).Resolve()
	if routing.Scheme != schemeDeepSeek {
		t.Fatalf("схема = %q (%s)", routing.Scheme, routing.Reason)
	}
	status, found := findProviderStatus(routing.Providers, "gemini")
	if !found || status.Available || !strings.Contains(status.Detail, "GEMINI_API_KEY") {
		t.Fatalf("причина недоступности Gemini не названа: %+v", status)
	}
}

// Пустой GEMINI_MODEL больше не роняет загрузку без учётных данных: раньше dry-run падал на
// «has empty model», так и не дойдя до вывода о том, что Gemini в этом прогоне не нужен.
func TestResolveSurvivesEmptyGeminiModel(t *testing.T) {
	chdirProjectRoot(t)
	t.Setenv("GEMINI_API_KEY", "test-gemini")
	t.Setenv("GEMINI_MODEL", "")
	t.Setenv("LLM_MODE", "")

	routing := testResolver(t, time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC), false).Resolve()
	if routing.Scheme != schemeDeepSeek {
		t.Fatalf("схема = %q (%s)", routing.Scheme, routing.Reason)
	}
	status, _ := findProviderStatus(routing.Providers, "gemini")
	if status.Available || !strings.Contains(status.Detail, "модель") {
		t.Fatalf("пустая модель не объяснена: %+v", status)
	}
}

func TestRoutingReportShowsModeProvidersAndStages(t *testing.T) {
	chdirProjectRoot(t)
	t.Setenv("GEMINI_API_KEY", "test-gemini")
	t.Setenv("GEMINI_MODEL", "gemini-2.5-flash")
	t.Setenv("LLM_MODE", "")

	routing := testResolver(t, time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC), true).Resolve()
	var report bytes.Buffer
	if err := writeRoutingReport(&report, routing); err != nil {
		t.Fatal(err)
	}
	output := report.String()
	for _, expected := range []string{"Режим: gemini", "Провайдеры:", "Стадии:", "gemini-2.5-flash", "deepseek_web"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("в отчёте нет %q:\n%s", expected, output)
		}
	}
	for _, stage := range pipelineStageOrder {
		if !strings.Contains(output, stage) {
			t.Fatalf("в отчёте нет стадии %q:\n%s", stage, output)
		}
	}
}

// Ключевая проверка C1: подменяется маршрут, который роутер действительно читает.
// Пока dry-run писал в stage.Model, заглушка получала настоящее имя модели и отвечала отказом.
func TestDryRunStageSetRewritesTargetsAndReachesTheStub(t *testing.T) {
	chdirProjectRoot(t)
	t.Setenv("GEMINI_API_KEY", "test-gemini")
	t.Setenv("GEMINI_MODEL", "gemini-2.5-flash")
	t.Setenv("LLM_MODE", "")

	routing := testResolver(t, time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC), true).Resolve()
	offline := dryRunStageSet(routing.Config)

	for stage, wantProvider := range wantGeminiStages {
		targets := offline.Stages[stage].Targets
		if targets[0].Provider != wantProvider {
			t.Fatalf("стадия %q сменила провайдера на %q", stage, targets[0].Provider)
		}
		if targets[0].Model != dryRunModelPrefix+stage {
			t.Fatalf("стадия %q осталась на модели %q", stage, targets[0].Model)
		}
	}
	// Исходная схема не испорчена: её же печатает отчёт.
	if routing.Config.Stages["article"].Targets[0].Model != "gemini-2.5-flash" {
		t.Fatal("подмена моделей затронула разрешённую схему")
	}

	clients := make(map[string]llm.Client, len(offline.Providers))
	for name := range offline.Providers {
		clients[name] = dryRunClient{}
	}
	router := llm.NewRouter(offline, clients, modeTestLogger())
	// Стадии без чата: article и info собираются обычным запросом, review и fix — диалогом.
	for _, stage := range []string{"structure", "html"} {
		// Данные — map: у стадий разные наборы полей, а недостающий ключ map даёт
		// «<no value>» вместо ошибки шаблона. Проверяется маршрут, а не рендеринг промпта.
		response, err := router.Generate(context.Background(), llm.Call{
			Stage: stage, ArticleID: 1, Data: map[string]any{},
		})
		if err != nil {
			t.Fatalf("стадия %q не дошла до локальной заглушки: %v", stage, err)
		}
		if response.Model != dryRunModelPrefix+stage {
			t.Fatalf("стадия %q ответила моделью %q", stage, response.Model)
		}
	}
}
