package deepseekweb

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/llm"
)

// testClient собирает клиента с управляемыми часами: настоящая пауза в 30–50 s сделала бы
// тест бессмысленно долгим.
func testClient(t *testing.T, profileDir string, clock *time.Time, randomValue int64, slept *[]time.Duration) *Client {
	t.Helper()
	client, err := NewClient(Config{
		ChatURL: "https://chat.deepseek.com/", LoginURL: "https://chat.deepseek.com/sign_in",
		ProfileDir: profileDir,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	client.pace = pacing{
		minInterval: minRequestInterval,
		jitter:      requestJitter,
		now:         func() time.Time { return *clock },
		random:      func(int64) int64 { return randomValue },
		sleep: func(ctx context.Context, delay time.Duration) error {
			if slept != nil {
				*slept = append(*slept, delay)
			}
			*clock = clock.Add(delay)
			return ctx.Err()
		},
	}
	return client
}

func TestPacingIntervalStaysWithinRange(t *testing.T) {
	for _, random := range []int64{0, int64(requestJitter) / 2, int64(requestJitter) - 1} {
		pace := pacing{minInterval: minRequestInterval, jitter: requestJitter, random: func(int64) int64 { return random }}
		interval := pace.interval()
		if interval < 20*time.Second || interval > 60*time.Second {
			t.Fatalf("interval = %v, want between 20s and 60s", interval)
		}
	}
}

func TestPacingIntervalIsNotConstant(t *testing.T) {
	low := pacing{minInterval: minRequestInterval, jitter: requestJitter, random: func(int64) int64 { return 0 }}
	high := pacing{minInterval: minRequestInterval, jitter: requestJitter, random: func(int64) int64 { return int64(requestJitter) - 1 }}
	if low.interval() == high.interval() {
		t.Fatal("интервал не зависит от случайной добавки")
	}
	if low.interval() != minRequestInterval {
		t.Fatalf("нижняя граница = %v, want %v", low.interval(), minRequestInterval)
	}
}

func TestFirstRequestIsNotThrottled(t *testing.T) {
	clock := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	var slept []time.Duration
	client := testClient(t, t.TempDir(), &clock, 0, &slept)
	if err := client.WaitBeforeRequest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 0 {
		t.Fatalf("первый запрос ждал %v", slept)
	}
}

func TestSecondRequestWaitsRemainingInterval(t *testing.T) {
	clock := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	var slept []time.Duration
	// random = 20s ⇒ целевой интервал 40s.
	client := testClient(t, t.TempDir(), &clock, int64(20*time.Second), &slept)
	client.markRequestFinished()

	clock = clock.Add(15 * time.Second)
	if err := client.WaitBeforeRequest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 1 || slept[0] != 25*time.Second {
		t.Fatalf("ожидание = %v, want [25s]", slept)
	}
}

func TestRequestIsNotThrottledAfterLongPause(t *testing.T) {
	clock := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	var slept []time.Duration
	client := testClient(t, t.TempDir(), &clock, 0, &slept)
	client.markRequestFinished()

	clock = clock.Add(2 * time.Minute)
	if err := client.WaitBeforeRequest(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 0 {
		t.Fatalf("после длинной паузы клиент ждал ещё %v", slept)
	}
}

func TestThrottleStopsOnCancelledContext(t *testing.T) {
	clock := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	client := testClient(t, t.TempDir(), &clock, 0, nil)
	client.markRequestFinished()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.WaitBeforeRequest(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestThrottleUsesRealSleepByDefault(t *testing.T) {
	pace := defaultPacing()
	if pace.minInterval != 20*time.Second || pace.jitter != 40*time.Second {
		t.Fatalf("pacing = %v/%v, want 20s/40s", pace.minInterval, pace.jitter)
	}
	started := time.Now()
	if err := pace.sleep(context.Background(), 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) < 20*time.Millisecond {
		t.Fatal("sleep вернулся раньше срока")
	}
}

func TestAccountUnavailableErrorIsTerminal(t *testing.T) {
	err := accountUnavailableError("account_blocked")
	var statusErr *llm.StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error = %#v", err)
	}
	if statusErr.Code != 403 || statusErr.Type != llm.ErrorTypeUnauthorized {
		t.Fatalf("code = %d, type = %s", statusErr.Code, statusErr.Type)
	}
	if !strings.Contains(err.Error(), "deepseek_account_unavailable") {
		t.Fatalf("message = %q", err.Error())
	}
}

func TestBlockAccountWritesCooldownAndKeepsSession(t *testing.T) {
	profileDir := t.TempDir()
	clock := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	client := testClient(t, profileDir, &clock, 0, nil)
	// Непустая сессия: blockAccount не должен её закрывать.
	client.session = &browserSession{}

	err := client.blockAccount("account_blocked")
	if !strings.Contains(err.Error(), "deepseek_account_unavailable: account_blocked") {
		t.Fatalf("error = %q", err.Error())
	}
	if client.session == nil {
		t.Fatal("blockAccount сбросил сессию, хотя перезапуск браузера здесь запрещён")
	}
	until, reason, blocked := readBlockedUntil(profileDir, clock)
	if !blocked {
		t.Fatal("cooldown не сохранён")
	}
	if reason != "account_blocked" {
		t.Fatalf("reason = %q", reason)
	}
	if want := clock.Add(blockCooldown); !until.Equal(want) {
		t.Fatalf("blocked_until = %s, want %s", until, want)
	}
}

func TestCooldownExpiresAfterDeadline(t *testing.T) {
	profileDir := t.TempDir()
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	if err := writeBlockedUntil(profileDir, now.Add(time.Hour), "challenge_or_captcha"); err != nil {
		t.Fatal(err)
	}
	if _, _, blocked := readBlockedUntil(profileDir, now.Add(59*time.Minute)); !blocked {
		t.Fatal("cooldown снят раньше времени")
	}
	if _, _, blocked := readBlockedUntil(profileDir, now.Add(time.Hour)); blocked {
		t.Fatal("cooldown не снят в момент истечения")
	}
	if _, _, blocked := readBlockedUntil(profileDir, now.Add(2*time.Hour)); blocked {
		t.Fatal("cooldown не снят после истечения")
	}
}

func TestCooldownIsAbsentWithoutMarkerOrOnBrokenMarker(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	empty := t.TempDir()
	if _, _, blocked := readBlockedUntil(empty, now); blocked {
		t.Fatal("cooldown найден без файла-маркера")
	}
	broken := t.TempDir()
	if err := writeBlockedUntil(broken, now.Add(time.Hour), "reason"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blockedStatePath(broken), []byte("не дата\nreason\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, blocked := readBlockedUntil(broken, now); blocked {
		t.Fatal("битый маркер выключил провайдера")
	}
}

func TestGenerateRejectedDuringCooldownWithoutBrowser(t *testing.T) {
	profileDir := t.TempDir()
	clock := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	client := testClient(t, profileDir, &clock, 0, nil)
	if err := writeBlockedUntil(profileDir, clock.Add(30*time.Minute), "account_blocked"); err != nil {
		t.Fatal(err)
	}

	_, err := client.Generate(context.Background(), llm.Request{Prompt: "любой промпт"})
	if err == nil || !strings.Contains(err.Error(), "deepseek_account_unavailable") {
		t.Fatalf("error = %v, want deepseek_account_unavailable", err)
	}
	if client.session != nil {
		t.Fatal("во время cooldown был запущен браузер")
	}
}
