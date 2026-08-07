package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/llm"
	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/generation"
)

func modeTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func chdirProjectRoot(t *testing.T) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
}

func TestBothSchemesLoadWhenGeminiIsConfigured(t *testing.T) {
	chdirProjectRoot(t)
	t.Setenv("GEMINI_API_KEY", "test-gemini")
	t.Setenv("GEMINI_MODEL", "gemini-2.5-flash")
	t.Setenv("LLM_MODE", "")

	configs, err := loadStageConfigs(modeTestLogger(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !configs.geminiFound {
		t.Fatal("схема Gemini не загружена")
	}
	if got := configs.gemini.Stages["article"].Targets[0].Provider; got != "gemini" {
		t.Fatalf("схема Gemini: article через %q", got)
	}
	for _, stage := range []string{"structure", "article", "info", "review", "fix", "html"} {
		if got := configs.deepseek.Stages[stage].Targets[0].Provider; got != "deepseek_web" {
			t.Fatalf("DeepSeek-only: стадия %q через %q", stage, got)
		}
	}
	if !configs.deepseek.Providers["deepseek_web"].SingleChatPerArticle {
		t.Fatal("DeepSeek-only не включил один чат на статью")
	}
	if configs.gemini.Providers["deepseek_web"].SingleChatPerArticle {
		t.Fatal("схема Gemini не должна включать один чат на статью")
	}
}

// Без ключа Gemini работа не останавливается: DeepSeek-only остаётся рабочим режимом.
func TestMissingGeminiKeyLeavesDeepSeekOnly(t *testing.T) {
	chdirProjectRoot(t)
	t.Setenv("GEMINI_MODEL", "gemini-2.5-flash")
	t.Setenv("LLM_MODE", "")
	if err := os.Unsetenv("GEMINI_API_KEY"); err != nil {
		t.Fatal(err)
	}
	configs, err := loadStageConfigs(modeTestLogger(), true)
	if err != nil {
		t.Fatalf("отсутствие ключа Gemini остановило запуск: %v", err)
	}
	if configs.geminiFound {
		t.Fatal("схема Gemini загружена без ключа")
	}
	if len(configs.deepseek.Stages) == 0 {
		t.Fatal("DeepSeek-only схема не загружена")
	}
}

func TestPinnedDeepSeekModeSkipsGemini(t *testing.T) {
	chdirProjectRoot(t)
	t.Setenv("GEMINI_API_KEY", "test-gemini")
	t.Setenv("GEMINI_MODEL", "gemini-2.5-flash")
	t.Setenv("LLM_MODE", "deepseek")
	configs, err := loadStageConfigs(modeTestLogger(), true)
	if err != nil {
		t.Fatal(err)
	}
	if configs.geminiFound {
		t.Fatal("LLM_MODE=deepseek не должен готовить схему Gemini")
	}
}

func TestUnknownLLMModeIsRejected(t *testing.T) {
	chdirProjectRoot(t)
	t.Setenv("LLM_MODE", "openrouter")
	if _, err := loadStageConfigs(modeTestLogger(), true); err == nil || !strings.Contains(err.Error(), "LLM_MODE") {
		t.Fatalf("error = %v, want отказ по неизвестному режиму", err)
	}
}

// testMode собирает переключатель с управляемыми часами и заглушками конвейеров: настоящие
// конвейеры для проверки выбора режима не нужны, важны только их адреса.
func testMode(t *testing.T, clock *time.Time, logs *bytes.Buffer) *articleMode {
	t.Helper()
	logger := modeTestLogger()
	if logs != nil {
		logger = slog.New(slog.NewTextHandler(logs, nil))
	}
	now := func() time.Time { return *clock }
	availability := geminiAvailability{path: filepath.Join(t.TempDir(), "gemini-unavailable"), now: now}
	return &articleMode{
		pipelines: map[schemeName]*generation.Pipeline{
			schemeGemini:   {},
			schemeDeepSeek: {},
		},
		resolver:     llmResolver{configs: stageConfigs{geminiFound: true}, availability: availability, now: now},
		availability: availability,
		logger:       logger,
	}
}

func TestGeminiSchemeIsChosenWhileAvailable(t *testing.T) {
	clock := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	mode := testMode(t, &clock, nil)
	scheme, pipeline := mode.pipelineFor("47")
	if scheme != schemeGemini {
		t.Fatalf("выбрана схема %q, хотя Gemini доступен", scheme)
	}
	if pipeline != mode.pipelines[schemeGemini] {
		t.Fatal("имя схемы и конвейер разошлись")
	}
}

func TestQuotaDisablesGeminiForADayAndRestartsArticle(t *testing.T) {
	clock := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	mode := testMode(t, &clock, &logs)

	restarted := 0
	mode.restart = func(_ context.Context, pipeline *generation.Pipeline, externalID string) error {
		if pipeline != mode.pipelines[schemeDeepSeek] || externalID != "47" {
			t.Fatalf("перезапуск пошёл не той схемой или не по той статье: %q", externalID)
		}
		restarted++
		return nil
	}
	if err := mode.guard(context.Background(), "47", schemeGemini, llm.NewStatusError(429, "quota exceeded")); err != nil {
		t.Fatal(err)
	}
	if restarted != 1 {
		t.Fatalf("статья перезапущена %d раз, want 1", restarted)
	}
	until, reason, blocked, _ := mode.availability.state()
	if !blocked {
		t.Fatal("Gemini не выключен после исчерпания квоты")
	}
	if want := clock.Add(24 * time.Hour); !until.Equal(want) {
		t.Fatalf("Gemini выключен до %s, ожидалось %s", until, want)
	}
	if reason != string(llm.ErrorTypeQuotaExhausted) {
		t.Fatalf("причина = %q", reason)
	}
	// Следующая статья обязана сразу идти через DeepSeek-only.
	if scheme, _ := mode.pipelineFor("48"); scheme != schemeDeepSeek {
		t.Fatalf("следующая статья ушла в %q", scheme)
	}
	output := logs.String()
	for _, expected := range []string{"Gemini is disabled", "gemini_disabled_until", "deepseek_only"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("в логе нет %q: %s", expected, output)
		}
	}
}

func TestCreditsExhaustedAlsoDisablesGemini(t *testing.T) {
	clock := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	mode := testMode(t, &clock, nil)
	mode.restart = func(context.Context, *generation.Pipeline, string) error { return nil }
	_ = mode.guard(context.Background(), "47", schemeGemini, llm.NewStatusError(402, ""))
	if _, _, blocked, _ := mode.availability.state(); !blocked {
		t.Fatal("исчерпание оплаты не выключило Gemini")
	}
}

// Прочие ошибки Gemini не выключают: провайдер остаётся доступным, статья просто падает.
func TestOtherErrorsKeepGeminiAvailable(t *testing.T) {
	clock := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	for _, failure := range []error{
		llm.NewStatusError(503, "service unavailable"),
		llm.NewStatusError(401, ""),
		errors.New("HTML не содержит заголовка или абзаца"),
	} {
		mode := testMode(t, &clock, nil)
		if got := mode.guard(context.Background(), "47", schemeGemini, failure); !errors.Is(got, failure) {
			t.Fatalf("ошибка подменена: %v", got)
		}
		if _, _, blocked, _ := mode.availability.state(); blocked {
			t.Fatalf("Gemini выключен из-за %v", failure)
		}
	}
}

// Статья, шедшая DeepSeek-only, не должна выключать Gemini: исчерпание квоты пришло не от него.
func TestDeepSeekSchemeDoesNotDisableGemini(t *testing.T) {
	clock := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	mode := testMode(t, &clock, nil)
	failure := llm.NewStatusError(429, "quota exceeded")
	if got := mode.guard(context.Background(), "47", schemeDeepSeek, failure); !errors.Is(got, failure) {
		t.Fatalf("ошибка подменена: %v", got)
	}
	if _, _, blocked, _ := mode.availability.state(); blocked {
		t.Fatal("Gemini выключен из-за отказа DeepSeek")
	}
}

func TestGeminiReturnsAfterTTL(t *testing.T) {
	clock := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	mode := testMode(t, &clock, nil)
	if _, err := mode.availability.disable("quota_exhausted"); err != nil {
		t.Fatal(err)
	}
	if scheme, _ := mode.pipelineFor("47"); scheme != schemeDeepSeek {
		t.Fatalf("статья пошла через %q, пока Gemini выключен", scheme)
	}

	clock = clock.Add(24*time.Hour + time.Minute)
	if scheme, _ := mode.pipelineFor("48"); scheme != schemeGemini {
		t.Fatalf("Gemini не вернулся после истечения суток, схема %q", scheme)
	}
}

// Истёкший маркер снимается явным шагом, а не выбором схемы: выбор вызывается на каждой статье
// и обязан оставаться чистым.
func TestExpireRemovesOnlyOutdatedMarker(t *testing.T) {
	clock := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "gemini-unavailable")
	state := geminiAvailability{path: path, now: func() time.Time { return clock }}
	if _, err := state.disable("quota_exhausted"); err != nil {
		t.Fatal(err)
	}
	if _, _, removed, err := state.expire(); err != nil || removed {
		t.Fatalf("действующий маркер снят: removed=%v err=%v", removed, err)
	}

	expired := geminiAvailability{path: path, now: func() time.Time { return clock.Add(25 * time.Hour) }}
	until, reason, removed, err := expired.expire()
	if err != nil || !removed {
		t.Fatalf("истёкший маркер не снят: removed=%v err=%v", removed, err)
	}
	if reason != "quota_exhausted" || !until.Equal(clock.Add(24*time.Hour)) {
		t.Fatalf("снятие вернуло %s / %q", until, reason)
	}
	if _, _, _, present := expired.state(); present {
		t.Fatal("файл маркера остался на диске")
	}
}

// Состояние обязано переживать перезапуск: маркер лежит в файле, а не в памяти процесса.
func TestGeminiStateSurvivesRestart(t *testing.T) {
	clock := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "gemini-unavailable")
	first := geminiAvailability{path: path, now: func() time.Time { return clock }}
	if _, err := first.disable("quota_exhausted"); err != nil {
		t.Fatal(err)
	}
	second := geminiAvailability{path: path, now: func() time.Time { return clock.Add(time.Hour) }}
	until, reason, blocked, present := second.state()
	if !blocked || !present {
		t.Fatal("новый процесс не увидел выключение Gemini")
	}
	if reason != "quota_exhausted" || !until.Equal(clock.Add(24*time.Hour)) {
		t.Fatalf("состояние прочитано неверно: %s / %s", until, reason)
	}
}

func TestBrokenGeminiStateDoesNotDisableProviderForever(t *testing.T) {
	clock := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "gemini-unavailable")
	if err := os.WriteFile(path, []byte("не дата\nquota\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := geminiAvailability{path: path, now: func() time.Time { return clock }}
	if _, _, blocked, _ := state.state(); blocked {
		t.Fatal("битый маркер выключил Gemini")
	}
}
